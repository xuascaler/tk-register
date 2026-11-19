package core

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AccountAuthJSONGenerationAudit records an account-scoped credential export.
// Credentials, proxy URLs and their encrypted drafts never enter this table.
// A generated credential does not confirm that the provider disabled old ones.
type AccountAuthJSONGenerationAudit struct {
	ID               uint64 `gorm:"primaryKey"`
	AccountID        uint64 `gorm:"not null;index"`
	ActorID          uint64 `gorm:"not null;uniqueIndex:credential_generation_actor_flow;index"`
	OwnerID          uint64 `gorm:"not null;index"`
	VendorID         uint64 `gorm:"not null;index"`
	IdentityKey      string `gorm:"size:64;not null"`
	CredentialFlowID string `gorm:"size:128;not null;uniqueIndex:credential_generation_actor_flow"`
	Method           string `gorm:"size:16;not null"`
	Purpose          string `gorm:"size:24;not null"`
	Admin            bool   `gorm:"not null"`
	Status           string `gorm:"size:24;not null"`
	RequestID        string `gorm:"size:64;not null;index"`
	CreatedAt        time.Time
}

// RecordAuthJSONGeneration is idempotent for an actor/flow. It rechecks the
// account lifetime and current actor under database locks, after slow helper
// verification. It only inserts metadata; no account or submission is updated.
func (s *Store) RecordAuthJSONGeneration(ctx context.Context, target SyncedAccount, actor User, admin bool, flowID, method, requestID string) (AccountAuthJSONGenerationAudit, error) {
	var audit AccountAuthJSONGenerationAudit
	if flowID == "" || len(flowID) > 128 || len(requestID) > 64 {
		return audit, validationErrorf("invalid credential generation metadata")
	}
	switch method {
	case "oauth", "setup", "json", "cookie", "token":
	default:
		return audit, validationErrorf("invalid credential generation method")
	}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var currentActor User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&currentActor, actor.ID).Error; err != nil {
			return err
		}
		if currentActor.Status != "active" || currentActor.AuthVersion != actor.AuthVersion || currentActor.Role != actor.Role || admin && currentActor.Role != "admin" {
			return vendorError(401, "UNAUTHENTICATED", "Please sign in again.")
		}
		var current SyncedAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, target.ID).Error; err != nil {
			return err
		}
		if !current.AvailableForMutation() || current.CurrentSubmissionID == nil || current.UserID != target.UserID || current.VendorID != target.VendorID || current.IdentityKey != target.IdentityKey || NormalizeEmail(current.Account) != NormalizeEmail(target.Account) {
			return ErrAccountIdentityChanged
		}
		if !admin && current.UserID != currentActor.ID {
			return ErrNotFound
		}
		if err := checkAuthJSONOwnership(tx, current); err != nil {
			return err
		}
		result := tx.Where("actor_id = ? AND credential_flow_id = ?", actor.ID, flowID).First(&audit)
		if result.Error == nil {
			if audit.AccountID != current.ID || audit.OwnerID != current.UserID || audit.VendorID != current.VendorID || audit.IdentityKey != current.IdentityKey || audit.Admin != admin || audit.Method != method || audit.Purpose != "deactivate" || audit.Status != "generated" {
				return ErrAccountIdentityChanged
			}
			return nil
		}
		if result.Error != gorm.ErrRecordNotFound {
			return result.Error
		}
		audit = AccountAuthJSONGenerationAudit{AccountID: current.ID, ActorID: actor.ID, OwnerID: current.UserID, VendorID: current.VendorID, IdentityKey: current.IdentityKey, CredentialFlowID: flowID, Method: method, Purpose: "deactivate", Admin: admin, Status: "generated", RequestID: requestID, CreatedAt: time.Now().UTC()}
		return tx.Create(&audit).Error
	})
	return audit, err
}
