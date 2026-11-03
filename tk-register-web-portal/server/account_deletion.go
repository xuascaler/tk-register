package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"goapi-tk-register/server/internal/core"
	"goapi-tk-register/server/internal/diagnostics"
	"goapi-tk-register/server/internal/supplier"
)

type accountExpirer interface {
	ExpireAccount(context.Context, string, string, supplier.Target, bool, func(context.Context, supplier.ExpireInfo) error) (supplier.ExpireResult, error)
}

func (a *app) expirer() accountExpirer {
	if a.accountExpirer != nil {
		return a.accountExpirer
	}
	if a.supplierClient != nil {
		return a.supplierClient
	}
	return supplier.NewClient()
}

func accountMutationLeaseKey(account core.SyncedAccount) string {
	return redisPrefix + "account-auth-json:lease:" + digest(strconv.FormatUint(account.VendorID, 10)+":"+strings.ToLower(strings.TrimSpace(account.Account)))
}

func validDeletionRequestID(value string) bool {
	if len(value) < 16 || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

type accountDeletionResponse struct {
	OperationID       string `json:"operation_id"`
	Status            string `json:"status"`
	Message           string `json:"message"`
	RetryAfterSeconds int    `json:"retry_after_seconds,omitempty"`
}

func deletionMessage(op core.AccountDeletionOperation) string {
	switch op.Status {
	case "completed":
		return "Account deleted. Submission and usage history have been retained."
	case "awaiting_verification":
		return "Deletion is being confirmed. Your account is retained until the operation completes."
	case "upstream_confirmed":
		return "Finishing account deletion. Submission and usage history will be retained."
	case "failed":
		switch op.ErrorCode {
		case "ACCOUNT_OWNERSHIP_CONFLICT":
			return "This account has conflicting ownership records. Ask an administrator to check the account."
		case "ACCOUNT_CHANGED":
			return "This account changed during deletion. Reload the page before trying again."
		case "SUPPLIER_LOCKED":
			return "The supplier has locked this account. It has not been deleted."
		case "SUPPLIER_REJECTED":
			return "The supplier did not allow this operation. Your account has been retained."
		case "SYNC_NOT_CONFIGURED":
			return "Supplier access has not been configured. Ask an administrator to check Account sync settings."
		default:
			return "The account could not be deleted. Your account and history have been retained. Please try again later."
		}
	default:
		return "Deleting account. You can close this dialog; the operation will continue."
	}
}

func deletionResponse(op core.AccountDeletionOperation) accountDeletionResponse {
	result := accountDeletionResponse{OperationID: strconv.FormatUint(op.ID, 10), Status: op.Status, Message: deletionMessage(op)}
	if op.Status != "completed" && op.Status != "failed" {
		result.RetryAfterSeconds = 2
		if seconds := int(time.Until(op.NextRunAt).Seconds()) + 1; seconds > result.RetryAfterSeconds {
			result.RetryAfterSeconds = seconds
		}
	}
	return result
}

func deletionFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, core.ErrNotFound):
		fail(w, 404, "ACCOUNT_NOT_FOUND", "The requested account was not found.")
	case errors.Is(err, core.ErrAccountDeletionUnconfirmed):
		fail(w, 409, "ACCOUNT_UNCONFIRMED", "This account has an unfinished or unconfirmed submission. Confirm its result before deleting it.")
	case errors.Is(err, core.ErrAccountOwnershipConflict):
		fail(w, 409, "ACCOUNT_OWNERSHIP_CONFLICT", "This account has conflicting ownership records. Ask an administrator to check the account.")
	case errors.Is(err, core.ErrAccountIdentityChanged):
		fail(w, 409, "ACCOUNT_CHANGED", "This account changed. Reload the page before trying again.")
	case errors.Is(err, core.ErrAccountDeleting):
		fail(w, 409, "ACCOUNT_DELETING", "This account is already being deleted.")
	case core.IsValidationError(err):
		fail(w, 400, "INVALID_REQUEST", "Please check the deletion request.")
	default:
		internal(w)
	}
}

// Session-only endpoints. Personal API tokens cannot acquire administrator
// privileges through this path, and no caller can provide an upstream URL/ID.
func (a *app) accountDeletion(w http.ResponseWriter, r *http.Request, user *core.User, id uint64, admin bool) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		fail(w, 405, "METHOD_NOT_ALLOWED", "This method is not supported.")
		return
	}
	if r.Method == http.MethodGet {
		query := r.URL.Query()
		if len(query) != 1 || len(query["operation_id"])+len(query["request_id"]) != 1 {
			fail(w, 400, "INVALID_REQUEST", "Provide an operation_id or request_id.")
			return
		}
		var op core.AccountDeletionOperation
		var err error
		if raw := query.Get("operation_id"); raw != "" {
			opID, e := core.ParseBusinessID(raw)
			if e != nil {
				fail(w, 400, "INVALID_REQUEST", "Provide a valid operation_id.")
				return
			}
			op, err = a.store.AccountDeletionForUser(r.Context(), opID, user.ID, admin)
		} else {
			requestID := query.Get("request_id")
			if !validDeletionRequestID(requestID) {
				fail(w, 400, "INVALID_REQUEST", "Provide a valid request_id.")
				return
			}
			q := a.store.DB.WithContext(r.Context()).Where("account_id = ? AND request_id = ?", id, requestID)
			if !admin {
				q = q.Where("owner_id = ?", user.ID)
			}
			err = q.First(&op).Error
			// Two tabs may coalesce onto an already-running operation. Recovery
			// can expose only this scoped account's current operation, never a
			// different account or an operation selected by caller identity data.
			if errors.Is(err, core.ErrNotFound) {
				var account core.SyncedAccount
				q = a.store.DB.WithContext(r.Context()).Where("id = ?", id)
				if !admin {
					q = q.Where("user_id = ?", user.ID)
				}
				if e := q.First(&account).Error; e == nil && account.DeletionOperationID != nil {
					op, err = a.store.AccountDeletionForUser(r.Context(), *account.DeletionOperationID, user.ID, admin)
				}
			}
		}
		if err != nil {
			deletionFailure(w, err)
			return
		}
		if op.AccountID != id {
			deletionFailure(w, core.ErrNotFound)
			return
		}
		ok(w, deletionResponse(op))
		return
	}
	if r.URL.RawQuery != "" {
		fail(w, 400, "INVALID_REQUEST", "Send request_id in the JSON body.")
		return
	}
	var input struct {
		RequestID string `json:"request_id"`
	}
	if !decode(w, r, &input) {
		return
	}
	if !validDeletionRequestID(input.RequestID) {
		fail(w, 400, "INVALID_REQUEST", "Provide a unique request_id for this deletion.")
		return
	}
	var target core.SyncedAccount
	q := a.store.DB.WithContext(r.Context()).Where("id = ?", id)
	if !admin {
		q = q.Where("user_id = ?", user.ID)
	}
	if err := q.First(&target).Error; err != nil {
		deletionFailure(w, err)
		return
	}
	retry, err := loginAttempt.Run(r.Context(), a.redis, []string{redisPrefix + "account-delete:rate:" + strconv.FormatUint(user.ID, 10)}, 100, 60).Int()
	if err != nil {
		internal(w)
		return
	}
	if retry > 0 {
		rateFailure(w, retry)
		return
	}
	token, err := randomToken()
	if err != nil {
		internal(w)
		return
	}
	key := accountMutationLeaseKey(target)
	locked, err := a.redis.SetNX(r.Context(), key, token, 90*time.Second).Result()
	if err != nil {
		internal(w)
		return
	}
	if !locked {
		fail(w, 409, "ACCOUNT_UPDATE_RUNNING", "An operation for this account is already running. Please wait.")
		return
	}
	defer a.releaseDeletionLease(key, token)
	op, _, err := a.store.BeginAccountDeletion(r.Context(), id, user.ID, admin, input.RequestID, time.Now().UTC())
	if err != nil {
		deletionFailure(w, err)
		return
	}
	status := http.StatusAccepted
	if op.Status == "completed" || op.Status == "failed" {
		status = http.StatusOK
	}
	respond(w, status, envelope{Success: true, Data: deletionResponse(op)})
}

func (a *app) releaseDeletionLease(key, token string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = releaseSend.Run(ctx, a.redis, []string{key}, token).Err()
}

func deletionErrorCode(err error) string {
	switch {
	case errors.Is(err, core.ErrAccountOwnershipConflict):
		return "ACCOUNT_OWNERSHIP_CONFLICT"
	case errors.Is(err, core.ErrAccountIdentityChanged):
		return "ACCOUNT_CHANGED"
	}
	var upstream *supplier.Error
	if errors.As(err, &upstream) {
		switch upstream.Code {
		case supplier.CodePlanDatesLocked:
			return "SUPPLIER_LOCKED"
		case supplier.CodePlanDatesRejected:
			return "SUPPLIER_REJECTED"
		case supplier.CodePlanDatesNotSent:
			return "SUPPLIER_REQUEST_NOT_SENT"
		case supplier.CodeAccountIdentityChanged:
			return "ACCOUNT_CHANGED"
		case supplier.CodeCredentialsInvalid:
			return "SUPPLIER_LOGIN_FAILED"
		case supplier.CodeContractMismatch:
			return "SUPPLIER_RESPONSE_INVALID"
		}
	}
	return "SUPPLIER_UNAVAILABLE"
}

func deletionPersistence() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

// Each worker pass handles a single durable operation. PUT is permitted only
// while pending; its callback commits awaiting_verification before any bytes
// are sent. Resuming after a crash or a timeout therefore performs reads only.
func (a *app) processAccountDeletion(parent context.Context, now time.Time) (processErr error) {
	ctx, cancel := context.WithTimeout(parent, 65*time.Second)
	defer cancel()
	op, err := a.store.NextAccountDeletion(ctx, now)
	if err != nil || op == nil {
		return err
	}
	ctx = accountSyncTrace(ctx, "account_delete", op.ActorID, op.AccountID)
	ctx = diagnostics.WithTrace(ctx, diagnostics.Trace{JobID: op.ID})
	token, err := randomToken()
	if err != nil {
		return err
	}
	opKey := redisPrefix + "account-delete:lease:" + strconv.FormatUint(op.ID, 10)
	locked, err := a.redis.SetNX(ctx, opKey, token, 90*time.Second).Result()
	if err != nil {
		return err
	}
	if !locked {
		return nil
	}
	defer a.releaseDeletionLease(opKey, token)
	// A damaged identity or transient persistence failure must not leave the
	// oldest operation permanently ahead of other due work. Keep its durable
	// state and retry later; this never replays a write awaiting verification.
	defer func() {
		if processErr != nil {
			persist, stop := deletionPersistence()
			defer stop()
			_ = a.store.DeferAccountDeletion(persist, op.ID, time.Now().UTC().Add(30*time.Second), "OPERATION_RETRY_PENDING")
		}
	}()
	current, target, err := a.store.AccountDeletionTarget(ctx, op.ID)
	if err != nil {
		return err
	}
	if current.Status == "completed" || current.Status == "failed" {
		return nil
	}
	finish := func() error {
		persist, stop := deletionPersistence()
		defer stop()
		if e := a.store.FinishAccountDeletion(persist, op.ID, time.Now().UTC()); e != nil {
			return e
		}
		log.Printf("TK_ACCOUNT_DELETE operation_id=%d account_id=%d outcome=completed", op.ID, op.AccountID)
		return nil
	}
	if current.Status == "upstream_confirmed" {
		return finish()
	}
	deferUntil := func(next time.Time, code string) error {
		persist, stop := deletionPersistence()
		defer stop()
		return a.store.DeferAccountDeletion(persist, op.ID, next, code)
	}
	failOperation := func(code string) error {
		persist, stop := deletionPersistence()
		defer stop()
		return a.store.FailAccountDeletion(persist, op.ID, code, time.Now().UTC())
	}
	key := accountMutationLeaseKey(target)
	locked, err = a.redis.SetNX(ctx, key, token, 90*time.Second).Result()
	if err != nil {
		return err
	}
	if !locked {
		return deferUntil(time.Now().UTC().Add(5*time.Second), "ACCOUNT_UPDATE_RUNNING")
	}
	defer a.releaseDeletionLease(key, token)
	locked, err = a.redis.SetNX(ctx, accountSyncLeaseKey, token, 90*time.Second).Result()
	if err != nil {
		return err
	}
	if !locked {
		return deferUntil(time.Now().UTC().Add(5*time.Second), "SYNC_RUNNING")
	}
	defer a.releaseDeletionLease(accountSyncLeaseKey, token)
	current, target, err = a.store.AccountDeletionTarget(ctx, op.ID)
	if err != nil {
		return err
	}
	if target.CurrentSubmissionID == nil {
		if err = a.store.ConfirmAccountDeletion(ctx, op.ID, "local_only", "", time.Now().UTC()); err != nil {
			return err
		}
		return finish()
	}
	var vendor core.Vendor
	if err = a.store.DB.WithContext(ctx).First(&vendor, target.VendorID).Error; err != nil {
		return err
	}
	if !vendor.Enabled || vendor.Code != "tk-register-0" || vendor.BaseURL != supplier.Origin || !strings.HasPrefix(target.Plan, "claude/") {
		if current.Status == "pending" {
			return failOperation("VENDOR_UNAVAILABLE")
		}
		return deferUntil(time.Now().UTC().Add(5*time.Minute), "VENDOR_UNAVAILABLE")
	}
	settings, err := a.store.Settings(ctx)
	if err != nil {
		return err
	}
	username, password := settings.String("account_sync_website_username"), settings.String("account_sync_website_password")
	if username == "" || password == "" {
		if current.Status == "pending" {
			return failOperation("SYNC_NOT_CONFIGURED")
		}
		return deferUntil(time.Now().UTC().Add(5*time.Minute), "SYNC_NOT_CONFIGURED")
	}
	selection := supplier.Target{Account: target.Account}
	if target.SupplierRecordID != nil {
		selection.RecordID = *target.SupplierRecordID
	}
	if current.SupplierRecordID != "" {
		selection.RecordID = current.SupplierRecordID
	}
	if selection.RecordID != "" {
		if err = a.store.CheckAccountDeletionOwnership(ctx, op.ID, selection.RecordID); err != nil {
			if current.Status == "pending" {
				return failOperation(deletionErrorCode(err))
			}
			return deferUntil(time.Now().UTC().Add(5*time.Minute), deletionErrorCode(err))
		}
	}
	allowWrite := current.Status == "pending"
	beforeWrite := func(check context.Context, info supplier.ExpireInfo) error {
		if !strings.EqualFold(strings.TrimSpace(info.Account), strings.TrimSpace(target.Account)) {
			return core.ErrAccountIdentityChanged
		}
		return a.store.MarkAccountDeletionWriteStarted(check, op.ID, info.RecordID, info.PlanStartTime, info.PlanEndTime, time.Now().UTC())
	}
	result, updateErr := a.expirer().ExpireAccount(ctx, username, password, selection, allowWrite, beforeWrite)
	persist, stop := deletionPersistence()
	saved, _, readErr := a.store.AccountDeletionTarget(persist, op.ID)
	stop()
	if readErr != nil {
		return readErr
	}
	if updateErr != nil {
		code := deletionErrorCode(updateErr)
		seconds, retryAt, _, _ := supplier.RetryDetails(updateErr)
		if code == "SUPPLIER_REJECTED" || code == "SUPPLIER_REQUEST_NOT_SENT" {
			persist, stop := deletionPersistence()
			defer stop()
			return a.store.RejectAccountDeletion(persist, op.ID, code, time.Now().UTC())
		}
		if seconds > 0 {
			if retryAt.IsZero() {
				retryAt = time.Now().UTC().Add(time.Duration(seconds) * time.Second)
			}
			return deferUntil(retryAt, "SUPPLIER_RATE_LIMITED")
		}
		if saved.Status == "pending" {
			return failOperation(code)
		}
		log.Printf("TK_ACCOUNT_DELETE operation_id=%d account_id=%d outcome=awaiting_verification error_code=%s", op.ID, op.AccountID, code)
		return deferUntil(time.Now().UTC().Add(30*time.Second), code)
	}
	if !result.Confirmed {
		if saved.Status != "awaiting_verification" {
			return failOperation("SUPPLIER_RESPONSE_INVALID")
		}
		// A read showing the old values does not prove a timed-out write will
		// never arrive. Keep the account and do not unlock another mutation.
		return deferUntil(time.Now().UTC().Add(5*time.Minute), "UPSTREAM_UPDATE_NOT_CONFIRMED")
	}
	mode := "upstream_not_found"
	recordID := selection.RecordID
	if result.Found {
		mode = "upstream_dates_updated"
		recordID = result.Info.RecordID
		if err = a.store.CheckAccountDeletionOwnership(ctx, op.ID, recordID); err != nil {
			if saved.Status == "pending" {
				return failOperation(deletionErrorCode(err))
			}
			return deferUntil(time.Now().UTC().Add(5*time.Minute), deletionErrorCode(err))
		}
		// Already-expired accounts skip PUT. Preserve the configuration read
		// before removal without creating a fictitious mutation attempt.
		if saved.Status == "pending" {
			if err = a.store.DB.WithContext(ctx).Model(&core.AccountDeletionOperation{}).Where("id = ? AND status = ?", op.ID, "pending").Updates(map[string]any{"supplier_record_id": recordID, "original_plan_start_time": result.Info.PlanStartTime, "original_plan_end_time": result.Info.PlanEndTime}).Error; err != nil {
				return err
			}
		}
	}
	persist, stop = deletionPersistence()
	defer stop()
	if err = a.store.ConfirmAccountDeletion(persist, op.ID, mode, recordID, time.Now().UTC()); err != nil {
		return err
	}
	return finish()
}

func (a *app) runAccountDeletions(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if err := a.processAccountDeletion(ctx, time.Now().UTC()); err != nil && ctx.Err() == nil {
			// No DB errors, upstream payloads, configuration, or credentials.
			log.Print("Account deletion could not complete; its saved operation will be checked again")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
