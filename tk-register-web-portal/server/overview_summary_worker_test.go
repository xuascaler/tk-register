package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"goapi-tk-register/server/internal/core"
)

func TestOverviewWorkerRunsEachViewOnceWithoutOtherSyncLocks(t *testing.T) {
	a, mr, _ := fixture(t)
	admin := addUser(t, a, "summary-admin@example.com", "admin")
	member := addUser(t, a, "summary-member@example.com", "member")
	now := time.Now().UTC()
	date := core.DailyUsageDueDate(now).Format("2006-01-02")
	job := core.DailyUsageJob{VendorID: 1, UsageDate: date, Status: "succeeded", OverviewState: "pending"}
	if err := a.store.DB.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	// Unrelated collection gates must not block local summary generation.
	mr.Set(redisPrefix+"account-sync:lease", "other")
	mr.Set(redisPrefix+"bill-export:lease", "other")
	calls := map[uint64]int{}
	a.overviewSummaryBuilder = func(ctx context.Context, userID uint64, administrative bool, usageDate, batch string, generatedAt time.Time) error {
		if usageDate != date || batch == "" || generatedAt.IsZero() {
			t.Fatal("missing job metadata")
		}
		if administrative && userID != admin.ID {
			t.Fatal("member received administrative view")
		}
		calls[userID]++
		return nil
	}
	for i := 0; i < 2; i++ {
		if err := a.processOverviewSummaries(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if calls[admin.ID] != 2 || calls[member.ID] != 1 {
		t.Fatalf("unexpected builds: %v", calls)
	}
	if err := a.store.DB.First(&job, job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if job.OverviewState != "succeeded" || job.OverviewGeneratedViews != 3 || job.OverviewRunToken != "" {
		t.Fatalf("job: %+v", job)
	}
}

func TestOverviewWorkerDoesNotRetryFailuresOrExpiredRuns(t *testing.T) {
	a, _, _ := fixture(t)
	addUser(t, a, "failed-summary@example.com", "member")
	now := time.Now().UTC()
	expired := now.Add(-time.Minute)
	jobs := []core.DailyUsageJob{
		{VendorID: 1, UsageDate: "2026-10-20", Status: "succeeded", OverviewState: "pending"},
		{VendorID: 1, UsageDate: "2026-10-21", Status: "succeeded", OverviewState: "running", OverviewRunToken: "abandoned", OverviewLeaseUntil: &expired},
		{VendorID: 1, UsageDate: "2026-10-22", Status: "succeeded"},
	}
	if err := a.store.DB.Create(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	calls := 0
	a.overviewSummaryBuilder = func(context.Context, uint64, bool, string, string, time.Time) error {
		calls++
		return errors.New("synthetic failure")
	}
	for i := 0; i < 3; i++ {
		if err := a.processOverviewSummaries(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("failure retried or old job rebuilt: %d", calls)
	}
	var saved []core.DailyUsageJob
	if err := a.store.DB.Order("id").Find(&saved).Error; err != nil {
		t.Fatal(err)
	}
	if saved[0].OverviewState != "failed" || saved[0].OverviewFailedViews != 1 || saved[0].OverviewError == "" || saved[1].OverviewState != "failed" || saved[2].OverviewState != "" {
		t.Fatalf("states: %+v", saved)
	}
}

func TestDailyCompletionQueuesOnlyCurrentDateAndOnlyOnce(t *testing.T) {
	a, _, _ := fixture(t)
	now := time.Now().UTC()
	due := core.DailyUsageDueDate(now)
	jobs := []core.DailyUsageJob{
		{VendorID: 1, UsageDate: due.Format("2006-01-02"), Status: "running", LeaseToken: "current"},
		{VendorID: 1, UsageDate: due.AddDate(0, 0, -1).Format("2006-01-02"), Status: "running", LeaseToken: "historic"},
	}
	if err := a.store.DB.Create(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	for _, job := range jobs {
		if err := a.store.CompleteDailyUsage(context.Background(), job, 0, 0, now); err != nil {
			t.Fatal(err)
		}
	}
	var current, historical core.DailyUsageJob
	a.store.DB.First(&current, jobs[0].ID)
	a.store.DB.First(&historical, jobs[1].ID)
	if current.OverviewState != "pending" || historical.OverviewState != "" {
		t.Fatalf("unexpected queue: %s %s", current.OverviewState, historical.OverviewState)
	}
	a.store.DB.Model(&current).Update("overview_state", "failed")
	// A stale billing worker cannot enqueue a second overview batch.
	_ = a.store.CompleteDailyUsage(context.Background(), jobs[0], 0, 0, now)
	a.store.DB.First(&current, current.ID)
	if current.OverviewState != "failed" {
		t.Fatal("completed summary was queued again")
	}
}
