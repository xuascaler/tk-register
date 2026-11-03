package supplier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"goapi-tk-register/server/internal/diagnostics"
)

func diagnosticEvents(t *testing.T, output string) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "TK_SUPPLIER_REQUEST ") {
			t.Fatal("unexpected log line")
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "TK_SUPPLIER_REQUEST ")), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	return events
}

func diagnosticContext(output *bytes.Buffer) context.Context {
	ctx := diagnostics.WithTrace(context.Background(), diagnostics.Trace{TraceID: "sync-fixture-001", Source: "manual_sync", Vendor: "tk-register-0", ActorID: 4, AccountID: 17})
	return diagnostics.WithLogger(ctx, log.New(output, "", 0))
}

type diagnosticUnreadBody struct{ read bool }

func (b *diagnosticUnreadBody) Read([]byte) (int, error) {
	b.read = true
	return 0, errors.New("sensitive-error-response")
}
func (*diagnosticUnreadBody) Close() error { return nil }

func TestSupplierDiagnosticsIdentifies429StageWithoutReadingSecrets(t *testing.T) {
	t.Setenv("TK_REGISTER_SUPPLIER_DIAGNOSTICS", "all")
	_, publicKey := keys(t)
	for i, stage := range []string{"pubkey", "login", "accounts"} {
		t.Run(stage, func(t *testing.T) {
			var output bytes.Buffer
			ctx := diagnosticContext(&output)
			body := &diagnosticUnreadBody{}
			calls := 0
			client := &Client{transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Path == apiPrefix+"/"+stage {
					return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{"30"}}, Body: body}, nil
				}
				if req.URL.Path == apiPrefix+"/pubkey" {
					return jsonResponse(200, map[string]string{"public_key": publicKey}), nil
				}
				if req.URL.Path == apiPrefix+"/login" {
					return jsonResponse(200, map[string]string{"token": "private-login-token"}), nil
				}
				t.Fatal("unexpected upstream path")
				return nil, nil
			})}
			results, err := client.CollectTargets(ctx, "private-user@example.com", "private-login-password", []Target{{Account: "private-account@example.com"}})
			if stage == "accounts" {
				if err != nil || len(results) != 1 {
					t.Fatal("query failure shape changed", err)
				}
				err = results[0].Err
			}
			var upstream *Error
			if !errors.As(err, &upstream) || upstream.Code != CodeRateLimited {
				t.Fatal("rate-limit behavior changed", err)
			}
			if calls != i+1 || body.read {
				t.Fatal("added requests or read private error body")
			}
			events := diagnosticEvents(t, output.String())
			if len(events) != calls || diagnostics.RequestCount(ctx) != int64(calls) {
				t.Fatal("missing actual request events")
			}
			last := events[len(events)-1]
			if last["stage"] != stage || last["http_status"] != float64(429) || last["error_kind"] != "rate_limited" || last["retry_after_seconds"] != float64(30) || last["trace_id"] != "sync-fixture-001" {
				t.Fatal("missing diagnostic fields", last)
			}
			if stage == "accounts" && (last["page"] != float64(1) || last["page_size"] != float64(100)) {
				t.Fatal("pagination missing", last)
			}
			for _, secret := range []string{"private-login-token", "private-login-password", "private-user@example.com", "private-account@example.com", publicKey, "sensitive-error-response", "keyword=", "Authorization"} {
				if strings.Contains(output.String(), secret) {
					t.Fatal("sensitive data leaked")
				}
			}
		})
	}
}

func TestSupplierDiagnosticsFilteringAndBehaviorParity(t *testing.T) {
	t.Setenv("TK_REGISTER_SUPPLIER_DIAGNOSTICS", "errors")
	var output bytes.Buffer
	ctx := diagnosticContext(&output)
	responseStatus := 200
	client := &Client{transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(responseStatus, map[string]string{"secret": "response-private"}), nil
	})}
	s := client.newSession()
	status, body, err := s.request(ctx, http.MethodGet, apiPrefix+"/accounts?keyword=private@example.com&page=2&page_size=100", nil)
	if status != 200 || err != nil || !bytes.Contains(body, []byte("response-private")) || output.Len() != 0 || diagnostics.RequestCount(ctx) != 1 {
		t.Fatal("errors mode changed successful request behavior")
	}
	responseStatus = 429
	status, body, err = s.request(ctx, http.MethodGet, apiPrefix+"/accounts?keyword=private@example.com&page=2&page_size=100", nil)
	// Unclassified collectors historically return HTTP 429 to readStatus. The
	// logger must identify it without altering that control flow.
	if status != 429 || err != nil || body != nil {
		t.Fatal("unclassified 429 behavior changed")
	}
	events := diagnosticEvents(t, output.String())
	if len(events) != 1 || events[0]["http_status"] != float64(429) || events[0]["page"] != float64(2) || diagnostics.RequestCount(ctx) != 2 {
		t.Fatal("failure event missing", events)
	}
	if strings.Contains(output.String(), "private") {
		t.Fatal("body or query leaked")
	}
	output.Reset()
	s.remaining = 0
	_, _, _ = s.request(ctx, http.MethodGet, apiPrefix+"/accounts", nil)
	if output.Len() != 0 || diagnostics.RequestCount(ctx) != 2 {
		t.Fatal("non-network rejection counted as HTTP attempt")
	}
}

func TestSupplierDiagnosticsPaginationAndReauthenticationAttempts(t *testing.T) {
	t.Setenv("TK_REGISTER_SUPPLIER_DIAGNOSTICS", "all")
	var output bytes.Buffer
	ctx := diagnosticContext(&output)
	f := &supplierFixture{t: t, read: func(req *http.Request, n int) *http.Response {
		if n == 2 {
			return jsonResponse(401, map[string]string{"detail": "PRIVATE_AUTH_FAILURE"})
		}
		id := 1
		if n == 3 {
			id = 2
		}
		return jsonResponse(200, map[string]any{"list": []any{fixtureAccount(id)}, "total": 2})
	}}
	accounts, err := f.client().Collect(ctx, "supplier-user", "private-password-中文")
	if err != nil || len(accounts) != 2 || f.logins != 2 || f.keys != 2 || f.reads != 3 {
		t.Fatal("pagination or retry behavior changed", err)
	}
	events := diagnosticEvents(t, output.String())
	wantStages := []string{"pubkey", "login", "accounts", "accounts", "pubkey", "login", "accounts"}
	if len(events) != len(wantStages) || diagnostics.RequestCount(ctx) != 7 {
		t.Fatal("not every actual attempt was logged")
	}
	var pages []float64
	for i, event := range events {
		if event["stage"] != wantStages[i] || event["trace_id"] != "sync-fixture-001" {
			t.Fatal("request chain lost correlation", event)
		}
		if event["stage"] == "accounts" {
			pages = append(pages, event["page"].(float64))
		}
	}
	if len(pages) != 3 || pages[0] != 1 || pages[1] != 2 || pages[2] != 2 || events[3]["http_status"] != float64(401) || events[3]["error_kind"] != "authentication" {
		t.Fatal("failed page and its retry are not identifiable", events)
	}
	for _, secret := range []string{"PRIVATE_AUTH_FAILURE", "UNRELATED_SECRET", "private-password", "supplier-user", "account@example.com"} {
		if strings.Contains(output.String(), secret) {
			t.Fatal("supplier response or credentials leaked")
		}
	}
}

type diagnosticFailRead struct{}

func (diagnosticFailRead) Read([]byte) (int, error) { return 0, errors.New("private-body-read-error") }
func (diagnosticFailRead) Close() error             { return nil }

func TestSupplierDiagnosticsBodyAndTransportErrors(t *testing.T) {
	t.Setenv("TK_REGISTER_SUPPLIER_DIAGNOSTICS", "errors")
	cases := []struct {
		name, kind string
		response   *http.Response
		err        error
	}{
		{"body", "body_read", &http.Response{StatusCode: 200, Header: make(http.Header), Body: diagnosticFailRead{}}, nil},
		{"transport", "transport", nil, errors.New("private-token-in-transport-error")},
		{"timeout", "timeout", nil, context.DeadlineExceeded},
		{"canceled", "canceled", nil, context.Canceled},
		{"dns", "dns", nil, &net.DNSError{Err: "private-dns-error", Name: "private-host"}},
		{"connect", "connect", nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("private-connect-error")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			ctx := diagnosticContext(&output)
			client := &Client{transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return tc.response, tc.err })}
			s := client.newSession()
			s.classifyReadErrors = true
			_, _, err := s.request(ctx, http.MethodGet, apiPrefix+"/accounts?keyword=private@example.com", nil)
			if err == nil {
				t.Fatal("expected unchanged request failure")
			}
			events := diagnosticEvents(t, output.String())
			if len(events) != 1 || events[0]["outcome"] != "failed" || events[0]["error_kind"] != tc.kind {
				t.Fatal("wrong safe error classification", events)
			}
			want := float64(0)
			if tc.response != nil {
				want = float64(tc.response.StatusCode)
			}
			if events[0]["http_status"] != want || strings.Contains(output.String(), "private") {
				t.Fatal("status missing or secret leaked")
			}
		})
	}
}

func TestSupplierDiagnosticsPaginationAndRetryAfterValidation(t *testing.T) {
	for _, value := range []string{"", "0", "-1", "+1", "1.5", "9999999999999999999", "secret", "1\n2"} {
		if diagnosticPositiveInt(url.Values{"page": []string{value}}, "page") != nil {
			t.Fatalf("invalid page accepted: %q", value)
		}
	}
	if diagnosticPositiveInt(url.Values{"page": []string{"1", "2"}}, "page") != nil {
		t.Fatal("duplicate page accepted")
	}
	if p := diagnosticPositiveInt(url.Values{"page": []string{"2"}}, "page"); p == nil || *p != 2 {
		t.Fatal("positive page rejected")
	}
	now := time.Date(2026, 11, 3, 13, 0, 0, 0, time.UTC)
	for _, value := range []string{"", "-1", "+1", "12.3", "Bearer secret", "999999999999999999999", "604801", strings.Repeat("1", 100), now.Add(-time.Minute).Format(http.TimeFormat)} {
		if diagnosticRetryAfter(value, now) != nil {
			t.Fatalf("invalid retry header accepted: %q", value)
		}
	}
	for _, value := range []string{"30", now.Add(30 * time.Second).Format(http.TimeFormat)} {
		if p := diagnosticRetryAfter(value, now); p == nil || *p != 30 {
			t.Fatal("valid retry header rejected", value)
		}
	}
	// Missing/invalid pagination is omitted, never printed verbatim.
	t.Setenv("TK_REGISTER_SUPPLIER_DIAGNOSTICS", "all")
	var output bytes.Buffer
	ctx := diagnosticContext(&output)
	client := &Client{transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})}
	_, _, _ = client.newSession().request(ctx, http.MethodGet, apiPrefix+"/accounts?page=private&page_size=100&page_size=200", nil)
	events := diagnosticEvents(t, output.String())
	if _, ok := events[0]["page"]; ok {
		t.Fatal("invalid page emitted")
	}
	if _, ok := events[0]["page_size"]; ok {
		t.Fatal("duplicate page size emitted")
	}
	if _, ok := events[0]["retry_after_seconds"]; ok {
		t.Fatal("absent retry-after emitted")
	}
}
