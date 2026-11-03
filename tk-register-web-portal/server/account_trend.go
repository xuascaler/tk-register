package main

import (
	"net/http"
	"strconv"

	"goapi-tk-register/server/internal/core"
)

// accountSyncRoutes has already enforced owner/admin access before calling here.
func (a *app) accountUsageData(w http.ResponseWriter, r *http.Request, account core.AccountDTO, id uint64, observationsOnly bool) {
	days := 7
	var err error
	if raw := r.URL.Query().Get("days"); raw != "" {
		days, err = strconv.Atoi(raw)
	}
	if err != nil || (days != 7 && days != 30 && days != 90) {
		fail(w, 400, "INVALID_RANGE", "Choose 7, 30, or 90 days.")
		return
	}
	page, size := 1, 50
	if observationsOnly {
		if raw := r.URL.Query().Get("page"); raw != "" {
			page, err = strconv.Atoi(raw)
		}
		if err == nil {
			if raw := r.URL.Query().Get("page_size"); raw != "" {
				size, err = strconv.Atoi(raw)
			}
		}
		if err != nil || page < 1 || page > 100000 || size < 1 || size > 100 {
			fail(w, 400, "INVALID_PAGE", "Use page 1–100000 and page_size 1–100.")
			return
		}
	}
	observations, err := a.store.AccountSnapshotPage(r.Context(), id, days, page, size)
	if err != nil {
		internal(w)
		return
	}
	if observationsOnly {
		ok(w, observations)
		return
	}
	values, err := a.store.Settings(r.Context())
	if err != nil {
		internal(w)
		return
	}
	interval := values.Int("account_sync_interval_seconds")
	trend, err := a.store.AccountTrend(r.Context(), id, days, interval)
	if err != nil {
		internal(w)
		return
	}
	ok(w, map[string]any{
		"account": account, "interval_seconds": interval, "timezone": values.String("account_sync_timezone"),
		"trend": trend.Trend, "trend_reduced": trend.Reduced, "snapshot_count": observations.Total,
		"snapshots": observations.Items, "snapshot_page": observations.Page, "snapshot_page_size": observations.PageSize,
	})
}
