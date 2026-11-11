package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"goapi-tk-register/server/internal/billing"
	"goapi-tk-register/server/internal/core"
	"goapi-tk-register/server/internal/supplier"
)

type fakeBillExporter struct {
	calls int
	raw   []byte
	err   error
}

func (f *fakeBillExporter) ExportClaudeBills(context.Context, string, string, string, string) ([]byte, error) {
	f.calls++
	return f.raw, f.err
}

func TestBillExportAdminCSRFAndDownloadIsolation(t *testing.T) {
	a, owner, memberCookie, memberCSRF, _, _ := setupPageSync(t)
	_, cookie, csrf := vendorSession(t, a, "bills-admin@example.com", "admin")
	body := map[string]string{"user_id": fmt.Sprint(owner.ID), "start_date": "2026-01-01", "end_date": "2026-01-02"}
	route := "admin/bill-exports"
	requireStatus(t, request(t, a, "POST", route, body, nil, ""), 401)
	requireStatus(t, request(t, a, "POST", route, body, memberCookie, memberCSRF), 403)
	requireStatus(t, request(t, a, "POST", route, body, cookie, ""), 403)
	requireStatus(t, request(t, a, "POST", route, map[string]string{"user_id": "1", "start_date": "2026-01-01", "end_date": "2026-04-01"}, cookie, csrf), 400)
	w := request(t, a, "POST", route, body, cookie, csrf)
	requireStatus(t, w, 200)
	id := data(t, w)["id"].(string)
	jobID, _ := core.ParseBusinessID(id)
	for _, suffix := range []string{"", "/results", "/download"} {
		requireStatus(t, request(t, a, "GET", route+"/"+id+suffix, nil, memberCookie, ""), 403)
		requireStatus(t, request(t, a, "GET", route+"/"+id+suffix, nil, nil, ""), 401)
	}
	requireStatus(t, request(t, a, "GET", route+"/"+id+"/download", nil, cookie, ""), 409)
	token := "bill-test-worker-token"
	job, err := a.store.ClaimBillExport(context.Background(), time.Now().UTC(), time.Now().Add(time.Minute), token)
	if err != nil || job == nil {
		t.Fatal(job, err)
	}
	report := billing.Report{Currency: "CNY", TotalCNY: "12.34", Columns: []string{"Account", "Cost (CNY)"}, Details: [][]string{{"one@example.com", "12.34"}}}
	raw, _ := json.Marshal(report)
	artifact := []byte("PKfiltered-xlsx-fixture")
	err = a.store.CompleteBillExport(context.Background(), jobID, token, core.BillExportResult{ReportJSON: string(raw), Artifact: artifact, TotalCNY: "12.34", RowCount: 1, MatchedAccountCount: 1}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	w = request(t, a, "GET", route+"/"+id, nil, cookie, "")
	requireStatus(t, w, 200)
	if strings.Contains(w.Body.String(), "filtered-xlsx-fixture") || strings.Contains(w.Body.String(), "one@example.com") {
		t.Fatal("polling must not expose artifact/report payload")
	}
	w = request(t, a, "GET", route+"/"+id+"/results?view=details&page=1", nil, cookie, "")
	requireStatus(t, w, 200)
	if data(t, w)["total"] != float64(1) {
		t.Fatal(w.Body.String())
	}
	requireStatus(t, request(t, a, "GET", route+"/"+id+"/results?view=bad", nil, cookie, ""), 400)
	w = request(t, a, "GET", route+"/"+id+"/download", nil, cookie, "")
	requireStatus(t, w, 200)
	if w.Body.String() != string(artifact) || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatal("wrong artifact response")
	}
	requireStatus(t, request(t, a, "POST", route+"/"+id+"/download", map[string]string{}, cookie, csrf), 405)
	requireStatus(t, request(t, a, "GET", route+"/99999999", nil, cookie, ""), 404)
}

func TestBillExportWorkerFailureAndCooldown(t *testing.T) {
	for _, test := range []struct {
		name     string
		upstream error
		raw      []byte
		status   string
	}{
		{"network", errors.New("secret remote details"), nil, "failed"},
		{"schema", nil, []byte("not an XLSX"), "failed"},
		{"cooldown", &supplier.Error{Code: supplier.CodeRateLimited, RetryAfterSeconds: 5, Stage: "login", Deferred: true}, nil, "queued"},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, owner, _, _, _, _ := setupPageSync(t)
			admin := addUser(t, a, "bills-admin@example.com", "admin")
			job, err := a.store.CreateBillExport(context.Background(), admin.ID, owner.ID, "2026-01-01", "2026-01-02", time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			fake := &fakeBillExporter{raw: test.raw, err: test.upstream}
			a.billExporter = fake
			if err = a.processBillExport(context.Background()); err != nil {
				t.Fatal(err)
			}
			saved, err := a.store.BillExportData(context.Background(), job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Status != test.status || fake.calls != 1 || len(saved.Artifact) != 0 || strings.Contains(saved.ErrorMessage, "secret") {
				t.Fatalf("invalid worker outcome %s %v", saved.Status, err)
			}
			if test.status == "queued" {
				if err = a.processBillExport(context.Background()); err != nil {
					t.Fatal(err)
				}
				if fake.calls != 1 {
					t.Fatal("ignored persisted cooldown")
				}
			}
		})
	}
}

func TestBillExportWorkerProducesOnlySelectedAccounts(t *testing.T) {
	a, owner, _, _, rows, _ := setupPageSync(t)
	admin := addUser(t, a, "bills-admin@example.com", "admin")
	if err := a.store.DB.Model(&rows[0]).Update("platform_account_id", "15001").Error; err != nil {
		t.Fatal(err)
	}
	if err := a.store.DB.Model(&rows[1]).Update("platform_account_id", "15002").Error; err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("internal/billing/testdata/claude-bills.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	a.billExporter = &fakeBillExporter{raw: raw}
	job, err := a.store.CreateBillExport(context.Background(), admin.ID, owner.ID, "2026-01-01", "2026-01-02", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err = a.processBillExport(context.Background()); err != nil {
		t.Fatal(err)
	}
	saved, err := a.store.BillExportData(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != "completed" || saved.TotalCNY != "12.34" || saved.MatchedAccountCount != 1 || saved.RowCount != 1 || saved.ReviewAccountCount != 1 {
		t.Fatalf("wrong filtered result: status=%s total=%s matched=%d rows=%d review=%d error=%s", saved.Status, saved.TotalCNY, saved.MatchedAccountCount, saved.RowCount, saved.ReviewAccountCount, saved.ErrorMessage)
	}
	if strings.Contains(saved.ReportJSON, "other@example.com") || strings.Contains(saved.ReportJSON, "99.99") || len(saved.Artifact) == 0 {
		t.Fatal("unselected row leaked or artifact missing")
	}
	var report billing.Report
	if err = json.Unmarshal([]byte(saved.ReportJSON), &report); err != nil {
		t.Fatal(err)
	}
	if report.UserEmail != owner.Email || len(report.Summary) != 3 || report.Summary[1].Status != "no_bills" || report.Summary[2].Status != "needs_review" {
		t.Fatal("coverage semantics incorrect")
	}
}
