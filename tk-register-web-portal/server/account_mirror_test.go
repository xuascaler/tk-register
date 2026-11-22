package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"goapi-tk-register/server/internal/billing"
	"goapi-tk-register/server/internal/core"
)

func TestAccountMirrorAccessAndSettings(t *testing.T) {
	a, _, _ := fixture(t)
	root, rootCookie, rootCSRF := vendorSession(t, a, core.InitialSuperAdminEmail, "admin")
	admin, adminCookie, adminCSRF := vendorSession(t, a, "delegate@example.com", "admin")
	member, memberCookie, _ := vendorSession(t, a, "member@example.com", "member")
	if !root.IsSuperAdmin || admin.IsSuperAdmin || member.IsSuperAdmin {
		t.Fatal("incorrect initial super administrator")
	}
	for _, route := range []string{"admin/raw-accounts", "admin/raw-accounts/overview", "admin/raw-accounts/overview/sources", "admin/raw-accounts/1/daily-usage"} {
		requireStatus(t, request(t, a, "GET", route, nil, memberCookie, ""), 403)
		requireStatus(t, request(t, a, "GET", route, nil, adminCookie, ""), 403)
		requireStatus(t, request(t, a, "GET", route, nil, nil, ""), 401)
	}
	requireStatus(t, request(t, a, "GET", "admin/raw-accounts", nil, rootCookie, ""), 200)
	visible := request(t, a, "GET", "admin/settings", nil, adminCookie, "")
	requireStatus(t, visible, 200)
	if strings.Contains(visible.Body.String(), "model_usage_source") || strings.Contains(visible.Body.String(), "fable_min_percent") {
		t.Fatal("super administrator settings exposed to ordinary administrator")
	}
	patch := func(cookie *http.Cookie, csrf string, settings map[string]string, status int) {
		t.Helper()
		requireStatus(t, request(t, a, "PUT", "admin/settings", map[string]any{"settings": settings}, cookie, csrf), status)
	}
	patch(adminCookie, adminCSRF, map[string]string{"fable_max_percent": "25"}, 403)
	patch(adminCookie, adminCSRF, map[string]string{"model_usage_source": "raw", "smtp_from_name": "blocked"}, 403)
	patch(rootCookie, rootCSRF, map[string]string{"user_overview_model_costs_enabled": "true"}, 200)
	patch(rootCookie, rootCSRF, map[string]string{"model_usage_source": "raw"}, 200)
	check := func(wantSource string, wantDetails bool) {
		t.Helper()
		source, enabled, err := a.store.ModelUsageSettings(context.Background())
		if err != nil || source != wantSource || enabled != wantDetails {
			t.Fatalf("settings = %s, %t, %v", source, enabled, err)
		}
	}
	check("raw", false)
	patch(rootCookie, rootCSRF, map[string]string{"user_overview_model_costs_enabled": "true"}, 400)
	patch(rootCookie, rootCSRF, map[string]string{"model_usage_source": "modified"}, 200)
	check("modified", false)
	patch(rootCookie, rootCSRF, map[string]string{"fable_min_percent": "19"}, 400)
	patch(rootCookie, rootCSRF, map[string]string{"fable_min_percent": "15.625"}, 200)

	grant := func(email string, cookie *http.Cookie, csrf string, code int) {
		t.Helper()
		requireStatus(t, request(t, a, "POST", "admin/users/role", map[string]string{"email": email, "role": "super_admin"}, cookie, csrf), code)
	}
	grant(admin.Email, adminCookie, adminCSRF, 403)
	grant(member.Email, rootCookie, rootCSRF, 409)
	grant(admin.Email, rootCookie, rootCSRF, 200)
	requireStatus(t, request(t, a, "GET", "auth/me", nil, adminCookie, ""), 401)
	promoted, err := a.store.GetUserByID(context.Background(), admin.ID)
	if err != nil || !promoted.IsSuperAdmin || promoted.Role != "admin" {
		t.Fatalf("delegated administrator = %+v, error %v", promoted, err)
	}
	token, newSession, err := a.newSession(context.Background(), promoted.ID, promoted.Role, 28800, 1800, promoted.AuthVersion)
	if err != nil {
		t.Fatal(err)
	}
	newCookie := &http.Cookie{Name: cookieName, Value: token}
	patch(newCookie, newSession.CSRF, map[string]string{"fable_trigger_percent": "20"}, 200)
	requireStatus(t, request(t, a, "GET", "admin/raw-accounts", nil, newCookie, ""), 200)
}

func TestCollectedAccountMirrorThroughAccountsAndRawAccount(t *testing.T) {
	a, owner, ownerCookie, _, accounts, _ := setupPageSync(t)
	admin, adminCookie, _ := vendorSession(t, a, "mirror-admin@example.com", "admin")
	root, rootCookie, rootCSRF := vendorSession(t, a, core.InitialSuperAdminEmail, "admin")
	ctx := context.Background()
	now := time.Now()
	date := now.In(time.FixedZone("Asia/Shanghai", 8*60*60)).AddDate(0, 0, -1).Format("2006-01-02")
	account := accounts[0]
	job := core.DailyUsageJob{VendorID: account.VendorID, UsageDate: date, Status: "running", LeaseToken: "mirror-collection", NextRunAt: now}
	if err := a.store.DB.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	result := billing.DailyAccount{LocalAccountID: fmt.Sprint(account.ID), IdentityKey: account.IdentityKey, Rows: []billing.DailyRow{
		{Model: "claude-fable-5", CostCNY: "3600", InputTokens: "3600", OutputTokens: "80", CacheReadTokens: "45", CacheWriteTokens: "30"},
		{Model: "claude-opus-5", CostCNY: "3600", InputTokens: "3600", OutputTokens: "360", CacheReadTokens: "80", CacheWriteTokens: "50"},
	}}
	if err := a.store.SaveDailyUsageAccount(ctx, job, result, now); err != nil {
		t.Fatal(err)
	}
	// Before the cutover, this account had an additional upstream bill without a mirror.
	start := now.In(time.FixedZone("Asia/Shanghai", 8*60*60)).AddDate(0, 0, -2).Format("2006-01-02")
	historical := core.AccountUsageDay{AccountID: account.ID, IdentityKey: account.IdentityKey, UsageDate: start, LastSuccessAt: &now, LastAttemptAt: now}
	if err := a.store.DB.Create(&historical).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.store.DB.Create(&core.AccountDailyUsage{DayID: historical.ID, Model: "only-in-upstream", CostCNY: "7.2", InputTokens: "37", OutputTokens: "0", CacheReadTokens: "0", CacheWriteTokens: "0"}).Error; err != nil {
		t.Fatal(err)
	}
	for _, viewer := range []*core.User{owner, admin, root} {
		if err := a.store.BuildOverviewSummaries(ctx, viewer.ID, viewer.Role == "admin", date, "mirror-collection", now); err != nil {
			t.Fatal(err)
		}
	}
	patch := func(settings map[string]string) {
		t.Helper()
		requireStatus(t, request(t, a, "PUT", "admin/settings", map[string]any{"settings": settings}, rootCookie, rootCSRF), 200)
	}
	patch(map[string]string{"user_overview_model_costs_enabled": "true"})
	daily := fmt.Sprintf("/%d/daily-usage?start_date=%s&end_date=%s&model_usage_source=raw", account.ID, start, date)
	for _, source := range []string{"modified", "raw"} {
		patch(map[string]string{"model_usage_source": source})
		for _, tc := range []struct {
			route    string
			cookie   *http.Cookie
			source   string
			showRows bool
		}{
			{"accounts/overview?period=week&model_usage_source=raw", ownerCookie, "modified", source == "modified"},
			{"accounts/overview?sources=myself&model_usage_source=raw", ownerCookie, "modified", source == "modified"},
			{"accounts" + daily, ownerCookie, "modified", source == "modified"},
			{"admin/accounts/overview?period=week&model_usage_source=raw", adminCookie, "modified", true},
			{"admin/accounts/overview?start_date=" + start + "&end_date=" + date, adminCookie, "modified", true},
			{"admin/synced-accounts" + daily, adminCookie, "modified", true},
			{"admin/accounts/overview?period=week", rootCookie, source, true},
			{"admin/accounts/overview?start_date=" + start + "&end_date=" + date, rootCookie, source, true},
			{"admin/synced-accounts" + daily, rootCookie, source, true},
			{"admin/raw-accounts/overview?start_date=" + start + "&end_date=" + date, rootCookie, "raw", true},
			{"admin/raw-accounts" + daily, rootCookie, "raw", true},
		} {
			w := request(t, a, "GET", tc.route, nil, tc.cookie, "")
			requireStatus(t, w, 200)
			out := data(t, w)
			marker, present := out["model_usage_source"]
			if tc.cookie == rootCookie && marker != tc.source || tc.cookie != rootCookie && present {
				t.Fatalf("%s %s: unexpected source marker %v", source, tc.route, marker)
			}
			total, rowCount := 1000.0, 2
			if tc.source == "raw" {
				total, rowCount = 1001.0, 3
			}
			if out["total_cost_usd"] != total || (tc.source == "modified" && strings.Contains(w.Body.String(), "only-in-upstream")) {
				t.Fatalf("%s %s: source %v, total %v", source, tc.route, out["model_usage_source"], out["total_cost_usd"])
			}
			rows, shown := out["rows"].([]any)
			if shown != tc.showRows {
				t.Fatalf("%s %s: unexpected visibility", source, tc.route)
			}
			if !shown {
				continue
			}
			if len(rows) != rowCount {
				t.Fatalf("%s: expected %d rows, got %d", tc.route, rowCount, len(rows))
			}
			for _, item := range rows {
				row := item.(map[string]any)
				if row["model"] == "claude-fable-5" {
					amount := row["cost_usd"].(float64)
					if tc.source == "raw" && amount != 500 || tc.source == "modified" && (amount < 160 || amount > 210) {
						t.Fatalf("%s: %s Fable cost %v", tc.route, tc.source, amount)
					}
				}
			}
		}
	}
}
