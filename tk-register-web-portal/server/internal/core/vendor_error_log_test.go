package core

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"unicode/utf8"

	"goapi-tk-register/server/common"
)

func vendorLogBody(t *testing.T, body any) []byte {
	t.Helper()
	raw, err := common.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestVendorErrorLogKeepsUsefulDiagnosticAndRedactsSecrets(t *testing.T) {
	password := "test-password-\"<&>"
	access := "account-access-secret"
	input := map[string]any{"password": password, "auth_json": map[string]any{"access_token": access, "nested": map[string]any{"value": "nested-auth-secret"}}}
	body := map[string]any{
		"detail": "register_location must be US; got MY", "code": "INVALID_LOCATION",
		"echo":         map[string]any{"password": password, "auth_json": input["auth_json"], "headers": map[string]any{"Authorization": "Bearer platform-token-secret"}},
		"message":      "Reflected " + password + " and " + access + " and nested-auth-secret",
		"validation":   []any{map[string]any{"loc": []any{"body", "password"}, "input": "unknown-credential-value", "msg": "Invalid password"}},
		"nested_json":  `{"password":"embedded-unknown-secret","detail":"wrong format"}`,
		"other_secret": "unrecognized-upstream-secret",
	}
	status := 400
	var buffer bytes.Buffer
	service := &VendorService{ErrorLogger: log.New(&buffer, "", 0)}
	service.logExecutionFailure(Submission{ID: 4, VendorID: 1}, ProtocolExecution{ID: 49, ProtocolVersionID: 7, RequestID: "request-id-test", Operation: "push"}, executionResult{status: "failed", code: "UPSTREAM_REJECTED", httpStatus: &status, responseBody: vendorLogBody(t, body)}, input, "platform-token-secret")
	line := buffer.String()
	if strings.Count(line, "\n") != 1 || !strings.HasPrefix(line, "TK_REGISTER_VENDOR_ERROR ") {
		t.Fatalf("expected one structured record: %q", line)
	}
	var event map[string]any
	if err := common.Unmarshal([]byte(strings.TrimPrefix(line, "TK_REGISTER_VENDOR_ERROR ")), &event); err != nil {
		t.Fatal(err)
	}
	if event["submission_id"] != float64(4) || event["execution_id"] != float64(49) || event["protocol_version_id"] != float64(7) || event["request_id"] != "request-id-test" || event["http_status"] != float64(400) {
		t.Fatalf("missing correlation metadata: %#v", event)
	}
	logged := event["body"].(string)
	for _, want := range []string{"register_location must be US; got MY", "INVALID_LOCATION", "wrong format"} {
		if !strings.Contains(logged, want) {
			t.Fatalf("lost diagnostic %q: %s", want, logged)
		}
	}
	for _, secret := range []string{password, access, "nested-auth-secret", "platform-token-secret", "unknown-credential-value", "embedded-unknown-secret", "unrecognized-upstream-secret"} {
		if strings.Contains(logged, secret) || strings.Contains(line, secret) {
			t.Errorf("secret leaked: %q", secret)
		}
	}
}

func TestVendorErrorLogEncodedAuthAndEscapedSecrets(t *testing.T) {
	secret := "escaped-token/with?symbols=\"&"
	auth := string(vendorLogBody(t, map[string]any{"access_token": secret, "nested": []any{"auth-leaf-secret"}}))
	replace := logSecretReplacer([]string{secret})
	if got := replace.Replace(secret); got != "[REDACTED]" {
		t.Fatal("raw secret not redacted")
	}
	// JSON decoding must occur before redaction so Unicode/HTML escapes cannot
	// defeat exact-value replacement.
	body := vendorLogBody(t, map[string]any{"detail": "Invalid MY", "reflected": secret, "other": "auth-leaf-secret"})
	logged, _, reason := safeVendorErrorBody(executionResult{responseBody: body}, map[string]any{"auth_json": auth}, "platform-token")
	if reason != "" || !strings.Contains(logged, "Invalid MY") || strings.Contains(logged, "auth-leaf-secret") || strings.Contains(logged, "escaped-token") {
		t.Fatalf("unsafe encoded auth result: %q reason=%s", logged, reason)
	}
}

func TestVendorErrorLogPlainTextAndMalformedBodies(t *testing.T) {
	cases := []struct{ name, body, want, reason string }{
		{"ordinary text", "Unsupported register_location MY\nPlease use US", "Unsupported register_location MY", ""},
		{"known reflected secret", "Rejected reflected-secret for MY", "[REDACTED]", ""},
		{"malformed credential JSON", `{"detail":"bad","password":"partial`, "", "unstructured_sensitive_response"},
		{"invalid token", "Invalid token", "Invalid token", ""},
		{"expired token", "access token expired", "access token expired", ""},
		{"known password", "Rejected password: reflected-secret", "password=[REDACTED]", ""},
		{"unknown bearer", "Rejected Bearer unknown-value", "Bearer [REDACTED]", ""},
		{"unknown authorization", "Authorization: Bearer unknown-value", "Authorization=[REDACTED]", ""},
		{"url credential", "Cannot connect to https://alice:unknown-value@example.com/path", "https://[REDACTED]@example.com/path", ""},
		{"ambiguous token", "Rejected token unknown-value", "", "unstructured_sensitive_response"},
		{"empty", "", "", "empty_response"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _, reason := safeVendorErrorBody(executionResult{responseBody: []byte(tc.body)}, nil, "reflected-secret")
			if reason != tc.reason || !strings.Contains(body, tc.want) || strings.Contains(body, "reflected-secret") || strings.Contains(body, "unknown-value") {
				t.Fatalf("body=%q reason=%s", body, reason)
			}
		})
	}
}

func TestVendorErrorLogOmitsIncompleteAndBoundsCompleteBody(t *testing.T) {
	for _, result := range []executionResult{
		{responseBody: []byte(`{"password":"partial-value`), responseIncomplete: true},
		{responseBody: []byte(`{"detail":"partial-secret`), responseTruncated: true},
	} {
		body, _, reason := safeVendorErrorBody(result, nil, "partial-secret-token")
		if body != "" || reason != "incomplete_or_oversized_response" {
			t.Fatalf("partial response logged: %q %s", body, reason)
		}
	}
	secret := "boundary-secret"
	raw := vendorLogBody(t, map[string]any{"detail": strings.Repeat("界", 3000) + secret})
	body, truncated, reason := safeVendorErrorBody(executionResult{responseBody: raw}, nil, secret)
	if len(body) > vendorErrorBodyLimit || !truncated || reason != "" || !utf8.ValidString(body) || strings.Contains(body, secret) {
		t.Fatalf("bad bounded response: len=%d truncated=%v reason=%s", len(body), truncated, reason)
	}
}

func TestVendorErrorLogDoesNotLogSuccessOrMultilineRecords(t *testing.T) {
	var buffer bytes.Buffer
	service := &VendorService{ErrorLogger: log.New(&buffer, "", 0)}
	service.logExecutionFailure(Submission{}, ProtocolExecution{}, executionResult{status: "succeeded", responseBody: []byte("never logged")}, nil, "")
	if buffer.Len() != 0 {
		t.Fatal("successful body was logged")
	}
	status := 500
	service.logExecutionFailure(Submission{ID: 4}, ProtocolExecution{RequestID: "trusted-request", Operation: "push"}, executionResult{status: "failed", httpStatus: &status, responseBody: []byte("bad MY\nFAKE_LOG_LINE\r\n")}, nil, "")
	if strings.Count(buffer.String(), "\n") != 1 {
		t.Fatalf("log injection allowed: %q", buffer.String())
	}
	var event map[string]any
	if err := common.Unmarshal([]byte(strings.TrimPrefix(buffer.String(), "TK_REGISTER_VENDOR_ERROR ")), &event); err != nil {
		t.Fatal(err)
	}
}
