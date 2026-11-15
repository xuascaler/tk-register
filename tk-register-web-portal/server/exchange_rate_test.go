package main

import (
	"context"
	"fmt"
	"goapi-tk-register/server/internal/core"
	"testing"
	"time"
)

func TestExchangeRateHTTPAuthorizationAndCurrentValue(t *testing.T) {
	a, _, _ := fixture(t)
	_, adminCookie, adminCSRF := vendorSession(t, a, "rate-admin@example.com", "admin")
	_, memberCookie, memberCSRF := vendorSession(t, a, "rate-member@example.com", "member")
	input := map[string]any{"settings": map[string]string{"exchange_rate_cny_per_usd": "8"}}
	requireStatus(t, request(t, a, "GET", "exchange-rate", nil, nil, ""), 401)
	requireStatus(t, request(t, a, "PUT", "admin/settings", input, memberCookie, memberCSRF), 403)
	requireStatus(t, request(t, a, "PUT", "admin/settings", input, adminCookie, ""), 403)
	requireStatus(t, request(t, a, "PUT", "admin/settings", input, adminCookie, adminCSRF), 200)
	w := request(t, a, "GET", "exchange-rate", nil, memberCookie, "")
	requireStatus(t, w, 200)
	result := data(t, w)
	if len(result) != 1 || result["exchange_rate_cny_per_usd"] != float64(8) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Body.String())
	}
	input["settings"] = map[string]string{"exchange_rate_cny_per_usd": "0"}
	requireStatus(t, request(t, a, "PUT", "admin/settings", input, adminCookie, adminCSRF), 400)
	w = request(t, a, "GET", "exchange-rate", nil, memberCookie, "")
	if data(t, w)["exchange_rate_cny_per_usd"] != float64(8) {
		t.Fatal("invalid update changed the rate")
	}
}

func TestConfiguredExchangeRateInSyncAndTrendWithoutNestedQueryDeadlock(t *testing.T) {
	a, owner, cookie, csrf, rows, _ := setupPageSync(t)
	ctx := context.Background()
	if err := a.store.PatchSettings(ctx, map[string]string{"exchange_rate_cny_per_usd": "8"}, owner.ID); err != nil {
		t.Fatal(err)
	}
	w := request(t, a, "POST", "accounts/sync", pageInput(rows[0]), cookie, csrf)
	requireStatus(t, w, 200)
	out := readPageResponse(t, w.Body.Bytes())
	snapshot := out.Accounts[0].Snapshot
	if snapshot == nil || snapshot.CostTotalUSD == nil || *snapshot.CostTotalUSD != 123.45/8 || snapshot.ExchangeRateCNYPerUSD != 8 {
		t.Fatal("manual sync ignored configured rate")
	}
	sqlDB, err := a.store.DB.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	deadline, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	trend, err := a.store.AccountTrend(deadline, rows[0].ID, 7, 300)
	if err != nil {
		t.Fatal("trend blocked or failed", err)
	}
	if got := trend.Trend["cost_total_usd"]; len(got) != 1 || len(got[0]) != 1 || got[0][0].Value != 123.45/8 {
		t.Fatal("trend ignored configured rate", got)
	}
	history, err := a.store.AccountSnapshots(ctx, rows[0].ID, 7)
	if err != nil || len(history) != 1 || history[0].ExchangeRateCNYPerUSD != 8 {
		t.Fatal("history ignored configured rate", err)
	}
	w = request(t, a, "GET", fmt.Sprintf("accounts/%d", rows[0].ID), nil, cookie, "")
	requireStatus(t, w, 200)
	var stored core.AccountSnapshot
	if err := a.store.DB.Where("account_id = ?", rows[0].ID).First(&stored).Error; err != nil || stored.CostTotal == nil || *stored.CostTotal != 123.45 || *stored.Currency != "CNY" {
		t.Fatal("original observation changed", err)
	}
}

func TestConfiguredExchangeRateInAuthJSONAPIResponse(t *testing.T) {
	a, owner, _, _, target, _ := setupAuthUpdate(t)
	ctx := context.Background()
	if err := a.store.PatchSettings(ctx, map[string]string{"exchange_rate_cny_per_usd": "8"}, owner.ID); err != nil {
		t.Fatal(err)
	}
	cost, currency := 80.0, "CNY"
	if err := a.store.RecordAccountSnapshot(ctx, target.ID, target.IdentityKey, core.AccountSnapshotValues{CostTotal: &cost, Currency: &currency}, time.Now().Add(-time.Minute), "UTC"); err != nil {
		t.Fatal(err)
	}
	token := lookupToken(t, a, owner.ID)
	w := apiRequest(t, a, "POST", claudeAccountAuthJSONAPIPath, token.PlaintextToken, "application/json", authAPIBody(t, owner.Email, target.Account, false))
	requireStatus(t, w, 200)
	out := authAPIResult(t, w)
	if out.Account == nil || out.Account.Snapshot == nil || out.Account.Snapshot.CostTotalUSD == nil || *out.Account.Snapshot.CostTotalUSD != 10 || out.Account.Snapshot.ExchangeRateCNYPerUSD != 8 {
		t.Fatal("Auth JSON response ignored configured rate")
	}
}
