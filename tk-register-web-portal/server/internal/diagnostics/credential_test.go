package diagnostics

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"strings"
	"testing"
)

func TestCredentialAuthLoggerWhitelistsAllFreeTextAndStatuses(t *testing.T) {
	var output bytes.Buffer
	ctx := WithLogger(WithTrace(context.Background(), Trace{TraceID: "safe-request"}), log.New(&output, "", 0))
	LogCredentialAuth(ctx, CredentialAuthEvent{Entry: "private-secret", Operation: "private-secret", Method: "private-secret", Code: "private-secret", HelperCode: "private-secret", Stage: "private-secret", UpstreamCode: "private-secret", ResponseKind: "private-secret", HelperStatus: 700, UpstreamStatus: -1, DurationMS: -1})
	if strings.Contains(output.String(), "private-secret") {
		t.Fatal("free text appeared in credential log")
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(output.String(), "TK_CREDENTIAL_AUTH "))), &event); err != nil {
		t.Fatal(err)
	}
	if event["trace_id"] != "safe-request" || event["method"] != "unknown" || event["helper_code"] != "unknown" || event["duration_ms"] != float64(0) {
		t.Fatalf("incorrect bounded log: %+v", event)
	}
	for _, key := range []string{"stage", "helper_http_status", "upstream_http_status", "upstream_code", "response_kind"} {
		if _, present := event[key]; present {
			t.Fatalf("untrusted field was not omitted: %s", key)
		}
	}
}

func TestCredentialDiagnosticEnums(t *testing.T) {
	for _, stage := range []string{"request_validation", "proxy_check", "cookie_organizations", "cookie_authorize", "oauth_token", "profile", "auth_json_conversion", "helper_transport"} {
		if CredentialStage(stage) != stage {
			t.Fatalf("missing supported stage %s", stage)
		}
	}
	for _, code := range []string{"invalid_grant", "invalid_scope", "authentication_error", "permission_error", "invalid_request", "rate_limit_error", "overloaded_error", "invalid_client", "unauthorized_client", "unsupported_grant_type"} {
		if CredentialUpstreamCode(code) != code {
			t.Fatalf("missing supported provider code %s", code)
		}
	}
	if CredentialStage("https://private.example") != "" || CredentialUpstreamCode("account@example.com") != "" || CredentialResponseKind("Bearer private-token") != "" {
		t.Fatal("diagnostic metadata accepted arbitrary text")
	}
}
