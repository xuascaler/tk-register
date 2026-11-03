package diagnostics

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"testing"
	"time"
)

func decoded(t *testing.T, line, prefix string) map[string]any {
	t.Helper()
	if !strings.HasPrefix(line, prefix+" ") {
		t.Fatal("missing diagnostic prefix")
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(line, prefix+" ")), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDiagnosticsTracePropagationAndConcurrentCounts(t *testing.T) {
	t.Setenv("TK_REGISTER_SUPPLIER_DIAGNOSTICS", "errors")
	var output bytes.Buffer
	ctx := WithLogger(context.Background(), log.New(&output, "", 0))
	ctx = WithTrace(ctx, Trace{TraceID: "trace-001", Source: "manual_sync", Vendor: "tk-register-0", ActorID: 5})
	child := WithTrace(ctx, Trace{AccountID: 7})
	var work sync.WaitGroup
	for i := 0; i < 40; i++ {
		work.Add(1)
		go func() {
			defer work.Done()
			LogRequest(child, RequestEvent{StartedAt: time.Now(), Method: "GET", Path: "/supplier-account/api/supplier/accounts", HTTPStatus: 200, Outcome: "succeeded"})
		}()
	}
	work.Wait()
	if RequestCount(ctx) != 40 || RequestCount(child) != 40 || output.Len() != 0 {
		t.Fatal("successful request counting or filtering failed")
	}
	LogSync(child, SyncEvent{Event: "summary", Outcome: "failed", ErrorKind: "contract", FailedCount: 1})
	event := decoded(t, strings.TrimSpace(output.String()), "TK_ACCOUNT_SYNC")
	if event["trace_id"] != "trace-001" || event["source"] != "manual_sync" || event["actor_id"] != float64(5) || event["account_id"] != float64(7) || event["http_request_count"] != float64(40) {
		t.Fatal("trace or summary count missing", event)
	}
	fresh := WithTrace(ctx, Trace{TraceID: "trace-002"})
	if RequestCount(fresh) != 0 || RequestCount(ctx) != 40 {
		t.Fatal("new trace must not reuse counters")
	}
}

func TestDiagnosticsAllowlistAndModes(t *testing.T) {
	t.Setenv("TK_REGISTER_SUPPLIER_DIAGNOSTICS", "all")
	var output bytes.Buffer
	ctx := WithLogger(WithTrace(context.Background(), Trace{TraceID: "bad\nAuthorization: Bearer secret", Source: "user@example.com", Vendor: "password-secret"}), log.New(&output, "", 0))
	page, size, retry := 2, 100, int64(30)
	LogRequest(ctx, RequestEvent{StartedAt: time.Now(), Method: "GET", Path: "/supplier-account/api/supplier/accounts", Page: &page, PageSize: &size, HTTPStatus: 429, Outcome: "failed", ErrorKind: "rate_limited", RetryAfterSeconds: &retry})
	event := decoded(t, strings.TrimSpace(output.String()), "TK_SUPPLIER_REQUEST")
	if _, ok := event["trace_id"]; ok {
		t.Fatal("untrusted trace leaked")
	}
	if event["source"] != "unknown" || event["vendor"] != "unknown" || event["page"] != float64(2) || event["page_size"] != float64(100) || event["retry_after_seconds"] != float64(30) {
		t.Fatal("metadata filtering failed", event)
	}
	output.Reset()
	LogRequest(ctx, RequestEvent{StartedAt: time.Now(), Method: "GET-secret", Path: "/supplier-account/api/supplier/accounts?keyword=secret", Stage: "password-secret", Page: &page, HTTPStatus: 999, Outcome: "failed", ErrorKind: "password-secret"})
	event = decoded(t, strings.TrimSpace(output.String()), "TK_SUPPLIER_REQUEST")
	if strings.Contains(output.String(), "secret") || strings.Contains(output.String(), "@") || event["http_status"] != float64(0) || event["path"] != "unknown" || event["error_kind"] != "unknown" {
		t.Fatal("arbitrary metadata leaked")
	}
	if _, ok := event["page"]; ok {
		t.Fatal("page must be omitted for unknown route")
	}
	output.Reset()
	LogRequest(ctx, RequestEvent{StartedAt: time.Now(), Method: "POST", Path: "/supplier-account/api/supplier/login", HTTPStatus: 200, Outcome: "succeeded", Page: &page, PageSize: &size})
	event = decoded(t, strings.TrimSpace(output.String()), "TK_SUPPLIER_REQUEST")
	if _, ok := event["page"]; ok {
		t.Fatal("login has no pagination")
	}
	if event["stage"] != "login" {
		t.Fatal("wrong stage")
	}
	output.Reset()
	LogSync(ctx, SyncEvent{Event: "gate", Outcome: "rejected", ErrorKind: "local_rate_limit", FailureStage: "local", Limit: 200, WindowSeconds: 60, WaitSeconds: 5})
	event = decoded(t, strings.TrimSpace(output.String()), "TK_ACCOUNT_SYNC")
	if event["event"] != "gate" || event["limit"] != float64(200) {
		t.Fatal("gate metadata missing")
	}
}

type panicLogger struct{}

func (panicLogger) Printf(string, ...any) { panic("logger failed") }

func TestDiagnosticsLoggerCannotBreakRequest(t *testing.T) {
	ctx := WithLogger(context.Background(), panicLogger{})
	LogRequest(ctx, RequestEvent{StartedAt: time.Now(), Outcome: "failed"})
	LogSync(ctx, SyncEvent{Event: "gate", Outcome: "rejected"})
}
