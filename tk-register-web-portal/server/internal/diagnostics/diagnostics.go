// Package diagnostics emits bounded operational metadata, never supplier
// credentials, request/response bodies or arbitrary upstream error strings.
package diagnostics

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

type Trace struct {
	TraceID   string
	Source    string
	Vendor    string
	ActorID   uint64
	AccountID uint64
	JobID     uint64
	Attempt   int
}

type Logger interface{ Printf(string, ...any) }
type traceKey struct{}
type loggerKey struct{}
type traceState struct {
	trace    Trace
	requests *atomic.Int64
}

// WithTrace merges nonzero metadata. Child contexts share request counts; a
// different explicit trace ID starts a new operation with independent counts.
func WithTrace(ctx context.Context, update Trace) context.Context {
	state, _ := ctx.Value(traceKey{}).(traceState)
	if state.requests == nil || (update.TraceID != "" && update.TraceID != state.trace.TraceID) {
		state.requests = new(atomic.Int64)
	}
	if update.TraceID != "" {
		state.trace.TraceID = update.TraceID
	}
	if update.Source != "" {
		state.trace.Source = update.Source
	}
	if update.Vendor != "" {
		state.trace.Vendor = update.Vendor
	}
	if update.ActorID != 0 {
		state.trace.ActorID = update.ActorID
	}
	if update.AccountID != 0 {
		state.trace.AccountID = update.AccountID
	}
	if update.JobID != 0 {
		state.trace.JobID = update.JobID
	}
	if update.Attempt != 0 {
		state.trace.Attempt = update.Attempt
	}
	return context.WithValue(ctx, traceKey{}, state)
}

func TraceFrom(ctx context.Context) Trace {
	state, _ := ctx.Value(traceKey{}).(traceState)
	return state.trace
}

func WithLogger(ctx context.Context, logger Logger) context.Context {
	if logger == nil {
		return ctx
	}
	return context.WithValue(ctx, loggerKey{}, logger)
}

func RequestCount(ctx context.Context) int64 {
	state, _ := ctx.Value(traceKey{}).(traceState)
	if state.requests == nil {
		return 0
	}
	return state.requests.Load()
}

type RequestEvent struct {
	StartedAt         time.Time
	Method            string
	Path              string
	Stage             string
	Page              *int
	PageSize          *int
	HTTPStatus        int
	Outcome           string
	ErrorKind         string
	DurationMS        int64
	RetryAfterSeconds *int64
}

type SyncEvent struct {
	Event         string
	Outcome       string
	FailureStage  string
	ErrorKind     string
	DurationMS    int64
	UpdatedCount  int
	FailedCount   int
	SkippedCount  int
	Limit         int
	WindowSeconds int
	WaitSeconds   int
}

type traceFields struct {
	TraceID   string `json:"trace_id,omitempty"`
	Source    string `json:"source"`
	Vendor    string `json:"vendor"`
	ActorID   uint64 `json:"actor_id,omitempty"`
	AccountID uint64 `json:"account_id,omitempty"`
	JobID     uint64 `json:"job_id,omitempty"`
	Attempt   int    `json:"attempt,omitempty"`
}

func allowed(value string, values ...string) string {
	for _, candidate := range values {
		if value == candidate {
			return value
		}
	}
	return "unknown"
}

func boundedID(value string) string {
	if len(value) < 1 || len(value) > 128 {
		return ""
	}
	for _, c := range value {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.ContainsRune("_-.:", c) {
			continue
		}
		return ""
	}
	return value
}

func fields(ctx context.Context) traceFields {
	t := TraceFrom(ctx)
	return traceFields{
		TraceID: boundedID(t.TraceID),
		Source:  allowed(t.Source, "manual_sync", "scheduled_sync", "post_submit_sync", "auth_json_refresh", "auth_json_check", "auth_json_update", "test_login", "detail_sync"),
		Vendor:  allowed(t.Vendor, "tk-register-0"),
		ActorID: t.ActorID, AccountID: t.AccountID, JobID: t.JobID, Attempt: max(0, t.Attempt),
	}
}

func errorKind(value string) string {
	if value == "" {
		return ""
	}
	return allowed(value, "http", "rate_limited", "authentication", "timeout", "canceled", "dns", "connect", "tls", "transport", "body_read", "response_too_large", "local_rate_limit", "sync_lock_busy", "persistence", "contract", "configuration", "unknown")
}

func allRequests() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("TK_REGISTER_SUPPLIER_DIAGNOSTICS")), "all")
}

func emit(ctx context.Context, prefix string, value any) {
	// Diagnostics must never change the outcome of an otherwise valid request,
	// including if an injected operational logger fails unexpectedly.
	defer func() { _ = recover() }()
	encoded, err := json.Marshal(value)
	if err != nil {
		return
	}
	logger, _ := ctx.Value(loggerKey{}).(Logger)
	if logger == nil {
		logger = log.Default()
	}
	logger.Printf("%s %s", prefix, encoded)
}

// LogRequest must be called once for each actual HTTP attempt, even when
// successful-request output is disabled. Counts therefore remain complete.
func LogRequest(ctx context.Context, e RequestEvent) {
	state, _ := ctx.Value(traceKey{}).(traceState)
	if state.requests != nil {
		state.requests.Add(1)
	}
	outcome := allowed(e.Outcome, "succeeded", "failed")
	if outcome == "succeeded" && !allRequests() {
		return
	}
	path, stage, method := "unknown", "unknown", "unknown"
	switch {
	case e.Method == "POST" && e.Path == "/supplier-account/api/supplier/pubkey":
		path, stage, method = e.Path, "pubkey", "POST"
	case e.Method == "POST" && e.Path == "/supplier-account/api/supplier/login":
		path, stage, method = e.Path, "login", "POST"
	case e.Method == "GET" && e.Path == "/supplier-account/api/supplier/accounts":
		path, stage, method = e.Path, "accounts", "GET"
	}
	var page, size *int
	if stage == "accounts" {
		if e.Page != nil && *e.Page > 0 && *e.Page <= 1000000 {
			page = e.Page
		}
		if e.PageSize != nil && *e.PageSize > 0 && *e.PageSize <= 1000000 {
			size = e.PageSize
		}
	}
	var retry *int64
	if e.RetryAfterSeconds != nil && *e.RetryAfterSeconds >= 0 && *e.RetryAfterSeconds <= 7*24*60*60 {
		retry = e.RetryAfterSeconds
	}
	status := e.HTTPStatus
	if status < 100 || status > 599 {
		status = 0
	}
	emit(ctx, "TK_SUPPLIER_REQUEST", struct {
		traceFields
		StartedAt         time.Time `json:"started_at"`
		Stage             string    `json:"stage"`
		Method            string    `json:"method"`
		Path              string    `json:"path"`
		Page              *int      `json:"page,omitempty"`
		PageSize          *int      `json:"page_size,omitempty"`
		HTTPStatus        int       `json:"http_status"`
		Outcome           string    `json:"outcome"`
		ErrorKind         string    `json:"error_kind,omitempty"`
		DurationMS        int64     `json:"duration_ms"`
		RetryAfterSeconds *int64    `json:"retry_after_seconds,omitempty"`
	}{fields(ctx), e.StartedAt.UTC(), stage, method, path, page, size, status, outcome, errorKind(e.ErrorKind), max(int64(0), e.DurationMS), retry})
}

func LogSync(ctx context.Context, e SyncEvent) {
	event := allowed(e.Event, "summary", "gate")
	outcome := allowed(e.Outcome, "succeeded", "failed", "rejected", "skipped")
	if event == "summary" && (outcome == "succeeded" || outcome == "skipped") && !allRequests() {
		return
	}
	stage := ""
	if e.FailureStage != "" {
		stage = allowed(e.FailureStage, "pubkey", "login", "accounts", "local", "lock", "persistence", "configuration", "collection", "authentication")
	}
	emit(ctx, "TK_ACCOUNT_SYNC", struct {
		traceFields
		Event            string `json:"event"`
		Outcome          string `json:"outcome"`
		FailureStage     string `json:"failure_stage,omitempty"`
		ErrorKind        string `json:"error_kind,omitempty"`
		DurationMS       int64  `json:"duration_ms"`
		HTTPRequestCount int64  `json:"http_request_count"`
		UpdatedCount     int    `json:"updated_count"`
		FailedCount      int    `json:"failed_count"`
		SkippedCount     int    `json:"skipped_count"`
		Limit            int    `json:"limit,omitempty"`
		WindowSeconds    int    `json:"window_seconds,omitempty"`
		WaitSeconds      int    `json:"wait_seconds,omitempty"`
	}{fields(ctx), event, outcome, stage, errorKind(e.ErrorKind), max(int64(0), e.DurationMS), RequestCount(ctx), max(0, e.UpdatedCount), max(0, e.FailedCount), max(0, e.SkippedCount), max(0, e.Limit), max(0, e.WindowSeconds), max(0, e.WaitSeconds)})
}

// LogSession records fixed operational state names only. Tokens, usernames,
// cache keys and credentials are never accepted as fields.
func LogSession(ctx context.Context, event, stage string, waitSeconds int) {
	event = allowed(event, "cache_hit", "cache_miss", "cache_expired", "session_saved", "session_refresh", "session_invalidated", "login_lock_wait", "cooldown_wait", "cooldown_started", "credentials_changed", "test_login", "cache_unavailable")
	if !allRequests() && event != "cooldown_wait" && event != "cooldown_started" && event != "cache_unavailable" {
		return
	}
	emit(ctx, "TK_SUPPLIER_SESSION", struct {
		traceFields
		Event       string `json:"event"`
		Stage       string `json:"stage"`
		WaitSeconds int    `json:"wait_seconds,omitempty"`
	}{fields(ctx), event, allowed(stage, "login", "accounts", "login_lock"), max(0, waitSeconds)})
}
