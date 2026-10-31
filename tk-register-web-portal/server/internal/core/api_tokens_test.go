package core

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goapi-tk-register/server/common"
)

func tokenUser(t *testing.T, s *Store, email string) User {
	t.Helper()
	u := User{Email: email, Role: "member", Status: "active", PasswordHash: "test-hash", EmailVerifiedAt: time.Now().UTC()}
	if err := s.CreateUser(context.Background(), &u); err != nil {
		t.Fatal(err)
	}
	return u
}

func tokenErrorStatus(t *testing.T, err error, status int) {
	t.Helper()
	var problem *VendorError
	if !errors.As(err, &problem) || problem.Status != status {
		t.Fatalf("got error %v; want status %d", err, status)
	}
}

func TestAPITokenLifecycleAndNoPlaintextPersistence(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := tokenUser(t, s, "api-token@example.com")
	empty, err := s.APIToken(ctx, u.ID)
	if err != nil || empty.Token != nil {
		t.Fatal("initial token exists", err)
	}
	generated, err := s.GenerateAPIToken(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw := generated.PlaintextToken
	if len(raw) != 47 || !strings.HasPrefix(raw, "tkr_") || generated.Token.Revision != 1 {
		t.Fatal("invalid generated token")
	}
	var row UserAPIToken
	if err := s.DB.Where("user_id = ?", u.ID).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if len(row.TokenHash) != 64 || row.TokenHash == raw || row.TokenHash != apiTokenHash(raw) || row.Scope != ClaudeSubmitScope {
		t.Fatal("token not stored as scoped digest")
	}
	metadata, err := s.APIToken(ctx, u.ID)
	if err != nil || metadata.PlaintextToken != "" {
		t.Fatal("token plaintext retrieved", err)
	}
	encoded, _ := common.Marshal(metadata)
	if strings.Contains(string(encoded), raw) || strings.Contains(string(encoded), row.TokenHash) {
		t.Fatal("metadata leaked token material")
	}
	serializedRow, _ := common.Marshal(row)
	if string(serializedRow) != "{}" {
		t.Fatal("raw token model should not be serializable")
	}
	_, err = s.GenerateAPIToken(ctx, u.ID)
	tokenErrorStatus(t, err, 409)
	for i := 0; i < 3; i++ {
		gotUser, gotToken, err := s.AuthenticateAPIToken(ctx, raw)
		if err != nil || gotUser.ID != u.ID || gotToken.ID != row.ID || gotToken.LastUsedAt == nil {
			t.Fatal("token authentication failed", err)
		}
	}
	rotated, err := s.RotateAPIToken(ctx, u.ID, row.ID, 1)
	if err != nil || rotated.PlaintextToken == raw || rotated.Token.Revision != 2 || rotated.Token.ID != generated.Token.ID {
		t.Fatal("rotation failed", err)
	}
	if _, _, err := s.AuthenticateAPIToken(ctx, raw); !errors.Is(err, ErrInvalidAPIToken) {
		t.Fatal("old token still accepted")
	}
	if _, err := s.RotateAPIToken(ctx, u.ID, row.ID, 1); err == nil {
		t.Fatal("stale rotation succeeded")
	}
	if _, err := s.RevokeAPIToken(ctx, u.ID, row.ID, 1); err == nil {
		t.Fatal("stale revoke succeeded")
	}
	revoked, err := s.RevokeAPIToken(ctx, u.ID, row.ID, 2)
	if err != nil || revoked.Token.RevokedAt == nil || revoked.PlaintextToken != "" || revoked.Token.Revision != 3 {
		t.Fatal("revoke failed", err)
	}
	if _, _, err := s.AuthenticateAPIToken(ctx, rotated.PlaintextToken); !errors.Is(err, ErrInvalidAPIToken) {
		t.Fatal("revoked token accepted")
	}
	again, err := s.GenerateAPIToken(ctx, u.ID)
	if err != nil || again.Token.ID != generated.Token.ID || again.Token.Revision != 4 || again.Token.RevokedAt != nil {
		t.Fatal("regeneration after revoke failed", err)
	}
	var count int64
	s.DB.Model(&UserAPIToken{}).Where("user_id = ?", u.ID).Count(&count)
	if count != 1 {
		t.Fatal("multiple token rows per user")
	}
}

func TestAPITokenRejectsExpiredScopedAndInactiveCredentials(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := tokenUser(t, s, "token-owner@example.com")
	other := tokenUser(t, s, "other-owner@example.com")
	created, err := s.GenerateAPIToken(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := ParseBusinessID(created.Token.ID)
	if _, err := s.RotateAPIToken(ctx, other.ID, id, 1); err == nil {
		t.Fatal("another user rotated token")
	}
	if _, err := s.RevokeAPIToken(ctx, other.ID, id, 1); err == nil {
		t.Fatal("another user revoked token")
	}
	for _, raw := range []string{"", "tkr_short", strings.Repeat("x", 47), "tkr_" + strings.Repeat("!", 43), "tkr_" + strings.Repeat("x", 43)} {
		if _, _, err := s.AuthenticateAPIToken(ctx, raw); !errors.Is(err, ErrInvalidAPIToken) {
			t.Fatal("invalid token accepted")
		}
	}
	past := time.Now().UTC().Add(-time.Minute)
	s.DB.Model(&UserAPIToken{}).Where("id = ?", id).Update("expires_at", past)
	if _, _, err := s.AuthenticateAPIToken(ctx, created.PlaintextToken); !errors.Is(err, ErrInvalidAPIToken) {
		t.Fatal("expired token accepted")
	}
	s.DB.Model(&UserAPIToken{}).Where("id = ?", id).Updates(map[string]any{"expires_at": nil, "scope": "admin"})
	if _, _, err := s.AuthenticateAPIToken(ctx, created.PlaintextToken); !errors.Is(err, ErrInvalidAPIToken) {
		t.Fatal("incorrect scope accepted")
	}
	s.DB.Model(&UserAPIToken{}).Where("id = ?", id).Update("scope", ClaudeSubmitScope)
	s.DB.Model(&User{}).Where("id = ?", u.ID).Update("status", "disabled")
	if _, _, err := s.AuthenticateAPIToken(ctx, created.PlaintextToken); !errors.Is(err, ErrInvalidAPIToken) {
		t.Fatal("disabled user accepted")
	}
	_, err = s.RotateAPIToken(ctx, u.ID, id, 1)
	tokenErrorStatus(t, err, 401)
	s.DB.Model(&User{}).Where("id = ?", u.ID).Updates(map[string]any{"status": "active", "email_verified_at": time.Time{}})
	if _, _, err := s.AuthenticateAPIToken(ctx, created.PlaintextToken); !errors.Is(err, ErrInvalidAPIToken) {
		t.Fatal("unverified user accepted")
	}
}

func TestConcurrentAPITokenMutationsHaveOneWinner(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := tokenUser(t, s, "token-race@example.com")
	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.GenerateAPIToken(ctx, u.ID); err == nil {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("%d concurrent generation winners", winners.Load())
	}
	metadata, err := s.APIToken(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := ParseBusinessID(metadata.Token.ID)
	winners.Store(0)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.RotateAPIToken(ctx, u.ID, id, metadata.Token.Revision); err == nil {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("%d concurrent rotation winners", winners.Load())
	}
}
