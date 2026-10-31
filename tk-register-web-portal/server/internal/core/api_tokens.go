package core

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const ClaudeSubmitScope = "claude:submit"

var ErrInvalidAPIToken = errors.New("invalid API token")

// UserAPIToken contains only a digest and display fragments. The plaintext is
// returned once by generation/rotation and is never recoverable from storage.
type UserAPIToken struct {
	ID         uint64     `gorm:"primaryKey" json:"-"`
	UserID     uint64     `gorm:"not null;uniqueIndex" json:"-"`
	User       User       `gorm:"foreignKey:UserID;constraint:OnDelete:RESTRICT" json:"-"`
	TokenHash  string     `gorm:"size:64;not null;uniqueIndex" json:"-"`
	Prefix     string     `gorm:"size:16;not null" json:"-"`
	LastFour   string     `gorm:"size:4;not null" json:"-"`
	Scope      string     `gorm:"size:32;not null" json:"-"`
	Revision   uint64     `gorm:"not null;default:1" json:"-"`
	UseCount   uint64     `gorm:"not null;default:0" json:"-"`
	CreatedAt  time.Time  `json:"-"`
	UpdatedAt  time.Time  `json:"-"`
	LastUsedAt *time.Time `json:"-"`
	ExpiresAt  *time.Time `json:"-"`
	RevokedAt  *time.Time `json:"-"`
}

type APITokenMetadata struct {
	ID         string     `json:"id"`
	Prefix     string     `json:"prefix"`
	LastFour   string     `json:"last_four"`
	Revision   uint64     `json:"revision"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

type APITokenResult struct {
	Token          *APITokenMetadata `json:"token"`
	PlaintextToken string            `json:"plaintext_token,omitempty"`
}

func apiTokenMetadata(t UserAPIToken) *APITokenMetadata {
	return &APITokenMetadata{ID: idString(t.ID), Prefix: t.Prefix, LastFour: t.LastFour, Revision: t.Revision, CreatedAt: t.CreatedAt, LastUsedAt: t.LastUsedAt, ExpiresAt: t.ExpiresAt, RevokedAt: t.RevokedAt}
}

func apiTokenHash(raw string) string {
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:])
}

func (s *Store) APIToken(ctx context.Context, userID uint64) (APITokenResult, error) {
	var row UserAPIToken
	err := s.DB.WithContext(ctx).Where("user_id = ?", userID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return APITokenResult{}, nil
	}
	if err != nil {
		return APITokenResult{}, err
	}
	return APITokenResult{Token: apiTokenMetadata(row)}, nil
}

// GenerateAPIToken never replaces an active token. Explicit rotation uses a
// revision so concurrent clicks or stale settings tabs cannot rotate twice.
func (s *Store) GenerateAPIToken(ctx context.Context, userID uint64) (APITokenResult, error) {
	return s.writeAPIToken(ctx, userID, 0, 0, false)
}

func (s *Store) RotateAPIToken(ctx context.Context, userID, tokenID, revision uint64) (APITokenResult, error) {
	return s.writeAPIToken(ctx, userID, tokenID, revision, true)
}

func lockTokenUser(tx *gorm.DB, userID uint64) error {
	var user User
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&user, userID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && (user.Status != "active" || user.EmailVerifiedAt.IsZero())) {
		return vendorError(401, "UNAUTHENTICATED", "Sign in to an active account to manage your API token.")
	}
	return err
}

func tokenChanged() error {
	return vendorError(409, "TOKEN_CHANGED", "Your API token has changed. Refresh its details and try again.")
}

func (s *Store) writeAPIToken(ctx context.Context, userID, tokenID, revision uint64, rotate bool) (APITokenResult, error) {
	var out APITokenResult
	entropy := make([]byte, 32)
	if _, err := rand.Read(entropy); err != nil {
		return out, err
	}
	raw := "tkr_" + base64.RawURLEncoding.EncodeToString(entropy)
	now := time.Now().UTC()
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockTokenUser(tx, userID); err != nil {
			return err
		}
		var row UserAPIToken
		err := tx.Where("user_id = ?", userID).First(&row).Error
		missing := errors.Is(err, gorm.ErrRecordNotFound)
		if err != nil && !missing {
			return err
		}
		if rotate && (missing || tokenID == 0 || revision == 0 || row.ID != tokenID || row.Revision != revision || row.RevokedAt != nil) {
			return tokenChanged()
		}
		if !rotate && !missing && row.RevokedAt == nil && (row.ExpiresAt == nil || row.ExpiresAt.After(now)) {
			return vendorError(409, "TOKEN_EXISTS", "An API token already exists. Use Regenerate to replace it.")
		}
		if missing {
			row = UserAPIToken{UserID: userID, TokenHash: apiTokenHash(raw), Prefix: raw[:12], LastFour: raw[len(raw)-4:], Scope: ClaudeSubmitScope, Revision: 1, CreatedAt: now, UpdatedAt: now}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		} else {
			changed := tx.Model(&UserAPIToken{}).Where("id = ? AND user_id = ? AND revision = ?", row.ID, userID, row.Revision).Updates(map[string]any{
				"token_hash": apiTokenHash(raw), "prefix": raw[:12], "last_four": raw[len(raw)-4:], "scope": ClaudeSubmitScope,
				"revision": row.Revision + 1, "use_count": 0, "created_at": now, "updated_at": now, "last_used_at": nil, "expires_at": nil, "revoked_at": nil,
			})
			if changed.Error != nil {
				return changed.Error
			}
			if changed.RowsAffected != 1 {
				return tokenChanged()
			}
			id := row.ID
			row = UserAPIToken{}
			if err := tx.First(&row, id).Error; err != nil {
				return err
			}
		}
		out = APITokenResult{Token: apiTokenMetadata(row), PlaintextToken: raw}
		return nil
	})
	if err != nil {
		return APITokenResult{}, err
	}
	return out, nil
}

func (s *Store) RevokeAPIToken(ctx context.Context, userID, tokenID, revision uint64) (APITokenResult, error) {
	var out APITokenResult
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockTokenUser(tx, userID); err != nil {
			return err
		}
		if tokenID == 0 || revision == 0 {
			return tokenChanged()
		}
		now := time.Now().UTC()
		result := tx.Model(&UserAPIToken{}).Where("id = ? AND user_id = ? AND revision = ? AND revoked_at IS NULL", tokenID, userID, revision).Updates(map[string]any{"revoked_at": now, "revision": revision + 1, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return tokenChanged()
		}
		var row UserAPIToken
		if err := tx.First(&row, tokenID).Error; err != nil {
			return err
		}
		out.Token = apiTokenMetadata(row)
		return nil
	})
	return out, err
}

func (s *Store) AuthenticateAPIToken(ctx context.Context, raw string) (User, UserAPIToken, error) {
	var user User
	var token UserAPIToken
	if len(raw) != 47 || !strings.HasPrefix(raw, "tkr_") {
		return user, token, ErrInvalidAPIToken
	}
	if decoded, err := base64.RawURLEncoding.DecodeString(raw[4:]); err != nil || len(decoded) != 32 {
		return user, token, ErrInvalidAPIToken
	}
	now := time.Now().UTC()
	err := s.DB.WithContext(ctx).Where("token_hash = ? AND scope = ? AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?)", apiTokenHash(raw), ClaudeSubmitScope, now).First(&token).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return user, token, ErrInvalidAPIToken
	}
	if err != nil {
		return user, token, err
	}
	if token.ExpiresAt != nil && !token.ExpiresAt.After(now) {
		return user, token, ErrInvalidAPIToken
	}
	err = s.DB.WithContext(ctx).First(&user, token.UserID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && (user.Status != "active" || user.EmailVerifiedAt.IsZero())) {
		return User{}, token, ErrInvalidAPIToken
	}
	if err != nil {
		return user, token, err
	}
	// A concurrent revoke/rotation must not authenticate a now stale digest.
	updated := s.DB.WithContext(ctx).Model(&UserAPIToken{}).Where("id = ? AND token_hash = ? AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?)", token.ID, token.TokenHash, now).UpdateColumns(map[string]any{"last_used_at": now, "use_count": gorm.Expr("use_count + ?", 1)})
	if updated.Error != nil {
		return user, token, updated.Error
	}
	if updated.RowsAffected != 1 {
		return User{}, token, ErrInvalidAPIToken
	}
	token.LastUsedAt = &now
	return user, token, nil
}
