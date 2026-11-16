package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"goapi-tk-register/server/internal/core"
)

const overviewSummaryLease = 35 * time.Minute

// This worker uses its own gate, never the account-sync or bill-export gate.
// A claimed batch is never reclaimed: failures wait for the next daily batch.
func (a *app) processOverviewSummaries(parent context.Context) error {
	now := time.Now().UTC()
	if err := a.store.DB.WithContext(parent).Model(&core.DailyUsageJob{}).
		Where("overview_state = ? AND overview_lease_until < ?", "running", now).
		Updates(map[string]any{"overview_state": "failed", "overview_error": "Summary generation was interrupted; no retry is scheduled.", "overview_finished_at": now, "overview_run_token": "", "overview_lease_until": nil}).Error; err != nil {
		return err
	}
	token, err := randomToken()
	if err != nil {
		return err
	}
	key := redisPrefix + "overview-summary:lease"
	locked, err := a.redis.SetNX(parent, key, token, overviewSummaryLease).Result()
	if err != nil || !locked {
		return err
	}
	defer a.releaseDeletionLease(key, token)
	var job core.DailyUsageJob
	err = a.store.DB.WithContext(parent).Where("status = ? AND overview_state = ?", "succeeded", "pending").Order("usage_date ASC, id ASC").First(&job).Error
	if errors.Is(err, core.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	claim := a.store.DB.WithContext(parent).Model(&core.DailyUsageJob{}).Where("id = ? AND status = ? AND overview_state = ?", job.ID, "succeeded", "pending").
		Updates(map[string]any{"overview_state": "running", "overview_started_at": now, "overview_run_token": token, "overview_lease_until": now.Add(overviewSummaryLease), "overview_error": ""})
	if claim.Error != nil || claim.RowsAffected != 1 {
		return claim.Error
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Minute)
	defer cancel()
	generated, failed := 0, 0
	lastFailure := ""
	finish := func(message string) error {
		status := "succeeded"
		if message != "" || failed > 0 {
			status = "failed"
		}
		save, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		result := a.store.DB.WithContext(save).Model(&core.DailyUsageJob{}).Where("id = ? AND overview_state = ? AND overview_run_token = ?", job.ID, "running", token).
			Updates(map[string]any{"overview_state": status, "overview_error": message, "overview_finished_at": time.Now().UTC(), "overview_run_token": "", "overview_lease_until": nil, "overview_generated_views": generated, "overview_failed_views": failed})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("overview summary claim was lost before completion")
		}
		return nil
	}
	log.Printf("tk-register overview_summary event=started job_id=%d date=%s", job.ID, job.UsageDate)
	var users []core.User
	if err = a.store.DB.WithContext(ctx).Select("id", "role").Order("id ASC").Find(&users).Error; err != nil {
		return finish("Could not load users; saved summaries are retained. No retry is scheduled.")
	}
	batch := fmt.Sprintf("daily-%d-%s", job.ID, token)
	for _, user := range users {
		views := []bool{false}
		if user.Role == "admin" {
			views = append(views, true)
		}
		for _, admin := range views {
			if ctx.Err() != nil {
				return finish("Summary generation was interrupted; saved summaries are retained. No retry is scheduled.")
			}
			build := func(ctx context.Context, userID uint64, administrative bool, usageDate, batchID string, generatedAt time.Time) error {
				return a.store.BuildOverviewSummaries(ctx, userID, administrative, usageDate, batchID, generatedAt, core.OverviewSummaryClaim{JobID: job.ID, Token: token})
			}
			if a.overviewSummaryBuilder != nil {
				build = a.overviewSummaryBuilder
			}
			// Bound each view separately so one user's failure does not prevent all
			// subsequent users from receiving their scheduled summary.
			viewCtx, stop := context.WithTimeout(ctx, 90*time.Second)
			err = build(viewCtx, user.ID, admin, job.UsageDate, batch, time.Now().UTC())
			stop()
			if err != nil {
				failed++
				lastFailure = overviewFailureReason(err)
				log.Printf("tk-register overview_summary event=failed job_id=%d user_id=%d admin=%t reason=%s error_type=%T retry=false", job.ID, user.ID, admin, overviewFailureReason(err), err)
			} else {
				generated++
			}
		}
	}
	message := ""
	if failed > 0 {
		message = fmt.Sprintf("%d view(s) failed (%s); previous summaries retained. No retry scheduled.", failed, lastFailure)
	}
	err = finish(message)
	if err == nil {
		log.Printf("tk-register overview_summary event=finished job_id=%d generated=%d failed=%d duration_ms=%d", job.ID, generated, failed, time.Since(now).Milliseconds())
	}
	return err
}

func (a *app) runOverviewSummaries(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		if err := a.processOverviewSummaries(ctx); err != nil && ctx.Err() == nil {
			log.Print("tk-register overview_summary worker could not inspect scheduled work")
		}
		timer := time.NewTimer(30 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func overviewFailureReason(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	if errors.Is(err, context.Canceled) {
		return "context_canceled"
	}
	if errors.Is(err, core.ErrNotFound) {
		return "claim_or_user_not_found"
	}
	return "summary_storage_or_aggregation_failed"
}
