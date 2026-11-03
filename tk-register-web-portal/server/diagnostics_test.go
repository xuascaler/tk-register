package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"goapi-tk-register/server/common"
	"goapi-tk-register/server/internal/diagnostics"
	"goapi-tk-register/server/internal/supplier"
)

func diagnosticRequest(t *testing.T, a *app, route string, body any, cookie *http.Cookie, csrf string, logs *bytes.Buffer) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := common.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", basePath+"/api/"+route, bytes.NewReader(raw))
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("Origin", a.origin)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", csrf)
	r.Header.Set("X-Request-ID", "client-controlled-correlation")
	r.AddCookie(cookie)
	r = r.WithContext(diagnostics.WithLogger(r.Context(), log.New(logs, "", 0)))
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}

func syncDiagnosticEvents(t *testing.T, logs *bytes.Buffer) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if !strings.HasPrefix(line, "TK_ACCOUNT_SYNC ") {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "TK_ACCOUNT_SYNC ")), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	return events
}

func TestSyncDiagnosticsTraceAndPartialHTTP200Failure(t *testing.T) {
	t.Setenv("TK_REGISTER_SUPPLIER_DIAGNOSTICS", "errors")
	a, owner, cookie, csrf, rows, fake := setupPageSync(t)
	var logs bytes.Buffer
	var captured diagnostics.Trace
	fake.run = func(ctx context.Context, targets []supplier.Target) ([]supplier.AccountResult, error) {
		captured = diagnostics.TraceFrom(ctx)
		return []supplier.AccountResult{pageRecord(targets[0]), {Target: targets[1], Err: &supplier.Error{Code: supplier.CodeRateLimited, Message: "private-password private-access-token owner@example.com"}}}, nil
	}
	w := diagnosticRequest(t, a, "accounts/sync", pageInput(rows...), cookie, csrf, &logs)
	requireStatus(t, w, 200)
	events := syncDiagnosticEvents(t, &logs)
	if len(events) != 1 {
		t.Fatalf("expected one summary for partial result, got %d", len(events))
	}
	event := events[0]
	if captured.TraceID == "" || captured.TraceID != w.Header().Get("X-Request-ID") || captured.TraceID == "client-controlled-correlation" || captured.Source != "manual_sync" || captured.ActorID != owner.ID {
		t.Fatal("request trace did not reach the collector")
	}
	if event["trace_id"] != captured.TraceID || event["outcome"] != "failed" || event["error_kind"] != "rate_limited" || event["updated_count"] != float64(1) || event["failed_count"] != float64(1) || event["skipped_count"] != float64(1) {
		t.Fatalf("partial result diagnostic was inaccurate: %+v", event)
	}
	for _, secret := range []string{"private-password", "private-access-token", "owner@example.com", rows[0].Account, rows[1].Account} {
		if strings.Contains(logs.String(), secret) {
			t.Fatal("private value appeared in sync diagnostics")
		}
	}
}

func TestSyncDiagnosticsLocalQuotaDoesNotContactSupplier(t *testing.T) {
	a, owner, cookie, csrf, rows, fake := setupPageSync(t)
	key := redisPrefix + "account-sync:page-rate:" + fmt.Sprint(owner.ID)
	if err := a.redis.Set(context.Background(), key, "200", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	w := diagnosticRequest(t, a, "accounts/sync", pageInput(rows[0]), cookie, csrf, &logs)
	requireStatus(t, w, 429)
	events := syncDiagnosticEvents(t, &logs)
	if len(events) != 1 || fake.calls != 0 {
		t.Fatal("quota rejection did not produce exactly one local gate")
	}
	event := events[0]
	if event["event"] != "gate" || event["outcome"] != "rejected" || event["error_kind"] != "local_rate_limit" || event["failure_stage"] != "local" || event["limit"] != float64(200) || event["window_seconds"] != float64(60) || event["http_request_count"] != float64(0) || event["account_id"] != float64(rows[0].ID) || event["actor_id"] != float64(owner.ID) {
		t.Fatalf("quota diagnostic was inaccurate: %+v", event)
	}
}

func TestSyncDiagnosticsBackgroundLockAndAttempt(t *testing.T) {
	t.Setenv("TK_REGISTER_SUPPLIER_DIAGNOSTICS", "all")
	a, target, job, fake, now := setupPostSubmissionSync(t)
	var logs bytes.Buffer
	ctx := diagnostics.WithLogger(context.Background(), log.New(&logs, "", 0))
	if err := a.redis.Set(ctx, accountSyncLeaseKey, "private-lease-token", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if err := a.processPostSubmissionSync(ctx, now); err != nil {
		t.Fatal(err)
	}
	events := syncDiagnosticEvents(t, &logs)
	if len(events) != 1 || fake.calls != 0 || readPostSubmissionJob(t, a, job.ID).AttemptCount != 0 {
		t.Fatal("busy diagnostic consumed a query attempt")
	}
	gate := events[0]
	if gate["event"] != "gate" || gate["error_kind"] != "sync_lock_busy" || gate["wait_seconds"] != float64(5) || gate["job_id"] != float64(job.ID) || gate["account_id"] != float64(target.ID) || gate["http_request_count"] != float64(0) {
		t.Fatalf("lock diagnostic was inaccurate: %+v", gate)
	}
	if strings.Contains(logs.String(), "private-lease-token") {
		t.Fatal("lock token appeared in diagnostic")
	}
	a.redis.Del(ctx, accountSyncLeaseKey)
	logs.Reset()
	var captured diagnostics.Trace
	fake.run = func(ctx context.Context, targets []supplier.Target) ([]supplier.AccountResult, error) {
		captured = diagnostics.TraceFrom(ctx)
		return []supplier.AccountResult{pageRecord(targets[0])}, nil
	}
	if err := a.processPostSubmissionSync(ctx, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	events = syncDiagnosticEvents(t, &logs)
	if len(events) != 1 || readPostSubmissionJob(t, a, job.ID).Status != "succeeded" {
		t.Fatal("claimed attempt did not produce one successful summary")
	}
	summary := events[0]
	if captured.TraceID == "" || captured.TraceID == gate["trace_id"] || captured.Source != "post_submit_sync" || captured.JobID != job.ID || captured.AccountID != target.ID || captured.Attempt != 1 || summary["trace_id"] != captured.TraceID || summary["outcome"] != "succeeded" || summary["updated_count"] != float64(1) {
		t.Fatalf("background trace was inaccurate: %+v", summary)
	}
}
