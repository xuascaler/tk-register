package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"goapi-tk-register/server/internal/core"
)

func seedVisibilityBills(t *testing.T, a *app, account core.SyncedAccount) string {
	t.Helper()
	now := time.Now().In(time.FixedZone("Asia/Shanghai", 8*60*60))
	for n := 1; n <= 2; n++ {
		day := core.AccountUsageDay{AccountID: account.ID, IdentityKey: account.IdentityKey, UsageDate: now.AddDate(0, 0, -n).Format("2006-01-02"), LastSuccessAt: &now, LastAttemptAt: now}
		if err := a.store.DB.Create(&day).Error; err != nil {
			t.Fatal(err)
		}
		if err := a.store.DB.Create(&core.AccountDailyFableRatio{DayID: day.ID, FableRatio: "0"}).Error; err != nil {
			t.Fatal(err)
		}
		for _, model := range []string{"private-model-one", "private-model-two"} {
			bill := core.AccountDailyUsage{DayID: day.ID, Model: model, CostCNY: "0.10000001", InputTokens: "9007199254740993", OutputTokens: "11", CacheReadTokens: "3", CacheWriteTokens: "4"}
			if err := a.store.DB.Create(&bill).Error; err != nil {
				t.Fatal(err)
			}
			allocated := core.AccountDailyUsageModified(bill)
			if err := a.store.DB.Create(&allocated).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	return now.AddDate(0, 0, -1).Format("2006-01-02")
}

func requireTotalsOnly(t *testing.T, out map[string]any, raw string) {
	t.Helper()
	for _, field := range []string{"rows", "model_costs"} {
		if _, present := out[field]; present {
			t.Fatalf("disabled response contains %s: %s", field, raw)
		}
	}
	if out["model_costs_enabled"] != false || strings.Contains(raw, "private-model") {
		t.Fatalf("disabled model data leaked: %s", raw)
	}
	var cents int64
	for _, row := range out["daily_totals"].([]any) {
		day := row.(map[string]any)
		if day["input_tokens"] != "18014398509481986" || day["cost_cny"] != "0.20000002" {
			t.Fatalf("exact counters or CNY changed: %+v", day)
		}
		cents += int64(math.Round(day["cost_usd"].(float64) * 100))
	}
	if cents != int64(math.Round(out["total_cost_usd"].(float64)*100)) {
		t.Fatalf("daily totals differ from total: %s", raw)
	}
}

func TestPersonalOverviewVisibilityTogglesExistingCacheAndLiveData(t *testing.T) {
	a, owner, cookie, _, accounts, fake := setupPageSync(t)
	end := seedVisibilityBills(t, a, accounts[0])
	ctx := context.Background()
	if enabled, err := a.userModelCostsEnabled(ctx); err != nil || enabled {
		t.Fatal("model details must default to disabled", enabled, err)
	}
	if err := a.store.BuildOverviewSummaries(ctx, owner.ID, false, end, "visibility-test", time.Now()); err != nil {
		t.Fatal(err)
	}
	var stored []core.OverviewSummary
	if err := a.store.DB.Order("id").Find(&stored).Error; err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(stored)
	for _, enabled := range []bool{false, true, false} {
		if err := a.store.PatchSettings(ctx, map[string]string{"user_overview_model_costs_enabled": fmt.Sprint(enabled)}, owner.ID); err != nil {
			t.Fatal(err)
		}
		for _, route := range []string{"accounts/overview", "accounts/overview?sources=myself"} {
			w := request(t, a, "GET", route, nil, cookie, "")
			requireStatus(t, w, 200)
			out := data(t, w)
			if enabled {
				if out["model_costs_enabled"] != true || !strings.Contains(w.Body.String(), "private-model-one") || len(out["rows"].([]any)) != 4 {
					t.Fatal("enabled model data missing", w.Body.String())
				}
			} else {
				requireTotalsOnly(t, out, w.Body.String())
				var cents int64
				for _, row := range out["sources"].([]any) {
					if cost := row.(map[string]any)["cost_usd"]; cost != nil {
						cents += int64(math.Round(cost.(float64) * 100))
					}
				}
				if float64(cents)/100 != out["total_cost_usd"] {
					t.Fatal("source total differs", w.Body.String())
				}
			}
			if route == "accounts/overview" && out["summary_period"] != "all_time" {
				t.Fatal("personal default is not All time", w.Body.String())
			}
		}
	}
	var afterRows []core.OverviewSummary
	if err := a.store.DB.Order("id").Find(&afterRows).Error; err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(afterRows)
	if string(before) != string(after) || fake.calls != 0 {
		t.Fatal("visibility toggle changed cache or contacted upstream")
	}
}

func TestPersonalOverviewDateAndModelRestrictionsLeaveManagementAvailable(t *testing.T) {
	a, owner, cookie, _, accounts, _ := setupPageSync(t)
	end := seedVisibilityBills(t, a, accounts[0])
	_, adminCookie, _ := vendorSession(t, a, "visibility-admin@example.com", "admin")
	for _, enabled := range []string{"false", "true"} {
		if err := a.store.PatchSettings(context.Background(), map[string]string{"user_overview_model_costs_enabled": enabled}, owner.ID); err != nil {
			t.Fatal(err)
		}
		for _, query := range []string{"start_date=" + end + "&end_date=" + end, "start_date=", "end_date=", "period=custom", "period=invalid"} {
			for _, session := range []*http.Cookie{cookie, adminCookie} {
				requireStatus(t, request(t, a, "GET", "accounts/overview?"+query, nil, session, ""), 400)
			}
		}
		if enabled == "false" {
			for _, query := range []string{"models=all", "models=none", "models=private-model-one", "models="} {
				requireStatus(t, request(t, a, "GET", "accounts/overview?"+query, nil, cookie, ""), 400)
			}
			w := request(t, a, "GET", "accounts/overview", nil, adminCookie, "")
			requireStatus(t, w, 200)
			if data(t, w)["model_costs_enabled"] != false {
				t.Fatal("administrator personal view bypassed setting")
			}
		}
		management := request(t, a, "GET", "admin/accounts/overview?start_date="+end+"&end_date="+end+"&models=private-model-one", nil, adminCookie, "")
		requireStatus(t, management, 200)
		if !strings.Contains(management.Body.String(), "private-model-one") || strings.Contains(management.Body.String(), "private-model-two") {
			t.Fatal("management custom date/model selection changed", management.Body.String())
		}
	}
}

func TestPersonalDailyUsageVisibilityUsesTotalsAndKeepsManagementRows(t *testing.T) {
	a, owner, cookie, _, accounts, _ := setupPageSync(t)
	end := seedVisibilityBills(t, a, accounts[0])
	_, adminCookie, _ := vendorSession(t, a, "daily-visibility-admin@example.com", "admin")
	path := fmt.Sprintf("accounts/%d/daily-usage", accounts[0].ID)
	w := request(t, a, "GET", path, nil, cookie, "")
	requireStatus(t, w, 200)
	requireTotalsOnly(t, data(t, w), w.Body.String())
	requireStatus(t, request(t, a, "GET", path+"?models=all", nil, cookie, ""), 400)
	management := request(t, a, "GET", "admin/synced-"+path+"?start_date="+end+"&end_date="+end, nil, adminCookie, "")
	requireStatus(t, management, 200)
	if !strings.Contains(management.Body.String(), "private-model-one") {
		t.Fatal("management daily rows disappeared", management.Body.String())
	}
	if err := a.store.PatchSettings(context.Background(), map[string]string{"user_overview_model_costs_enabled": "true"}, owner.ID); err != nil {
		t.Fatal(err)
	}
	w = request(t, a, "GET", path, nil, cookie, "")
	requireStatus(t, w, 200)
	if data(t, w)["model_costs_enabled"] != true || !strings.Contains(w.Body.String(), "private-model-one") {
		t.Fatal("enabled daily rows missing", w.Body.String())
	}
}

func TestPersonalTotalsDistinguishCollectedZeroAndUnavailable(t *testing.T) {
	for _, collected := range []bool{false, true} {
		now := time.Now()
		out := core.AccountsOverview{ExchangeRate: 7.2, Rows: []core.DailyUsageRowDTO{}, Sources: []core.OverviewSource{{AccountSource: core.AccountSource{ID: "myself"}}}}
		daily := core.DailyUsageDTO{ExchangeRate: 7.2, Rows: []core.DailyUsageRowDTO{}, Days: []core.DailyUsageDayDTO{{Date: "2026-11-15", Status: "missing"}}}
		if collected {
			zero := 0.0
			out.Sources[0].CostUSD = &zero
			out.Sources[0].CollectedDays = 1
			daily.Days[0].Status, daily.Days[0].CollectedAt = "complete", &now
			daily.TotalCostUSD = &zero
		}
		overview, err := personalOverview(out, false)
		if err != nil || (overview.TotalCostUSD != nil) != collected || overview.TotalCostUSD != nil && *overview.TotalCostUSD != 0 {
			t.Fatal("overview zero/unavailable confused", overview, err)
		}
		usage, err := personalDailyUsage(daily, false)
		if err != nil || (usage.TotalCostUSD != nil) != collected || usage.TotalCostUSD != nil && *usage.TotalCostUSD != 0 {
			t.Fatal("daily zero/unavailable confused", usage, err)
		}
		for _, value := range []any{overview, usage} {
			raw, err := json.Marshal(value)
			if err != nil || strings.Contains(string(raw), `"rows"`) || strings.Contains(string(raw), `"model_costs"`) || !strings.Contains(string(raw), `"daily_totals":[]`) {
				t.Fatal("disabled empty DTO has model fields or null totals", string(raw), err)
			}
		}
	}
}
