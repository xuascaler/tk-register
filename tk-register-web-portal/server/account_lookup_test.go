package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"goapi-tk-register/server/common"
	"goapi-tk-register/server/internal/core"
)

func lookupURL(username, account string) string {
	return claudeAccountLookupAPIPath + "?" + url.Values{"username": {username}, "account": {account}}.Encode()
}

func lookupToken(t *testing.T, a *app, owner uint64) core.APITokenResult {
	t.Helper()
	token, err := a.store.GenerateAPIToken(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func lookupData(t *testing.T, w *httptest.ResponseRecorder) accountLookupResult {
	t.Helper()
	var result struct {
		Data accountLookupResult `json:"data"`
	}
	if err := common.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.Data
}

func TestAccountLookupOwnerAndSnapshotContract(t *testing.T) {
	a, _, _ := fixture(t)
	ctx := context.Background()
	admin, _, _ := vendorSession(t, a, "lookup-admin@example.com", "admin")
	owner, cookie, _ := vendorSession(t, a, "lookup-owner@example.com", "member")
	other, _, _ := vendorSession(t, a, "lookup-other@example.com", "member")
	vendor := configuredVendor(t, a, admin.ID)
	now := time.Now().UTC().Truncate(time.Second)
	seedAdminSubmission(t, a, owner.ID, vendor, " Account@Example.com ", "accepted", now.Add(-time.Hour))
	seedAdminSubmission(t, a, owner.ID, vendor, "account@example.com", "failed", now)
	seedAdminSubmission(t, a, other.ID, vendor, "account@example.com", "accepted", now.Add(time.Second))
	target := syncTarget(t, a, owner.ID)
	zero, count, active, currency, platform := 0.0, int64(123), "active", "CNY", "9001"
	values := core.AccountSnapshotValues{CostTotal: &zero, InputTokensTotal: &count, AccountStatus: &active, Currency: &currency, PlatformAccountID: &platform}
	if err := a.store.RecordAccountSnapshot(ctx, target.ID, target.IdentityKey, values, now.Add(-30*time.Minute), "Asia/Shanghai"); err != nil {
		t.Fatal(err)
	}
	if err := a.store.RecordAccountFailure(ctx, target.ID, target.IdentityKey, "Supplier connection failed.", now); err != nil {
		t.Fatal(err)
	}
	fake := &mockAccountCollector{}
	a.accountCollector = fake
	a.vendorTransport = vendorRoundTrip(func(r *http.Request) (*http.Response, error) {
		t.Error("lookup contacted supplier")
		return remoteResponse(503, `{}`), nil
	})
	token := lookupToken(t, a, owner.ID)
	path := lookupURL(" LOOKUP-OWNER@EXAMPLE.COM ", " ACCOUNT@EXAMPLE.COM ")
	w := apiRequest(t, a, "GET", path, token.PlaintextToken, "", "")
	requireStatus(t, w, 200)
	out := lookupData(t, w)
	if out.ID != fmt.Sprint(target.ID) || out.Username != owner.Email || out.Vendor != "tk-register-0" || out.SubmissionStatus != "failed" || out.IntervalHours != 2 {
		t.Fatalf("wrong account metadata: %+v", out)
	}
	if out.Snapshot == nil || out.Snapshot.CostTotal == nil || *out.Snapshot.CostTotal != 0 || out.Snapshot.OutputTokensTotal != nil || *out.Snapshot.AccountStatus != "active" || out.LastError == nil || out.LastSuccessAt == nil {
		t.Fatalf("lost retained, zero or nullable data: %+v", out)
	}
	if out.Snapshot.CostTotalUSD == nil || *out.Snapshot.CostTotalUSD != 0 || out.Snapshot.ExchangeRateCNYPerUSD != 7.2 {
		t.Fatal("public lookup lost derived USD zero or exchange rate")
	}
	if out.PlatformAccountID == nil || *out.PlatformAccountID != platform || out.RemoteID == nil || *out.RemoteID != *target.RemoteID {
		t.Fatal("account IDs were mixed")
	}
	saved, err := a.store.SyncedAccountForUser(ctx, owner.ID, target.ID)
	if err != nil || !reflect.DeepEqual(out.Snapshot, saved.Snapshot) {
		t.Fatal("lookup differs from Accounts data", err)
	}
	if w.Header().Get("X-Request-ID") == "" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing correlation or private response header")
	}
	for _, secret := range []string{"password", "auth_json", "private-access-token", "mock-platform-access-token", token.PlaintextToken, "secret_parameters"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("response leaked private field", secret)
		}
	}
	// Neither an admin token nor the supplied username can change the owner scope.
	adminToken := lookupToken(t, a, admin.ID)
	for _, query := range []struct{ username, account, token string }{
		{other.Email, "account@example.com", token.PlaintextToken},
		{owner.Email, "account@example.com", adminToken.PlaintextToken},
		{admin.Email, "account@example.com", adminToken.PlaintextToken},
		{owner.Email, "account@", token.PlaintextToken},
		{owner.Email, "not-found@example.com", token.PlaintextToken},
	} {
		w = apiRequest(t, a, "GET", lookupURL(query.username, query.account), query.token, "", "")
		requireStatus(t, w, 404)
		if !strings.Contains(w.Body.String(), `"code":"ACCOUNT_NOT_FOUND"`) {
			t.Fatal(w.Body.String())
		}
	}
	// A browser session alone must not authenticate the public API.
	requireStatus(t, request(t, a, "GET", strings.TrimPrefix(path, basePath+"/api/"), nil, cookie, ""), 401)
	if fake.collectCalls != 0 || fake.loginCalls != 0 {
		t.Fatal("lookup triggered website login/collection")
	}
	var countExecutions int64
	a.store.DB.Model(&core.ProtocolExecution{}).Count(&countExecutions)
	if countExecutions != 0 {
		t.Fatal("lookup created a protocol execution")
	}
}

func TestAccountLookupNoSnapshotAndConfiguration(t *testing.T) {
	a, _, _ := fixture(t)
	owner, _, _ := vendorSession(t, a, "no-snapshot@example.com", "member")
	vendor := configuredVendor(t, a, owner.ID)
	seedAdminSubmission(t, a, owner.ID, vendor, "pending@example.com", "unknown", time.Now().UTC())
	token := lookupToken(t, a, owner.ID)
	if err := a.store.PatchSettings(context.Background(), map[string]string{"account_sync_interval_hours": "3"}, owner.ID); err != nil {
		t.Fatal(err)
	}
	w := apiRequest(t, a, "GET", lookupURL(owner.Email, "pending@example.com"), token.PlaintextToken, "", "")
	requireStatus(t, w, 200)
	out := lookupData(t, w)
	if out.Snapshot != nil || out.RemoteID != nil || out.PlatformAccountID != nil || out.LastError != nil || out.LastSuccessAt != nil || out.LastAttemptAt != nil || out.IntervalHours != 3 {
		t.Fatalf("unknown metrics became values: %+v", out)
	}
	for _, field := range []string{"snapshot", "last_error", "remote_id", "last_success_at"} {
		if !strings.Contains(w.Body.String(), `"`+field+`":null`) {
			t.Fatal("missing explicit null", field)
		}
	}
}

func TestAccountLookupAuthenticationAndRequestValidation(t *testing.T) {
	a, _, _ := fixture(t)
	owner, _, _ := vendorSession(t, a, "validation@example.com", "member")
	token := lookupToken(t, a, owner.ID)
	path := lookupURL(owner.Email, "account@example.com")
	for _, raw := range []string{"", "invalid"} {
		requireStatus(t, apiRequest(t, a, "GET", path, raw, "", ""), 401)
	}
	for _, method := range []string{"POST", "PUT", "DELETE", "HEAD"} {
		w := apiRequest(t, a, method, path, token.PlaintextToken, "", "")
		requireStatus(t, w, 405)
		if w.Header().Get("Allow") != "GET" {
			t.Fatal("wrong Allow header")
		}
	}
	r := httptest.NewRequest("GET", path, nil)
	r.Header.Add("Authorization", "Bearer "+token.PlaintextToken)
	r.Header.Add("Authorization", "Bearer "+token.PlaintextToken)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	requireStatus(t, w, 401)
	for _, query := range []string{
		"", "username=" + owner.Email, "account=a", "username=&account=a", "username=invalid&account=a",
		"username=" + owner.Email + "&account=", "username=" + owner.Email + "&account=a&account=b",
		"username=" + owner.Email + "&username=other@example.com&account=a",
		"username=" + owner.Email + "&account=a&token=x", "username=" + owner.Email + "&account=%ZZ",
		"username=" + owner.Email + "&account=%FF", "username=" + owner.Email + "&account=a+b",
		"username=" + owner.Email + "&account=a%00b", "username=" + owner.Email + "&account=" + strings.Repeat("a", 255),
	} {
		t.Run(query, func(t *testing.T) {
			requireStatus(t, apiRequest(t, a, "GET", claudeAccountLookupAPIPath+"?"+query, token.PlaintextToken, "", ""), 400)
		})
	}
	requireStatus(t, apiRequest(t, a, "GET", path, token.PlaintextToken, "application/json", `{}`), 400)
	// Explicitly insufficient scope is distinct from an invalid or inactive token.
	if err := a.store.DB.Model(&core.UserAPIToken{}).Where("user_id = ?", owner.ID).Update("scope", core.ClaudeSubmitScope).Error; err != nil {
		t.Fatal(err)
	}
	w = apiRequest(t, a, "GET", path, token.PlaintextToken, "", "")
	requireStatus(t, w, 403)
	if !strings.Contains(w.Body.String(), `"code":"INSUFFICIENT_SCOPE"`) {
		t.Fatal(w.Body.String())
	}
	if err := a.store.DB.Model(&core.User{}).Where("id = ?", owner.ID).Update("status", "disabled").Error; err != nil {
		t.Fatal(err)
	}
	requireStatus(t, apiRequest(t, a, "GET", path, token.PlaintextToken, "", ""), 401)
}

func TestAccountLookupPerUserRateLimit(t *testing.T) {
	a, m, _ := fixture(t)
	ctx := context.Background()
	owner, _, _ := vendorSession(t, a, "rate-owner@example.com", "member")
	other, _, _ := vendorSession(t, a, "rate-other@example.com", "member")
	token := lookupToken(t, a, owner.ID)
	path := lookupURL(owner.Email, "missing@example.com")
	for i := 0; i < 60; i++ {
		requireStatus(t, apiRequest(t, a, "GET", path, token.PlaintextToken, "", ""), 404)
	}
	w := apiRequest(t, a, "GET", path, token.PlaintextToken, "", "")
	requireStatus(t, w, 429)
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After")
	}
	id, _ := core.ParseBusinessID(token.Token.ID)
	rotated, err := a.store.RotateAPIToken(ctx, owner.ID, id, token.Token.Revision)
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, apiRequest(t, a, "GET", path, rotated.PlaintextToken, "", ""), 429)
	otherToken := lookupToken(t, a, other.ID)
	requireStatus(t, apiRequest(t, a, "GET", lookupURL(other.Email, "missing@example.com"), otherToken.PlaintextToken, "", ""), 404)
	if a.redis.Exists(ctx, redisPrefix+"vendor-action:push:"+fmt.Sprint(owner.ID)).Val() != 0 {
		t.Fatal("lookup consumed submission allowance")
	}
	m.FastForward(61 * time.Second)
	requireStatus(t, apiRequest(t, a, "GET", path, rotated.PlaintextToken, "", ""), 404)
	for _, key := range a.redis.Keys(ctx, "*").Val() {
		if !strings.HasPrefix(key, redisPrefix) || strings.Contains(key, token.PlaintextToken) {
			t.Fatal("unsafe Redis key", key)
		}
	}
	m.Close()
	w = apiRequest(t, a, "GET", path, rotated.PlaintextToken, "", "")
	requireStatus(t, w, 503)
	if strings.Contains(w.Body.String(), "dial") {
		t.Fatal("raw Redis error exposed")
	}
}
