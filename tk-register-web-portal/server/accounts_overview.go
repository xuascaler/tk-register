package main

import (
	"context"
	"net/http"
	"strings"
	"time"

	"goapi-tk-register/server/internal/core"
)

func (a *app) serveAccountsOverview(w http.ResponseWriter, r *http.Request, viewer *core.User, administrative, rawAccount bool) {
	if r.Method != http.MethodGet {
		fail(w, 405, "METHOD_NOT_ALLOWED", "This method is not supported.")
		return
	}
	q := r.URL.Query()
	period, start, end := q.Get("period"), q.Get("start_date"), q.Get("end_date")
	view, modelCostsEnabled, err := a.modelUsageView(r.Context(), viewer, administrative, rawAccount)
	if err != nil {
		internal(w)
		return
	}
	if !administrative {
		if q.Has("start_date") || q.Has("end_date") {
			fail(w, 400, "INVALID_RANGE", "Choose day, week, month, or all_time.")
			return
		}
		if !modelCostsEnabled && q.Has("models") {
			fail(w, 400, "MODEL_DETAILS_DISABLED", "Model filtering is disabled for personal account pages.")
			return
		}
	}
	allTime := q.Get("range") == "all"
	if q.Get("range") != "" && !allTime {
		fail(w, 400, "INVALID_RANGE", "Choose a date range or All time.")
		return
	}
	if period != "" && ((start != "" || end != "") || (allTime && period != "all_time")) {
		fail(w, 400, "INVALID_RANGE", "Choose a preset or a custom date range.")
		return
	}
	if period == "" && start == "" && end == "" {
		period = "week"
		if allTime || !administrative {
			period = "all_time"
		}
	}
	if period != "" && period != "day" && period != "week" && period != "month" && period != "all_time" {
		fail(w, 400, "INVALID_RANGE", "Choose day, week, month, or all_time.")
		return
	}
	models, valid := overviewModels(q.Get("models"))
	if !valid {
		fail(w, 400, "INVALID_MODELS", "Choose valid models or all models.")
		return
	}
	now := time.Now()
	ctx, cancel := context.WithTimeout(view, 15*time.Second)
	defer cancel()
	var result core.AccountsOverview
	allSources := q.Get("sources") == "" || q.Get("sources") == "all"
	if allSources && period != "" {
		// A missing or stale summary is returned as such. Reading never schedules
		// generation or falls back to scanning the underlying billing tables.
		result, err = a.store.ReadOverviewSummary(ctx, viewer.ID, administrative, period, now)
	} else {
		if period != "" {
			zone := time.FixedZone("Asia/Shanghai", 8*60*60)
			yesterday := now.In(zone).AddDate(0, 0, -1)
			end = yesterday.Format("2006-01-02")
			days := 1
			if period == "week" {
				days = 7
			}
			if period == "month" {
				days = 30
			}
			start = yesterday.AddDate(0, 0, 1-days).Format("2006-01-02")
			allTime = period == "all_time"
		}
		query := a.store.AccountsOverview
		if administrative {
			query = a.store.AdminAccountsOverview
		}
		result, err = query(ctx, viewer.ID, q.Get("sources"), start, end, allTime, now)
	}
	if err == nil {
		err = core.ProjectOverviewBilling(&result)
	}
	if core.IsValidationError(err) {
		fail(w, 400, "INVALID_RANGE", err.Error())
	} else if err != nil {
		vendorFailure(w, err)
	} else {
		filterOverviewModels(&result, models)
		if !viewer.IsSuperAdmin {
			result.ModelUsageSource = ""
		}
		if administrative {
			ok(w, result)
			return
		}
		personal, err := personalOverview(result, modelCostsEnabled)
		if err != nil {
			internal(w)
			return
		}
		ok(w, personal)
	}
}

// nil means every model; a non-nil empty selection means none. Models affect
// period billing totals only, never account counts or statuses.
func overviewModels(raw string) (map[string]bool, bool) {
	if raw == "" || raw == "all" {
		return nil, true
	}
	if raw == "none" {
		return map[string]bool{}, true
	}
	if len(raw) > 8192 {
		return nil, false
	}
	out := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		model := strings.TrimSpace(part)
		if model == "" || model == "all" || model == "none" || len(model) > 255 {
			return nil, false
		}
		out[model] = true
	}
	return out, len(out) <= 100
}

func filterOverviewModels(out *core.AccountsOverview, models map[string]bool) {
	core.FilterOverviewBillingModels(out, models)
}

func (a *app) adminOverviewSources(w http.ResponseWriter, r *http.Request, userID uint64) {
	if r.Method != http.MethodGet {
		fail(w, 405, "METHOD_NOT_ALLOWED", "This method is not supported.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	sources, err := a.store.AdminOverviewSources(ctx, userID)
	if err != nil {
		vendorFailure(w, err)
		return
	}
	ok(w, map[string]any{"sources": sources})
}
