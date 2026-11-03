package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAccountDeletionArchivesOnceAndBlocksResurrection(t *testing.T) {
	s, owner, vendor, version := syncFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	sub := syncSubmission(t, s, owner.ID, vendor.ID, version.ID, "delete@example.com", "claude/cc-pro", "accepted", "301", now.Add(-time.Hour))
	key := "delete-fixture-active-key"
	if err := s.DB.Model(&sub).Update("active_account_key", key).Error; err != nil {
		t.Fatal(err)
	}
	job := enqueuePostSubmissionFixture(t, s, sub, now.Add(-time.Minute))
	targets, err := s.CollectionTargets(ctx, owner.ID, 0)
	if err != nil || len(targets) != 1 {
		t.Fatal(targets, err)
	}
	target := targets[0]
	status, cost, currency := "active", 12.3, "CNY"
	if err = s.RecordAccountSnapshot(ctx, target.ID, target.IdentityKey, AccountSnapshotValues{AccountStatus: &status, CostTotal: &cost, Currency: &currency}, now.Add(-time.Minute), "UTC"); err != nil {
		t.Fatal(err)
	}
	audit, err := s.BeginAuthJSONUpdate(ctx, target, owner.ID, "before-delete-audit")
	if err != nil {
		t.Fatal(err)
	}
	op, _, err := s.BeginAccountDeletion(ctx, target.ID, owner.ID, false, "delete-request", now)
	if err != nil {
		t.Fatal(err)
	}
	if op.Status != "pending" || op.AccountID != target.ID {
		t.Fatal(op)
	}
	if _, err = s.BeginAuthJSONUpdate(ctx, target, owner.ID, "during-delete"); !errors.Is(err, ErrAccountIdentityChanged) {
		t.Fatal("accepted credential mutation during deletion", err)
	}
	if err = s.RecordAccountSnapshot(ctx, target.ID, target.IdentityKey, AccountSnapshotValues{}, now, "UTC"); !errors.Is(err, ErrAccountIdentityChanged) {
		t.Fatal("accepted in-flight snapshot", err)
	}
	if err = s.RecordAccountFailure(ctx, target.ID, target.IdentityKey, "late failure", now); !errors.Is(err, ErrAccountIdentityChanged) {
		t.Fatal("accepted in-flight failure", err)
	}
	if err = s.ConfirmSupplierRecord(ctx, target.ID, target.IdentityKey, "301"); !errors.Is(err, ErrAccountIdentityChanged) {
		t.Fatal("accepted in-flight mapping", err)
	}
	again, _, err := s.BeginAccountDeletion(ctx, target.ID, owner.ID, false, "other-click", now)
	if err != nil || again.ID != op.ID {
		t.Fatal("duplicate click created an operation", again, err)
	}
	if err = s.MarkAccountDeletionWriteStarted(ctx, op.ID, "301", "2026-11-01", "2026-12-01", now); err != nil {
		t.Fatal(err)
	}
	if err = s.FailAccountDeletion(ctx, op.ID, "TIMEOUT", now); !errors.Is(err, ErrAccountIdentityChanged) {
		t.Fatal("uncertain write cleared deletion", err)
	}
	if err = s.ConfirmAccountDeletion(ctx, op.ID, "upstream_dates_updated", "301", now); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = s.FinishAccountDeletion(ctx, op.ID, now); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.EnsureSyncedAccounts(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListSyncedAccounts(ctx, owner.ID, 1, "", "")
	if err != nil || page.Total != 0 {
		t.Fatal("deleted account reappeared", page, err)
	}
	if _, err = s.LookupSyncedAccountForUser(ctx, owner.ID, "tk-register-0", target.Account); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted account available via token lookup", err)
	}
	if _, err = s.SyncedAccountForAdmin(ctx, target.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted account available via admin account endpoint", err)
	}
	var archives []DeletedAccount
	if err = s.DB.Find(&archives).Error; err != nil || len(archives) != 1 {
		t.Fatal(archives, err)
	}
	if archives[0].OwnerUserID != owner.ID || archives[0].DeletionMode != "upstream_dates_updated" || !strings.Contains(archives[0].SnapshotJSON, "2026-12-01") || !strings.Contains(archives[0].SnapshotJSON, "12.3") {
		t.Fatal("archive missing original metadata", archives[0])
	}
	var saved Submission
	if err = s.DB.First(&saved, sub.ID).Error; err != nil || saved.Status != "accepted" || saved.ActiveAccountKey != nil {
		t.Fatal("submission history removed or slot held", saved, err)
	}
	var snapshots int64
	s.DB.Model(&AccountSnapshot{}).Where("account_id = ?", target.ID).Count(&snapshots)
	if snapshots != 1 {
		t.Fatal("history snapshot removed")
	}
	var savedJob PostSubmissionSyncJob
	s.DB.First(&savedJob, job.ID)
	if savedJob.Status != "cancelled" {
		t.Fatal("follow-up job still active", savedJob)
	}
	unchanged, err := s.FinishAuthJSONUpdate(ctx, audit, "updated", "301", "", now)
	if err != nil || unchanged {
		t.Fatal("late credential completion revived account", unchanged, err)
	}
	// New failed attempts remain historical. Only a NEW accepted submission can
	// reactivate, and even an identical remote ID gets a fresh identity.
	syncSubmission(t, s, owner.ID, vendor.ID, version.ID, target.Account, target.Plan, "failed", "", now.Add(time.Minute))
	if err = s.EnsureSyncedAccounts(ctx); err != nil {
		t.Fatal(err)
	}
	page, _ = s.ListSyncedAccounts(ctx, owner.ID, 1, "", "")
	if page.Total != 0 {
		t.Fatal("failed retry restored tombstone")
	}
	newSub := syncSubmission(t, s, owner.ID, vendor.ID, version.ID, target.Account, target.Plan, "accepted", "301", now.Add(2*time.Minute))
	if err = s.EnsureSyncedAccounts(ctx); err != nil {
		t.Fatal(err)
	}
	targets, err = s.CollectionTargets(ctx, owner.ID, 0)
	if err != nil || len(targets) != 1 || targets[0].IdentityKey == target.IdentityKey || targets[0].DeletedAt != nil || targets[0].CurrentSubmissionID == nil || *targets[0].CurrentSubmissionID != newSub.ID {
		t.Fatal("new accepted lifetime not isolated", targets, err)
	}
	dto, err := s.SyncedAccountForUser(ctx, owner.ID, target.ID)
	if err != nil || dto.Snapshot != nil {
		t.Fatal("old usage leaked into new lifetime", dto, err)
	}
}

func TestAccountDeletionFailedOnlyDoesNotTouchOtherOwner(t *testing.T) {
	s, owner, vendor, version := syncFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	other := tokenUser(t, s, "other-deletion-owner@example.com")
	syncSubmission(t, s, other.ID, vendor.ID, version.ID, "same@example.com", "claude/cc-pro", "accepted", "900", now.Add(-time.Hour))
	syncSubmission(t, s, owner.ID, vendor.ID, version.ID, "same@example.com", "claude/cc-pro", "failed", "", now.Add(-time.Minute))
	page, err := s.ListSyncedAccounts(ctx, owner.ID, 1, "", "")
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	id, _ := ParseBusinessID(page.Items[0].ID)
	if _, _, err = s.BeginAccountDeletion(ctx, id, other.ID, false, "foreign-delete", now); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign owner can delete", err)
	}
	op, target, err := s.BeginAccountDeletion(ctx, id, owner.ID, false, "failed-delete", now)
	if err != nil || target.CurrentSubmissionID != nil {
		t.Fatal(op, target, err)
	}
	if err = s.MarkAccountDeletionWriteStarted(ctx, op.ID, "900", "2026-01-01", "2026-12-31", now); !errors.Is(err, ErrAccountDeletionUnconfirmed) {
		t.Fatal("failed record allowed vendor mutation", err)
	}
	if err = s.ConfirmAccountDeletion(ctx, op.ID, "local_only", "", now); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishAccountDeletion(ctx, op.ID, now); err != nil {
		t.Fatal(err)
	}
	otherPage, err := s.ListSyncedAccounts(ctx, other.ID, 1, "", "")
	if err != nil || otherPage.Total != 1 || otherPage.Items[0].CurrentSubmissionID == nil {
		t.Fatal("other owner changed", otherPage, err)
	}
}

func TestAccountDeletionFailedRequestsAreImmutableAndRetriesNewOperations(t *testing.T) {
	s, owner, target := authAuditFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	op, _, err := s.BeginAccountDeletion(ctx, target.ID, owner.ID, false, "retry-one", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FailAccountDeletion(ctx, op.ID, "SUPPLIER_UNAVAILABLE", now); err != nil {
		t.Fatal(err)
	}
	replay, _, err := s.BeginAccountDeletion(ctx, target.ID, owner.ID, false, "retry-one", now)
	if err != nil || replay.ID != op.ID || replay.Status != "failed" {
		t.Fatal("failed request automatically restarted", replay, err)
	}
	retry, _, err := s.BeginAccountDeletion(ctx, target.ID, owner.ID, false, "retry-two", now)
	if err != nil || retry.ID == op.ID || retry.Status != "pending" {
		t.Fatal("intentional retry not separate", retry, err)
	}
	if err = s.MarkAccountDeletionWriteStarted(ctx, retry.ID, "301", "2026-01-01", "2026-12-01", now); err != nil {
		t.Fatal(err)
	}
	if err = s.RejectAccountDeletion(ctx, retry.ID, "UPSTREAM_REJECTED", now); err != nil {
		t.Fatal(err)
	}
	var current SyncedAccount
	s.DB.First(&current, target.ID)
	if !current.AvailableForMutation() {
		t.Fatal("explicit rejection did not release pending state", current)
	}
	var count int64
	s.DB.Model(&AccountDeletionOperation{}).Count(&count)
	if count != 2 {
		t.Fatal("lost failed deletion audit", count)
	}
}

func TestAccountDeletionUnknownSubmissionCannotBeLocalOnly(t *testing.T) {
	s, owner, vendor, version := syncFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	syncSubmission(t, s, owner.ID, vendor.ID, version.ID, "uncertain@example.com", "claude/cc-pro", "unknown", "", now.Add(-time.Minute))
	page, err := s.ListSyncedAccounts(ctx, owner.ID, 1, "", "")
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	id, _ := ParseBusinessID(page.Items[0].ID)
	if _, _, err = s.BeginAccountDeletion(ctx, id, owner.ID, false, "unknown-delete", now); !errors.Is(err, ErrAccountDeletionUnconfirmed) {
		t.Fatal("uncertain submission treated as failed", err)
	}
}

func TestDeletedAcceptedHistoryNoLongerClaimsOtherOwnersAccount(t *testing.T) {
	s, owner, target := authAuditFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := s.ConfirmSupplierRecord(ctx, target.ID, target.IdentityKey, "301"); err != nil {
		t.Fatal(err)
	}
	op, _, err := s.BeginAccountDeletion(ctx, target.ID, owner.ID, false, "ownership-delete", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ConfirmAccountDeletion(ctx, op.ID, "upstream_not_found", "", now); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishAccountDeletion(ctx, op.ID, now); err != nil {
		t.Fatal(err)
	}
	other := tokenUser(t, s, "next-owner@example.com")
	var sub Submission
	s.DB.First(&sub, *target.CurrentSubmissionID)
	syncSubmission(t, s, other.ID, sub.VendorID, sub.ProtocolVersionID, target.Account, target.Plan, "accepted", "301", now.Add(time.Minute))
	targets, err := s.CollectionTargets(ctx, other.ID, 0)
	if err != nil || len(targets) != 1 {
		t.Fatal(targets, err)
	}
	recordID := "301"
	targets[0].SupplierRecordID = &recordID
	if err = s.CheckAuthJSONOwnership(ctx, targets[0]); err != nil {
		t.Fatal("archived owner retains active credential claim", err)
	}
}

func TestAccountDeletionCutoffIncludesBackdatedImportedSubmissions(t *testing.T) {
	s, owner, vendor, version := syncFixture(t)
	ctx, now := context.Background(), time.Now().UTC()
	latest := syncSubmission(t, s, owner.ID, vendor.ID, version.ID, "imported@example.com", "claude/cc-pro", "accepted", "501", now.Add(-time.Hour))
	// A later database insertion can have an earlier imported creation time.
	// The cutoff must cover its ID even though it is not LatestSubmissionID.
	imported := syncSubmission(t, s, owner.ID, vendor.ID, version.ID, latest.Account, latest.Plan, "accepted", "502", now.Add(-2*time.Hour))
	targets, err := s.CollectionTargets(ctx, owner.ID, 0)
	if err != nil || len(targets) != 1 || targets[0].LatestSubmissionID != latest.ID || imported.ID <= latest.ID {
		t.Fatal("invalid backdated fixture", targets, err)
	}
	op, _, err := s.BeginAccountDeletion(ctx, targets[0].ID, owner.ID, false, "backdated-deletion", now)
	if err != nil || op.ThroughSubmissionID != imported.ID {
		t.Fatal("deletion cutoff missed an older imported record", op, err)
	}
	if err = s.ConfirmAccountDeletion(ctx, op.ID, "upstream_not_found", "", now); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishAccountDeletion(ctx, op.ID, now); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		page, err := s.ListSyncedAccounts(ctx, owner.ID, 1, "", "")
		if err != nil || page.Total != 0 {
			t.Fatal("backdated accepted submission resurrected deleted account", page, err)
		}
	}
}
