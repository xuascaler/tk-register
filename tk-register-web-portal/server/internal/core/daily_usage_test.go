package core

import (
	"context"
	"goapi-tk-register/server/internal/billing"
	"strings"
	"testing"
	"time"
)

func TestDailyUsageScheduleAtThreeBeijingAndLeaseRecovery(t *testing.T) {
	for _, tc := range []struct{ utc, date string }{{"2026-11-14T18:59:59Z", "2026-11-13"}, {"2026-11-14T19:00:00Z", "2026-11-14"}, {"2026-12-31T19:00:00Z", "2026-12-31"}} {
		now, _ := time.Parse(time.RFC3339, tc.utc)
		if got := DailyUsageDueDate(now).Format("2006-01-02"); got != tc.date {
			t.Fatal(got, tc)
		}
	}
	s := testStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 15, 4, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		if err := s.EnsureDailyUsageJobs(ctx, now); err != nil {
			t.Fatal(err)
		}
	}
	var n int64
	s.DB.Model(&DailyUsageJob{}).Count(&n)
	if n != 7 {
		t.Fatal("duplicate schedule", n)
	}
	job, err := s.ClaimDailyUsageJob(ctx, now, strings.Repeat("a", 32))
	if err != nil || job == nil || job.UsageDate != "2026-11-14" {
		t.Fatal(job, err)
	}
	newer, err := s.ClaimDailyUsageJob(ctx, now.Add(5*time.Minute), strings.Repeat("b", 32))
	if err != nil || newer == nil || newer.ID != job.ID {
		t.Fatal("lost job not recovered", newer, err)
	}
	if err = s.CompleteDailyUsage(ctx, *job, 1, 0, now); err != nil {
		t.Fatal(err)
	}
	var saved DailyUsageJob
	s.DB.First(&saved, job.ID)
	if saved.Status != "running" || saved.LeaseToken != newer.LeaseToken {
		t.Fatal("stale worker overwrote newer lease")
	}
	if err = s.DeferDailyUsage(ctx, *newer, now.Add(10*time.Minute), "Waiting for supplier", true); err != nil {
		t.Fatal(err)
	}
	s.DB.First(&saved, job.ID)
	if saved.Status != "queued" || saved.Attempts != 1 {
		t.Fatal("cooldown counted as attempt", saved)
	}
	for _, dates := range [][2]string{{"2026-11-15", "2026-11-15"}, {"2026-01-01", "2026-11-14"}, {"2026-2-1", "2026-02-02"}} {
		if err = ValidateDailyUsageRange(dates[0], dates[1], now); err == nil {
			t.Fatal("bad range", dates)
		}
	}
}
func TestDailyUsageIdempotencyEmptyDaysStaleDataRateAndIdentity(t *testing.T) {
	s, user, vendor, version := syncFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, _ := s.DB.DB()
	db.SetMaxOpenConns(1)
	now := time.Date(2026, 11, 15, 4, 0, 0, 0, time.UTC)
	syncSubmission(t, s, user.ID, vendor.ID, version.ID, "one@example.com", "claude/cc-pro", "accepted", "501", now.Add(-72*time.Hour))
	targets, err := s.CollectionTargets(ctx, user.ID, 0)
	if err != nil || len(targets) != 1 {
		t.Fatal(targets, err)
	}
	account := targets[0]
	if err = s.EnsureDailyUsageJobs(ctx, now); err != nil {
		t.Fatal(err)
	}
	job, err := s.ClaimDailyUsageJob(ctx, now, strings.Repeat("t", 32))
	if err != nil || job == nil {
		t.Fatal(err)
	}
	result := billing.DailyAccount{LocalAccountID: idString(account.ID), IdentityKey: account.IdentityKey, Rows: []billing.DailyRow{{Model: "claude-test", InputTokens: "9007199254740993", OutputTokens: "20", CacheReadTokens: "30", CacheWriteTokens: "40", CostCNY: "72.5"}}}
	for i := 0; i < 2; i++ {
		if err = s.SaveDailyUsageAccount(ctx, *job, result, now); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.PatchSettings(ctx, map[string]string{"exchange_rate_cny_per_usd": "7.25"}, user.ID); err != nil {
		t.Fatal(err)
	}
	out, err := s.AccountDailyUsage(ctx, account.ID, "2026-11-13", "2026-11-14")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Rows) != 1 || out.Rows[0].CostUSD != 10 || out.Rows[0].InputTokens != "9007199254740993" || out.Rows[0].UsageDate != "2026-11-14" || out.Days[0].CollectedAt != nil {
		t.Fatalf("incorrect daily data %+v", out)
	}
	result.Error = "Account identity needs review."
	result.Rows = nil
	if err = s.SaveDailyUsageAccount(ctx, *job, result, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	out, err = s.AccountDailyUsage(ctx, account.ID, "2026-11-14", "2026-11-14")
	if err != nil || len(out.Rows) != 1 || out.Days[0].Status != "stale" {
		t.Fatal("failure lost success", out, err)
	}
	result.Error = ""
	if err = s.SaveDailyUsageAccount(ctx, *job, result, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	out, err = s.AccountDailyUsage(ctx, account.ID, "2026-11-14", "2026-11-14")
	if err != nil || len(out.Rows) != 0 || out.Days[0].Status != "complete" || out.Days[0].CollectedAt == nil {
		t.Fatal("confirmed empty day missing", out, err)
	}
	s.DB.Model(&SyncedAccount{}).Where("id = ?", account.ID).Update("identity_key", "new-lifetime")
	if err = s.SaveDailyUsageAccount(ctx, *job, result, now); err == nil {
		t.Fatal("old identity was written")
	}
	out, err = s.AccountDailyUsage(ctx, account.ID, "2026-11-14", "2026-11-14")
	if err != nil || out.Days[0].CollectedAt != nil {
		t.Fatal("old lifetime inherited", out, err)
	}
}
