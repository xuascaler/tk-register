package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"goapi-tk-register/server/common"
	"goapi-tk-register/server/internal/core"
)

func apiRequest(t *testing.T, a *app, method, path, token, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:12345"
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}

func managedToken(t *testing.T, w *httptest.ResponseRecorder) core.APITokenResult {
	t.Helper()
	var result struct {
		Data core.APITokenResult `json:"data"`
	}
	if err := common.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.Data
}

func TestAPITokenManagementAuthorizationAndLifecycle(t *testing.T) {
	a, _, _ := fixture(t)
	user, cookie, csrf := vendorSession(t, a, "token-browser@example.com", "member")
	requireStatus(t, request(t, a, "GET", "account/api-token", nil, nil, ""), 401)
	w := request(t, a, "GET", "account/api-token", nil, cookie, "")
	requireStatus(t, w, 200)
	if managedToken(t, w).Token != nil {
		t.Fatal("unexpected token")
	}
	requireStatus(t, request(t, a, "POST", "account/api-token", map[string]any{}, cookie, ""), 403)
	r := httptest.NewRequest("POST", basePath+"/api/account/api-token", strings.NewReader(`{}`))
	r.AddCookie(cookie)
	r.Header.Set("X-CSRF-Token", csrf)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	requireStatus(t, w, 403)
	w = request(t, a, "POST", "account/api-token", map[string]any{}, cookie, csrf)
	requireStatus(t, w, 200)
	generated := managedToken(t, w)
	if generated.PlaintextToken == "" || generated.Token == nil {
		t.Fatal("generation missing plaintext")
	}
	requireStatus(t, request(t, a, "POST", "account/api-token", map[string]any{}, cookie, csrf), 409)
	w = request(t, a, "GET", "account/api-token", nil, cookie, "")
	requireStatus(t, w, 200)
	if strings.Contains(w.Body.String(), generated.PlaintextToken) || managedToken(t, w).PlaintextToken != "" {
		t.Fatal("metadata exposed plaintext")
	}
	mutation := map[string]any{"token_id": generated.Token.ID, "revision": generated.Token.Revision}
	w = request(t, a, "POST", "account/api-token/rotate", mutation, cookie, csrf)
	requireStatus(t, w, 200)
	rotated := managedToken(t, w)
	if rotated.PlaintextToken == generated.PlaintextToken {
		t.Fatal("rotation reused token")
	}
	requireStatus(t, request(t, a, "POST", "account/api-token/rotate", mutation, cookie, csrf), 409)
	requireStatus(t, request(t, a, "DELETE", "account/api-token", mutation, cookie, csrf), 409)
	if _, _, err := a.store.AuthenticateAPIToken(context.Background(), generated.PlaintextToken); !errors.Is(err, core.ErrInvalidAPIToken) {
		t.Fatal("old token accepted")
	}
	_, otherCookie, otherCSRF := vendorSession(t, a, "token-other@example.com", "member")
	mutation["revision"] = rotated.Token.Revision
	requireStatus(t, request(t, a, "DELETE", "account/api-token", mutation, otherCookie, otherCSRF), 409)
	w = request(t, a, "DELETE", "account/api-token", mutation, cookie, csrf)
	requireStatus(t, w, 200)
	if managedToken(t, w).Token.RevokedAt == nil {
		t.Fatal("token not revoked")
	}
	if _, _, err := a.store.AuthenticateAPIToken(context.Background(), rotated.PlaintextToken); !errors.Is(err, core.ErrInvalidAPIToken) {
		t.Fatal("revoked token accepted")
	}
	a.store.DB.Model(&core.User{}).Where("id = ?", user.ID).Update("status", "disabled")
	requireStatus(t, request(t, a, "POST", "account/api-token", map[string]any{}, cookie, csrf), 401)
}

func TestBearerOnlyRouteCannotAccessCookieOrAdminRoutes(t *testing.T) {
	a, _, _ := fixture(t)
	u, cookie, csrf := vendorSession(t, a, "api-scope@example.com", "admin")
	generated, err := a.store.GenerateAPIToken(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"account/api-token", "auth/me", "admin/users", "admin/settings", "submissions"} {
		w := apiRequest(t, a, "GET", basePath+"/api/"+route, generated.PlaintextToken, "", "")
		requireStatus(t, w, 401)
	}
	w := request(t, a, "POST", "v1/vendors/tk-register-0/claude/accounts", submissionParameters(), cookie, csrf)
	requireStatus(t, w, 401)
	if w.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("missing Bearer challenge")
	}
	r := httptest.NewRequest("POST", claudeAccountsAPIPath, strings.NewReader(`{}`))
	r.Header.Add("Authorization", "Bearer "+generated.PlaintextToken)
	r.Header.Add("Authorization", "Bearer "+generated.PlaintextToken)
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	requireStatus(t, w, 401)
	requireStatus(t, apiRequest(t, a, "GET", claudeAccountsAPIPath, generated.PlaintextToken, "", ""), 405)
	requireStatus(t, apiRequest(t, a, "POST", claudeAccountsAPIPath, "invalid", "application/json", `{}`), 401)
	requireStatus(t, apiRequest(t, a, "POST", claudeAccountsAPIPath+"?token="+generated.PlaintextToken, "", "application/json", `{}`), 401)
}

func TestExternalSubmissionPassthroughAndOwnerAttribution(t *testing.T) {
	for _, status := range []int{201, 400, 409, 429, 503} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			a, _, _ := fixture(t)
			admin, _, _ := vendorSession(t, a, "api-vendor-admin@example.com", "admin")
			user, _, _ := vendorSession(t, a, "api-submit@example.com", "member")
			configuredVendor(t, a, admin.ID)
			token, err := a.store.GenerateAPIToken(context.Background(), user.ID)
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			response := `{"id":98765,"message":"vendor response","extra":{"value":true}}`
			a.vendorTransport = vendorRoundTrip(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				if r.Header.Get("Authorization") != "Bearer mock-platform-access-token" {
					t.Error("user token was forwarded to vendor")
				}
				body, _ := io.ReadAll(r.Body)
				if !strings.Contains(string(body), `"account":"supplier-account@example.com"`) {
					t.Error("parameters missing from upstream request")
				}
				return remoteResponse(status, response), nil
			})
			raw, _ := common.Marshal(submissionParameters())
			w := apiRequest(t, a, "POST", claudeAccountsAPIPath, token.PlaintextToken, "application/json; charset=utf-8", string(raw))
			requireStatus(t, w, status)
			if w.Body.String() != response || w.Header().Get("Content-Type") != "application/json" || calls.Load() != 1 {
				t.Fatal("upstream response changed", w.Body.String())
			}
			if w.Header().Get("X-Submission-ID") == "" || w.Header().Get("X-Request-ID") == "" {
				t.Fatal("missing correlation IDs")
			}
			var submission core.Submission
			if err := a.store.DB.First(&submission, w.Header().Get("X-Submission-ID")).Error; err != nil {
				t.Fatal(err)
			}
			if submission.UserID != user.ID {
				t.Fatal("submission not owned by token user")
			}
			var execution core.ProtocolExecution
			if err := a.store.DB.Where("submission_id = ?", submission.ID).First(&execution).Error; err != nil {
				t.Fatal(err)
			}
			if execution.RequestID != w.Header().Get("X-Request-ID") {
				t.Fatal("request ID does not correlate with execution")
			}
		})
	}
}

func TestExternalAPIValidationAndSharedWebLimit(t *testing.T) {
	a, m, _ := fixture(t)
	user, cookie, csrf := vendorSession(t, a, "api-limit@example.com", "member")
	token, err := a.store.GenerateAPIToken(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	a.vendorTransport = vendorRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return remoteResponse(200, `{"id":1}`), nil
	})
	requireStatus(t, apiRequest(t, a, "POST", claudeAccountsAPIPath, token.PlaintextToken, "application/json-malformed", `{}`), 415)
	for _, body := range []string{`null`, `[]`, `{} {}`, `{"auth_json":`} {
		requireStatus(t, apiRequest(t, a, "POST", claudeAccountsAPIPath, token.PlaintextToken, "application/json", body), 400)
	}
	large := `{"account":"` + strings.Repeat("a", 3<<20) + `"}`
	requireStatus(t, apiRequest(t, a, "POST", claudeAccountsAPIPath, token.PlaintextToken, "application/json", large), 413)
	if calls.Load() != 0 {
		t.Fatal("invalid request reached upstream")
	}
	m.FastForward(61 * time.Second)
	key := redisPrefix + "vendor-action:push:" + strconv.FormatUint(user.ID, 10)
	if err := a.redis.Set(context.Background(), key, 10, time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	w := apiRequest(t, a, "POST", claudeAccountsAPIPath, token.PlaintextToken, "application/json", `{}`)
	requireStatus(t, w, 429)
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After")
	}
	w = request(t, a, "POST", "submissions", map[string]any{}, cookie, csrf)
	requireStatus(t, w, 429)
	for _, key := range a.redis.Keys(context.Background(), "*").Val() {
		if !strings.HasPrefix(key, redisPrefix) {
			t.Fatal("unprefixed Redis key")
		}
	}
}

func TestExternalAPIDisabledUserRejectedBeforeUpstream(t *testing.T) {
	a, _, _ := fixture(t)
	u, _, _ := vendorSession(t, a, "disabled-api@example.com", "member")
	token, err := a.store.GenerateAPIToken(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	a.store.DB.Model(&core.User{}).Where("id = ?", u.ID).Update("status", "disabled")
	w := apiRequest(t, a, "POST", claudeAccountsAPIPath, token.PlaintextToken, "application/json", `{}`)
	requireStatus(t, w, 401)
}
