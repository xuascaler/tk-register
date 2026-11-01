package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"goapi-tk-register/server/internal/core"
	"goapi-tk-register/server/internal/supplier"
)

type accountCollector interface {
	Collect(context.Context, string, string) ([]supplier.Account, error)
	TestLogin(context.Context, string, string) error
}

func (a *app) collector() accountCollector {
	if a.accountCollector != nil {
		return a.accountCollector
	}
	return supplier.NewClient()
}

const accountSyncLeaseKey = redisPrefix + "account-sync:lease"

var errSyncBusy = errors.New("a collection is already running")
var errSyncNotConfigured = errors.New("website login is not configured")
var errSyncUnsupported = errors.New("this account is not eligible for collection")

type accountSyncConfig struct {
	Enabled            bool       `json:"enabled"`
	IntervalHours      int        `json:"interval_hours"`
	Timezone           string     `json:"timezone"`
	DataSource         string     `json:"data_source"`
	WebsiteUsername    string     `json:"website_username"`
	PasswordConfigured bool       `json:"password_configured"`
	Running            bool       `json:"running"`
	LastRunAt          *time.Time `json:"last_run_at"`
	LastSuccessAt      *time.Time `json:"last_success_at"`
	LastError          string     `json:"last_error"`
	NextRunAt          *time.Time `json:"next_run_at"`
}

func syncDue(v core.Values, state core.AccountSyncState, now time.Time) bool {
	if !v.Bool("account_sync_enabled") || v.String("account_sync_website_username") == "" || v.String("account_sync_website_password") == "" {
		return false
	}
	return state.LastStartedAt == nil || !now.Before(state.LastStartedAt.Add(time.Duration(v.Int("account_sync_interval_hours"))*time.Hour))
}

func (a *app) syncConfig(ctx context.Context) (accountSyncConfig, error) {
	var out accountSyncConfig
	v, err := a.store.Settings(ctx)
	if err != nil {
		return out, err
	}
	var state core.AccountSyncState
	if err = a.store.DB.WithContext(ctx).First(&state, 1).Error; err != nil {
		return out, err
	}
	running, err := a.redis.Exists(ctx, accountSyncLeaseKey).Result()
	if err != nil {
		return out, err
	}
	out = accountSyncConfig{Enabled: v.Bool("account_sync_enabled"), IntervalHours: v.Int("account_sync_interval_hours"), Timezone: v.String("account_sync_timezone"), DataSource: "website", WebsiteUsername: v.String("account_sync_website_username"), PasswordConfigured: v.String("account_sync_website_password") != "", Running: running > 0, LastRunAt: state.LastStartedAt, LastSuccessAt: state.LastSuccessAt, LastError: state.LastError}
	if state.Status == "running" && running == 0 {
		out.LastError = "The last collection was interrupted. Previously collected data is retained."
	}
	if out.Enabled && out.PasswordConfigured && out.WebsiteUsername != "" {
		next := time.Now().UTC()
		if state.LastStartedAt != nil {
			next = state.LastStartedAt.Add(time.Duration(out.IntervalHours) * time.Hour)
		}
		out.NextRunAt = &next
	}
	return out, nil
}

func (a *app) accountSyncRoutes(w http.ResponseWriter, r *http.Request, route string) bool {
	configRoute := route == "admin/account-sync" || strings.HasPrefix(route, "admin/account-sync/")
	adminAccounts := route == "admin/synced-accounts" || strings.HasPrefix(route, "admin/synced-accounts/")
	if !configRoute && !adminAccounts && route != "accounts" && !strings.HasPrefix(route, "accounts/") {
		return false
	}
	user, _, valid := a.authenticated(w, r, r.Method != "GET")
	if !valid {
		return true
	}
	if configRoute || adminAccounts {
		if user.Role != "admin" {
			fail(w, 403, "FORBIDDEN", "Administrator access is required.")
			return true
		}
		if configRoute {
			a.adminAccountSync(w, r, route, user.ID)
			return true
		}
	}
	base := "accounts"
	lookupAccount := func(id uint64) (core.AccountDTO, error) {
		return a.store.SyncedAccountForUser(r.Context(), user.ID, id)
	}
	if adminAccounts {
		base = "admin/synced-accounts"
		lookupAccount = func(id uint64) (core.AccountDTO, error) {
			return a.store.SyncedAccountForAdmin(r.Context(), id)
		}
	}
	if route == base && r.Method == "GET" {
		page, valid := adminPage(w, r)
		if !valid {
			return true
		}
		var result core.AccountPage
		var err error
		if adminAccounts {
			result, err = a.store.ListAdminSyncedAccounts(r.Context(), page, r.URL.Query().Get("search"), r.URL.Query().Get("status"))
		} else {
			result, err = a.store.ListSyncedAccounts(r.Context(), user.ID, page, r.URL.Query().Get("search"), r.URL.Query().Get("status"))
		}
		if core.IsValidationError(err) {
			fail(w, 400, "INVALID_FILTER", err.Error())
		} else if err != nil {
			vendorFailure(w, err)
		} else {
			ok(w, result)
		}
		return true
	}
	if route == base {
		fail(w, 405, "METHOD_NOT_ALLOWED", "This method is not supported.")
		return true
	}
	parts := strings.Split(strings.TrimPrefix(route, base+"/"), "/")
	if len(parts) < 1 || len(parts) > 2 {
		fail(w, 404, "NOT_FOUND", "This endpoint does not exist.")
		return true
	}
	id, err := core.ParseBusinessID(parts[0])
	if err != nil {
		vendorFailure(w, err)
		return true
	}
	account, err := lookupAccount(id)
	if err != nil {
		vendorFailure(w, err)
		return true
	}
	if len(parts) == 2 && parts[1] == "auth-json" {
		a.accountAuthJSON(w, r, user, id, adminAccounts, lookupAccount)
		return true
	}
	if len(parts) == 1 && r.Method == "GET" {
		days := 7
		if raw := r.URL.Query().Get("days"); raw != "" {
			days, err = strconv.Atoi(raw)
		}
		if err != nil || (days != 7 && days != 30 && days != 90) {
			fail(w, 400, "INVALID_RANGE", "Choose 7, 30, or 90 days.")
			return true
		}
		snapshots, err := a.store.AccountSnapshots(r.Context(), id, days)
		if err != nil {
			internal(w)
			return true
		}
		v, err := a.store.Settings(r.Context())
		if err != nil {
			internal(w)
			return true
		}
		ok(w, map[string]any{"account": account, "snapshots": snapshots, "interval_hours": v.Int("account_sync_interval_hours"), "timezone": v.String("account_sync_timezone")})
		return true
	}
	if len(parts) == 2 && parts[1] == "sync" && r.Method == "POST" {
		if account.CurrentSubmissionID == nil {
			fail(w, 409, "ACCOUNT_NOT_ACCEPTED", "Only an accepted account can be synchronized.")
			return true
		}
		allowed, e := a.redis.SetNX(r.Context(), redisPrefix+"account-sync:manual:"+strconv.FormatUint(user.ID, 10)+":"+strconv.FormatUint(id, 10), "1", time.Minute).Result()
		if e != nil {
			internal(w)
			return true
		}
		if !allowed {
			rateFailure(w, 60)
			return true
		}
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		collectionUserID := user.ID
		if adminAccounts {
			// The account ID has already passed administrator authorization. Keep
			// the acting administrator's rate limit without filtering by its owner.
			collectionUserID = 0
		}
		err = a.collectAccounts(ctx, collectionUserID, id, false)
		if errors.Is(err, errSyncBusy) {
			fail(w, 409, "SYNC_RUNNING", "A collection is already running. Please try again shortly.")
			return true
		}
		if errors.Is(err, errSyncUnsupported) {
			fail(w, 409, "SYNC_UNAVAILABLE", "Website sync is unavailable for this account or vendor.")
			return true
		}
		if errors.Is(err, errSyncNotConfigured) {
			fail(w, 503, "SYNC_NOT_CONFIGURED", "Website login has not been configured by an administrator.")
			return true
		}
		if err != nil {
			internal(w)
			return true
		}
		account, err = lookupAccount(id)
		if err != nil {
			internal(w)
		} else {
			ok(w, map[string]any{"account": account})
		}
		return true
	}
	fail(w, 405, "METHOD_NOT_ALLOWED", "This method is not supported.")
	return true
}

func (a *app) adminAccountSync(w http.ResponseWriter, r *http.Request, route string, actorID uint64) {
	if route == "admin/account-sync" && r.Method == "GET" {
		out, err := a.syncConfig(r.Context())
		if err != nil {
			internal(w)
		} else {
			ok(w, out)
		}
		return
	}
	if route == "admin/account-sync" && r.Method == "PUT" {
		var in struct {
			Enabled         *bool   `json:"enabled"`
			IntervalHours   *int    `json:"interval_hours"`
			Timezone        *string `json:"timezone"`
			WebsiteUsername *string `json:"website_username"`
			WebsitePassword *string `json:"website_password"`
		}
		if !decode(w, r, &in) {
			return
		}
		changes := map[string]string{}
		if in.Enabled != nil {
			changes["account_sync_enabled"] = strconv.FormatBool(*in.Enabled)
		}
		if in.IntervalHours != nil {
			changes["account_sync_interval_hours"] = strconv.Itoa(*in.IntervalHours)
		}
		if in.Timezone != nil {
			changes["account_sync_timezone"] = *in.Timezone
		}
		if in.WebsiteUsername != nil {
			changes["account_sync_website_username"] = strings.TrimSpace(*in.WebsiteUsername)
		}
		if in.WebsitePassword != nil {
			changes["account_sync_website_password"] = *in.WebsitePassword
		}
		if err := a.store.PatchSettings(r.Context(), changes, actorID); err != nil {
			if core.IsValidationError(err) {
				fail(w, 400, "INVALID_SETTINGS", err.Error())
			} else {
				internal(w)
			}
			return
		}
		out, err := a.syncConfig(r.Context())
		if err != nil {
			internal(w)
		} else {
			ok(w, out)
		}
		return
	}
	if route == "admin/account-sync/test-login" && r.Method == "POST" {
		var in struct {
			WebsiteUsername string `json:"website_username"`
			WebsitePassword string `json:"website_password"`
		}
		if !decode(w, r, &in) {
			return
		}
		in.WebsiteUsername = strings.TrimSpace(in.WebsiteUsername)
		if len(in.WebsiteUsername) == 0 || len(in.WebsiteUsername) > 254 || len(in.WebsitePassword) > 4096 || strings.ContainsAny(in.WebsiteUsername, "\r\n\x00") {
			fail(w, 400, "INVALID_LOGIN", "Enter a website username and password.")
			return
		}
		v, err := a.store.Settings(r.Context())
		if err != nil {
			internal(w)
			return
		}
		if in.WebsitePassword == "" {
			if in.WebsiteUsername != v.String("account_sync_website_username") {
				fail(w, 400, "PASSWORD_REQUIRED", "Enter the password for the new username.")
				return
			}
			in.WebsitePassword = v.String("account_sync_website_password")
		}
		if in.WebsitePassword == "" {
			fail(w, 400, "PASSWORD_REQUIRED", "Enter a website password.")
			return
		}
		allowed, err := a.redis.SetNX(r.Context(), redisPrefix+"account-sync:test:"+strconv.FormatUint(actorID, 10), "1", 5*time.Second).Result()
		if err != nil {
			internal(w)
			return
		}
		if !allowed {
			rateFailure(w, 5)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err = a.collector().TestLogin(ctx, in.WebsiteUsername, in.WebsitePassword); err != nil {
			fail(w, 502, "WEBSITE_LOGIN_FAILED", publicCollectionError(err))
			return
		}
		ok(w, map[string]bool{"authenticated": true})
		return
	}
	fail(w, 405, "METHOD_NOT_ALLOWED", "This method is not supported.")
}

func publicCollectionError(err error) string {
	var upstream *supplier.Error
	if errors.As(err, &upstream) {
		return upstream.Error()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "The supplier website timed out. Previously collected data is retained."
	}
	return "The supplier website could not be queried. Previously collected data is retained."
}

func matchSupplierAccount(target core.SyncedAccount, records []supplier.Account) (*supplier.Account, error) {
	var matches []int
	for i := range records {
		if strings.EqualFold(strings.TrimSpace(records[i].Account), strings.TrimSpace(target.Account)) && records[i].Plan == target.Plan {
			matches = append(matches, i)
		}
	}
	if len(matches) == 0 {
		return nil, errors.New("This account was not found in the supplier website. Previously collected data is retained.")
	}
	if target.PlatformAccountID != nil && *target.PlatformAccountID != "" {
		var exact []int
		for _, i := range matches {
			if records[i].PlatformAccountID == *target.PlatformAccountID {
				exact = append(exact, i)
			}
		}
		if len(exact) == 1 {
			return &records[exact[0]], nil
		}
		return nil, errors.New("The platform account ID no longer matches the supplier record. Previously collected data is retained.")
	}
	if target.RemoteID != nil {
		// Historical imports sometimes used the visible platform ID as RemoteID.
		var exact []int
		for _, i := range matches {
			if records[i].ID == *target.RemoteID || records[i].PlatformAccountID == *target.RemoteID {
				exact = append(exact, i)
			}
		}
		if len(exact) == 1 {
			return &records[exact[0]], nil
		}
		return nil, errors.New("The account ID no longer matches the supplier record. Previously collected data is retained.")
	}
	if len(matches) == 1 {
		return &records[matches[0]], nil
	}
	return nil, errors.New("More than one supplier record matches this account. An administrator must check the account identity.")
}

func (a *app) collectAccounts(parent context.Context, userID, accountID uint64, scheduled bool) error {
	ctx, cancel := context.WithTimeout(parent, 5*time.Minute)
	defer cancel()
	token, err := randomToken()
	if err != nil {
		return err
	}
	locked, err := a.redis.SetNX(ctx, accountSyncLeaseKey, token, 7*time.Minute).Result()
	if err != nil {
		return err
	}
	if !locked {
		return errSyncBusy
	}
	defer func() {
		release, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = releaseSend.Run(release, a.redis, []string{accountSyncLeaseKey}, token).Err()
	}()
	v, err := a.store.Settings(ctx)
	if err != nil {
		return err
	}
	username, password := v.String("account_sync_website_username"), v.String("account_sync_website_password")
	if username == "" || password == "" {
		return errSyncNotConfigured
	}
	now := time.Now().UTC()
	if scheduled {
		var state core.AccountSyncState
		if err = a.store.DB.WithContext(ctx).First(&state, 1).Error; err != nil {
			return err
		}
		if !syncDue(v, state, now) {
			return nil
		}
		if err = a.store.DB.WithContext(ctx).Model(&state).Updates(map[string]any{"last_started_at": now, "last_finished_at": nil, "last_error": "", "status": "running"}).Error; err != nil {
			return err
		}
	}
	targets, err := a.store.CollectionTargets(ctx, userID, accountID)
	if err != nil {
		a.finishCollection(scheduled, "Could not load the account list.")
		return err
	}
	if len(targets) == 0 {
		if accountID != 0 {
			return errSyncUnsupported
		}
		return a.finishCollection(scheduled, "")
	}
	// Collect creates a fresh authenticated session for each call. No token is
	// shared with a previous scheduled run or stored in Redis/SQL.
	records, collectionErr := a.collector().Collect(ctx, username, password)
	// Persist a failed attempt even if the read deadline has expired. No further
	// upstream work happens under this short persistence-only context.
	saveCtx, saveCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer saveCancel()
	at := time.Now().UTC()
	failures := 0
	for _, target := range targets {
		message := ""
		var record *supplier.Account
		if collectionErr != nil {
			message = publicCollectionError(collectionErr)
		} else {
			var e error
			record, e = matchSupplierAccount(target, records)
			if e != nil {
				message = e.Error()
			}
		}
		if message != "" {
			failures++
			e := a.store.RecordAccountFailure(saveCtx, target.ID, target.IdentityKey, message, at)
			if e != nil && !errors.Is(e, core.ErrAccountIdentityChanged) {
				a.finishCollection(scheduled, "Could not save account collection results.")
				return e
			}
			continue
		}
		values := core.AccountSnapshotValues{AccountStatus: record.Status, InputTokensTotal: record.InputTokensTotal, OutputTokensTotal: record.OutputTokensTotal, CostTotal: record.CostTotal, Currency: record.Currency, Usage5hPercent: record.Usage5HPercent, Reset5hAt: record.Reset5HAt, Usage7dPercent: record.Usage7DPercent, Reset7dAt: record.Reset7DAt, SourceUpdatedAt: record.SourceUpdatedAt}
		if record.PlatformAccountID != "" {
			values.PlatformAccountID = &record.PlatformAccountID
		}
		e := a.store.RecordAccountSnapshot(saveCtx, target.ID, target.IdentityKey, values, at, v.String("account_sync_timezone"))
		if core.IsValidationError(e) {
			failures++
			e = a.store.RecordAccountFailure(saveCtx, target.ID, target.IdentityKey, "The supplier returned invalid usage data. Previously collected data is retained.", at)
		}
		if e != nil && !errors.Is(e, core.ErrAccountIdentityChanged) {
			a.finishCollection(scheduled, "Could not save account collection results.")
			return e
		}
	}
	message := ""
	if collectionErr != nil {
		message = publicCollectionError(collectionErr)
	} else if failures > 0 {
		message = fmt.Sprintf("%d account(s) could not be updated. Their last successful data is retained.", failures)
	}
	return a.finishCollection(scheduled, message)
}

func (a *app) finishCollection(scheduled bool, message string) error {
	if !scheduled {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	at := time.Now().UTC()
	updates := map[string]any{"last_finished_at": at, "last_error": message, "status": "failed"}
	if message == "" {
		updates["status"] = "succeeded"
		updates["last_success_at"] = at
	}
	return a.store.DB.WithContext(ctx).Model(&core.AccountSyncState{}).Where("id = ?", 1).Updates(updates).Error
}

func (a *app) runAccountSync(ctx context.Context) {
	// Due times are persisted. Restarting this process does not reset the timer.
	check := func() {
		lookup, cancel := context.WithTimeout(ctx, 10*time.Second)
		v, err := a.store.Settings(lookup)
		var state core.AccountSyncState
		if err == nil {
			err = a.store.DB.WithContext(lookup).First(&state, 1).Error
		}
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				log.Print("Account sync: could not read scheduling state")
			}
			return
		}
		if syncDue(v, state, time.Now().UTC()) {
			if err = a.collectAccounts(ctx, 0, 0, true); err != nil && !errors.Is(err, errSyncBusy) && !errors.Is(err, errSyncNotConfigured) && ctx.Err() == nil {
				log.Print("Account sync: collection could not complete; check the saved sync status")
			}
		}
	}
	check()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check()
		}
	}
}
