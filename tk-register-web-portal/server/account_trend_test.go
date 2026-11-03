package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"goapi-tk-register/server/internal/core"
)

func TestAccountTrendReadOwnershipPagesAndSeconds(t *testing.T) {
	a, _, _ := fixture(t)
	admin, adminCookie, _ := vendorSession(t, a, "trend-admin@example.com", "admin")
	owner, ownerCookie, _ := vendorSession(t, a, "trend-owner@example.com", "member")
	_, otherCookie, _ := vendorSession(t, a, "trend-other@example.com", "member")
	vendor := configuredVendor(t, a, admin.ID)
	seedAdminSubmission(t, a, owner.ID, vendor, "trend-account@example.com", "accepted", time.Now().UTC())
	account := syncTarget(t, a, owner.ID)
	base := time.Now().UTC().Add(-6 * time.Hour).Truncate(time.Second)
	currency := "CNY"
	cadence := 300
	rows := []core.AccountSnapshot{}
	for i := 0; i < 60; i++ {
		cost := float64(i) * 7.2
		rows = append(rows, core.AccountSnapshot{AccountID: account.ID, IdentityKey: account.IdentityKey, SnapshotDate: base.Format("2006-01-02"), CollectedAt: base.Add(time.Duration(i) * 5 * time.Minute), DataSource: "website", AccountSnapshotValues: core.AccountSnapshotValues{CostTotal: &cost, Currency: &currency, CollectionIntervalSeconds: &cadence}})
	}
	if err := a.store.DB.CreateInBatches(rows, 50).Error; err != nil {
		t.Fatal(err)
	}
	fake := &mockAccountCollector{}
	a.accountCollector = fake
	route := fmt.Sprintf("accounts/%d", account.ID)
	for _, suffix := range []string{"", "/snapshots"} {
		requireStatus(t, request(t, a, "GET", route+suffix, nil, nil, ""), 401)
		requireStatus(t, request(t, a, "GET", route+suffix, nil, otherCookie, ""), 404)
		requireStatus(t, request(t, a, "GET", "admin/synced-"+route+suffix, nil, ownerCookie, ""), 403)
		requireStatus(t, request(t, a, "GET", "admin/synced-"+route+suffix, nil, adminCookie, ""), 200)
	}
	w := request(t, a, "GET", route+"?days=7", nil, ownerCookie, "")
	requireStatus(t, w, 200)
	body := data(t, w)
	if body["interval_seconds"] != float64(300) || body["snapshot_count"] != float64(60) || len(body["snapshots"].([]any)) != 50 {
		t.Fatal("detail contract", body)
	}
	if _, old := body["interval_hours"]; old {
		t.Fatal("legacy units leaked")
	}
	if len(body["trend"].(map[string]any)["cost_total_usd"].([]any)) != 1 {
		t.Fatal("curve missing")
	}
	w = request(t, a, "GET", route+"/snapshots?days=7&page=2&page_size=50", nil, ownerCookie, "")
	requireStatus(t, w, 200)
	if len(data(t, w)["items"].([]any)) != 10 {
		t.Fatal("wrong page")
	}
	for _, query := range []string{"days=1", "page=0", "page_size=101", "page=xyz"} {
		requireStatus(t, request(t, a, "GET", route+"/snapshots?"+query, nil, ownerCookie, ""), 400)
	}
	if fake.collectCalls != 0 || fake.loginCalls != 0 {
		t.Fatal("read triggered upstream")
	}
	var count int64
	if err := a.store.DB.WithContext(context.Background()).Model(&core.AccountSnapshot{}).Count(&count).Error; err != nil || count != 60 {
		t.Fatal("history altered", count, err)
	}
}
