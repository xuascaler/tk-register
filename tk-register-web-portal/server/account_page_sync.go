package main

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"goapi-tk-register/server/common"
	"goapi-tk-register/server/internal/core"
	"goapi-tk-register/server/internal/supplier"
)

type accountPageCollector interface {
	CollectTargets(context.Context, string, string, []supplier.Target) ([]supplier.AccountResult, error)
}

func (a *app) pageCollector() accountPageCollector {
	if a.accountPageCollector != nil {
		return a.accountPageCollector
	}
	if a.supplierClient != nil {
		return a.supplierClient
	}
	return supplier.NewClient()
}

type pageSyncResult struct {
	AccountID  string `json:"account_id"`
	Status     string `json:"status"`
	Message    string `json:"message,omitempty"`
	RetryAfter int    `json:"retry_after,omitempty"`
	RetryStage string `json:"retry_stage,omitempty"`
}
type pageSyncResponse struct {
	Accounts     []core.AccountDTO `json:"accounts"`
	Results      []pageSyncResult  `json:"results"`
	UpdatedCount int               `json:"updated_count"`
	FailedCount  int               `json:"failed_count"`
	SkippedCount int               `json:"skipped_count"`
}

func pageTargetKey(target supplier.Target) string {
	return strings.ToLower(strings.TrimSpace(target.Account)) + "\x00" + target.RecordID
}

func supplierAccountSnapshot(record *supplier.Account) core.AccountSnapshotValues {
	snapshot := core.AccountSnapshotValues{AccountStatus: record.Status, InputTokensTotal: record.InputTokensTotal, OutputTokensTotal: record.OutputTokensTotal, CostTotal: record.CostTotal, Currency: record.Currency, Usage5hPercent: record.Usage5HPercent, Reset5hAt: record.Reset5HAt, Usage7dPercent: record.Usage7DPercent, Reset7dAt: record.Reset7DAt, SourceUpdatedAt: record.SourceUpdatedAt}
	if record.PlatformAccountID != "" {
		snapshot.PlatformAccountID = &record.PlatformAccountID
	}
	return snapshot
}

func pageCollectionError(err error) string {
	if seconds, _, _, _ := supplier.RetryDetails(err); seconds > 0 {
		return supplierWaitMessage(err)
	}
	var upstream *supplier.Error
	if errors.As(err, &upstream) {
		switch upstream.Code {
		case supplier.CodeTimeout:
			return "The supplier website timed out. Previously collected data is retained."
		case supplier.CodeRateLimited:
			return supplierWaitMessage(err)
		case supplier.CodeAccountNotFound:
			return "This account was not found on the supplier website. Previously collected data is retained."
		case supplier.CodeAccountAmbiguous:
			return "More than one supplier record matches this account. An administrator must check its identity."
		case supplier.CodeCredentialsInvalid, supplier.CodeSessionExpired:
			return "The supplier website login failed. Ask an administrator to check Account sync settings."
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "The supplier website timed out. Previously collected data is retained."
	}
	return "The supplier website could not be queried. Previously collected data is retained."
}

func pagePersistenceContext(deadline time.Time, limit time.Duration) (context.Context, context.CancelFunc) {
	end := time.Now().Add(limit)
	if deadline.Before(end) {
		end = deadline
	}
	return context.WithDeadline(context.Background(), end)
}

func (a *app) acquirePageSyncLease(ctx context.Context, token string) error {
	wait, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		locked, err := a.redis.SetNX(wait, accountSyncLeaseKey, token, 90*time.Second).Result()
		if err != nil {
			if wait.Err() != nil {
				return errSyncBusy
			}
			return err
		}
		if locked {
			return nil
		}
		select {
		case <-wait.Done():
			return errSyncBusy
		case <-ticker.C:
		}
	}
}

func (a *app) syncAccountPage(w http.ResponseWriter, r *http.Request, user *core.User, admin bool) {
	if r.Method != "POST" {
		fail(w, 405, "METHOD_NOT_ALLOWED", "This method is not supported.")
		return
	}
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		fail(w, 415, "INVALID_CONTENT_TYPE", "Use application/json.")
		return
	}
	var input struct {
		AccountIDs []string `json:"account_ids"`
	}
	if err := common.DecodeJson(http.MaxBytesReader(w, r.Body, 8192), &input); err != nil {
		fail(w, 400, "INVALID_REQUEST", "Provide account_ids as an array of local account IDs.")
		return
	}
	if len(input.AccountIDs) < 1 || len(input.AccountIDs) > 20 {
		fail(w, 400, "INVALID_ACCOUNTS", "Choose between 1 and 20 accounts from the current page.")
		return
	}
	ids := make([]uint64, 0, len(input.AccountIDs))
	seen := map[uint64]bool{}
	for _, value := range input.AccountIDs {
		id, err := core.ParseBusinessID(value)
		if err != nil || seen[id] {
			fail(w, 400, "INVALID_ACCOUNTS", "Provide distinct, valid local account IDs.")
			return
		}
		seen[id] = true
		ids = append(ids, id)
	}
	deadline := time.Now().Add(57 * time.Second)
	ctx, cancel := context.WithDeadline(r.Context(), deadline)
	defer cancel()
	targets, err := a.store.PageSyncTargets(ctx, user.ID, ids, admin)
	if err != nil {
		vendorFailure(w, err)
		return
	}
	var diagnosticAccountID uint64
	if len(targets) == 1 {
		diagnosticAccountID = targets[0].ID
	}
	ctx = accountSyncTrace(ctx, "manual_sync", user.ID, diagnosticAccountID)
	retry, err := loginAttempt.Run(ctx, a.redis, []string{redisPrefix + "account-sync:page-rate:" + strconv.FormatUint(user.ID, 10)}, 200, 60).Int()
	if err != nil {
		internal(w)
		return
	}
	if retry > 0 {
		syncDiagnosticGate(ctx, "local_rate_limit", 200, 60, retry)
		rateFailure(w, retry)
		return
	}
	summary, finishDiagnostic := beginSyncDiagnostic(ctx)
	defer finishDiagnostic()
	out := pageSyncResponse{Accounts: []core.AccountDTO{}, Results: make([]pageSyncResult, len(targets))}
	eligible := []int{}
	selections := []supplier.Target{}
	selectionKeys := map[string]bool{}
	vendors := map[uint64]core.Vendor{}
	for i, target := range targets {
		out.Results[i] = pageSyncResult{AccountID: strconv.FormatUint(target.ID, 10), Status: "skipped"}
		vendor, found := vendors[target.VendorID]
		if !found {
			if err = a.store.DB.WithContext(ctx).First(&vendor, target.VendorID).Error; err != nil {
				internal(w)
				return
			}
			vendors[target.VendorID] = vendor
		}
		switch {
		case !target.AvailableForMutation():
			out.Results[i].Message = "This account is being deleted. Please wait."
		case target.CurrentSubmissionID == nil:
			out.Results[i].Message = "Only accepted accounts can be synchronized."
		case !vendor.Enabled:
			out.Results[i].Message = "This vendor is disabled."
		case vendor.Code != "tk-register-0" || vendor.BaseURL != supplier.Origin || !strings.HasPrefix(target.Plan, "claude/"):
			out.Results[i].Message = "Website sync is unavailable for this account or vendor."
		default:
			eligible = append(eligible, i)
			selection := supplier.Target{Account: target.Account}
			if target.SupplierRecordID != nil {
				selection.RecordID = *target.SupplierRecordID
			}
			key := pageTargetKey(selection)
			if !selectionKeys[key] {
				selectionKeys[key] = true
				selections = append(selections, selection)
			}
		}
	}
	if len(eligible) > 0 {
		values, e := a.store.Settings(ctx)
		if e != nil {
			internal(w)
			return
		}
		username, password := values.String("account_sync_website_username"), values.String("account_sync_website_password")
		if username == "" || password == "" {
			summary.ErrorKind = "configuration"
			fail(w, 503, "SYNC_NOT_CONFIGURED", "Website login has not been configured by an administrator.")
			return
		}
		token, e := randomToken()
		if e != nil {
			internal(w)
			return
		}
		if e = a.acquirePageSyncLease(ctx, token); e != nil {
			if errors.Is(e, errSyncBusy) {
				summary.Event = ""
				syncDiagnosticGate(ctx, "sync_lock_busy", 0, 0, 5)
				fail(w, 409, "SYNC_RUNNING", "An account sync is still running. Please try again shortly.")
			} else {
				internal(w)
			}
			return
		}
		defer func() {
			release, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = releaseSend.Run(release, a.redis, []string{accountSyncLeaseKey}, token).Err()
		}()
		collection, cancelCollect := context.WithTimeout(ctx, 40*time.Second)
		records, collectionErr := a.pageCollector().CollectTargets(collection, username, password, selections)
		if collectionErr != nil {
			summary.ErrorKind = syncDiagnosticError(collectionErr)
		}
		cancelCollect()
		// The collector returns an explicit result for each queried target. Preserve
		// successful rows even if a later filtered search failed or timed out.
		byTarget := map[string]supplier.AccountResult{}
		duplicates := map[string]bool{}
		for _, record := range records {
			key := pageTargetKey(record.Target)
			if _, exists := byTarget[key]; exists {
				duplicates[key] = true
			}
			byTarget[key] = record
		}
		saveCtx, saveCancel := pagePersistenceContext(deadline, 5*time.Second)
		at := time.Now().UTC()
		for _, i := range eligible {
			target := targets[i]
			selection := supplier.Target{Account: target.Account}
			if target.SupplierRecordID != nil {
				selection.RecordID = *target.SupplierRecordID
			}
			key := pageTargetKey(selection)
			result, found := byTarget[key]
			rowErr := result.Err
			if !found || duplicates[key] {
				rowErr = collectionErr
				if rowErr == nil {
					rowErr = errors.New("missing or duplicate collection result")
				}
			}
			if rowErr == nil && (result.Account == nil || !strings.EqualFold(strings.TrimSpace(result.Account.Account), strings.TrimSpace(target.Account)) || !strings.HasPrefix(result.Account.Plan, "claude/")) {
				rowErr = errors.New("account identity mismatch")
			}
			message := ""
			if rowErr == nil {
				record := result.Account
				rowErr = a.store.ConfirmSupplierRecord(saveCtx, target.ID, target.IdentityKey, record.ID)
				if rowErr == nil {
					rowErr = a.store.RecordAccountSnapshot(saveCtx, target.ID, target.IdentityKey, supplierAccountSnapshot(record), at, values.String("account_sync_timezone"))
				}
				if rowErr != nil {
					summary.ErrorKind = "persistence"
					message = "Account data could not be saved. Previously collected data is retained."
				}
			} else {
				if summary.ErrorKind == "unknown" {
					summary.ErrorKind = syncDiagnosticError(rowErr)
				}
				message = pageCollectionError(rowErr)
			}
			if rowErr == nil {
				out.Results[i].Status = "updated"
				out.Results[i].Message = ""
				continue
			}
			out.Results[i].Status = "failed"
			out.Results[i].RetryAfter, _, out.Results[i].RetryStage, _ = supplier.RetryDetails(rowErr)
			if errors.Is(rowErr, core.ErrAccountIdentityChanged) {
				message = "This account changed during synchronization. Reload the page before trying again."
			} else {
				if saveErr := a.store.RecordAccountFailure(saveCtx, target.ID, target.IdentityKey, message, at); errors.Is(saveErr, core.ErrAccountIdentityChanged) {
					message = "This account changed during synchronization. Reload the page before trying again."
				}
			}
			out.Results[i].Message = message
		}
		saveCancel()
	}
	readCtx, readCancel := pagePersistenceContext(deadline, 4*time.Second)
	defer readCancel()
	var identities map[uint64]string
	out.Accounts, identities, err = a.store.PageSyncedAccountDTOs(readCtx, user.ID, ids, admin)
	if err != nil {
		internal(w)
		return
	}
	for i, target := range targets {
		if out.Results[i].Status == "updated" && identities[target.ID] != target.IdentityKey {
			out.Results[i].Status = "failed"
			out.Results[i].Message = "This account changed during synchronization. Reload the page before trying again."
		}
	}
	for _, result := range out.Results {
		switch result.Status {
		case "updated":
			out.UpdatedCount++
		case "failed":
			out.FailedCount++
		case "skipped":
			out.SkippedCount++
		}
	}
	summary.UpdatedCount, summary.FailedCount, summary.SkippedCount = out.UpdatedCount, out.FailedCount, out.SkippedCount
	if out.FailedCount == 0 {
		summary.Outcome, summary.ErrorKind = "succeeded", ""
		if out.UpdatedCount == 0 {
			summary.Outcome = "skipped"
		}
	}
	ok(w, out)
}
