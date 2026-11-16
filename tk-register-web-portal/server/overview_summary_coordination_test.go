package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goapi-tk-register/server/internal/core"
)

func TestOverviewSummaryConcurrentWorkersShareRedisGate(t *testing.T) {
	a, mr, _ := fixture(t)
	addUser(t, a, "coordinated-summary@example.com", "member")
	now := time.Now().UTC()
	job := core.DailyUsageJob{VendorID: 1, UsageDate: core.DailyUsageDueDate(now).Format("2006-01-02"), Status: "succeeded", OverviewState: "pending"}
	if err := a.store.DB.Create(&job).Error; err != nil {
		t.Fatal(err)
	}

	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	var releaseOnce sync.Once
	a.overviewSummaryBuilder = func(ctx context.Context, _ uint64, _ bool, _, _ string, _ time.Time) error {
		if calls.Add(1) == 1 {
			close(entered)
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// Both app instances share the same database and Redis, as replicas do.
	other := *a
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var firstErr error
	go func() {
		firstErr = a.processOverviewSummaries(ctx)
		close(finished)
	}()
	defer func() {
		releaseOnce.Do(func() { close(release) })
		cancel()
		select {
		case <-finished:
		case <-time.After(10 * time.Second):
			t.Error("summary worker did not stop")
		}
	}()
	select {
	case <-entered:
	case <-finished:
		t.Fatalf("worker ended before generation: %v", firstErr)
	case <-ctx.Done():
		t.Fatal("worker never entered generation")
	}
	var running core.DailyUsageJob
	if err := a.store.DB.WithContext(ctx).First(&running, job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if running.OverviewState != "running" || running.OverviewRunToken == "" || !mr.Exists(redisPrefix+"overview-summary:lease") {
		t.Fatal("first worker did not hold its claim and Redis gate")
	}
	if err := other.processOverviewSummaries(ctx); err != nil {
		t.Fatal(err)
	}
	var after core.DailyUsageJob
	if err := a.store.DB.WithContext(ctx).First(&after, job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || after.OverviewState != "running" || after.OverviewRunToken != running.OverviewRunToken {
		t.Fatal("concurrent worker entered generation or replaced the running claim")
	}

	releaseOnce.Do(func() { close(release) })
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("first worker did not finish")
	}
	if firstErr != nil {
		t.Fatal(firstErr)
	}
	if err := a.store.DB.First(&after, job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.OverviewState != "succeeded" || after.OverviewGeneratedViews != 1 || mr.Exists(redisPrefix+"overview-summary:lease") {
		t.Fatal("completed worker retained its gate or did not finish the claim")
	}
	if err := other.processOverviewSummaries(ctx); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("completed summary was generated again")
	}
}

func TestOverviewSummaryFailedViewDoesNotBlockOtherUsers(t *testing.T) {
	a, _, _ := fixture(t)
	failedUser := addUser(t, a, "summary-first-fails@example.com", "member")
	successfulUser := addUser(t, a, "summary-second-succeeds@example.com", "member")
	now := time.Now().UTC()
	due := core.DailyUsageDueDate(now)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := a.store.BuildOverviewSummaries(ctx, failedUser.ID, false, due.AddDate(0, 0, -1).Format("2006-01-02"), "previous-success", now); err != nil {
		t.Fatal(err)
	}
	job := core.DailyUsageJob{VendorID: 1, UsageDate: due.Format("2006-01-02"), Status: "succeeded", OverviewState: "pending"}
	if err := a.store.DB.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	calls := map[uint64]int{}
	a.overviewSummaryBuilder = func(ctx context.Context, userID uint64, administrative bool, date, batch string, generatedAt time.Time) error {
		calls[userID]++
		if userID == failedUser.ID {
			return errors.New("synthetic first-user failure")
		}
		var claimed core.DailyUsageJob
		if err := a.store.DB.WithContext(ctx).First(&claimed, job.ID).Error; err != nil {
			return err
		}
		return a.store.BuildOverviewSummaries(ctx, userID, administrative, date, batch, generatedAt, core.OverviewSummaryClaim{JobID: job.ID, Token: claimed.OverviewRunToken})
	}
	for i := 0; i < 2; i++ {
		if err := a.processOverviewSummaries(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if calls[failedUser.ID] != 1 || calls[successfulUser.ID] != 1 {
		t.Fatalf("failure stopped later users or was retried: %v", calls)
	}
	if err := a.store.DB.First(&job, job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if job.OverviewState != "failed" || job.OverviewFailedViews != 1 || job.OverviewGeneratedViews != 1 || job.OverviewError == "" {
		t.Fatalf("unexpected batch outcome: %+v", job)
	}
	var retained, published []core.OverviewSummary
	if err := a.store.DB.Where("user_id = ?", failedUser.ID).Find(&retained).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.store.DB.Where("user_id = ?", successfulUser.ID).Find(&published).Error; err != nil {
		t.Fatal(err)
	}
	if len(retained) != 4 || len(published) != 4 {
		t.Fatal("failure removed saved periods or prevented the later user's four periods")
	}
	for _, row := range retained {
		if row.BatchID != "previous-success" || row.EndDate != due.AddDate(0, 0, -1).Format("2006-01-02") {
			t.Fatal("failed view replaced its previous summary")
		}
	}
	for _, row := range published {
		if row.EndDate != job.UsageDate || row.BatchID == "previous-success" {
			t.Fatal("later user's summary was not published")
		}
	}
}
