package core

import (
	"context"
	"encoding/json"
	"testing"
)

func TestMirrorReadsRetainLedgerAcrossSources(t *testing.T) {
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
	if rawDay.TotalCostUSD == nil || changed.TotalCostUSD == nil || *rawDay.TotalCostUSD != 40 || *changed.TotalCostUSD != 40 || changed.DailyCosts[0].CostUSD == nil || *changed.DailyCosts[0].CostUSD != 20 {
		t.Fatal("historical detail gap changed the daily bill")
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
		if orig.TotalCostUSD == nil || mirror.TotalCostUSD == nil || *orig.TotalCostUSD != 160 || *mirror.TotalCostUSD != 160 || len(mirror.Rows) != 2 || mirror.Rows[0].InputTokens != "18446744073709551616" || len(orig.Rows) != 4 {
			t.Fatalf("incorrect overview cached=%v raw=%+v mirror=%+v", cached, orig, mirror)
		}
		for _, s := range mirror.Sources {
			if s.Total > 0 && (s.CostUSD == nil || *s.CostUSD != 40) {
				t.Fatalf("source omitted historical bill %+v", s)
			}
		}
		if mirror.Days[0].Collected != 0 || mirror.DailyCosts[0].Collected != 4 || mirror.Days[5].Collected != 1 {
			t.Fatal("model coverage mixed up with bill coverage", mirror.Days)
		}
		FilterOverviewBillingModels(&mirror, map[string]bool{"claude-opus": true})
		if mirror.TotalCostUSD == nil || *mirror.TotalCostUSD != 5 || len(mirror.Rows) != 1 || mirror.Rows[0].CostUSD != 5 {
			t.Fatal("filtered total leaked raw or historical model cost", mirror)
		}
	}
	// A summary saved before rollout retains its total and has no mirror breakdown.
	var saved OverviewSummary
	if err = store.DB.Where("user_id=? AND view_type='admin' AND period='week'", users[0].ID).First(&saved).Error; err != nil {
		t.Fatal(err)
	}
	var legacy overviewSummaryPayload
	if err = json.Unmarshal([]byte(saved.PayloadJSON), &legacy); err != nil {
		t.Fatal(err)
	}
	legacy.Modified = nil
	for i := range legacy.Sources {
		legacy.Sources[i].TotalCostDays = 0
	}
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.DB.Model(&saved).Update("payload_json", string(encoded)).Error; err != nil {
		t.Fatal(err)
	}
	historical, err := store.ReadOverviewSummary(modified, users[0].ID, true, "week", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = ProjectOverviewBilling(&historical); err != nil {
		t.Fatal(err)
	}
	if historical.TotalCostUSD == nil || *historical.TotalCostUSD != 160 || historical.TotalCostDays != 3 || len(historical.ModelCosts) != 0 || len(historical.Rows) != 0 || historical.Days[0].Collected != 0 || historical.Sources[0].CostUSD == nil {
		t.Fatalf("legacy raw details leaked or aggregate lost %+v", historical)
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
