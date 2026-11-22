package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"goapi-tk-register/server/internal/core"
)

func TestSubmissionBillsAreScopedToAuthorizedLedgerAndAccountLifetime(t *testing.T) {
	a, owner, member, memberCSRF, accounts, _ := setupPageSync(t)
	super, superCookie, _ := vendorSession(t, a, core.InitialSuperAdminEmail, "admin")
	_, admin, adminCSRF := vendorSession(t, a, "bills-admin@example.com", "admin")
	_, stranger, strangerCSRF := vendorSession(t, a, "bills-stranger@example.com", "member")
	account := accounts[0]
	id := *account.CurrentSubmissionID
	path := fmt.Sprintf("submissions/%d", id)
	now := time.Now().UTC()
	yesterday := now.In(time.FixedZone("Asia/Shanghai", 8*3600)).AddDate(0, 0, -1).Format("2006-01-02")
	older := now.AddDate(0, 0, -5).Format("2006-01-02")
	dates := core.BillsInput{StartDS: yesterday, EndDS: yesterday}
	rawJSON := fmt.Sprintf(`{"start_ds":%q,"end_ds":%q,"currency":"CNY","account":{"id":%q,"cost":789123.45,"prompt_tokens":989898}}`, older, older, *account.RemoteID)
	db := a.store.DB
	var record core.Submission
	if err := db.First(&record, id).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&record).Updates(map[string]any{"bills_json": rawJSON, "metrics_json": `{"account":{"status":"online","prompt_tokens":989898,"completion_tokens":989898}}`}).Error; err != nil {
		t.Fatal(err)
	}
	execution := core.ProtocolExecution{SubmissionID: id, ProtocolVersionID: record.ProtocolVersionID, RequestID: "local-bills-test", Operation: "bills", Status: "succeeded", ResultJSON: rawJSON, ErrorBody: `{"cost":789123.45,"models":["legacy-only"]}`}
	if err := db.Create(&execution).Error; err != nil {
		t.Fatal(err)
	}
	saveDay := func(accountID uint64, identity, date string, modified bool) {
		t.Helper()
		day := core.AccountUsageDay{AccountID: accountID, IdentityKey: identity, UsageDate: date, LastSuccessAt: &now, LastAttemptAt: now}
		if err := db.Create(&day).Error; err != nil {
			t.Fatal(err)
		}
		raw := core.AccountDailyUsage{DayID: day.ID, Model: "legacy-only", CostCNY: "789123.45", InputTokens: "989898", OutputTokens: "6", CacheReadTokens: "7", CacheWriteTokens: "8"}
		if err := db.Create(&raw).Error; err != nil {
			t.Fatal(err)
		}
		if modified {
			line := core.AccountDailyUsageModified{DayID: day.ID, Model: "model", CostCNY: "12.34567891", InputTokens: "15", OutputTokens: "3", CacheReadTokens: "5", CacheWriteTokens: "7"}
			if err := db.Create(&line).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	saveDay(account.ID, account.IdentityKey, yesterday, true)
	saveDay(account.ID, account.IdentityKey, older, false)
	saveDay(account.ID, "another-lifetime", yesterday, true)
	saveDay(accounts[1].ID, accounts[1].IdentityKey, yesterday, true)
	a.vendorTransport = vendorRoundTrip(func(*http.Request) (*http.Response, error) {
		t.Error("a local billing query reached the supplier")
		return nil, fmt.Errorf("unexpected supplier query")
	})
	if err := a.store.PatchSettings(context.Background(), map[string]string{"user_overview_model_costs_enabled": "true"}, super.ID); err != nil {
		t.Fatal(err)
	}
	rawRead := request(t, a, "GET", "admin/"+path, nil, superCookie, "")
	requireStatus(t, rawRead, 200)
	if !strings.Contains(rawRead.Body.String(), "789123.45") {
		t.Fatal("super administrator lost saved supplier bills")
	}
	for _, scope := range []struct {
		path   string
		cookie *http.Cookie
		csrf   string
	}{{path, member, memberCSRF}, {"admin/" + path, admin, adminCSRF}} {
		if err := db.Model(&record).UpdateColumn("modified_bills_queried_at", nil).Error; err != nil {
			t.Fatal(err)
		}
		oldDetail := request(t, a, "GET", scope.path, nil, scope.cookie, "")
		requireStatus(t, oldDetail, 200)
		if submissionDetail(t, oldDetail.Body.Bytes()).Bills != nil || strings.Contains(oldDetail.Body.String(), "789123.45") || strings.Contains(oldDetail.Body.String(), "989898") || strings.Contains(oldDetail.Body.String(), "legacy-only") {
			t.Fatal("old bill, raw metrics or execution fields escaped")
		}
		query := request(t, a, "POST", scope.path+"/bills", dates, scope.cookie, scope.csrf)
		requireStatus(t, query, 200)
		b := submissionDetail(t, query.Body.Bytes()).Bills
		if b == nil || b.Account == nil || b.Account.Cost == nil || *b.Account.Cost != 12.34567891 || *b.Account.PromptTokens != 15 || *b.Account.CompletionTokens != 3 || *b.Account.CacheReadInputTokens != 5 || *b.Account.CacheWriteInputTokens != 7 || b.Account.RequestCount != nil {
			t.Fatal("incorrect account/day/range or local token projection", b)
		}
		read := request(t, a, "GET", scope.path, nil, scope.cookie, "")
		requireStatus(t, read, 200)
		cached := submissionDetail(t, read.Body.Bytes()).Bills
		if cached == nil || cached.Account == nil || *cached.Account.Cost != *b.Account.Cost || cached.QueriedAt.Sub(b.QueriedAt).Abs() > time.Millisecond {
			t.Fatal("last selected local bill or its timestamp changed")
		}
		empty := request(t, a, "POST", scope.path+"/bills", core.BillsInput{StartDS: older, EndDS: older}, scope.cookie, scope.csrf)
		requireStatus(t, empty, 200)
		if b := submissionDetail(t, empty.Body.Bytes()).Bills; b == nil || b.Account != nil {
			t.Fatal("raw-only day supplied a bill")
		}
	}
	requireStatus(t, request(t, a, "POST", path+"/bills", dates, stranger, strangerCSRF), 404)
	if err := db.Model(&core.SyncedAccount{}).Where("id = ?", account.ID).Update("deleted_through_submission_id", id).Error; err != nil {
		t.Fatal(err)
	}
	requireStatus(t, request(t, a, "POST", path+"/bills", dates, member, memberCSRF), 409)
	var final core.Submission
	if err := db.First(&final, id).Error; err != nil || final.BillsJSON != rawJSON || final.UserID != owner.ID {
		t.Fatal("local query changed the original bill or owner", err)
	}
}
