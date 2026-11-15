package core

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func reasonFixture(t *testing.T) (*Store, SyncedAccount, AccountReasonTarget, time.Time) {
	t.Helper()
	s, u, v, p := syncFixture(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	syncSubmission(t, s, u.ID, v.ID, p.ID, "reason@example.com", "claude/cc-pro", "accepted", "111", at.Add(-time.Minute))
	rows, e := s.CollectionTargets(ctx, u.ID, 0)
	if e != nil || len(rows) != 1 {
		t.Fatal(e, len(rows))
	}
	a := rows[0]
	status := "sync_failed"
	cost := 12.5
	e = s.RecordAccountSnapshot(ctx, a.ID, a.IdentityKey, AccountSnapshotValues{SupplierRecordID: "222", AccountStatus: &status, UpstreamStatusRaw: &status, CostTotal: &cost}, at, "UTC")
	if e != nil {
		t.Fatal(e)
	}
	targets, e := s.MissingAccountReasons(ctx, 0, 20)
	if e != nil || len(targets) != 1 {
		t.Fatal(e, targets)
	}
	if targets[0].SupplierRecordID != "222" {
		t.Fatal("confirmed supplier ID was not saved")
	}
	return s, a, targets[0], at
}
func TestBackgroundReasonPersistsOnlyReasonAndSkipsCompleted(t *testing.T) {
	for _, reason := range []string{"proxy service unavailable", ""} {
		t.Run(fmt.Sprint(len(reason)), func(t *testing.T) {
			s, a, target, at := reasonFixture(t)
			ctx := context.Background()
			if e := s.RecordAccountFailure(ctx, a.ID, a.IdentityKey, "Last collection timed out", at.Add(time.Minute)); e != nil {
				t.Fatal(e)
			}
			saved, e := s.SaveAccountReason(ctx, target, reason)
			if e != nil || !saved {
				t.Fatal(saved, e)
			}
			if saved, e = s.SaveAccountReason(ctx, target, "different reason"); e != nil || saved {
				t.Fatal("overwrote completed reason", e)
			}
			dto, e := s.SyncedAccountForUser(ctx, a.UserID, a.ID)
			if e != nil || dto.Snapshot == nil || dto.Snapshot.UpstreamErrorMessage == nil || *dto.Snapshot.UpstreamErrorMessage != reason || *dto.Snapshot.CostTotal != 12.5 || !dto.LastSuccessAt.Equal(at) || !dto.LastAttemptAt.Equal(at.Add(time.Minute)) || dto.LastError != "Last collection timed out" {
				t.Fatal("reason changed collection result", dto, e)
			}
			var count int64
			s.DB.Model(&AccountSnapshot{}).Count(&count)
			if count != 1 {
				t.Fatal("backfill added a sample")
			}
			// Scheduled reads carry a fetched reason, including a successful empty result.
			status := "sync_failed"
			e = s.RecordAccountSnapshot(ctx, a.ID, a.IdentityKey, AccountSnapshotValues{AccountStatus: &status, UpstreamStatusRaw: &status}, at.Add(2*time.Minute), "UTC")
			if e != nil {
				t.Fatal(e)
			}
			targets, e := s.MissingAccountReasons(ctx, 0, 20)
			if e != nil || len(targets) != 0 {
				t.Fatal("already completed result queued again", e, targets)
			}
			if s.DB.Migrator().HasColumn(&AccountSnapshot{}, "supplier_record_id") {
				t.Fatal("transient ID created snapshot column")
			}
		})
	}
}
func TestBackgroundReasonDiscardsStaleAndUnavailableTargets(t *testing.T) {
	for _, scenario := range []string{"new_snapshot", "new_status", "identity", "supplier_id", "deleted", "deleting", "unaccepted", "manual_reason"} {
		t.Run(scenario, func(t *testing.T) {
			s, a, target, at := reasonFixture(t)
			ctx := context.Background()
			switch scenario {
			case "new_snapshot", "new_status":
				status := "sync_failed"
				if scenario == "new_status" {
					status = "offline"
				}
				if e := s.RecordAccountSnapshot(ctx, a.ID, a.IdentityKey, AccountSnapshotValues{AccountStatus: &status, UpstreamStatusRaw: &status}, at.Add(time.Minute), "UTC"); e != nil {
					t.Fatal(e)
				}
			case "identity":
				s.DB.Model(&a).Update("identity_key", "replacement")
			case "supplier_id":
				s.DB.Model(&a).Update("supplier_record_id", "333")
			case "deleted":
				s.DB.Model(&a).Update("deleted_at", time.Now())
			case "deleting":
				s.DB.Model(&a).Update("deletion_status", "pending")
			case "unaccepted":
				s.DB.Model(&a).Update("current_submission_id", nil)
			case "manual_reason":
				s.DB.Model(&AccountSnapshot{}).Where("id = ?", target.SnapshotID).UpdateColumn("upstream_error_message", "manually checked")
			}
			if saved, e := s.SaveAccountReason(ctx, target, "outdated"); e != nil || saved {
				t.Fatal("stale reason saved", e)
			}
			var n int64
			s.DB.Model(&AccountSnapshot{}).Where("upstream_error_message = ?", "outdated").Count(&n)
			if n != 0 {
				t.Fatal("stale reason persisted")
			}
		})
	}
}
func TestBackgroundReasonCandidateEligibilityAndCursor(t *testing.T) {
	for _, scenario := range []string{"healthy", "no_supplier_id", "no_raw_status", "disabled_vendor", "foreign_vendor", "deleting", "deleted", "unaccepted", "cursor"} {
		t.Run(scenario, func(t *testing.T) {
			s, a, target, _ := reasonFixture(t)
			ctx := context.Background()
			after := uint64(0)
			switch scenario {
			case "healthy":
				s.DB.Model(&AccountSnapshot{}).Where("id = ?", target.SnapshotID).Update("account_status", "active")
			case "no_raw_status":
				s.DB.Model(&AccountSnapshot{}).Where("id = ?", target.SnapshotID).Update("upstream_status_raw", nil)
			case "no_supplier_id":
				s.DB.Model(&a).Update("supplier_record_id", nil)
			case "disabled_vendor":
				s.DB.Model(&Vendor{}).Where("id = ?", a.VendorID).Update("enabled", false)
			case "foreign_vendor":
				s.DB.Model(&Vendor{}).Where("id = ?", a.VendorID).Update("base_url", "https://other.example")
			case "deleting":
				s.DB.Model(&a).Update("deletion_status", "pending")
			case "deleted":
				s.DB.Model(&a).Update("deleted_at", time.Now())
			case "unaccepted":
				s.DB.Model(&a).Update("current_submission_id", nil)
			case "cursor":
				after = a.ID
			}
			targets, e := s.MissingAccountReasons(ctx, after, 20)
			if e != nil || len(targets) != 0 {
				t.Fatal("ineligible target queried", e, targets)
			}
		})
	}
}
