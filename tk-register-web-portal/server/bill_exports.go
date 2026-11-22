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
	users := billExportSelectedUsers(job)
	if len(users) > 1 {
		return fmt.Sprintf("claude-bills_%d-users_%d_%s_to_%s.xlsx", len(users), job.ID, job.StartDate, job.EndDate)
	}
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

// Older jobs predate the selected-user snapshot table and have one target user.
func billExportSelectedUsers(job core.BillExportJob) []map[string]string {
	users := make([]map[string]string, 0, len(job.Users))
	for _, user := range job.Users {
		users = append(users, map[string]string{"id": strconv.FormatUint(user.UserID, 10), "email": user.Email})
	}
	if len(users) == 0 {
		users = append(users, map[string]string{"id": strconv.FormatUint(job.TargetUserID, 10), "email": job.UserEmail})
	}
	return users
}

func billJobDTO(job core.BillExportJob, includeSource bool) map[string]any {
	if job.Status == "completed" && !job.ExpiresAt.After(time.Now()) {
		job.Status = "expired"
	}
	summaryCount := job.AccountCount
	if job.UsageSource == "modified" {
		summaryCount = job.MatchedAccountCount + job.ReviewAccountCount
	}
	users := billExportSelectedUsers(job)
	result := map[string]any{
		"id": strconv.FormatUint(job.ID, 10), "status": job.Status,
		"download_filename": billExportFilename(job),
		"users":             users,
		"include_sub_users": job.IncludeSubUsers,
		"start_date":        job.StartDate, "end_date": job.EndDate, "created_at": job.CreatedAt, "finished_at": job.FinishedAt, "expires_at": job.ExpiresAt,
		"account_count": job.AccountCount, "matched_account_count": job.MatchedAccountCount, "review_account_count": job.ReviewAccountCount,
		"summary_count": summaryCount, "row_count": job.RowCount, "total_cny": job.TotalCNY, "currency": "CNY", "error": job.ErrorMessage,
	}
	if len(users) == 1 {
		result["user"] = users[0]
	}
	if includeSource {
		source := job.UsageSource
		if source == "modified" {
			source = "cache"
		}
		result["model_usage_source"] = source
	}
	return result
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
			jobs, err := a.store.RecentBillExports(r.Context(), actor.ID)
			if err != nil {
				internal(w)
				return true
			}
			items := make([]map[string]any, 0, len(jobs))
			for _, job := range jobs {
				items = append(items, billJobDTO(job, actor.IsSuperAdmin))
			}
			ok(w, map[string]any{"items": items})
		case "POST":
			var input struct {
				UserID          string   `json:"user_id"`
				UserIDs         []string `json:"user_ids"`
				IncludeSubUsers bool     `json:"include_sub_users"`
				UsageSource     string   `json:"model_usage_source"`
				StartDate       string   `json:"start_date"`
				EndDate         string   `json:"end_date"`
			}
			if !decode(w, r, &input) {
				return true
			}
			if input.UserIDs != nil && input.UserID != "" {
				fail(w, 400, "INVALID_REQUEST", "Use user_ids or user_id, not both.")
				return true
			}
			ids := input.UserIDs
			if ids == nil && input.UserID != "" {
				ids = []string{input.UserID}
			}
			userIDs := make([]uint64, 0, len(ids))
			seen := make(map[uint64]bool)
			for _, value := range ids {
				id, err := core.ParseBusinessID(value)
				if err != nil {
					fail(w, 400, "INVALID_REQUEST", "Choose valid users.")
					return true
				}
				if !seen[id] {
					seen[id] = true
					userIDs = append(userIDs, id)
				}
			}
			if len(userIDs) == 0 || len(userIDs) > core.MaxBillExportUsers {
				fail(w, 400, "INVALID_REQUEST", "Choose between 1 and 20 users.")
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
			job, err := a.store.CreateBillExportForUsers(r.Context(), actor.ID, userIDs, input.StartDate, input.EndDate, time.Now().UTC(), core.BillExportOptions{IncludeSubUsers: input.IncludeSubUsers, UsageSource: input.UsageSource})
			if core.IsValidationError(err) {
				fail(w, 400, "INVALID_REQUEST", err.Error())
			} else if err != nil {
				vendorFailure(w, err)
			} else {
				ok(w, billJobDTO(job, actor.IsSuperAdmin))
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
	job, err := a.store.BillExport(r.Context(), actor.ID, id)
	if errors.Is(err, core.ErrNotFound) {
		fail(w, 404, "NOT_FOUND", "This export does not exist.")
		return true
	}
	if err != nil {
		internal(w)
		return true
	}
	if len(parts) == 1 {
		ok(w, billJobDTO(job, actor.IsSuperAdmin))
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
	job, err = a.store.BillExportData(r.Context(), actor.ID, id)
	if errors.Is(err, core.ErrNotFound) {
		fail(w, 404, "NOT_FOUND", "This export does not exist.")
		return true
	}
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
		return finishFailure("EXPORT_EXPIRED", "The export did not finish in time. Generate a new export.")
	}
	// Legacy queued jobs and retries must still be authorized when they run.
	if _, err = a.store.BillExport(ctx, job.RequestedBy, job.ID); errors.Is(err, core.ErrNotFound) {
		return finishFailure("ACCESS_CHANGED", "Access to this export has changed.")
	} else if err != nil {
		return err
	}
	var raw []byte
	switch job.UsageSource {
	case "raw":
		settings, err := a.store.Settings(ctx)
		if err != nil {
			return finishFailure("CONFIGURATION", "Export configuration could not be read.")
		}
		username, password := settings.String("account_sync_website_username"), settings.String("account_sync_website_password")
		if username == "" || password == "" {
			return finishFailure("NOT_CONFIGURED", "Configure the vendor website login in Account sync before exporting.")
		}
		raw, err = a.billExportClient().ExportClaudeBills(ctx, username, password, job.StartDate, job.EndDate)
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
	case "modified":
		// The report is built from the local ledger after loading its account selection.
	default:
		return finishFailure("INVALID_SOURCE", "The export could not be generated.")
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
		accounts = append(accounts, billing.Account{LocalAccountID: strconv.FormatUint(row.LocalAccountID, 10), Username: row.Username, Account: row.AccountEmail, Plan: row.Plan, PlatformAccountID: billString(row.PlatformAccountID), SupplierRecordID: billString(row.SupplierRecordID), RemoteID: billString(row.RemoteID), IdentityKey: row.IdentityKey, LatestSubmittedAt: row.LatestSubmittedAt})
	}
	var report billing.Report
	if job.UsageSource == "modified" {
		rows, e := a.store.ModifiedBillExportRows(ctx, job.ID)
		if e != nil {
			if core.IsValidationError(e) {
				return finishFailure("REPORT_TOO_LARGE", "This export is too large. Select a shorter date range.")
			}
			return finishFailure("BILL_READ_FAILED", "The saved bills could not be read.")
		}
		report, err = billing.StoredUsageReport(accounts, rows)
	} else {
		report, err = billing.Filter(raw, accounts)
		raw = nil
	}
	if err != nil {
		return finishFailure("INVALID_BILL_FORMAT", "The bill format could not be verified. No incomplete report was generated.")
	}
	report.UserEmail, report.StartDate, report.EndDate = job.UserEmail, job.StartDate, job.EndDate
	for _, user := range billExportSelectedUsers(*job) {
		report.UserEmails = append(report.UserEmails, user["email"])
	}
	if len(report.UserEmails) > 1 {
		report.UserEmail = ""
	}
	report.IncludeSubUsers = job.IncludeSubUsers
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
