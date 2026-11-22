package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"goapi-tk-register/server/internal/core"
)

func dailyAPIJSON(username string, accounts []string, date string) string {
	body, _ := json.Marshal(map[string]any{"username": username, "accounts": accounts, "date": date})
	return string(body)
}

func TestDailyUsageAPISavedBillsAndIndependentVisibility(t *testing.T) {
	a, owner, cookie, _, accounts, pageCollector := setupPageSync(t)
	now := time.Now().UTC()
	root, rootCookie, rootCSRF := vendorSession(t, a, core.InitialSuperAdminEmail, "admin")
	admin, adminCookie, adminCSRF := vendorSession(t, a, "daily-admin@example.com", "admin")
	const date = "2026-01-05"
	for i, account := range accounts[:2] {
		day := core.AccountUsageDay{AccountID: account.ID, IdentityKey: account.IdentityKey, UsageDate: date, LastSuccessAt: &now, LastAttemptAt: now}
		if err := a.store.DB.Create(&day).Error; err != nil {
			t.Fatal(err)
		}
		if err := a.store.DB.Create(&core.AccountDailyUsage{DayID: day.ID, Model: "raw-only", CostCNY: "999999", InputTokens: "1", OutputTokens: "2", CacheReadTokens: "3", CacheWriteTokens: "4"}).Error; err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			for model, cost := range map[string]string{"claude-fable-5-2": "144", "claude-opus-5": "576"} {
				row := core.AccountDailyUsageModified{DayID: day.ID, Model: model, CostCNY: cost, InputTokens: "1", OutputTokens: "2", CacheReadTokens: "3", CacheWriteTokens: "4"}
				if err := a.store.DB.Create(&row).Error; err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	a.vendorTransport = vendorRoundTrip(func(r *http.Request) (*http.Response, error) {
		t.Error("daily API contacted supplier")
		return remoteResponse(503, `{}`), nil
	})
	token := lookupToken(t, a, owner.ID)
	body := dailyAPIJSON(" OWNER@EXAMPLE.COM ", []string{" ONE@EXAMPLE.COM ", "two@example.com", "one@example.com", "missing@example.com"}, date)
	call := func() map[string]any {
		t.Helper()
		w := apiRequest(t, a, "POST", claudeAccountDailyUsageAPIPath, token.PlaintextToken, "application/json; charset=utf-8", body)
		requireStatus(t, w, 200)
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Request-ID") == "" {
			t.Fatal("missing private response or correlation headers")
		}
		for _, field := range []string{"model_usage_source", "raw-only", "999999", "fable_ratio", "password", "auth_json", token.PlaintextToken} {
			if strings.Contains(w.Body.String(), field) {
				t.Fatalf("response exposed internal data: %s", field)
			}
		}
		out := data(t, w)
		summary := out["summary"].(map[string]any)
		if out["username"] != owner.Email || out["date"] != date || out["timezone"] != "Asia/Shanghai" || out["exchange_rate_cny_per_usd"] != "7.2" || summary["cost_cny"] != "720.00000000" || summary["cost_usd"] != "100.00" || summary["status"] != "partial" {
			t.Fatal("incorrect owner, period or totals", out)
		}
		if summary["requested_accounts"] != float64(3) || summary["available_accounts"] != float64(1) || summary["no_data_accounts"] != float64(1) || summary["not_found_accounts"] != float64(1) {
			t.Fatal("incorrect deduplicated coverage", summary)
		}
		items := out["accounts"].([]any)
		for i, status := range []string{"ok", "no_data", "not_found"} {
			item := items[i].(map[string]any)
			if item["status"] != status || i > 0 && (item["cost_cny"] != nil || item["cost_usd"] != nil) {
				t.Fatal("missing data became an amount", items)
			}
		}
		return out
	}
	checkHidden := func(out map[string]any) {
		t.Helper()
		if out["model_costs_enabled"] != false {
			t.Fatal("model details should be disabled")
		}
		if _, exists := out["model_totals"]; exists {
			t.Fatal("disabled model totals were not omitted")
		}
		for _, item := range out["accounts"].([]any) {
			if _, exists := item.(map[string]any)["models"]; exists {
				t.Fatal("disabled models were not omitted")
			}
		}
	}
	checkHidden(call()) // Default false even for accounts with known models.
	patch := func(changes map[string]string) {
		t.Helper()
		requireStatus(t, request(t, a, "PUT", "admin/settings", map[string]any{"settings": changes}, rootCookie, rootCSRF), 200)
	}
	patch(map[string]string{"user_overview_model_costs_enabled": "true"})
	checkHidden(call()) // Browser model visibility does not turn on external models.
	patch(map[string]string{"api_user_overview_model_costs_enabled": "true", "model_usage_source": "raw"})
	out := call()
	if out["model_costs_enabled"] != true || len(out["model_totals"].([]any)) != 2 || len(out["accounts"].([]any)[0].(map[string]any)["models"].([]any)) != 2 {
		t.Fatal("independent API setting did not enable models while browser source was raw", out)
	}
	for _, item := range out["accounts"].([]any)[1:] {
		if len(item.(map[string]any)["models"].([]any)) != 0 {
			t.Fatal("missing account has model rows")
		}
	}
	patch(map[string]string{"api_user_overview_model_costs_enabled": "false"})
	checkHidden(call()) // The next request sees changes without rebuilding summaries.
	visible := request(t, a, "GET", "admin/settings", nil, adminCookie, "")
	requireStatus(t, visible, 200)
	if strings.Contains(visible.Body.String(), "api_user_overview_model_costs_enabled") {
		t.Fatal("ordinary administrator can see protected configuration")
	}
	requireStatus(t, request(t, a, "PUT", "admin/settings", map[string]any{"settings": map[string]string{"api_user_overview_model_costs_enabled": "true"}}, adminCookie, adminCSRF), 403)
	requireStatus(t, request(t, a, "PUT", "admin/settings", map[string]any{"settings": map[string]string{"api_user_overview_model_costs_enabled": "yes"}}, rootCookie, rootCSRF), 400)
	// Browser sessions never authenticate this external API.
	r := httptest.NewRequest("POST", claudeAccountDailyUsageAPIPath, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	requireStatus(t, w, 401)
	for _, actor := range []*core.User{admin, root} {
		actorToken := lookupToken(t, a, actor.ID)
		requireStatus(t, apiRequest(t, a, "POST", claudeAccountDailyUsageAPIPath, actorToken.PlaintextToken, "application/json", body), 404)
		w := apiRequest(t, a, "POST", claudeAccountDailyUsageAPIPath, actorToken.PlaintextToken, "application/json", dailyAPIJSON(actor.Email, []string{accounts[0].Account}, date))
		requireStatus(t, w, 200)
		result := data(t, w)
		if result["accounts"].([]any)[0].(map[string]any)["status"] != "not_found" || result["summary"].(map[string]any)["cost_usd"] != nil {
			t.Fatal("administrator token expanded owner scope", result)
		}
	}
	if pageCollector.calls != 0 {
		t.Fatal("daily API triggered collection")
	}
	// Query failures must not silently turn into missing bills or raw fallbacks.
	if err := a.store.DB.Migrator().DropTable(&core.AccountDailyUsageModified{}); err != nil {
		t.Fatal(err)
	}
	w = apiRequest(t, a, "POST", claudeAccountDailyUsageAPIPath, token.PlaintextToken, "application/json", body)
	requireStatus(t, w, 503)
	if strings.Contains(w.Body.String(), "SELECT") || strings.Contains(w.Body.String(), "account_daily_usages") {
		t.Fatal("SQL error exposed")
	}
}

func TestDailyUsageAPIInputAndTokenValidation(t *testing.T) {
	a, _, _ := fixture(t)
	owner, _, _ := vendorSession(t, a, "daily-validation@example.com", "member")
	token := lookupToken(t, a, owner.ID)
	body := dailyAPIJSON(owner.Email, []string{"account@example.com"}, "2026-01-05")
	for _, invalidToken := range []string{"", "invalid"} {
		requireStatus(t, apiRequest(t, a, "POST", claudeAccountDailyUsageAPIPath, invalidToken, "application/json", body), 401)
	}
	for _, method := range []string{"GET", "PUT", "DELETE", "HEAD"} {
		w := apiRequest(t, a, method, claudeAccountDailyUsageAPIPath, token.PlaintextToken, "application/json", body)
		requireStatus(t, w, 405)
		if w.Header().Get("Allow") != "POST" {
			t.Fatal("incorrect allowed method")
		}
	}
	for _, contentType := range []string{"", "text/plain", "application/json; broken"} {
		requireStatus(t, apiRequest(t, a, "POST", claudeAccountDailyUsageAPIPath, token.PlaintextToken, contentType, body), 415)
	}
	tooMany := make([]string, 101)
	for i := range tooMany {
		tooMany[i] = "account@example.com"
	}
	invalidBodies := []string{
		`null`, `[]`, `{}`, body + `{}`, strings.TrimSuffix(body, "}") + `,"username":"other@example.com"}`,
		strings.TrimSuffix(body, "}") + `,"model_usage_source":"raw"}`,
		strings.TrimSuffix(body, "}") + `,"include_sub_users":true}`,
		`{"username":"daily-validation@example.com","accounts":"account@example.com","date":"2026-01-05"}`,
		`{"username":"daily-validation@example.com","accounts":[null],"date":"2026-01-05"}`,
		dailyAPIJSON(owner.Email, nil, "2026-01-05"), dailyAPIJSON(owner.Email, []string{}, "2026-01-05"), dailyAPIJSON(owner.Email, tooMany, "2026-01-05"),
		dailyAPIJSON("invalid", []string{"account@example.com"}, "2026-01-05"),
		dailyAPIJSON(owner.Email, []string{" "}, "2026-01-05"), dailyAPIJSON(owner.Email, []string{"a\u2003b"}, "2026-01-05"),
		dailyAPIJSON(owner.Email, []string{strings.Repeat("a", 255)}, "2026-01-05"),
		dailyAPIJSON(owner.Email, []string{"account@example.com"}, "2026-1-5"),
		dailyAPIJSON(owner.Email, []string{"account@example.com"}, "2026-02-30"),
		dailyAPIJSON(owner.Email, []string{"account@example.com"}, "2026-01-05T00:00:00Z"),
		dailyAPIJSON(owner.Email, []string{"account@example.com"}, time.Now().In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01-02")),
		dailyAPIJSON(owner.Email, []string{"account@example.com"}, "2999-01-01"),
	}
	for _, invalid := range invalidBodies {
		requireStatus(t, apiRequest(t, a, "POST", claudeAccountDailyUsageAPIPath, token.PlaintextToken, "application/json", invalid), 400)
	}
	requireStatus(t, apiRequest(t, a, "POST", claudeAccountDailyUsageAPIPath+"?source=raw", token.PlaintextToken, "application/json", body), 400)
	requireStatus(t, apiRequest(t, a, "POST", claudeAccountDailyUsageAPIPath, token.PlaintextToken, "application/json", strings.Repeat(" ", dailyUsageAPIBodyLimit)+body), 413)
	if err := a.store.DB.Model(&core.UserAPIToken{}).Where("user_id = ?", owner.ID).Update("scope", core.ClaudeSubmitScope).Error; err != nil {
		t.Fatal(err)
	}
	w := apiRequest(t, a, "POST", claudeAccountDailyUsageAPIPath, token.PlaintextToken, "application/json", body)
	requireStatus(t, w, 403)
	if !strings.Contains(w.Body.String(), "INSUFFICIENT_SCOPE") {
		t.Fatal(w.Body.String())
	}
	if err := a.store.DB.Model(&core.User{}).Where("id = ?", owner.ID).Update("status", "disabled").Error; err != nil {
		t.Fatal(err)
	}
	requireStatus(t, apiRequest(t, a, "POST", claudeAccountDailyUsageAPIPath, token.PlaintextToken, "application/json", body), 401)
}

func TestDailyUsageAPIPerUserRateLimit(t *testing.T) {
	a, redis, _ := fixture(t)
	ctx := context.Background()
	owner, _, _ := vendorSession(t, a, "daily-rate@example.com", "member")
	other, _, _ := vendorSession(t, a, "daily-rate-other@example.com", "member")
	token := lookupToken(t, a, owner.ID)
	body := dailyAPIJSON(owner.Email, []string{"missing@example.com"}, "2026-01-05")
	for i := 0; i < 60; i++ {
		requireStatus(t, apiRequest(t, a, "POST", claudeAccountDailyUsageAPIPath, token.PlaintextToken, "application/json", body), 200)
	}
	w := apiRequest(t, a, "POST", claudeAccountDailyUsageAPIPath, token.PlaintextToken, "application/json", body)
	requireStatus(t, w, 429)
	if w.Header().Get("Retry-After") != "60" {
		t.Fatal("incorrect retry window")
	}
	id, _ := core.ParseBusinessID(token.Token.ID)
	rotated, err := a.store.RotateAPIToken(ctx, owner.ID, id, token.Token.Revision)
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, apiRequest(t, a, "POST", claudeAccountDailyUsageAPIPath, rotated.PlaintextToken, "application/json", body), 429)
	otherToken := lookupToken(t, a, other.ID)
	requireStatus(t, apiRequest(t, a, "POST", claudeAccountDailyUsageAPIPath, otherToken.PlaintextToken, "application/json", dailyAPIJSON(other.Email, []string{"missing@example.com"}, "2026-01-05")), 200)
	for _, prefix := range []string{"account-lookup:", "vendor-action:push:"} {
		if a.redis.Exists(ctx, redisPrefix+prefix+fmt.Sprint(owner.ID)).Val() != 0 {
			t.Fatal("batch query consumed another API allowance")
		}
	}
	redis.FastForward(59 * time.Second)
	w = apiRequest(t, a, "POST", claudeAccountDailyUsageAPIPath, rotated.PlaintextToken, "application/json", body)
	requireStatus(t, w, 429)
	if w.Header().Get("Retry-After") != "1" {
		t.Fatal("rate limit did not retain its original window")
	}
	redis.FastForward(time.Second)
	requireStatus(t, apiRequest(t, a, "POST", claudeAccountDailyUsageAPIPath, rotated.PlaintextToken, "application/json", body), 200)
	redis.Close()
	requireStatus(t, apiRequest(t, a, "POST", claudeAccountDailyUsageAPIPath, rotated.PlaintextToken, "application/json", body), 503)
}

func TestDailyUsageAPITimeoutIsNotMissingData(t *testing.T) {
	w := httptest.NewRecorder()
	dailyUsageAPIUnavailable(w, context.Background(), errors.Join(errors.New("query stopped"), context.DeadlineExceeded))
	requireStatus(t, w, 504)
	if !strings.Contains(w.Body.String(), "REQUEST_TIMEOUT") {
		t.Fatal(w.Body.String())
	}
}
