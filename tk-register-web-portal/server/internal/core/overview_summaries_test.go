package core

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

// One account per owner makes any recursive double counting visible.
func overviewSummaryFixture(t *testing.T) (*Store, []User, []SyncedAccount, time.Time, uint64) {
	t.Helper()
	s, a, v, p := syncFixture(t)
	ctx := context.Background()
	if err := s.DB.Model(&a).Updates(map[string]any{"role": "admin", "is_super_admin": true}).Error; err != nil {
		t.Fatal(err)
	}
	b := hierarchyChild(t, s, a, "snapshot-b@example.com")
	c := hierarchyChild(t, s, b, "snapshot-c@example.com")
	outsider := User{Email: "snapshot-other@example.com", Role: "admin", PasswordHash: "fixture", EmailVerifiedAt: time.Now()}
	if err := s.CreateUser(ctx, &outsider); err != nil {
		t.Fatal(err)
	}
	empty := User{Email: "snapshot-empty@example.com", Role: "member", PasswordHash: "fixture", EmailVerifiedAt: time.Now()}
	if err := s.CreateUser(ctx, &empty); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 16, 4, 0, 0, 0, dailyZone)
	users := []User{a, b, c, outsider, empty}
	accounts := []SyncedAccount{}
	statuses := []string{"active", "pending", "sync_failed", "banned"}
	for i, u := range users[:4] {
		email := "summary-account-" + idString(u.ID) + "@example.com"
		syncSubmission(t, s, u.ID, v.ID, p.ID, email, "claude/cc-pro", "accepted", idString(u.ID+100), now.AddDate(0, 0, -60).Add(time.Duration(i)*time.Second))
		var account SyncedAccount
		if err := s.DB.Where("account = ?", email).First(&account).Error; err != nil {
			t.Fatal(err)
		}
		if err := s.DB.Model(&account).Updates(map[string]any{"snapshot_status": statuses[i], "created_at": now.AddDate(0, 0, -60), "last_success_at": now}).Error; err != nil {
			t.Fatal(err)
		}
		currency, cost := "CNY", float64(i+1)*72
		for n := 0; n < 2; n++ {
			snap := AccountSnapshot{AccountID: account.ID, IdentityKey: account.IdentityKey, SnapshotDate: "2026-11-16", CollectedAt: now.Add(time.Duration(n-1) * time.Minute), DataSource: "website", AccountSnapshotValues: AccountSnapshotValues{AccountStatus: &statuses[i], CostTotal: &cost, Currency: &currency}}
			if err := s.DB.Create(&snap).Error; err != nil {
				t.Fatal(err)
			}
		}
		for _, date := range []string{"2026-10-01", "2026-11-09", "2026-11-14", "2026-11-15"} {
			day := AccountUsageDay{AccountID: account.ID, IdentityKey: account.IdentityKey, UsageDate: date, LastSuccessAt: &now, LastAttemptAt: now}
			if err := s.DB.Create(&day).Error; err != nil {
				t.Fatal(err)
			}
			if date == "2026-11-15" {
				continue
			} // confirmed zero still counts as successful
			for _, model := range []string{"claude-opus", "claude-sonnet"} {
				usage := AccountDailyUsage{DayID: day.ID, Model: model, CostCNY: "72", InputTokens: "9007199254740993", OutputTokens: "20", CacheReadTokens: "5", CacheWriteTokens: "2"}
				if err := s.DB.Create(&usage).Error; err != nil {
					t.Fatal(err)
				}
			}
		}
		old := AccountUsageDay{AccountID: account.ID, IdentityKey: strings.Repeat("z", 64), UsageDate: "2025-01-01", LastSuccessAt: &now, LastAttemptAt: now}
		if err := s.DB.Create(&old).Error; err != nil {
			t.Fatal(err)
		}
		accounts = append(accounts, account)
	}
	if err := s.PatchSettings(ctx, map[string]string{"exchange_rate_cny_per_usd": "7.2"}, a.ID); err != nil {
		t.Fatal(err)
	}
	return s, users, accounts, now, v.ID
}

func readSummary(t *testing.T, s *Store, id uint64, admin bool, period string, now time.Time) AccountsOverview {
	t.Helper()
	out, err := s.ReadOverviewSummary(context.Background(), id, admin, period, now)
	if err != nil {
		t.Fatal(err)
	}
	if out.SummaryAvailable == nil || !*out.SummaryAvailable || out.SummaryPeriod != period {
		t.Fatalf("summary not available: %+v", out)
	}
	return out
}
func summarySource(t *testing.T, out AccountsOverview, id string) OverviewSource {
	t.Helper()
	for _, source := range out.Sources {
		if source.ID == id {
			return source
		}
	}
	t.Fatalf("source %q missing from %+v", id, out.Sources)
	return OverviewSource{}
}

func TestOverviewSummariesPeriodsDirectOwnersAndPersonalScope(t *testing.T) {
	s, users, _, now, _ := overviewSummaryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// MaxOpenConns(1) detects accidental queries through s.DB inside tx.
	db, _ := s.DB.DB()
	db.SetMaxOpenConns(1)
	for _, admin := range []bool{false, true} {
		if err := s.BuildOverviewSummaries(ctx, users[0].ID, admin, "2026-11-15", "batch-one", now); err != nil {
			t.Fatal(err)
		}
	}
	var saved []OverviewSummary
	if err := s.DB.Order("view_type, period").Find(&saved).Error; err != nil || len(saved) != 8 {
		t.Fatalf("four rows per view: %d %v", len(saved), err)
	}
	for _, row := range saved {
		if row.BatchID != "batch-one" || row.EndDate != "2026-11-15" || !row.AccountsAsOf.Equal(now) || row.GeneratedAt.Before(now) {
			t.Fatalf("wrong batch metadata: %+v", row)
		}
		if strings.Contains(row.PayloadJSON, "cost_usd") || strings.Contains(row.PayloadJSON, "exchange_rate") {
			t.Fatal("converted money persisted")
		}
	}
	expectedStarts := map[string]string{"day": "2026-11-15", "week": "2026-11-09", "month": "2026-10-17", "all_time": "2026-10-01"}
	for period, start := range expectedStarts {
		out := readSummary(t, s, users[0].ID, true, period, now)
		if out.StartDate != start || out.EndDate != "2026-11-15" || out.EarliestDate == nil || *out.EarliestDate != "2026-10-01" {
			t.Fatalf("incorrect period dates: %+v", out)
		}
		var total int64
		var cost float64
		for _, source := range out.Sources {
			total += source.Total
			if source.CostUSD != nil {
				cost += *source.CostUSD
			}
		}
		if len(out.Sources) != 5 || total != 4 || cost != 100 {
			t.Fatalf("counts/cumulative cost depend on period: %+v", out.Sources)
		}
		for i, id := range []string{"myself", idString(users[1].ID), idString(users[2].ID), idString(users[3].ID)} {
			source := summarySource(t, out, id)
			if source.Total != 1 || source.CostKnown != 1 || source.CostUSD == nil || *source.CostUSD != float64((i+1)*10) {
				t.Fatalf("descendant counted twice: %+v", source)
			}
		}
		empty := summarySource(t, out, idString(users[4].ID))
		if empty.Total != 0 || empty.CostUSD != nil || empty.Statuses == nil {
			t.Fatalf("empty user invented values: %+v", empty)
		}
	}
	admin := readSummary(t, s, users[0].ID, true, "week", now)
	if len(admin.Rows) != 4 || admin.Rows[0].InputTokens != "36028797018963972" || admin.Rows[0].CostCNY != "288" || admin.Rows[0].CostUSD != 40 {
		t.Fatalf("exact totals incorrect: %+v", admin.Rows)
	}
	for _, cost := range admin.ModelCosts {
		if cost.CostCNY != "144" || cost.CostUSD != 20 {
			t.Fatalf("owner model totals duplicated: %+v", cost)
		}
	}
	if len(admin.Days) != 7 || admin.Days[0].Status != "complete" || admin.Days[1].Status != "missing" || admin.Days[6].Status != "complete" || admin.Days[6].Collected != 4 {
		t.Fatalf("zero and missing conflated: %+v", admin.Days)
	}
	day := readSummary(t, s, users[0].ID, true, "day", now)
	if len(day.Rows) != 0 || len(day.ModelCosts) != 0 || day.Days[0].Status != "complete" {
		t.Fatalf("zero day lost: %+v", day)
	}
	personal := readSummary(t, s, users[0].ID, false, "week", now)
	branch := summarySource(t, personal, idString(users[1].ID))
	if len(personal.Sources) != 2 || branch.Total != 2 || branch.Statuses["pending"] != 1 || branch.Statuses["sync_failed"] != 1 || branch.CostUSD == nil || *branch.CostUSD != 50 || personal.Rows[0].InputTokens != "27021597764222979" {
		t.Fatalf("personal recursive semantics lost: %+v", personal)
	}
	raw, _ := json.Marshal(personal)
	if strings.Contains(string(raw), users[2].Email) || strings.Contains(string(raw), users[3].Email) || strings.Contains(string(raw), "identity_key") {
		t.Fatal("personal summary leaked deeper owner data")
	}
}

func TestOverviewSummariesFreezeOwnershipStateAndReadWithCurrentRate(t *testing.T) {
	s, users, accounts, now, _ := overviewSummaryFixture(t)
	ctx := context.Background()
	if err := s.BuildOverviewSummaries(ctx, users[0].ID, true, "2026-11-15", "original", now); err != nil {
		t.Fatal(err)
	}
	before := readSummary(t, s, users[0].ID, true, "week", now)
	if err := s.DB.Model(&accounts[0]).Update("snapshot_status", "banned").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Model(&accounts[1]).Update("user_id", users[2].ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Model(&accounts[3]).Update("deleted_at", now).Error; err != nil {
		t.Fatal(err)
	}
	// The next day's requests retain the original interval, owner rows and state.
	frozen := readSummary(t, s, users[0].ID, true, "week", now.AddDate(0, 0, 1))
	if !reflect.DeepEqual(before, frozen) {
		t.Fatalf("request changed frozen data: before=%+v after=%+v", before, frozen)
	}
	if err := s.PatchSettings(ctx, map[string]string{"exchange_rate_cny_per_usd": "8"}, users[0].ID); err != nil {
		t.Fatal(err)
	}
	after := readSummary(t, s, users[0].ID, true, "week", now)
	if after.ExchangeRate != 8 || *summarySource(t, after, "myself").CostUSD != 9 || after.Rows[0].CostUSD != 36 || after.ModelCosts[0].CostUSD != 18 {
		t.Fatalf("latest rate not applied to every money value: %+v", after)
	}
	if after.Rows[0].CostCNY != before.Rows[0].CostCNY || after.Rows[0].InputTokens != before.Rows[0].InputTokens {
		t.Fatal("rate update changed originals")
	}
	if err := s.BuildOverviewSummaries(ctx, users[0].ID, true, "2026-11-16", "next-day", now.AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}
	rebuilt := readSummary(t, s, users[0].ID, true, "week", now.AddDate(0, 0, 1))
	if summarySource(t, rebuilt, "myself").Statuses["banned"] != 1 || summarySource(t, rebuilt, idString(users[1].ID)).Total != 0 || summarySource(t, rebuilt, idString(users[2].ID)).Total != 2 || summarySource(t, rebuilt, idString(users[3].ID)).Total != 0 {
		t.Fatalf("next batch did not adopt changes: %+v", rebuilt.Sources)
	}
	if err := s.BuildOverviewSummaries(ctx, users[0].ID, true, "2026-11-15", "stale-day", now.AddDate(0, 0, 1)); err == nil {
		t.Fatal("older day replaced new summary")
	}
}

func TestOverviewSummaryMissAndReadNeverScanAccountsOrBills(t *testing.T) {
	s, users, _, now, _ := overviewSummaryFixture(t)
	ctx := context.Background()
	if err := s.BuildOverviewSummaries(ctx, users[0].ID, false, "2026-11-15", "saved", now); err != nil {
		t.Fatal(err)
	}
	// Reject any query outside the summary/settings tables, including accidental
	// visibility resolution or scanning bill details on a cache miss.
	guard := func(db *gorm.DB) {
		sql := strings.ToLower(db.Statement.SQL.String())
		for _, table := range []string{"synced_accounts", "account_snapshots", "account_daily_usages", "account_usage_days", "user_hierarchy", "users"} {
			if db.Statement.Table == table || strings.Contains(sql, table) {
				db.AddError(errors.New("summary read scanned " + table))
				return
			}
		}
	}
	if err := s.DB.Callback().Query().Before("gorm:query").Register("summary-read-guard", guard); err != nil {
		t.Fatal(err)
	}
	defer s.DB.Callback().Query().Remove("summary-read-guard")
	if err := s.DB.Callback().Row().Before("gorm:row").Register("summary-read-guard", guard); err != nil {
		t.Fatal(err)
	}
	defer s.DB.Callback().Row().Remove("summary-read-guard")
	_ = readSummary(t, s, users[0].ID, false, "week", now)
	missing, err := s.ReadOverviewSummary(ctx, users[4].ID, false, "week", now)
	if err != nil || missing.SummaryAvailable == nil || *missing.SummaryAvailable || missing.SummaryGeneratedAt != nil || missing.StartDate != "2026-11-09" || missing.EndDate != "2026-11-15" || missing.ExchangeRate != 7.2 || missing.Sources == nil || missing.Rows == nil || missing.ModelCosts == nil || missing.Days == nil {
		t.Fatalf("bad missing response: %+v %v", missing, err)
	}
	var count int64
	if err := s.DB.Model(&OverviewSummary{}).Where("user_id = ?", users[4].ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("miss generated summary", count, err)
	}
	if _, err := s.ReadOverviewSummary(ctx, users[0].ID, false, "year", now); err == nil {
		t.Fatal("unknown period accepted")
	}
}

func TestOverviewSummariesAtomicFailureCancellationAndClaimFence(t *testing.T) {
	s, users, _, now, vendorID := overviewSummaryFixture(t)
	ctx := context.Background()
	id := users[0].ID
	if err := s.BuildOverviewSummaries(ctx, id, false, "2026-11-15", "good", now); err != nil {
		t.Fatal(err)
	}
	var original []OverviewSummary
	if err := s.DB.Order("period").Find(&original).Error; err != nil {
		t.Fatal(err)
	}
	assertUnchanged := func() {
		t.Helper()
		var current []OverviewSummary
		if err := s.DB.Order("period").Find(&current).Error; err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(original, current) {
			t.Fatal("failed build partially replaced previous four-row batch")
		}
	}
	if err := s.DB.Exec("CREATE TRIGGER reject_overview_month BEFORE INSERT ON overview_summaries WHEN NEW.period = 'month' AND NEW.batch_id = 'reject' BEGIN SELECT RAISE(ABORT, 'test publication failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.BuildOverviewSummaries(ctx, id, false, "2026-11-15", "reject", now); err == nil {
		t.Fatal("injected failure ignored")
	}
	assertUnchanged()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.BuildOverviewSummaries(cancelled, id, false, "2026-11-15", "cancelled", now); !errors.Is(err, context.Canceled) {
		t.Fatal("cancel not propagated", err)
	}
	assertUnchanged()
	lease := now.Add(time.Hour)
	job := DailyUsageJob{VendorID: vendorID, UsageDate: "2026-11-15", Status: "succeeded", NextRunAt: now, OverviewState: "running", OverviewRunToken: "current-token", OverviewLeaseUntil: &lease}
	if err := s.DB.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		state, token string
		until        time.Time
		status       string
	}{
		{"running", "stale-token", lease, "succeeded"},
		{"failed", "current-token", lease, "succeeded"},
		{"running", "current-token", now.Add(-time.Second), "succeeded"},
		{"running", "current-token", lease, "running"},
	} {
		if err := s.DB.Model(&job).Updates(map[string]any{"overview_state": tc.state, "overview_lease_until": tc.until, "status": tc.status}).Error; err != nil {
			t.Fatal(err)
		}
		if err := s.BuildOverviewSummaries(ctx, id, false, "2026-11-15", "stale-claim", now, OverviewSummaryClaim{job.ID, tc.token}); err == nil {
			t.Fatalf("bad claim published: %+v", tc)
		}
		assertUnchanged()
	}
	if err := s.DB.Model(&job).Updates(map[string]any{"overview_state": "running", "overview_lease_until": lease, "status": "succeeded"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.BuildOverviewSummaries(ctx, id, false, "2026-11-15", "claimed", now, OverviewSummaryClaim{job.ID, "current-token"}); err != nil {
		t.Fatal(err)
	}
	var current []OverviewSummary
	if err := s.DB.Find(&current).Error; err != nil || len(current) != 4 {
		t.Fatal("rebuild duplicated rows", len(current), err)
	}
	for _, row := range current {
		if row.BatchID != "claimed" {
			t.Fatal("valid claim did not replace all periods")
		}
	}
}

func TestOverviewSummariesJobDateCutoffAndExactStoredAmounts(t *testing.T) {
	s, users, accounts, now, _ := overviewSummaryFixture(t)
	// This job finished before the next 03:00 schedule: its end date must remain
	// Nov 14 even though calendar yesterday is already Nov 15.
	early := time.Date(2026, 11, 16, 1, 0, 0, 0, dailyZone)
	if err := s.BuildOverviewSummaries(context.Background(), users[0].ID, false, "2026-11-14", "recovered", early); err != nil {
		t.Fatal(err)
	}
	out := readSummary(t, s, users[0].ID, false, "day", now)
	if out.StartDate != "2026-11-14" || out.EndDate != "2026-11-14" || len(out.Days) != 1 || len(out.Rows) != 2 {
		t.Fatalf("job date replaced with current yesterday: %+v", out)
	}
	// A newer cumulative snapshot and precise bill fractions are kept as decimal
	// strings instead of persisting/reversing rounded USD totals.
	for i, a := range accounts[:3] {
		if err := s.DB.Model(&AccountSnapshot{}).Where("account_id = ?", a.ID).Update("cost_total", float64(i+1)/10).Error; err != nil {
			t.Fatal(err)
		}
		if err := s.DB.Model(&AccountDailyUsage{}).Where("day_id IN (?)", s.DB.Model(&AccountUsageDay{}).Select("id").Where("account_id = ? AND usage_date = ?", a.ID, "2026-11-14")).Update("cost_cny", "0.10000001").Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := s.BuildOverviewSummaries(context.Background(), users[0].ID, false, "2026-11-15", "exact", now); err != nil {
		t.Fatal(err)
	}
	var stored OverviewSummary
	if err := s.DB.Where("user_id = ? AND period = ?", users[0].ID, "week").First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	var raw overviewSummaryPayload
	if err := json.Unmarshal([]byte(stored.PayloadJSON), &raw); err != nil {
		t.Fatal(err)
	}
	for _, source := range raw.Sources {
		want := "0.1"
		if source.ID != "myself" {
			want = "0.5"
		}
		if source.CostCNY == nil || *source.CostCNY != want {
			t.Fatalf("cumulative original money lost: %+v", source)
		}
	}
	exact := readSummary(t, s, users[0].ID, false, "week", now)
	for _, row := range exact.Rows {
		if row.UsageDate == "2026-11-14" && (row.CostCNY != "0.30000003" || row.InputTokens != "27021597764222979" || math.Abs(row.CostUSD-0.30000003/7.2) > 1e-14) {
			t.Fatalf("precision lost: %+v", row)
		}
	}
}

func TestOverviewSummarySplitMigratesBothRolesAndPreservesOriginalBills(t *testing.T) {
	s, users, accounts, now, _ := overviewSummaryFixture(t)
	ctx := context.Background()
	var day AccountUsageDay
	if err := s.DB.Where("account_id = ? AND usage_date = ?", accounts[0].ID, "2026-11-14").First(&day).Error; err != nil {
		t.Fatal(err)
	}
	modified := AccountDailyUsageModified{DayID: day.ID, Model: "mirror-only", CostCNY: "36", InputTokens: "11", OutputTokens: "2", CacheReadTokens: "3", CacheWriteTokens: "4"}
	if err := s.DB.Create(&modified).Error; err != nil {
		t.Fatal(err)
	}
	// The old cache counted this day in modified.collections even when the bill had no lines.
	var emptyDay AccountUsageDay
	if err := s.DB.Where("account_id = ? AND usage_date = ?", accounts[0].ID, "2026-11-15").First(&emptyDay).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Create(&AccountDailyFableRatio{DayID: emptyDay.ID, FableRatio: "0"}).Error; err != nil {
		t.Fatal(err)
	}
	type oldCache struct {
		User User
		Data string
	}
	for _, cache := range []oldCache{
		{users[0], `{"version":1,"rows":[],"modified":{"rows":[],"collections":[{"Date":"2026-11-15","Count":1}]}}`},
		{users[1], `{"version":1,"rows":[],"model_costs":[]}`},
	} {
		row := OverviewSummary{UserID: cache.User.ID, ViewType: "personal", Period: "all_time", OverviewSummaryData: OverviewSummaryData{
			StartDate: "2026-10-01", EndDate: "2026-11-15", Timezone: "Asia/Shanghai", BatchID: "legacy-mixed", PayloadJSON: cache.Data, AccountsAsOf: now, GeneratedAt: now,
		}}
		if err := s.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := s.migrateOverviewSummaries(); err != nil {
		t.Fatal(err)
	}
	var raw []OverviewSummary
	if err := s.DB.Find(&raw).Error; err != nil || len(raw) != 1 || raw[0].UserID != users[0].ID || strings.Contains(raw[0].PayloadJSON, `"modified"`) || raw[0].BatchID != "legacy-mixed" {
		t.Fatal("raw cache migration", raw, err)
	}
	var first []OverviewSummaryModified
	if err := s.DB.Order("id").Find(&first).Error; err != nil || len(first) != 8 {
		t.Fatal("did not initialize all modified periods", len(first), err)
	}
	view := WithModelUsageSource(ctx, "modified")
	out, err := s.ReadOverviewSummary(view, users[0].ID, false, "all_time", now)
	if err != nil || out.TotalCostUSD == nil || *out.TotalCostUSD != 5 || out.StartDate != "2026-11-14" || out.TotalCostDays != 1 || len(out.Rows) != 1 || out.Rows[0].Model != "mirror-only" || out.Days[1].Collected != 0 {
		t.Fatal("migration used a raw amount, raw date, or empty ratio marker", out, err)
	}
	for _, source := range out.Sources {
		if source.CostUSD != nil || source.CostKnown != 0 {
			t.Fatal("raw account snapshot entered the modified payload", source)
		}
	}
	if err = s.migrateOverviewSummaries(); err != nil {
		t.Fatal(err)
	}
	var second []OverviewSummaryModified
	if err = s.DB.Order("id").Find(&second).Error; err != nil || !reflect.DeepEqual(first, second) {
		t.Fatal("second start rewrote frozen summaries", err)
	}
	var materialized int64
	if err = s.DB.Model(&AccountDailyUsageModified{}).Count(&materialized).Error; err != nil || materialized != 1 {
		t.Fatal("migration backfilled past bills", materialized, err)
	}

	if err = s.DB.Model(&User{}).Where("id = ?", users[0].ID).Update("is_super_admin", false).Error; err != nil {
		t.Fatal(err)
	}
	out, err = s.ReadOverviewSummary(WithModelUsageSource(ctx, "raw"), users[0].ID, false, "all_time", now)
	if err != nil || out.SummaryAvailable == nil || *out.SummaryAvailable {
		t.Fatal("demoted viewer read its stale raw cache", out, err)
	}
}
