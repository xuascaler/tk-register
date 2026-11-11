package core

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// query_only makes any INSERT/UPDATE (including conflict no-ops) fail. This
// protects against restoring read-time reconciliation and its multi-row locks.
func TestAccountReadsWorkWithDatabaseWritesDisabled(t *testing.T) {
	s, owner, vendor, version := syncFixture(t)
	ctx := context.Background()
	syncSubmission(t, s, owner.ID, vendor.ID, version.ID, "read-only@example.com", "claude/cc-pro", "accepted", "9001", time.Now().Add(-time.Hour))
	targets, err := s.CollectionTargets(ctx, owner.ID, 0)
	if err != nil || len(targets) != 1 {
		t.Fatal(targets, err)
	}
	account := targets[0]
	status, currency, cost := "active", "CNY", 72.0
	if err := s.RecordAccountSnapshot(ctx, account.ID, account.IdentityKey, AccountSnapshotValues{AccountStatus: &status, Currency: &currency, CostTotal: &cost}, time.Now(), "UTC"); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Exec("PRAGMA query_only = ON").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.Exec("PRAGMA query_only = OFF") })
	reads := []struct {
		name string
		run  func() error
	}{
		{"member list", func() error {
			p, e := s.ListSyncedAccounts(ctx, owner.ID, 1, "read-only", "active")
			if e == nil && (p.Total != 1 || len(p.Items) != 1 || p.Items[0].Snapshot == nil) {
				return fmt.Errorf("missing member account")
			}
			return e
		}},
		{"admin list", func() error {
			p, e := s.ListAdminSyncedAccounts(ctx, 1, owner.Email, "")
			if e == nil && (p.Total != 1 || len(p.Items) != 1 || p.Items[0].UserEmail != owner.Email) {
				return fmt.Errorf("missing account owner")
			}
			return e
		}},
		{"member detail", func() error { _, e := s.SyncedAccountForUser(ctx, owner.ID, account.ID); return e }},
		{"admin detail", func() error { _, e := s.SyncedAccountForAdmin(ctx, account.ID); return e }},
		{"API lookup", func() error {
			_, e := s.LookupSyncedAccountForUser(ctx, owner.ID, vendor.Code, account.Account)
			return e
		}},
		{"collection targets", func() error { _, e := s.CollectionTargets(ctx, 0, 0); return e }},
		{"member page targets", func() error { _, e := s.PageSyncTargets(ctx, owner.ID, []uint64{account.ID}, false); return e }},
		{"admin page targets", func() error { _, e := s.PageSyncTargets(ctx, owner.ID, []uint64{account.ID}, true); return e }},
		{"page response", func() error { _, _, e := s.PageSyncedAccountDTOs(ctx, owner.ID, []uint64{account.ID}, true); return e }},
		{"trend", func() error { _, e := s.AccountSnapshots(ctx, account.ID, 7); return e }},
	}
	var wg sync.WaitGroup
	for round := 0; round < 4; round++ {
		for _, read := range reads {
			wg.Add(1)
			go func(name string, run func() error) {
				defer wg.Done()
				if err := run(); err != nil {
					t.Errorf("%s: %v", name, err)
				}
			}(read.name, read.run)
		}
	}
	wg.Wait()
}
