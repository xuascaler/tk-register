package core

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestTrendSegmentsPreserveMissingResetAndTimeGap(t *testing.T) {
	var series trendAccumulator
	base := time.Now().UTC()
	values := []*float64{trendNumber(0), trendNumber(10), nil, trendNumber(20), trendNumber(2), trendNumber(3), trendNumber(4)}
	for i, v := range values {
		at := base.Add(time.Duration(i) * time.Minute)
		if i == 6 {
			at = at.Add(time.Hour)
		}
		series.add(uint64(i+1), at, v, 150*time.Second)
	}
	want := [][]float64{{0, 10}, {20}, {2, 3}, {4}}
	if len(series.segments) != len(want) {
		t.Fatalf("segments: %+v", series.segments)
	}
	for i, segment := range series.segments {
		if len(segment) != len(want[i]) {
			t.Fatal("unexpected segment", segment)
		}
		for j, p := range segment {
			if p.Value != want[i][j] {
				t.Fatal(p)
			}
		}
	}
}

func trendNumber(n float64) *float64 { return &n }

func TestTrendStreamingReductionBoundedAndRetainsActualEndpoints(t *testing.T) {
	var series trendAccumulator
	base := time.Now().UTC()
	for i := 0; i < 100000; i++ {
		value := float64(i * i)
		series.add(uint64(i+1), base.Add(time.Duration(i)*time.Minute), &value, 150*time.Second)
		if series.count > accountTrendPointLimit*2 {
			t.Fatal("unbounded buffer", series.count)
		}
	}
	series.reduce(accountTrendPointLimit)
	if !series.reduced || len(series.segments) != 1 || len(series.segments[0]) != accountTrendPointLimit {
		t.Fatal("incorrect reduction", series.count)
	}
	points := series.segments[0]
	if points[0].SnapshotID != "1" || points[len(points)-1].SnapshotID != "100000" {
		t.Fatal("endpoints lost")
	}
	for i, p := range points {
		index := p.CollectedAt.Sub(base) / time.Minute
		if p.Value != float64(index*index) {
			t.Fatal("fabricated value", p)
		}
		if i > 0 && (!p.CollectedAt.After(points[i-1].CollectedAt) || p.Value < points[i-1].Value) {
			t.Fatal("reordered data")
		}
	}
}

func TestTrendManyGapsNeverBecomeConnectingLines(t *testing.T) {
	var series trendAccumulator
	base := time.Now().UTC()
	for i := 0; i < 5000; i++ {
		series.add(uint64(i+1), base.Add(time.Duration(i)*time.Hour), trendNumber(float64(i)), time.Minute)
	}
	series.reduce(accountTrendPointLimit)
	if !series.reduced || series.count > accountTrendPointLimit {
		t.Fatal("budget not respected")
	}
	for _, segment := range series.segments {
		if len(segment) != 1 {
			t.Fatal("gap bridged")
		}
	}
	if series.segments[len(series.segments)-1][0].SnapshotID != "5000" {
		t.Fatal("newest observation lost")
	}
}

func TestTrendInvalidValuesBreakAndZeroRemains(t *testing.T) {
	var series trendAccumulator
	base := time.Now().UTC()
	for i, v := range []float64{0, math.NaN(), 1, math.Inf(1), 2, -1, 3} {
		series.add(uint64(i+1), base.Add(time.Duration(i)*time.Minute), &v, 150*time.Second)
	}
	if len(series.segments) != 4 || series.count != 4 || series.segments[0][0].Value != 0 {
		t.Fatal("invalid values or zero mishandled")
	}
}

func TestTrendCadenceChangePreservesHistoricalCurve(t *testing.T) {
	var series trendAccumulator
	base := time.Now().UTC()
	for i := 0; i < 3; i++ {
		series.add(uint64(i+1), base.Add(time.Duration(i)*2*time.Hour), trendNumber(float64(i)), 5*time.Hour)
	}
	series.add(4, base.Add(4*time.Hour+5*time.Minute), trendNumber(3), 750*time.Second)
	series.add(5, base.Add(4*time.Hour+10*time.Minute), trendNumber(4), 750*time.Second)
	series.add(6, base.Add(5*time.Hour), trendNumber(5), 750*time.Second)
	if len(series.segments) != 2 || len(series.segments[0]) != 5 || len(series.segments[1]) != 1 {
		t.Fatal("cadence transition or long gap lost", series.segments)
	}
}

func TestAccountTrendAndSnapshotPagesKeepIdentityAndOriginalAmounts(t *testing.T) {
	s, owner, vendor, version := syncFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-10 * time.Hour).Truncate(time.Second)
	syncSubmission(t, s, owner.ID, vendor.ID, version.ID, "trend@example.com", "claude/cc-pro", "accepted", "501", base.Add(-time.Hour))
	targets, err := s.CollectionTargets(ctx, owner.ID, 0)
	if err != nil || len(targets) != 1 {
		t.Fatal(targets, err)
	}
	account := targets[0]
	currency := "CNY"
	rows := make([]AccountSnapshot, 0, 122)
	for i := 0; i < 120; i++ {
		cost := float64(i) * 7.2
		tokens := int64(i)
		rows = append(rows, AccountSnapshot{AccountID: account.ID, IdentityKey: account.IdentityKey, SnapshotDate: base.Format("2006-01-02"), CollectedAt: base.Add(time.Duration(i) * 5 * time.Minute), DataSource: "website", AccountSnapshotValues: AccountSnapshotValues{CostTotal: &cost, Currency: &currency, InputTokensTotal: &tokens}})
	}
	old := rows[0]
	old.IdentityKey = "old-account-lifetime"
	rows = append(rows, old)
	if err = s.DB.CreateInBatches(rows, 50).Error; err != nil {
		t.Fatal(err)
	}
	data, err := s.AccountTrend(ctx, account.ID, 7, 300)
	if err != nil || data.SnapshotCount != 120 || data.Reduced {
		t.Fatal(data, err)
	}
	line := data.Trend["cost_total_usd"]
	if len(line) != 1 || len(line[0]) != 120 || math.Abs(line[0][119].Value-119) > 1e-9 {
		t.Fatal("USD trend incorrect", line)
	}
	if len(data.Trend["output_tokens_total"]) != 0 {
		t.Fatal("missing output turned into zero")
	}
	p1, err := s.AccountSnapshotPage(ctx, account.ID, 7, 1, 50)
	if err != nil || p1.Total != 120 || len(p1.Items) != 50 || !p1.Items[0].CollectedAt.Equal(base.Add(70*5*time.Minute)) {
		t.Fatal(p1, err)
	}
	p2, err := s.AccountSnapshotPage(ctx, account.ID, 7, 2, 50)
	if err != nil || len(p2.Items) != 50 || !p2.Items[49].CollectedAt.Before(p1.Items[0].CollectedAt) {
		t.Fatal("pages overlap", err)
	}
	if math.Abs(*p1.Items[49].CostTotal-119*7.2) > 1e-9 || *p1.Items[49].Currency != "CNY" {
		t.Fatal("original amount changed")
	}
	for _, params := range [][3]int{{1, 1, 50}, {7, 0, 50}, {7, 1, 101}} {
		if _, e := s.AccountSnapshotPage(ctx, account.ID, params[0], params[1], params[2]); !IsValidationError(e) {
			t.Fatal("bad page accepted", params, e)
		}
	}
	if _, e := s.AccountTrend(ctx, account.ID, 7, 0); !IsValidationError(e) {
		t.Fatal("bad interval accepted", e)
	}
}
