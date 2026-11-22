package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
	"goapi-tk-register/server/internal/billing"
	"goapi-tk-register/server/internal/core"
)

func TestBillExportHTTPSourceSelectionControlsReportAndWorkbook(t *testing.T) {
	a, owner, _, _, accounts, _ := setupPageSync(t)
	admin, cookie, csrf := vendorSession(t, a, "source-admin@example.com", "admin")
	super, superCookie, superCSRF := vendorSession(t, a, core.InitialSuperAdminEmail, "admin")
	ctx, now := context.Background(), time.Now().UTC()
	if err := a.store.DB.Model(&accounts[0]).Update("platform_account_id", "15001").Error; err != nil {
		t.Fatal(err)
	}
	day := core.AccountUsageDay{AccountID: accounts[0].ID, IdentityKey: accounts[0].IdentityKey, UsageDate: "2026-01-05", LastSuccessAt: &now, LastAttemptAt: now}
	if err := a.store.DB.Create(&day).Error; err != nil {
		t.Fatal(err)
	}
	line := core.AccountDailyUsageModified{DayID: day.ID, Model: "stored-model", CostCNY: "5", InputTokens: "1", OutputTokens: "2", CacheReadTokens: "3", CacheWriteTokens: "4"}
	if err := a.store.DB.Create(&line).Error; err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("internal/billing/testdata/claude-bills.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeBillExporter{raw: raw}
	a.billExporter = fake
	for _, test := range []struct {
		name, source, want string
		actor              *core.User
		cookie             *http.Cookie
		csrf               string
	}{
		{"ordinary default", "", "modified", admin, cookie, csrf},
		{"ordinary forged raw", "raw", "modified", admin, cookie, csrf},
		{"ordinary modified", "modified", "modified", admin, cookie, csrf},
		{"super default", "", "raw", super, superCookie, superCSRF},
		{"super raw", "raw", "raw", super, superCookie, superCSRF},
		{"super modified", "modified", "modified", super, superCookie, superCSRF},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := map[string]any{"user_id": fmt.Sprint(owner.ID), "start_date": "2026-01-05", "end_date": "2026-01-05", "include_sub_users": true}
			if test.source != "" {
				body["model_usage_source"] = test.source
			}
			response := request(t, a, "POST", "admin/bill-exports", body, test.cookie, test.csrf)
			requireStatus(t, response, 200)
			metadata := data(t, response)
			if test.actor.IsSuperAdmin {
				if metadata["model_usage_source"] != test.want {
					t.Fatal("wrong selected source", metadata)
				}
			} else if metadata["model_usage_source"] != nil {
				t.Fatal("ordinary admin received source metadata", metadata)
			}
			before := fake.calls
			if err := a.processBillExport(ctx); err != nil {
				t.Fatal(err)
			}
			id := metadata["id"].(string)
			jobID, _ := core.ParseBusinessID(id)
			job, err := a.store.BillExportData(ctx, test.actor.ID, jobID)
			if err != nil || job.Status != "completed" || job.UsageSource != test.want {
				t.Fatal("wrong saved source or job status", job.Status, job.UsageSource, err)
			}
			cost, calls := "5.00", 0
			if test.want == "raw" {
				cost, calls = "12.34", 1
			}
			var report billing.Report
			if err = json.Unmarshal([]byte(job.ReportJSON), &report); err != nil {
				t.Fatal(err)
			}
			if fake.calls-before != calls || job.TotalCNY != cost || report.TotalCNY != cost || len(report.Details) != 1 || report.Details[0][9] != cost {
				t.Fatalf("report/source mismatch: calls=%d, job total=%s, report=%+v", fake.calls-before, job.TotalCNY, report)
			}
			response = request(t, a, "GET", "admin/bill-exports/"+id+"/download", nil, test.cookie, "")
			requireStatus(t, response, 200)
			book, err := excelize.OpenReader(bytes.NewReader(response.Body.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			value, _ := book.GetCellValue("Bill details", "J7")
			book.Close()
			if value != cost {
				t.Fatal("workbook differs from selected-source preview", value, cost)
			}
		})
	}
	response := request(t, a, "POST", "admin/bill-exports", map[string]any{"user_id": fmt.Sprint(owner.ID), "start_date": "2026-01-05", "end_date": "2026-01-05", "model_usage_source": "unknown"}, superCookie, superCSRF)
	requireStatus(t, response, 400)
}
