package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"reflect"
	"testing"
	"time"

	"gorm.io/gorm"
)

func overviewBillingRow(date, model, amount string) DailyUsageRowDTO {
	return DailyUsageRowDTO{UsageDate: date, AccountDailyUsage: AccountDailyUsage{Model: model, CostCNY: amount, InputTokens: "9007199254740993", OutputTokens: "20"}}
}

func overviewBillingFixture() AccountsOverview {
	old := 968142.91
	return AccountsOverview{
		ExchangeRate: 7.2,
		Sources: []OverviewSource{
			{AccountSource: AccountSource{ID: "myself"}, Total: 3, Statuses: map[string]int64{"active": 2, "banned": 1}, CostUSD: &old, CostKnown: 3, CollectedDays: 2},
			{AccountSource: AccountSource{ID: "member"}, Total: 2, Statuses: map[string]int64{"sync_failed": 2}, CostUSD: &old, CostKnown: 2, CollectedDays: 2},
			{AccountSource: AccountSource{ID: "zero"}, Total: 1, Statuses: map[string]int64{"offline": 1}, CollectedDays: 1},
			{AccountSource: AccountSource{ID: "missing"}, Total: 1, Statuses: map[string]int64{"unknown": 1}},
		},
		ModelCosts: []OverviewModelCost{
			{SourceID: "myself", Model: "alpha", CostCNY: "0.0504"},
			{SourceID: "member", Model: "alpha", CostCNY: "0.054"},
			{SourceID: "myself", Model: "beta", CostCNY: "0.0504"},
			{SourceID: "member", Model: "beta", CostCNY: "0.054"},
			{SourceID: "myself", Model: "gamma", CostCNY: "0.0504"},
			{SourceID: "member", Model: "gamma", CostCNY: "0.054"},
		},
		Rows: []DailyUsageRowDTO{
			overviewBillingRow("2026-11-14", "alpha", "0.0504"), overviewBillingRow("2026-11-15", "alpha", "0.054"),
			overviewBillingRow("2026-11-14", "beta", "0.0504"), overviewBillingRow("2026-11-15", "beta", "0.054"),
			overviewBillingRow("2026-11-14", "gamma", "0.0504"), overviewBillingRow("2026-11-15", "gamma", "0.054"),
		},
		Days: []OverviewDay{{Date: "2026-11-14", Status: "complete", Expected: 7, Collected: 5}},
	}
}

func overviewCents(value float64) int64 { return int64(math.Round(value * 100)) }

func assertOverviewBillingSums(t *testing.T, out AccountsOverview, expected int64) {
	t.Helper()
	var sourceTotal, modelTotal, dayTotal int64
	models, dates := map[string]int64{}, map[string]int64{}
	for _, source := range out.Sources {
		if source.CostUSD != nil {
			sourceTotal += overviewCents(*source.CostUSD)
		}
	}
	for _, cell := range out.ModelCosts {
		cents := overviewCents(cell.CostUSD)
		modelTotal += cents
		models[cell.Model] += cents
	}
	for _, row := range out.Rows {
		cents := overviewCents(row.CostUSD)
		dayTotal += cents
		dates[row.Model] += cents
	}
	if sourceTotal != expected || modelTotal != expected || dayTotal != expected || !reflect.DeepEqual(models, dates) {
		t.Fatalf("monetary totals differ: sources=%d models=%d daily=%d want=%d; model matrix=%v daily=%v", sourceTotal, modelTotal, dayTotal, expected, models, dates)
	}
}

func TestProjectOverviewBillingRoundingAndFilters(t *testing.T) {
	out := overviewBillingFixture()
	if err := ProjectOverviewBilling(&out); err != nil {
		t.Fatal(err)
	}
	// Three models at 1.45 cents each: the rounded grand total is 4 cents.
	// Stable largest-remainder allocation gives alpha 2, beta/gamma 1 each.
	assertOverviewBillingSums(t, out, 4)
	if out.Sources[0].Total != 3 || out.Sources[0].Statuses["banned"] != 1 || out.Sources[0].CostKnown != 3 || out.Sources[0].CollectedDays != 2 || out.Rows[0].InputTokens != "9007199254740993" || out.Rows[0].CostCNY != "0.0504" || out.Days[0].Expected != 7 {
		t.Fatal("projection changed counts, raw money, tokens, or coverage")
	}
	if out.Sources[2].CostUSD == nil || *out.Sources[2].CostUSD != 0 || out.Sources[3].CostUSD != nil {
		t.Fatal("successful zero billing and missing collection conflated")
	}
	FilterOverviewBillingModels(&out, map[string]bool{"alpha": true})
	assertOverviewBillingSums(t, out, 2)
	if out.Sources[0].Statuses["active"] != 2 || len(out.Rows) != 2 || len(out.ModelCosts) != 2 {
		t.Fatal("model selection affected account statuses")
	}
	FilterOverviewBillingModels(&out, map[string]bool{})
	if len(out.Rows) != 0 || len(out.ModelCosts) != 0 {
		t.Fatal("empty model selection returned bills")
	}
	for _, source := range out.Sources {
		if source.CostUSD != nil {
			t.Fatal("explicit no models should show no monetary value")
		}
	}
	out = overviewBillingFixture()
	if err := ProjectOverviewBilling(&out); err != nil {
		t.Fatal(err)
	}
	FilterOverviewBillingModels(&out, map[string]bool{"nonexistent": true})
	if out.Sources[0].CostUSD == nil || *out.Sources[0].CostUSD != 0 || out.Sources[3].CostUSD != nil {
		t.Fatal("unbilled selected model should be zero only for collected sources")
	}
}

func TestProjectOverviewBillingDeterministicAcrossOrderAndRefunds(t *testing.T) {
	original, shuffled := overviewBillingFixture(), overviewBillingFixture()
	for i, j := 0, len(shuffled.ModelCosts)-1; i < j; i, j = i+1, j-1 {
		shuffled.ModelCosts[i], shuffled.ModelCosts[j] = shuffled.ModelCosts[j], shuffled.ModelCosts[i]
	}
	for i, j := 0, len(shuffled.Rows)-1; i < j; i, j = i+1, j-1 {
		shuffled.Rows[i], shuffled.Rows[j] = shuffled.Rows[j], shuffled.Rows[i]
	}
	for _, out := range []*AccountsOverview{&original, &shuffled} {
		if err := ProjectOverviewBilling(out); err != nil {
			t.Fatal(err)
		}
	}
	values := func(out AccountsOverview) map[string]float64 {
		m := map[string]float64{}
		for _, c := range out.ModelCosts {
			m["source:"+c.SourceID+":"+c.Model] = c.CostUSD
		}
		for _, r := range out.Rows {
			m["day:"+r.UsageDate+":"+r.Model] = r.CostUSD
		}
		return m
	}
	if !reflect.DeepEqual(values(original), values(shuffled)) {
		t.Fatal("allocation depends on SQL or map iteration order")
	}
	for _, amount := range []string{"-719.9999", "-720", "-720.0001", "-720.036"} {
		out := AccountsOverview{ExchangeRate: 7.2,
			Sources:    []OverviewSource{{AccountSource: AccountSource{ID: "a"}, CollectedDays: 1}, {AccountSource: AccountSource{ID: "b"}, CollectedDays: 1}},
			ModelCosts: []OverviewModelCost{{SourceID: "a", Model: "refund", CostCNY: "720"}, {SourceID: "b", Model: "refund", CostCNY: amount}},
			Rows:       []DailyUsageRowDTO{overviewBillingRow("2026-11-14", "refund", "720"), overviewBillingRow("2026-11-15", "refund", amount)},
		}
		if err := ProjectOverviewBilling(&out); err != nil {
			t.Fatal(amount, err)
		}
		want := int64(0)
		if amount == "-720.036" {
			want = -1 // exact negative half cent, rounded away from zero
		}
		assertOverviewBillingSums(t, out, want)
		if math.Abs(out.Rows[0].CostUSD-100) > 0.010001 || math.Abs(out.Rows[1].CostUSD+100) > 0.010001 {
			t.Fatalf("refund netting distorted daily amounts: %+v", out.Rows)
		}
	}
}

func TestProjectOverviewBillingRejectsInconsistentRawData(t *testing.T) {
	for _, change := range []func(*AccountsOverview){
		func(o *AccountsOverview) { o.ExchangeRate = 0 },
		func(o *AccountsOverview) { o.Rows[0].CostCNY = "invalid" },
		func(o *AccountsOverview) { o.Rows[0].CostCNY = "0.2" },
		func(o *AccountsOverview) { o.Rows = nil },
		func(o *AccountsOverview) { o.ModelCosts[0].SourceID = "missing" },
		func(o *AccountsOverview) { o.ModelCosts[0].SourceID = "not-visible" },
	} {
		out := overviewBillingFixture()
		change(&out)
		if err := ProjectOverviewBilling(&out); err == nil {
			t.Fatal("inconsistent billing data accepted")
		}
	}
}

func TestProjectOverviewBillingLegacyCacheAndLiveConsistency(t *testing.T) {
	s, users, _, now, _ := overviewSummaryFixture(t)
	ctx := context.Background()
	for _, administrative := range []bool{false, true} {
		if err := s.BuildOverviewSummaries(ctx, users[0].ID, administrative, "2026-11-15", "legacy-billing-projection", now); err != nil {
			t.Fatal(err)
		}
		for _, period := range []string{"day", "week", "month", "all_time"} {
			cached := readSummary(t, s, users[0].ID, administrative, period, now)
			query := s.AccountsOverview
			if administrative {
				query = s.AdminAccountsOverview
			}
			live, err := query(ctx, users[0].ID, "all", cached.StartDate, cached.EndDate, false, now)
			if err != nil {
				t.Fatal(err)
			}
			for _, out := range []*AccountsOverview{&cached, &live} {
				if err := ProjectOverviewBilling(out); err != nil {
					t.Fatal(err)
				}
			}
			modelValues := func(out AccountsOverview) map[string]float64 {
				values := map[string]float64{}
				for _, cost := range out.ModelCosts {
					values[cost.SourceID+":"+cost.Model] = cost.CostUSD
				}
				return values
			}
			if !reflect.DeepEqual(cached.Sources, live.Sources) || !reflect.DeepEqual(modelValues(cached), modelValues(live)) {
				t.Fatalf("cache/live monetary projection differs for %s admin=%v: cache=%+v live=%+v", period, administrative, cached.Sources, live.Sources)
			}
			for i := range cached.Rows {
				if cached.Rows[i].CostUSD != live.Rows[i].CostUSD {
					t.Fatal("daily cache/live money differs")
				}
			}
			if period == "day" && (cached.Sources[0].CostUSD == nil || *cached.Sources[0].CostUSD != 0) {
				t.Fatal("legacy cumulative snapshot was used for zero billing day")
			}
		}
	}
	// Selected sources still use the exact same projection on the scoped bills.
	subset, err := s.AdminAccountsOverview(ctx, users[0].ID, "myself", "2026-11-09", "2026-11-15", false, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := ProjectOverviewBilling(&subset); err != nil {
		t.Fatal(err)
	}
	if len(subset.Sources) != 1 || subset.Sources[0].Total != 1 {
		t.Fatal("billing projection changed source scope")
	}
	assertOverviewBillingSums(t, subset, 4000)
	FilterOverviewBillingModels(&subset, map[string]bool{"claude-sonnet": true})
	assertOverviewBillingSums(t, subset, 2000)
	// Projection is read-only; already persisted v1 payloads need no migration.
	var saved []OverviewSummary
	if err := s.DB.Find(&saved).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range saved {
		var payload overviewSummaryPayload
		if err := json.Unmarshal([]byte(row.PayloadJSON), &payload); err != nil || payload.Version != 1 || payload.Sources[0].CostCNY == nil || *payload.Sources[0].CostCNY != "72" {
			t.Fatal("legacy payload was modified", err)
		}
	}
}

func TestLiveOverviewSingleConnectionAndNestedReadTransaction(t *testing.T) {
	s, users, _, now, _ := overviewSummaryFixture(t)
	db, err := s.DB.DB()
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	for _, administrative := range []bool{false, true} {
		for _, nested := range []bool{false, true} {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			read := func(store *Store) error {
				query := store.AccountsOverview
				if administrative {
					query = store.AdminAccountsOverview
				}
				out, err := query(ctx, users[0].ID, "all", "2026-11-09", "2026-11-15", false, now)
				if err != nil {
					return err
				}
				if len(out.Sources) == 0 || len(out.Rows) == 0 {
					t.Fatal("live transaction returned no account/bill data")
				}
				return ProjectOverviewBilling(&out)
			}
			if nested {
				// Read-only diagnostics may already hold a transaction. GORM's
				// nested savepoint must reuse that connection, not open another.
				err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
					scoped := *s
					scoped.DB = tx
					return read(&scoped)
				}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
			} else {
				err = read(s)
			}
			cancel()
			if err != nil {
				t.Fatalf("single-connection overview admin=%v nested=%v: %v", administrative, nested, err)
			}
			if db.Stats().InUse != 0 {
				t.Fatal("live overview retained a connection after completing")
			}
		}
	}
}
