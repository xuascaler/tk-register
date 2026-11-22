package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"

	"goapi-tk-register/server/internal/core"
	"goapi-tk-register/server/internal/diagnostics"
	"goapi-tk-register/server/internal/supplier"
)

func partialRecord(row core.SyncedAccount, cost float64, at time.Time) supplier.Account {
	status, currency := "active", "CNY"
	return supplier.Account{ID: *row.RemoteID, Account: row.Account, Plan: row.Plan, Status: &status, Currency: &currency, CostTotal: &cost, CollectedAt: at}
}

func partialCollectionFailure(cause error, safe bool) *supplier.CollectionError {
	return &supplier.CollectionError{Cause: cause, CompletedPages: 1, FailedPage: 2, TotalRecords: 600, PageSize: 500, RetryCount: 2, PartialSafe: safe}
}

func TestPartialCollectionSavesValidatedAccountsAndKeepsUncoveredSnapshots(t *testing.T) {
	a, owner, _, _, rows, _ := setupPageSync(t)
	configureSync(t, a, owner.ID, true)
	makeScheduleDue(t, a)
	ctx := context.Background()
	oldAt := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	for _, row := range rows[:2] {
		record := partialRecord(row, 10, oldAt)
		if err := a.store.RecordAccountSnapshot(ctx, row.ID, row.IdentityKey, supplierAccountSnapshot(&record), oldAt, "Asia/Shanghai"); err != nil {
			t.Fatal(err)
		}
		if err := a.store.RecordAccountFailure(ctx, row.ID, row.IdentityKey, "Previous failed attempt", oldAt.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	observedAt := time.Now().Add(-3 * time.Second).UTC().Truncate(time.Millisecond)
	cause := waitingSupplier("accounts", false, time.Now().Add(3*time.Second))
	a.accountCollector = &mockAccountCollector{records: []supplier.Account{partialRecord(rows[0], 25, observedAt)}, err: partialCollectionFailure(cause, true)}
	var logs bytes.Buffer
	ctx = diagnostics.WithLogger(ctx, log.New(&logs, "", 0))
	if err := a.collectAccounts(ctx, 0, 0, true); !errors.Is(err, cause) {
		t.Fatalf("lost upstream cause: %v", err)
	}
	updated, err := a.store.SyncedAccountForUser(ctx, owner.ID, rows[0].ID)
	if err != nil || updated.Snapshot == nil || *updated.Snapshot.CostTotal != 25 || updated.LastError != "" || updated.LastSuccessAt == nil || !updated.LastSuccessAt.Equal(observedAt) {
		t.Fatalf("successful page was not saved with its observation time: %+v %v", updated, err)
	}
	uncovered, err := a.store.SyncedAccountForUser(ctx, owner.ID, rows[1].ID)
	if err != nil || uncovered.Snapshot == nil || *uncovered.Snapshot.CostTotal != 10 || uncovered.LastSuccessAt == nil || !uncovered.LastSuccessAt.Equal(oldAt) {
		t.Fatalf("uncovered account lost saved data: %+v %v", uncovered, err)
	}
	if !strings.Contains(uncovered.LastError, "rate limited") || strings.Contains(uncovered.LastError, "not found") || strings.Contains(uncovered.LastError, "seconds") || strings.Contains(uncovered.LastError, "private-access-token") {
		t.Fatalf("unsafe or misleading saved error: %q", uncovered.LastError)
	}
	var state core.AccountSyncState
	if err := a.store.DB.First(&state, 1).Error; err != nil || state.Status != "partial" || state.LastSuccessAt != nil || !strings.Contains(state.LastError, "1 updated, 1 not updated") {
		t.Fatalf("invalid partial schedule: %+v %v", state, err)
	}
	events := syncDiagnosticEvents(t, &logs)
	if len(events) != 1 || events[0]["outcome"] != "partial" || events[0]["updated_count"] != float64(1) || events[0]["failed_count"] != float64(1) || events[0]["incomplete_count"] != float64(1) {
		t.Fatalf("invalid diagnostics: %+v", events)
	}
	cfg, err := a.syncConfig(ctx)
	if err != nil || cfg.LastOutcome != "partial" {
		t.Fatalf("partial result missing from configuration: %+v %v", cfg, err)
	}
}

func TestPartialCollectionDetailSuccessIgnoresLaterPageFailure(t *testing.T) {
	a, _, cookie, csrf, rows, _ := setupPageSync(t)
	a.accountCollector = &mockAccountCollector{records: []supplier.Account{partialRecord(rows[0], 42, time.Now())}, err: partialCollectionFailure(waitingSupplier("accounts", false, time.Now().Add(time.Minute)), true)}
	w := request(t, a, "POST", fmt.Sprintf("accounts/%d/sync", rows[0].ID), map[string]any{}, cookie, csrf)
	requireStatus(t, w, 200)
	account := data(t, w)["account"].(map[string]any)
	if account["last_error"] != "" || account["last_success_at"] == nil {
		t.Fatalf("successful target reported failure: %s", w.Body.String())
	}
}

func TestPartialCollectionDoesNotTrustUnvalidatedOrWrongIdentityRows(t *testing.T) {
	for _, kind := range []string{"unmarked", "contract", "wrong_identity"} {
		t.Run(kind, func(t *testing.T) {
			a, owner, _, _, rows, _ := setupPageSync(t)
			record := partialRecord(rows[0], 42, time.Now())
			var collectionErr error = partialCollectionFailure(waitingSupplier("accounts", false, time.Now().Add(time.Minute)), true)
			switch kind {
			case "unmarked":
				collectionErr = waitingSupplier("accounts", false, time.Now().Add(time.Minute))
			case "contract":
				collectionErr = partialCollectionFailure(&supplier.Error{Code: supplier.CodeContractMismatch, Message: "The supplier returned invalid data."}, false)
			case "wrong_identity":
				record.ID = "wrong-record"
			}
			a.accountCollector = &mockAccountCollector{records: []supplier.Account{record}, err: collectionErr}
			_ = a.collectAccounts(context.Background(), owner.ID, rows[0].ID, false)
			out, err := a.store.SyncedAccountForUser(context.Background(), owner.ID, rows[0].ID)
			if err != nil || out.Snapshot != nil || out.LastSuccessAt != nil || out.LastError == "" {
				t.Fatalf("untrusted record saved: %+v %v", out, err)
			}
			if strings.Contains(out.LastError, "not found") || strings.Contains(out.LastError, "no longer matches") {
				t.Fatalf("incomplete inventory used to conclude identity is absent: %q", out.LastError)
			}
		})
	}
}

func TestCompleteCollectionStillReportsMissingAccount(t *testing.T) {
	a, owner, _, _, rows, _ := setupPageSync(t)
	a.accountCollector = &mockAccountCollector{}
	if err := a.collectAccounts(context.Background(), owner.ID, rows[0].ID, false); err != nil {
		t.Fatal(err)
	}
	out, err := a.store.SyncedAccountForUser(context.Background(), owner.ID, rows[0].ID)
	if err != nil || !strings.Contains(out.LastError, "not found") {
		t.Fatalf("complete inventory lost missing-account diagnosis: %+v %v", out, err)
	}
}
