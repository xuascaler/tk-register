package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"goapi-tk-register/server/internal/core"
	"goapi-tk-register/server/internal/supplier"
)

func setupPostSubmissionSync(t *testing.T) (*app, core.SyncedAccount, core.PostSubmissionSyncJob, *mockPageCollector, time.Time) {
	t.Helper()
	a, _, _, _, rows, fake := setupPageSync(t)
	target := rows[0]
	var submission core.Submission
	if err := a.store.DB.First(&submission, *target.CurrentSubmissionID).Error; err != nil {
		t.Fatal(err)
	}
	// setupPageSync uses local times. SQLite compares stored datetime text when
	// ordering submissions, so keep this fixture and its later replacements in
	// UTC, like the durable worker timestamps.
	if err := a.store.DB.Model(&submission).Updates(map[string]any{"source": "api", "created_at": submission.CreatedAt.UTC(), "updated_at": submission.UpdatedAt.UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	job := core.PostSubmissionSyncJob{SubmissionID: *target.CurrentSubmissionID, SubmissionTriggerID: target.CurrentSubmissionID, AccountID: target.ID, UserID: target.UserID, IdentityKey: target.IdentityKey, Status: "pending", NextRunAt: now, CreatedAt: now.Add(-3 * time.Second)}
	if err := a.store.DB.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	return a, target, job, fake, now
}

func readPostSubmissionJob(t *testing.T, a *app, id uint64) core.PostSubmissionSyncJob {
	t.Helper()
	var job core.PostSubmissionSyncJob
	if err := a.store.DB.First(&job, id).Error; err != nil {
		t.Fatal(err)
	}
	return job
}

type postSubmissionCredentialCollector struct {
	username, password string
	delegate           accountPageCollector
}

func (c *postSubmissionCredentialCollector) CollectTargets(ctx context.Context, username, password string, targets []supplier.Target) ([]supplier.AccountResult, error) {
	c.username, c.password = username, password
	return c.delegate.CollectTargets(ctx, username, password, targets)
}

func TestPostSubmissionSyncUsesFreshSavedCredentials(t *testing.T) {
	a, target, job, fake, now := setupPostSubmissionSync(t)
	collector := &postSubmissionCredentialCollector{delegate: fake}
	a.accountPageCollector = collector
	if err := a.store.PatchSettings(context.Background(), map[string]string{"account_sync_website_username": "changed-supplier-user", "account_sync_website_password": "changed-supplier-password"}, target.UserID); err != nil {
		t.Fatal(err)
	}
	if err := a.processPostSubmissionSync(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if readPostSubmissionJob(t, a, job.ID).Status != "succeeded" || collector.username != "changed-supplier-user" || collector.password != "changed-supplier-password" {
		t.Fatal("worker did not use the current saved website login")
	}
}

func TestPostSubmissionSyncBusyDefersWithoutAttempt(t *testing.T) {
	a, target, job, fake, now := setupPostSubmissionSync(t)
	if err := a.redis.Set(context.Background(), accountSyncLeaseKey, "scheduled-sync", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if err := a.processPostSubmissionSync(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	saved := readPostSubmissionJob(t, a, job.ID)
	if saved.Status != "pending" || saved.AttemptCount != 0 || saved.StartedAt != nil || saved.FinishedAt != nil || !saved.NextRunAt.Equal(now.Add(5*time.Second)) || fake.calls != 0 {
		t.Fatalf("busy job consumed an attempt: status=%s started=%v finished=%v next=%v calls=%d", saved.Status, saved.StartedAt, saved.FinishedAt, saved.NextRunAt, fake.calls)
	}
	account, err := a.store.SyncedAccountForUser(context.Background(), target.UserID, target.ID)
	if err != nil || account.LastAttemptAt != nil {
		t.Fatal("busy lease changed account attempt", err)
	}
	if a.redis.Get(context.Background(), accountSyncLeaseKey).Val() != "scheduled-sync" {
		t.Fatal("busy job released another worker's lease")
	}
	a.redis.Del(context.Background(), accountSyncLeaseKey)
	if err = a.processPostSubmissionSync(context.Background(), now.Add(4*time.Second)); err != nil || fake.calls != 0 {
		t.Fatal("deferred job ran early", err)
	}
	if err = a.processPostSubmissionSync(context.Background(), now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if readPostSubmissionJob(t, a, job.ID).Status != "succeeded" || fake.calls != 1 {
		t.Fatal("deferred job did not run once")
	}
}

func TestPostSubmissionSyncOneTargetOnceAndIndependentSchedule(t *testing.T) {
	a, target, job, fake, now := setupPostSubmissionSync(t)
	at := now.Add(-time.Hour)
	state := core.AccountSyncState{ID: 1, Status: "succeeded", LastStartedAt: &at, LastFinishedAt: &at, LastSuccessAt: &at}
	if err := a.store.DB.Save(&state).Error; err != nil {
		t.Fatal(err)
	}
	full := &mockAccountCollector{}
	a.accountCollector = full
	fake.run = func(ctx context.Context, targets []supplier.Target) ([]supplier.AccountResult, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 40*time.Second || len(targets) != 1 || targets[0].Account != target.Account {
			t.Fatal("collection is not bounded to this account")
		}
		if a.redis.Exists(ctx, accountSyncLeaseKey).Val() != 1 {
			t.Fatal("collector did not hold the shared sync lease")
		}
		return []supplier.AccountResult{pageRecord(targets[0])}, nil
	}
	for _, when := range []time.Time{now, now.Add(time.Hour)} {
		if err := a.processPostSubmissionSync(context.Background(), when); err != nil {
			t.Fatal(err)
		}
	}
	saved := readPostSubmissionJob(t, a, job.ID)
	if saved.Status != "succeeded" || saved.StartedAt == nil || saved.FinishedAt == nil || saved.LastError != "" || fake.calls != 1 || full.collectCalls != 0 {
		t.Fatalf("job was not completed exactly once: status=%s started=%v finished=%v calls=%d full_calls=%d", saved.Status, saved.StartedAt, saved.FinishedAt, fake.calls, full.collectCalls)
	}
	account, err := a.store.SyncedAccountForUser(context.Background(), target.UserID, target.ID)
	if err != nil || account.Snapshot == nil || *account.Snapshot.CostTotal != 123.45 || account.SupplierRecordID == nil || *account.SupplierRecordID != "450001" || account.LastSuccessAt == nil {
		t.Fatalf("snapshot is not visible in account reads: error=%v snapshot=%t confirmed=%v success=%v", err, account.Snapshot != nil, account.SupplierRecordID, account.LastSuccessAt)
	}
	var after core.AccountSyncState
	if err = a.store.DB.First(&after, 1).Error; err != nil || !reflect.DeepEqual(state, after) {
		t.Fatal("post-submission job changed scheduled sync state", err)
	}
	if a.redis.Exists(context.Background(), accountSyncLeaseKey).Val() != 0 {
		t.Fatal("lease leaked")
	}
}

func TestPostSubmissionSyncFailuresRetainLastSnapshot(t *testing.T) {
	for _, scenario := range []string{"login", "missing", "duplicate", "account", "plan", "pinned", "invalid-usage"} {
		t.Run(scenario, func(t *testing.T) {
			a, target, job, fake, now := setupPostSubmissionSync(t)
			at, cost, currency := now.Add(-time.Hour), 8.0, "CNY"
			if err := a.store.RecordAccountSnapshot(context.Background(), target.ID, target.IdentityKey, core.AccountSnapshotValues{CostTotal: &cost, Currency: &currency}, at, "Asia/Shanghai"); err != nil {
				t.Fatal(err)
			}
			if scenario == "pinned" {
				if err := a.store.ConfirmSupplierRecord(context.Background(), target.ID, target.IdentityKey, "450123"); err != nil {
					t.Fatal(err)
				}
			}
			fake.run = func(ctx context.Context, targets []supplier.Target) ([]supplier.AccountResult, error) {
				record := pageRecord(targets[0])
				switch scenario {
				case "login":
					return nil, &supplier.Error{Code: supplier.CodeCredentialsInvalid, Message: "private-access-token"}
				case "missing":
					return nil, nil
				case "duplicate":
					return []supplier.AccountResult{record, record}, nil
				case "account":
					record.Account.Account = "wrong@example.com"
				case "plan":
					record.Account.Plan = "codex/pro"
				case "pinned":
					if targets[0].RecordID != "450123" {
						t.Fatal("known website row ID was not used")
					}
				case "invalid-usage":
					invalid := -1.0
					record.Account.CostTotal = &invalid
				}
				return []supplier.AccountResult{record}, nil
			}
			for attempt := 1; attempt <= 4; attempt++ {
				before := time.Now().UTC()
				if err := a.processPostSubmissionSync(context.Background(), readPostSubmissionJob(t, a, job.ID).NextRunAt); err != nil {
					t.Fatal(err)
				}
				saved := readPostSubmissionJob(t, a, job.ID)
				if saved.AttemptCount != attempt || fake.calls != attempt {
					t.Fatalf("retry budget mismatch: attempts=%d calls=%d expected=%d", saved.AttemptCount, fake.calls, attempt)
				}
				if attempt < 4 {
					if saved.Status != "pending" || saved.FinishedAt != nil || saved.NextRunAt.Before(before.Add(3*time.Second)) || saved.NextRunAt.After(time.Now().UTC().Add(3*time.Second)) {
						t.Fatalf("retry was not persisted for three seconds later: status=%s next=%v", saved.Status, saved.NextRunAt)
					}
					if err := a.processPostSubmissionSync(context.Background(), saved.NextRunAt.Add(-time.Millisecond)); err != nil || fake.calls != attempt {
						t.Fatal("retry ran before its due time", err)
					}
				}
			}
			if err := a.processPostSubmissionSync(context.Background(), now.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			saved := readPostSubmissionJob(t, a, job.ID)
			account, err := a.store.SyncedAccountForUser(context.Background(), target.UserID, target.ID)
			if err != nil || saved.Status != "failed" || saved.FinishedAt == nil || saved.LastError == "" || fake.calls != 4 || account.LastAttemptAt == nil || !account.LastAttemptAt.After(at) || !account.LastSuccessAt.Equal(at) || account.Snapshot == nil || *account.Snapshot.CostTotal != cost {
				t.Fatalf("exhausted retries changed saved observation or exceeded cap: error=%v status=%s calls=%d snapshot=%t attempt=%v success=%v", err, saved.Status, fake.calls, account.Snapshot != nil, account.LastAttemptAt, account.LastSuccessAt)
			}
			assertNoSecrets(t, saved.LastError+account.LastError)
			if scenario == "pinned" && (account.SupplierRecordID == nil || *account.SupplierRecordID != "450123") {
				t.Fatal("mismatched supplier result replaced the pinned row")
			}
		})
	}
}

func TestPostSubmissionSyncChecksEligibilityBeforeAndAfterQuery(t *testing.T) {
	for _, scenario := range []string{"new-before", "new-after", "same-identity-after", "disabled-user", "disabled-vendor", "unsupported-plan", "disabled-user-after", "disabled-vendor-after", "missing-login"} {
		t.Run(scenario, func(t *testing.T) {
			a, target, job, fake, now := setupPostSubmissionSync(t)
			replaceSubmission := func(sameIdentity bool) {
				var sub core.Submission
				if err := a.store.DB.First(&sub, job.SubmissionID).Error; err != nil {
					t.Fatal(err)
				}
				sub.ID, sub.ClientRequestKey = 0, "new-post-submission-lifetime"
				sub.CreatedAt, sub.UpdatedAt = now.Add(time.Second), now.Add(time.Second)
				if !sameIdentity {
					remote := "999999"
					sub.RemoteID = &remote
				}
				if err := a.store.DB.Create(&sub).Error; err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "new-before":
				replaceSubmission(false)
			case "disabled-user":
				a.store.DB.Model(&core.User{}).Where("id = ?", target.UserID).Update("status", "disabled")
			case "disabled-vendor":
				a.store.DB.Model(&core.Vendor{}).Where("id = ?", target.VendorID).Update("enabled", false)
			case "unsupported-plan":
				a.store.DB.Model(&core.Submission{}).Where("id = ?", job.SubmissionID).Update("plan", "codex/pro")
			case "missing-login":
				a.store.DB.Model(&core.Setting{}).Where("key = ?", "account_sync_website_password").Update("value", "")
			}
			fake.run = func(ctx context.Context, targets []supplier.Target) ([]supplier.AccountResult, error) {
				if scenario == "new-after" || scenario == "same-identity-after" {
					replaceSubmission(scenario == "same-identity-after")
				}
				if scenario == "disabled-user-after" {
					a.store.DB.Model(&core.User{}).Where("id = ?", target.UserID).Update("status", "disabled")
				}
				if scenario == "disabled-vendor-after" {
					a.store.DB.Model(&core.Vendor{}).Where("id = ?", target.VendorID).Update("enabled", false)
				}
				return []supplier.AccountResult{pageRecord(targets[0])}, nil
			}
			if err := a.processPostSubmissionSync(context.Background(), now); err != nil {
				t.Fatal(err)
			}
			wantStatus, wantCalls := "skipped", 0
			if scenario == "missing-login" {
				wantStatus = "pending"
			}
			if strings.HasSuffix(scenario, "after") {
				wantCalls = 1
			}
			saved := readPostSubmissionJob(t, a, job.ID)
			var snapshots int64
			a.store.DB.Model(&core.AccountSnapshot{}).Where("account_id = ?", target.ID).Count(&snapshots)
			var current core.SyncedAccount
			a.store.DB.First(&current, target.ID)
			if saved.Status != wantStatus || saved.LastError == "" || fake.calls != wantCalls || snapshots != 0 || current.LastAttemptAt != nil || current.SupplierRecordID != nil {
				t.Fatalf("ineligible job wrote account data: status=%s want=%s calls=%d want=%d snapshots=%d current_submission=%v attempt=%v confirmed=%v", saved.Status, wantStatus, fake.calls, wantCalls, snapshots, current.CurrentSubmissionID, current.LastAttemptAt, current.SupplierRecordID)
			}
		})
	}
}

func TestPostSubmissionSyncRecoversStaleRunningWithRemainingBudget(t *testing.T) {
	a, _, job, fake, now := setupPostSubmissionSync(t)
	started := now.Add(-3 * time.Minute)
	if err := a.store.DB.Model(&job).Updates(map[string]any{"status": "running", "started_at": started, "attempt_count": 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.processPostSubmissionSync(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	saved := readPostSubmissionJob(t, a, job.ID)
	if saved.Status != "pending" || saved.AttemptCount != 1 || saved.FinishedAt != nil || saved.LastError == "" || fake.calls != 0 || !saved.NextRunAt.Equal(now.Add(3*time.Second)) {
		t.Fatalf("interrupted read lost its bounded retry: status=%s attempts=%d next=%v calls=%d", saved.Status, saved.AttemptCount, saved.NextRunAt, fake.calls)
	}
	if err := a.processPostSubmissionSync(context.Background(), saved.NextRunAt); err != nil || fake.calls != 1 || readPostSubmissionJob(t, a, job.ID).Status != "succeeded" || readPostSubmissionJob(t, a, job.ID).AttemptCount != 2 {
		t.Fatal("recovered read did not consume the next attempt", err)
	}
}

func TestPostSubmissionSyncShutdownCancelsAndJoins(t *testing.T) {
	a, _, job, fake, _ := setupPostSubmissionSync(t)
	started := make(chan struct{})
	fake.run = func(ctx context.Context, targets []supplier.Target) ([]supplier.AccountResult, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); a.runPostSubmissionSync(ctx) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not pick up durable pending job")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not join after cancellation")
	}
	if saved := readPostSubmissionJob(t, a, job.ID); saved.Status != "pending" || saved.FinishedAt != nil || saved.AttemptCount != 1 || fake.calls != 1 {
		t.Fatalf("cancelled read did not preserve its bounded retry: status=%s attempts=%d calls=%d", saved.Status, saved.AttemptCount, fake.calls)
	}
	if a.redis.Exists(context.Background(), accountSyncLeaseKey).Val() != 0 {
		t.Fatal("cancelled worker leaked shared lease")
	}
}

func TestPostSubmissionSyncCancelledPendingJobStaysPending(t *testing.T) {
	a, _, job, fake, now := setupPostSubmissionSync(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.processPostSubmissionSync(ctx, now); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled poll did not stop", err)
	}
	if saved := readPostSubmissionJob(t, a, job.ID); saved.Status != "pending" || saved.StartedAt != nil || fake.calls != 0 {
		t.Fatalf("cancelled poll consumed pending work: status=%s started=%v calls=%d", saved.Status, saved.StartedAt, fake.calls)
	}
}

func TestPostSubmissionSyncReleasePreservesReplacedLease(t *testing.T) {
	a, _, _, fake, now := setupPostSubmissionSync(t)
	fake.run = func(ctx context.Context, targets []supplier.Target) ([]supplier.AccountResult, error) {
		if err := a.redis.Set(ctx, accountSyncLeaseKey, "replacement-worker", time.Minute).Err(); err != nil {
			t.Fatal(err)
		}
		return []supplier.AccountResult{pageRecord(targets[0])}, nil
	}
	if err := a.processPostSubmissionSync(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if a.redis.Get(context.Background(), accountSyncLeaseKey).Val() != "replacement-worker" {
		t.Fatal("worker released a lease owned by someone else")
	}
}
