package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm/schema"
)

func billExportFixture(t *testing.T) (*Store, User, User, Vendor, ProtocolVersion, time.Time) {
	t.Helper()
	s, user, vendor, version := syncFixture(t)
	admin := User{Email: "export-admin@example.com", PasswordHash: "test", Role: "admin", Status: "active", EmailVerifiedAt: time.Now().UTC()}
	if err := s.CreateUser(context.Background(), &admin); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 12, 1, 0, 0, 0, time.UTC)
	return s, user, admin, vendor, version, now
}

func TestBillExportDateRangeUsesInclusiveBeijingDays(t *testing.T) {
	now := time.Date(2026, 11, 11, 16, 0, 0, 0, time.UTC) // November 12 in Beijing.
	for _, dates := range [][2]string{{"2026-11-12", "2026-11-12"}, {"2026-09-14", "2026-11-12"}} {
		if err := ValidateBillExportDates(dates[0], dates[1], now); err != nil {
			t.Fatal(dates, err)
		}
	}
	for _, dates := range [][2]string{{"2026-09-13", "2026-11-12"}, {"2026-11-12", "2026-11-13"}, {"2026-11-12", "2026-11-11"}, {"2026-2-1", "2026-11-12"}, {"bad", "2026-11-12"}} {
		if err := ValidateBillExportDates(dates[0], dates[1], now); !IsValidationError(err) {
			t.Fatal("invalid dates accepted", dates, err)
		}
	}
}

func TestBillExportFreezesAllCurrentUserClaudeAccountsAndSnapshotIDs(t *testing.T) {
	s, user, admin, vendor, version, now := billExportFixture(t)
	ctx := context.Background()
	for i := 0; i < 23; i++ {
		syncSubmission(t, s, user.ID, vendor.ID, version.ID, fmt.Sprintf("account-%02d@example.com", i), "claude/cc-pro", "accepted", fmt.Sprintf("%d", 1000+i), now.Add(time.Duration(i)*time.Second))
	}
	// Repeated submission must not duplicate a stable account.
	syncSubmission(t, s, user.ID, vendor.ID, version.ID, "account-00@example.com", "claude/cc-pro", "failed", "", now.Add(time.Minute))
	syncSubmission(t, s, user.ID, vendor.ID, version.ID, "codex@example.com", "codex/pro", "accepted", "9000", now.Add(2*time.Minute))
	syncSubmission(t, s, admin.ID, vendor.ID, version.ID, "foreign@example.com", "claude/cc-pro", "accepted", "9001", now.Add(3*time.Minute))
	var first, deleted SyncedAccount
	if err := s.DB.Where("user_id = ? AND account = ?", user.ID, "account-00@example.com").First(&first).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Where("user_id = ? AND account = ?", user.ID, "account-22@example.com").First(&deleted).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Model(&deleted).Update("deleted_at", now).Error; err != nil {
		t.Fatal(err)
	}
	currentID, oldID, earlierID := "platform-current", "platform-old-lifetime", "platform-earlier"
	for i, snap := range []AccountSnapshot{
		{AccountID: first.ID, IdentityKey: first.IdentityKey, AccountSnapshotValues: AccountSnapshotValues{PlatformAccountID: &earlierID}},
		{AccountID: first.ID, IdentityKey: first.IdentityKey, AccountSnapshotValues: AccountSnapshotValues{PlatformAccountID: &currentID}},
		{AccountID: first.ID, IdentityKey: "different-lifetime", AccountSnapshotValues: AccountSnapshotValues{PlatformAccountID: &oldID}},
	} {
		snap.SnapshotDate = "2026-11-12"
		snap.CollectedAt = now.Add(time.Duration(i) * time.Minute)
		snap.DataSource = "website"
		if err := s.DB.Create(&snap).Error; err != nil {
			t.Fatal(err)
		}
	}
	job, err := s.CreateBillExport(ctx, admin.ID, user.ID, "2026-11-11", "2026-11-11", now)
	if err != nil {
		t.Fatal(err)
	}
	if job.AccountCount != 22 || job.UserEmail != user.Email || job.Status != "queued" || job.Timezone != "Asia/Shanghai" {
		t.Fatalf("bad job %+v", job)
	}
	rows, err := s.BillExportAccounts(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 22 {
		t.Fatalf("export used one page or included foreign/deleted accounts: %d", len(rows))
	}
	found := false
	for _, row := range rows {
		if row.LocalAccountID == first.ID {
			found = true
			if row.LatestSubmittedAt == nil || !row.LatestSubmittedAt.Equal(now.Add(time.Minute)) {
				t.Fatalf("latest resubmission time missing from export snapshot: %+v", row)
			}
			if row.PlatformAccountID == nil || *row.PlatformAccountID != currentID || row.RemoteID == nil || *row.RemoteID == currentID {
				t.Fatalf("wrong ID namespace/fallback %+v", row)
			}
		}
		if strings.Contains(row.AccountEmail, "foreign") || strings.Contains(row.AccountEmail, "codex") || row.LocalAccountID == deleted.ID {
			t.Fatalf("out of scope account %+v", row)
		}
	}
	if !found {
		t.Fatal("first account missing")
	}
	// Later ownership, submission time, or ID changes never change the previously frozen selection.
	if err := s.DB.Model(&first).Updates(map[string]any{"user_id": admin.ID, "platform_account_id": "new-platform", "account": "changed@example.com", "latest_submitted_at": now.Add(time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	frozen, err := s.BillExportAccounts(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if frozen[0].LatestSubmittedAt == nil || !frozen[0].LatestSubmittedAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("submission time was not frozen: %+v", frozen[0])
	}
	if frozen[0].AccountEmail != "account-00@example.com" || frozen[0].PlatformAccountID == nil || *frozen[0].PlatformAccountID != currentID {
		t.Fatalf("selection was not frozen: %+v", frozen[0])
	}
}

func TestBillExportAdmissionRequiresAdminAndLimitsQueue(t *testing.T) {
	s, user, admin, vendor, version, now := billExportFixture(t)
	ctx := context.Background()
	if _, err := s.CreateBillExport(ctx, admin.ID, user.ID, "2026-11-11", "2026-11-11", now); !IsValidationError(err) {
		t.Fatal("empty selection accepted", err)
	}
	syncSubmission(t, s, user.ID, vendor.ID, version.ID, "included@example.com", "claude/cc-pro", "accepted", "1", now)
	if _, err := s.CreateBillExport(ctx, user.ID, user.ID, "2026-11-11", "2026-11-11", now); !errors.Is(err, ErrNotFound) {
		t.Fatal("member allowed to create admin export", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := s.CreateBillExport(ctx, admin.ID, user.ID, "2026-11-11", "2026-11-11", now); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err := s.CreateBillExport(ctx, admin.ID, user.ID, "2026-11-11", "2026-11-11", now); !IsValidationError(err) {
		t.Fatal("unbounded admission", err)
	}
}

func TestBillExportClaimRecoveryFencingAndPrivatePayload(t *testing.T) {
	s, user, admin, vendor, version, now := billExportFixture(t)
	ctx := context.Background()
	syncSubmission(t, s, user.ID, vendor.ID, version.ID, "included@example.com", "claude/cc-pro", "accepted", "1", now)
	job, err := s.CreateBillExport(ctx, admin.ID, user.ID, "2026-11-11", "2026-11-11", now)
	if err != nil {
		t.Fatal(err)
	}
	token1, token2 := strings.Repeat("a", 32), strings.Repeat("b", 32)
	claim, err := s.ClaimBillExport(ctx, now, now.Add(time.Minute), token1)
	if err != nil || claim == nil || claim.ID != job.ID {
		t.Fatal(claim, err)
	}
	if other, err := s.ClaimBillExport(ctx, now, now.Add(time.Minute), token2); err != nil || other != nil {
		t.Fatal("running job claimed twice", other, err)
	}
	if err := s.SetBillExportStage(ctx, job.ID, token1, "filtering"); err != nil {
		t.Fatal(err)
	}
	reclaim, err := s.ClaimBillExport(ctx, now.Add(2*time.Minute), now.Add(3*time.Minute), token2)
	if err != nil || reclaim == nil || reclaim.AttemptCount != 2 {
		t.Fatal("stale lease not recovered", reclaim, err)
	}
	result := BillExportResult{MatchedAccountCount: 1, RowCount: 2, TotalCNY: "0.123456", ReportJSON: `{"summary":[{"account":"included@example.com"}]}`, Artifact: []byte("PK-test-artifact")}
	if err := s.CompleteBillExport(ctx, job.ID, token1, result, now); !errors.Is(err, ErrBillExportClaimLost) {
		t.Fatal("stale worker completed job", err)
	}
	if err := s.CompleteBillExport(ctx, job.ID, token2, result, now); err != nil {
		t.Fatal(err)
	}
	meta, err := s.BillExport(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Status != "completed" || meta.TotalCNY != "0.123456" || meta.Artifact != nil || meta.ReportJSON != "" || meta.LeaseToken != "" {
		t.Fatalf("metadata leaks report or omitted totals %+v", meta)
	}
	recent, err := s.RecentBillExports(ctx)
	if err != nil || len(recent) != 1 || recent[0].Artifact != nil || recent[0].ReportJSON != "" {
		t.Fatal("recent jobs leaked file data", err)
	}
	full, err := s.BillExportData(ctx, job.ID)
	if err != nil || full.ReportJSON != result.ReportJSON || string(full.Artifact) != string(result.Artifact) {
		t.Fatal("result persistence", err)
	}
	if again, err := s.ClaimBillExport(ctx, now.Add(time.Hour), now.Add(2*time.Hour), token1); err != nil || again != nil {
		t.Fatal("terminal job reclaimed", err)
	}
	if err := s.ExpireBillExports(ctx, now.Add(BillExportRetention+time.Hour)); err != nil {
		t.Fatal(err)
	}
	expired, err := s.BillExportData(ctx, job.ID)
	if err != nil || expired.Artifact != nil || expired.ReportJSON != "" || expired.TotalCNY != result.TotalCNY {
		t.Fatal("payload not purged or audit lost", err)
	}
}

func TestBillExportDefersOnlyUntilDueAndRejectsOversizedResult(t *testing.T) {
	s, user, admin, vendor, version, now := billExportFixture(t)
	ctx := context.Background()
	syncSubmission(t, s, user.ID, vendor.ID, version.ID, "included@example.com", "claude/cc-pro", "accepted", "1", now)
	job, err := s.CreateBillExport(ctx, admin.ID, user.ID, "2026-11-11", "2026-11-11", now)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("c", 32)
	if _, err := s.ClaimBillExport(ctx, now, now.Add(time.Minute), token); err != nil {
		t.Fatal(err)
	}
	due := now.Add(5 * time.Minute)
	if err := s.DeferBillExport(ctx, job.ID, token, due, "Supplier cooldown"); err != nil {
		t.Fatal(err)
	}
	if claim, err := s.ClaimBillExport(ctx, now, now.Add(time.Minute), token); err != nil || claim != nil {
		t.Fatal("cooldown ignored", err)
	}
	if claim, err := s.ClaimBillExport(ctx, due, due.Add(time.Minute), token); err != nil || claim == nil {
		t.Fatal("deferred job not due", err)
	}
	result := BillExportResult{TotalCNY: "0", ReportJSON: `{}`, Artifact: append([]byte("PK"), make([]byte, MaxBillExportArtifactBytes)...)}
	if err := s.CompleteBillExport(ctx, job.ID, token, result, due); !IsValidationError(err) {
		t.Fatal("oversized result accepted", err)
	}
	if err := s.FailBillExport(ctx, job.ID, token, "TEST_FAILURE", "A safe failure message", due); err != nil {
		t.Fatal(err)
	}
	failed, err := s.BillExport(ctx, job.ID)
	if err != nil || failed.Status != "failed" || failed.ErrorCode != "TEST_FAILURE" {
		t.Fatal(failed, err)
	}
}

func TestBillExportPayloadColumnTypesArePortable(t *testing.T) {
	model, err := schema.Parse(&BillExportJob{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatal(err)
	}
	mysqlDialect := mysql.New(mysql.Config{})
	postgresDialect := postgres.New(postgres.Config{})
	for _, check := range []struct{ field, mysqlType, postgresType string }{
		{"ReportJSON", "longtext", "text"}, {"Artifact", "longblob", "bytea"},
	} {
		field := model.LookUpField(check.field)
		if got := mysqlDialect.DataTypeOf(field); got != check.mysqlType {
			t.Errorf("MySQL %s: %s", check.field, got)
		}
		if got := postgresDialect.DataTypeOf(field); got != check.postgresType {
			t.Errorf("PostgreSQL %s: %s", check.field, got)
		}
	}
}

func TestBillExportMissingSubmissionTimeRemainsNull(t *testing.T) {
	s, user, admin, vendor, version, now := billExportFixture(t)
	ctx := context.Background()
	syncSubmission(t, s, user.ID, vendor.ID, version.ID, "legacy@example.com", "claude/cc-pro", "accepted", "1000", now)
	if err := s.DB.Model(&SyncedAccount{}).Where("user_id = ?", user.ID).Update("latest_submitted_at", time.Time{}).Error; err != nil {
		t.Fatal(err)
	}
	job, err := s.CreateBillExport(ctx, admin.ID, user.ID, "2026-11-11", "2026-11-11", now)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.BillExportAccounts(ctx, job.ID)
	if err != nil || len(rows) != 1 || rows[0].LatestSubmittedAt != nil {
		t.Fatalf("missing submission time must stay null: %+v %v", rows, err)
	}
}
