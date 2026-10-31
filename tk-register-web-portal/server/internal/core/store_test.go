package core

import (
	"context"
	mysqldriver "github.com/go-sql-driver/mysql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm/clause"
)

const testKey = "test-only-32-byte-encryption-key-not-for-production"

func TestMySQLDSNTimeParsingAndTimeouts(t *testing.T) {
	dsn, err := mysqlDSN("test:test@tcp(localhost:3306)/tk_register")
	if err != nil {
		t.Fatal(err)
	}
	config, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if !config.ParseTime || config.Loc != time.UTC || config.Timeout != 10*time.Second || config.ReadTimeout != 15*time.Second || config.WriteTimeout != 15*time.Second {
		t.Fatal("missing safe database connection defaults")
	}
}

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open("sqlite", filepath.Join(t.TempDir(), "tk.sqlite"), testKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestUserIdentityVerifiedAndUnique(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := &User{Email: "  Someone@Example.com  ", Role: "member", PasswordHash: "hash", EmailVerifiedAt: time.Now().UTC()}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if u.ID == 0 || u.Email != "someone@example.com" || u.CreatedAt.IsZero() || u.UpdatedAt.IsZero() {
		t.Fatalf("invalid persisted user: %#v", u)
	}
	got, err := s.GetUserByEmail(ctx, "Someone@Example.com")
	if err != nil || got.ID != u.ID {
		t.Fatal("canonical lookup failed", err)
	}
	copy := *u
	copy.ID = 0
	if err = s.CreateUser(ctx, &copy); err == nil {
		t.Fatal("duplicate email was accepted")
	}
	if err = s.CreateUser(ctx, &User{Email: "unverified@example.com", Role: "member", PasswordHash: "hash"}); err == nil {
		t.Fatal("unverified registration accepted")
	}
	if err = s.CreateUser(ctx, &User{Email: "username", Role: "member", PasswordHash: "hash", EmailVerifiedAt: time.Now()}); err == nil {
		t.Fatal("non-email registration accepted")
	}
}

func TestSettingsSecretsAndAtomicValidation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	secret := "test-SMTP-secret"
	if err := s.PatchSettings(ctx, map[string]string{"smtp_password": secret, "smtp_host": "smtp.example.com"}, 7); err != nil {
		t.Fatal(err)
	}
	var row Setting
	if err := s.DB.Where(clause.Eq{Column: "key", Value: "smtp_password"}).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(row.Value, secret) || !strings.HasPrefix(row.Value, "enc:v1:") {
		t.Fatal("secret not encrypted")
	}
	values, err := s.Settings(ctx)
	if err != nil || values.String("smtp_password") != secret {
		t.Fatal("secret did not round trip", err)
	}
	descriptors, err := s.Descriptors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(descriptors) != 25 {
		t.Fatalf("got %d settings", len(descriptors))
	}
	for _, d := range descriptors {
		if d.Key == "smtp_password" && (d.Value != "" || !d.Configured || !d.Secret) {
			t.Fatal("secret disclosure or missing presence flag")
		}
	}
	if err = s.PatchSettings(ctx, map[string]string{"smtp_password": ""}, 7); err != nil {
		t.Fatal(err)
	}
	values, _ = s.Settings(ctx)
	if values.String("smtp_password") != secret {
		t.Fatal("blank password erased existing secret")
	}
	if err = s.PatchSettings(ctx, map[string]string{"smtp_host": "changed.example.com", "verification_max_attempts": "0"}, 7); err == nil {
		t.Fatal("invalid attempt limit accepted")
	}
	values, _ = s.Settings(ctx)
	if values.String("smtp_host") != "smtp.example.com" {
		t.Fatal("invalid patch partially committed")
	}
	if err = s.PatchSettings(ctx, map[string]string{"email_verification_required": "false"}, 7); err == nil {
		t.Fatal("immutable policy bypass accepted")
	}
	if err = s.PatchSettings(ctx, map[string]string{"password_login_enabled": "false", "email_code_login_enabled": "false"}, 7); err == nil {
		t.Fatal("disabled all member login methods")
	}
	if err = s.PatchSettings(ctx, map[string]string{"admin_session_absolute_ttl_seconds": "600", "admin_session_idle_ttl_seconds": "900"}, 7); err == nil {
		t.Fatal("invalid session lifetimes accepted")
	}
	if _, err = s.decrypt("different-setting", row.Value); err == nil {
		t.Fatal("ciphertext could be copied across setting keys")
	}
}

func TestSMTPImportOnceAndExplicitConfigWins(t *testing.T) {
	ctx := context.Background()
	sourcePath := filepath.Join(t.TempDir(), "goapi.sqlite")
	source, err := openDB("sqlite", sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	db, _ := source.DB()
	defer db.Close()
	type option struct {
		Key   string `gorm:"primaryKey"`
		Value string
	}
	if err = source.Table("options").AutoMigrate(&option{}); err != nil {
		t.Fatal(err)
	}
	for _, o := range []option{{"SMTPServer", "smtp.source.example"}, {"SMTPPort", "465"}, {"SMTPAccount", "sender@example.com"}, {"SMTPFrom", "noreply@example.com"}, {"SMTPToken", "source-password"}} {
		if err = source.Table("options").Create(&o).Error; err != nil {
			t.Fatal(err)
		}
	}
	s := testStore(t)
	if err = s.ImportGoAPISMTP(ctx, "sqlite", sourcePath); err != nil {
		t.Fatal(err)
	}
	values, _ := s.Settings(ctx)
	if values.String("smtp_password") != "source-password" || values.String("smtp_tls_mode") != "tls" || values.String("smtp_from_name") != "GoAPI" {
		t.Fatal("wrong imported values")
	}
	if err = s.PatchSettings(ctx, map[string]string{"smtp_host": "tk.example.com"}, 1); err != nil {
		t.Fatal(err)
	}
	if err = s.ImportGoAPISMTP(ctx, "sqlite", sourcePath); err != nil {
		t.Fatal(err)
	}
	values, _ = s.Settings(ctx)
	if values.String("smtp_host") != "tk.example.com" {
		t.Fatal("reimport overwrote TK config")
	}
	manual := testStore(t)
	if err = manual.PatchSettings(ctx, map[string]string{"smtp_host": "manual.example.com"}, 1); err != nil {
		t.Fatal(err)
	}
	if err = manual.ImportGoAPISMTP(ctx, "sqlite", sourcePath); err != nil {
		t.Fatal(err)
	}
	values, _ = manual.Settings(ctx)
	if values.String("smtp_host") != "manual.example.com" {
		t.Fatal("initial import overwrote explicit config")
	}
	var records []option
	if err = source.Table("options").Find(&records).Error; err != nil {
		t.Fatal(err)
	}
	if len(records) != 5 {
		t.Fatal("source database changed")
	}
}

func TestVerificationAuditDoesNotNeedCode(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	audit := &EmailVerification{RequestID: "unique-request", Email: "someone@example.com", Purpose: "registration", Status: "sending", ExpiresAt: time.Now().Add(10 * time.Minute)}
	if err := s.RecordVerification(ctx, audit); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateVerification(ctx, audit.RequestID, "sent"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateVerification(ctx, audit.RequestID, "consumed"); err != nil {
		t.Fatal(err)
	}
	var got EmailVerification
	if err := s.DB.First(&got, audit.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.SentAt == nil || got.ConsumedAt == nil || got.Status != "consumed" {
		t.Fatal("audit lifecycle missing")
	}
	dup := *audit
	dup.ID = 0
	if err := s.RecordVerification(ctx, &dup); err == nil {
		t.Fatal("duplicate request ID accepted")
	}
}

func TestWrongEncryptionKeyFailsStartup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tk.sqlite")
	s, err := Open("sqlite", path, testKey)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.PatchSettings(context.Background(), map[string]string{"smtp_password": "protected"}, 1); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err = Open("sqlite", path, strings.Repeat("x", 32)); err == nil {
		t.Fatal("changed encryption key silently accepted")
	}
}

func TestValidationErrorsAreDistinguishableFromDatabaseFailures(t *testing.T) {
	s := testStore(t)
	err := s.PatchSettings(context.Background(), map[string]string{"smtp_port": "invalid"}, 1)
	if !IsValidationError(err) {
		t.Fatal("invalid settings should produce a validation error")
	}
	s.Close()
	err = s.PatchSettings(context.Background(), map[string]string{"smtp_port": "587"}, 1)
	if err == nil || IsValidationError(err) {
		t.Fatal("database failure must not be exposed as a validation error")
	}
}
