package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"goapi-tk-register/server/internal/core"
	"goapi-tk-register/server/internal/supplier"
)

type mockReasonReader struct {
	calls atomic.Int32
	run   func(context.Context, string, string) (*string, error)
}

func (m *mockReasonReader) ReadAccountReason(ctx context.Context, u, p, id, status string) (*string, error) {
	m.calls.Add(1)
	return m.run(ctx, id, status)
}
func setupReasonWorker(t *testing.T) (*app, []core.SyncedAccount, *mockReasonReader) {
	t.Helper()
	a, owner, _, _, rows, _ := setupPageSync(t)
	configureSync(t, a, owner.ID, true)
	next := time.Now().UTC().Add(10 * time.Minute)
	if e := a.store.DB.Model(&core.AccountSyncState{}).Where("id = 1").Update("next_run_at", next).Error; e != nil {
		t.Fatal(e)
	}
	status := "sync_failed"
	cost := 27.0
	if e := a.store.RecordAccountSnapshot(context.Background(), rows[0].ID, rows[0].IdentityKey, core.AccountSnapshotValues{SupplierRecordID: "450001", AccountStatus: &status, UpstreamStatusRaw: &status, CostTotal: &cost}, time.Now().UTC().Add(-time.Minute), "UTC"); e != nil {
		t.Fatal(e)
	}
	fake := &mockReasonReader{run: func(ctx context.Context, id, status string) (*string, error) {
		value := "Authentication unavailable"
		return &value, nil
	}}
	a.accountReasonReader = fake
	return a, rows, fake
}
func TestBackgroundReasonWorkerSavesThenSkips(t *testing.T) {
	a, rows, fake := setupReasonWorker(t)
	ctx := context.Background()
	before, e := a.store.SyncedAccountForAdmin(ctx, rows[0].ID)
	if e != nil {
		t.Fatal(e)
	}
	var state core.AccountSyncState
	a.store.DB.First(&state, 1)
	for i := 0; i < 2; i++ {
		if e = a.backfillAccountReasons(ctx); e != nil {
			t.Fatal(e)
		}
	}
	after, e := a.store.SyncedAccountForAdmin(ctx, rows[0].ID)
	if e != nil || fake.calls.Load() != 1 || after.Snapshot.UpstreamErrorMessage == nil || *after.Snapshot.UpstreamErrorMessage != "Authentication unavailable" || !after.LastSuccessAt.Equal(*before.LastSuccessAt) || *after.Snapshot.CostTotal != 27 {
		t.Fatal("worker mutated metrics or re-read completed reason", e, fake.calls.Load())
	}
	var actual core.AccountSyncState
	a.store.DB.First(&actual, 1)
	if !actual.NextRunAt.Equal(*state.NextRunAt) {
		t.Fatal("reason worker changed configured schedule")
	}
	if a.redis.Exists(ctx, accountSyncLeaseKey, accountReasonLeaseKey).Val() != 0 {
		t.Fatal("lease leaked")
	}
}
func TestBackgroundReasonWorkerFailedReadRetainsDataAndRetriesLater(t *testing.T) {
	a, rows, fake := setupReasonWorker(t)
	ctx := context.Background()
	fake.run = func(context.Context, string, string) (*string, error) { return nil, errors.New("network timeout") }
	for i := 0; i < 2; i++ {
		if e := a.backfillAccountReasons(ctx); e != nil {
			t.Fatal(e)
		}
	}
	dto, e := a.store.SyncedAccountForAdmin(ctx, rows[0].ID)
	if e != nil || fake.calls.Load() != 1 || dto.LastError != "" || dto.Snapshot.UpstreamErrorMessage != nil || *dto.Snapshot.CostTotal != 27 {
		t.Fatal("reason failure polluted account state", e)
	}
	targets, e := a.store.MissingAccountReasons(ctx, 0, 20)
	if e != nil || len(targets) != 1 {
		t.Fatal(e, targets)
	}
	if ttl := a.redis.TTL(ctx, reasonRetryKey(targets[0])).Val(); ttl <= 0 || ttl > time.Minute {
		t.Fatal("failed lookup has no retry delay", ttl)
	}
	a.redis.Del(ctx, reasonRetryKey(targets[0])) // Simulate retry delay expiration.
	fake.run = func(context.Context, string, string) (*string, error) { empty := ""; return &empty, nil }
	if e = a.backfillAccountReasons(ctx); e != nil {
		t.Fatal(e)
	}
	if e = a.backfillAccountReasons(ctx); e != nil {
		t.Fatal(e)
	}
	if fake.calls.Load() != 2 {
		t.Fatal("empty successful result retried")
	}
}
func TestBackgroundReasonWorkerYieldsToSyncAndConfiguredDueTime(t *testing.T) {
	for _, gate := range []string{"main_lease", "reason_lease", "due", "disabled"} {
		t.Run(gate, func(t *testing.T) {
			a, _, fake := setupReasonWorker(t)
			ctx := context.Background()
			switch gate {
			case "main_lease":
				a.redis.Set(ctx, accountSyncLeaseKey, "normal-sync", time.Minute)
			case "reason_lease":
				a.redis.Set(ctx, accountReasonLeaseKey, "other-worker", time.Minute)
			case "due":
				// A custom interval still uses the durable schedule, not a 300s timer.
				if e := a.store.PatchSettings(ctx, map[string]string{"account_sync_interval_seconds": "900"}, 1); e != nil {
					t.Fatal(e)
				}
				a.store.DB.Model(&core.AccountSyncState{}).Where("id = 1").Update("next_run_at", time.Now().Add(5*time.Second))
			case "disabled":
				a.store.PatchSettings(ctx, map[string]string{"account_sync_enabled": "false"}, 1)
			}
			if e := a.backfillAccountReasons(ctx); e != nil {
				t.Fatal(e)
			}
			if fake.calls.Load() != 0 {
				t.Fatal("worker ignored main sync priority or config")
			}
			if gate == "main_lease" && a.redis.Get(ctx, accountSyncLeaseKey).Val() != "normal-sync" {
				t.Fatal("main lease modified")
			}
		})
	}
}
func TestBackgroundReasonWorkerDoesNotBlockScheduledCollection(t *testing.T) {
	a, rows, fake := setupReasonWorker(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	release := make(chan struct{})
	fake.run = func(ctx context.Context, id, status string) (*string, error) {
		close(started)
		select {
		case <-release:
			value := "outdated"
			return &value, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	done := make(chan error, 1)
	go func() { done <- a.backfillAccountReasons(ctx) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("reason worker did not start")
	}
	status := "active"
	collector := &mockAccountCollector{records: []supplier.Account{{ID: "450001", PlatformAccountID: *rows[0].RemoteID, Account: rows[0].Account, Plan: rows[0].Plan, Status: &status, UpstreamStatusRaw: &status}}}
	a.accountCollector = collector
	makeScheduleDue(t, a)
	timeout, stop := context.WithTimeout(context.Background(), 2*time.Second)
	e := a.collectAccounts(timeout, 0, 0, true)
	stop()
	if e != nil || collector.collectCalls != 1 {
		t.Fatal("scheduled sync blocked by reason worker", e)
	}
	close(release)
	select {
	case e = <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("reason worker did not finish")
	}
	dto, e := a.store.SyncedAccountForAdmin(context.Background(), rows[0].ID)
	if e != nil || dto.Snapshot.AccountStatus == nil || *dto.Snapshot.AccountStatus != "active" || dto.Snapshot.UpstreamErrorMessage != nil {
		t.Fatal("stale reason overwrote fresh state", e)
	}
}
func TestBackgroundReasonWorkerRechecksMainLeaseBetweenAccounts(t *testing.T) {
	a, rows, fake := setupReasonWorker(t)
	ctx := context.Background()
	status := "login_failed"
	if e := a.store.RecordAccountSnapshot(ctx, rows[1].ID, rows[1].IdentityKey, core.AccountSnapshotValues{SupplierRecordID: "450002", AccountStatus: &status, UpstreamStatusRaw: &status}, time.Now().Add(-time.Minute), "UTC"); e != nil {
		t.Fatal(e)
	}
	fake.run = func(context.Context, string, string) (*string, error) {
		a.redis.Set(ctx, accountSyncLeaseKey, "normal-sync", time.Minute)
		reason := "Upstream login failed"
		return &reason, nil
	}
	if e := a.backfillAccountReasons(ctx); e != nil {
		t.Fatal(e)
	}
	if fake.calls.Load() != 1 || a.redis.Get(ctx, accountSyncLeaseKey).Val() != "normal-sync" {
		t.Fatal("worker issued more reads during normal sync")
	}
}
