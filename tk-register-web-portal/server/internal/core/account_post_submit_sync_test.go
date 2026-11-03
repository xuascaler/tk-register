package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
)

func loadPostSubmissionJob(t *testing.T, store *Store, submissionID uint64) PostSubmissionSyncJob {
	t.Helper()
	var job PostSubmissionSyncJob
	if err := store.DB.Where("submission_id = ?", submissionID).First(&job).Error; err != nil {
		t.Fatal(err)
	}
	return job
}

func enqueuePostSubmissionFixture(t *testing.T, store *Store, submission Submission, at time.Time) PostSubmissionSyncJob {
	t.Helper()
	submission.Source = "api"
	if err := store.DB.Model(&submission).Update("source", "api").Error; err != nil {
		t.Fatal(err)
	}
	if err := store.DB.Transaction(func(tx *gorm.DB) error { return enqueuePostSubmissionSyncTx(tx, submission, at) }); err != nil {
		t.Fatal(err)
	}
	return loadPostSubmissionJob(t, store, submission.ID)
}

func TestPostSubmissionAcceptedAPIEnqueuesExactlyOnceAfterThreeSeconds(t *testing.T) {
	service, owner, tokenID, params := apiExecutionFixture(t)
	ctx := context.Background()
	calls := 0
	body := `{"id":13152,"alias":"test account","opaque":9007199254740993}`
	service.Transport = storageTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return apiRemoteResponse(201, body), nil
	})
	response, err := service.CreateAPI(ctx, owner.ID, tokenID, params)
	if err != nil {
		t.Fatal(err)
	}
	if response.HTTPStatus != 201 || string(response.Body) != body || calls != 1 {
		t.Fatal("API passthrough changed or follow-up ran inline", response, calls)
	}
	submissionID, _ := ParseBusinessID(response.SubmissionID)
	job := loadPostSubmissionJob(t, service.Store, submissionID)
	var execution ProtocolExecution
	if err = service.Store.DB.Where("submission_id = ?", submissionID).First(&execution).Error; err != nil {
		t.Fatal(err)
	}
	if execution.FinishedAt == nil || !job.CreatedAt.Equal(*execution.FinishedAt) || !job.NextRunAt.Equal(execution.FinishedAt.Add(3*time.Second)) || job.Status != "pending" || job.StartedAt != nil || job.UserID != owner.ID || job.AttemptCount != 0 || job.SubmissionTriggerID == nil || *job.SubmissionTriggerID != submissionID || job.AuthJSONAuditID != nil {
		t.Fatal("wrong durable scheduling metadata", job)
	}
	account, err := service.Store.PostSubmissionSyncTarget(ctx, job)
	if err != nil || account.UserID != owner.ID || account.CurrentSubmissionID == nil || *account.CurrentSubmissionID != submissionID || account.IdentityKey != job.IdentityKey || account.LastAttemptAt != nil {
		t.Fatal("account was not materialized atomically for the owner", account, err)
	}
	if due, err := service.Store.NextPostSubmissionSyncJob(ctx, job.NextRunAt.Add(-time.Nanosecond)); err != nil || due != nil {
		t.Fatal("job ran before its three second delay", due, err)
	}
	if due, err := service.Store.NextPostSubmissionSyncJob(ctx, job.NextRunAt); err != nil || due == nil || due.ID != job.ID {
		t.Fatal("job was not due at three seconds", due, err)
	}
	var submission Submission
	if err = service.Store.DB.First(&submission, submissionID).Error; err != nil {
		t.Fatal(err)
	}
	// Duplicate completion and duplicate enqueue must preserve the original job.
	if _, err = service.finish(owner.ID, submission, execution, executionResult{status: "succeeded", remoteID: submission.RemoteID}, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err = service.Store.DB.Transaction(func(tx *gorm.DB) error {
		return enqueuePostSubmissionSyncTx(tx, submission, job.CreatedAt.Add(time.Hour))
	}); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err = service.Store.DB.Model(&PostSubmissionSyncJob{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("completion generated duplicate jobs", count, err)
	}
	again := loadPostSubmissionJob(t, service.Store, submissionID)
	if again.ID != job.ID || !again.NextRunAt.Equal(job.NextRunAt) {
		t.Fatal("duplicate enqueue rescheduled the job", again)
	}
}

func TestPostSubmissionOnlyAcceptedAPIPushEnqueues(t *testing.T) {
	for _, tc := range []struct {
		name, source, body string
		status             int
	}{
		{"web accepted", "web", `{"id":13152}`, 201},
		{"API rejected", "api", `{"error":"invalid parameters"}`, 400},
		{"API ambiguous", "api", `{"error":"unavailable"}`, 503},
		{"API unrecognized success", "api", `{"accepted":true}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, owner, tokenID, params := apiExecutionFixture(t)
			service.Transport = storageTransport(func(*http.Request) (*http.Response, error) { return apiRemoteResponse(tc.status, tc.body), nil })
			var err error
			if tc.source == "api" {
				_, err = service.CreateAPI(context.Background(), owner.ID, tokenID, params)
			} else {
				vendor, version, lookupErr := service.currentVersion(context.Background(), 1, "submit_account")
				if lookupErr != nil {
					t.Fatal(lookupErr)
				}
				_, err = service.Create(context.Background(), owner.ID, CreateSubmissionInput{VendorID: idString(vendor.ID), ProtocolVersionID: idString(version.ID), ClientRequestKey: "web-not-scheduled-0001", Parameters: params})
			}
			if err != nil {
				t.Fatal(err)
			}
			var count int64
			if err = service.Store.DB.Model(&PostSubmissionSyncJob{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatal("ineligible result scheduled supplier work", count, err)
			}
		})
	}
}

func TestPostSubmissionEnqueueFailureRollsBackAcceptanceAndAccount(t *testing.T) {
	service, owner, tokenID, params := apiExecutionFixture(t)
	if err := service.Store.DB.Exec(`CREATE TRIGGER reject_post_submit_job BEFORE INSERT ON account_post_submit_sync_jobs BEGIN SELECT RAISE(ABORT, 'fixture job failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	calls := 0
	service.Transport = storageTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return apiRemoteResponse(201, `{"id":13152}`), nil
	})
	response, err := service.CreateAPI(context.Background(), owner.ID, tokenID, params)
	if err == nil || response.HTTPStatus != 201 || string(response.Body) != `{"id":13152}` || calls != 1 {
		t.Fatal("persistence failure lost passthrough or resent the push", response, calls, err)
	}
	var submission Submission
	if err = service.Store.DB.First(&submission, response.SubmissionID).Error; err != nil || submission.Status != "running" {
		t.Fatal("accepted status escaped failed job transaction", submission, err)
	}
	var execution ProtocolExecution
	if err = service.Store.DB.Where("submission_id = ?", submission.ID).First(&execution).Error; err != nil || execution.Status != "running" || execution.FinishedAt != nil {
		t.Fatal("execution completion escaped failed transaction", execution, err)
	}
	for _, model := range []any{&SyncedAccount{}, &PostSubmissionSyncJob{}} {
		var count int64
		if err = service.Store.DB.Model(model).Count(&count).Error; err != nil || count != 0 {
			t.Fatal("account/job escaped failed transaction", count, err)
		}
	}
}

func TestPostSubmissionWebAcceptanceMaterializesWithoutEnqueue(t *testing.T) {
	service, owner, _, params := apiExecutionFixture(t)
	ctx := context.Background()
	vendor, version, err := service.currentVersion(ctx, 1, "submit_account")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Hour)
	old := syncSubmission(t, service.Store, owner.ID, vendor.ID, version.ID, params["account"].(string), params["plan"].(string), "accepted", "10000", base)
	oldJob := enqueuePostSubmissionFixture(t, service.Store, old, base)
	calls := 0
	service.Transport = storageTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return apiRemoteResponse(201, `{"id":13152}`), nil
	})
	detail, err := service.Create(ctx, owner.ID, CreateSubmissionInput{VendorID: idString(vendor.ID), ProtocolVersionID: idString(version.ID), ClientRequestKey: "web-materialized-no-job-0001", Parameters: params})
	if err != nil {
		t.Fatal(err)
	}
	newID, _ := ParseBusinessID(detail.Submission.ID)
	var account SyncedAccount
	if err := service.Store.DB.First(&account, oldJob.AccountID).Error; err != nil || account.CurrentSubmissionID == nil || *account.CurrentSubmissionID != newID || account.IdentityKey == oldJob.IdentityKey || account.SubmissionStatus != "accepted" {
		t.Fatal("web acceptance did not atomically advance the stable account", account, err)
	}
	var count int64
	if err := service.Store.DB.Model(&PostSubmissionSyncJob{}).Count(&count).Error; err != nil || count != 1 || calls != 1 {
		t.Fatal("web acceptance enqueued work or called supplier twice", count, calls, err)
	}
	if _, err := service.Store.PostSubmissionSyncTarget(ctx, oldJob); !errors.Is(err, ErrPostSubmissionSyncIneligible) {
		t.Fatal("old API job remained eligible after committed web acceptance", err)
	}
}

func TestPostSubmissionSuccessfulQueryDoesNotEnqueueHistoricalAPI(t *testing.T) {
	store, owner, vendor, version := syncFixture(t)
	submission := syncSubmission(t, store, owner.ID, vendor.ID, version.ID, "historical-query@example.com", "claude/cc-pro", "accepted", "13152", time.Now().UTC().Add(-time.Hour))
	if err := store.DB.Model(&submission).Update("source", "api").Error; err != nil {
		t.Fatal(err)
	}
	execution := ProtocolExecution{SubmissionID: submission.ID, ProtocolVersionID: version.ID, RequestID: "historical-query-execution", Operation: "query", Status: "running", RequestJSON: "{}"}
	if err := store.DB.Create(&execution).Error; err != nil {
		t.Fatal(err)
	}
	service := VendorService{Store: store}
	if _, err := service.finish(owner.ID, submission, execution, executionResult{status: "succeeded"}, nil, ""); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := store.DB.Model(&PostSubmissionSyncJob{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("a successful read scheduled a new follow-up", count, err)
	}
}

func TestPostSubmissionClaimDeferralAndTerminalOutcomes(t *testing.T) {
	store, owner, vendor, version := syncFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)
	submission := syncSubmission(t, store, owner.ID, vendor.ID, version.ID, "queued@example.com", "claude/cc-pro", "accepted", "13152", base)
	job := enqueuePostSubmissionFixture(t, store, submission, base)
	if claimed, err := store.ClaimPostSubmissionSyncJob(ctx, job.ID, base); err != nil || claimed {
		t.Fatal("future job claimed", claimed, err)
	}
	if err := store.FinishPostSubmissionSyncJob(ctx, job.ID, "failed", "Should not apply.", base); err != nil {
		t.Fatal(err)
	}
	if err := store.DeferPostSubmissionSyncJob(ctx, job.ID, job.NextRunAt); err != nil {
		t.Fatal(err)
	}
	deferred := loadPostSubmissionJob(t, store, submission.ID)
	if deferred.Status != "pending" || deferred.StartedAt != nil || deferred.FinishedAt != nil || deferred.LastError != "" || deferred.AttemptCount != 0 || !deferred.NextRunAt.Equal(job.NextRunAt.Add(5*time.Second)) {
		t.Fatal("busy lease consumed attempt or used wrong delay", deferred)
	}
	if claimed, err := store.ClaimPostSubmissionSyncJob(ctx, job.ID, deferred.NextRunAt.Add(-time.Nanosecond)); err != nil || claimed {
		t.Fatal("deferred job claimed early", claimed, err)
	}
	var winners atomic.Int32
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			claimed, err := store.ClaimPostSubmissionSyncJob(ctx, job.ID, deferred.NextRunAt)
			if err != nil {
				t.Error(err)
			} else if claimed {
				winners.Add(1)
			}
		}()
	}
	workers.Wait()
	if winners.Load() != 1 {
		t.Fatal("job consumed by multiple workers", winners.Load())
	}
	if err := store.DeferPostSubmissionSyncJob(ctx, job.ID, deferred.NextRunAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishPostSubmissionSyncJob(ctx, job.ID, "succeeded", "must be cleared", deferred.NextRunAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishPostSubmissionSyncJob(ctx, job.ID, "failed", "must not replace success", deferred.NextRunAt.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	finished := loadPostSubmissionJob(t, store, submission.ID)
	if finished.Status != "succeeded" || finished.LastError != "" || !finished.NextRunAt.Equal(deferred.NextRunAt) || finished.FinishedAt == nil || finished.AttemptCount != 1 {
		t.Fatal("terminal result mutated or running job deferred", finished)
	}
	if claimed, err := store.ClaimPostSubmissionSyncJob(ctx, job.ID, base.Add(time.Hour)); err != nil || claimed {
		t.Fatal("terminal job retried", claimed, err)
	}
	if err := store.FinishPostSubmissionSyncJob(ctx, job.ID, "pending", "", base); !IsValidationError(err) {
		t.Fatal("retry status accepted", err)
	}
}

func TestPostSubmissionTargetPreservesOwnerAndRejectsSupersededIdentity(t *testing.T) {
	store, owner, vendor, version := syncFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)
	old := syncSubmission(t, store, owner.ID, vendor.ID, version.ID, " SAME@Example.com ", "claude/cc-pro", "accepted", "13152", base)
	if err := store.EnsureSyncedAccounts(ctx); err != nil {
		t.Fatal(err)
	}
	var previous SyncedAccount
	if err := store.DB.Where("current_submission_id = ?", old.ID).First(&previous).Error; err != nil {
		t.Fatal(err)
	}
	cost := float64(2)
	if err := store.RecordAccountSnapshot(ctx, previous.ID, previous.IdentityKey, AccountSnapshotValues{CostTotal: &cost}, base.Add(time.Second), "UTC"); err != nil {
		t.Fatal(err)
	}
	other := User{Email: "post-submit-other@example.com", PasswordHash: "fake", Role: "member", Status: "active", EmailVerifiedAt: base}
	if err := store.CreateUser(ctx, &other); err != nil {
		t.Fatal(err)
	}
	syncSubmission(t, store, other.ID, vendor.ID, version.ID, "same@example.com", "claude/cc-pro", "accepted", "999", base.Add(time.Minute))
	current := syncSubmission(t, store, owner.ID, vendor.ID, version.ID, "same@example.com", "claude/cc-pro", "accepted", "13152", base.Add(2*time.Minute))
	job := enqueuePostSubmissionFixture(t, store, current, base.Add(2*time.Minute))
	target, err := store.PostSubmissionSyncTarget(ctx, job)
	if err != nil || target.ID != previous.ID || target.IdentityKey != previous.IdentityKey || target.UserID != owner.ID || target.LastSuccessAt == nil {
		t.Fatal("stable lifetime or ownership changed", target, err)
	}
	for _, altered := range []PostSubmissionSyncJob{
		{SubmissionID: job.SubmissionID, AccountID: job.AccountID, UserID: other.ID, IdentityKey: job.IdentityKey},
		{SubmissionID: job.SubmissionID, AccountID: job.AccountID + 100, UserID: job.UserID, IdentityKey: job.IdentityKey},
		{SubmissionID: job.SubmissionID, AccountID: job.AccountID, UserID: job.UserID, IdentityKey: "wrong"},
	} {
		if _, err := store.PostSubmissionSyncTarget(ctx, altered); !errors.Is(err, ErrPostSubmissionSyncIneligible) {
			t.Fatal("altered ownership/identity accepted", altered, err)
		}
	}
	// A newer web acceptance with the same remote ID preserves the lifetime but
	// must still invalidate the old API job's exact current-submission guard.
	newer := syncSubmission(t, store, owner.ID, vendor.ID, version.ID, "same@example.com", "claude/cc-pro", "accepted", "13152", base.Add(3*time.Minute))
	if _, err := store.PostSubmissionSyncTarget(ctx, job); !errors.Is(err, ErrPostSubmissionSyncIneligible) {
		t.Fatal("superseded exact submission remained eligible", err)
	}
	var after SyncedAccount
	if err := store.DB.First(&after, previous.ID).Error; err != nil || after.CurrentSubmissionID == nil || *after.CurrentSubmissionID != newer.ID || after.IdentityKey != previous.IdentityKey {
		t.Fatal("stale-job check rolled back latest materialization", after, err)
	}
	newLifetime := syncSubmission(t, store, owner.ID, vendor.ID, version.ID, "same@example.com", "claude/cc-pro", "accepted", "88888", base.Add(4*time.Minute))
	newJob := enqueuePostSubmissionFixture(t, store, newLifetime, base.Add(4*time.Minute))
	if newJob.AccountID != previous.ID || newJob.IdentityKey == previous.IdentityKey {
		t.Fatal("new lifetime reused old identity", newJob)
	}
	if err := store.RecordAccountSnapshot(ctx, previous.ID, job.IdentityKey, AccountSnapshotValues{CostTotal: &cost}, time.Now().UTC(), "UTC"); !errors.Is(err, ErrAccountIdentityChanged) {
		t.Fatal("stale snapshot overwrote new lifetime", err)
	}
	if err := store.DB.Model(&owner).Update("status", "disabled").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store.PostSubmissionSyncTarget(ctx, newJob); !errors.Is(err, ErrPostSubmissionSyncIneligible) {
		t.Fatal("disabled owner remained eligible", err)
	}
	if err := store.DB.Model(&owner).Update("status", "active").Error; err != nil {
		t.Fatal(err)
	}
	if err := store.DB.Model(&vendor).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store.PostSubmissionSyncTarget(ctx, newJob); !errors.Is(err, ErrPostSubmissionSyncIneligible) {
		t.Fatal("disabled vendor remained eligible", err)
	}
}

func TestPostSubmissionRestartPreservesPendingWithoutHistoricalBackfill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "post-submit-restart.sqlite")
	store, err := Open("sqlite", path, testKey)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	owner := User{Email: "post-submit-restart@example.com", PasswordHash: "fake", Role: "member", Status: "active", EmailVerifiedAt: base}
	if err := store.CreateUser(ctx, &owner); err != nil {
		t.Fatal(err)
	}
	var vendor Vendor
	var version ProtocolVersion
	if err := store.DB.First(&vendor).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.DB.First(&version).Error; err != nil {
		t.Fatal(err)
	}
	var jobs []PostSubmissionSyncJob
	for i := 0; i < 4; i++ {
		submission := syncSubmission(t, store, owner.ID, vendor.ID, version.ID, fmt.Sprintf("restart-%d@example.com", i), "claude/cc-pro", "accepted", idString(uint64(100+i)), base.Add(time.Duration(i)*time.Second))
		if i == 0 {
			// A historical API success must never become a new follow-up job.
			if err := store.DB.Model(&submission).Update("source", "api").Error; err != nil {
				t.Fatal(err)
			}
			continue
		}
		jobs = append(jobs, enqueuePostSubmissionFixture(t, store, submission, base))
	}
	if ok, err := store.ClaimPostSubmissionSyncJob(ctx, jobs[1].ID, base.Add(3*time.Second)); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, err := store.ClaimPostSubmissionSyncJob(ctx, jobs[2].ID, base.Add(time.Minute)); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open("sqlite", path, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.RecoverPostSubmissionSyncJobs(ctx, base.Add(124*time.Second)); err != nil {
		t.Fatal(err)
	}
	var saved []PostSubmissionSyncJob
	if err := store.DB.Order("id ASC").Find(&saved).Error; err != nil || len(saved) != 3 {
		t.Fatal("migration created historical jobs or lost pending work", len(saved), err)
	}
	if saved[0].Status != "pending" || saved[0].StartedAt != nil || !saved[0].NextRunAt.Equal(jobs[0].NextRunAt) || saved[1].Status != "pending" || saved[1].FinishedAt != nil || saved[1].LastError == "" || saved[1].AttemptCount != 1 || !saved[1].NextRunAt.Equal(base.Add(127*time.Second)) || saved[2].Status != "running" {
		t.Fatal("restart lost schedule, retry budget, or interrupted-read delay", saved)
	}
	if next, err := store.NextPostSubmissionSyncJob(ctx, base.Add(time.Hour)); err != nil || next == nil || next.ID != jobs[0].ID {
		t.Fatal("pending work did not survive restart", next, err)
	}
	if ok, err := store.ClaimPostSubmissionSyncJob(ctx, jobs[1].ID, base.Add(127*time.Second)); err != nil || !ok {
		t.Fatal("recovered read could not consume its remaining retry budget", ok, err)
	}
}
