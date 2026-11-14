package core

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"strings"
	"testing"
	"time"

	"goapi-tk-register/server/common"
)

func TestVendorFailureBodyLoggedWithoutLeakingIntoAPI(t *testing.T) {
	for _, operation := range []string{"submit_account", "metrics", "bills"} {
		t.Run(operation, func(t *testing.T) {
			store := testStore(t)
			ctx := context.Background()
			user := User{Email: "logging@example.com", PasswordHash: "test-hash", Role: "admin", Status: "active", EmailVerifiedAt: time.Now().UTC()}
			if err := store.CreateUser(ctx, &user); err != nil {
				t.Fatal(err)
			}
			token := "platform-log-test-secret"
			vendor, err := store.PatchVendor(ctx, 1, user.ID, VendorPatch{AccessToken: &token})
			if err != nil {
				t.Fatal(err)
			}
			auth := map[string]any{"id_token": "id-log-test-secret", "access_token": "access-log-test-secret", "refresh_token": "refresh-log-test-secret", "last_refresh": "2026-10-01T00:00:00Z", "email": "account@example.com", "account_uuid": "account-uuid-secret", "organization_uuid": "organization-uuid-secret", "organization_name": "test", "claude_device_ids": []any{"device-log-test-secret"}, "type": "claude", "expired": "2026-11-01T00:00:00Z", "proxy_url": "http://user:proxy-log-test-secret@proxy.example:80", "verification_status": "verified"}
			params := map[string]any{"plan": "claude/cc-pro", "account": "account@example.com", "password": "password-log-test-secret", "auth_json": auth, "register_location": "MY", "register_time": "2026-10-01", "plan_start_time": "2026-10-01", "plan_end_time": "2026-11-01"}
			response, err := common.Marshal(map[string]any{"detail": "Unsupported register_location: MY", "input": params, "authorization": "Bearer " + token, "echo": "password-log-test-secret / access-log-test-secret"})
			if err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			calls := 0
			service := VendorService{Store: store, ErrorLogger: log.New(&logs, "", 0), Transport: storageTransport(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(response))}, nil
			})}
			key := "test-failure-logging-0001"
			version := vendor.Protocols[0].VersionID
			var detail SubmissionDetail
			if operation == "submit_account" {
				detail, err = service.Create(ctx, user.ID, CreateSubmissionInput{VendorID: vendor.ID, ProtocolVersionID: version, ClientRequestKey: key, Parameters: params})
			} else {
				versionID, _ := ParseBusinessID(version)
				secretJSON, _ := jsonText(map[string]any{"password": params["password"], "auth_json": auth})
				encrypted, cipherErr := store.encrypt("submission:"+idString(user.ID)+":"+key, secretJSON)
				if cipherErr != nil {
					t.Fatal(cipherErr)
				}
				remote := "12345"
				record := Submission{UserID: user.ID, VendorID: 1, ProtocolVersionID: versionID, ClientRequestKey: key, RequestHash: "test-log-hash", Account: "account@example.com", Plan: "claude/cc-pro", ParametersJSON: `{"register_location":"MY"}`, SecretParametersCiphertext: encrypted, Status: "accepted", RemoteID: &remote}
				if err := store.DB.Create(&record).Error; err != nil {
					t.Fatal(err)
				}
				loc, _ := time.LoadLocation("Asia/Shanghai")
				end := time.Now().In(loc).Add(-time.Minute)
				detail, err = service.Query(ctx, user.ID, record.ID, operation, QueryInput{Metrics: MetricsInput{BeginTime: end.Add(-time.Hour).Format("2006-01-02 15:04:05"), EndTime: end.Format("2006-01-02 15:04:05")}, Bills: BillsInput{StartDS: end.Format("2006-01-02"), EndDS: end.Format("2006-01-02")}})
			}
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("failure caused %d calls; must not retry", calls)
			}
			if len(detail.Executions) != 1 || detail.Executions[0].HTTPStatus == nil || *detail.Executions[0].HTTPStatus != 400 {
				t.Fatal("failed HTTP execution was not persisted")
			}
			logged := logs.String()
			if !strings.Contains(logged, "Unsupported register_location: MY") || !strings.Contains(logged, detail.Executions[0].RequestID) {
				t.Fatalf("missing error reason/request correlation: %s", logged)
			}
			for _, secret := range []string{token, "password-log-test-secret", "access-log-test-secret", "refresh-log-test-secret", "proxy-log-test-secret"} {
				if strings.Contains(logged, secret) {
					t.Fatal("log exposed a credential")
				}
			}
			encoded, err := common.Marshal(detail)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(encoded), "Unsupported register_location") || strings.Contains(string(encoded), "access-log-test-secret") {
				t.Fatal("diagnostic body escaped into public detail")
			}
			var saved ProtocolExecution
			if err := store.DB.Where("request_id = ?", detail.Executions[0].RequestID).First(&saved).Error; err != nil {
				t.Fatal(err)
			}

			if saved.ErrorBodyCiphertext == "" || strings.Contains(saved.ErrorBodyCiphertext, "access-log-test-secret") {
				t.Fatal("raw error must be encrypted")
			}
			raw, err := store.decrypt("execution-error:"+idString(saved.ID), saved.ErrorBodyCiphertext)
			if err != nil || raw != string(response) {
				t.Fatal("raw response did not survive persistence")
			}
			if !strings.Contains(saved.ErrorBody, "Unsupported register_location") {
				t.Fatal("missing safe diagnostic")
			}
			for _, secret := range []string{token, "password-log-test-secret", "access-log-test-secret", "refresh-log-test-secret", "proxy-log-test-secret"} {
				if strings.Contains(string(encoded), secret) || strings.Contains(saved.ErrorBody, secret) {
					t.Fatal("public diagnostic leaked secret")
				}
			}
			if saved.ResultJSON != "" {
				t.Fatal("raw diagnostic body was persisted into public result")
			}
		})
	}
}

type incompleteVendorBody struct{ read bool }

func (b *incompleteVendorBody) Read(p []byte) (int, error) {
	if b.read {
		return 0, io.EOF
	}
	b.read = true
	return copy(p, `{"password":"partial-secret`), io.ErrUnexpectedEOF
}
func (*incompleteVendorBody) Close() error { return nil }

func TestVendorFailureCapturesOnlyBoundedUnsuccessfulResponses(t *testing.T) {
	for _, tc := range []struct {
		name                            string
		status                          int
		body                            func() io.ReadCloser
		wantBody, truncated, incomplete bool
	}{
		{"rejection", 400, func() io.ReadCloser { return io.NopCloser(strings.NewReader(`{"detail":"bad location"}`)) }, true, false, false},
		{"invalid success body", 200, func() io.ReadCloser { return io.NopCloser(strings.NewReader(`{"unexpected":true}`)) }, true, false, false},
		{"oversized", 500, func() io.ReadCloser { return io.NopCloser(strings.NewReader(strings.Repeat("x", 300<<10))) }, true, true, false},
		{"incomplete", 400, func() io.ReadCloser { return &incompleteVendorBody{} }, true, false, true},
		{"success", 200, func() io.ReadCloser { return io.NopCloser(strings.NewReader(`{"id":12345}`)) }, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := VendorService{Transport: storageTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Header: http.Header{}, Body: tc.body()}, nil
			})}
			result := service.execute(Vendor{BaseURL: "https://vendor.example"}, queryErrorTestVersion(t, "submit_account"), "test-log-capture", map[string]any{}, nil, "test-token")
			if (len(result.responseBody) > 0) != tc.wantBody || result.responseTruncated != tc.truncated || result.responseIncomplete != tc.incomplete {
				t.Fatal("incorrect diagnostic capture")
			}
			if len(result.responseBody) > 256<<10+1 {
				t.Fatal("unbounded error response")
			}
		})
	}
}
