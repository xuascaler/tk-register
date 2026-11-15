package core

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

func TestAdminAccountsOverviewDirectOwnersAndPersonalSubtrees(t *testing.T) {
	s, a, vendor, version := syncFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, _ := s.DB.DB()
	db.SetMaxOpenConns(1)
	if err := s.DB.Model(&a).Update("role", "admin").Error; err != nil {
		t.Fatal(err)
	}
	b := hierarchyChild(t, s, a, "direct-b@example.com")
	c := hierarchyChild(t, s, b, "nested-c@example.com")
	other := User{Email: "other-admin@example.com", Role: "admin", PasswordHash: "secret-test-hash", EmailVerifiedAt: time.Now()}
	empty := User{Email: "empty-user@example.com", Role: "member", PasswordHash: "secret-test-hash", EmailVerifiedAt: time.Now()}
	for _, user := range []*User{&other, &empty} {
		if err := s.CreateUser(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	statuses := []string{"active", "pending", "banned", "login_failed"}
	accounts := []SyncedAccount{}
	for i, user := range []User{a, b, c, other} {
		email := "overview-account-" + idString(user.ID) + "@example.com"
		syncSubmission(t, s, user.ID, vendor.ID, version.ID, email, "claude/cc-pro", "accepted", idString(user.ID+100), now.AddDate(0, 0, -150).Add(time.Duration(i)*time.Second))
		var account SyncedAccount
		if err := s.DB.Where("user_id = ?", user.ID).First(&account).Error; err != nil {
			t.Fatal(err)
		}
		if err := s.DB.Model(&account).Updates(map[string]any{"created_at": now.AddDate(0, 0, -150), "snapshot_status": statuses[i], "last_success_at": now}).Error; err != nil {
			t.Fatal(err)
		}
		accounts = append(accounts, account)
		currency, cost := "CNY", 72.5*float64(i+1)
		// Two snapshots must contribute only the latest cost once.
		for n := 0; n < 2; n++ {
			snapshot := AccountSnapshot{AccountID: account.ID, IdentityKey: account.IdentityKey, SnapshotDate: "2026-11-16", CollectedAt: now.Add(time.Duration(n-1) * time.Minute), DataSource: "website", AccountSnapshotValues: AccountSnapshotValues{AccountStatus: &statuses[i], Currency: &currency, CostTotal: &cost}}
			if err := s.DB.Create(&snapshot).Error; err != nil {
				t.Fatal(err)
			}
		}
		day := AccountUsageDay{AccountID: account.ID, IdentityKey: account.IdentityKey, UsageDate: "2026-11-14", LastSuccessAt: &now, LastAttemptAt: now}
		if err := s.DB.Create(&day).Error; err != nil {
			t.Fatal(err)
		}
		for _, model := range []string{"claude-opus", "claude-sonnet"} {
			row := AccountDailyUsage{DayID: day.ID, Model: model, CostCNY: "72.5", InputTokens: "100", OutputTokens: "20", CacheReadTokens: "5", CacheWriteTokens: "2"}
			if err := s.DB.Create(&row).Error; err != nil {
				t.Fatal(err)
			}
		}
		missing := AccountUsageDay{AccountID: account.ID, IdentityKey: account.IdentityKey, UsageDate: "2026-11-13", LastAttemptAt: now, LastError: "upstream unavailable"}
		if err := s.DB.Create(&missing).Error; err != nil {
			t.Fatal(err)
		}
	}
	// Successful zero day, including a later failed refresh, remains available.
	for _, date := range []string{"2026-07-10", "2026-11-15"} {
		day := AccountUsageDay{AccountID: accounts[2].ID, IdentityKey: accounts[2].IdentityKey, UsageDate: date, LastSuccessAt: &now, LastAttemptAt: now, LastError: "later refresh failed"}
		if err := s.DB.Create(&day).Error; err != nil {
			t.Fatal(err)
		}
	}
	oldIdentityDay := AccountUsageDay{AccountID: accounts[0].ID, IdentityKey: strings.Repeat("z", 64), UsageDate: "2025-01-01", LastSuccessAt: &now, LastAttemptAt: now}
	if err := s.DB.Create(&oldIdentityDay).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.PatchSettings(ctx, map[string]string{"exchange_rate_cny_per_usd": "7.25"}, a.ID); err != nil {
		t.Fatal(err)
	}
	out, err := s.AdminAccountsOverview(ctx, a.ID, "all", "", "", false, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Sources) != 5 || out.Sources[0].ID != "myself" || out.StartDate != "2026-11-09" || out.EndDate != "2026-11-15" || out.ExchangeRate != 7.25 {
		t.Fatalf("bad admin defaults: %+v", out)
	}
	bySource := map[string]OverviewSource{}
	var total int64
	var totalCost float64
	for _, source := range out.Sources {
		bySource[source.ID] = source
		total += source.Total
		if source.CostUSD != nil {
			totalCost += *source.CostUSD
		}
	}
	if total != 4 || totalCost != 100 {
		t.Fatalf("admin accounts or snapshot costs duplicated: %d %.2f", total, totalCost)
	}
	for i, id := range []string{"myself", idString(b.ID), idString(c.ID), idString(other.ID)} {
		source := bySource[id]
		if source.Total != 1 || source.Statuses[statuses[i]] != 1 || source.CostKnown != 1 || source.CostUSD == nil || *source.CostUSD != float64((i+1)*10) {
			t.Fatalf("source %s includes another owner's data: %+v", id, source)
		}
	}
	if source := bySource[idString(empty.ID)]; source.Total != 0 || source.CostUSD != nil || source.CostKnown != 0 {
		t.Fatalf("empty user invented usage: %+v", source)
	}
	if len(out.Rows) != 2 || len(out.ModelCosts) != 8 {
		t.Fatalf("model aggregation mismatch: %+v %+v", out.Rows, out.ModelCosts)
	}
	for _, row := range out.Rows {
		if row.CostUSD != 40 || row.InputTokens != "400" || row.OutputTokens != "80" || row.CacheReadTokens != "20" || row.CacheWriteTokens != "8" {
			t.Fatalf("daily usage duplicated or omitted: %+v", row)
		}
	}
	for _, cost := range out.ModelCosts {
		if cost.CostUSD != 10 {
			t.Fatalf("owner model cost duplicated: %+v", cost)
		}
	}
	if out.Days[4].Status != "missing" || out.Days[4].Collected != 0 || out.Days[5].Collected != 4 || out.Days[5].Expected != 4 || out.Days[6].Collected != 1 || out.Days[6].Status != "complete" {
		t.Fatalf("missing and successful zero days conflated: %+v", out.Days)
	}
	if bySource[idString(c.ID)].CollectedDays != 2 || out.LatestAccountData == nil || !out.LatestAccountData.Equal(now) {
		t.Fatal("incorrect collection metadata", out)
	}
	personal, err := s.AccountsOverview(ctx, a.ID, "all", "", "", false, now)
	if err != nil || len(personal.Sources) != 2 || personal.Sources[1].ID != idString(b.ID) || personal.Sources[1].Total != 2 || *personal.Sources[1].CostUSD != 50 || len(personal.Rows) != 2 || personal.Rows[0].CostUSD != 30 {
		t.Fatalf("personal subtree behavior changed: %+v %v", personal, err)
	}
	for _, tc := range []struct {
		sources string
		count   int
		total   int64
		cost    float64
	}{{idString(b.ID), 1, 1, 20}, {idString(b.ID) + "," + idString(c.ID), 2, 2, 50}, {"myself," + idString(a.ID) + ",myself", 1, 1, 10}, {idString(empty.ID), 1, 0, 0}, {"none", 0, 0, 0}} {
		filtered, filterErr := s.AdminAccountsOverview(ctx, a.ID, tc.sources, "", "", false, now)
		var count int64
		var cost float64
		for _, source := range filtered.Sources {
			count += source.Total
			if source.CostUSD != nil {
				cost += *source.CostUSD
			}
		}
		if filterErr != nil || len(filtered.Sources) != tc.count || count != tc.total || math.Abs(cost-tc.cost) > 0.00001 {
			t.Fatalf("filter %q failed: %+v %v", tc.sources, filtered, filterErr)
		}
	}
	allTime, err := s.AdminAccountsOverview(ctx, a.ID, "all", "", "", true, now)
	if err != nil || allTime.StartDate != "2026-07-10" || len(allTime.Days) < 90 {
		t.Fatal("all time changed or old identity included", allTime.StartDate, err)
	}
	for _, bad := range []string{"99999999", "0", "-1", "01", "myself,all", "none,myself", " ", strings.Repeat("1", 8193)} {
		if _, err := s.AdminAccountsOverview(ctx, a.ID, bad, "", "", false, now); err == nil {
			t.Fatalf("invalid admin filter accepted: %q", bad)
		}
	}
	if err := s.DB.Model(&accounts[2]).Update("deleted_at", now).Error; err != nil {
		t.Fatal(err)
	}
	deleted, err := s.AdminAccountsOverview(ctx, a.ID, idString(c.ID), "", "", true, now)
	if err != nil || len(deleted.Sources) != 1 || deleted.Sources[0].Total != 0 || deleted.Sources[0].CostUSD != nil || deleted.EarliestDate != nil || len(deleted.Rows) != 0 || len(deleted.ModelCosts) != 0 {
		t.Fatal("deleted account included", deleted, err)
	}
	raw, _ := json.Marshal(out)
	for _, secret := range []string{"secret-test-hash", "password", "identity_key", "parent_user_id"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("private field leaked: %s", secret)
		}
	}
}
