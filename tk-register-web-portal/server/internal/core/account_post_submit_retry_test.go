package core

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestPostSubmissionFiveFailedRoundsRemainTerminal(t *testing.T) {
	store, owner, vendor, version := syncFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)
	submission := syncSubmission(t, store, owner.ID, vendor.ID, version.ID, "retry@example.com", "claude/cc-pro", "accepted", "501", base)
	job := enqueuePostSubmissionFixture(t, store, submission, base)
	for attempt := 1; attempt <= 5; attempt++ {
		if ok, err := store.ClaimPostSubmissionSyncJob(ctx, job.ID, job.NextRunAt); err != nil || !ok {
			t.Fatal("due retry was not claimed", attempt, ok, err)
		}
		finish := job.NextRunAt.Add(2 * time.Second)
		if err := store.FinishPostSubmissionSyncJob(ctx, job.ID, "failed", "Supplier temporarily unavailable.", finish); err != nil {
			t.Fatal(err)
		}
		job = loadPostSubmissionJob(t, store, submission.ID)
		if job.AttemptCount != attempt || job.FailureCount != attempt || job.SuccessCount != 0 || job.LastError == "" {
			t.Fatal("incorrect consumed attempt", job)
		}
		if attempt < 5 {
			if job.Status != "pending" || job.FinishedAt != nil || !job.NextRunAt.Equal(finish.Add(10*time.Second)) {
				t.Fatal("retry was not delayed from completion", job)
			}
			if ok, err := store.ClaimPostSubmissionSyncJob(ctx, job.ID, job.NextRunAt.Add(-time.Nanosecond)); err != nil || ok {
				t.Fatal("retry claimed early", ok, err)
			}
		} else if job.Status != "failed" || job.FinishedAt == nil || !job.FinishedAt.Equal(finish) {
			t.Fatal("fifth failed read was not terminal", job)
		}
	}
	if err := store.RecoverPostSubmissionSyncJobs(ctx, base.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.ClaimPostSubmissionSyncJob(ctx, job.ID, base.Add(time.Hour)); err != nil || ok {
		t.Fatal("exhausted read restarted", ok, err)
	}
}

func TestPostSubmissionMixedRoundsHonorCooldownAndDoNotCountLocalWaits(t *testing.T) {
	store, owner, vendor, version := syncFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)
	submission := syncSubmission(t, store, owner.ID, vendor.ID, version.ID, "mixed@example.com", "claude/cc-pro", "accepted", "502", base)
	job := enqueuePostSubmissionFixture(t, store, submission, base)
	successes, failures := 0, 0
	for i, outcome := range []string{"succeeded", "failed", "succeeded", "failed", "succeeded"} {
		round := i + 1
		started := job.NextRunAt
		if ok, err := store.ClaimPostSubmissionSyncJob(ctx, job.ID, started); err != nil || !ok {
			t.Fatal("round was not claimed", round, ok, err)
		}
		if round == 3 {
			// Cached cooldown/login-lock waits return the claim without spending a round.
			until := started.Add(time.Minute)
			if err := store.DeferClaimedPostSubmissionSyncJob(ctx, job.ID, round, started, until, started, "Waiting for supplier."); err != nil {
				t.Fatal(err)
			}
			waiting := loadPostSubmissionJob(t, store, submission.ID)
			if waiting.AttemptCount != round-1 || waiting.SuccessCount != successes || waiting.FailureCount != failures || waiting.Status != "pending" || !waiting.NextRunAt.Equal(until) {
				t.Fatal("local wait consumed a round or changed prior outcomes", waiting)
			}
			if ok, err := store.ClaimPostSubmissionSyncJob(ctx, job.ID, until); err != nil || !ok {
				t.Fatal("deferred round was not claimed", ok, err)
			}
			started = until
		}
		finished := started.Add(2 * time.Second)
		retryAt := time.Time{}
		if round == 2 {
			// An actual HTTP 429 counts as a failed round and delays the next one.
			retryAt = finished.Add(time.Minute)
		}
		if err := store.FinishPostSubmissionSyncJobAfter(ctx, job.ID, outcome, "Safe supplier failure.", finished, retryAt); err != nil {
			t.Fatal(err)
		}
		if outcome == "succeeded" {
			successes++
		} else {
			failures++
		}
		job = loadPostSubmissionJob(t, store, submission.ID)
		if job.AttemptCount != round || job.SuccessCount != successes || job.FailureCount != failures {
			t.Fatal("mixed outcome counters diverged", job)
		}
		if round < 5 {
			next := finished.Add(10 * time.Second)
			if retryAt.After(next) {
				next = retryAt
			}
			if job.Status != "pending" || job.FinishedAt != nil || !job.NextRunAt.Equal(next) {
				t.Fatal("round ended early or missed completion/cooldown deadline", job)
			}
		} else if job.Status != "succeeded" || job.FinishedAt == nil || job.SuccessCount != 3 || job.FailureCount != 2 {
			t.Fatal("mixed sequence did not end on fifth result", job)
		}
	}
}

func beginAPIAuthSyncAudit(t *testing.T, store *Store, owner User, target SyncedAccount, requestID string) AccountAuthJSONAudit {
	t.Helper()
	ctx := context.Background()
	var token UserAPIToken
	if err := store.DB.Where("user_id = ?", owner.ID).First(&token).Error; errors.Is(err, ErrNotFound) {
		generated, err := store.GenerateAPIToken(ctx, owner.ID)
		if err != nil {
			t.Fatal(err)
		}
		token.ID, _ = ParseBusinessID(generated.Token.ID)
		token.Revision = generated.Token.Revision
	} else if err != nil {
		t.Fatal(err)
	}
	audit, err := store.BeginAuthJSONUpdateWithSource(ctx, target, owner.ID, requestID, "api", &token.ID, &token.Revision)
	if err != nil {
		t.Fatal(err)
	}
	return audit
}

func authSyncJob(t *testing.T, store *Store, auditID uint64) PostSubmissionSyncJob {
	t.Helper()
	var job PostSubmissionSyncJob
	if err := store.DB.Where("auth_json_audit_id = ?", auditID).First(&job).Error; err != nil {
		t.Fatal(err)
	}
	return job
}

func TestAuthJSONAPICompletionQueuesAndDuplicateDoesNotRewriteNewerMetadata(t *testing.T) {
	store, owner, target := authAuditFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)
	first := beginAPIAuthSyncAudit(t, store, owner, target, "first-update")
	if unchanged, err := store.FinishAuthJSONUpdate(ctx, first, "updated", "13152", "", base); err != nil || !unchanged {
		t.Fatal(unchanged, err)
	}
	job := authSyncJob(t, store, first.ID)
	if job.SubmissionID != *target.CurrentSubmissionID || job.SubmissionTriggerID != nil || job.AttemptCount != 0 || !job.NextRunAt.Equal(base.Add(10*time.Second)) || job.IdentityKey != target.IdentityKey {
		t.Fatal("wrong update trigger/anchor/delay", job)
	}
	// The accepted submission was created through the web; the successful API
	// credential update is sufficient to schedule its own read.
	if _, err := store.PostSubmissionSyncTarget(ctx, job); err != nil {
		t.Fatal("API update on web-created account was ineligible", err)
	}
	second := beginAPIAuthSyncAudit(t, store, owner, target, "second-update")
	if unchanged, err := store.FinishAuthJSONUpdate(ctx, second, "updated", "13152", "", base.Add(time.Minute)); err != nil || !unchanged {
		t.Fatal(unchanged, err)
	}
	if unchanged, err := store.FinishAuthJSONUpdate(ctx, first, "updated", "99999", "", base.Add(2*time.Minute)); err != nil || unchanged {
		t.Fatal("duplicate completion was applied", unchanged, err)
	}
	var account SyncedAccount
	if err := store.DB.First(&account, target.ID).Error; err != nil || account.SupplierRecordID == nil || *account.SupplierRecordID != "13152" || account.AuthJSONUpdatedAt == nil || !account.AuthJSONUpdatedAt.Equal(base.Add(time.Minute)) {
		t.Fatal("duplicate completion rewrote newer account metadata", account, err)
	}
	var count int64
	if err := store.DB.Model(&PostSubmissionSyncJob{}).Count(&count).Error; err != nil || count != 2 {
		t.Fatal("same accepted anchor did not permit exactly one job per update", count, err)
	}
	if _, err := store.PostSubmissionSyncTarget(ctx, job); !errors.Is(err, ErrPostSubmissionSyncIneligible) {
		t.Fatal("superseded update job remained eligible", err)
	}
	if _, err := store.PostSubmissionSyncTarget(ctx, authSyncJob(t, store, second.ID)); err != nil {
		t.Fatal("newest update job became ineligible", err)
	}
	// A newer successful web update supersedes a queued API read and gets its
	// own independent background sequence.
	web, err := store.BeginAuthJSONUpdate(ctx, account, owner.ID, "newer-web-update")
	if err != nil {
		t.Fatal(err)
	}
	if unchanged, err := store.FinishAuthJSONUpdate(ctx, web, "updated", "13152", "", base.Add(3*time.Minute)); err != nil || !unchanged {
		t.Fatal(unchanged, err)
	}
	if _, err := store.PostSubmissionSyncTarget(ctx, authSyncJob(t, store, second.ID)); !errors.Is(err, ErrPostSubmissionSyncIneligible) {
		t.Fatal("web update did not supersede older API job", err)
	}
	if err := store.DB.Model(&PostSubmissionSyncJob{}).Count(&count).Error; err != nil || count != 3 {
		t.Fatal("web update did not schedule its own background sequence", count, err)
	}
	if _, err := store.PostSubmissionSyncTarget(ctx, authSyncJob(t, store, web.ID)); err != nil {
		t.Fatal("newest web update job became ineligible", err)
	}
}

func TestAuthJSONWebOwnerAndAdminUpdatesQueueFiveSuccessfulRounds(t *testing.T) {
	for _, admin := range []bool{false, true} {
		t.Run(fmt.Sprintf("admin=%t", admin), func(t *testing.T) {
			store, owner, target := authAuditFixture(t)
			ctx := context.Background()
			base := time.Now().UTC().Truncate(time.Second)
			actor := owner
			if admin {
				actor = tokenUser(t, store, "web-update-admin@example.com")
				if err := store.DB.Model(&actor).Update("role", "admin").Error; err != nil {
					t.Fatal(err)
				}
			}
			audit, err := store.BeginAuthJSONUpdate(ctx, target, actor.ID, "web-five-rounds")
			if err != nil {
				t.Fatal(err)
			}
			if unchanged, err := store.FinishAuthJSONUpdate(ctx, audit, "updated", "13152", "", base); err != nil || !unchanged {
				t.Fatal("web update failed to enqueue", unchanged, err)
			}
			job := authSyncJob(t, store, audit.ID)
			if job.UserID != owner.ID || !job.NextRunAt.Equal(base.Add(10*time.Second)) {
				t.Fatal("web admin changed owner or first delay", job)
			}
			for round := 1; round <= 5; round++ {
				if _, err := store.PostSubmissionSyncTarget(ctx, job); err != nil {
					t.Fatal("confirmed web update is not eligible", err)
				}
				if claimed, err := store.ClaimPostSubmissionSyncJob(ctx, job.ID, job.NextRunAt); err != nil || !claimed {
					t.Fatal(claimed, err)
				}
				if err := store.FinishPostSubmissionSyncJob(ctx, job.ID, "succeeded", "", job.NextRunAt.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				job = authSyncJob(t, store, audit.ID)
				var saved AccountAuthJSONAudit
				if err := store.DB.First(&saved, audit.ID).Error; err != nil {
					t.Fatal(err)
				}
				want := "pending"
				if round == 5 {
					want = "succeeded"
				}
				if job.Status != want || saved.RefreshStatus != want || saved.Status != "updated" || saved.ActorID != actor.ID || saved.OwnerID != owner.ID || job.AttemptCount != round || job.SuccessCount != round || job.FailureCount != 0 {
					t.Fatal("web update sequence ended early or changed audit identity", job, saved)
				}
			}
		})
	}
}

func TestAuthJSONSyncRetryAuditAndRestartRecovery(t *testing.T) {
	for _, outcome := range []string{"failed", "succeeded", "skipped", "interrupted"} {
		t.Run(outcome, func(t *testing.T) {
			store, owner, target := authAuditFixture(t)
			ctx := context.Background()
			base := time.Now().UTC().Truncate(time.Second)
			audit := beginAPIAuthSyncAudit(t, store, owner, target, "retry-audit-"+outcome)
			if _, err := store.FinishAuthJSONUpdate(ctx, audit, "updated", "13152", "", base); err != nil {
				t.Fatal(err)
			}
			job := authSyncJob(t, store, audit.ID)
			for attempt := 1; attempt <= 5; attempt++ {
				if ok, err := store.ClaimPostSubmissionSyncJob(ctx, job.ID, job.NextRunAt); err != nil || !ok {
					t.Fatal(ok, err)
				}
				finish := job.NextRunAt.Add(time.Second)
				if outcome == "interrupted" {
					finish = job.NextRunAt.Add(121 * time.Second)
					if err := store.RecoverPostSubmissionSyncJobs(ctx, finish); err != nil {
						t.Fatal(err)
					}
				} else {
					status := "failed"
					if attempt == 5 {
						status = outcome
					}
					if err := store.FinishPostSubmissionSyncJob(ctx, job.ID, status, "Safe failure.", finish); err != nil {
						t.Fatal(err)
					}
				}
				job = authSyncJob(t, store, audit.ID)
				var saved AccountAuthJSONAudit
				if err := store.DB.First(&saved, audit.ID).Error; err != nil {
					t.Fatal(err)
				}
				wantRefresh, wantJob := "pending", "pending"
				if attempt == 5 {
					wantRefresh, wantJob = "failed", outcome
					if outcome == "succeeded" {
						wantRefresh = "succeeded"
					}
					if outcome == "interrupted" {
						wantJob = "failed"
					}
				}
				if saved.Status != "updated" || saved.RefreshStatus != wantRefresh || job.Status != wantJob || job.AttemptCount != attempt {
					t.Fatal("audit/attempt/terminal state diverged", saved, job)
				}
				if attempt < 5 && !job.NextRunAt.Equal(finish.Add(10*time.Second)) {
					t.Fatal("wrong retry deadline", job)
				}
			}
		})
	}
}

func TestAuthJSONQueueFailureRollsBackAuditAndAccountMetadata(t *testing.T) {
	store, owner, target := authAuditFixture(t)
	ctx := context.Background()
	audit := beginAPIAuthSyncAudit(t, store, owner, target, "atomic-update")
	if err := store.DB.Exec(`CREATE TRIGGER fail_auth_sync_queue BEFORE INSERT ON account_post_submit_sync_jobs BEGIN SELECT RAISE(ABORT, 'fixture queue failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishAuthJSONUpdate(ctx, audit, "updated", "13152", "", time.Now().UTC()); err == nil {
		t.Fatal("queue failure was hidden")
	}
	var saved AccountAuthJSONAudit
	var account SyncedAccount
	if err := store.DB.First(&saved, audit.ID).Error; err != nil || saved.Status != "pending" || saved.FinishedAt != nil || saved.RefreshStatus != "" {
		t.Fatal("audit escaped failed queue transaction", saved, err)
	}
	if err := store.DB.First(&account, target.ID).Error; err != nil || account.AuthJSONUpdatedAt != nil || account.SupplierRecordID != nil {
		t.Fatal("account metadata escaped failed queue transaction", account, err)
	}
}

// Keep the previously deployed GORM schema here, including associations and
// generated index names, so the upgrade test starts from a real legacy table.
type legacyPostSubmissionSyncJob struct {
	ID           uint64        `gorm:"primaryKey"`
	SubmissionID uint64        `gorm:"not null;uniqueIndex"`
	Submission   Submission    `gorm:"foreignKey:SubmissionID;constraint:OnDelete:RESTRICT"`
	AccountID    uint64        `gorm:"not null;index"`
	Account      SyncedAccount `gorm:"foreignKey:AccountID;constraint:OnDelete:RESTRICT"`
	UserID       uint64        `gorm:"not null;index"`
	User         User          `gorm:"foreignKey:UserID;constraint:OnDelete:RESTRICT"`
	IdentityKey  string        `gorm:"size:64;not null"`
	Status       string        `gorm:"size:24;not null;index:idx_post_submit_sync_due,priority:1"`
	NextRunAt    time.Time     `gorm:"not null;index:idx_post_submit_sync_due,priority:2"`
	CreatedAt    time.Time     `gorm:"not null"`
	StartedAt    *time.Time
	FinishedAt   *time.Time
	LastError    string `gorm:"size:255;not null;default:''"`
}

func (legacyPostSubmissionSyncJob) TableName() string { return "account_post_submit_sync_jobs" }

func TestPostSubmissionLegacyJobMigrationPreservesTerminalRows(t *testing.T) {
	store, owner, vendor, version := syncFixture(t)
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	var old []PostSubmissionSyncJob
	for i, status := range []string{"pending", "running", "succeeded", "failed", "skipped"} {
		submission := syncSubmission(t, store, owner.ID, vendor.ID, version.ID, fmt.Sprintf("legacy-%d@example.com", i), "claude/cc-pro", "accepted", idString(uint64(100+i)), base.Add(time.Duration(i)*time.Second))
		job := enqueuePostSubmissionFixture(t, store, submission, base)
		job.Status = status
		old = append(old, job)
	}
	if err := store.DB.Migrator().DropTable(&PostSubmissionSyncJob{}); err != nil {
		t.Fatal(err)
	}
	if err := store.DB.AutoMigrate(&legacyPostSubmissionSyncJob{}); err != nil {
		t.Fatal(err)
	}
	for _, job := range old {
		var started, finished any
		if job.Status != "pending" {
			started = base
		}
		if job.Status != "pending" && job.Status != "running" {
			finished = base.Add(time.Second)
		}
		if err := store.DB.Exec(`INSERT INTO account_post_submit_sync_jobs (id, submission_id, account_id, user_id, identity_key, status, next_run_at, created_at, started_at, finished_at, last_error) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, job.ID, job.SubmissionID, job.AccountID, job.UserID, job.IdentityKey, job.Status, job.NextRunAt, job.CreatedAt, started, finished, "old safe status").Error; err != nil {
			t.Fatal(err)
		}
	}
	for run := 0; run < 2; run++ {
		if err := store.migratePostSubmissionSyncJobs(); err != nil {
			t.Fatal("legacy schema migration failed", err)
		}
	}
	if store.DB.Migrator().HasIndex(&PostSubmissionSyncJob{}, "idx_account_post_submit_sync_jobs_submission_id") || !store.DB.Migrator().HasIndex(&PostSubmissionSyncJob{}, "idx_post_submit_sync_submission_trigger") {
		t.Fatal("old submission uniqueness was not replaced")
	}
	var jobs []PostSubmissionSyncJob
	if err := store.DB.Order("id").Find(&jobs).Error; err != nil || len(jobs) != len(old) {
		t.Fatal("migration lost or created jobs", jobs, err)
	}
	for i, job := range jobs {
		wantCount := 1
		if job.Status == "pending" {
			wantCount = 0
		}
		if job.Status != old[i].Status || job.AttemptCount != wantCount || job.SubmissionTriggerID == nil || *job.SubmissionTriggerID != job.SubmissionID || job.AuthJSONAuditID != nil || !job.NextRunAt.Equal(old[i].NextRunAt) || job.LastError != "old safe status" {
			t.Fatal("migration changed lifecycle or failed to backfill metadata", job)
		}
	}
	// Existing terminal jobs stay terminal when restart recovery is applied.
	if err := store.RecoverPostSubmissionSyncJobs(context.Background(), base.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, oldJob := range old[2:] {
		var saved PostSubmissionSyncJob
		if err := store.DB.First(&saved, oldJob.ID).Error; err != nil || saved.Status != oldJob.Status {
			t.Fatal("historical terminal job reopened", saved, err)
		}
	}
	// The trigger uniqueness still prevents an old submission from being
	// scheduled again even though submission_id is no longer globally unique.
	var submission Submission
	if err := store.DB.First(&submission, old[0].SubmissionID).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.DB.Transaction(func(tx *gorm.DB) error { return enqueuePostSubmissionSyncTx(tx, submission, base.Add(time.Hour)) }); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := store.DB.Model(&PostSubmissionSyncJob{}).Count(&count).Error; err != nil || count != int64(len(old)) {
		t.Fatal("legacy submission duplicated", count, err)
	}
}
