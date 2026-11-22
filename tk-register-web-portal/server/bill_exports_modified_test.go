package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
	"goapi-tk-register/server/internal/billing"
	"goapi-tk-register/server/internal/core"
)

func TestModifiedBillExportUsesStoredRowsAndPreservesEmptyPeriods(t *testing.T) {
	a, owner, _, _, accounts, _ := setupPageSync(t)
	admin := addUser(t, a, "period-export-admin@example.com", "admin")
	ctx := context.Background()
	now := time.Now().UTC()
	day := func(account core.SyncedAccount, date, identity string) uint64 {
		t.Helper()
		d := core.AccountUsageDay{AccountID: account.ID, UsageDate: date, IdentityKey: identity, LastSuccessAt: &now, LastAttemptAt: now}
		if err := a.store.DB.Create(&d).Error; err != nil {
			t.Fatal(err)
		}
		return d.ID
	}
	line := func(id uint64, model, cost string) {
		t.Helper()
		row := core.AccountDailyUsageModified{DayID: id, Model: model, CostCNY: cost, InputTokens: "9007199254740993", OutputTokens: "10", CacheReadTokens: "2", CacheWriteTokens: "3"}
		if err := a.store.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, date := range []string{"2026-01-05", "2026-01-06"} {
		line(day(accounts[0], date, accounts[0].IdentityKey), "local-model", "1.23456789")
	}
	line(day(accounts[2], "2026-01-05", accounts[2].IdentityKey), "free", "0")
	line(day(accounts[1], "2026-01-04", accounts[1].IdentityKey), "outside-range", "88")
	line(day(accounts[1], "2026-01-05", "previous-lifetime"), "old-identity", "77")
	outsider := addUser(t, a, "another-owner@example.com", "member")
	seedAdminSubmission(t, a, outsider.ID, configuredVendor(t, a, admin.ID), "outside-selection@example.com", "accepted", now)
	foreign := syncTarget(t, a, outsider.ID)
	line(day(foreign, "2026-01-05", foreign.IdentityKey), "other-owner-model", "66")
	legacy := core.AccountDailyUsage{DayID: day(accounts[1], "2026-01-05", accounts[1].IdentityKey), Model: "raw-only", CostCNY: "99", InputTokens: "1", OutputTokens: "2", CacheReadTokens: "3", CacheWriteTokens: "4"}
	if err := a.store.DB.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	fake := &fakeBillExporter{}
	a.billExporter = fake
	generate := func(start, end string) (core.BillExportJob, billing.Report) {
		t.Helper()
		job, err := a.store.CreateBillExport(ctx, admin.ID, owner.ID, start, end, now)
		if err != nil {
			t.Fatal(err)
		}
		if err = a.processBillExport(ctx); err != nil {
			t.Fatal(err)
		}
		result, err := a.store.BillExportData(ctx, admin.ID, job.ID)
		if err != nil || result.Status != "completed" || fake.calls != 0 {
			t.Fatalf("local export %s %s, supplier calls=%d: %v", result.Status, result.ErrorMessage, fake.calls, err)
		}
		var report billing.Report
		if err = json.Unmarshal([]byte(result.ReportJSON), &report); err != nil {
			t.Fatal(err)
		}
		return result, report
	}
	result, report := generate("2026-01-05", "2026-01-06")
	if result.TotalCNY != "2.46913578" || result.RowCount != 2 || len(report.Details) != 2 || len(report.Summary) != 2 || report.TotalCNY != result.TotalCNY {
		t.Fatalf("incorrect period totals: %+v", report)
	}
	tokens := "18014398509481986"
	if report.Details[0][4] != "local-model" || report.Details[0][5] != tokens || report.Details[0][6] != "20" || report.Details[0][7] != "4" || report.Details[0][8] != "6" ||
		report.Details[0][9] != result.TotalCNY || report.Summary[1].Account != accounts[2].Account || report.Summary[1].AmountCNY == nil || *report.Summary[1].AmountCNY != "0.00" {
		t.Fatalf("wrong account/model data: %+v", report)
	}
	book, err := excelize.OpenReader(bytes.NewReader(result.Artifact))
	if err != nil {
		t.Fatal(err)
	}
	defer book.Close()
	for cell, want := range map[string]string{"E7": "local-model", "F7": tokens, "J7": result.TotalCNY, "E8": "free"} {
		got, err := book.GetCellValue("Bill details", cell, excelize.Options{RawCellValue: true})
		if err != nil || got != want {
			t.Fatalf("XLSX cell %s = %q, want %q, err %v", cell, got, want, err)
		}
	}
	empty, noRows := generate("2026-01-01", "2026-01-01")
	if empty.TotalCNY != "" || noRows.TotalCNY != "" || len(noRows.Summary) != 0 || len(noRows.Details) != 0 {
		t.Fatalf("missing period treated as measured usage: %+v", noRows)
	}
}

func TestLegacyQueuedRawExportRequiresCurrentSuperAdmin(t *testing.T) {
	a, owner, _, _, _, _ := setupPageSync(t)
	admin := addUser(t, a, core.InitialSuperAdminEmail, "admin")
	ctx := context.Background()
	job, err := a.store.CreateBillExport(ctx, admin.ID, owner.ID, "2026-01-01", "2026-01-02", time.Now().UTC())
	if err != nil || job.UsageSource != "raw" {
		t.Fatal(err, job.UsageSource)
	}
	if err = a.store.DB.Model(admin).Update("is_super_admin", false).Error; err != nil {
		t.Fatal(err)
	}
	fake := &fakeBillExporter{}
	a.billExporter = fake
	if err = a.processBillExport(ctx); err != nil {
		t.Fatal(err)
	}
	if err = a.store.DB.First(&job, job.ID).Error; err != nil || job.Status != "failed" || job.ErrorCode != "ACCESS_CHANGED" || fake.calls != 0 {
		t.Fatal(job.Status, job.ErrorCode, fake.calls, err)
	}
}
