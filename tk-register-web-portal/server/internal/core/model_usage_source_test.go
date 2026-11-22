package core

import (
	"context"
	"testing"
)

func TestMirrorReadsUseOnlySelectedLedger(t *testing.T) {
	store, users, accounts, now, _ := overviewSummaryFixture(t)
	raw := WithModelUsageSource(context.Background(), "raw")
	modified := WithModelUsageSource(context.Background(), "modified")
	var day AccountUsageDay
	if err := store.DB.Where("account_id=? AND identity_key=? AND usage_date=?", accounts[0].ID, accounts[0].IdentityKey, "2026-11-14").First(&day).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.DB.Create(&AccountDailyFableRatio{DayID: day.ID, FableRatio: "0"}).Error; err != nil {
		t.Fatal(err)
	}
	for model, cost := range map[string]string{"claude-opus": "36", "claude-sonnet": "108"} {
		row := AccountDailyUsageModified{DayID: day.ID, Model: model, CostCNY: cost, InputTokens: "18446744073709551616", OutputTokens: "20", CacheReadTokens: "5", CacheWriteTokens: "2"}
		if err := store.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	rawDay, err := store.AccountDailyUsage(raw, accounts[0].ID, "2026-11-09", "2026-11-15")
	if err != nil {
		t.Fatal(err)
	}
	changed, err := store.AccountDailyUsage(modified, accounts[0].ID, "2026-11-09", "2026-11-15")
	if err != nil {
		t.Fatal(err)
	}
	if len(rawDay.Rows) != 4 || len(changed.Rows) != 2 || changed.Rows[0].CostUSD != 5 || changed.Rows[0].InputTokens != "18446744073709551616" || changed.Days[0].CollectedAt != nil || changed.Days[5].CollectedAt == nil {
		t.Fatalf("wrong account detail %+v", changed)
	}
	if rawDay.TotalCostUSD == nil || changed.TotalCostUSD == nil || *rawDay.TotalCostUSD != 40 || *changed.TotalCostUSD != 20 || changed.DailyCosts[0].CostUSD != nil || changed.TotalCostDays != 1 {
		t.Fatal("historical raw bill leaked into modified totals")
	}
	if err = store.BuildOverviewSummaries(raw, users[0].ID, true, "2026-11-15", "test", now); err != nil {
		t.Fatal(err)
	}
	for _, cached := range []bool{false, true} {
		read := func(ctx context.Context) AccountsOverview {
			var out AccountsOverview
			var err error
			if cached {
				out, err = store.ReadOverviewSummary(ctx, users[0].ID, true, "week", now)
			} else {
				out, err = store.AdminAccountsOverview(ctx, users[0].ID, "all", "2026-11-09", "2026-11-15", false, now)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = ProjectOverviewBilling(&out); err != nil {
				t.Fatal(err)
			}
			return out
		}
		orig, mirror := read(raw), read(modified)
		if orig.TotalCostUSD == nil || mirror.TotalCostUSD == nil || *orig.TotalCostUSD != 160 || *mirror.TotalCostUSD != 20 || len(mirror.Rows) != 2 || mirror.Rows[0].InputTokens != "18446744073709551616" || len(orig.Rows) != 4 {
			t.Fatalf("incorrect overview cached=%v raw=%+v mirror=%+v", cached, orig, mirror)
		}
		for _, source := range mirror.Sources {
			if source.ID == "myself" {
				if source.CostUSD == nil || *source.CostUSD != 20 {
					t.Fatalf("wrong modified source total: %+v", source)
				}
			} else if source.CostUSD != nil {
				t.Fatalf("raw-only source reported a modified cost: %+v", source)
			}
		}
		if mirror.Days[0].Collected != 0 || mirror.DailyCosts[0].Collected != 0 || mirror.Days[5].Collected != 1 || mirror.TotalCostDays != 1 {
			t.Fatal("incorrect modified bill coverage", mirror.Days)
		}
		FilterOverviewBillingModels(&mirror, map[string]bool{"claude-opus": true})
		if mirror.TotalCostUSD == nil || *mirror.TotalCostUSD != 5 || len(mirror.Rows) != 1 || mirror.Rows[0].CostUSD != 5 {
			t.Fatal("filtered total leaked raw or historical model cost", mirror)
		}
	}
	// A missing modified cache must not fall back to the available raw cache.
	if err = store.DB.Where("user_id=? AND view_type='admin' AND period='week'", users[0].ID).Delete(&OverviewSummaryModified{}).Error; err != nil {
		t.Fatal(err)
	}
	missing, err := store.ReadOverviewSummary(modified, users[0].ID, true, "week", now)
	if err != nil || missing.SummaryAvailable == nil || *missing.SummaryAvailable || missing.TotalCostUSD != nil || len(missing.Rows) != 0 {
		t.Fatalf("missing modified cache fell back to raw: %+v %v", missing, err)
	}
}

func TestModifiedLiveReadsMissingAndRecordedZero(t *testing.T) {
	store, users, accounts, now, _ := overviewSummaryFixture(t)
	view := WithModelUsageSource(context.Background(), "modified")
	var rawDay, zeroDay AccountUsageDay
	for date, day := range map[string]*AccountUsageDay{"2026-11-14": &rawDay, "2026-11-15": &zeroDay} {
		if err := store.DB.Where("account_id=? AND identity_key=? AND usage_date=?", accounts[0].ID, accounts[0].IdentityKey, date).First(day).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := store.DB.Create(&AccountDailyFableRatio{DayID: rawDay.ID, FableRatio: "0"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.DB.Create(&AccountDailyUsageModified{DayID: zeroDay.ID, Model: "free", CostCNY: "0", InputTokens: "17", OutputTokens: "0", CacheReadTokens: "0", CacheWriteTokens: "0"}).Error; err != nil {
		t.Fatal(err)
	}
	// Make raw ledger reads fail, even if their result would later be hidden.
	if err := store.DB.Migrator().RenameTable("account_daily_usages", "isolated_raw_usage"); err != nil {
		t.Fatal(err)
	}
	daily, err := store.AccountDailyUsage(view, accounts[0].ID, "2026-11-14", "2026-11-15")
	if err != nil || daily.TotalCostUSD == nil || *daily.TotalCostUSD != 0 || daily.TotalCostDays != 1 || daily.DailyCosts[0].CostUSD != nil || daily.Days[0].Status != "missing" || daily.DailyCosts[1].CostUSD == nil || len(daily.Rows) != 1 || daily.Rows[0].InputTokens != "17" {
		t.Fatalf("incorrect zero and missing daily usage: %+v %v", daily, err)
	}
	overview, err := store.AccountsOverview(view, users[0].ID, "myself", "", "", true, now)
	if err != nil || overview.StartDate != "2026-11-15" || overview.TotalCostUSD == nil || *overview.TotalCostUSD != 0 || overview.TotalCostDays != 1 || len(overview.Rows) != 1 || overview.Rows[0].InputTokens != "17" {
		t.Fatalf("incorrect source and date range for filtered overview: %+v %v", overview, err)
	}
}

func TestMirrorDailyModelRoundingMatchesDay(t *testing.T) {
	const date = "2026-11-15"
	out := DailyUsageDTO{ExchangeRate: 1, UsageBillTotals: UsageBillTotals{DailyCosts: []DailyBillCost{{Date: date, cents: 2}}}}
	for _, model := range []string{"c", "a", "b"} {
		out.Rows = append(out.Rows, DailyUsageRowDTO{UsageDate: date, AccountDailyUsage: AccountDailyUsage{Model: model, CostCNY: "0.006"}})
	}
	if err := out.allocateModelCents(); err != nil {
		t.Fatal(err)
	}
	if out.Rows[0].CostUSD != 0 || out.Rows[1].CostUSD != 0.01 || out.Rows[2].CostUSD != 0.01 {
		t.Fatal("USD cents do not add up in stable model order", out.Rows)
	}
}
