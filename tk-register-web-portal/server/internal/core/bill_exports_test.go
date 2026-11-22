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
		if row.Username != user.Email {
			t.Fatalf("actual account owner missing from export snapshot: %+v", row)
		}
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
	if frozen[0].Username != user.Email || frozen[0].AccountEmail != "account-00@example.com" || frozen[0].PlatformAccountID == nil || *frozen[0].PlatformAccountID != currentID {
		t.Fatalf("selection was not frozen: %+v", frozen[0])
	}
}

func TestBillExportOptionalDescendantsFreezeActualOwnersAndFullTree(t *testing.T) {
	s, owner, admin, vendor, version, now := billExportFixture(t)
	ctx := context.Background()
	child := hierarchyChild(t, s, owner, "export-child@example.com")
	grandchild := hierarchyChild(t, s, child, "export-grandchild@example.com")
	greatGrandchild := hierarchyChild(t, s, grandchild, "export-great-grandchild@example.com")
	sibling := hierarchyChild(t, s, owner, "export-sibling@example.com")
	users := []User{owner, child, grandchild, greatGrandchild, sibling, admin}
	owners := map[string]string{}
	for i, user := range users {
		email := fmt.Sprintf("tree-account-%d@example.com", i)
		syncSubmission(t, s, user.ID, vendor.ID, version.ID, email, "claude/cc-pro", "accepted", fmt.Sprint(700+i), now)
		owners[email] = user.Email
	}
	self, err := s.CreateBillExport(ctx, admin.ID, owner.ID, "2026-11-10", "2026-11-11", now)
	if err != nil || self.IncludeSubUsers || self.AccountCount != 1 {
		t.Fatalf("default export must include only direct ownership: %+v %v", self, err)
	}
	all, err := s.CreateBillExport(ctx, admin.ID, owner.ID, "2026-11-10", "2026-11-11", now, BillExportOptions{IncludeSubUsers: true})
	if err != nil || !all.IncludeSubUsers || all.AccountCount != 5 || all.UsageSource != "modified" {
		t.Fatalf("full descendant export: %+v %v", all, err)
	}
	rows, err := s.BillExportAccounts(ctx, all.ID)
	if err != nil || len(rows) != 5 {
		t.Fatalf("descendant selection: %+v %v", rows, err)
	}
	seen := map[uint64]bool{}
	for _, row := range rows {
		if seen[row.LocalAccountID] || row.Username != owners[row.AccountEmail] || row.Username == admin.Email {
			t.Fatalf("repeated visibility, wrong owner, or foreign account: %+v", row)
		}
		seen[row.LocalAccountID] = true
		// Each selected account still contributes every valid date/model line.
		for _, date := range []string{"2026-11-10", "2026-11-11"} {
			day := AccountUsageDay{AccountID: row.LocalAccountID, IdentityKey: row.IdentityKey, UsageDate: date, LastSuccessAt: &now}
			if err := s.DB.Create(&day).Error; err != nil {
				t.Fatal(err)
			}
			for _, model := range []string{"claude-fable-5", "claude-opus-5"} {
				usage := AccountDailyUsageModified{DayID: day.ID, Model: model, CostCNY: "3.25", InputTokens: "10", OutputTokens: "20", CacheReadTokens: "30", CacheWriteTokens: "40"}
				if err := s.DB.Create(&usage).Error; err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	usage, err := s.ModifiedBillExportRows(ctx, all.ID)
	if err != nil || len(usage) != 20 {
		t.Fatalf("date/model lines were dropped or duplicated: %+v %v", usage, err)
	}
	subtree, err := s.CreateBillExport(ctx, admin.ID, child.ID, "2026-11-10", "2026-11-11", now, BillExportOptions{IncludeSubUsers: true})
	if err != nil || subtree.AccountCount != 3 {
		t.Fatalf("subtree includes ancestors or siblings: %+v %v", subtree, err)
	}
	newChild := hierarchyChild(t, s, greatGrandchild, "export-new-child@example.com")
	syncSubmission(t, s, newChild.ID, vendor.ID, version.ID, "new-after-export@example.com", "claude/cc-pro", "accepted", "800", now.Add(time.Minute))
	if err := s.DB.Model(&User{}).Where("id = ?", child.ID).Update("email", "renamed-child@example.com").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Model(&SyncedAccount{}).Where("user_id = ?", child.ID).Update("user_id", admin.ID).Error; err != nil {
		t.Fatal(err)
	}
	frozen, err := s.BillExportAccounts(ctx, all.ID)
	if err != nil || len(frozen) != 5 {
		t.Fatalf("new descendant changed an existing export: %+v %v", frozen, err)
	}
	for _, row := range frozen {
		if row.Username != owners[row.AccountEmail] {
			t.Fatalf("later owner changes altered the snapshot: %+v", row)
		}
	}
	usage, err = s.ModifiedBillExportRows(ctx, all.ID)
	if err != nil || len(usage) != 20 {
		t.Fatalf("later ownership changed frozen bill lines: %+v %v", usage, err)
	}
}

func TestBillExportDescendantsWorkWithoutDirectAccounts(t *testing.T) {
	s, owner, admin, vendor, version, now := billExportFixture(t)
	ctx := context.Background()
	child := hierarchyChild(t, s, owner, "only-child@example.com")
	syncSubmission(t, s, child.ID, vendor.ID, version.ID, "only-child-account@example.com", "claude/cc-pro", "accepted", "900", now)
	if _, err := s.CreateBillExport(ctx, admin.ID, owner.ID, "2026-11-11", "2026-11-11", now, BillExportOptions{IncludeSubUsers: false}); !IsValidationError(err) {
		t.Fatal("direct-only empty export accepted", err)
	}
	job, err := s.CreateBillExport(ctx, admin.ID, owner.ID, "2026-11-11", "2026-11-11", now, BillExportOptions{IncludeSubUsers: true})
	if err != nil || job.AccountCount != 1 {
		t.Fatalf("root without direct accounts could not export its child: %+v %v", job, err)
	}
	rows, err := s.BillExportAccounts(ctx, job.ID)
	if err != nil || len(rows) != 1 || rows[0].Username != child.Email {
		t.Fatalf("child owner was replaced with root: %+v %v", rows, err)
	}
}

func TestBillExportScopeColumnsMigrateExistingRows(t *testing.T) {
	s, owner, admin, vendor, version, now := billExportFixture(t)
	ctx := context.Background()
	syncSubmission(t, s, owner.ID, vendor.ID, version.ID, "before-upgrade@example.com", "claude/cc-pro", "accepted", "1000", now)
	job, err := s.CreateBillExport(ctx, admin.ID, owner.ID, "2026-11-11", "2026-11-11", now)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"ALTER TABLE bill_export_jobs DROP COLUMN include_sub_users", "ALTER TABLE bill_export_accounts DROP COLUMN username"} {
		if err := s.DB.Exec(query).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := s.migrateBillExports(); err != nil {
		t.Fatal(err)
	}
	old, err := s.BillExport(ctx, admin.ID, job.ID)
	if err != nil || old.IncludeSubUsers {
		t.Fatalf("existing export gained descendant scope: %+v %v", old, err)
	}
	rows, err := s.BillExportAccounts(ctx, job.ID)
	if err != nil || len(rows) != 1 || rows[0].Username != "" {
		t.Fatalf("existing snapshot was recomputed during migration: %+v %v", rows, err)
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
	meta, err := s.BillExport(ctx, admin.ID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Status != "completed" || meta.TotalCNY != "0.123456" || meta.Artifact != nil || meta.ReportJSON != "" || meta.LeaseToken != "" {
		t.Fatalf("metadata leaks report or omitted totals %+v", meta)
	}
	recent, err := s.RecentBillExports(ctx, admin.ID)
	if err != nil || len(recent) != 1 || recent[0].Artifact != nil || recent[0].ReportJSON != "" {
		t.Fatal("recent jobs leaked file data", err)
	}
	full, err := s.BillExportData(ctx, admin.ID, job.ID)
	if err != nil || full.ReportJSON != result.ReportJSON || string(full.Artifact) != string(result.Artifact) {
		t.Fatal("result persistence", err)
	}
	if again, err := s.ClaimBillExport(ctx, now.Add(time.Hour), now.Add(2*time.Hour), token1); err != nil || again != nil {
		t.Fatal("terminal job reclaimed", err)
	}
	if err := s.ExpireBillExports(ctx, now.Add(BillExportRetention+time.Hour)); err != nil {
		t.Fatal(err)
	}
	expired, err := s.BillExportData(ctx, admin.ID, job.ID)
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
	failed, err := s.BillExport(ctx, admin.ID, job.ID)
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

func TestBillExportSourceUpgradeAndViewerIsolation(t *testing.T) {
	s, owner, admin, vendor, version, now := billExportFixture(t)
	ctx := context.Background()
	syncSubmission(t, s, owner.ID, vendor.ID, version.ID, "selected@example.com", "claude/cc-pro", "accepted", "1100", now)
	old, err := s.CreateBillExport(ctx, admin.ID, owner.ID, "2026-11-10", "2026-11-11", now)
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce the deployed schema, with a pre-existing supplier workbook.
	if err := s.DB.Model(&old).Updates(map[string]any{"report_json": `{"total":"raw"}`, "artifact": []byte("PK-raw-file"), "status": "completed", "total_cny": "999.00"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Migrator().DropIndex(&BillExportJob{}, "UsageSource"); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Exec("ALTER TABLE bill_export_jobs DROP COLUMN usage_source").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.migrateBillExports(); err != nil {
		t.Fatal(err)
	}
	ordinary, err := s.CreateBillExport(WithModelUsageSource(ctx, "raw"), admin.ID, owner.ID, "2026-11-10", "2026-11-11", now)
	if err != nil || ordinary.UsageSource != "modified" {
		t.Fatal("ordinary admin selected a raw job", ordinary.UsageSource, err)
	}
	meta, err := s.BillExport(ctx, admin.ID, old.ID)
	if !errors.Is(err, ErrNotFound) || meta.TotalCNY != "" {
		t.Fatal("old job's raw metadata returned", err)
	}
	full, err := s.BillExportData(ctx, admin.ID, old.ID)
	if !errors.Is(err, ErrNotFound) || len(full.Artifact) != 0 || full.ReportJSON != "" {
		t.Fatal("old supplier result returned", err)
	}
	recent, err := s.RecentBillExports(ctx, admin.ID)
	if err != nil || len(recent) != 1 || recent[0].ID != ordinary.ID {
		t.Fatal("recent list includes raw metadata", recent, err)
	}
	if _, err := s.BillExport(ctx, owner.ID, ordinary.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("ordinary user read exports", err)
	}
	if err := s.DB.Model(&admin).Update("is_super_admin", true).Error; err != nil {
		t.Fatal(err)
	}
	superJob, err := s.CreateBillExport(ctx, admin.ID, owner.ID, "2026-11-10", "2026-11-11", now)
	if err != nil || superJob.UsageSource != "raw" {
		t.Fatal("super-admin upstream export", superJob.UsageSource, err)
	}
	legacy, err := s.BillExportData(ctx, admin.ID, old.ID)
	if err != nil || legacy.UsageSource != "raw" || legacy.TotalCNY != "999.00" || string(legacy.Artifact) != "PK-raw-file" {
		t.Fatal("legacy export no longer intact", err)
	}
	if err := s.DB.Model(&admin).Update("is_super_admin", false).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.BillExportData(ctx, admin.ID, superJob.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("demoted admin kept raw access", err)
	}
}

func TestBillExportSourceChoiceRequiresCurrentSuperAdmin(t *testing.T) {
	s, owner, admin, vendor, version, now := billExportFixture(t)
	ctx := context.Background()
	child := hierarchyChild(t, s, owner, "source-child@example.com")
	syncSubmission(t, s, owner.ID, vendor.ID, version.ID, "source-owner@example.com", "claude/cc-pro", "accepted", "1100", now)
	syncSubmission(t, s, child.ID, vendor.ID, version.ID, "source-child-account@example.com", "claude/cc-pro", "accepted", "1101", now)
	if err := s.DB.Model(&admin).Update("is_super_admin", true).Error; err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		requested string
		include   bool
		want      string
		count     int
	}{{"", false, "raw", 1}, {"raw", true, "raw", 2}, {"cache", true, "modified", 2}, {"modified", true, "modified", 2}} {
		job, err := s.CreateBillExport(WithModelUsageSource(ctx, "modified"), admin.ID, owner.ID, "2026-11-10", "2026-11-11", now, BillExportOptions{IncludeSubUsers: tc.include, UsageSource: tc.requested})
		if err != nil || job.UsageSource != tc.want || job.IncludeSubUsers != tc.include || job.AccountCount != tc.count {
			t.Fatalf("source/scope %q: %+v %v", tc.requested, job, err)
		}
	}
	var before, after int64
	if err := s.DB.Model(&BillExportJob{}).Count(&before).Error; err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"invalid", "Raw", " raw "} {
		job, err := s.CreateBillExport(ctx, admin.ID, owner.ID, "2026-11-10", "2026-11-11", now, BillExportOptions{UsageSource: invalid})
		if !IsValidationError(err) || job.ID != 0 {
			t.Fatalf("invalid source %q created a job: %+v %v", invalid, job, err)
		}
	}
	if err := s.DB.Model(&BillExportJob{}).Count(&after).Error; err != nil || after != before {
		t.Fatalf("invalid source persisted export jobs: %d => %d, %v", before, after, err)
	}
	// The request must not rely on an actor role cached before demotion.
	if err := s.DB.Model(&admin).Update("is_super_admin", false).Error; err != nil {
		t.Fatal(err)
	}
	job, err := s.CreateBillExport(WithModelUsageSource(ctx, "raw"), admin.ID, owner.ID, "2026-11-10", "2026-11-11", now, BillExportOptions{IncludeSubUsers: true, UsageSource: "raw"})
	if err != nil || job.UsageSource != "modified" || !job.IncludeSubUsers || job.AccountCount != 2 {
		t.Fatalf("demoted admin obtained Raw or lost subtree scope: %+v %v", job, err)
	}
}

func TestBillExportOrdinaryAdminAlwaysUsesModified(t *testing.T) {
	s, owner, admin, vendor, version, now := billExportFixture(t)
	ctx := WithModelUsageSource(context.Background(), "raw")
	syncSubmission(t, s, owner.ID, vendor.ID, version.ID, "ordinary-source@example.com", "claude/cc-pro", "accepted", "1200", now)
	for _, source := range []string{"", "raw", "cache", "modified", "invalid"} {
		job, err := s.CreateBillExport(ctx, admin.ID, owner.ID, "2026-11-10", "2026-11-11", now, BillExportOptions{UsageSource: source})
		if err != nil || job.UsageSource != "modified" {
			t.Fatalf("ordinary source %q selected data outside Modified: %+v %v", source, job, err)
		}
	}
}

func TestBillExportSourceSnapshotIgnoresLaterGlobalConfiguration(t *testing.T) {
	s, owner, admin, vendor, version, now := billExportFixture(t)
	ctx := context.Background()
	syncSubmission(t, s, owner.ID, vendor.ID, version.ID, "frozen-source@example.com", "claude/cc-pro", "accepted", "1300", now)
	if err := s.DB.Model(&admin).Update("is_super_admin", true).Error; err != nil {
		t.Fatal(err)
	}
	jobs := map[uint64]string{}
	for _, selected := range []string{"raw", "modified"} {
		other := "raw"
		if selected == "raw" {
			other = "modified"
		}
		if err := s.PatchSettings(ctx, map[string]string{"model_usage_source": other}, admin.ID); err != nil {
			t.Fatal(err)
		}
		job, err := s.CreateBillExport(WithModelUsageSource(ctx, other), admin.ID, owner.ID, "2026-11-10", "2026-11-11", now, BillExportOptions{UsageSource: selected})
		if err != nil || job.UsageSource != selected {
			t.Fatalf("global source overrode explicit job source: %+v %v", job, err)
		}
		jobs[job.ID] = selected
	}
	for _, global := range []string{"raw", "modified"} {
		if err := s.PatchSettings(ctx, map[string]string{"model_usage_source": global}, admin.ID); err != nil {
			t.Fatal(err)
		}
		for id, selected := range jobs {
			job, err := s.BillExport(WithModelUsageSource(ctx, global), admin.ID, id)
			if err != nil || job.UsageSource != selected {
				t.Fatalf("global setting changed a frozen export: %+v %v", job, err)
			}
		}
	}
}

func TestModifiedBillExportUsesFrozenSelectionAndSavedDates(t *testing.T) {
	s, owner, admin, vendor, version, now := billExportFixture(t)
	ctx := context.Background()
	for i, email := range []string{"selected@example.com", "foreign@example.com"} {
		id := []uint64{owner.ID, admin.ID}[i]
		syncSubmission(t, s, id, vendor.ID, version.ID, email, "claude/cc-pro", "accepted", fmt.Sprint(100+i), now)
	}
	job, err := s.CreateBillExport(ctx, admin.ID, owner.ID, "2026-11-10", "2026-11-11", now)
	if err != nil {
		t.Fatal(err)
	}
	var accounts []SyncedAccount
	if err = s.DB.Order("id ASC").Find(&accounts).Error; err != nil || len(accounts) != 2 {
		t.Fatal("fixture account count", len(accounts), err)
	}
	for _, day := range []AccountUsageDay{
		{AccountID: accounts[0].ID, IdentityKey: accounts[0].IdentityKey, UsageDate: "2026-11-10", LastSuccessAt: &now},
		{AccountID: accounts[0].ID, IdentityKey: "prior-lifetime", UsageDate: "2026-11-10", LastSuccessAt: &now},
		{AccountID: accounts[0].ID, IdentityKey: accounts[0].IdentityKey, UsageDate: "2026-11-09", LastSuccessAt: &now},
		{AccountID: accounts[1].ID, IdentityKey: accounts[1].IdentityKey, UsageDate: "2026-11-10", LastSuccessAt: &now},
		{AccountID: accounts[0].ID, IdentityKey: accounts[0].IdentityKey, UsageDate: "2026-11-11"},
	} {
		if err = s.DB.Create(&day).Error; err != nil {
			t.Fatal(err)
		}
		raw := AccountDailyUsage{DayID: day.ID, Model: "raw-only", CostCNY: "840", InputTokens: "940", OutputTokens: "2", CacheReadTokens: "3", CacheWriteTokens: "4"}
		if err = s.DB.Create(&raw).Error; err != nil {
			t.Fatal(err)
		}
		u := AccountDailyUsageModified{DayID: day.ID, Model: "stored", CostCNY: "7.125", InputTokens: "18446744073709551616", OutputTokens: "211", CacheReadTokens: "31", CacheWriteTokens: "41"}
		if err = s.DB.Create(&u).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err = s.DB.Model(&accounts[0]).Updates(map[string]any{"identity_key": "subsequent-identity", "user_id": admin.ID}).Error; err != nil {
		t.Fatal(err)
	}
	rows, err := s.ModifiedBillExportRows(ctx, job.ID)
	if err != nil || len(rows) != 1 || rows[0].LocalAccountID != idString(accounts[0].ID) || rows[0].UsageDate != "2026-11-10" || rows[0].Model != "stored" || rows[0].CostCNY != "7.125" || rows[0].InputTokens != "18446744073709551616" {
		t.Fatal("modified export scope/currency/counter", rows, err)
	}
}
