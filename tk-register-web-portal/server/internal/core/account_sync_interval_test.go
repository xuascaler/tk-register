package core

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func intervalSnapshotFixture(t *testing.T) (*Store, SyncedAccount, time.Time) {
	t.Helper()
	s, owner, vendor, version := syncFixture(t)
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	syncSubmission(t, s, owner.ID, vendor.ID, version.ID, "interval@example.com", "claude/cc-max-20x", "accepted", "901", at.Add(-time.Minute))
	rows, err := s.CollectionTargets(context.Background(), owner.ID, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("collection targets: %v %v", rows, err)
	}
	return s, rows[0], at
}

func TestAccountSnapshotWithIntervalUsesRoundCadenceWithoutSettingsQuery(t *testing.T) {
	s, account, at := intervalSnapshotFixture(t)
	ctx := context.Background()
	// The round already read 300; an administrator changes the setting before
	// its observations are saved. This round must not mix old and new cadences.
	if err := s.DB.Model(&Setting{}).Where(clause.Eq{Column: "key", Value: "account_sync_interval_seconds"}).Update("value", "600").Error; err != nil {
		t.Fatal(err)
	}
	const callback = "interval-settings-query-guard"
	guard := func(db *gorm.DB) {
		if db.Statement.Table == "settings" {
			db.AddError(errors.New("snapshot reread settings"))
		}
	}
	if err := s.DB.Callback().Query().Before("gorm:query").Register(callback, guard); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.Callback().Query().Remove(callback) })
	for n, seconds := range []int{300, 300, 600} {
		if err := s.RecordAccountSnapshotWithInterval(ctx, account.ID, account.IdentityKey, AccountSnapshotValues{}, at.Add(time.Duration(n)*time.Minute), "UTC", seconds); err != nil {
			t.Fatal(err)
		}
	}
	var snapshots []AccountSnapshot
	if err := s.DB.Where("account_id = ?", account.ID).Order("collected_at").Find(&snapshots).Error; err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 3 {
		t.Fatalf("saved %d snapshots, want 3", len(snapshots))
	}
	for i, want := range []int{300, 300, 600} {
		if got := snapshots[i].CollectionIntervalSeconds; got == nil || *got != want {
			t.Fatalf("snapshot %d cadence = %v, want %d", i, got, want)
		}
	}
	if err := s.DB.Callback().Query().Remove(callback); err != nil {
		t.Fatal(err)
	}
	// The original single-snapshot API continues to read the current setting.
	if err := s.RecordAccountSnapshot(ctx, account.ID, account.IdentityKey, AccountSnapshotValues{}, at.Add(3*time.Minute), "UTC"); err != nil {
		t.Fatal(err)
	}
	var latest AccountSnapshot
	if err := s.DB.Where("account_id = ?", account.ID).Order("collected_at DESC").First(&latest).Error; err != nil || latest.CollectionIntervalSeconds == nil || *latest.CollectionIntervalSeconds != 600 {
		t.Fatalf("legacy snapshot lost current setting: %+v %v", latest, err)
	}
}

func TestAccountSnapshotWithIntervalRejectsInvalidCadenceAndStaleIdentity(t *testing.T) {
	for _, tc := range []struct {
		name     string
		seconds  int
		mutation map[string]any
	}{
		{name: "zero interval", seconds: 0},
		{name: "short interval", seconds: 59},
		{name: "long interval", seconds: 604801},
		{name: "new lifetime", seconds: 300, mutation: map[string]any{"identity_key": "replacement-lifetime"}},
		{name: "deleting account", seconds: 300, mutation: map[string]any{"deletion_status": "pending"}},
		{name: "deleted account", seconds: 300, mutation: map[string]any{"deleted_at": time.Now().UTC()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, account, at := intervalSnapshotFixture(t)
			ctx := context.Background()
			if len(tc.mutation) > 0 {
				if err := s.DB.Model(&SyncedAccount{}).Where("id = ?", account.ID).Updates(tc.mutation).Error; err != nil {
					t.Fatal(err)
				}
			}
			var before, after SyncedAccount
			if err := s.DB.First(&before, account.ID).Error; err != nil {
				t.Fatal(err)
			}
			err := s.RecordAccountSnapshotWithInterval(ctx, account.ID, account.IdentityKey, AccountSnapshotValues{}, at, "UTC", tc.seconds)
			if len(tc.mutation) == 0 && !IsValidationError(err) {
				t.Fatalf("invalid interval was accepted: %v", err)
			}
			if len(tc.mutation) > 0 && !errors.Is(err, ErrAccountIdentityChanged) {
				t.Fatalf("stale account was accepted: %v", err)
			}
			var count int64
			if err := s.DB.Model(&AccountSnapshot{}).Where("account_id = ?", account.ID).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("rejected snapshot was written: %d %v", count, err)
			}
			if err := s.DB.First(&after, account.ID).Error; err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("rejected snapshot changed account state: %v", err)
			}
		})
	}
}

func TestAccountSnapshotWithIntervalDuplicateRetainsNewerFailure(t *testing.T) {
	s, account, at := intervalSnapshotFixture(t)
	ctx := context.Background()
	cost := 10.0
	if err := s.RecordAccountSnapshotWithInterval(ctx, account.ID, account.IdentityKey, AccountSnapshotValues{CostTotal: &cost}, at, "UTC", 300); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAccountFailure(ctx, account.ID, account.IdentityKey, "Newer collection failed.", at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	cost = 20
	if err := s.RecordAccountSnapshotWithInterval(ctx, account.ID, account.IdentityKey, AccountSnapshotValues{CostTotal: &cost}, at, "UTC", 600); err != nil {
		t.Fatal(err)
	}
	var snapshots []AccountSnapshot
	if err := s.DB.Where("account_id = ?", account.ID).Find(&snapshots).Error; err != nil || len(snapshots) != 1 {
		t.Fatalf("duplicate snapshot saved: %d %v", len(snapshots), err)
	}
	if snapshots[0].CostTotal == nil || *snapshots[0].CostTotal != 10 || snapshots[0].CollectionIntervalSeconds == nil || *snapshots[0].CollectionIntervalSeconds != 300 {
		t.Fatalf("original observation was rewritten: %+v", snapshots[0])
	}
	var current SyncedAccount
	if err := s.DB.First(&current, account.ID).Error; err != nil || current.LastError != "Newer collection failed." || current.LastAttemptAt == nil || !current.LastAttemptAt.Equal(at.Add(time.Minute)) || current.LastSuccessAt == nil || !current.LastSuccessAt.Equal(at) {
		t.Fatalf("duplicate result replaced newer failure: %+v %v", current, err)
	}
}
