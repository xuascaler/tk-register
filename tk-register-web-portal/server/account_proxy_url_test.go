package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"goapi-tk-register/server/internal/core"
	"goapi-tk-register/server/internal/supplier"
)

func setupProxyUpdate(t *testing.T) (*app, *core.User, *http.Cookie, string, core.SyncedAccount, *mockAuthEditor) {
	t.Helper()
	a, user, cookie, csrf, target, fake := setupAuthUpdate(t)
	requireStatus(t, request(t, a, "POST", authRoute(target.ID), authInput(), cookie, csrf), 200)
	credentialHelperFixture(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/proxy-test" || body["proxy_url"] == nil || body["auth_json"] != nil {
			t.Error("unexpected helper operation")
		}
		result := credentialReply(user.ID, false)
		result.FlowID, result.AuthURL = "", ""
		_ = json.NewEncoder(w).Encode(result)
	})
	return a, user, cookie, csrf, target, fake
}

func proxyRoute(id uint64) string { return fmt.Sprintf("accounts/%d/proxy-url", id) }

func testedProxyInput(t *testing.T, a *app, target core.SyncedAccount, cookie *http.Cookie, csrf string) map[string]any {
	t.Helper()
	w := request(t, a, "GET", proxyRoute(target.ID), nil, cookie, "")
	requireStatus(t, w, 200)
	input := map[string]any{"proxy_url": testCredentialProxy, "revision": data(t, w)["revision"]}
	w = request(t, a, "POST", proxyRoute(target.ID)+"-test", input, cookie, csrf)
	requireStatus(t, w, 200)
	input["test_token"] = data(t, w)["test_token"]
	return input
}

func TestProxyUpdateOnlyChangesProxyAndPersistsEncryptedResult(t *testing.T) {
	a, user, cookie, csrf, target, fake := setupProxyUpdate(t)
	var original map[string]json.RawMessage
	if err := json.Unmarshal(fake.raw, &original); err != nil {
		t.Fatal(err)
	}
	input := testedProxyInput(t, a, target, cookie, csrf)
	w := request(t, a, "POST", proxyRoute(target.ID), input, cookie, csrf)
	requireStatus(t, w, 200)
	var changed map[string]json.RawMessage
	json.Unmarshal(fake.raw, &changed)
	for key, value := range original {
		if key != "proxy_url" && string(changed[key]) != string(value) {
			t.Fatal("non-proxy credential changed", key)
		}
	}
	if fake.writes != 2 || fake.refreshes != 0 {
		t.Fatal("unexpected writes or inline sync")
	}
	if strings.Contains(w.Body.String(), "private-user") || strings.Contains(w.Body.String(), "private-password") || strings.Contains(w.Body.String(), "refresh_token") {
		t.Fatal("response leaked credentials")
	}
	info, err := a.store.AccountProxyInfo(context.Background(), target)
	if err != nil || info.Revision == input["revision"] || !strings.Contains(info.ProxyURLMasked, "proxy.example") {
		t.Fatal("latest credential did not advance", err)
	}
	var row core.AccountCredential
	if err = a.store.DB.First(&row, "account_id = ?", target.ID).Error; err != nil {
		t.Fatal(err)
	}
	if row.State != "ready" || row.AuthJSONCiphertext == "" || strings.Contains(row.AuthJSONCiphertext, "private-password") || row.PendingCiphertext != "" {
		t.Fatal("credential not encrypted/confirmed")
	}
	var audit core.AccountAuthJSONAudit
	a.store.DB.Last(&audit)
	if audit.Method != "proxy" || audit.ActorID != user.ID || audit.Status != "updated" {
		t.Fatal("proxy audit missing")
	}
	// A double-click or replay with the old dialog must not issue another PUT.
	w = request(t, a, "POST", proxyRoute(target.ID), input, cookie, csrf)
	requireStatus(t, w, 409)
	if !strings.Contains(w.Body.String(), "ACCOUNT_CREDENTIALS_CHANGED") || fake.writes != 2 {
		t.Fatal("stale proxy request was sent")
	}
}

func TestProxyUpdateRequiresSavedCredentialOwnershipAndCSRF(t *testing.T) {
	a, _, cookie, csrf, target, fake := setupAuthUpdate(t)
	checks := fake.checks
	w := request(t, a, "GET", proxyRoute(target.ID), nil, cookie, "")
	requireStatus(t, w, 409)
	if !strings.Contains(w.Body.String(), "ACCOUNT_CREDENTIALS_MISSING") || fake.checks != checks {
		t.Fatal("missing credential should fail before upstream")
	}
	_, other, _ := vendorSession(t, a, "other-proxy@example.com", "member")
	for _, route := range []string{proxyRoute(target.ID), proxyRoute(target.ID) + "-test"} {
		requireStatus(t, request(t, a, "POST", route, map[string]any{}, nil, ""), 401)
		requireStatus(t, request(t, a, "POST", route, map[string]any{}, cookie, ""), 403)
		requireStatus(t, request(t, a, "GET", route, nil, other, ""), 404)
	}
	requireStatus(t, request(t, a, "GET", fmt.Sprintf("admin/synced-accounts/%d/proxy-url", target.ID), nil, cookie, ""), 403)
	requireStatus(t, request(t, a, "POST", authRoute(target.ID), authInput(), cookie, csrf), 200)
	requireStatus(t, request(t, a, "GET", proxyRoute(target.ID), nil, cookie, ""), 200)
}

func TestProxyUpdateTestProofAndSharedLocks(t *testing.T) {
	a, _, cookie, csrf, target, fake := setupProxyUpdate(t)
	input := testedProxyInput(t, a, target, cookie, csrf)
	proof := input["test_token"]
	input["test_token"] = "unknown-test-token-1234"
	w := request(t, a, "POST", proxyRoute(target.ID), input, cookie, csrf)
	requireStatus(t, w, 409)
	if !strings.Contains(w.Body.String(), "PROXY_TEST_REQUIRED") {
		t.Fatal(w.Body.String())
	}
	input["test_token"] = proof
	input["proxy_url"] = "http://different.example:8080"
	requireStatus(t, request(t, a, "POST", proxyRoute(target.ID), input, cookie, csrf), 409)
	input["proxy_url"] = testCredentialProxy
	for _, key := range []string{accountMutationLeaseKey(target), accountSyncLeaseKey} {
		a.redis.Set(context.Background(), key, "another-operation", time.Minute)
		requireStatus(t, request(t, a, "POST", proxyRoute(target.ID), input, cookie, csrf), 409)
		a.redis.Del(context.Background(), key)
	}
	if fake.writes != 1 {
		t.Fatal("invalid proof or busy lock allowed proxy write")
	}
	// Full JSON updates invalidate a previously successful proxy test.
	requireStatus(t, request(t, a, "POST", authRoute(target.ID), authInput(), cookie, csrf), 200)
	w = request(t, a, "POST", proxyRoute(target.ID), input, cookie, csrf)
	requireStatus(t, w, 409)
	if !strings.Contains(w.Body.String(), "ACCOUNT_CREDENTIALS_CHANGED") || fake.writes != 2 {
		t.Fatal("concurrent Auth JSON change was overwritten")
	}
}

func TestProxyUpdateUnknownDoesNotRetryOrReuseLastConfirmedCredential(t *testing.T) {
	a, _, cookie, csrf, target, fake := setupProxyUpdate(t)
	input := testedProxyInput(t, a, target, cookie, csrf)
	fake.updateErr = &supplier.Error{Code: supplier.CodeUpdateUnconfirmed}
	requireStatus(t, request(t, a, "POST", proxyRoute(target.ID), input, cookie, csrf), 504)
	for _, method := range []string{"GET", "POST"} {
		w := request(t, a, method, proxyRoute(target.ID), input, cookie, csrf)
		requireStatus(t, w, 409)
		if !strings.Contains(w.Body.String(), "ACCOUNT_CREDENTIALS_UNCONFIRMED") {
			t.Fatal("uncertain result was not retained")
		}
	}
	if fake.writes != 2 {
		t.Fatal("uncertain proxy update retried")
	}
	var row core.AccountCredential
	a.store.DB.First(&row, "account_id = ?", target.ID)
	if row.State != "unknown" || row.PendingCiphertext == "" || row.AuthJSONCiphertext == "" {
		t.Fatal("uncertain and confirmed credentials not retained separately")
	}
	// A later explicitly submitted complete Auth JSON can establish a fresh
	// confirmed version. It is never an automatic retry of the proxy request.
	fake.updateErr = nil
	requireStatus(t, request(t, a, "POST", authRoute(target.ID), authInput(), cookie, csrf), 200)
	requireStatus(t, request(t, a, "GET", proxyRoute(target.ID), nil, cookie, ""), 200)
}

func TestProxyUpdateRechecksEligibilityAndAllowsAdministrator(t *testing.T) {
	a, _, cookie, csrf, target, fake := setupProxyUpdate(t)
	input := testedProxyInput(t, a, target, cookie, csrf)
	fake.info.Editable = false
	w := request(t, a, "GET", proxyRoute(target.ID), nil, cookie, "")
	requireStatus(t, w, 200)
	if data(t, w)["editable"] != false {
		t.Fatal("active/locked account appears editable")
	}
	fake.updateErr = &supplier.Error{Code: supplier.CodeNotEditable}
	requireStatus(t, request(t, a, "POST", proxyRoute(target.ID), input, cookie, csrf), 409)
	fake.info.Editable, fake.updateErr = true, nil
	admin, ac, acsrf := vendorSession(t, a, "proxy-admin@example.com", "admin")
	route := fmt.Sprintf("admin/synced-accounts/%d/proxy-url", target.ID)
	w = request(t, a, "GET", route, nil, ac, "")
	requireStatus(t, w, 200)
	input = map[string]any{"proxy_url": testCredentialProxy, "revision": data(t, w)["revision"]}
	// A member's successful test is not reusable by an administrator.
	memberInput := testedProxyInput(t, a, target, cookie, csrf)
	input["test_token"] = memberInput["test_token"]
	requireStatus(t, request(t, a, "POST", route, input, ac, acsrf), 409)
	// The helper result must also identify the authenticated admin actor.
	credentialHelperFixture(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		result := credentialReply(admin.ID, false)
		result.FlowID = ""
		result.AuthURL = ""
		json.NewEncoder(w).Encode(result)
	})
	w = request(t, a, "POST", route+"-test", input, ac, acsrf)
	requireStatus(t, w, 200)
	input["test_token"] = data(t, w)["test_token"]
	requireStatus(t, request(t, a, "POST", route, input, ac, acsrf), 200)
}

func TestAPIAuthJSONUpdateAlsoSavesCurrentCredentials(t *testing.T) {
	a, owner, _, _, target, _ := setupAuthUpdate(t)
	token := lookupToken(t, a, owner.ID)
	w := apiRequest(t, a, "POST", claudeAccountAuthJSONAPIPath, token.PlaintextToken, "application/json", authAPIBody(t, owner.Email, target.Account, false))
	requireStatus(t, w, 200)
	if _, err := a.store.AccountProxyInfo(context.Background(), target); err != nil {
		t.Fatal("API update did not persist latest credentials", err)
	}
}
