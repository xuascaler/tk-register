package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"goapi-tk-register/server/internal/core"
	"goapi-tk-register/server/internal/supplier"
)

type mockAccountCollector struct {
	records                  []supplier.Account
	err                      error
	collectCalls, loginCalls int
	username, password       string
}

func (m *mockAccountCollector) Collect(_ context.Context, u, p string) ([]supplier.Account, error) {
	m.collectCalls++
	m.username = u
	m.password = p
	return m.records, m.err
}
func (m *mockAccountCollector) TestLogin(_ context.Context, u, p string) error {
	m.loginCalls++
	m.username = u
	m.password = p
	return m.err
}
func configureSync(t *testing.T, a *app, actor uint64, enabled bool) {
	t.Helper()
	value := "false"
	if enabled {
		value = "true"
	}
	if err := a.store.PatchSettings(context.Background(), map[string]string{"account_sync_enabled": value, "account_sync_website_username": "supplier-user", "account_sync_website_password": "secret-supplier-password"}, actor); err != nil {
		t.Fatal(err)
	}
}
func syncTarget(t *testing.T, a *app, owner uint64) core.SyncedAccount {
	t.Helper()
	rows, err := a.store.CollectionTargets(context.Background(), owner, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("targets: %d %v", len(rows), err)
	}
	return rows[0]
}

func TestAccountsOwnershipCSRFAndReadOnlyViews(t *testing.T) {
	a, _, _ := fixture(t)
	admin, ac, acsrf := vendorSession(t, a, "admin@example.com", "admin")
	owner, oc, ocsrf := vendorSession(t, a, "owner@example.com", "member")
	_, other, _ := vendorSession(t, a, "other@example.com", "member")
	vendor := configuredVendor(t, a, admin.ID)
	seedAdminSubmission(t, a, owner.ID, vendor, "account@example.com", "accepted", time.Now().UTC())
	target := syncTarget(t, a, owner.ID)
	route := "accounts/" + fmt.Sprint(target.ID)
	fake := &mockAccountCollector{}
	a.accountCollector = fake
	requireStatus(t, request(t, a, "GET", "accounts", nil, nil, ""), 401)
	requireStatus(t, request(t, a, "GET", route, nil, other, ""), 404)
	requireStatus(t, request(t, a, "POST", route+"/sync", map[string]any{}, oc, ""), 403)
	requireStatus(t, request(t, a, "GET", "admin/account-sync", nil, oc, ""), 403)
	requireStatus(t, request(t, a, "PUT", "admin/account-sync", map[string]any{}, ac, ""), 403)
	requireStatus(t, request(t, a, "POST", "admin/account-sync/test-login", map[string]any{}, oc, ocsrf), 403)
	requireStatus(t, request(t, a, "POST", route+"/sync", map[string]any{}, oc, ocsrf), 503)
	for _, p := range []string{"accounts", route + "?days=7", route + "?days=30", route + "?days=90"} {
		requireStatus(t, request(t, a, "GET", p, nil, oc, ""), 200)
	}
	requireStatus(t, request(t, a, "GET", route+"?days=1", nil, oc, ""), 400)
	if fake.collectCalls != 0 || fake.loginCalls != 0 {
		t.Fatal("GET or failed authorization performed an upstream request")
	}
	w := request(t, a, "GET", "admin/account-sync", nil, ac, "")
	requireStatus(t, w, 200)
	if data(t, w)["interval_hours"] != float64(8) || data(t, w)["enabled"] != false {
		t.Fatal("incorrect schedule defaults")
	}
	requireStatus(t, request(t, a, "PUT", "admin/account-sync", map[string]any{"enabled": true}, ac, acsrf), 400)
}

func TestAccountSyncSettingsAndUnsavedLogin(t *testing.T) {
	a, m, _ := fixture(t)
	admin, ac, csrf := vendorSession(t, a, "admin@example.com", "admin")
	fake := &mockAccountCollector{}
	a.accountCollector = fake
	body := map[string]any{"website_username": "supplier-user", "website_password": "secret-supplier-password"}
	w := request(t, a, "POST", "admin/account-sync/test-login", body, ac, csrf)
	requireStatus(t, w, 200)
	values, _ := a.store.Settings(context.Background())
	if values.String("account_sync_website_password") != "" || fake.loginCalls != 1 {
		t.Fatal("test login saved credentials")
	}
	body["enabled"] = false
	body["interval_hours"] = 8
	body["timezone"] = "Asia/Shanghai"
	w = request(t, a, "PUT", "admin/account-sync", body, ac, csrf)
	requireStatus(t, w, 200)
	if strings.Contains(w.Body.String(), "secret-supplier-password") || data(t, w)["password_configured"] != true {
		t.Fatal("password exposure or lost configured state")
	}
	m.FastForward(6 * time.Second)
	w = request(t, a, "POST", "admin/account-sync/test-login", map[string]string{"website_username": "supplier-user"}, ac, csrf)
	requireStatus(t, w, 200)
	if fake.password != "secret-supplier-password" {
		t.Fatal("saved password was not used")
	}
	w = request(t, a, "POST", "admin/account-sync/test-login", map[string]string{"website_username": "different-user"}, ac, csrf)
	requireStatus(t, w, 400)
	w = request(t, a, "PUT", "admin/account-sync", map[string]any{"website_username": "different-user", "website_password": ""}, ac, csrf)
	requireStatus(t, w, 400)
	configureSync(t, a, admin.ID, true)
	values, _ = a.store.Settings(context.Background())
	if values.String("account_sync_website_username") != "supplier-user" {
		t.Fatal("invalid patch partly changed credentials")
	}
	m.FastForward(6 * time.Second)
	fake.err = errors.New("raw vendor response with secret-supplier-password")
	w = request(t, a, "POST", "admin/account-sync/test-login", map[string]string{"website_username": "supplier-user"}, ac, csrf)
	requireStatus(t, w, 502)
	if strings.Contains(w.Body.String(), "secret-supplier-password") {
		t.Fatal("unsanitized error")
	}
}

func TestAccountCollectionFreshRunsRetainDataAndSchedule(t *testing.T) {
	a, m, _ := fixture(t)
	admin, _, _ := vendorSession(t, a, "admin@example.com", "admin")
	owner, oc, csrf := vendorSession(t, a, "owner@example.com", "member")
	vendor := configuredVendor(t, a, admin.ID)
	seedAdminSubmission(t, a, owner.ID, vendor, "account@example.com", "accepted", time.Now().UTC())
	configureSync(t, a, admin.ID, false)
	target := syncTarget(t, a, owner.ID)
	active, currency, platform := "active", "CNY", "13152"
	cost := float64(12.5)
	count := int64(123)
	fake := &mockAccountCollector{records: []supplier.Account{{ID: *target.RemoteID, PlatformAccountID: platform, Account: target.Account, Plan: target.Plan, Status: &active, CostTotal: &cost, Currency: &currency, InputTokensTotal: &count}}}
	a.accountCollector = fake
	route := "accounts/" + fmt.Sprint(target.ID)
	w := request(t, a, "POST", route+"/sync", map[string]any{}, oc, csrf)
	requireStatus(t, w, 200)
	result, err := a.store.SyncedAccountForUser(context.Background(), owner.ID, target.ID)
	if err != nil || result.Snapshot == nil || *result.Snapshot.CostTotal != 12.5 || result.LastSuccessAt == nil || result.LastError != "" {
		t.Fatalf("snapshot not saved: %+v %v", result, err)
	}
	if result.PlatformAccountID == nil || *result.PlatformAccountID != platform {
		t.Fatal("website platform ID was not displayed")
	}
	first := result.LastSuccessAt
	requireStatus(t, request(t, a, "POST", route+"/sync", map[string]any{}, oc, csrf), 429)
	m.FastForward(time.Minute)
	fake.err = &supplier.Error{Code: supplier.CodeCredentialsInvalid, Message: "The supplier rejected the username or password."}
	w = request(t, a, "POST", route+"/sync", map[string]any{}, oc, csrf)
	requireStatus(t, w, 200)
	result, err = a.store.SyncedAccountForUser(context.Background(), owner.ID, target.ID)
	if err != nil || result.Snapshot == nil || *result.Snapshot.CostTotal != 12.5 || result.LastError == "" || !result.LastSuccessAt.Equal(*first) {
		t.Fatal("failure replaced last successful data")
	}
	if fake.collectCalls != 2 || fake.username != "supplier-user" || fake.password != "secret-supplier-password" {
		t.Fatal("each run must call the collector with credentials")
	}
	configureSync(t, a, admin.ID, true)
	fake.err = nil
	if err = a.collectAccounts(context.Background(), 0, 0, true); err != nil {
		t.Fatal(err)
	}
	if err = a.collectAccounts(context.Background(), 0, 0, true); err != nil {
		t.Fatal(err)
	}
	if fake.collectCalls != 3 {
		t.Fatal("scheduler ignored interval")
	}
	past := time.Now().Add(-8*time.Hour - time.Minute)
	if err = a.store.DB.Model(&core.AccountSyncState{}).Where("id = ?", 1).Update("last_started_at", past).Error; err != nil {
		t.Fatal(err)
	}
	if err = a.collectAccounts(context.Background(), 0, 0, true); err != nil {
		t.Fatal(err)
	}
	if fake.collectCalls != 4 {
		t.Fatal("next scheduled run did not collect afresh")
	}
	cfg, err := a.syncConfig(context.Background())
	if err != nil || cfg.Running || cfg.LastSuccessAt == nil || cfg.NextRunAt == nil || cfg.LastError != "" {
		t.Fatalf("schedule state: %+v %v", cfg, err)
	}
	if a.redis.Exists(context.Background(), accountSyncLeaseKey).Val() != 0 {
		t.Fatal("lease not released")
	}
	a.redis.Set(context.Background(), accountSyncLeaseKey, "other-worker", time.Minute)
	if err = a.collectAccounts(context.Background(), owner.ID, target.ID, false); !errors.Is(err, errSyncBusy) {
		t.Fatal("concurrent run acquired lease")
	}
	if fake.collectCalls != 4 {
		t.Fatal("concurrent collection ran")
	}
}

func TestAccountIdentityMatchingAndInterval(t *testing.T) {
	remote, platform := "321", "123"
	target := core.SyncedAccount{Account: " Account@Example.com ", Plan: "claude/cc-max-20x", RemoteID: &remote}
	records := []supplier.Account{{ID: remote, PlatformAccountID: platform, Account: "account@example.com", Plan: target.Plan}}
	if _, err := matchSupplierAccount(target, records); err != nil {
		t.Fatal(err)
	}
	target.RemoteID = &platform
	if _, err := matchSupplierAccount(target, records); err != nil {
		t.Fatal("historically imported platform ID should match", err)
	}
	wrong := "wrong"
	target.PlatformAccountID = &wrong
	if _, err := matchSupplierAccount(target, records); err == nil {
		t.Fatal("mismatching platform ID was accepted")
	}
	target.PlatformAccountID = nil
	target.RemoteID = nil
	records = append(records, records[0])
	if _, err := matchSupplierAccount(target, records); err == nil {
		t.Fatal("ambiguous email was matched")
	}
	now := time.Now().UTC()
	v := core.Values{"account_sync_enabled": "true", "account_sync_interval_hours": "8", "account_sync_website_username": "user", "account_sync_website_password": "password"}
	for _, elapsed := range []time.Duration{0, 7 * time.Hour, 8*time.Hour - time.Nanosecond, 8 * time.Hour, 9 * time.Hour} {
		last := now.Add(-elapsed)
		if got := syncDue(v, core.AccountSyncState{LastStartedAt: &last}, now); got != (elapsed >= 8*time.Hour) {
			t.Fatalf("interval boundary %s", elapsed)
		}
	}
	v["account_sync_enabled"] = "false"
	if syncDue(v, core.AccountSyncState{}, now) {
		t.Fatal("disabled schedule ran")
	}
}

func TestAccountCollectionInvalidMetricDoesNotAbortOtherAccounts(t *testing.T) {
	a, _, _ := fixture(t)
	admin, _, _ := vendorSession(t, a, "admin@example.com", "admin")
	owner, oc, csrf := vendorSession(t, a, "owner@example.com", "member")
	vendor := configuredVendor(t, a, admin.ID)
	now := time.Now().UTC()
	first := seedAdminSubmission(t, a, owner.ID, vendor, "invalid@example.com", "accepted", now)
	second := seedAdminSubmission(t, a, owner.ID, vendor, "valid@example.com", "accepted", now.Add(time.Second))
	configureSync(t, a, admin.ID, true)
	invalid := float64(150)
	cost := float64(0)
	currency := "CNY"
	a.accountCollector = &mockAccountCollector{records: []supplier.Account{
		{ID: *first.RemoteID, Account: first.Account, Plan: first.Plan, Usage5HPercent: &invalid},
		{ID: *second.RemoteID, Account: second.Account, Plan: second.Plan, CostTotal: &cost, Currency: &currency},
	}}
	if err := a.collectAccounts(context.Background(), 0, 0, true); err != nil {
		t.Fatal(err)
	}
	page, err := a.store.ListSyncedAccounts(context.Background(), owner.ID, 1, "", "")
	if err != nil {
		t.Fatal(err)
	}
	var invalidID string
	for _, item := range page.Items {
		if item.Account == first.Account {
			invalidID = item.ID
			if item.LastError == "" || item.Snapshot != nil {
				t.Fatal("invalid values should produce a safe failure")
			}
		}
		if item.Account == second.Account && (item.Snapshot == nil || item.Snapshot.CostTotal == nil || *item.Snapshot.CostTotal != 0) {
			t.Fatal("valid account wasn't collected after another failed")
		}
	}
	cfg, err := a.syncConfig(context.Background())
	if err != nil || cfg.LastError == "" || cfg.LastSuccessAt != nil {
		t.Fatal("partial run reported success")
	}
	vendorID, _ := core.ParseBusinessID(vendor.ID)
	if err = a.store.DB.Model(&core.Vendor{}).Where("id = ?", vendorID).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	requireStatus(t, request(t, a, "POST", "accounts/"+invalidID+"/sync", map[string]any{}, oc, csrf), 409)
}
