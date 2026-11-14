package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"goapi-tk-register/server/internal/billing"
	"goapi-tk-register/server/internal/core"
	"goapi-tk-register/server/internal/diagnostics"
	"goapi-tk-register/server/internal/supplier"
)

type billExporter interface {
	ExportClaudeBills(context.Context, string, string, string, string) ([]byte, error)
}

func (a *app) billExportClient() billExporter {
	if a.billExporter != nil {
		return a.billExporter
	}
	if a.supplierClient != nil {
		return a.supplierClient
	}
	return supplier.NewClient()
}

func billExportFilename(job core.BillExportJob) string {
	email := strings.Map(func(r rune) rune {
		if r < 32 || r == 127 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, strings.TrimSpace(job.UserEmail))
	if email == "" {
		email = "user-" + strconv.FormatUint(job.TargetUserID, 10)
	}
	return fmt.Sprintf("claude-bills_%s_%s_to_%s.xlsx", email, job.StartDate, job.EndDate)
}

func billJobDTO(job core.BillExportJob) map[string]any {
	if job.Status == "completed" && !job.ExpiresAt.After(time.Now()) {
		job.Status = "expired"
	}
	return map[string]any{
		"id": strconv.FormatUint(job.ID, 10), "status": job.Status,
		"download_filename": billExportFilename(job),
		"user":              map[string]string{"id": strconv.FormatUint(job.TargetUserID, 10), "email": job.UserEmail},
		"start_date":        job.StartDate, "end_date": job.EndDate, "created_at": job.CreatedAt, "finished_at": job.FinishedAt, "expires_at": job.ExpiresAt,
		"account_count": job.AccountCount, "matched_account_count": job.MatchedAccountCount, "review_account_count": job.ReviewAccountCount,
		"row_count": job.RowCount, "total_cny": job.TotalCNY, "currency": "CNY", "error": job.ErrorMessage,
	}
}

func (a *app) billExportRoutes(w http.ResponseWriter, r *http.Request, route string) bool {
	const base = "admin/bill-exports"
	if route != base && !strings.HasPrefix(route, base+"/") {
		return false
	}
	actor, _, valid := a.authenticated(w, r, r.Method != "GET")
	if !valid {
		return true
	}
	if actor.Role != "admin" {
		fail(w, 403, "FORBIDDEN", "Administrator access is required.")
		return true
	}
	if route == base {
		switch r.Method {
		case "GET":
			jobs, err := a.store.RecentBillExports(r.Context())
			if err != nil {
				internal(w)
				return true
			}
			items := make([]map[string]any, 0, len(jobs))
			for _, job := range jobs {
				items = append(items, billJobDTO(job))
			}
			ok(w, map[string]any{"items": items})
		case "POST":
			var input struct {
				UserID    string `json:"user_id"`
				StartDate string `json:"start_date"`
				EndDate   string `json:"end_date"`
			}
			if !decode(w, r, &input) {
				return true
			}
			userID, err := core.ParseBusinessID(input.UserID)
			if err != nil {
				fail(w, 400, "INVALID_REQUEST", "Choose a user.")
				return true
			}
			retry, err := loginAttempt.Run(r.Context(), a.redis, []string{redisPrefix + "bill-export:create:" + strconv.FormatUint(actor.ID, 10)}, 5, 60).Int()
			if err != nil {
				internal(w)
				return true
			}
			if retry > 0 {
				rateFailure(w, retry)
				return true
			}
			job, err := a.store.CreateBillExport(r.Context(), actor.ID, userID, input.StartDate, input.EndDate, time.Now().UTC())
			if core.IsValidationError(err) {
				fail(w, 400, "INVALID_REQUEST", err.Error())
			} else if err != nil {
				vendorFailure(w, err)
			} else {
				ok(w, billJobDTO(job))
			}
		default:
			fail(w, 405, "METHOD_NOT_ALLOWED", "This method is not supported.")
		}
		return true
	}
	parts := strings.Split(strings.TrimPrefix(route, base+"/"), "/")
	if len(parts) > 2 || r.Method != "GET" {
		fail(w, 405, "METHOD_NOT_ALLOWED", "This method is not supported.")
		return true
	}
	id, err := core.ParseBusinessID(parts[0])
	if err != nil {
		fail(w, 404, "NOT_FOUND", "This export does not exist.")
		return true
	}
	job, err := a.store.BillExport(r.Context(), id)
	if errors.Is(err, core.ErrNotFound) {
		fail(w, 404, "NOT_FOUND", "This export does not exist.")
		return true
	}
	if err != nil {
		internal(w)
		return true
	}
	if len(parts) == 1 {
		ok(w, billJobDTO(job))
		return true
	}
	if parts[1] != "results" && parts[1] != "download" {
		fail(w, 404, "NOT_FOUND", "This endpoint does not exist.")
		return true
	}
	if !job.ExpiresAt.After(time.Now()) {
		fail(w, 410, "EXPORT_EXPIRED", "This export has expired. Generate a new export.")
		return true
	}
	if job.Status != "completed" {
		fail(w, 409, "EXPORT_NOT_READY", "The export is not ready to download.")
		return true
	}
	job, err = a.store.BillExportData(r.Context(), id)
	if err != nil {
		internal(w)
		return true
	}
	if parts[1] == "download" {
		if len(job.Artifact) == 0 {
			internal(w)
			return true
		}
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": billExportFilename(job)}))
		w.Header().Set("Content-Length", strconv.Itoa(len(job.Artifact)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(job.Artifact)
		return true
	}
	page, good := adminPage(w, r)
	if !good {
		return true
	}
	var report billing.Report
	if json.Unmarshal([]byte(job.ReportJSON), &report) != nil {
		internal(w)
		return true
	}
	const size = 50
	start := (page - 1) * size
	result := map[string]any{"page": page, "page_size": size}
	switch r.URL.Query().Get("view") {
	case "", "summary":
		rows := report.Summary
		result["total"] = len(rows)
		if start > len(rows) {
			start = len(rows)
		}
		result["items"] = rows[start:min(start+size, len(rows))]
	case "details":
		rows := report.Details
		result["total"] = len(rows)
		result["columns"] = report.Columns
		if start > len(rows) {
			start = len(rows)
		}
		result["items"] = rows[start:min(start+size, len(rows))]
	case "review":
		rows := report.Review
		result["total"] = len(rows)
		if start > len(rows) {
			start = len(rows)
		}
		result["items"] = rows[start:min(start+size, len(rows))]
	default:
		fail(w, 400, "INVALID_FILTER", "Choose a valid result view.")
		return true
	}
	ok(w, result)
	return true
}

// A durable database lease fences worker completion. The Redis gate serializes
// exports across replicas; it is separate from account observation work because
// export does not mutate upstream accounts or local account snapshots.
func (a *app) processBillExport(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, 3*time.Minute)
	defer cancel()
	token, err := randomToken()
	if err != nil {
		return err
	}
	const key = redisPrefix + "bill-export:lease"
	locked, err := a.redis.SetNX(ctx, key, token, 4*time.Minute).Result()
	if err != nil || !locked {
		return err
	}
	defer a.releaseDeletionLease(key, token)
	now := time.Now().UTC()
	job, err := a.store.ClaimBillExport(ctx, now, now.Add(4*time.Minute), token)
	if err != nil || job == nil {
		return err
	}
	ctx = diagnostics.WithTrace(ctx, diagnostics.Trace{Source: "bill_export", Vendor: "tk-register-0", ActorID: job.RequestedBy, JobID: job.ID})
	finishFailure := func(code, message string) error {
		save, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		return a.store.FailBillExport(save, job.ID, token, code, message, time.Now().UTC())
	}
	// Deferral is bounded by total job age; an unavailable supplier cannot leave
	// an administrator's export pending forever.
	if now.Sub(job.CreatedAt) > 30*time.Minute {
		return finishFailure("EXPORT_EXPIRED", "The supplier did not become available in time. Generate a new export.")
	}
	settings, err := a.store.Settings(ctx)
	if err != nil {
		return finishFailure("CONFIGURATION", "Export configuration could not be read.")
	}
	username, password := settings.String("account_sync_website_username"), settings.String("account_sync_website_password")
	if username == "" || password == "" {
		return finishFailure("NOT_CONFIGURED", "Configure the vendor website login in Account sync before exporting.")
	}
	raw, err := a.billExportClient().ExportClaudeBills(ctx, username, password, job.StartDate, job.EndDate)
	if err != nil {
		seconds, at, _, _ := supplier.RetryDetails(err)
		if seconds > 0 {
			if at.IsZero() {
				at = time.Now().UTC().Add(time.Duration(seconds) * time.Second)
			}
			save, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			return a.store.DeferBillExport(save, job.ID, token, at, "Waiting for the supplier rate limit to reset.")
		}
		return finishFailure("UPSTREAM_EXPORT_FAILED", "The supplier bill export failed. Please try again later.")
	}
	if err = a.store.SetBillExportStage(ctx, job.ID, token, "filtering"); err != nil {
		return err
	}
	selection, err := a.store.BillExportAccounts(ctx, job.ID)
	if err != nil {
		return finishFailure("SELECTION_UNAVAILABLE", "The saved account selection could not be read.")
	}
	accounts := make([]billing.Account, 0, len(selection))
	for _, row := range selection {
		accounts = append(accounts, billing.Account{LocalAccountID: strconv.FormatUint(row.LocalAccountID, 10), Account: row.AccountEmail, Plan: row.Plan, PlatformAccountID: billString(row.PlatformAccountID), SupplierRecordID: billString(row.SupplierRecordID), RemoteID: billString(row.RemoteID), IdentityKey: row.IdentityKey, LatestSubmittedAt: row.LatestSubmittedAt})
	}
	report, err := billing.Filter(raw, accounts)
	raw = nil
	if err != nil {
		return finishFailure("INVALID_BILL_FORMAT", "The supplier bill format could not be verified. No incomplete report was generated.")
	}
	report.UserEmail, report.StartDate, report.EndDate = job.UserEmail, job.StartDate, job.EndDate
	artifact, err := billing.Workbook(report)
	if err != nil {
		return finishFailure("REPORT_FAILED", "The filtered Excel report could not be generated.")
	}
	reportJSON, err := json.Marshal(report)
	if err != nil {
		return finishFailure("REPORT_FAILED", "The filtered report could not be saved.")
	}
	matched := 0
	for _, row := range report.Summary {
		if row.Status == "matched" {
			matched++
		}
	}
	save, stop := context.WithTimeout(context.Background(), 15*time.Second)
	defer stop()
	err = a.store.CompleteBillExport(save, job.ID, token, core.BillExportResult{ReportJSON: string(reportJSON), Artifact: artifact, MatchedAccountCount: matched, ReviewAccountCount: len(report.Review), RowCount: len(report.Details), TotalCNY: report.TotalCNY}, time.Now().UTC())
	if err != nil {
		return finishFailure("REPORT_SAVE_FAILED", "The report could not be saved. It may exceed the supported size; select a shorter date range.")
	}
	return nil
}
func billString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
func (a *app) runBillExports(ctx context.Context) {
	nextCleanup := time.Time{}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !time.Now().Before(nextCleanup) {
				cleanup, stop := context.WithTimeout(ctx, 10*time.Second)
				err := a.store.ExpireBillExports(cleanup, time.Now().UTC())
				stop()
				if err != nil {
					log.Print("Bill export retention cleanup deferred")
				}
				nextCleanup = time.Now().Add(time.Hour)
			}
			if err := a.processBillExport(ctx); err != nil && ctx.Err() == nil {
				log.Print("Bill export worker could not complete; saved job will be recovered")
			}
		}
	}
}
