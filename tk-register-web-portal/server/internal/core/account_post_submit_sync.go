package core

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PostSubmissionSyncJob schedules one read after a newly accepted API push.
// It stores identity and timing metadata only, never account credentials or
// upstream response bodies. Migration deliberately does not backfill old pushes.
type PostSubmissionSyncJob struct {
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

func (PostSubmissionSyncJob) TableName() string { return "account_post_submit_sync_jobs" }

var ErrPostSubmissionSyncIneligible = errors.New("post-submission sync target is no longer eligible")

func enqueuePostSubmissionSyncTx(tx *gorm.DB, submission Submission, at time.Time) error {
	if submission.Status != "accepted" || submission.RemoteID == nil || strings.TrimSpace(*submission.RemoteID) == "" {
		return nil
	}
	// Every successful push, including web submissions, changes the accepted
	// lifetime under the same account row lock used by result persistence. Only
	// API submissions schedule work; web submissions perform no supplier read.
	account, err := ensureSyncedAccountForSubmissionTx(tx, submission)
	if err != nil {
		return err
	}
	if submission.Source != "api" {
		return nil
	}
	job := PostSubmissionSyncJob{SubmissionID: submission.ID, AccountID: account.ID, UserID: submission.UserID, IdentityKey: account.IdentityKey, Status: "pending", CreatedAt: at.UTC(), NextRunAt: at.UTC().Add(3 * time.Second)}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&job).Error
}

// NextPostSubmissionSyncJob is a read only peek. The worker must hold the shared
// supplier lease before claiming; contention postpones pending work by 5s.
func (s *Store) NextPostSubmissionSyncJob(ctx context.Context, now time.Time) (*PostSubmissionSyncJob, error) {
	var job PostSubmissionSyncJob
	err := s.DB.WithContext(ctx).Where("status = ? AND next_run_at <= ?", "pending", now.UTC()).Order("next_run_at ASC, id ASC").First(&job).Error
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &job, nil
}

func (s *Store) DeferPostSubmissionSyncJob(ctx context.Context, id uint64, now time.Time) error {
	return s.DB.WithContext(ctx).Model(&PostSubmissionSyncJob{}).Where("id = ? AND status = ? AND next_run_at <= ?", id, "pending", now.UTC()).Update("next_run_at", now.UTC().Add(5*time.Second)).Error
}

// Claim is conditional so concurrent workers cannot consume the same job.
// A running or terminal job is never made pending again.
func (s *Store) ClaimPostSubmissionSyncJob(ctx context.Context, id uint64, now time.Time) (bool, error) {
	result := s.DB.WithContext(ctx).Model(&PostSubmissionSyncJob{}).Where("id = ? AND status = ? AND next_run_at <= ?", id, "pending", now.UTC()).Updates(map[string]any{"status": "running", "started_at": now.UTC(), "last_error": ""})
	return result.RowsAffected == 1, result.Error
}

// PostSubmissionSyncTarget rechecks the trusted owner and current accepted API
// submission before a supplier read or result save. Calling this through a Store
// bound to an outer transaction retains the account row lock through the save.
func (s *Store) PostSubmissionSyncTarget(ctx context.Context, job PostSubmissionSyncJob) (SyncedAccount, error) {
	var target SyncedAccount
	ineligible := false
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var submission Submission
		err := tx.Select("id, user_id, vendor_id, account, plan, status, source, remote_id, platform_account_id, created_at").Where("id = ? AND user_id = ? AND source = ? AND status = ?", job.SubmissionID, job.UserID, "api", "accepted").First(&submission).Error
		if errors.Is(err, ErrNotFound) {
			return ErrPostSubmissionSyncIneligible
		}
		if err != nil {
			return err
		}
		if submission.RemoteID == nil || strings.TrimSpace(*submission.RemoteID) == "" || !strings.HasPrefix(submission.Plan, "claude/") {
			return ErrPostSubmissionSyncIneligible
		}
		var eligible int64
		if err := tx.Model(&User{}).Where("id = ? AND status = ?", job.UserID, "active").Count(&eligible).Error; err != nil {
			return err
		}
		if eligible != 1 {
			return ErrPostSubmissionSyncIneligible
		}
		if err := tx.Model(&Vendor{}).Where("id = ? AND enabled = ? AND code = ? AND base_url = ?", submission.VendorID, true, "tk-register-0", VendorOrigin).Count(&eligible).Error; err != nil {
			return err
		}
		if eligible != 1 {
			return ErrPostSubmissionSyncIneligible
		}
		target, err = ensureSyncedAccountForSubmissionTx(tx, submission)
		if err != nil {
			return err
		}
		ineligible = target.ID != job.AccountID || target.UserID != job.UserID || target.VendorID != submission.VendorID || target.IdentityKey != job.IdentityKey || target.CurrentSubmissionID == nil || *target.CurrentSubmissionID != job.SubmissionID || NormalizeEmail(target.Account) != NormalizeEmail(submission.Account) || !strings.HasPrefix(target.Plan, "claude/")
		// Commit a newly materialized lifetime even when this old job is stale.
		return nil
	})
	if err != nil {
		return SyncedAccount{}, err
	}
	if ineligible {
		return SyncedAccount{}, ErrPostSubmissionSyncIneligible
	}
	return target, nil
}

// message must be a safe application-defined status line, never an exception,
// supplier response or credential. Terminal jobs are immutable and never retried.
func (s *Store) FinishPostSubmissionSyncJob(ctx context.Context, id uint64, status, message string, at time.Time) error {
	if status != "succeeded" && status != "failed" && status != "skipped" {
		return validationErrorf("invalid post-submission sync outcome")
	}
	if at.IsZero() {
		return validationErrorf("invalid post-submission sync finish time")
	}
	message = strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ", "\x00", "").Replace(message))
	if status == "succeeded" {
		message = ""
	} else if message == "" || len(message) > 255 {
		message = "Post-submission account sync did not complete."
	}
	return s.DB.WithContext(ctx).Model(&PostSubmissionSyncJob{}).Where("id = ? AND status = ?", id, "running").Updates(map[string]any{"status": status, "finished_at": at.UTC(), "last_error": message}).Error
}

// An interrupted attempt may already have reached the supplier, so recovery
// records a terminal failure rather than replaying it. Pending jobs survive.
func (s *Store) RecoverPostSubmissionSyncJobs(ctx context.Context, now time.Time) error {
	return s.DB.WithContext(ctx).Model(&PostSubmissionSyncJob{}).Where("status = ? AND started_at < ?", "running", now.UTC().Add(-2*time.Minute)).Updates(map[string]any{"status": "failed", "finished_at": now.UTC(), "last_error": "Post-submission account sync was interrupted; use Sync to refresh again."}).Error
}
