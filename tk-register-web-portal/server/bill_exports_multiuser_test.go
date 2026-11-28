package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
	"goapi-tk-register/server/internal/billing"
	"goapi-tk-register/server/internal/core"
)

func TestBillExportHTTPMultipleUsersFrozenReportAndWorkbook(t *testing.T) {
	for _, source := range []string{"cache", "raw"} {
		t.Run(source, func(t *testing.T) {
			a, owner, _, _, accounts, _ := setupPageSync(t)
			email := "multi-admin@example.com"
			if source == "raw" {
				email = core.InitialSuperAdminEmail
			}
			admin, cookie, csrf := vendorSession(t, a, email, "admin")
			ctx, now := context.Background(), time.Now().UTC()
			child := core.User{Email: "multi-child@example.com", PasswordHash: "fake", EmailVerifiedAt: now}
			if err := a.store.CreateMemberByUser(ctx, owner.ID, owner.AuthVersion, &child); err != nil {
				t.Fatal(err)
			}
			seedAdminSubmission(t, a, child.ID, configuredVendor(t, a, admin.ID), "multi-claude@example.com", "accepted", now)
			childAccount := syncTarget(t, a, child.ID)
			if err := a.store.DB.Model(&accounts[0]).Update("platform_account_id", "15001").Error; err != nil {
				t.Fatal(err)
			}
			for _, account := range []core.SyncedAccount{accounts[0], childAccount} {
				day := core.AccountUsageDay{AccountID: account.ID, IdentityKey: account.IdentityKey, UsageDate: "2026-01-05", LastSuccessAt: &now, LastAttemptAt: now}
				if err := a.store.DB.Create(&day).Error; err != nil {
					t.Fatal(err)
				}
				line := core.AccountDailyUsageModified{DayID: day.ID, Model: "one", CostCNY: "5", InputTokens: "1", OutputTokens: "2", CacheReadTokens: "3", CacheWriteTokens: "4"}
				if err := a.store.DB.Create(&line).Error; err != nil {
					t.Fatal(err)
				}
			}
			raw, err := os.ReadFile("internal/billing/testdata/claude-bills.xlsx")
			if err != nil {
				t.Fatal(err)
			}
			fake := &fakeBillExporter{raw: raw}
			a.billExporter = fake
			body := map[string]any{
				"user_ids":   []string{fmt.Sprint(owner.ID), fmt.Sprint(child.ID), fmt.Sprint(owner.ID), fmt.Sprint(admin.ID)},
				"start_date": "2026-01-05", "end_date": "2026-01-05", "include_sub_users": true,
				// Ordinary administrators cannot select Raw even with a forged field.
				"model_usage_source": "raw",
			}
			response := request(t, a, "POST", "admin/bill-exports", body, cookie, csrf)
			requireStatus(t, response, 200)
			metadata := data(t, response)
			users := metadata["users"].([]any)
			if len(users) != 3 || metadata["user"] != nil || users[1].(map[string]any)["email"] != child.Email || metadata["account_count"] != float64(4) {
				t.Fatal("selection or descendant union was not deduplicated", metadata)
			}
			if source == "cache" && metadata["model_usage_source"] != nil {
				t.Fatal("ordinary administrator received source metadata")
			}
			id := metadata["id"].(string)
			// Later user changes must not rewrite selected-user or account attribution.
			if err = a.store.DB.Model(&child).Update("email", "renamed-child@example.com").Error; err != nil {
				t.Fatal(err)
			}
			if err = a.processBillExport(ctx); err != nil {
				t.Fatal(err)
			}
			jobID, _ := core.ParseBusinessID(id)
			job, err := a.store.BillExportData(ctx, admin.ID, jobID)
			wantTotal, wantCalls := "10.00", 0
			if source == "raw" {
				wantTotal, wantCalls = "12.34", 1
			}
			if err != nil || job.Status != "completed" || job.TotalCNY != wantTotal || fake.calls != wantCalls {
				t.Fatal("merged report failed", err, job.Status, job.TotalCNY, fake.calls)
			}
			var report billing.Report
			if err = json.Unmarshal([]byte(job.ReportJSON), &report); err != nil {
				t.Fatal(err)
			}
			if len(report.UserEmails) != 3 || report.UserEmails[1] != "multi-child@example.com" || report.UserEmail != "" {
				t.Fatal("worker lost multiuser snapshot", report.UserEmails, report.UserEmail)
			}
			if source == "cache" && (len(report.Details) != 2 || report.Details[1][0] != "multi-child@example.com") {
				t.Fatal("owner attribution was not frozen", report.Details)
			}
			response = request(t, a, "GET", "admin/bill-exports/"+id, nil, cookie, "")
			requireStatus(t, response, 200)
			if got := data(t, response)["users"].([]any); len(got) != 3 || got[1].(map[string]any)["email"] != "multi-child@example.com" {
				t.Fatal("poll returned live users instead of snapshot", got)
			}
			response = request(t, a, "GET", "admin/bill-exports/"+id+"/download", nil, cookie, "")
			requireStatus(t, response, 200)
			_, params, err := mime.ParseMediaType(response.Header().Get("Content-Disposition"))
			if err != nil || !strings.HasPrefix(params["filename"], "claude-bills_3-users_"+id+"_") {
				t.Fatal("multiuser filename missing", params, err)
			}
			book, err := excelize.OpenReader(bytes.NewReader(response.Body.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			defer book.Close()
			for _, sheet := range book.GetSheetList() {
				label, _ := book.GetCellValue(sheet, "A2")
				value, _ := book.GetCellValue(sheet, "B2")
				if label != "Users (3)" || value != strings.Join(report.UserEmails, "\n") {
					t.Fatal("sheet missing selected users", sheet, label, value)
				}
			}
		})
	}
}

func TestBillExportHTTPRejectsInvalidMultiUserSelections(t *testing.T) {
	a, owner, _, _, _, _ := setupPageSync(t)
	_, cookie, csrf := vendorSession(t, a, "multi-validation@example.com", "admin")
	for _, ids := range [][]string{{}, {"0"}, {"abc"}} {
		body := map[string]any{"user_ids": ids, "start_date": "2026-01-05", "end_date": "2026-01-05"}
		requireStatus(t, request(t, a, "POST", "admin/bill-exports", body, cookie, csrf), 400)
	}
	requireStatus(t, request(t, a, "POST", "admin/bill-exports", map[string]any{"user_ids": []string{fmt.Sprint(owner.ID), "999999999"}, "start_date": "2026-01-05", "end_date": "2026-01-05"}, cookie, csrf), 404)
	body := map[string]any{"user_ids": []string{fmt.Sprint(owner.ID)}, "user_id": fmt.Sprint(owner.ID), "start_date": "2026-01-05", "end_date": "2026-01-05"}
	requireStatus(t, request(t, a, "POST", "admin/bill-exports", body, cookie, csrf), 400)
}

func TestBillExportAllUserOptionsAndLargeSelection(t *testing.T) {
	a, owner, ownerCookie, _, accounts, _ := setupPageSync(t)
	admin, cookie, csrf := vendorSession(t, a, "all-export-admin@example.com", "admin")
	ctx, now := context.Background(), time.Now().UTC()
	for i := 0; i < 131; i++ {
		user := core.User{Email: fmt.Sprintf("all-export-%03d@example.com", i), PasswordHash: "not-for-the-browser", EmailVerifiedAt: now, Role: "member"}
		if err := a.store.CreateUser(ctx, &user); err != nil {
			t.Fatal(err)
		}
	}
	const route = "admin/bill-exports/users"
	requireStatus(t, request(t, a, "GET", route, nil, nil, ""), 401)
	requireStatus(t, request(t, a, "GET", route, nil, ownerCookie, ""), 403)
	requireStatus(t, request(t, a, "POST", route, map[string]any{}, cookie, csrf), 405)
	response := request(t, a, "GET", route+"?search=no-match&page=99", nil, cookie, "")
	requireStatus(t, response, 200)
	items := data(t, response)["items"].([]any)
	var total int64
	if err := a.store.DB.Model(&core.User{}).Count(&total).Error; err != nil {
		t.Fatal(err)
	}
	if len(items) != int(total) || len(items) <= 132 {
		t.Fatalf("All omitted users: got %d want %d", len(items), total)
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		user := item.(map[string]any)
		if len(user) != 2 || user["email"] == "" {
			t.Fatal("selection endpoint must only return ID/email", user)
		}
		ids = append(ids, user["id"].(string))
	}
	// Freeze a real Modified bill and exercise the entire worker/download path.
	day := core.AccountUsageDay{AccountID: accounts[0].ID, IdentityKey: accounts[0].IdentityKey, UsageDate: "2026-01-05", LastSuccessAt: &now, LastAttemptAt: now}
	if err := a.store.DB.Create(&day).Error; err != nil {
		t.Fatal(err)
	}
	line := core.AccountDailyUsageModified{DayID: day.ID, Model: "one", CostCNY: "5", InputTokens: "1", OutputTokens: "2", CacheReadTokens: "3", CacheWriteTokens: "4"}
	if err := a.store.DB.Create(&line).Error; err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"user_ids": append(ids, fmt.Sprint(owner.ID)), "start_date": "2026-01-05", "end_date": "2026-01-05"}
	response = request(t, a, "POST", "admin/bill-exports", body, cookie, csrf)
	requireStatus(t, response, 200)
	metadata := data(t, response)
	if len(metadata["users"].([]any)) != len(ids) {
		t.Fatal("large selection was truncated or duplicated")
	}
	if err := a.processBillExport(ctx); err != nil {
		t.Fatal(err)
	}
	id := metadata["id"].(string)
	jobID, _ := core.ParseBusinessID(id)
	job, err := a.store.BillExportData(ctx, admin.ID, jobID)
	if err != nil || job.Status != "completed" || job.TotalCNY != "5.00" || len(job.Users) != len(ids) {
		t.Fatalf("large selection export failed: status=%s total=%s users=%d err=%v", job.Status, job.TotalCNY, len(job.Users), err)
	}
	response = request(t, a, "GET", "admin/bill-exports/"+id+"/download", nil, cookie, "")
	requireStatus(t, response, 200)
	book, err := excelize.OpenReader(bytes.NewReader(response.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer book.Close()
	for _, sheet := range book.GetSheetList() {
		label, _ := book.GetCellValue(sheet, "A2")
		if label != fmt.Sprintf("Users (%d)", len(ids)) {
			t.Fatal("workbook lost selected users", sheet, label)
		}
	}
}

func TestBillExportDTOLegacyUserFallback(t *testing.T) {
	job := core.BillExportJob{ID: 9, TargetUserID: 7, UserEmail: "legacy@example.com", StartDate: "2026-01-01", EndDate: "2026-01-02"}
	metadata := billJobDTO(job, false)
	users := metadata["users"].([]map[string]string)
	if len(users) != 1 || users[0]["email"] != job.UserEmail || metadata["user"] == nil || !strings.Contains(billExportFilename(job), job.UserEmail) {
		t.Fatal("legacy job is no longer represented as one selected user", metadata)
	}
}
