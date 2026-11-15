package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAccountsOverviewScopeAggregationCoverageAndRate(t *testing.T) {
	s, owner, v, p := syncFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, _ := s.DB.DB()
	db.SetMaxOpenConns(1) // regression: no nested transactions/connection deadlocks
	b := hierarchyChild(t, s, owner, "overview-b@example.com")
	c := hierarchyChild(t, s, owner, "overview-c@example.com")
	d := hierarchyChild(t, s, b, "hidden-descendant@example.com")
	other := User{Email: "outside-overview@example.com", PasswordHash: "fake", Role: "member", EmailVerifiedAt: time.Now()}
	if err := s.CreateUser(ctx, &other); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	for i, u := range []User{owner, b, c, d, other} {
		syncSubmission(t, s, u.ID, v.ID, p.ID, "account-"+idString(u.ID)+"@example.com", "claude/cc-pro", "accepted", idString(u.ID+100), now.AddDate(0, 0, -130).Add(time.Duration(i)*time.Second))
		var a SyncedAccount
		if err := s.DB.Where("user_id = ?", u.ID).First(&a).Error; err != nil {
			t.Fatal(err)
		}
		if err := s.DB.Model(&a).Updates(map[string]any{"created_at": now.AddDate(0, 0, -130), "snapshot_status": "active", "last_success_at": now, "last_error": "collection failed later"}).Error; err != nil {
			t.Fatal(err)
		}
		status, currency := "active", "CNY"
		cost := 72.5
		for n := 0; n < 2; n++ {
			at := now.Add(time.Duration(n-1) * time.Minute)
			snap := AccountSnapshot{AccountID: a.ID, IdentityKey: a.IdentityKey, SnapshotDate: "2026-11-16", CollectedAt: at, DataSource: "website", AccountSnapshotValues: AccountSnapshotValues{AccountStatus: &status, CostTotal: &cost, Currency: &currency}}
			if err := s.DB.Create(&snap).Error; err != nil {
				t.Fatal(err)
			}
		}
		for _, date := range []string{"2026-07-10", "2026-11-14", "2026-11-15"} {
			if u.ID == c.ID && date == "2026-11-14" {
				continue
			} // one account missing, not zero
			day := AccountUsageDay{AccountID: a.ID, IdentityKey: a.IdentityKey, UsageDate: date, LastSuccessAt: &now, LastAttemptAt: now}
			if err := s.DB.Create(&day).Error; err != nil {
				t.Fatal(err)
			}
			if date == "2026-11-15" {
				continue
			} // confirmed zero bill day
			for _, model := range []string{"claude-opus", "claude-sonnet"} {
				row := AccountDailyUsage{DayID: day.ID, Model: model, InputTokens: "9007199254740993", OutputTokens: "20", CacheReadTokens: "0", CacheWriteTokens: "0", CostCNY: "72.5"}
				if err := s.DB.Create(&row).Error; err != nil {
					t.Fatal(err)
				}
			}
		}
		oldDay := AccountUsageDay{AccountID: a.ID, IdentityKey: strings.Repeat("z", 64), UsageDate: "2025-01-01", LastSuccessAt: &now, LastAttemptAt: now}
		if err := s.DB.Create(&oldDay).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PatchSettings(ctx, map[string]string{"exchange_rate_cny_per_usd": "7.25"}, owner.ID); err != nil {
		t.Fatal(err)
	}
	out, err := s.AccountsOverview(ctx, owner.ID, "all", "", "", false, now)
	if err != nil {
		t.Fatal(err)
	}
	if out.StartDate != "2026-11-09" || out.EndDate != "2026-11-15" || len(out.Sources) != 3 || out.ExchangeRate != 7.25 {
		t.Fatalf("bad defaults %+v", out)
	}
	var total int64
	var cost float64
	for _, row := range out.Sources {
		total += row.Total
		cost += *row.CostUSD
		if row.Statuses["active"] != row.Total {
			t.Fatal("collection failure changed status")
		}
		if row.ID == idString(b.ID) && row.Total != 2 {
			t.Fatal("descendants not grouped under direct child")
		}
	}
	if total != 4 || cost != 40 {
		t.Fatal("snapshot or bill joins inflated totals", total, cost)
	}
	if len(out.Rows) != 2 || out.Rows[0].CostUSD != 30 || out.Rows[0].InputTokens != "27021597764222979" {
		t.Fatalf("aggregation/precision wrong: %+v", out.Rows)
	}
	if out.Days[5].Status != "partial" || out.Days[5].Collected != 3 || out.Days[6].Status != "complete" || out.Days[6].Collected != 4 || out.Days[0].Status != "missing" {
		t.Fatalf("bad coverage %+v", out.Days)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), d.Email) || strings.Contains(string(raw), other.Email) || strings.Contains(string(raw), "identity_key") {
		t.Fatal("private data exposed")
	}
	all, err := s.AccountsOverview(ctx, owner.ID, "myself", "", "", true, now)
	if err != nil || all.StartDate != "2026-07-10" || len(all.Days) < 90 || len(all.Sources) != 1 {
		t.Fatal("all time truncated or leaked old identity", all.StartDate, err)
	}
	for _, bad := range []string{idString(d.ID), idString(other.ID)} {
		if _, err = s.AccountsOverview(ctx, owner.ID, bad, "", "", false, now); err == nil {
			t.Fatal("invalid source accepted")
		}
	}
	none, err := s.AccountsOverview(ctx, owner.ID, "none", "", "", true, now)
	if err != nil || len(none.Sources) != 0 || len(none.Rows) != 0 || none.EarliestDate != nil {
		t.Fatal("empty selection", none, err)
	}
	if err = s.DB.Model(&SyncedAccount{}).Where("user_id = ?", d.ID).Update("deleted_at", now).Error; err != nil {
		t.Fatal(err)
	}
	branch, err := s.AccountsOverview(ctx, owner.ID, idString(b.ID), "", "", false, now)
	if err != nil || len(branch.Sources) != 1 || branch.Sources[0].Total != 1 {
		t.Fatal("deleted account counted", branch, err)
	}
}

func TestOverviewRangeValidation(t *testing.T) {
	now := time.Date(2026, 11, 16, 0, 0, 0, 0, dailyZone)
	for _, dates := range [][2]string{{"2026-01-01", "2026-11-15"}, {"2026-11-15", "2026-11-15"}} {
		if err := ValidateOverviewRange(dates[0], dates[1], now); err != nil {
			t.Fatal(err)
		}
	}
	for _, dates := range [][2]string{{"2026-11-16", "2026-11-16"}, {"2026-11-15", "2026-11-14"}, {"2026-02-30", "2026-11-15"}, {"0001-01-01", "2026-11-15"}} {
		if err := ValidateOverviewRange(dates[0], dates[1], now); err == nil {
			t.Fatal("bad dates accepted", dates)
		}
	}
}
