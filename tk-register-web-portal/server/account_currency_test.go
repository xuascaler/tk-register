package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"goapi-tk-register/server/internal/core"
)

func assertSnapshotCurrencyContract(t *testing.T, snapshot map[string]any, cost *float64, currency string) {
	t.Helper()
	if snapshot["currency"] != currency || snapshot["exchange_rate_cny_per_usd"] != 7.2 {
		t.Fatalf("currency or rate missing: %+v", snapshot)
	}
	if cost == nil {
		if value, exists := snapshot["cost_total"]; !exists || value != nil {
			t.Fatal("missing original cost became a value", snapshot)
		}
	} else if snapshot["cost_total"] != *cost {
		t.Fatal("original cost changed", snapshot)
	}
	usd, exists := snapshot["cost_total_usd"]
	if !exists {
		t.Fatal("USD field omitted")
	}
	if cost == nil || currency != "CNY" {
		if usd != nil {
			t.Fatal("unavailable USD amount became a value", snapshot)
		}
	} else if usd != *cost/7.2 {
		t.Fatal("incorrect or prematurely rounded USD amount", snapshot)
	}
}

func TestAccountCurrencyContractAcrossListsDetailsHistoryAndPublicLookup(t *testing.T) {
	a, _, _ := fixture(t)
	admin, adminCookie, _ := vendorSession(t, a, "currency-admin@example.com", "admin")
	owner, ownerCookie, _ := vendorSession(t, a, "currency-owner@example.com", "member")
	vendor := configuredVendor(t, a, admin.ID)
	base := time.Now().UTC().Add(-time.Hour)
	seedAdminSubmission(t, a, owner.ID, vendor, "currency@example.com", "accepted", base.Add(-time.Hour))
	target := syncTarget(t, a, owner.ID)
	token := lookupToken(t, a, owner.ID)
	cost, zero := 123.45, 0.0
	samples := []struct {
		cost     *float64
		currency string
	}{{&cost, "CNY"}, {&zero, "CNY"}, {nil, "CNY"}, {&cost, "USD"}}
	for i, sample := range samples {
		if err := a.store.RecordAccountSnapshot(context.Background(), target.ID, target.IdentityKey, core.AccountSnapshotValues{CostTotal: sample.cost, Currency: &sample.currency}, base.Add(time.Duration(i)*time.Minute), "UTC"); err != nil {
			t.Fatal(err)
		}
		for _, isAdmin := range []bool{false, true} {
			path, cookie := "accounts", ownerCookie
			if isAdmin {
				path, cookie = "admin/synced-accounts", adminCookie
			}
			listResponse := request(t, a, "GET", path, nil, cookie, "")
			requireStatus(t, listResponse, 200)
			items := data(t, listResponse)["items"].([]any)
			if len(items) != 1 {
				t.Fatal("unexpected account list length", len(items))
			}
			assertSnapshotCurrencyContract(t, items[0].(map[string]any)["snapshot"].(map[string]any), sample.cost, sample.currency)
			detailResponse := request(t, a, "GET", path+"/"+fmt.Sprint(target.ID)+"?days=7", nil, cookie, "")
			requireStatus(t, detailResponse, 200)
			detail := data(t, detailResponse)
			assertSnapshotCurrencyContract(t, detail["account"].(map[string]any)["snapshot"].(map[string]any), sample.cost, sample.currency)
			history := detail["snapshots"].([]any)
			if len(history) != i+1 {
				t.Fatal("history dropped observations", len(history))
			}
			for j, snapshot := range history {
				assertSnapshotCurrencyContract(t, snapshot.(map[string]any), samples[j].cost, samples[j].currency)
			}
		}
		publicResponse := apiRequest(t, a, "GET", lookupURL(owner.Email, target.Account), token.PlaintextToken, "", "")
		requireStatus(t, publicResponse, 200)
		assertSnapshotCurrencyContract(t, data(t, publicResponse)["snapshot"].(map[string]any), sample.cost, sample.currency)
	}
}
