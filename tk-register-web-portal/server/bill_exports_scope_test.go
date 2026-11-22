package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
	"goapi-tk-register/server/internal/billing"
	"goapi-tk-register/server/internal/core"
)

func TestBillExportHTTPDescendantScopeAndUsername(t *testing.T) {
	a, owner, _, _, accounts, _ := setupPageSync(t)
	admin, cookie, csrf := vendorSession(t, a, "scope-admin@example.com", "admin")
	ctx, now := context.Background(), time.Now().UTC()
	child := core.User{Email: "child@example.com", PasswordHash: "fake", EmailVerifiedAt: now}
	if err := a.store.CreateMemberByUser(ctx, owner.ID, owner.AuthVersion, &child); err != nil {
		t.Fatal(err)
	}
	grand := core.User{Email: "grand@example.com", PasswordHash: "fake", EmailVerifiedAt: now}
	if err := a.store.CreateMemberByUser(ctx, child.ID, child.AuthVersion, &grand); err != nil {
		t.Fatal(err)
	}
	seedAdminSubmission(t, a, grand.ID, configuredVendor(t, a, admin.ID), "grand-claude@example.com", "accepted", now)
	grandAccount := syncTarget(t, a, grand.ID)
	for _, account := range []core.SyncedAccount{accounts[0], grandAccount} {
		day := core.AccountUsageDay{AccountID: account.ID, IdentityKey: account.IdentityKey, UsageDate: "2026-01-05", LastSuccessAt: &now, LastAttemptAt: now}
		if err := a.store.DB.Create(&day).Error; err != nil {
			t.Fatal(err)
		}
		line := core.AccountDailyUsageModified{DayID: day.ID, Model: "one", CostCNY: "5", InputTokens: "1", OutputTokens: "2", CacheReadTokens: "3", CacheWriteTokens: "4"}
		if err := a.store.DB.Create(&line).Error; err != nil {
			t.Fatal(err)
		}
	}
	fake := &fakeBillExporter{}
	a.billExporter = fake
	for _, include := range []bool{false, true} {
		body := map[string]any{"user_id": fmt.Sprint(owner.ID), "start_date": "2026-01-05", "end_date": "2026-01-05", "include_sub_users": include}
		response := request(t, a, "POST", "admin/bill-exports", body, cookie, csrf)
		requireStatus(t, response, 200)
		metadata := data(t, response)
		if metadata["include_sub_users"] != include || metadata["model_usage_source"] != nil {
			t.Fatal("wrong scope or source visibility", metadata)
		}
		id := metadata["id"].(string)
		if err := a.processBillExport(ctx); err != nil {
			t.Fatal(err)
		}
		jobID, _ := core.ParseBusinessID(id)
		job, err := a.store.BillExportData(ctx, admin.ID, jobID)
		if err != nil || job.Status != "completed" || fake.calls != 0 {
			t.Fatal("export not completed from saved ledger", job.Status, err, fake.calls)
		}
		var report billing.Report
		if err = json.Unmarshal([]byte(job.ReportJSON), &report); err != nil {
			t.Fatal(err)
		}
		count, total := 1, "5.00"
		if include {
			count, total = 2, "10.00"
		}
		if len(report.Summary) != count || job.TotalCNY != total || report.IncludeSubUsers != include || report.Summary[0].Username != owner.Email {
			t.Fatalf("wrong scope result: %+v", report)
		}
		if include && report.Summary[1].Username != grand.Email {
			t.Fatal("descendant attributed to root", report.Summary)
		}
		response = request(t, a, "GET", "admin/bill-exports/"+id+"/results?view=summary", nil, cookie, "")
		requireStatus(t, response, 200)
		items := data(t, response)["items"].([]any)
		if len(items) != count || items[0].(map[string]any)["username"] != owner.Email {
			t.Fatal("missing API Username", data(t, response))
		}
		download := request(t, a, "GET", "admin/bill-exports/"+id+"/download", nil, cookie, "")
		requireStatus(t, download, 200)
		book, err := excelize.OpenReader(bytes.NewReader(download.Body.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		username, _ := book.GetCellValue("Bill details", "A7")
		book.Close()
		if username != owner.Email {
			t.Fatal("download missing frozen Username", username)
		}
	}
}
