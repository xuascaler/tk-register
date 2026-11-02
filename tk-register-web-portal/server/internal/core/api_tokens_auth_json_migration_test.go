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

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestAuthJSONScopeMigrationPreservesExistingTokensAndRunsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-auth-json-tokens.sqlite")
	s, err := Open("sqlite", path, testKey)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	raw := map[string]string{}
	scopes := map[string]string{
		"active":      legacyReadAPITokenScope,
		"submit-only": ClaudeSubmitScope,
		"read-only":   AccountsReadScope,
		"revoked":     legacyReadAPITokenScope,
		"expired":     legacyReadAPITokenScope,
		"unrelated":   "admin",
		"empty":       "",
		"already":     DefaultAPITokenScope,
	}
	past := time.Now().UTC().Add(-time.Hour)
	for kind, scope := range scopes {
		user := tokenUser(t, s, kind+"-auth-migration@example.com")
		generated, err := s.GenerateAPIToken(ctx, user.ID)
		if err != nil {
			t.Fatal(err)
		}
		raw[kind] = generated.PlaintextToken
		updates := map[string]any{"scope": scope, "revision": uint64(8), "use_count": uint64(4), "last_used_at": past}
		if kind == "revoked" {
			updates["revoked_at"] = past
		}
		if kind == "expired" {
			updates["expires_at"] = past
		}
		if err := s.DB.Model(&UserAPIToken{}).Where("user_id = ?", user.ID).UpdateColumns(updates).Error; err != nil {
			t.Fatal(err)
		}
	}
	var before []UserAPIToken
	if err := s.DB.Order("id").Find(&before).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Where(clause.Eq{Column: "key", Value: apiTokenAuthJSONScopeMigration}).Delete(&dataMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	s.Close()
	for i := 0; i < 2; i++ {
		s, err = Open("sqlite", path, testKey)
		if err != nil {
			t.Fatal(err)
		}
		var after []UserAPIToken
		if err := s.DB.Order("id").Find(&after).Error; err != nil {
			t.Fatal(err)
		}
		if len(after) != len(before) {
			t.Fatal("migration changed token count")
		}
		for j := range after {
			want := before[j]
			if !apiTokenHasScope(want.Scope, ClaudeAuthJSONUpdateScope) && (apiTokenHasScope(want.Scope, ClaudeSubmitScope) || apiTokenHasScope(want.Scope, AccountsReadScope)) {
				want.Scope = strings.TrimSpace(want.Scope) + " " + ClaudeAuthJSONUpdateScope
			}
			if !reflect.DeepEqual(after[j], want) {
				t.Fatalf("migration changed token validity or duplicated a permission: got scope %q want %q", after[j].Scope, want.Scope)
			}
		}
		if i == 0 {
			s.Close()
		}
	}
	defer func() { s.Close() }()
	for kind, credential := range raw {
		_, _, err := s.AuthenticateAPITokenForScope(ctx, credential, ClaudeAuthJSONUpdateScope)
		switch kind {
		case "revoked", "expired":
			if !errors.Is(err, ErrInvalidAPIToken) {
				t.Fatal("migration revived invalid token", kind, err)
			}
		case "unrelated", "empty":
			if !errors.Is(err, ErrAPITokenScope) {
				t.Fatal("migration widened unrelated permissions", kind, err)
			}
		default:
			if err != nil {
				t.Fatal("unchanged existing token cannot update Auth JSON", kind, err)
			}
		}
	}
	if err := s.DB.Model(&UserAPIToken{}).Where("token_hash = ?", apiTokenHash(raw["active"])).UpdateColumn("scope", legacyReadAPITokenScope).Error; err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open("sqlite", path, testKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AuthenticateAPITokenForScope(ctx, raw["active"], ClaudeAuthJSONUpdateScope); !errors.Is(err, ErrAPITokenScope) {
		t.Fatal("restart overrode explicit later scope reduction", err)
	}
	if _, _, err := s.AuthenticateAPITokenForScope(ctx, raw["active"], AccountsReadScope); err != nil {
		t.Fatal("narrowed existing token lost read access", err)
	}
}

func TestAuthJSONScopeMigrationRollbackAndRetry(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	user := tokenUser(t, s, "auth-migration-rollback@example.com")
	generated, err := s.GenerateAPIToken(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Model(&UserAPIToken{}).Where("user_id = ?", user.ID).UpdateColumn("scope", legacyReadAPITokenScope).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Where(clause.Eq{Column: "key", Value: apiTokenAuthJSONScopeMigration}).Delete(&dataMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	const callback = "test:fail_auth_scope_migration"
	if err := s.DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "user_api_tokens" {
			tx.AddError(errors.New("simulated migration failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	err = s.migrateAPITokenAuthJSONScope()
	if removeErr := s.DB.Callback().Update().Remove(callback); removeErr != nil {
		t.Fatal(removeErr)
	}
	if err == nil {
		t.Fatal("failed migration was marked successful")
	}
	var count int64
	if err := s.DB.Model(&dataMigration{}).Where(clause.Eq{Column: "key", Value: apiTokenAuthJSONScopeMigration}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("rolled-back migration retained marker", err, count)
	}
	if _, _, err := s.AuthenticateAPITokenForScope(ctx, generated.PlaintextToken, ClaudeAuthJSONUpdateScope); !errors.Is(err, ErrAPITokenScope) {
		t.Fatal("failed transaction partially granted scope", err)
	}
	if err := s.migrateAPITokenAuthJSONScope(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AuthenticateAPITokenForScope(ctx, generated.PlaintextToken, ClaudeAuthJSONUpdateScope); err != nil {
		t.Fatal("retry did not upgrade existing credential", err)
	}
}

func TestConcurrentAuthJSONScopeMigrationUpdatesOnce(t *testing.T) {
	s := testStore(t)
	user := tokenUser(t, s, "auth-migration-concurrent@example.com")
	if _, err := s.GenerateAPIToken(context.Background(), user.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Model(&UserAPIToken{}).Where("user_id = ?", user.ID).UpdateColumn("scope", legacyReadAPITokenScope).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Where(clause.Eq{Column: "key", Value: apiTokenAuthJSONScopeMigration}).Delete(&dataMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	var updates atomic.Int32
	const callback = "test:count_auth_scope_migration"
	if err := s.DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "user_api_tokens" {
			updates.Add(1)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer s.DB.Callback().Update().Remove(callback)
	var wg sync.WaitGroup
	failures := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); failures <- s.migrateAPITokenAuthJSONScope() }()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if updates.Load() != 1 {
		t.Fatal("concurrent startup changed token multiple times", updates.Load())
	}
}
