package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"goapi-tk-register/server/common"
	"goapi-tk-register/server/internal/core"
	"goapi-tk-register/server/internal/supplier"
)

type mockPageCollector struct {
	calls   int
	targets []supplier.Target
	run     func(context.Context, []supplier.Target) ([]supplier.AccountResult, error)
}

func (m *mockPageCollector) CollectTargets(ctx context.Context, u, p string, targets []supplier.Target) ([]supplier.AccountResult, error) {
	m.calls++
	m.targets = append([]supplier.Target(nil), targets...)
	return m.run(ctx, targets)
}
func pageRecord(target supplier.Target) supplier.AccountResult {
	status, currency := "active", "CNY"
	cost := 123.45
	five, seven := 5.0, 42.0
	input, output := int64(120), int64(42)
	return supplier.AccountResult{Target: target, Account: &supplier.Account{ID: "450001", PlatformAccountID: "15001", Account: target.Account, Plan: "claude/cc-max-20x", Status: &status, CostTotal: &cost, Currency: &currency, Usage5HPercent: &five, Usage7DPercent: &seven, InputTokensTotal: &input, OutputTokensTotal: &output}}
}
func setupPageSync(t *testing.T) (*app, *core.User, *http.Cookie, string, []core.SyncedAccount, *mockPageCollector) {
	t.Helper()
	a, _, _ := fixture(t)
	admin := addUser(t, a, "admin@example.com", "admin")
	owner, cookie, csrf := vendorSession(t, a, "owner@example.com", "member")
	vendor := configuredVendor(t, a, admin.ID)
	configureSync(t, a, admin.ID, false)
	for i, email := range []string{"one@example.com", "two@example.com", "unaccepted@example.com"} {
		status := "accepted"
		if i == 2 {
			status = "failed"
		}
		seedAdminSubmission(t, a, owner.ID, vendor, email, status, time.Now().Add(time.Duration(i)*time.Second))
	}
	if err := a.store.EnsureSyncedAccounts(context.Background()); err != nil {
		t.Fatal(err)
	}
	var rows []core.SyncedAccount
	if err := a.store.DB.Where("user_id = ?", owner.ID).Order("id").Find(&rows).Error; err != nil || len(rows) != 3 {
		t.Fatal(err, len(rows))
	}
	fake := &mockPageCollector{run: func(ctx context.Context, targets []supplier.Target) ([]supplier.AccountResult, error) {
		out := []supplier.AccountResult{}
		for _, target := range targets {
			out = append(out, pageRecord(target))
		}
		return out, nil
	}}
	a.accountPageCollector = fake
	return a, owner, cookie, csrf, rows, fake
}
func pageInput(rows ...core.SyncedAccount) map[string]any {
	ids := []string{}
	for _, row := range rows {
		ids = append(ids, fmt.Sprint(row.ID))
	}
	return map[string]any{"account_ids": ids}
}
func readPageResponse(t *testing.T, raw []byte) pageSyncResponse {
	t.Helper()
	var envelope struct {
		Data pageSyncResponse `json:"data"`
	}
	if err := common.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

func TestPageSyncOwnershipCSRFAndBounds(t *testing.T) {
	a, _, cookie, csrf, rows, fake := setupPageSync(t)
	requireStatus(t, request(t, a, "POST", "accounts/sync", pageInput(rows[0]), nil, ""), 401)
	requireStatus(t, request(t, a, "POST", "accounts/sync", pageInput(rows[0]), cookie, ""), 403)
	requireStatus(t, request(t, a, "POST", "admin/synced-accounts/sync", pageInput(rows[0]), cookie, csrf), 403)
	requireStatus(t, request(t, a, "GET", "accounts/sync", nil, cookie, ""), 405)
	invalid := []any{map[string]any{}, map[string]any{"account_ids": []string{}}, map[string]any{"account_ids": []string{"1", "1"}}, map[string]any{"account_ids": []string{"0"}}, map[string]any{"account_ids": []string{"-1"}}, map[string]any{"account_ids": []string{"x"}}, map[string]any{"account_ids": []int{1}}, map[string]any{"account_ids": []string{fmt.Sprint(rows[0].ID)}, "account": "injected@example.com"}}
	tooMany := []string{}
	for i := 1; i <= 21; i++ {
		tooMany = append(tooMany, fmt.Sprint(i))
	}
	invalid = append(invalid, map[string]any{"account_ids": tooMany})
	for _, body := range invalid {
		requireStatus(t, request(t, a, "POST", "accounts/sync", body, cookie, csrf), 400)
	}
	other, oc, ocsrf := vendorSession(t, a, "other@example.com", "member")
	vendor := configuredVendor(t, a, other.ID)
	seedAdminSubmission(t, a, other.ID, vendor, "foreign@example.com", "accepted", time.Now())
	foreign := syncTarget(t, a, other.ID)
	requireStatus(t, request(t, a, "POST", "accounts/sync", pageInput(rows[0], foreign), cookie, csrf), 404)
	requireStatus(t, request(t, a, "POST", "accounts/sync", pageInput(rows[0]), oc, ocsrf), 404)
	requireStatus(t, request(t, a, "POST", "accounts/sync", map[string]any{"account_ids": []string{"999999"}}, cookie, csrf), 404)
	if fake.calls != 0 {
		t.Fatal("authorization contacted supplier")
	}
	// A personal API token cannot authenticate this session-only endpoint.
	r, _ := http.NewRequest("POST", "http://fixture/tk-register/api/accounts/sync", strings.NewReader(`{"account_ids":["1"]}`))
	r.Header.Set("Origin", a.origin)
	r.Header.Set("Authorization", "Bearer tkr_not-a-session")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	requireStatus(t, w, 401)
}

func TestPageSyncPartialResultsAndLastSuccess(t *testing.T) {
	a, owner, cookie, csrf, rows, fake := setupPageSync(t)
	at := time.Now().UTC().Add(-2 * time.Hour)
	cost := 8.0
	currency := "CNY"
	if err := a.store.RecordAccountSnapshot(context.Background(), rows[1].ID, rows[1].IdentityKey, core.AccountSnapshotValues{CostTotal: &cost, Currency: &currency}, at, "Asia/Shanghai"); err != nil {
		t.Fatal(err)
	}
	confirmed := "450123"
	if err := a.store.ConfirmSupplierRecord(context.Background(), rows[0].ID, rows[0].IdentityKey, confirmed); err != nil {
		t.Fatal(err)
	}
	fake.run = func(ctx context.Context, targets []supplier.Target) ([]supplier.AccountResult, error) {
		return []supplier.AccountResult{pageRecord(targets[0]), {Target: targets[1], Err: &supplier.Error{Code: supplier.CodeAccountNotFound, Message: "private-access-token"}}}, nil
	}
	w := request(t, a, "POST", "accounts/sync", pageInput(rows...), cookie, csrf)
	requireStatus(t, w, 200)
	assertNoSecrets(t, w.Body.String())
	out := readPageResponse(t, w.Body.Bytes())
	if fake.calls != 1 || len(fake.targets) != 2 || fake.targets[0].RecordID != confirmed || fake.targets[1].RecordID != "" {
		t.Fatal("wrong collection targets", fake.targets)
	}
	if out.UpdatedCount != 1 || out.FailedCount != 1 || out.SkippedCount != 1 || len(out.Accounts) != 3 {
		t.Fatalf("response %+v", out)
	}
	if out.Accounts[0].Snapshot == nil || *out.Accounts[0].Snapshot.CostTotal != 123.45 || out.Accounts[0].LastSuccessAt.Before(at) || out.Accounts[0].UserID != "" {
		t.Fatal("successful row not refreshed")
	}
	updatedSnapshot := out.Accounts[0].Snapshot
	if updatedSnapshot.CostTotalUSD == nil || *updatedSnapshot.CostTotalUSD != *updatedSnapshot.CostTotal/7.2 || updatedSnapshot.ExchangeRateCNYPerUSD != 7.2 {
		t.Fatal("row sync omitted or rounded derived USD values")
	}
	failed, _ := a.store.SyncedAccountForUser(context.Background(), owner.ID, rows[1].ID)
	if failed.Snapshot == nil || *failed.Snapshot.CostTotal != 8 || !failed.LastSuccessAt.Equal(at) || failed.LastAttemptAt == nil || !failed.LastAttemptAt.After(at) || failed.LastError == "" {
		t.Fatal("failed row lost saved data", failed)
	}
	if out.Accounts[2].LastAttemptAt != nil || out.Results[2].Status != "skipped" {
		t.Fatal("skipped row was collected")
	}
	if a.redis.Exists(context.Background(), accountSyncLeaseKey).Val() != 0 {
		t.Fatal("lease leaked")
	}
}

func TestPageSyncGlobalFailureAndIdentityGuards(t *testing.T) {
	for _, scenario := range []string{"login", "missing", "duplicate", "email", "plan", "identity"} {
		t.Run(scenario, func(t *testing.T) {
			a, _, cookie, csrf, rows, fake := setupPageSync(t)
			fake.run = func(ctx context.Context, targets []supplier.Target) ([]supplier.AccountResult, error) {
				record := pageRecord(targets[0])
				switch scenario {
				case "login":
					return nil, &supplier.Error{Code: supplier.CodeCredentialsInvalid, Message: "private-access-token"}
				case "missing":
					return nil, nil
				case "duplicate":
					return []supplier.AccountResult{record, record}, nil
				case "email":
					record.Account.Account = "other@example.com"
				case "plan":
					record.Account.Plan = "codex/pro"
				case "identity":
					var sub core.Submission
					a.store.DB.First(&sub, *rows[0].CurrentSubmissionID)
					sub.ID = 0
					sub.ClientRequestKey = "new-page-sync-lifetime"
					sub.CreatedAt = time.Now().Add(time.Hour)
					sub.UpdatedAt = sub.CreatedAt
					remote := "999999"
					sub.RemoteID = &remote
					if err := a.store.DB.Create(&sub).Error; err != nil {
						t.Fatal(err)
					}
					// Real acceptance now projects the new lifetime in its write transaction.
					// This direct database fixture must do the same before the old query ends.
					if err := a.store.EnsureSyncedAccounts(ctx); err != nil {
						t.Fatal(err)
					}
				}
				return []supplier.AccountResult{record}, nil
			}
			w := request(t, a, "POST", "accounts/sync", pageInput(rows[0]), cookie, csrf)
			requireStatus(t, w, 200)
			assertNoSecrets(t, w.Body.String())
			out := readPageResponse(t, w.Body.Bytes())
			if out.FailedCount != 1 || out.UpdatedCount != 0 || out.Accounts[0].Snapshot != nil {
				t.Fatal("unsafe collection accepted", w.Body.String())
			}
			if scenario == "identity" && out.Accounts[0].LastAttemptAt != nil {
				t.Fatal("stale result altered new identity")
			}
		})
	}
}

func TestPageSyncAdminSkipConfigAndRate(t *testing.T) {
	a, owner, cookie, csrf, rows, fake := setupPageSync(t)
	_, ac, acsrf := vendorSession(t, a, "admin2@example.com", "admin")
	w := request(t, a, "POST", "admin/synced-accounts/sync", pageInput(rows[0]), ac, acsrf)
	requireStatus(t, w, 200)
	out := readPageResponse(t, w.Body.Bytes())
	if out.Accounts[0].UserID != fmt.Sprint(owner.ID) || out.Accounts[0].UserEmail != owner.Email {
		t.Fatal("admin owner missing")
	}
	for _, change := range []map[string]any{{"enabled": false}, {"enabled": true, "base_url": "https://untrusted.example"}, {"base_url": supplier.Origin, "code": "unsupported"}} {
		a.store.DB.Model(&core.Vendor{}).Where("id = ?", rows[0].VendorID).Updates(change)
		before := fake.calls
		w = request(t, a, "POST", "accounts/sync", pageInput(rows[0]), cookie, csrf)
		requireStatus(t, w, 200)
		if readPageResponse(t, w.Body.Bytes()).SkippedCount != 1 || fake.calls != before {
			t.Fatal("unsupported vendor queried")
		}
	}
	a.store.DB.Model(&core.Vendor{}).Where("id = ?", rows[0].VendorID).Updates(map[string]any{"enabled": true, "base_url": supplier.Origin, "code": "tk-register-0"})
	if err := a.store.DB.Model(&core.Setting{}).Where("key = ?", "account_sync_website_password").Update("value", "").Error; err != nil {
		t.Fatal(err)
	}
	requireStatus(t, request(t, a, "POST", "accounts/sync", pageInput(rows[0]), cookie, csrf), 503)
	// Four owner attempts above count toward the 200-attempt minute. All-skipped
	// batches need no login configuration and still consume that allowance.
	for i := 0; i < 196; i++ {
		requireStatus(t, request(t, a, "POST", "accounts/sync", pageInput(rows[2]), cookie, csrf), 200)
	}
	w = request(t, a, "POST", "accounts/sync", pageInput(rows[2]), cookie, csrf)
	requireStatus(t, w, 429)
	key := redisPrefix + "account-sync:page-rate:" + fmt.Sprint(owner.ID)
	if w.Header().Get("Retry-After") != "60" || a.redis.TTL(context.Background(), key).Val() != time.Minute {
		t.Fatal("page sync rate limit window changed")
	}
	// An admin syncing the same owner's row uses the admin's own allowance.
	requireStatus(t, request(t, a, "POST", "admin/synced-accounts/sync", pageInput(rows[2]), ac, acsrf), 200)
	if fake.calls != 1 {
		t.Fatal("skipped or rate-limited request contacted supplier")
	}
}

func TestPageSyncWaitsForLeaseThenCollectsFresh(t *testing.T) {
	a, _, cookie, csrf, rows, fake := setupPageSync(t)
	a.redis.Set(context.Background(), accountSyncLeaseKey, "scheduled", time.Minute)
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(150 * time.Millisecond)
		a.redis.Del(context.Background(), accountSyncLeaseKey)
	}()
	w := request(t, a, "POST", "accounts/sync", pageInput(rows[0]), cookie, csrf)
	<-done
	requireStatus(t, w, 200)
	if fake.calls != 1 || readPageResponse(t, w.Body.Bytes()).UpdatedCount != 1 {
		t.Fatal("used stale data after lease")
	}
	a.redis.Set(context.Background(), accountSyncLeaseKey, "auth-update", time.Minute)
	fake.calls = 0
	w = request(t, a, "POST", "accounts/sync", pageInput(rows[0]), cookie, csrf)
	requireStatus(t, w, 409)
	if fake.calls != 0 {
		t.Fatal("raced existing lease")
	}
}

func TestPageSyncDeduplicatesSharedSupplierAccount(t *testing.T) {
	a, _, _, _, rows, fake := setupPageSync(t)
	other := addUser(t, a, "shared@example.com", "member")
	_, ac, acsrf := vendorSession(t, a, "admin3@example.com", "admin")
	vendor := configuredVendor(t, a, other.ID)
	seedAdminSubmission(t, a, other.ID, vendor, rows[0].Account, "accepted", time.Now())
	foreign := syncTarget(t, a, other.ID)
	w := request(t, a, "POST", "admin/synced-accounts/sync", pageInput(rows[0], foreign), ac, acsrf)
	requireStatus(t, w, 200)
	if len(fake.targets) != 1 || readPageResponse(t, w.Body.Bytes()).UpdatedCount != 2 {
		t.Fatal("shared account duplicated supplier query", w.Body.String())
	}
}

func TestPageSyncAllowsChangedClaudePlan(t *testing.T) {
	a, _, cookie, csrf, rows, fake := setupPageSync(t)
	fake.run = func(ctx context.Context, targets []supplier.Target) ([]supplier.AccountResult, error) {
		result := pageRecord(targets[0])
		result.Account.Plan = "claude/cc-pro"
		return []supplier.AccountResult{result}, nil
	}
	w := request(t, a, "POST", "accounts/sync", pageInput(rows[0]), cookie, csrf)
	requireStatus(t, w, 200)
	if readPageResponse(t, w.Body.Bytes()).UpdatedCount != 1 {
		t.Fatal("changed Claude plan incorrectly rejected", w.Body.String())
	}
}
