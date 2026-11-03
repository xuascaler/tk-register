package main

import (
	"context"
	"errors"
	"time"

	"goapi-tk-register/server/internal/diagnostics"
	"goapi-tk-register/server/internal/supplier"
)

// Diagnostic identifiers are generated locally. Neither a supplier credential
// nor a client-provided header is used to correlate operational logs.
func accountSyncTrace(ctx context.Context, source string, actorID, accountID uint64) context.Context {
	trace := diagnostics.Trace{Source: source, Vendor: "tk-register-0", ActorID: actorID, AccountID: accountID}
	if diagnostics.TraceFrom(ctx).TraceID == "" {
		// Logging must not change the outcome when entropy is unavailable.
		trace.TraceID, _ = randomToken()
	}
	return diagnostics.WithTrace(ctx, trace)
}

func syncDiagnosticGate(ctx context.Context, kind string, limit, window, wait int) {
	stage := "local"
	if kind == "sync_lock_busy" {
		stage = "lock"
	}
	diagnostics.LogSync(ctx, diagnostics.SyncEvent{Event: "gate", Outcome: "rejected", FailureStage: stage, ErrorKind: kind, Limit: limit, WindowSeconds: window, WaitSeconds: wait})
}

func beginSyncDiagnostic(ctx context.Context) (*diagnostics.SyncEvent, func()) {
	started := time.Now()
	event := &diagnostics.SyncEvent{Event: "summary", Outcome: "failed", ErrorKind: "unknown"}
	return event, func() {
		if event.Event == "" {
			return
		}
		event.DurationMS = time.Since(started).Milliseconds()
		if event.Outcome == "failed" && event.FailureStage == "" {
			switch event.ErrorKind {
			case "persistence", "configuration":
				event.FailureStage = event.ErrorKind
			default:
				event.FailureStage = "collection"
			}
		}
		diagnostics.LogSync(ctx, *event)
	}
}

func syncDiagnosticError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var upstream *supplier.Error
	if errors.As(err, &upstream) {
		switch upstream.Code {
		case supplier.CodeRateLimited, supplier.CodeSessionBusy:
			return "rate_limited"
		case supplier.CodeTimeout:
			return "timeout"
		case supplier.CodeCredentialsInvalid, supplier.CodeSessionExpired:
			return "authentication"
		case supplier.CodeContractMismatch, supplier.CodeAccountNotFound, supplier.CodeAccountAmbiguous:
			return "contract"
		case supplier.CodeUnavailable:
			return "transport"
		}
	}
	return "unknown"
}
