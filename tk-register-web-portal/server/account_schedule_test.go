package main

import (
	"context"
	"testing"
	"time"

	"goapi-tk-register/server/internal/core"
)

func TestAccountScheduleUsesSecondsAndWakesAfterBothSettingsRoutes(t *testing.T) {
	a, _, _ := fixture(t)
	_, cookie, csrf := vendorSession(t, a, "admin@example.com", "admin")
	a.accountSyncWake = make(chan struct{}, 1)
	body := map[string]any{"enabled": true, "interval_seconds": 61, "website_username": "supplier-user", "website_password": "test-password"}
	before := time.Now().UTC()
	w := request(t, a, "PUT", "admin/account-sync", body, cookie, csrf)
	requireStatus(t, w, 200)
	if data(t, w)["interval_seconds"] != float64(61) {
		t.Fatal("seconds not returned")
	}
	select {
	case <-a.accountSyncWake:
	default:
		t.Fatal("dedicated settings did not wake scheduler")
	}
	cfg, err := a.syncConfig(context.Background())
	if err != nil || cfg.NextRunAt == nil || cfg.NextRunAt.Before(before.Add(60*time.Second)) || cfg.NextRunAt.After(time.Now().Add(62*time.Second)) {
		t.Fatal("incorrect first due time", cfg, err)
	}
	w = request(t, a, "PUT", "admin/settings", map[string]any{"settings": map[string]string{"account_sync_interval_seconds": "300"}}, cookie, csrf)
	requireStatus(t, w, 200)
	select {
	case <-a.accountSyncWake:
	default:
		t.Fatal("generic settings did not wake scheduler")
	}
	cfg, err = a.syncConfig(context.Background())
	if err != nil || cfg.IntervalSeconds != 300 || cfg.NextRunAt == nil || cfg.NextRunAt.Before(time.Now().Add(299*time.Second)) {
		t.Fatal("generic settings did not reset due time", cfg, err)
	}
	for _, bad := range []any{59, 604801, 1.5, "300"} {
		w = request(t, a, "PUT", "admin/account-sync", map[string]any{"interval_seconds": bad}, cookie, csrf)
		requireStatus(t, w, 400)
	}
	requireStatus(t, request(t, a, "PUT", "admin/account-sync", map[string]any{"interval_hours": 1}, cookie, csrf), 400)
}

func TestAccountScheduleTimerIsNotQuantizedToPollInterval(t *testing.T) {
	now := time.Now().UTC()
	values := core.Values{"account_sync_enabled": "true", "account_sync_website_username": "u", "account_sync_website_password": "p", "account_sync_interval_seconds": "61"}
	for _, expected := range []time.Duration{time.Second, 7 * time.Second, 29 * time.Second} {
		due := now.Add(expected)
		if wait := accountScheduleWait(values, core.AccountSyncState{NextRunAt: &due}, now); wait != expected {
			t.Fatalf("expected %v got %v", expected, wait)
		}
	}
	due := now.Add(time.Hour)
	if wait := accountScheduleWait(values, core.AccountSyncState{NextRunAt: &due}, now); wait != 30*time.Second {
		t.Fatal("missing cross-instance rescan bound", wait)
	}
	due = now.Add(-time.Second)
	if wait := accountScheduleWait(values, core.AccountSyncState{NextRunAt: &due}, now); wait != 0 {
		t.Fatal("overdue timer delayed", wait)
	}
	values["account_sync_enabled"] = "false"
	if wait := accountScheduleWait(values, core.AccountSyncState{NextRunAt: &due}, now); wait != 30*time.Second {
		t.Fatal("disabled schedule spins", wait)
	}
}
