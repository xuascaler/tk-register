package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrAccountDeleting            = errors.New("account deletion is in progress")
	ErrAccountDeletionUnconfirmed = errors.New("the account has an unconfirmed submission")
)

// Deletion records contain identity and non-secret state only. Supplier writes
// are performed outside SQL transactions; awaiting_verification is persisted
// before a PUT so a restarted worker can never blindly replay that write.
type AccountDeletionOperation struct {
	ID                    uint64        `gorm:"primaryKey" json:"id"`
	RequestID             string        `gorm:"size:64;not null;uniqueIndex" json:"request_id"`
	AccountID             uint64        `gorm:"not null;index" json:"account_id"`
	Account               SyncedAccount `gorm:"foreignKey:AccountID;constraint:OnDelete:RESTRICT" json:"-"`
	IdentityKey           string        `gorm:"size:64;not null;index" json:"-"`
	OwnerID               uint64        `gorm:"not null;index" json:"owner_id"`
	Owner                 User          `gorm:"foreignKey:OwnerID;constraint:OnDelete:RESTRICT" json:"-"`
	ActorID               uint64        `gorm:"not null" json:"actor_id"`
	Actor                 User          `gorm:"foreignKey:ActorID;constraint:OnDelete:RESTRICT" json:"-"`
	VendorID              uint64        `gorm:"not null;index" json:"vendor_id"`
	Vendor                Vendor        `gorm:"foreignKey:VendorID;constraint:OnDelete:RESTRICT" json:"-"`
	ThroughSubmissionID   uint64        `gorm:"not null" json:"-"`
	SupplierRecordID      string        `gorm:"size:32;not null;default:''" json:"-"`
	OriginalPlanStartTime string        `gorm:"size:10;not null;default:''" json:"-"`
	OriginalPlanEndTime   string        `gorm:"size:10;not null;default:''" json:"-"`
	Status                string        `gorm:"size:32;not null;index:idx_account_deletion_due,priority:1" json:"status"`
	Mode                  string        `gorm:"size:32;not null;default:''" json:"-"`
	ErrorCode             string        `gorm:"size:80;not null;default:''" json:"error_code,omitempty"`
	VerificationCount     int           `gorm:"not null;default:0" json:"-"`
	NextRunAt             time.Time     `gorm:"not null;index:idx_account_deletion_due,priority:2" json:"next_run_at"`
	CreatedAt             time.Time     `json:"created_at"`
	UpdatedAt             time.Time     `json:"updated_at"`
	CompletedAt           *time.Time    `json:"completed_at,omitempty"`
}

func (AccountDeletionOperation) TableName() string { return "account_deletion_operations" }

type DeletedAccount struct {
	ID               uint64        `gorm:"primaryKey"`
	AccountID        uint64        `gorm:"not null;uniqueIndex:idx_deleted_account_lifetime"`
	Account          SyncedAccount `gorm:"foreignKey:AccountID;constraint:OnDelete:RESTRICT"`
	IdentityKey      string        `gorm:"size:64;not null;uniqueIndex:idx_deleted_account_lifetime"`
	OwnerUserID      uint64        `gorm:"not null;index"`
	Owner            User          `gorm:"foreignKey:OwnerUserID;constraint:OnDelete:RESTRICT"`
	DeletedBy        uint64        `gorm:"not null"`
	Actor            User          `gorm:"foreignKey:DeletedBy;constraint:OnDelete:RESTRICT"`
	VendorID         uint64        `gorm:"not null;index"`
	Vendor           Vendor        `gorm:"foreignKey:VendorID;constraint:OnDelete:RESTRICT"`
	AccountEmail     string        `gorm:"size:254;not null"`
	Plan             string        `gorm:"size:64;not null"`
	SupplierRecordID string        `gorm:"size:32;not null;default:''"`
	SubmissionID     *uint64
	Submission       *Submission `gorm:"foreignKey:SubmissionID;constraint:OnDelete:RESTRICT"`
	SnapshotJSON     string      `gorm:"type:text;not null"`
	DeletionMode     string      `gorm:"size:32;not null"`
	DeletedAt        time.Time   `gorm:"not null;index"`
}

func (DeletedAccount) TableName() string { return "deleted_account" }

func (a SyncedAccount) AvailableForMutation() bool {
	return a.DeletedAt == nil && a.DeletionStatus == ""
}

func (s *Store) BeginAccountDeletion(ctx context.Context, accountID, actorID uint64, admin bool, requestID string, at time.Time) (AccountDeletionOperation, SyncedAccount, error) {
	var op AccountDeletionOperation
	var account SyncedAccount
	if accountID == 0 || actorID == 0 || strings.TrimSpace(requestID) == "" || len(requestID) > 64 || at.IsZero() {
		return op, account, validationErrorf("invalid deletion request")
	}
	// The vendor row is the common serialization point with submission creation.
	var initial SyncedAccount
	q := s.DB.WithContext(ctx).Where("id = ?", accountID)
	if !admin {
		q = q.Where("user_id = ?", actorID)
	}
	if err := q.First(&initial).Error; err != nil {
		return op, account, err
	}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var vendor Vendor
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&vendor, initial.VendorID).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&account, accountID).Error; err != nil {
			return err
		}
		if account.UserID != initial.UserID || (!admin && account.UserID != actorID) {
			return ErrNotFound
		}
		// A client request is immutable: replaying a failed operation does not
		// restart it. An intentional retry supplies a new request ID and creates
		// a new operation, preserving the previous failure audit.
		found := tx.Where("request_id = ?", requestID).First(&op).Error
		if found == nil {
			if op.AccountID != account.ID || op.OwnerID != account.UserID {
				return validationErrorf("request ID belongs to a different account")
			}
			return nil
		}
		if !errors.Is(found, ErrNotFound) {
			return found
		}
		if account.DeletionOperationID != nil && account.DeletionStatus != "" {
			if err := tx.First(&op, *account.DeletionOperationID).Error; err != nil {
				return err
			}
			if op.IdentityKey != account.IdentityKey || op.AccountID != account.ID {
				return ErrAccountIdentityChanged
			}
			return nil
		}
		if account.DeletedAt != nil {
			return ErrNotFound
		}
		var latest Submission
		if err := tx.Where("user_id = ? AND vendor_id = ? AND LOWER(TRIM(account)) = ?", account.UserID, account.VendorID, NormalizeEmail(account.Account)).Order("created_at DESC,id DESC").First(&latest).Error; err != nil {
			return err
		}
		var err error
		account, err = ensureSyncedAccountTx(tx, latest)
		if err != nil {
			return err
		}
		if account.CurrentSubmissionID == nil && account.SubmissionStatus != "failed" {
			return ErrAccountDeletionUnconfirmed
		}
		var running int64
		if err := tx.Model(&Submission{}).Where("vendor_id = ? AND LOWER(TRIM(account)) = ? AND status = ?", account.VendorID, NormalizeEmail(account.Account), "running").Count(&running).Error; err != nil {
			return err
		}
		if running != 0 {
			return ErrAccountDeletionUnconfirmed
		}
		if account.CurrentSubmissionID != nil {
			if err := checkAuthJSONOwnership(tx, account); err != nil {
				return err
			}
		}
		var cutoff uint64
		if err := tx.Model(&Submission{}).Select("COALESCE(MAX(id),0)").Where("user_id = ? AND vendor_id = ? AND LOWER(TRIM(account)) = ?", account.UserID, account.VendorID, NormalizeEmail(account.Account)).Scan(&cutoff).Error; err != nil {
			return err
		}
		op = AccountDeletionOperation{RequestID: requestID, AccountID: account.ID, IdentityKey: account.IdentityKey, OwnerID: account.UserID, ActorID: actorID, VendorID: account.VendorID, ThroughSubmissionID: cutoff, Status: "pending", NextRunAt: at.UTC()}
		if err := tx.Create(&op).Error; err != nil {
			return err
		}
		if err := tx.Model(&account).Updates(map[string]any{"deletion_status": "pending", "deletion_operation_id": op.ID}).Error; err != nil {
			return err
		}
		account.DeletionStatus = "pending"
		account.DeletionOperationID = &op.ID
		return nil
	})
	return op, account, err
}

func (s *Store) AccountDeletionForUser(ctx context.Context, id, actorID uint64, admin bool) (AccountDeletionOperation, error) {
	var op AccountDeletionOperation
	q := s.DB.WithContext(ctx).Where("id = ?", id)
	if !admin {
		q = q.Where("owner_id = ?", actorID)
	}
	err := q.First(&op).Error
	return op, err
}

func (s *Store) NextAccountDeletion(ctx context.Context, at time.Time) (*AccountDeletionOperation, error) {
	var op AccountDeletionOperation
	err := s.DB.WithContext(ctx).Where("status IN ? AND next_run_at <= ?", []string{"pending", "awaiting_verification", "upstream_confirmed"}, at.UTC()).Order("next_run_at ASC,id ASC").First(&op).Error
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &op, nil
}

func deletionTargetTx(tx *gorm.DB, id uint64) (AccountDeletionOperation, SyncedAccount, error) {
	var op AccountDeletionOperation
	var account SyncedAccount
	// All transitions lock account before operation, matching credential audits.
	if err := tx.First(&op, id).Error; err != nil {
		return op, account, err
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&account, op.AccountID).Error; err != nil {
		return op, account, err
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&op, id).Error; err != nil {
		return op, account, err
	}
	if account.UserID != op.OwnerID || account.VendorID != op.VendorID || account.IdentityKey != op.IdentityKey || account.DeletionOperationID == nil || *account.DeletionOperationID != op.ID {
		return op, account, ErrAccountIdentityChanged
	}
	if op.Status != "completed" && (account.DeletedAt != nil || account.DeletionStatus == "") {
		return op, account, ErrAccountIdentityChanged
	}
	return op, account, nil
}

func (s *Store) AccountDeletionTarget(ctx context.Context, id uint64) (AccountDeletionOperation, SyncedAccount, error) {
	var op AccountDeletionOperation
	var account SyncedAccount
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error { var err error; op, account, err = deletionTargetTx(tx, id); return err })
	return op, account, err
}

func (s *Store) CheckAccountDeletionOwnership(ctx context.Context, id uint64, recordID string) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		op, account, err := deletionTargetTx(tx, id)
		if err != nil {
			return err
		}
		if op.Status != "pending" && op.Status != "awaiting_verification" {
			return ErrAccountIdentityChanged
		}
		if account.CurrentSubmissionID == nil {
			return ErrAccountDeletionUnconfirmed
		}
		if !validSupplierRecordID(recordID) {
			return validationErrorf("invalid supplier account ID")
		}
		account.SupplierRecordID = &recordID
		return checkAuthJSONOwnership(tx, account)
	})
}

func (s *Store) DeferAccountDeletion(ctx context.Context, id uint64, next time.Time, code string) error {
	if next.IsZero() || !safeDeletionCode(code) {
		return validationErrorf("invalid deletion delay")
	}
	return s.DB.WithContext(ctx).Model(&AccountDeletionOperation{}).Where("id = ? AND status IN ?", id, []string{"pending", "awaiting_verification", "upstream_confirmed"}).Updates(map[string]any{"next_run_at": next.UTC(), "error_code": code}).Error
}

func safeDeletionCode(code string) bool {
	if len(code) > 80 {
		return false
	}
	for _, r := range code {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

func (s *Store) MarkAccountDeletionWriteStarted(ctx context.Context, id uint64, recordID, beforeStart, beforeEnd string, at time.Time) error {
	if !validSupplierRecordID(recordID) || len(beforeStart) > 10 || len(beforeEnd) > 10 || at.IsZero() {
		return validationErrorf("invalid deletion update")
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		op, account, err := deletionTargetTx(tx, id)
		if err != nil {
			return err
		}
		if op.Status != "pending" {
			return ErrAccountIdentityChanged
		}
		if account.CurrentSubmissionID == nil {
			return ErrAccountDeletionUnconfirmed
		}
		account.SupplierRecordID = &recordID
		if err := checkAuthJSONOwnership(tx, account); err != nil {
			return err
		}
		if err := tx.Model(&op).Updates(map[string]any{"status": "awaiting_verification", "supplier_record_id": recordID, "original_plan_start_time": beforeStart, "original_plan_end_time": beforeEnd, "next_run_at": at.UTC(), "error_code": ""}).Error; err != nil {
			return err
		}
		return tx.Model(&account).Update("deletion_status", "awaiting_verification").Error
	})
}

func (s *Store) ConfirmAccountDeletion(ctx context.Context, id uint64, mode, recordID string, at time.Time) error {
	if at.IsZero() || (mode != "local_only" && mode != "upstream_not_found" && mode != "upstream_dates_updated") || recordID != "" && !validSupplierRecordID(recordID) {
		return validationErrorf("invalid deletion confirmation")
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		op, account, err := deletionTargetTx(tx, id)
		if err != nil {
			return err
		}
		if op.Status == "completed" || op.Status == "upstream_confirmed" {
			return nil
		}
		if op.Status != "pending" && op.Status != "awaiting_verification" {
			return ErrAccountIdentityChanged
		}
		if mode == "local_only" && (account.CurrentSubmissionID != nil || account.SubmissionStatus != "failed") {
			return ErrAccountDeletionUnconfirmed
		}
		if mode != "local_only" && account.CurrentSubmissionID == nil {
			return ErrAccountDeletionUnconfirmed
		}
		if mode == "upstream_dates_updated" && !validSupplierRecordID(recordID) {
			return validationErrorf("missing supplier account ID")
		}
		if err := tx.Model(&op).Updates(map[string]any{"status": "upstream_confirmed", "mode": mode, "supplier_record_id": recordID, "next_run_at": at.UTC(), "error_code": ""}).Error; err != nil {
			return err
		}
		return tx.Model(&account).Update("deletion_status", "upstream_confirmed").Error
	})
}

// FailAccountDeletion is valid only before any remote write. For an uncertain
// write, the caller must first obtain a fresh vendor read and use Resolve below.
func (s *Store) FailAccountDeletion(ctx context.Context, id uint64, code string, at time.Time) error {
	return s.failAccountDeletion(ctx, id, code, at, false)
}

// Reject is only for a definitive vendor refusal, never an ambiguous timeout.
func (s *Store) RejectAccountDeletion(ctx context.Context, id uint64, code string, at time.Time) error {
	return s.failAccountDeletion(ctx, id, code, at, true)
}
func (s *Store) ResolveAccountDeletionNotApplied(ctx context.Context, id uint64, at time.Time) error {
	return s.failAccountDeletion(ctx, id, "UPSTREAM_UPDATE_NOT_CONFIRMED", at, true)
}
func (s *Store) failAccountDeletion(ctx context.Context, id uint64, code string, at time.Time, verified bool) error {
	if !safeDeletionCode(code) || at.IsZero() {
		return validationErrorf("invalid deletion failure")
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		op, account, err := deletionTargetTx(tx, id)
		if err != nil {
			return err
		}
		if op.Status != "pending" && !(verified && op.Status == "awaiting_verification") {
			return ErrAccountIdentityChanged
		}
		if err := tx.Model(&op).Updates(map[string]any{"status": "failed", "error_code": code, "completed_at": at.UTC()}).Error; err != nil {
			return err
		}
		return tx.Model(&account).Updates(map[string]any{"deletion_status": "", "deletion_operation_id": nil}).Error
	})
}

func (s *Store) FinishAccountDeletion(ctx context.Context, id uint64, at time.Time) error {
	if at.IsZero() {
		return validationErrorf("invalid deletion time")
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		op, account, err := deletionTargetTx(tx, id)
		if err != nil {
			return err
		}
		if op.Status == "completed" {
			return nil
		}
		if op.Status != "upstream_confirmed" {
			return ErrAccountDeletionUnconfirmed
		}
		reader := *s
		reader.DB = tx
		dto, err := reader.accountDTO(ctx, account)
		if err != nil {
			return err
		}
		// DTO and explicit metadata have no credential fields. Do not archive
		// full supplier payloads or immutable submission secret envelopes.
		snapshot, err := json.Marshal(map[string]any{"account": dto, "plan_start_time": op.OriginalPlanStartTime, "plan_end_time": op.OriginalPlanEndTime})
		if err != nil {
			return err
		}
		submissionID := account.CurrentSubmissionID
		if submissionID == nil {
			submissionID = &account.LatestSubmissionID
		}
		archive := DeletedAccount{AccountID: account.ID, IdentityKey: account.IdentityKey, OwnerUserID: account.UserID, DeletedBy: op.ActorID, VendorID: account.VendorID, AccountEmail: account.Account, Plan: account.Plan, SupplierRecordID: op.SupplierRecordID, SubmissionID: submissionID, SnapshotJSON: string(snapshot), DeletionMode: op.Mode, DeletedAt: at.UTC()}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&archive).Error; err != nil {
			return err
		}
		if err := tx.Model(&account).Updates(map[string]any{"deleted_at": at.UTC(), "deleted_through_submission_id": op.ThroughSubmissionID, "deletion_status": "completed"}).Error; err != nil {
			return err
		}
		if err := tx.Model(&Submission{}).Where("user_id = ? AND vendor_id = ? AND LOWER(TRIM(account)) = ? AND id <= ?", account.UserID, account.VendorID, NormalizeEmail(account.Account), op.ThroughSubmissionID).Update("active_account_key", nil).Error; err != nil {
			return err
		}
		if err := tx.Model(&PostSubmissionSyncJob{}).Where("account_id = ? AND identity_key = ? AND status IN ?", account.ID, account.IdentityKey, []string{"pending", "running"}).Updates(map[string]any{"status": "cancelled", "finished_at": at.UTC(), "last_error": "Account deleted."}).Error; err != nil {
			return err
		}
		return tx.Model(&op).Updates(map[string]any{"status": "completed", "error_code": "", "completed_at": at.UTC()}).Error
	})
}

// Called while holding the vendor row lock shared with BeginAccountDeletion.
func rejectSubmissionDuringDeletion(tx *gorm.DB, vendorID uint64, account string) error {
	var count int64
	if err := tx.Model(&SyncedAccount{}).Where("vendor_id = ? AND LOWER(TRIM(account)) = ? AND deleted_at IS NULL AND deletion_status <> ?", vendorID, NormalizeEmail(account), "").Count(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return vendorError(409, "ACCOUNT_DELETING", "This account is being deleted. Please wait before submitting again.")
	}
	return nil
}
