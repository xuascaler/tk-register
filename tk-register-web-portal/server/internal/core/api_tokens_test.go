package core

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goapi-tk-register/server/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
	if len(row.TokenHash) != 64 || row.TokenHash == raw || row.TokenHash != apiTokenHash(raw) || row.Scope != DefaultAPITokenScope {
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
	if _, _, err := s.AuthenticateAPIToken(ctx, created.PlaintextToken); !errors.Is(err, ErrAPITokenScope) {
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

func TestAPITokenScopePermissionsAndNoAdministratorBypass(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := tokenUser(t, s, "read-scope@example.com")
	generated, err := s.GenerateAPIToken(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(DefaultAPITokenScope) > 32 {
		t.Fatal("default scopes exceed stored column size")
	}
	id, _ := ParseBusinessID(generated.Token.ID)
	for _, scope := range []string{ClaudeSubmitScope, AccountsReadScope} {
		got, _, err := s.AuthenticateAPITokenForScope(ctx, generated.PlaintextToken, scope)
		if err != nil || got.ID != u.ID {
			t.Fatal("default token lacks advertised permission", scope, err)
		}
	}
	if err = s.DB.Model(&User{}).Where("id = ?", u.ID).Update("role", "admin").Error; err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{ClaudeSubmitScope, "admin", "accounts:read:any", "not-accounts:read", "accounts:reader", ""} {
		if err = s.DB.Model(&UserAPIToken{}).Where("id = ?", id).Update("scope", scope).Error; err != nil {
			t.Fatal(err)
		}
		if _, _, err = s.AuthenticateAPITokenForScope(ctx, generated.PlaintextToken, AccountsReadScope); !errors.Is(err, ErrAPITokenScope) {
			t.Fatal("role or nonmatching scope granted read permission", scope, err)
		}
	}
	if err = s.DB.Model(&UserAPIToken{}).Where("id = ?", id).Update("scope", AccountsReadScope).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.AuthenticateAPIToken(ctx, generated.PlaintextToken); !errors.Is(err, ErrAPITokenScope) {
		t.Fatal("read-only token submitted an account", err)
	}
	if _, _, err = s.AuthenticateAPITokenForScope(ctx, generated.PlaintextToken, AccountsReadScope); err != nil {
		t.Fatal("read-only token cannot read", err)
	}
	if _, _, err = s.AuthenticateAPITokenForScope(ctx, generated.PlaintextToken, ""); !errors.Is(err, ErrAPITokenScope) {
		t.Fatal("unspecified permission allowed authentication", err)
	}
	if err = s.DB.Model(&User{}).Where("id = ?", u.ID).Update("status", "disabled").Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.AuthenticateAPITokenForScope(ctx, generated.PlaintextToken, ClaudeSubmitScope); !errors.Is(err, ErrInvalidAPIToken) {
		t.Fatal("inactive user must fail authentication before scope validation", err)
	}
	if err = s.DB.Model(&User{}).Where("id = ?", u.ID).Update("status", "active").Error; err != nil {
		t.Fatal(err)
	}
	rotated, err := s.RotateAPIToken(ctx, u.ID, id, generated.Token.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.AuthenticateAPITokenForScope(ctx, generated.PlaintextToken, AccountsReadScope); !errors.Is(err, ErrInvalidAPIToken) {
		t.Fatal("rotation left old read credential active")
	}
	for _, scope := range []string{ClaudeSubmitScope, AccountsReadScope} {
		if _, _, err = s.AuthenticateAPITokenForScope(ctx, rotated.PlaintextToken, scope); err != nil {
			t.Fatal("rotated token lacks default permission", scope, err)
		}
	}
	if _, err = s.RevokeAPIToken(ctx, u.ID, id, rotated.Token.Revision); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.AuthenticateAPITokenForScope(ctx, rotated.PlaintextToken, AccountsReadScope); !errors.Is(err, ErrInvalidAPIToken) {
		t.Fatal("revoked read token accepted")
	}
}

func TestLegacyAPITokenScopeMigrationRunsOnOpenAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-tokens.sqlite")
	s, err := Open("sqlite", path, testKey)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var before []UserAPIToken
	raw := map[string]string{}
	past := time.Now().UTC().Add(-time.Hour)
	for _, kind := range []string{"active", "revoked", "expired", "read-only"} {
		u := tokenUser(t, s, kind+"@example.com")
		generated, err := s.GenerateAPIToken(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		raw[kind] = generated.PlaintextToken
		updates := map[string]any{"scope": ClaudeSubmitScope, "revision": 7, "use_count": 3, "last_used_at": past}
		if kind == "revoked" {
			updates["revoked_at"] = past
		}
		if kind == "expired" {
			updates["expires_at"] = past
		}
		if kind == "read-only" {
			updates["scope"] = AccountsReadScope
		}
		if err = s.DB.Model(&UserAPIToken{}).Where("user_id = ?", u.ID).UpdateColumns(updates).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err = s.DB.Order("id").Find(&before).Error; err != nil {
		t.Fatal(err)
	}
	// The test fixture was created by the new schema. Remove its marker to model
	// an existing deployment that has never run the own-account-read migration.
	if err = s.DB.Where(clause.Eq{Column: "key", Value: apiTokenReadScopeMigration}).Delete(&dataMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	s.Close()
	for i := 0; i < 2; i++ {
		s, err = Open("sqlite", path, testKey)
		if err != nil {
			t.Fatal(err)
		}
		var after []UserAPIToken
		if err = s.DB.Order("id").Find(&after).Error; err != nil {
			t.Fatal(err)
		}
		if len(after) != len(before) {
			t.Fatal("migration changed token count")
		}
		for j := range after {
			want := before[j]
			if want.Scope == ClaudeSubmitScope {
				want.Scope = DefaultAPITokenScope
			}
			if !reflect.DeepEqual(after[j], want) {
				t.Fatal("migration changed token fields beyond the legacy scope")
			}
		}
		if i == 0 {
			s.Close()
		}
	}
	defer func() { s.Close() }()
	for _, kind := range []string{"active", "revoked", "expired", "read-only"} {
		_, _, err = s.AuthenticateAPITokenForScope(ctx, raw[kind], AccountsReadScope)
		if kind == "active" || kind == "read-only" {
			if err != nil {
				t.Fatal("migration invalidated unchanged credential", kind, err)
			}
		} else if !errors.Is(err, ErrInvalidAPIToken) {
			t.Fatal("migration revived invalid credential", kind, err)
		}
	}
	// An explicit permission reduction after upgrade must survive later starts.
	if err = s.DB.Model(&UserAPIToken{}).Where("token_hash = ?", apiTokenHash(raw["active"])).UpdateColumn("scope", ClaudeSubmitScope).Error; err != nil {
		t.Fatal(err)
	}
	s.Close()
	reopened, err := Open("sqlite", path, testKey)
	if err != nil {
		t.Fatal(err)
	}
	s = reopened
	if _, _, err = s.AuthenticateAPITokenForScope(ctx, raw["active"], AccountsReadScope); !errors.Is(err, ErrAPITokenScope) {
		t.Fatal("restart regranted a deliberately removed read permission", err)
	}
	if _, _, err = s.AuthenticateAPIToken(ctx, raw["active"]); err != nil {
		t.Fatal("narrowed submission token stopped working", err)
	}
}

func TestAPITokenScopeMigrationRollsBackMarkerOnFailure(t *testing.T) {
	s := testStore(t)
	u := tokenUser(t, s, "migration-retry@example.com")
	ctx := context.Background()
	generated, err := s.GenerateAPIToken(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DB.Model(&UserAPIToken{}).Where("user_id = ?", u.ID).UpdateColumn("scope", ClaudeSubmitScope).Error; err != nil {
		t.Fatal(err)
	}
	if err = s.DB.Where(clause.Eq{Column: "key", Value: apiTokenReadScopeMigration}).Delete(&dataMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	const callback = "test:fail_token_scope_migration"
	if err = s.DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "user_api_tokens" {
			tx.AddError(errors.New("simulated token scope update failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	err = s.migrateAPITokenReadScope()
	if removeErr := s.DB.Callback().Update().Remove(callback); removeErr != nil {
		t.Fatal(removeErr)
	}
	if err == nil {
		t.Fatal("simulated migration failure was ignored")
	}
	var markerCount int64
	if err = s.DB.Model(&dataMigration{}).Where(clause.Eq{Column: "key", Value: apiTokenReadScopeMigration}).Count(&markerCount).Error; err != nil || markerCount != 0 {
		t.Fatal("failed migration retained its completion marker", markerCount, err)
	}
	if _, _, err = s.AuthenticateAPITokenForScope(ctx, generated.PlaintextToken, AccountsReadScope); !errors.Is(err, ErrAPITokenScope) {
		t.Fatal("failed migration partially changed token permissions", err)
	}
	if err = s.migrateAPITokenReadScope(); err != nil {
		t.Fatal("migration could not retry after rollback", err)
	}
	if _, _, err = s.AuthenticateAPITokenForScope(ctx, generated.PlaintextToken, AccountsReadScope); err != nil {
		t.Fatal("successful retry did not grant read scope", err)
	}
	if err = s.DB.Model(&dataMigration{}).Where(clause.Eq{Column: "key", Value: apiTokenReadScopeMigration}).Count(&markerCount).Error; err != nil || markerCount != 1 {
		t.Fatal("successful migration must leave exactly one marker", markerCount, err)
	}
}

func TestConcurrentAPITokenScopeMigrationUpdatesOnce(t *testing.T) {
	s := testStore(t)
	u := tokenUser(t, s, "migration-concurrent@example.com")
	if _, err := s.GenerateAPIToken(context.Background(), u.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Model(&UserAPIToken{}).Where("user_id = ?", u.ID).UpdateColumn("scope", ClaudeSubmitScope).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Where(clause.Eq{Column: "key", Value: apiTokenReadScopeMigration}).Delete(&dataMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	var updates atomic.Int32
	const callback = "test:count_token_scope_migration"
	if err := s.DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "user_api_tokens" {
			updates.Add(1)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer s.DB.Callback().Update().Remove(callback)
	var wg sync.WaitGroup
	errorsFound := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errorsFound <- s.migrateAPITokenReadScope()
		}()
	}
	wg.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	if updates.Load() != 1 {
		t.Fatal("concurrent startup applied scope changes more than once", updates.Load())
	}
}
