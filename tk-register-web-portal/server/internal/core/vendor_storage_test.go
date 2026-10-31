package core

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"goapi-tk-register/server/common"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm/schema"
)

type storageTransport func(*http.Request) (*http.Response, error)

func (f storageTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestVendorLargeSecretStorageAndCrossDialectType(t *testing.T) {
	parsed, e := schema.Parse(&Submission{}, &sync.Map{}, schema.NamingStrategy{})
	if e != nil {
		t.Fatal(e)
	}
	field := parsed.LookUpField("SecretParametersCiphertext")
	mysqlType := mysql.New(mysql.Config{SkipInitializeWithVersion: true}).DataTypeOf(field)
	if mysqlType != "mediumtext" && mysqlType != "longtext" {
		t.Fatal("MySQL would truncate large encrypted Auth JSON", mysqlType)
	}
	pgType := postgres.New(postgres.Config{}).DataTypeOf(field)
	if pgType != "text" && pgType != "varchar(4194304)" {
		t.Fatal("unexpected PostgreSQL secret type", pgType)
	}
	store, e := Open("sqlite", filepath.Join(t.TempDir(), "vendor.sqlite"), strings.Repeat("q", 32))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	user := User{Email: "storage@example.com", PasswordHash: "fake-test-hash", Role: "admin", Status: "active", EmailVerifiedAt: time.Now().UTC()}
	if e = store.CreateUser(context.Background(), &user); e != nil {
		t.Fatal(e)
	}
	token := "platform-secret"
	vendor, e := store.PatchVendor(context.Background(), 1, user.ID, VendorPatch{AccessToken: &token})
	if e != nil {
		t.Fatal(e)
	}
	auth := map[string]any{"id_token": "", "access_token": "access-secret", "refresh_token": "refresh-secret", "last_refresh": "2026-10-01T00:00:00Z", "email": "account@example.com", "account_uuid": "uuid-secret", "organization_uuid": "org-secret", "organization_name": "test", "claude_device_ids": []any{"device-secret"}, "type": "claude", "expired": "2026-11-01T00:00:00Z", "proxy_url": "http://user:password@proxy.example:80", "verification_status": "verified", "extended_credential": strings.Repeat("p", 1<<20)}
	params := map[string]any{"plan": "claude/cc-pro", "account": "account@example.com", "password": "a1b", "auth_json": auth, "register_location": "US", "register_time": "2026-10-01", "plan_start_time": "2026-10-01", "plan_end_time": "2026-11-01"}
	service := VendorService{Store: store, Transport: storageTransport(func(r *http.Request) (*http.Response, error) {
		raw, e := io.ReadAll(r.Body)
		if e != nil {
			t.Error(e)
		}
		if len(raw) < 1<<20 {
			t.Error("authentication payload was truncated")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"id":12345,"alias":"a1b-platform-secret-access-secret"}`))}, nil
	})}
	result, e := service.Create(context.Background(), user.ID, CreateSubmissionInput{VendorID: vendor.ID, ProtocolVersionID: vendor.Protocols[0].VersionID, ClientRequestKey: "large-secret-request-0001", Parameters: params})
	if e != nil {
		t.Fatal(e)
	}
	id, _ := ParseBusinessID(result.Submission.ID)
	record, e := store.OwnedSubmission(context.Background(), user.ID, id)
	if e != nil {
		t.Fatal(e)
	}
	if len(record.SecretParametersCiphertext) < 65536 {
		t.Fatal("large secret not persisted")
	}
	plain, e := store.decrypt("submission:"+idString(user.ID)+":"+record.ClientRequestKey, record.SecretParametersCiphertext)
	if e != nil {
		t.Fatal(e)
	}
	var saved map[string]any
	if e = common.Unmarshal([]byte(plain), &saved); e != nil {
		t.Fatal(e)
	}
	savedAuth := saved["auth_json"].(map[string]any)
	if savedAuth["extended_credential"] != auth["extended_credential"] || saved["password"] != "a1b" {
		t.Fatal("encrypted DB snapshot differs from submitted secret")
	}
	if strings.Contains(*result.Submission.RemoteAlias, "a1b") || strings.Contains(*result.Submission.RemoteAlias, "platform-secret") || strings.Contains(*result.Submission.RemoteAlias, "access-secret") {
		t.Fatal("short/long secret echoed by remote alias")
	}
	auth["extended_credential"] = strings.Repeat("p", 2<<20)
	if _, _, e := ValidateSubmissionInput(params); e == nil {
		t.Fatal("oversized Auth JSON accepted")
	}
}
func TestVendorDateRangesAndUnknownFields(t *testing.T) {
	now := time.Date(2026, 10, 25, 3, 0, 0, 0, time.UTC)
	if ValidateMetrics(MetricsInput{"2026-10-24 11:00:00", "2026-10-25 10:59:00"}, "Asia/Shanghai", now) != nil {
		t.Fatal("valid vendor local range rejected")
	}
	for _, input := range []MetricsInput{{"2026-10-01 00:00:00", "2026-10-25 00:00:00"}, {"2026-10-24 00:00:00", "2026-10-26 00:00:00"}, {"2026-10-25 00:00:00", "2026-10-24 00:00:00"}} {
		if ValidateMetrics(input, "Asia/Shanghai", now) == nil {
			t.Fatal("invalid metrics range accepted")
		}
	}
	if ValidateBills(BillsInput{"2026-10-01", "2026-10-25"}, "Asia/Shanghai", now) != nil {
		t.Fatal("valid bill range rejected")
	}
	if ValidateBills(BillsInput{"2026-01-01", "2026-10-25"}, "Asia/Shanghai", now) == nil {
		t.Fatal("bill range over90days accepted")
	}
	for _, raw := range []string{`{"accounts":null}`, `{"error":"failed"}`, `{"accounts":[{"id":1},{"id":1}]}`} {
		if _, e := parseMetrics([]byte(raw), "1", MetricsInput{}); e == nil {
			t.Fatal("invalid remote metrics accepted")
		}
	}
	parsed, e := parseMetrics([]byte(`{"accounts":[{"id":1,"aid":0,"requests":null,"prompt_tokens":-1,"success_rate":101}]}`), "1", MetricsInput{})
	if e != nil {
		t.Fatal(e)
	}
	if parsed.Account.AID != nil || parsed.Account.Requests != nil || parsed.Account.PromptTokens != nil || parsed.Account.SuccessRate != nil {
		t.Fatal("unavailable or invalid metrics fabricated")
	}
}
