package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"goapi-tk-register/server/common"
	"goapi-tk-register/server/internal/core"
	"goapi-tk-register/server/internal/supplier"
)

type accountAuthEditor interface {
	CheckAuthJSON(context.Context, string, string, supplier.Target) (supplier.EditInfo, error)
	UpdateAuthJSON(context.Context, string, string, supplier.Target, json.RawMessage) (supplier.EditInfo, error)
	CollectAccount(context.Context, string, string, supplier.Target) (supplier.Account, error)
}

func (a *app) authEditor() accountAuthEditor {
	if a.accountAuthEditor != nil {
		return a.accountAuthEditor
	}
	return supplier.NewClient()
}

type authJSONProblem struct {
	status        int
	code, message string
}

func authJSONError(err error) authJSONProblem {
	var upstream *supplier.Error
	if errors.As(err, &upstream) {
		switch upstream.Code {
		case supplier.CodeAccountNotFound:
			return authJSONProblem{404, "AUTH_JSON_ACCOUNT_NOT_FOUND", "This account was not found on the supplier website."}
		case supplier.CodeAccountAmbiguous:
			return authJSONProblem{409, "AUTH_JSON_ACCOUNT_AMBIGUOUS", "More than one supplier record matches this email. An administrator must check the account identity."}
		case supplier.CodeNotEditable:
			return authJSONProblem{409, "AUTH_JSON_NOT_EDITABLE", "The supplier does not allow Auth JSON updates for this account's current status."}
		case supplier.CodeUpdateRejected:
			return authJSONProblem{502, "AUTH_JSON_UPDATE_REJECTED", "The supplier rejected the credential update. Your account data has been retained."}
		case supplier.CodeUpdateUnconfirmed:
			return authJSONProblem{504, "AUTH_JSON_UPDATE_UNCONFIRMED", "We could not confirm whether the update completed. Check the account before trying again."}
		case supplier.CodeInvalidAuthJSON:
			return authJSONProblem{400, "INVALID_AUTH_JSON", "Please check the Auth JSON format and required fields."}
		case supplier.CodeCredentialsInvalid, supplier.CodeSessionExpired:
			return authJSONProblem{502, "AUTH_JSON_LOGIN_FAILED", "The supplier website login failed. Ask an administrator to check Account sync settings."}
		}
	}
	// No arbitrary error body or exception is returned or recorded.
	return authJSONProblem{502, "AUTH_JSON_UNAVAILABLE", "The supplier website could not be reached or returned an unsupported response. No update was confirmed."}
}
func authJSONFail(w http.ResponseWriter, p authJSONProblem) { fail(w, p.status, p.code, p.message) }

func (a *app) accountAuthJSON(w http.ResponseWriter, r *http.Request, user *core.User, id uint64, admin bool, lookup func(uint64) (core.AccountDTO, error)) {
	if r.Method != "GET" && r.Method != "POST" {
		fail(w, 405, "METHOD_NOT_ALLOWED", "This method is not supported.")
		return
	}
	// accountSyncRoutes has already performed the session/CSRF, role and owner
	// checks. Re-read the scoped row instead of trusting any client-supplied ID.
	query := a.store.DB.WithContext(r.Context()).Where("id = ?", id)
	if !admin {
		query = query.Where("user_id = ?", user.ID)
	}
	var target core.SyncedAccount
	if err := query.First(&target).Error; err != nil {
		vendorFailure(w, err)
		return
	}
	var vendor core.Vendor
	if err := a.store.DB.WithContext(r.Context()).First(&vendor, target.VendorID).Error; err != nil {
		internal(w)
		return
	}
	if target.CurrentSubmissionID == nil {
		fail(w, 409, "ACCOUNT_NOT_ACCEPTED", "Only an accepted account can have its credentials updated.")
		return
	}
	if !vendor.Enabled || vendor.Code != "tk-register-0" || vendor.BaseURL != supplier.Origin || !strings.HasPrefix(target.Plan, "claude/") {
		fail(w, 409, "AUTH_JSON_UNSUPPORTED", "Auth JSON updates are unavailable for this account or vendor.")
		return
	}
	values, err := a.store.Settings(r.Context())
	if err != nil {
		internal(w)
		return
	}
	username, password := values.String("account_sync_website_username"), values.String("account_sync_website_password")
	if username == "" || password == "" {
		fail(w, 503, "SYNC_NOT_CONFIGURED", "Website login has not been configured by an administrator.")
		return
	}
	limit := 30
	if r.Method == "POST" {
		limit = 10
	}
	retry, err := loginAttempt.Run(r.Context(), a.redis, []string{redisPrefix + "account-auth-json:rate:" + r.Method + ":" + strconv.FormatUint(user.ID, 10)}, limit, 60).Int()
	if err != nil {
		internal(w)
		return
	}
	if retry > 0 {
		rateFailure(w, retry)
		return
	}
	selection := supplier.Target{Account: target.Account}
	if target.SupplierRecordID != nil {
		selection.RecordID = *target.SupplierRecordID
	}
	editor := a.authEditor()
	if r.Method == "GET" {
		ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
		defer cancel()
		info, err := editor.CheckAuthJSON(ctx, username, password, selection)
		if err != nil {
			authJSONFail(w, authJSONError(err))
			return
		}
		if !strings.EqualFold(strings.TrimSpace(info.Account), strings.TrimSpace(target.Account)) {
			authJSONFail(w, authJSONError(errors.New("identity mismatch")))
			return
		}
		if err = a.store.ConfirmSupplierRecord(ctx, target.ID, target.IdentityKey, info.RecordID); err != nil {
			fail(w, 409, "ACCOUNT_CHANGED", "This account changed while it was being checked. Reload the page.")
			return
		}
		ok(w, map[string]any{"account": info.Account, "plan": info.Plan, "editable": info.Editable, "reason": info.Reason, "supplier_record_id": info.RecordID, "updated_at": target.AuthJSONUpdatedAt})
		return
	}
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		fail(w, 415, "INVALID_CONTENT_TYPE", "Use application/json.")
		return
	}
	var input struct {
		AuthJSON any `json:"auth_json"`
	}
	if err = common.DecodeJson(http.MaxBytesReader(w, r.Body, 3<<20), &input); err != nil {
		fail(w, 400, "INVALID_REQUEST", "Provide auth_json as a JSON object or JSON string.")
		return
	}
	normalized, err := core.ValidateClaudeAuthJSON(input.AuthJSON)
	if err != nil {
		vendorFailure(w, err)
		return
	}
	raw, err := common.Marshal(normalized)
	if err != nil {
		internal(w)
		return
	}
	// The lock is keyed by vendor+normalized email, not owner, since two local
	// histories can refer to the same supplier row. A global collection lease
	// also prevents a pre-update collection overwriting the immediate refresh.
	lockKey := redisPrefix + "account-auth-json:lease:" + digest(strconv.FormatUint(target.VendorID, 10)+":"+strings.ToLower(strings.TrimSpace(target.Account)))
	token, err := randomToken()
	if err != nil {
		internal(w)
		return
	}
	locked, err := a.redis.SetNX(r.Context(), lockKey, token, 90*time.Second).Result()
	if err != nil {
		internal(w)
		return
	}
	if !locked {
		fail(w, 409, "AUTH_JSON_UPDATE_RUNNING", "An update for this account is already running. Please wait.")
		return
	}
	release := func(key string) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = releaseSend.Run(ctx, a.redis, []string{key}, token).Err()
	}
	defer release(lockKey)
	locked, err = a.redis.SetNX(r.Context(), accountSyncLeaseKey, token, 90*time.Second).Result()
	if err != nil {
		internal(w)
		return
	}
	if !locked {
		fail(w, 409, "SYNC_RUNNING", "An account sync is running. Please try again shortly.")
		return
	}
	defer release(accountSyncLeaseKey)
	// After authorization and lock acquisition, finish the bounded mutation and
	// persist its outcome even when the browser navigates away or disconnects.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 55*time.Second)
	defer cancel()
	audit, err := a.store.BeginAuthJSONUpdate(ctx, target, user.ID, w.Header().Get("X-Request-ID"))
	if err != nil {
		if errors.Is(err, core.ErrAccountIdentityChanged) {
			fail(w, 409, "ACCOUNT_CHANGED", "This account changed. Reload the page before updating.")
		} else {
			internal(w)
		}
		return
	}
	updateCtx, updateCancel := context.WithTimeout(ctx, 35*time.Second)
	info, updateErr := editor.UpdateAuthJSON(updateCtx, username, password, selection, json.RawMessage(raw))
	updateCancel()
	at := time.Now().UTC()
	status, code := "updated", ""
	problem := authJSONProblem{}
	if updateErr != nil {
		problem = authJSONError(updateErr)
		code = problem.code
		status = "failed"
		if code == "AUTH_JSON_UPDATE_UNCONFIRMED" {
			status = "unknown"
		} else if code == "AUTH_JSON_UPDATE_REJECTED" || code == "AUTH_JSON_NOT_EDITABLE" {
			status = "rejected"
		}
	}
	saveCtx, saveCancel := context.WithTimeout(context.Background(), 5*time.Second)
	unchanged, persistErr := a.store.FinishAuthJSONUpdate(saveCtx, audit, status, info.RecordID, code, at)
	saveCancel()
	if updateErr != nil {
		authJSONFail(w, problem)
		return
	}
	refreshStatus, refreshMessage := "failed", "Auth JSON updated, but account refresh failed. Your last successful data is retained. Try syncing again."
	if persistErr == nil && unchanged {
		refreshCtx, refreshCancel := context.WithTimeout(ctx, 15*time.Second)
		record, refreshErr := editor.CollectAccount(refreshCtx, username, password, supplier.Target{Account: target.Account, RecordID: info.RecordID})
		refreshCancel()
		if refreshErr == nil && (!strings.EqualFold(strings.TrimSpace(record.Account), strings.TrimSpace(target.Account)) || record.ID != info.RecordID) {
			refreshErr = errors.New("identity mismatch")
		}
		persistCtx, persistCancel := context.WithTimeout(context.Background(), 5*time.Second)
		now := time.Now().UTC()
		if refreshErr == nil {
			snapshot := core.AccountSnapshotValues{AccountStatus: record.Status, InputTokensTotal: record.InputTokensTotal, OutputTokensTotal: record.OutputTokensTotal, CostTotal: record.CostTotal, Currency: record.Currency, Usage5hPercent: record.Usage5HPercent, Reset5hAt: record.Reset5HAt, Usage7dPercent: record.Usage7DPercent, Reset7dAt: record.Reset7DAt, SourceUpdatedAt: record.SourceUpdatedAt}
			if record.PlatformAccountID != "" {
				snapshot.PlatformAccountID = &record.PlatformAccountID
			}
			refreshErr = a.store.RecordAccountSnapshot(persistCtx, target.ID, target.IdentityKey, snapshot, now, values.String("account_sync_timezone"))
		}
		if refreshErr == nil {
			refreshStatus, refreshMessage = "succeeded", "Auth JSON updated and account data refreshed."
		} else {
			_ = a.store.RecordAccountFailure(persistCtx, target.ID, target.IdentityKey, "Auth JSON was updated, but the account could not be refreshed. Previously collected data is retained.", now)
		}
		_ = a.store.FinishAuthJSONRefresh(persistCtx, audit.ID, refreshStatus == "succeeded")
		persistCancel()
	} else if !unchanged && persistErr == nil {
		refreshMessage = "Auth JSON updated, but this local account changed during the request. Reload the page to view its current data."
		recordCtx, recordCancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = a.store.FinishAuthJSONRefresh(recordCtx, audit.ID, false)
		recordCancel()
	}
	// A known successful vendor write is never reported as a failed write just
	// because subsequent persistence/readback fails.
	account, lookupErr := lookup(id)
	if lookupErr != nil {
		ok(w, map[string]any{"updated": true, "refresh_status": "failed", "refresh_message": "Auth JSON updated, but local account data could not be loaded. Reload the page.", "account": nil, "updated_at": at})
		return
	}
	ok(w, map[string]any{"updated": true, "refresh_status": refreshStatus, "refresh_message": refreshMessage, "account": account, "updated_at": at})
}
