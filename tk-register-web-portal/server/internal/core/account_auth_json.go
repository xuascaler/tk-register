package core

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AccountAuthJSONAudit stores only metadata. New credentials are forwarded to
// the supplier, never persisted here, and historical submissions stay immutable.
type AccountAuthJSONAudit struct {
	Source           string        `gorm:"size:16;not null;default:web;index"`
	APITokenID       *uint64       `gorm:"index"`
	APIToken         *UserAPIToken `gorm:"foreignKey:APITokenID;constraint:OnDelete:RESTRICT"`
	APITokenRevision *uint64
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

// ErrAccountOwnershipConflict indicates that another local owner has an
// accepted account pointing at the same vendor account. A vendor lease prevents
// concurrent writes but cannot resolve ambiguous ownership.
var ErrAccountOwnershipConflict = errors.New("account ownership conflict")

// ValidateClaudeAccountAuthJSON applies update-specific identity validation.
// Submission validation remains unchanged; updates cannot replace credentials
// with an object belonging to a different account.
func ValidateClaudeAccountAuthJSON(input any, account string) (map[string]any, error) {
	auth, err := ValidateClaudeAuthJSON(input)
	if err != nil {
		return nil, err
	}
	if NormalizeEmail(auth["email"].(string)) != NormalizeEmail(account) {
		return nil, vendorError(400, "AUTH_JSON_EMAIL_MISMATCH", "The Auth JSON email must match the account being updated.")
	}
	return auth, nil
}

func checkAuthJSONOwnership(tx *gorm.DB, target SyncedAccount) error {
	// Check accepted submissions directly, so another owner's recent accepted
	// submission is covered even before their synced-account row is materialized.
	var count int64
	if err := tx.Model(&Submission{}).Where("vendor_id = ? AND user_id <> ? AND status = ? AND remote_id IS NOT NULL AND remote_id <> ? AND LOWER(TRIM(account)) = ?",
		target.VendorID, target.UserID, "accepted", "", NormalizeEmail(target.Account)).Count(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return ErrAccountOwnershipConflict
	}
	if target.SupplierRecordID == nil || *target.SupplierRecordID == "" {
		return nil
	}
	if err := tx.Model(&SyncedAccount{}).Where("vendor_id = ? AND user_id <> ? AND current_submission_id IS NOT NULL AND supplier_record_id = ?",
		target.VendorID, target.UserID, *target.SupplierRecordID).Count(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return ErrAccountOwnershipConflict
	}
	return nil
}

// CheckAuthJSONOwnership is also used after resolving the confirmed supplier
// row ID. It reads local ownership only and never scans the supplier inventory.
func (s *Store) CheckAuthJSONOwnership(ctx context.Context, target SyncedAccount) error {
	return checkAuthJSONOwnership(s.DB.WithContext(ctx), target)
}

func (s *Store) BeginAuthJSONUpdate(ctx context.Context, target SyncedAccount, actor uint64, requestID string) (AccountAuthJSONAudit, error) {
	return s.BeginAuthJSONUpdateWithSource(ctx, target, actor, requestID, "web", nil, nil)
}

// BeginAuthJSONUpdateWithSource records the authenticated token reference and
// revision, never its plaintext/digest. Existing web callers use the wrapper.
func (s *Store) BeginAuthJSONUpdateWithSource(ctx context.Context, target SyncedAccount, actor uint64, requestID, source string, tokenID, revision *uint64) (AccountAuthJSONAudit, error) {
	audit := AccountAuthJSONAudit{AccountID: target.ID, ActorID: actor, OwnerID: target.UserID, VendorID: target.VendorID, IdentityKey: target.IdentityKey, RequestID: requestID, Status: "pending", Source: source, APITokenID: tokenID, APITokenRevision: revision}
	if source != "web" && source != "api" || source == "web" && (tokenID != nil || revision != nil) || source == "api" && (tokenID == nil || revision == nil || *tokenID == 0 || *revision == 0 || actor != target.UserID) {
		return AccountAuthJSONAudit{}, validationErrorf("invalid auth update source")
	}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current SyncedAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, target.ID).Error; err != nil {
			return err
		}
		if current.IdentityKey != target.IdentityKey || current.CurrentSubmissionID == nil || current.UserID != target.UserID {
			return ErrAccountIdentityChanged
		}
		if err := checkAuthJSONOwnership(tx, current); err != nil {
			return err
		}
		if source == "api" {
			var token UserAPIToken
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ? AND revision = ?", *tokenID, actor, *revision).First(&token).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrInvalidAPIToken
				}
				return err
			}
			if token.RevokedAt != nil || token.ExpiresAt != nil && !token.ExpiresAt.After(time.Now().UTC()) {
				return ErrInvalidAPIToken
			}
			if !apiTokenHasScope(token.Scope, ClaudeAuthJSONUpdateScope) {
				return ErrAPITokenScope
			}
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
