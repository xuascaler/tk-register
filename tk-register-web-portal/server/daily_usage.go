package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"goapi-tk-register/server/internal/billing"
	"goapi-tk-register/server/internal/core"
	"goapi-tk-register/server/internal/diagnostics"
	"goapi-tk-register/server/internal/supplier"
)

func (a *app) dailyUsageData(w http.ResponseWriter, r *http.Request, id uint64, administrative bool) {
	modelCostsEnabled := true
	if !administrative {
		var err error
		modelCostsEnabled, err = a.userModelCostsEnabled(r.Context())
		if err != nil {
			internal(w)
			return
		}
		if !modelCostsEnabled && r.URL.Query().Has("models") {
			fail(w, 400, "MODEL_DETAILS_DISABLED", "Model filtering is disabled for personal account pages.")
			return
		}
	}
	start, end := r.URL.Query().Get("start_date"), r.URL.Query().Get("end_date")
	now := time.Now().In(time.FixedZone("Asia/Shanghai", 8*60*60))
	if start == "" && end == "" {
		end = now.AddDate(0, 0, -1).Format("2006-01-02")
		start = now.AddDate(0, 0, -7).Format("2006-01-02")
	}
	if err := core.ValidateDailyUsageRange(start, end, now); err != nil {
		fail(w, 400, "INVALID_DATE_RANGE", err.Error())
		return
	}
	data, err := a.store.AccountDailyUsage(r.Context(), id, start, end)
	if err != nil {
		internal(w)
		return
	}
	if administrative {
		ok(w, data)
		return
	}
	personal, err := personalDailyUsage(data, modelCostsEnabled)
	if err != nil {
		internal(w)
		return
	}
	ok(w, personal)
}

var errDailyExportBusy = errors.New("daily usage export lease is busy")

func (a *app) processDailyUsage(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, 3*time.Minute)
	defer cancel()
	settings, err := a.store.Settings(ctx)
	if err != nil {
		return err
	}
	username, password := settings.String("account_sync_website_username"), settings.String("account_sync_website_password")
	if !settings.Bool("account_sync_enabled") || username == "" || password == "" {
		return nil
	}
	// Share only the export lease. Account Sync has its own lock; both clients
	// share the existing encrypted login token and upstream cooldown state.
	token, err := randomToken()
	if err != nil {
		return err
	}
	const key = redisPrefix + "bill-export:lease"
	locked, err := a.redis.SetNX(ctx, key, token, 4*time.Minute).Result()
	if err != nil {
		return err
	}
	if !locked {
		return errDailyExportBusy
	}
	defer a.releaseDeletionLease(key, token)
	now := time.Now().UTC()
	if err = a.store.EnsureDailyUsageJobs(ctx, now); err != nil {
		return err
	}
	job, err := a.store.ClaimDailyUsageJob(ctx, now, token)
	if err != nil || job == nil {
		return err
	}
	ctx = diagnostics.WithTrace(ctx, diagnostics.Trace{Source: "daily_usage", Vendor: "tk-register-0", JobID: job.ID})
	log.Printf("tk-register daily_usage event=claimed date=%s attempt=%d", job.UsageDate, job.Attempts)
	retry := func(message string, upstream error) error {
		next := time.Now().UTC().Add(time.Duration(min(60, 5*job.Attempts)) * time.Minute)
		seconds, at, _, _ := supplier.RetryDetails(upstream)
		cooldown := seconds > 0
		if cooldown {
			if at.IsZero() {
				at = time.Now().UTC().Add(time.Duration(seconds) * time.Second)
			}
			next = at
			message = "Waiting for the supplier rate limit to reset."
		}
		save, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		log.Printf("tk-register daily_usage event=deferred date=%s cooldown=%t next_run_at=%s reason=%s", job.UsageDate, cooldown, next.Format(time.RFC3339), message)
		return a.store.DeferDailyUsage(save, *job, next, message, cooldown)
	}
	accounts, err := a.store.DailyUsageTargets(ctx, job.VendorID)
	if err != nil {
		return retry("Account selection could not be loaded. Saved daily data is retained.", err)
	}
	if len(accounts) == 0 {
		return a.store.CompleteDailyUsage(ctx, *job, 0, 0, now)
	}
	raw, err := a.billExportClient().ExportClaudeBills(ctx, username, password, job.UsageDate, job.UsageDate)
	if err != nil {
		return retry("The supplier daily bill export failed. Saved daily data is retained.", err)
	}
	results, err := billing.Daily(raw, accounts)
	raw = nil
	if err != nil {
		return retry("The supplier daily bill format is unsupported. Saved daily data is retained.", err)
	}
	review := 0
	for _, result := range results {
		if result.Error != "" {
			review++
		}
		if err = a.store.SaveDailyUsageAccount(ctx, *job, result, time.Now().UTC()); errors.Is(err, core.ErrNotFound) {
			continue
		} else if err != nil {
			return retry("Daily bill storage was interrupted. Saved data is retained.", err)
		}
	}
	err = a.store.CompleteDailyUsage(ctx, *job, len(results), review, time.Now().UTC())
	if err == nil {
		log.Printf("tk-register daily_usage date=%s accounts=%d review=%d duration_ms=%d", job.UsageDate, len(results), review, time.Since(now).Milliseconds())
	}
	return err
}
func dailyUsageDelay(err error) time.Duration {
	if errors.Is(err, errDailyExportBusy) {
		return time.Second
	}
	return 30 * time.Second
}
func (a *app) runDailyUsage(ctx context.Context) {
	log.Print("tk-register daily_usage worker_started")
	for {
		if ctx.Err() != nil {
			return
		}
		err := a.processDailyUsage(ctx)
		if err != nil && !errors.Is(err, errDailyExportBusy) && ctx.Err() == nil {
			log.Print("tk-register daily usage collection could not complete; durable work is retained")
		}
		// Retry a busy export gate off the administrator worker's two-second
		// cadence. A fixed 30-second ticker can repeatedly lose the same race.
		timer := time.NewTimer(dailyUsageDelay(err))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
