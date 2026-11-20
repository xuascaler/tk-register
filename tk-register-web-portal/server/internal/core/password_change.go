package core

import (
	"context"
	"crypto/subtle"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrPasswordChangeConflict = errors.New("password change is no longer valid")

// PasswordChangeChallenge stores binding and completion metadata, never a code
// or password. RequestFingerprint is a keyed HMAC of the submitted operation.
type PasswordChangeChallenge struct {
	ID                 string     `gorm:"size:64;primaryKey" json:"-"`
	UserID             uint64     `gorm:"not null;index" json:"-"`
	Email              string     `gorm:"size:50;not null" json:"-"`
	Role               string     `gorm:"size:16;not null" json:"-"`
	AuthVersion        uint64     `gorm:"not null" json:"-"`
	SessionHash        string     `gorm:"size:64;not null;index" json:"-"`
	Status             string     `gorm:"size:16;not null" json:"-"`
	RequestHash        *string    `gorm:"size:64;uniqueIndex" json:"-"`
	RequestFingerprint string     `gorm:"size:64" json:"-"`
	CreatedAt          time.Time  `json:"-"`
	ExpiresAt          time.Time  `json:"-"`
	CompletedAt        *time.Time `json:"-"`
}

func passwordChangeUser(tx *gorm.DB, c *PasswordChangeChallenge) (*User, error) {
	var u User
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&u, c.UserID).Error; err != nil {
		return nil, err
	}
	if u.Status != "active" || u.Role != c.Role || u.Email != c.Email || u.AuthVersion != c.AuthVersion || u.EmailVerifiedAt.IsZero() {
		return nil, ErrPasswordChangeConflict
	}
	return &u, nil
}

func (s *Store) CreatePasswordChange(ctx context.Context, c *PasswordChangeChallenge) error {
	if len(c.ID) != 64 || len(c.SessionHash) != 64 || !ValidEmail(c.Email) || !c.ExpiresAt.After(time.Now()) {
		return ErrPasswordChangeConflict
	}
	c.Status = "pending"
	return s.DB.WithContext(ctx).Create(c).Error
}

// Activate only after successful SMTP delivery, so a failed resend does not
// revoke the last usable code. User locking serializes resends with completion.
func (s *Store) ActivatePasswordChange(ctx context.Context, c *PasswordChangeChallenge) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := passwordChangeUser(tx, c); err != nil {
			return err
		}
		now := time.Now().UTC()
		if !c.ExpiresAt.After(now) {
			return ErrPasswordChangeConflict
		}
		if err := tx.Model(&PasswordChangeChallenge{}).Where("user_id = ? AND session_hash = ? AND auth_version = ? AND status = ? AND id <> ?", c.UserID, c.SessionHash, c.AuthVersion, "sent", c.ID).Update("status", "revoked").Error; err != nil {
			return err
		}
		result := tx.Model(&PasswordChangeChallenge{}).Where("id = ? AND status = ? AND expires_at > ?", c.ID, "pending", now).Update("status", "sent")
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrPasswordChangeConflict
		}
		return nil
	})
}

func (s *Store) PasswordChange(ctx context.Context, id string) (*PasswordChangeChallenge, error) {
	var c PasswordChangeChallenge
	err := s.DB.WithContext(ctx).First(&c, "id = ?", id).Error
	return &c, err
}

// CompletePasswordChange commits proof use, password hash and session version
// together. Redis verification deliberately leaves proof intact until this
// transaction succeeds, permitting the same request after a database failure.
func (s *Store) CompletePasswordChange(ctx context.Context, expected *PasswordChangeChallenge, requestHash, fingerprint, passwordHash, verificationRequest string) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var u User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&u, expected.UserID).Error; err != nil {
			return err
		}
		var c PasswordChangeChallenge
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&c, "id = ?", expected.ID).Error; err != nil {
			return err
		}
		if c.UserID != expected.UserID || c.Email != expected.Email || c.Role != expected.Role || c.AuthVersion != expected.AuthVersion || c.SessionHash != expected.SessionHash {
			return ErrPasswordChangeConflict
		}
		if c.Status == "consumed" && c.RequestHash != nil && *c.RequestHash == requestHash && subtle.ConstantTimeCompare([]byte(c.RequestFingerprint), []byte(fingerprint)) == 1 {
			return nil
		}
		now := time.Now().UTC()
		if c.Status != "sent" || !c.ExpiresAt.After(now) || u.Status != "active" || u.Role != c.Role || u.Email != c.Email || u.AuthVersion != c.AuthVersion || u.EmailVerifiedAt.IsZero() {
			return ErrPasswordChangeConflict
		}
		result := tx.Model(&User{}).Where("id = ? AND status = ? AND role = ? AND email = ? AND auth_version = ?", u.ID, "active", c.Role, c.Email, c.AuthVersion).Updates(map[string]any{"password_hash": passwordHash, "auth_version": gorm.Expr("auth_version + 1"), "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrPasswordChangeConflict
		}
		result = tx.Model(&PasswordChangeChallenge{}).Where("id = ? AND status = ? AND expires_at > ?", c.ID, "sent", time.Now().UTC()).Updates(map[string]any{"status": "consumed", "request_hash": requestHash, "request_fingerprint": fingerprint, "completed_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrPasswordChangeConflict
		}
		if err := tx.Model(&EmailVerification{}).Where("request_id = ? AND purpose = ?", verificationRequest, "password_change").Updates(map[string]any{"status": "consumed", "consumed_at": now}).Error; err != nil {
			return err
		}
		return nil
	})
}
