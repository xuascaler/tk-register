package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"goapi-tk-register/server/internal/core"
	"goapi-tk-register/server/internal/diagnostics"
)

func credentialDiagnosticEvents(t *testing.T, logs *bytes.Buffer) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if !strings.HasPrefix(line, "TK_CREDENTIAL_AUTH ") {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "TK_CREDENTIAL_AUTH ")), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	return events
}

func TestCookieAuthorizationDiagnosticsAcrossRealEntries(t *testing.T) {
	for _, entry := range []string{"submit", "update", "deactivate"} {
		t.Run(entry, func(t *testing.T) {
			a, _, cookie, csrf, target, editor := setupAuthUpdate(t)
			route := "credential-flows/import"
			if entry == "update" {
				route = authFlowRoute(target.ID, "import")
			}
			if entry == "deactivate" {
				route = deactivateRoute(target.ID, "import")
			}
			var helperID string
			var calls atomic.Int32
			credentialHelperFixture(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
				calls.Add(1)
				helperID = r.Header.Get("X-Request-ID")
				if r.URL.Path != "/import" || r.Header.Get("X-TK-Credential-Entry") != entry || r.Header.Get("X-TK-Credential-Method") != "cookie" || len(body) != 4 {
					t.Error("helper correlation headers or strict payload changed")
				}
				w.WriteHeader(502)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
					"code": "upstream_rejected", "message": "private-cookie private-password private-access-token account@example.com",
					"stage": "cookie_organizations", "upstream_status": 403, "upstream_code": "permission_error", "response_kind": "html",
				}})
			})
			var logs bytes.Buffer
			w := diagnosticRequest(t, a, route, map[string]any{"proxy_url": testCredentialProxy, "kind": "cookie", "session_key": "sk-ant-sid-private-cookie"}, cookie, csrf, &logs)
			requireStatus(t, w, 502)
			var reply struct {
				Success       bool `json:"success"`
				Code, Message string
				Data          struct {
					Diagnostics credentialAuthorizationDiagnostics `json:"diagnostics"`
				} `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
				t.Fatal(err)
			}
			d := reply.Data.Diagnostics
			if reply.Success || reply.Code != "AUTHORIZATION_REJECTED" || d.RequestID == "" || d.RequestID != w.Header().Get("X-Request-ID") || d.RequestID != helperID || d.RequestID == "client-controlled-correlation" || d.Stage != "cookie_organizations" || d.UpstreamStatus != 403 || d.UpstreamCode != "permission_error" || d.ResponseKind != "html" {
				t.Fatalf("inaccurate credential diagnostics: %s", w.Body.String())
			}
			if strings.Contains(strings.ToLower(reply.Message), "expired") || strings.Contains(strings.ToLower(reply.Message), "cookie") {
				t.Fatal("a provider 403 must not be diagnosed as an expired Cookie")
			}
			events := credentialDiagnosticEvents(t, &logs)
			if len(events) != 1 {
				t.Fatalf("expected one failure event, got %d", len(events))
			}
			e := events[0]
			if e["trace_id"] != helperID || e["entry"] != entry || e["operation"] != "import" || e["method"] != "cookie" || e["stage"] != "cookie_organizations" || e["helper_http_status"] != float64(502) || e["upstream_http_status"] != float64(403) || e["helper_code"] != "upstream_rejected" || e["upstream_code"] != "permission_error" || e["response_kind"] != "html" {
				t.Fatalf("inaccurate safe log: %+v", e)
			}
			if calls.Load() != 1 || editor.writes != 0 || editor.refreshes != 0 || len(authAudits(t, a)) != 0 || len(generationAudits(t, a)) != 0 {
				t.Fatal("a failed authorization performed another credential or supplier operation")
			}
			for _, secret := range []string{"private-cookie", "private-password", "private-access-token", "account@example.com", testCredentialProxy, "helper-secret-", "sk-ant-sid"} {
				if strings.Contains(logs.String(), secret) || strings.Contains(w.Body.String(), secret) {
					t.Fatalf("credential secret leaked: %q", secret)
				}
			}
		})
	}
}

func TestCredentialHelperDiagnosticsDropUntrustedFields(t *testing.T) {
	for _, body := range []string{
		`{"error":{"code":"private-secret-code","message":"private-secret-message","stage":"private-secret-stage","upstream_status":700,"upstream_code":"private-secret-upstream","response_kind":"private-secret-kind","request_id":"private-secret-request"}}`,
		`{"error":{"code":"upstream_rejected","stage":[],"upstream_status":"403","upstream_code":{},"response_kind":17}}`,
		`{"error":{"code":"upstream_rejected","upstream_status":403.5}}`,
		`{"error":{"code":"upstream_rejected","upstream_status":-1}}`,
		`{"error":{"code":"upstream_rejected","upstream_status":600}}`,
		`<html>private-secret-response</html>`,
	} {
		t.Run(body[:min(len(body), 50)], func(t *testing.T) {
			credentialHelperFixture(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
				w.WriteHeader(502)
				_, _ = w.Write([]byte(body))
			})
			var logs bytes.Buffer
			ctx := diagnostics.WithLogger(diagnostics.WithTrace(context.Background(), diagnostics.Trace{TraceID: "safe-request_123"}), log.New(&logs, "", 0))
			_, err := (&app{}).callCredentialHelper(ctx, 7, "/import", map[string]any{"kind": "cookie", "session_key": "private-secret-cookie"})
			var authorization *credentialAuthorizationError
			var vendor *core.VendorError
			if !errors.As(err, &authorization) || !errors.As(err, &vendor) {
				t.Fatalf("missing typed/unwrap contract: %T", err)
			}
			d := authorization.diagnostics
			if d.RequestID != "safe-request_123" || d.Stage != "helper_transport" || d.UpstreamStatus != 0 || d.UpstreamCode != "" || d.ResponseKind != "" {
				t.Fatalf("unsafe diagnostic: %+v", d)
			}
			w := httptest.NewRecorder()
			w.Header().Set("X-Request-ID", "authoritative-response-id")
			vendorFailure(w, err)
			if !strings.Contains(w.Body.String(), `"request_id":"authoritative-response-id"`) || strings.Contains(w.Body.String(), "private-secret") || strings.Contains(logs.String(), "private-secret") {
				t.Fatal("untrusted metadata or body escaped the whitelist")
			}
			if len(credentialDiagnosticEvents(t, &logs)) != 1 {
				t.Fatal("missing single failure event")
			}
		})
	}
}

func TestCredentialHelperDiagnosticStatusAndCodeBoundaries(t *testing.T) {
	for _, status := range []int{100, 401, 403, 429, 500, 599} {
		raw, _ := json.Marshal(map[string]any{"error": map[string]any{"code": "upstream_rejected", "stage": "oauth_token", "upstream_status": status, "upstream_code": "invalid_grant", "response_kind": "json", "request_id": "helper-must-not-select-the-trace"}})
		code, d := credentialHelperDiagnostic(raw)
		if code != "upstream_rejected" || d.UpstreamStatus != status || d.UpstreamCode != "invalid_grant" || d.Stage != "oauth_token" || d.ResponseKind != "json" || d.RequestID != "" {
			t.Fatalf("incorrect safe helper diagnostic: %+v", d)
		}
	}
}

func TestCredentialDiagnosticsDoNotTurnUpstream401IntoSession401(t *testing.T) {
	for _, test := range []struct {
		helperStatus   int
		code           string
		upstreamStatus int
		expectedStatus int
		expectedCode   string
	}{
		{502, "upstream_rejected", 401, 502, "AUTHORIZATION_REJECTED"},
		{502, "upstream_rejected", 403, 502, "AUTHORIZATION_REJECTED"},
		{401, "unauthorized", 0, 503, "CREDENTIAL_HELPER_UNAVAILABLE"},
	} {
		t.Run(test.code+http.StatusText(test.upstreamStatus), func(t *testing.T) {
			credentialHelperFixture(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
				w.WriteHeader(test.helperStatus)
				failure := map[string]any{"code": test.code, "stage": "cookie_organizations"}
				if test.upstreamStatus != 0 {
					failure["upstream_status"] = test.upstreamStatus
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"error": failure})
			})
			var logs bytes.Buffer
			ctx := diagnostics.WithLogger(context.Background(), log.New(&logs, "", 0))
			_, err := (&app{}).callCredentialHelper(ctx, 7, "/import", map[string]any{"kind": "cookie"})
			w := httptest.NewRecorder()
			vendorFailure(w, err)
			if w.Code != test.expectedStatus || !strings.Contains(w.Body.String(), `"code":"`+test.expectedCode+`"`) {
				t.Fatalf("incorrect status/code: %d %s", w.Code, w.Body.String())
			}
			events := credentialDiagnosticEvents(t, &logs)
			if len(events) != 1 || events[0]["helper_http_status"] != float64(test.helperStatus) {
				t.Fatal("helper status lost")
			}
			if test.upstreamStatus == 0 {
				if _, present := events[0]["upstream_http_status"]; present {
					t.Fatal("helper HTTP status misreported as provider status")
				}
			} else if events[0]["upstream_http_status"] != float64(test.upstreamStatus) {
				t.Fatal("upstream status lost")
			}
		})
	}
}

func TestCredentialHelperTransportFailuresAreLoggedWithoutInventedUpstreamStatus(t *testing.T) {
	for _, test := range []string{"configuration", "network", "invalid_response"} {
		t.Run(test, func(t *testing.T) {
			switch test {
			case "configuration":
				t.Setenv("TK_REGISTER_CREDENTIAL_HELPER_TOKEN", "private-secret-short-token")
			case "network":
				server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
				server.Close()
				t.Setenv("TK_REGISTER_CREDENTIAL_HELPER_TOKEN", strings.Repeat("helper-secret-", 4))
				t.Setenv("TK_REGISTER_CREDENTIAL_HELPER_URL", server.URL)
			case "invalid_response":
				credentialHelperFixture(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
					_, _ = w.Write([]byte(`{"private-secret":"not a credential flow"}`))
				})
			}
			var logs bytes.Buffer
			ctx := diagnostics.WithLogger(context.Background(), log.New(&logs, "", 0))
			_, err := (&app{}).callCredentialHelper(ctx, 7, "/import", map[string]any{"kind": "cookie", "session_key": "private-secret-cookie"})
			var authorization *credentialAuthorizationError
			if !errors.As(err, &authorization) {
				t.Fatalf("unwrapped helper failure: %T", err)
			}
			events := credentialDiagnosticEvents(t, &logs)
			if len(events) != 1 || events[0]["stage"] != "helper_transport" {
				t.Fatal("missing transport failure diagnostic")
			}
			if _, present := events[0]["upstream_http_status"]; present {
				t.Fatal("invented an upstream response on a local transport failure")
			}
			if strings.Contains(logs.String(), "private-secret") || strings.Contains(logs.String(), "helper-secret-") || strings.Contains(logs.String(), "http://") {
				t.Fatal("unsafe local transport detail logged")
			}
		})
	}
}

func TestCredentialHelperStoredMethodAndFixedHeaders(t *testing.T) {
	for _, method := range []string{"oauth", "setup", "json", "cookie", "token", "private-secret-method"} {
		t.Run(method, func(t *testing.T) {
			want := method
			if method == "private-secret-method" {
				want = "unknown"
			}
			credentialHelperFixture(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
				if r.Header.Get("X-TK-Credential-Entry") != "deactivate" || r.Header.Get("X-TK-Credential-Method") != want || r.Header.Get("X-Request-ID") != "safe-server-trace" {
					t.Error("unsafe or incorrect helper correlation headers")
				}
				w.WriteHeader(502)
				_, _ = w.Write([]byte(`{"error":{"code":"upstream_rejected","stage":"oauth_token"}}`))
			})
			var logs bytes.Buffer
			ctx := diagnostics.WithTrace(context.Background(), diagnostics.Trace{TraceID: "safe-server-trace"})
			ctx = diagnostics.WithLogger(ctx, log.New(&logs, "", 0))
			ctx = context.WithValue(ctx, deactivateFlowContextKey{}, authJSONFlowTarget{})
			ctx = withAuthJSONFlowMethod(ctx, method)
			_, _ = (&app{}).callCredentialHelper(ctx, 7, "/verify", map[string]any{"flow_id": testCredentialFlowID})
			events := credentialDiagnosticEvents(t, &logs)
			if len(events) != 1 || events[0]["method"] != want || events[0]["entry"] != "deactivate" || strings.Contains(logs.String(), "private-secret") {
				t.Fatal("bound method was not sanitized")
			}
		})
	}
}

func TestCredentialHelperErrorRefinementsRemainSafe(t *testing.T) {
	for _, test := range []struct {
		helperCode, publicCode string
		status                 int
	}{
		{"proxy_authentication_failed", "PROXY_AUTHENTICATION_FAILED", 502},
		{"organization_missing", "AUTHORIZATION_ORGANIZATION_MISSING", 502},
		{"invalid_token_response", "AUTHORIZATION_INVALID_RESPONSE", 502},
		{"authorization_response_invalid", "AUTHORIZATION_INVALID_RESPONSE", 502},
		{"upstream_invalid_response", "AUTHORIZATION_INVALID_RESPONSE", 502},
		{"invalid_session_key", "INVALID_CREDENTIAL", 400},
		{"invalid_token", "INVALID_CREDENTIAL", 400},
		{"upstream_rejected", "AUTHORIZATION_REJECTED", 502},
		{"private-cookie-error", "AUTHORIZATION_FAILED", 502},
	} {
		t.Run(test.helperCode, func(t *testing.T) {
			var problem *core.VendorError
			if !errors.As(credentialHelperFailure(test.helperCode), &problem) || problem.Code != test.publicCode || problem.Status != test.status || strings.Contains(problem.Message, "private-cookie-error") {
				t.Fatal("incorrect safe error refinement")
			}
		})
	}
}
