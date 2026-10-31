package core

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"goapi-tk-register/server/common"
)

func apiExecutionFixture(t *testing.T) (*VendorService, User, uint64, map[string]any) {
	t.Helper()
	store, err := Open("sqlite", filepath.Join(t.TempDir(), "api-execution.sqlite"), strings.Repeat("x", 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	user := User{Email: "api-execution@example.com", PasswordHash: "fixture-hash", Role: "admin", Status: "active", EmailVerifiedAt: time.Now().UTC()}
	if err = store.CreateUser(context.Background(), &user); err != nil {
		t.Fatal(err)
	}
	vendorToken := "shared-vendor-token-fixture"
	if _, err = store.PatchVendor(context.Background(), 1, user.ID, VendorPatch{AccessToken: &vendorToken}); err != nil {
		t.Fatal(err)
	}
	generated, err := store.GenerateAPIToken(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	tokenID, err := ParseBusinessID(generated.Token.ID)
	if err != nil {
		t.Fatal(err)
	}
	params := map[string]any{
		"plan": "claude/cc-pro", "account": "test-account@example.com", "password": "fixture-password", "register_location": "MY",
		"register_time": "2026-10-01", "plan_start_time": "2026-10-01", "plan_end_time": "2026-11-01",
		"auth_json": map[string]any{"id_token": "", "access_token": "fixture-access", "refresh_token": "fixture-refresh", "last_refresh": "2026-10-01T00:00:00Z", "email": "test-account@example.com", "account_uuid": "fixture-uuid", "organization_uuid": "fixture-org", "organization_name": "test", "claude_device_ids": []any{"fixture-device"}, "type": "claude", "expired": "2026-11-01T00:00:00Z", "proxy_url": "http://user:pwd@proxy.example:80", "verification_status": "verified"},
	}
	return &VendorService{Store: store, ErrorLogger: log.New(io.Discard, "", 0)}, user, tokenID, params
}

func apiRemoteResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json; charset=utf-8"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestCreateAPIReturnsCompleteUpstreamResponse(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		body, state string
	}{
		{"success", 201, " {\"id\":13152, \"alias\":\"remote-account\", \"extra\":{\"accepted\":true}}\n", "accepted"},
		{"bad request", 400, `{"error":"invalid parameters","fields":["plan"]}`, "failed"},
		{"unauthorized", 401, `{"error":"invalid vendor credential"}`, "failed"},
		{"duplicate", 409, `{"error":"account already exists"}`, "unknown"},
		{"unavailable", 503, `{"error":"try later","opaque":9007199254740993}`, "unknown"},
		{"redirect", 302, `{"message":"redirect unavailable"}`, "unknown"},
		{"unrecognized success", 200, `{"new_response_shape":true}`, "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service, user, tokenID, params := apiExecutionFixture(t)
			var outboundRequestID string
			calls := 0
			service.Transport = storageTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "POST" || r.URL.String() != VendorOrigin+"/supplier-account/api/openapi/claude/accounts" || r.Header.Get("Authorization") != "Bearer shared-vendor-token-fixture" {
					t.Error("incorrect pinned vendor request")
				}
				outboundRequestID = r.Header.Get("X-Request-ID")
				var body map[string]any
				raw, err := io.ReadAll(r.Body)
				if err != nil || common.Unmarshal(raw, &body) != nil || body["account"] != params["account"] || body["register_location"] != "MY" {
					t.Error("business parameters not forwarded")
				}
				return apiRemoteResponse(tc.status, tc.body), nil
			})
			result, err := service.CreateAPI(context.Background(), user.ID, tokenID, params)
			if err != nil {
				t.Fatal(err)
			}
			if result.HTTPStatus != tc.status || string(result.Body) != tc.body || result.ContentType != "application/json; charset=utf-8" || result.ErrorCode != "" {
				t.Fatalf("upstream status/body/content type altered: %+v", result)
			}
			if result.SubmissionID == "" || result.RequestID == "" || result.RequestID != outboundRequestID || calls != 1 {
				t.Fatal("missing correlated IDs or repeated send")
			}
			var saved Submission
			if err = service.Store.DB.First(&saved, result.SubmissionID).Error; err != nil {
				t.Fatal(err)
			}
			if saved.Source != "api" || saved.APITokenID == nil || *saved.APITokenID != tokenID || saved.Status != tc.state || saved.UserID != user.ID {
				t.Fatalf("incorrect API attribution/state: %+v", saved)
			}
			if (tc.state == "accepted") != (saved.ActiveAccountKey != nil) {
				t.Fatal("only accepted result should keep its account lock")
			}
			var execution ProtocolExecution
			if err = service.Store.DB.Where("submission_id = ?", saved.ID).First(&execution).Error; err != nil {
				t.Fatal(err)
			}
			if execution.HTTPStatus == nil || *execution.HTTPStatus != tc.status || execution.RequestID != result.RequestID || strings.Contains(execution.ResultJSON, "extra") || strings.Contains(execution.ResultJSON, "opaque") {
				t.Fatal("unexpected durable response snapshot")
			}
		})
	}
}

type incompleteAPIBody struct{}

func (incompleteAPIBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (incompleteAPIBody) Close() error             { return nil }

func TestCreateAPITransportFailuresAllowOneManualRetry(t *testing.T) {
	for _, mode := range []string{"connection", "timeout", "partial", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			service, user, tokenID, params := apiExecutionFixture(t)
			calls := 0
			service.Transport = storageTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls > 1 {
					return apiRemoteResponse(200, `{"id":13152}`), nil
				}
				switch mode {
				case "connection":
					return nil, errors.New("connection reset")
				case "timeout":
					return nil, context.DeadlineExceeded
				case "partial":
					response := apiRemoteResponse(200, "")
					response.Body = incompleteAPIBody{}
					return response, nil
				default:
					return apiRemoteResponse(200, strings.Repeat("x", 256<<10+1)), nil
				}
			})
			first, err := service.CreateAPI(context.Background(), user.ID, tokenID, params)
			if err != nil {
				t.Fatal(err)
			}
			wantStatus := 502
			if mode == "timeout" {
				wantStatus = 504
			}
			if first.HTTPStatus != wantStatus || first.ErrorCode == "" || len(first.Body) != 0 || calls != 1 {
				t.Fatalf("bad gateway result or automatic retry: %+v; calls=%d", first, calls)
			}
			second, err := service.CreateAPI(context.Background(), user.ID, tokenID, params)
			if err != nil || second.HTTPStatus != 200 || second.SubmissionID == first.SubmissionID || second.RequestID == first.RequestID || calls != 2 {
				t.Fatal("manual retry not separately recorded", err)
			}
			var original Submission
			if err = service.Store.DB.First(&original, first.SubmissionID).Error; err != nil || original.Status != "unknown" || original.ActiveAccountKey != nil {
				t.Fatal("original unknown history lost", err)
			}
			third, err := service.CreateAPI(context.Background(), user.ID, tokenID, params)
			var business *VendorError
			if !errors.As(err, &business) || business.Code != "ACCOUNT_ALREADY_SUBMITTED" || third.SubmissionID != "" || calls != 2 {
				t.Fatal("accepted retry failed to protect account", err)
			}
			var count int64
			if err = service.Store.DB.Model(&Submission{}).Count(&count).Error; err != nil || count != 2 {
				t.Fatal("unexpected number of attempts", count, err)
			}
		})
	}
}

func TestCreateAPIProtectsSharedCredentialAndDoesNotLogSuccess(t *testing.T) {
	service, user, tokenID, params := apiExecutionFixture(t)
	var logs bytes.Buffer
	service.ErrorLogger = log.New(&logs, "", 0)
	service.Transport = storageTransport(func(*http.Request) (*http.Response, error) {
		return apiRemoteResponse(200, `{"id":13152,"message":"shared-vendor-token-fixture","caller_value":"fixture-password"}`), nil
	})
	result, err := service.CreateAPI(context.Background(), user.ID, tokenID, params)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(result.Body), "shared-vendor-token-fixture") || !strings.Contains(string(result.Body), "[REDACTED]") || !strings.Contains(string(result.Body), "fixture-password") {
		t.Fatal("shared vendor credential exposed or caller response changed unexpectedly")
	}
	if logs.Len() != 0 {
		t.Fatal("successful response was logged")
	}
	quoted := `a"b\c`
	raw, _ := common.Marshal(map[string]string{"error": quoted})
	redacted := redactVendorCredential(raw, quoted)
	var parsed map[string]string
	if common.Unmarshal(redacted, &parsed) != nil || parsed["error"] != "[REDACTED]" {
		t.Fatal("JSON-escaped credential was exposed")
	}
}

func TestCreateAPIUsesSharedValidationAndInFlightProtection(t *testing.T) {
	service, user, tokenID, params := apiExecutionFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	service.Transport = storageTransport(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		close(entered)
		<-release
		return apiRemoteResponse(200, `{"id":13152}`), nil
	})
	params["provider"] = "invalid"
	if _, err := service.CreateAPI(context.Background(), user.ID, tokenID, params); err == nil || calls.Load() != 0 {
		t.Fatal("API accepted parameters rejected by web")
	}
	delete(params, "provider")
	first := make(chan error, 1)
	go func() { _, err := service.CreateAPI(context.Background(), user.ID, tokenID, params); first <- err }()
	<-entered
	_, err := service.CreateAPI(context.Background(), user.ID, tokenID, params)
	close(release)
	if firstErr := <-first; firstErr != nil {
		t.Fatal(firstErr)
	}
	var business *VendorError
	if !errors.As(err, &business) || business.Code != "ACCOUNT_ALREADY_SUBMITTED" || calls.Load() != 1 {
		t.Fatal("concurrent active account was submitted twice", err)
	}
}

func TestUnconfirmedLockMigrationPreservesHistoryAndAcceptedLocks(t *testing.T) {
	service, user, tokenID, params := apiExecutionFixture(t)
	service.Transport = storageTransport(func(*http.Request) (*http.Response, error) { return apiRemoteResponse(503, `{"error":"retry"}`), nil })
	unknown, err := service.CreateAPI(context.Background(), user.ID, tokenID, params)
	if err != nil {
		t.Fatal(err)
	}
	oldKey := "legacy-unknown-lock"
	if err = service.Store.DB.Model(&Submission{}).Where("id = ?", unknown.SubmissionID).Update("active_account_key", oldKey).Error; err != nil {
		t.Fatal(err)
	}
	params["account"] = "accepted@example.com"
	service.Transport = storageTransport(func(*http.Request) (*http.Response, error) { return apiRemoteResponse(200, `{"id":13153}`), nil })
	accepted, err := service.CreateAPI(context.Background(), user.ID, tokenID, params)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Store.migrateVendors(); err != nil {
		t.Fatal(err)
	}
	if err = service.Store.migrateVendors(); err != nil {
		t.Fatal(err)
	}
	var first, second Submission
	if err = service.Store.DB.First(&first, unknown.SubmissionID).Error; err != nil {
		t.Fatal(err)
	}
	if err = service.Store.DB.First(&second, accepted.SubmissionID).Error; err != nil {
		t.Fatal(err)
	}
	if first.Status != "unknown" || first.ActiveAccountKey != nil || second.Status != "accepted" || second.ActiveAccountKey == nil {
		t.Fatal("migration changed history or accepted protection")
	}
	var violations []map[string]any
	if err = service.Store.DB.Raw("PRAGMA foreign_key_check").Scan(&violations).Error; err != nil || len(violations) != 0 {
		t.Fatal("invalid submission/token foreign key", err)
	}
}
