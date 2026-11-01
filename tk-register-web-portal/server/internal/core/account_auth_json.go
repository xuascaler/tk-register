package core

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AccountAuthJSONAudit stores only metadata. New credentials are forwarded to
// the supplier, never persisted here, and historical submissions stay immutable.
type AccountAuthJSONAudit struct {
	ID               uint64        `gorm:"primaryKey"`
	AccountID        uint64        `gorm:"not null;index"`
	Account          SyncedAccount `gorm:"foreignKey:AccountID;constraint:OnDelete:RESTRICT"`
	ActorID          uint64        `gorm:"not null;index"`
	Actor            User          `gorm:"foreignKey:ActorID;constraint:OnDelete:RESTRICT"`
	OwnerID          uint64        `gorm:"not null;index"`
	Owner            User          `gorm:"foreignKey:OwnerID;constraint:OnDelete:RESTRICT"`
	VendorID         uint64        `gorm:"not null;index"`
	Vendor           Vendor        `gorm:"foreignKey:VendorID;constraint:OnDelete:RESTRICT"`
	IdentityKey      string        `gorm:"size:64;not null"`
	RequestID        string        `gorm:"size:64;not null;uniqueIndex"`
	SupplierRecordID string        `gorm:"size:32;not null;default:''"`
	Status           string        `gorm:"size:24;not null"`
	ErrorCode        string        `gorm:"size:80;not null;default:''"`
	RefreshStatus    string        `gorm:"size:24;not null;default:''"`
	CreatedAt        time.Time
	FinishedAt       *time.Time
	UpdatedAt        time.Time
}

func validSupplierRecordID(value string) bool {
	if value == "" || len(value) > 32 || strings.Trim(value, "0123456789") != "" {
		return false
	}
	return strings.TrimLeft(value, "0") != ""
}

// ConfirmSupplierRecord saves only a verified website row ID for this lifetime.
// It deliberately does not use historical submission IDs or platform IDs.
func (s *Store) ConfirmSupplierRecord(ctx context.Context, id uint64, identity, recordID string) error {
	if !validSupplierRecordID(recordID) {
		return validationErrorf("invalid supplier account ID")
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current SyncedAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, id).Error; err != nil {
			return err
		}
		if current.IdentityKey != identity || current.CurrentSubmissionID == nil {
			return ErrAccountIdentityChanged
		}
		// MySQL may report zero affected rows when the confirmed ID is already
		// present. Identity is checked under a row lock, not inferred from it.
		if current.SupplierRecordID != nil && *current.SupplierRecordID == recordID {
			return nil
		}
		return tx.Model(&current).Update("supplier_record_id", recordID).Error
	})
}

func (s *Store) BeginAuthJSONUpdate(ctx context.Context, target SyncedAccount, actor uint64, requestID string) (AccountAuthJSONAudit, error) {
	audit := AccountAuthJSONAudit{AccountID: target.ID, ActorID: actor, OwnerID: target.UserID, VendorID: target.VendorID, IdentityKey: target.IdentityKey, RequestID: requestID, Status: "pending"}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current SyncedAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, target.ID).Error; err != nil {
			return err
		}
		if current.IdentityKey != target.IdentityKey || current.CurrentSubmissionID == nil || current.UserID != target.UserID {
			return ErrAccountIdentityChanged
		}
		return tx.Create(&audit).Error
	})
	return audit, err
}

// FinishAuthJSONUpdate records a confirmed vendor result even if a new account
// lifetime appeared in flight. It never writes metadata onto the new lifetime.
func (s *Store) FinishAuthJSONUpdate(ctx context.Context, audit AccountAuthJSONAudit, status, recordID, code string, at time.Time) (bool, error) {
	if status != "updated" && status != "rejected" && status != "unknown" && status != "failed" {
		return false, validationErrorf("invalid auth update outcome")
	}
	if recordID != "" && !validSupplierRecordID(recordID) {
		return false, validationErrorf("invalid supplier account ID")
	}
	currentIdentity := false
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var account SyncedAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&account, audit.AccountID).Error; err != nil {
			return err
		}
		currentIdentity = account.IdentityKey == audit.IdentityKey && account.CurrentSubmissionID != nil
		refresh := ""
		if status == "updated" {
			refresh = "pending"
		}
		if err := tx.Model(&AccountAuthJSONAudit{}).Where("id = ? AND status = ?", audit.ID, "pending").Updates(map[string]any{"status": status, "supplier_record_id": recordID, "error_code": code, "finished_at": at.UTC(), "refresh_status": refresh}).Error; err != nil {
			return err
		}
		if status == "updated" && currentIdentity {
			return tx.Model(&account).Updates(map[string]any{"supplier_record_id": recordID, "auth_json_updated_at": at.UTC()}).Error
		}
		return nil
	})
	return currentIdentity, err
}

func (s *Store) FinishAuthJSONRefresh(ctx context.Context, auditID uint64, success bool) error {
	status := "failed"
	if success {
		status = "succeeded"
	}
	return s.DB.WithContext(ctx).Model(&AccountAuthJSONAudit{}).Where("id = ? AND status = ?", auditID, "updated").Update("refresh_status", status).Error
}
