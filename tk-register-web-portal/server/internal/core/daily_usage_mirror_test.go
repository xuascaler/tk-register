package core

import (
	"context"
	"math/big"
	"reflect"
	"testing"
	"time"

	"goapi-tk-register/server/internal/billing"
)

func mirrorTestRow(name, cost string) AccountDailyUsage {
	return AccountDailyUsage{Model: name, CostCNY: cost, InputTokens: "100", OutputTokens: "9007199254740993", CacheReadTokens: "50", CacheWriteTokens: "10"}
}

func TestDailyMirrorAllocation(t *testing.T) {
	rules := fableAllocationRules{big.NewRat(19, 100), big.NewRat(16, 100), big.NewRat(21, 100)}
	raw := []AccountDailyUsage{
		mirrorTestRow("claude-fable-v1", "50"),
		mirrorTestRow("claude-fable-v2", "30"),
		mirrorTestRow("claude-opus", "320"),
		mirrorTestRow("claude-free", "0"),
	}
	ratio, rows, err := allocateDailyMirror(raw, rules, mirrorIntervals/2)
	if err != nil || ratio.Cmp(big.NewRat(18, 100)) != 0 {
		t.Fatalf("ratio = %s, err = %v", ratio, err)
	}
	for i, cost := range []string{"45.00000000", "27.00000000", "328.00000000", "0"} {
		if rows[i].CostCNY != cost {
			t.Errorf("model %s cost %s, want %s", rows[i].Model, rows[i].CostCNY, cost)
		}
	}
	f := rows[0]
	if f.InputTokens != "90" || f.OutputTokens != "8106479329266894" || f.CacheReadTokens != "45" || f.CacheWriteTokens != "9" {
		t.Fatal("incorrect token scaling", f)
	}
	if rows[2].InputTokens != "103" || AccountDailyUsage(rows[3]) != raw[3] {
		t.Fatal("rounding and zero-cost counters", rows)
	}
	for _, c := range []struct {
		label string
		raw   []AccountDailyUsage
		want  *big.Rat
	}{
		{"threshold", []AccountDailyUsage{mirrorTestRow("fable", "19"), mirrorTestRow("other", "81")}, big.NewRat(19, 100)},
		{"below", []AccountDailyUsage{mirrorTestRow("fable", "1"), mirrorTestRow("other", "7")}, big.NewRat(1, 8)},
		{"all Fable including free non-Fable", []AccountDailyUsage{mirrorTestRow("Claude-Fable-5-1", "1"), mirrorTestRow("claude/fable-5-2", "8"), mirrorTestRow("other", "0")}, big.NewRat(1, 1)},
		{"no Fable", []AccountDailyUsage{mirrorTestRow("notfable", "9")}, new(big.Rat)},
		{"all free", []AccountDailyUsage{mirrorTestRow("fable", "0")}, new(big.Rat)},
		{"empty", nil, new(big.Rat)},
	} {
		t.Run(c.label, func(t *testing.T) {
			ratio, got, err := allocateDailyMirror(c.raw, rules, 1)
			if err != nil || ratio.Cmp(c.want) != 0 || len(got) != len(c.raw) {
				t.Fatal(ratio, got, err)
			}
			for i, row := range got {
				if AccountDailyUsage(row) != c.raw[i] {
					t.Fatalf("unchanged row differs: %+v", row)
				}
			}
		})
	}

	// Two distinct one-unit models tie for the group's remaining unit. Model
	// name decides the winner, independently of the original slice order.
	tiny := []AccountDailyUsage{mirrorTestRow("fable-b", "0.00000001"), mirrorTestRow("fable-a", "0.00000001"), mirrorTestRow("other", "0.00000001")}
	for _, slot := range []int64{1, mirrorIntervals - 1} {
		ratio, got, err := allocateDailyMirror(tiny, rules, slot)
		if err != nil || ratio.Cmp(rules.minimum) <= 0 || ratio.Cmp(rules.maximum) >= 0 {
			t.Fatal(ratio, err)
		}
		gotTotal := new(big.Rat)
		for _, row := range got {
			cost, _ := new(big.Rat).SetString(row.CostCNY)
			gotTotal.Add(gotTotal, cost)
		}
		if gotTotal.Cmp(big.NewRat(3, 100000000)) != 0 || got[0].CostCNY != "0.00000000" {
			t.Fatal("lost rounding unit", got)
		}
	}
}

func TestDailyMirrorFirstSuccessfulCapture(t *testing.T) {
	s, user, vendor, version := syncFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 15, 4, 0, 0, 0, time.UTC)
	syncSubmission(t, s, user.ID, vendor.ID, version.ID, "one@example.com", "claude/cc-pro", "accepted", "501", now.Add(-72*time.Hour))
	accounts, err := s.CollectionTargets(ctx, user.ID, 0)
	if err != nil || len(accounts) != 1 {
		t.Fatal(accounts, err)
	}
	a := accounts[0]
	result := billing.DailyAccount{LocalAccountID: idString(a.ID), IdentityKey: a.IdentityKey, Rows: []billing.DailyRow{
		{Model: "claude-fable-1", InputTokens: "100", OutputTokens: "200", CacheReadTokens: "30", CacheWriteTokens: "10", CostCNY: "30"},
		{Model: "claude-opus-1", InputTokens: "100", OutputTokens: "200", CacheReadTokens: "30", CacheWriteTokens: "10", CostCNY: "70"},
	}}
	const token = "mirror-test-lease"
	job := DailyUsageJob{VendorID: vendor.ID, UsageDate: "2026-11-14", Status: "running", LeaseToken: token, NextRunAt: now}
	if err = s.DB.Create(&job).Error; err != nil {
		t.Fatal(err)
	}

	if err = s.DB.Exec("CREATE TRIGGER fail_mirror BEFORE INSERT ON account_daily_usages_modifiled BEGIN SELECT RAISE(ABORT, 'simulated failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	if err = s.SaveDailyUsageAccount(ctx, job, result, now); err == nil {
		t.Fatal("expected mirror write failure")
	}
	for _, table := range []string{"account_daily_usages", "account_daily_usages_modifiled", "account_daily_fable_ratios", "account_usage_days"} {
		var count int64
		if err := s.DB.Table(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatal("partial account day", table, count, err)
		}
	}
	if err = s.DB.Exec("DROP TRIGGER fail_mirror").Error; err != nil {
		t.Fatal(err)
	}
	ch := make(chan error, 2)
	for i := 0; i < cap(ch); i++ {
		go func() { ch <- s.SaveDailyUsageAccount(ctx, job, result, now) }()
	}
	for i := 0; i < cap(ch); i++ {
		if err := <-ch; err != nil {
			t.Fatal(err)
		}
	}
	readMirror := func() ([]AccountDailyUsageModified, []AccountDailyFableRatio) {
		t.Helper()
		var rows []AccountDailyUsageModified
		var ratios []AccountDailyFableRatio
		if err := s.DB.Order("model").Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		if err := s.DB.Find(&ratios).Error; err != nil {
			t.Fatal(err)
		}
		if len(rows) != 2 || len(ratios) != 1 {
			t.Fatal("duplicate or missing day", rows, ratios)
		}
		return rows, ratios
	}
	before, ratioBefore := readMirror()
	result.Rows = nil // Upstream changes must not rewrite the frozen bill.
	if err := s.SaveDailyUsageAccount(ctx, job, result, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	after, ratioAfter := readMirror()
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(ratioBefore, ratioAfter) {
		t.Fatal("retry changed allocation")
	}
	var raw []AccountDailyUsage
	if err := s.DB.Order("model").Find(&raw).Error; err != nil || len(raw) != 2 || raw[0].Model != "claude-fable-1" || raw[0].CostCNY != "30" {
		t.Fatal("original rows changed", raw, err)
	}

	// Simulate a successful bill from before migration. Reading, restarting,
	// or retrying it must never manufacture historical modified rows.
	oldJob := DailyUsageJob{VendorID: vendor.ID, UsageDate: "2026-11-13", Status: "running", LeaseToken: token, NextRunAt: now}
	if err := s.DB.Create(&oldJob).Error; err != nil {
		t.Fatal(err)
	}
	oldDay := AccountUsageDay{AccountID: a.ID, IdentityKey: a.IdentityKey, UsageDate: oldJob.UsageDate, LastAttemptAt: now, LastSuccessAt: &now}
	if err := s.DB.Create(&oldDay).Error; err != nil {
		t.Fatal(err)
	}
	old := mirrorTestRow("claude-fable-older", "10")
	old.DayID = oldDay.ID
	if err := s.DB.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.SaveDailyUsageAccount(ctx, oldJob, result, now); err != nil {
		t.Fatal(err)
	}
	readMirror()

	newJob := DailyUsageJob{VendorID: vendor.ID, UsageDate: "2026-11-15", Status: "running", LeaseToken: token, NextRunAt: now}
	if err := s.DB.Create(&newJob).Error; err != nil {
		t.Fatal(err)
	}
	result.Error = "Supplier unavailable."
	if err := s.SaveDailyUsageAccount(ctx, newJob, result, now); err != nil {
		t.Fatal(err)
	}
	readMirror()
	result.Error = ""
	if err := s.SaveDailyUsageAccount(ctx, newJob, result, now); err != nil {
		t.Fatal(err)
	}
	var emptyBill AccountDailyFableRatio
	if err := s.DB.Joins("JOIN account_usage_days d ON d.id=account_daily_fable_ratios.day_id").Where("d.usage_date = ?", newJob.UsageDate).First(&emptyBill).Error; err != nil || emptyBill.FableRatio != "0" {
		t.Fatal("missing successful empty bill", emptyBill.FableRatio, err)
	}
}
