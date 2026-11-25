package main

import (
	"context"
	"testing"
	"time"

	"goapi-tk-register/server/internal/core"
	"goapi-tk-register/server/internal/supplier"
	"gorm.io/gorm"
)

type reconfiguringAccountCollector struct {
	*mockAccountCollector
	beforeCollect func(context.Context) error
}

func (c *reconfiguringAccountCollector) Collect(ctx context.Context, username, password string) ([]supplier.Account, error) {
	if c.beforeCollect != nil {
		if err := c.beforeCollect(ctx); err != nil {
			return nil, err
		}
	}
	return c.mockAccountCollector.Collect(ctx, username, password)
}

func TestAccountCollectionCapturesIntervalPerRoundWithoutPerAccountReads(t *testing.T) {
	a, owner, _, _, rows, _ := setupPageSync(t)
	configureSync(t, a, owner.ID, true)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	collector := &reconfiguringAccountCollector{mockAccountCollector: &mockAccountCollector{}}
	collector.beforeCollect = func(ctx context.Context) error {
		// An administrator changes the configuration while the supplier request
		// is in progress. All accounts in this round must retain the old cadence.
		collector.beforeCollect = nil
		return a.store.PatchSettings(ctx, map[string]string{"account_sync_interval_seconds": "600"}, owner.ID)
	}
	a.accountCollector = collector

	cadenceReads := 0
	const callback = "test:collection-cadence-reads"
	if err := a.store.DB.Callback().Query().After("gorm:query").Register(callback, func(db *gorm.DB) {
		if db.Statement.Table != "settings" {
			return
		}
		for _, value := range db.Statement.Vars {
			if value == "account_sync_interval_seconds" {
				cadenceReads++
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer a.store.DB.Callback().Query().Remove(callback)

	for round, expected := range []int{300, 600} {
		observedAt := base.Add(time.Duration(round) * time.Minute)
		collector.records = []supplier.Account{
			partialRecord(rows[0], 10, observedAt),
			partialRecord(rows[1], 20, observedAt),
		}
		makeScheduleDue(t, a)
		if err := a.collectAccounts(ctx, 0, 0, true); err != nil {
			t.Fatal(err)
		}
		var snapshots []core.AccountSnapshot
		if err := a.store.DB.Where("collected_at = ?", observedAt).Find(&snapshots).Error; err != nil {
			t.Fatal(err)
		}
		if len(snapshots) != 2 {
			t.Fatalf("round %d: saved %d observations, want 2", round, len(snapshots))
		}
		for _, snapshot := range snapshots {
			if snapshot.CollectionIntervalSeconds == nil || *snapshot.CollectionIntervalSeconds != expected {
				t.Fatalf("round %d: observation did not retain interval %d: %+v", round, expected, snapshot)
			}
		}
	}
	if cadenceReads != 0 {
		t.Fatalf("collection performed %d redundant cadence queries", cadenceReads)
	}
}
