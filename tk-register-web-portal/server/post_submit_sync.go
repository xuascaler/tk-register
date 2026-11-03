package main

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"goapi-tk-register/server/internal/core"
	"goapi-tk-register/server/internal/diagnostics"
	"goapi-tk-register/server/internal/supplier"
	"gorm.io/gorm"
)

const postSubmissionSyncUnavailable = "This account operation is no longer eligible for automatic account sync."

// processPostSubmissionSync handles at most one due job. The lease is acquired
// before claiming so competing sync work only postpones a pending job; it never
// consumes a query attempt. The persisted job controls the retry budget for both
// successful API submissions and API Auth JSON updates.
func (a *app) processPostSubmissionSync(parent context.Context, now time.Time) error {
	deadline := time.Now().Add(55 * time.Second)
	ctx, cancel := context.WithDeadline(parent, deadline)
	defer cancel()
	if err := a.store.RecoverPostSubmissionSyncJobs(ctx, now); err != nil {
		return err
	}
	job, err := a.store.NextPostSubmissionSyncJob(ctx, now)
	if err != nil || job == nil {
		return err
	}
	source := "post_submit_sync"
	if job.AuthJSONAuditID != nil {
		source = "auth_json_refresh"
	}
	ctx = accountSyncTrace(ctx, source, 0, job.AccountID)
	ctx = diagnostics.WithTrace(ctx, diagnostics.Trace{JobID: job.ID})
	token, err := randomToken()
	if err != nil {
		return err
	}
	locked, err := a.redis.SetNX(ctx, accountSyncLeaseKey, token, 90*time.Second).Result()
	if err != nil {
		return err
	}
	if !locked {
		syncDiagnosticGate(ctx, "sync_lock_busy", 0, 0, 5)
		return a.store.DeferPostSubmissionSyncJob(ctx, job.ID, now)
	}
	defer func() {
		release, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_ = releaseSend.Run(release, a.redis, []string{accountSyncLeaseKey}, token).Err()
	}()
	claimed, err := a.store.ClaimPostSubmissionSyncJob(ctx, job.ID, now)
	if err != nil || !claimed {
		return err
	}
	ctx = diagnostics.WithTrace(ctx, diagnostics.Trace{Attempt: job.AttemptCount + 1})
	summary, finishDiagnostic := beginSyncDiagnostic(ctx)
	defer finishDiagnostic()

	var retryAt time.Time
	status, message := "failed", "Automatic account sync could not complete. Previously collected data is retained."
	finish := func() error {
		persist, stop := pagePersistenceContext(deadline, 5*time.Second)
		defer stop()
		summary.Outcome = status
		switch status {
		case "succeeded":
			summary.UpdatedCount, summary.ErrorKind = 1, ""
		case "skipped":
			summary.SkippedCount, summary.ErrorKind = 1, ""
		default:
			summary.FailedCount = 1
		}
		err := a.store.FinishPostSubmissionSyncJobAfter(persist, job.ID, status, message, time.Now().UTC(), retryAt)
		if err != nil {
			summary.Outcome, summary.ErrorKind = "failed", "persistence"
		}
		return err
	}
	target, err := a.store.PostSubmissionSyncTarget(ctx, *job)
	if err != nil {
		if errors.Is(err, core.ErrPostSubmissionSyncIneligible) {
			status, message = "skipped", postSubmissionSyncUnavailable
		} else {
			summary.ErrorKind = "persistence"
		}
		return finish()
	}
	values, err := a.store.Settings(ctx)
	if err != nil {
		summary.ErrorKind = "persistence"
		return finish()
	}
	username, password := values.String("account_sync_website_username"), values.String("account_sync_website_password")
	if username == "" || password == "" {
		summary.ErrorKind = "configuration"
		message = "Website login has not been configured by an administrator."
		return finish()
	}
	if ctx.Err() != nil {
		summary.ErrorKind = syncDiagnosticError(ctx.Err())
		return finish()
	}
	selection := supplier.Target{Account: target.Account}
	if target.SupplierRecordID != nil {
		selection.RecordID = *target.SupplierRecordID
	}
	// CollectTargets reuses the shared session for the current saved credentials.
	// No account submission or full inventory call occurs.
	collection, stopCollection := context.WithTimeout(ctx, 40*time.Second)
	records, collectionErr := a.pageCollector().CollectTargets(collection, username, password, []supplier.Target{selection})
	stopCollection()
	record, rowErr := postSubmissionSyncResult(target, selection, records, collectionErr)
	retrySeconds, retryDeadline, _, deferred := supplier.RetryDetails(rowErr)
	if retrySeconds > 0 {
		retryAt = retryDeadline
		if retryAt.IsZero() {
			retryAt = time.Now().UTC().Add(time.Duration(retrySeconds) * time.Second)
		}
	}
	if deferred && retrySeconds > 0 {
		summary.Outcome, summary.ErrorKind, summary.WaitSeconds = "rejected", "rate_limited", retrySeconds
		persist, stop := pagePersistenceContext(deadline, 5*time.Second)
		defer stop()
		err := a.store.DeferClaimedPostSubmissionSyncJob(persist, job.ID, job.AttemptCount+1, now, retryAt, time.Now().UTC(), supplierWaitMessage(rowErr))
		if err != nil {
			summary.Outcome, summary.ErrorKind = "failed", "persistence"
		}
		return err
	}
	if rowErr != nil {
		summary.ErrorKind = syncDiagnosticError(rowErr)
		message = pageCollectionError(rowErr)
	}
	persist, stopPersist := pagePersistenceContext(deadline, 5*time.Second)
	at := time.Now().UTC()
	// Keep the final eligibility check and all account writes in one transaction.
	// The account row lock also guards a newer accepted submission which retains
	// the same identity key and must not receive this older job's observation.
	err = a.store.DB.WithContext(persist).Transaction(func(tx *gorm.DB) error {
		store := *a.store
		store.DB = tx
		current, e := store.PostSubmissionSyncTarget(persist, *job)
		if e != nil {
			return e
		}
		if rowErr == nil && current.SupplierRecordID != nil && *current.SupplierRecordID != record.ID {
			rowErr = errors.New("supplier record changed during collection")
			summary.ErrorKind = "contract"
			message = pageCollectionError(rowErr)
		}
		if rowErr != nil {
			return store.RecordAccountFailure(persist, target.ID, target.IdentityKey, message, at)
		}
		if e = store.ConfirmSupplierRecord(persist, target.ID, target.IdentityKey, record.ID); e != nil {
			return e
		}
		return store.RecordAccountSnapshot(persist, target.ID, target.IdentityKey, supplierAccountSnapshot(record), at, values.String("account_sync_timezone"))
	})
	if errors.Is(err, core.ErrPostSubmissionSyncIneligible) || errors.Is(err, core.ErrAccountIdentityChanged) {
		status, message = "skipped", postSubmissionSyncUnavailable
	} else if err != nil {
		summary.ErrorKind = "persistence"
		message = "Account data could not be saved. Previously collected data is retained."
		// Invalid snapshot data must leave the previous observation intact while
		// still recording this failed attempt. A failed confirmation or
		// snapshot transaction has already rolled back before this write.
		failureErr := a.store.DB.WithContext(persist).Transaction(func(tx *gorm.DB) error {
			store := *a.store
			store.DB = tx
			if _, e := store.PostSubmissionSyncTarget(persist, *job); e != nil {
				return e
			}
			return store.RecordAccountFailure(persist, target.ID, target.IdentityKey, message, at)
		})
		if errors.Is(failureErr, core.ErrPostSubmissionSyncIneligible) || errors.Is(failureErr, core.ErrAccountIdentityChanged) {
			status, message = "skipped", postSubmissionSyncUnavailable
		}
	} else if rowErr == nil {
		status, message = "succeeded", ""
	}
	stopPersist()
	return finish()
}

func postSubmissionSyncResult(target core.SyncedAccount, selection supplier.Target, records []supplier.AccountResult, collectionErr error) (*supplier.Account, error) {
	if len(records) != 1 || pageTargetKey(records[0].Target) != pageTargetKey(selection) {
		if collectionErr != nil {
			return nil, collectionErr
		}
		return nil, errors.New("missing or duplicate collection result")
	}
	result := records[0]
	if result.Err != nil {
		return nil, result.Err
	}
	record := result.Account
	if record == nil || !strings.EqualFold(strings.TrimSpace(record.Account), strings.TrimSpace(target.Account)) || !strings.HasPrefix(record.Plan, "claude/") || (selection.RecordID != "" && selection.RecordID != record.ID) {
		return nil, errors.New("account identity mismatch")
	}
	return record, nil
}

func (a *app) runPostSubmissionSync(ctx context.Context) {
	check := func() {
		if err := a.processPostSubmissionSync(ctx, time.Now().UTC()); err != nil && ctx.Err() == nil {
			// Database and upstream errors can contain private configuration. Only a
			// fixed diagnostic reaches logs; jobs retain fixed user-facing messages.
			log.Print("Automatic account sync could not complete; check the saved job status")
		}
	}
	check()
	ticker := time.NewTicker(time.Second)
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
