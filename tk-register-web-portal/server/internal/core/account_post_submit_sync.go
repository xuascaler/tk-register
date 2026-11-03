package core

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PostSubmissionSyncJob schedules a read after an accepted API push or a
// confirmed API Auth JSON update, with up to three retries of that read.
// It stores identity and timing metadata only, never account credentials or
// upstream response bodies. Migration deliberately does not backfill old pushes.
type PostSubmissionSyncJob struct {
	ID           uint64     `gorm:"primaryKey"`
	SubmissionID uint64     `gorm:"not null;index:idx_post_submit_sync_submission_anchor"`
	Submission   Submission `gorm:"foreignKey:SubmissionID;constraint:OnDelete:RESTRICT"`
	// Exactly one nullable trigger reference is set. Separate unique indexes
	// allow multiple credential updates for the same accepted submission.
	SubmissionTriggerID *uint64               `gorm:"uniqueIndex:idx_post_submit_sync_submission_trigger"`
	AuthJSONAuditID     *uint64               `gorm:"uniqueIndex:idx_post_submit_sync_auth_audit"`
	AuthJSONAudit       *AccountAuthJSONAudit `gorm:"foreignKey:AuthJSONAuditID;constraint:OnDelete:RESTRICT"`
	AccountID           uint64                `gorm:"not null;index"`
	Account             SyncedAccount         `gorm:"foreignKey:AccountID;constraint:OnDelete:RESTRICT"`
	UserID              uint64                `gorm:"not null;index"`
	User                User                  `gorm:"foreignKey:UserID;constraint:OnDelete:RESTRICT"`
	IdentityKey         string                `gorm:"size:64;not null"`
	Status              string                `gorm:"size:24;not null;index:idx_post_submit_sync_due,priority:1"`
	AttemptCount        int                   `gorm:"not null;default:0"`
	NextRunAt           time.Time             `gorm:"not null;index:idx_post_submit_sync_due,priority:2"`
	CreatedAt           time.Time             `gorm:"not null"`
	StartedAt           *time.Time
	FinishedAt          *time.Time
	LastError           string `gorm:"size:255;not null;default:''"`
}

func (PostSubmissionSyncJob) TableName() string { return "account_post_submit_sync_jobs" }

var ErrPostSubmissionSyncIneligible = errors.New("post-submission sync target is no longer eligible")

const postSubmissionSyncMaxAttempts = 4

// Add the replacement uniqueness constraint before removing the old one. Each
// step is repeatable because MySQL schema changes are not transactional. Existing
// terminal jobs remain terminal; historical submissions/updates get no jobs.
func (s *Store) migratePostSubmissionSyncJobs() error {
	// This SQLite driver renders newly added uniqueIndex fields as inline
	// UNIQUE columns, which SQLite cannot add to an existing table. Add nullable
	// columns first; AutoMigrate then installs their indexes and audit FK.
	if s.DB.Dialector.Name() == "sqlite" && s.DB.Migrator().HasTable(&PostSubmissionSyncJob{}) {
		for _, column := range []string{"submission_trigger_id", "auth_json_audit_id"} {
			if !s.DB.Migrator().HasColumn(&PostSubmissionSyncJob{}, column) {
				if err := s.DB.Exec("ALTER TABLE `account_post_submit_sync_jobs` ADD COLUMN `" + column + "` integer").Error; err != nil && !s.DB.Migrator().HasColumn(&PostSubmissionSyncJob{}, column) {
					return err
				}
			}
		}
	}
	if err := s.DB.AutoMigrate(&PostSubmissionSyncJob{}); err != nil {
		return err
	}
	if err := s.DB.Model(&PostSubmissionSyncJob{}).Where("auth_json_audit_id IS NULL AND submission_trigger_id IS NULL").UpdateColumn("submission_trigger_id", gorm.Expr("submission_id")).Error; err != nil {
		return err
	}
	if err := s.DB.Model(&PostSubmissionSyncJob{}).Where("attempt_count = ? AND status <> ?", 0, "pending").UpdateColumn("attempt_count", 1).Error; err != nil {
		return err
	}
	const oldIndex = "idx_account_post_submit_sync_jobs_submission_id"
	if s.DB.Migrator().HasIndex(&PostSubmissionSyncJob{}, oldIndex) {
		if err := s.DB.Migrator().DropIndex(&PostSubmissionSyncJob{}, oldIndex); err != nil && s.DB.Migrator().HasIndex(&PostSubmissionSyncJob{}, oldIndex) {
			return err
		}
	}
	return nil
}

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
	job := PostSubmissionSyncJob{SubmissionID: submission.ID, SubmissionTriggerID: &submission.ID, AccountID: account.ID, UserID: submission.UserID, IdentityKey: account.IdentityKey, Status: "pending", CreatedAt: at.UTC(), NextRunAt: at.UTC().Add(3 * time.Second)}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&job).Error
}

func enqueueAuthJSONSyncTx(tx *gorm.DB, account SyncedAccount, audit AccountAuthJSONAudit, at time.Time) error {
	if audit.Source != "api" || audit.Status != "updated" || account.CurrentSubmissionID == nil || account.IdentityKey != audit.IdentityKey || account.UserID != audit.OwnerID || account.ID != audit.AccountID {
		return ErrPostSubmissionSyncIneligible
	}
	job := PostSubmissionSyncJob{SubmissionID: *account.CurrentSubmissionID, AuthJSONAuditID: &audit.ID, AccountID: account.ID, UserID: account.UserID, IdentityKey: account.IdentityKey, Status: "pending", CreatedAt: at.UTC(), NextRunAt: at.UTC().Add(3 * time.Second)}
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
// A running job cannot be claimed twice, and terminal jobs are never reopened.
func (s *Store) ClaimPostSubmissionSyncJob(ctx context.Context, id uint64, now time.Time) (bool, error) {
	result := s.DB.WithContext(ctx).Model(&PostSubmissionSyncJob{}).Where("id = ? AND status = ? AND next_run_at <= ? AND attempt_count < ?", id, "pending", now.UTC(), postSubmissionSyncMaxAttempts).Updates(map[string]any{"status": "running", "started_at": now.UTC(), "finished_at": nil, "last_error": "", "attempt_count": gorm.Expr("attempt_count + 1")})
	return result.RowsAffected == 1, result.Error
}

// PostSubmissionSyncTarget rechecks the trusted owner, accepted submission, and
// optional successful API update before a supplier read or result save. A Store
// bound to an outer transaction retains the account row lock through the save.
func (s *Store) PostSubmissionSyncTarget(ctx context.Context, job PostSubmissionSyncJob) (SyncedAccount, error) {
	var target SyncedAccount
	ineligible := false
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var submission Submission
		err := tx.Select("id, user_id, vendor_id, account, plan, status, source, remote_id, platform_account_id, created_at").Where("id = ? AND user_id = ? AND status = ?", job.SubmissionID, job.UserID, "accepted").First(&submission).Error
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
		if ineligible {
			return nil
		}
		if job.AuthJSONAuditID == nil {
			ineligible = submission.Source != "api" || job.SubmissionTriggerID == nil || *job.SubmissionTriggerID != job.SubmissionID
			return nil
		}
		if job.SubmissionTriggerID != nil {
			ineligible = true
			return nil
		}
		var audit AccountAuthJSONAudit
		err = tx.Where("id = ? AND account_id = ? AND owner_id = ? AND vendor_id = ? AND identity_key = ? AND source = ? AND status = ?", *job.AuthJSONAuditID, target.ID, target.UserID, target.VendorID, target.IdentityKey, "api", "updated").First(&audit).Error
		if errors.Is(err, ErrNotFound) {
			ineligible = true
			return nil
		}
		if err != nil {
			return err
		}
		if audit.ActorID != target.UserID || target.SupplierRecordID == nil || !validSupplierRecordID(audit.SupplierRecordID) || *target.SupplierRecordID != audit.SupplierRecordID {
			ineligible = true
			return nil
		}
		var superseded int64
		if err := tx.Model(&AccountAuthJSONAudit{}).Where("account_id = ? AND identity_key = ? AND status = ? AND id > ?", target.ID, target.IdentityKey, "updated", audit.ID).Count(&superseded).Error; err != nil {
			return err
		}
		ineligible = superseded != 0
		if !ineligible {
			if err := checkAuthJSONOwnership(tx, target); err != nil {
				if errors.Is(err, ErrAccountOwnershipConflict) {
					ineligible = true
					return nil
				}
				return err
			}
		}
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
// supplier response or credential. A failed read is delayed three seconds while
// its remaining retry budget allows it. This never retries a supplier mutation.
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
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job PostSubmissionSyncJob
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND status = ?", id, "running").First(&job).Error
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		return finishPostSubmissionSyncTx(tx, job, status, message, at)
	})
}

func finishPostSubmissionSyncTx(tx *gorm.DB, job PostSubmissionSyncJob, status, message string, at time.Time) error {
	updates := map[string]any{"status": status, "finished_at": at.UTC(), "last_error": message}
	refreshStatus := "failed"
	if status == "succeeded" {
		refreshStatus = "succeeded"
	} else if status == "failed" && job.AttemptCount < postSubmissionSyncMaxAttempts {
		updates["status"], updates["finished_at"], updates["next_run_at"] = "pending", nil, at.UTC().Add(3*time.Second)
		refreshStatus = "pending"
	}
	if err := tx.Model(&PostSubmissionSyncJob{}).Where("id = ? AND status = ?", job.ID, "running").Updates(updates).Error; err != nil {
		return err
	}
	if job.AuthJSONAuditID != nil {
		return tx.Model(&AccountAuthJSONAudit{}).Where("id = ? AND status = ?", *job.AuthJSONAuditID, "updated").Update("refresh_status", refreshStatus).Error
	}
	return nil
}

// Interrupted work only repeats a read, within the same four-attempt budget.
// Pending jobs and terminal outcomes survive restarts unchanged.
func (s *Store) RecoverPostSubmissionSyncJobs(ctx context.Context, now time.Time) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var jobs []PostSubmissionSyncJob
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("status = ? AND started_at < ?", "running", now.UTC().Add(-2*time.Minute)).Find(&jobs).Error; err != nil {
			return err
		}
		for _, job := range jobs {
			if err := finishPostSubmissionSyncTx(tx, job, "failed", "Automatic account sync was interrupted. Previously collected data is retained.", now); err != nil {
				return err
			}
		}
		return nil
	})
}
