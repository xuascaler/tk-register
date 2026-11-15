package main

import (
	"context"
	"goapi-tk-register/server/internal/core"
	"net/http"
	"time"
)

func (a *app) accountsOverview(w http.ResponseWriter, r *http.Request, userID uint64) {
	if r.Method != http.MethodGet {
		fail(w, 405, "METHOD_NOT_ALLOWED", "This method is not supported.")
		return
	}
	q := r.URL.Query()
	if q.Get("range") != "" && q.Get("range") != "all" {
		fail(w, 400, "INVALID_RANGE", "Choose a date range or All time.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	data, err := a.store.AccountsOverview(ctx, userID, q.Get("sources"), q.Get("start_date"), q.Get("end_date"), q.Get("range") == "all", time.Now())
	if core.IsValidationError(err) {
		fail(w, 400, "INVALID_RANGE", err.Error())
	} else if err != nil {
		vendorFailure(w, err)
	} else {
		ok(w, data)
	}
}
