package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"goapi-tk-register/server/internal/core"
)

const testCredentialFlowID = "flow-test-0123456789abcdef"
const testCredentialProxy = "http://private-user:private-password@proxy.example:8080"

func TestCredentialAuthShapePreservesStandardTemplateFields(t *testing.T) {
	auth := submissionParameters()["auth_json"].(map[string]any)
	auth["refresh_token"] = ""
	if err := credentialAuthShape(auth, testCredentialProxy); err != nil {
		t.Fatal("an empty template value must be allowed")
	}
	delete(auth, "refresh_token")
	if err := credentialAuthShape(auth, testCredentialProxy); err == nil {
		t.Fatal("export templates must retain standard keys")
	}
}

func credentialHelperFixture(t *testing.T, fn func(http.ResponseWriter, *http.Request, map[string]any)) {
	t.Helper()
	token := strings.Repeat("helper-secret-", 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("helper bearer missing")
			w.WriteHeader(401)
			return
		}
		body := map[string]any{}
		if r.Method != "GET" && json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid helper body")
			w.WriteHeader(400)
			return
		}
		fn(w, r, body)
	}))
	t.Cleanup(server.Close)
	t.Setenv("TK_REGISTER_CREDENTIAL_HELPER_TOKEN", token)
	t.Setenv("TK_REGISTER_CREDENTIAL_HELPER_URL", server.URL)
}

func credentialReply(owner uint64, ready bool) credentialHelperResult {
	r := credentialHelperResult{OwnerID: strconv.FormatUint(owner, 10), FlowID: testCredentialFlowID,
		AuthURL: "https://claude.ai/oauth/authorize?state=fixture", Notes: []string{},
		ProxyCheck: credentialProxyCheck{OK: true, IP: "203.0.113.24", CheckedAt: time.Now().UTC(), ElapsedMS: 12}, ExpiresAt: time.Now().Add(29 * time.Minute)}
	if ready {
		r.Auth = submissionParameters()["auth_json"].(map[string]any)
	}
	return r
}

func storedCredentialFlow(t *testing.T, a *app, user *core.User, ready bool) credentialFlowRecord {
	t.Helper()
	r, err := a.newCredentialFlow(context.Background(), user, credentialReply(user.ID, ready), testCredentialProxy, ready)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestCredentialFlowOwnerEncryptionExpiryAndTamper(t *testing.T) {
	a, m, _ := fixture(t)
	owner, cookie, csrf := vendorSession(t, a, "draft-owner@example.com", "member")
	other, otherCookie, otherCSRF := vendorSession(t, a, "draft-other@example.com", "member")
	_, adminCookie, adminCSRF := vendorSession(t, a, "draft-admin@example.com", "admin")
	var calls atomic.Int32
	credentialHelperFixture(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		calls.Add(1)
		if body["owner_id"] != strconv.FormatUint(owner.ID, 10) || r.URL.Path != "/start" {
			t.Error("unexpected helper request")
		}
		_ = json.NewEncoder(w).Encode(credentialReply(owner.ID, false))
	})
	start := map[string]any{"proxy_url": testCredentialProxy, "mode": "oauth", "setup_kind": "full"}
	requireStatus(t, request(t, a, "POST", "credential-flows/start", start, nil, ""), 401)
	requireStatus(t, request(t, a, "POST", "credential-flows/start", start, cookie, "wrong-csrf"), 403)
	if calls.Load() != 0 {
		t.Fatal("helper called without valid session and CSRF")
	}
	w := request(t, a, "POST", "credential-flows/start", start, cookie, csrf)
	requireStatus(t, w, 200)
	if data(t, w)["status"] != "awaiting_code" {
		t.Fatal("wrong draft status")
	}
	key := credentialFlowKey(owner.ID, testCredentialFlowID)
	encrypted, err := a.redis.Get(context.Background(), key).Result()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encrypted, "enc:v1:") || strings.Contains(encrypted, "private-password") {
		t.Fatal("draft is not encrypted")
	}
	if !strings.HasPrefix(key, "tk-register:{tk-register}:") {
		t.Fatal("not cluster-safe")
	}
	for _, session := range []struct {
		c    *http.Cookie
		csrf string
	}{{otherCookie, otherCSRF}, {adminCookie, adminCSRF}} {
		requireStatus(t, request(t, a, "GET", "credential-flows/"+testCredentialFlowID, nil, session.c, ""), 404)
		requireStatus(t, request(t, a, "POST", "credential-flows/exchange", map[string]any{"flow_id": testCredentialFlowID, "code": "secret-code"}, session.c, session.csrf), 404)
	}
	if calls.Load() != 1 {
		t.Fatal("unauthorized flow reached helper")
	}
	// Copying ciphertext to another user's key cannot authorize it.
	if err := a.redis.Set(context.Background(), credentialFlowKey(other.ID, testCredentialFlowID), encrypted, time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, request(t, a, "GET", "credential-flows/"+testCredentialFlowID, nil, otherCookie, ""), 404)
	if err := a.redis.Set(context.Background(), key, encrypted+"tampered", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, request(t, a, "GET", "credential-flows/"+testCredentialFlowID, nil, cookie, ""), 404)
	if err := a.redis.Set(context.Background(), key, encrypted, time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	m.FastForward(2 * time.Minute)
	requireStatus(t, request(t, a, "GET", "credential-flows/"+testCredentialFlowID, nil, cookie, ""), 404)
}

func TestCredentialImportOmittedOptionalsAndDraftCompletion(t *testing.T) {
	a, m, _ := fixture(t)
	u, cookie, csrf := vendorSession(t, a, "import-owner@example.com", "member")
	var imports atomic.Int32
	credentialHelperFixture(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/import" {
			t.Fatal("unexpected helper route")
		}
		imports.Add(1)
		if _, has := body["account_index"]; has {
			t.Error("missing account index serialized as null")
		}
		if _, ok := body["document"].([]any); !ok {
			t.Error("multi-account array not preserved")
		}
		result := credentialReply(u.ID, true)
		result.Auth["refresh_token"] = ""
		result.Auth["organization_name"] = ""
		_ = json.NewEncoder(w).Encode(result)
	})
	body := map[string]any{"proxy_url": testCredentialProxy, "kind": "json", "document": []any{map[string]any{"access_token": "private-access-token"}}}
	requireStatus(t, request(t, a, "POST", "credential-flows/import", body, cookie, csrf), 200)
	key := credentialFlowKey(u.ID, testCredentialFlowID)
	m.FastForward(4 * time.Minute)
	before := m.TTL(key)
	patchRoute := "credential-flows/" + testCredentialFlowID + "/draft"
	for _, patch := range []map[string]any{{"email": "replacement@example.com"}, {"access_token": "replacement-token"}, {"proxy_url": testCredentialProxy}, {"verification_status": "verified"}, {"unexpected": "value"}} {
		requireStatus(t, request(t, a, "POST", patchRoute, map[string]any{"auth_patch": patch}, cookie, csrf), 400)
	}
	w := request(t, a, "POST", patchRoute, map[string]any{"auth_patch": map[string]any{"refresh_token": "completed-refresh-secret", "organization_name": "Completed organization"}}, cookie, csrf)
	requireStatus(t, w, 200)
	if m.TTL(key) > before {
		t.Fatal("draft update extended expiry")
	}
	replayed := request(t, a, "POST", "credential-flows/import", body, cookie, csrf)
	requireStatus(t, replayed, 200)
	auth := data(t, replayed)["auth"].(map[string]any)
	if auth["refresh_token"] != "completed-refresh-secret" {
		t.Fatal("cached import overwrote completed draft")
	}
	if imports.Load() != 2 {
		t.Fatal("unexpected import count")
	}
	encrypted, _ := m.Get(key)
	for _, secret := range []string{"private-access-token", "completed-refresh-secret", "private-password"} {
		if strings.Contains(encrypted, secret) {
			t.Fatal("sensitive draft plaintext stored")
		}
	}
}

func TestCredentialExchangeRetryAndExportVerify(t *testing.T) {
	a, m, _ := fixture(t)
	u, cookie, csrf := vendorSession(t, a, "exchange-owner@example.com", "member")
	storedCredentialFlow(t, a, u, false)
	var exchanged, verified atomic.Int32
	credentialHelperFixture(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if body["flow_id"] != testCredentialFlowID {
			t.Error("wrong helper flow")
		}
		result := credentialReply(u.ID, true)
		switch r.URL.Path {
		case "/exchange":
			exchanged.Add(1)
			if body["code"] != "temporary-secret-code" {
				t.Error("code not forwarded")
			}
		case "/verify":
			verified.Add(1)
			if body["proxy_url"] != testCredentialProxy {
				t.Error("server proxy not used")
			}
			if verified.Load() > 1 {
				result.ProxyCheck.IP = "203.0.113.25"
			}
		default:
			t.Error("unexpected helper route")
		}
		_ = json.NewEncoder(w).Encode(result)
	})
	exchange := map[string]any{"flow_id": testCredentialFlowID, "code": "temporary-secret-code"}
	requireStatus(t, request(t, a, "POST", "credential-flows/exchange", exchange, cookie, csrf), 200)
	requireStatus(t, request(t, a, "POST", "credential-flows/exchange", exchange, cookie, csrf), 200)
	if exchanged.Load() != 1 {
		t.Fatal("authorization code redeemed twice")
	}
	flow, _, err := a.loadCredentialFlow(context.Background(), u, testCredentialFlowID)
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := json.Marshal(flow)
	if strings.Contains(string(plain), "temporary-secret-code") {
		t.Fatal("authorization code persisted")
	}
	m.FastForward(time.Minute)
	before := m.TTL(credentialFlowKey(u.ID, testCredentialFlowID))
	w := request(t, a, "POST", "credential-flows/export", map[string]any{"flow_id": testCredentialFlowID}, cookie, csrf)
	requireStatus(t, w, 200)
	if data(t, w)["auth"] == nil || verified.Load() != 1 {
		t.Fatal("export did not reverify")
	}
	if m.TTL(credentialFlowKey(u.ID, testCredentialFlowID)) > before {
		t.Fatal("export extended expiry")
	}
	requireStatus(t, request(t, a, "POST", "credential-flows/export", map[string]any{"flow_id": testCredentialFlowID}, cookie, csrf), 409)
}

func TestCredentialSubmissionUsesExistingWorkflow(t *testing.T) {
	a, _, _ := fixture(t)
	admin, _, _ := vendorSession(t, a, "credential-admin@example.com", "admin")
	u, cookie, csrf := vendorSession(t, a, "credential-member@example.com", "member")
	vendor := configuredVendor(t, a, admin.ID)
	storedCredentialFlow(t, a, u, true)
	var verifies, pushes atomic.Int32
	credentialHelperFixture(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/verify" || body["owner_id"] != strconv.FormatUint(u.ID, 10) {
			t.Error("wrong verify request")
		}
		verifies.Add(1)
		_ = json.NewEncoder(w).Encode(credentialReply(u.ID, true))
	})
	a.vendorTransport = vendorRoundTrip(func(r *http.Request) (*http.Response, error) {
		pushes.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		auth, ok := body["auth_json"].(map[string]any)
		if !ok || auth["access_token"] != "private-access-token" || body["account"] != "supplier-account@example.com" {
			t.Error("client replaced server credentials")
		}
		return remoteResponse(200, `{"id":765432,"alias":"test accepted"}`), nil
	})
	input := createInput(vendor, "credential-submit-key-0001")
	body := map[string]any{"flow_id": testCredentialFlowID, "vendor_id": input.VendorID, "protocol_version_id": input.ProtocolVersionID, "client_request_key": input.ClientRequestKey, "parameters": input.Parameters}
	input.Parameters["account"] = "other@example.com"
	requireStatus(t, request(t, a, "POST", "credential-flows/submit", body, cookie, csrf), 400)
	if pushes.Load() != 0 || verifies.Load() != 0 {
		t.Fatal("email mismatch reached remote")
	}
	input.Parameters["account"] = "supplier-account@example.com"
	input.Parameters["auth_json"] = map[string]any{"access_token": "client-tampered-token"}
	w := request(t, a, "POST", "credential-flows/submit", body, cookie, csrf)
	requireStatus(t, w, 200)
	assertNoSecrets(t, w.Body.String())
	detail := submissionDetail(t, w.Body.Bytes())
	if detail.Submission.Status != "accepted" || detail.Submission.RemoteID == nil || *detail.Submission.RemoteID != "765432" {
		t.Fatal("submission workflow did not persist accepted response")
	}
	replay := request(t, a, "POST", "credential-flows/submit", body, cookie, csrf)
	requireStatus(t, replay, 200)
	if submissionDetail(t, replay.Body.Bytes()).Submission.ID != detail.Submission.ID || pushes.Load() != 1 {
		t.Fatal("same request key was submitted twice")
	}
	id, _ := core.ParseBusinessID(detail.Submission.ID)
	var jobs int64
	if err := a.store.DB.Model(&core.PostSubmissionSyncJob{}).Where("submission_id = ?", id).Count(&jobs).Error; err != nil || jobs != 1 {
		t.Fatalf("existing post-submit sync missing: %d %v", jobs, err)
	}
	requireStatus(t, request(t, a, "GET", "submissions/"+detail.Submission.ID, nil, cookie, ""), 200)
	// The new entry point shares the exact limiter with the manual form.
	count, err := a.redis.Get(context.Background(), redisPrefix+"vendor-action:push:"+strconv.FormatUint(u.ID, 10)).Int()
	if err != nil || count != 3 {
		t.Fatalf("submission budget mismatch: %d %v", count, err)
	}
}

func TestCredentialSubmissionUnknownAllowsExplicitRetry(t *testing.T) {
	a, _, _ := fixture(t)
	admin, _, _ := vendorSession(t, a, "retry-admin@example.com", "admin")
	u, cookie, csrf := vendorSession(t, a, "retry-member@example.com", "member")
	v := configuredVendor(t, a, admin.ID)
	storedCredentialFlow(t, a, u, true)
	credentialHelperFixture(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		_ = json.NewEncoder(w).Encode(credentialReply(u.ID, true))
	})
	var calls atomic.Int32
	a.vendorTransport = vendorRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("mock network failure")
	})
	ids := map[string]bool{}
	for i := 0; i < 2; i++ {
		input := createInput(v, fmt.Sprintf("credential-explicit-retry-%d", i))
		body := map[string]any{"flow_id": testCredentialFlowID, "vendor_id": input.VendorID, "protocol_version_id": input.ProtocolVersionID, "client_request_key": input.ClientRequestKey, "parameters": input.Parameters}
		w := request(t, a, "POST", "credential-flows/submit", body, cookie, csrf)
		requireStatus(t, w, 200)
		d := submissionDetail(t, w.Body.Bytes())
		if d.Submission.Status != "unknown" {
			t.Fatal("ambiguous response not retained")
		}
		ids[d.Submission.ID] = true
	}
	if calls.Load() != 2 || len(ids) != 2 {
		t.Fatal("active retry did not create a new submission")
	}
}

func TestCredentialSubmissionRespectsPersonalModelVisibility(t *testing.T) {
	for _, role := range []string{"member", "admin"} {
		for _, upstreamStatus := range []int{http.StatusOK, http.StatusBadRequest} {
			t.Run(fmt.Sprintf("%s/%d", role, upstreamStatus), func(t *testing.T) {
				a, _, _ := fixture(t)
				admin, adminCookie, _ := vendorSession(t, a, "visibility-config@example.com", "admin")
				owner, cookie, csrf := vendorSession(t, a, "visibility-submit@example.com", role)
				vendor := configuredVendor(t, a, admin.ID)
				storedCredentialFlow(t, a, owner, true)
				credentialHelperFixture(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
					_ = json.NewEncoder(w).Encode(credentialReply(owner.ID, true))
				})
				var pushes atomic.Int32
				const marker = "claude-model-visibility-marker"
				const diagnostic = "账号重复录入,id:571510"
				a.vendorTransport = vendorRoundTrip(func(*http.Request) (*http.Response, error) {
					pushes.Add(1)
					if upstreamStatus == http.StatusBadRequest {
						return remoteResponse(upstreamStatus, `{"detail":"账号重复录入,id:571510","error":"rejected","model_usage":{"claude-model-visibility-marker":{"cost":1234,"input_tokens":99}}}`), nil
					}
					return remoteResponse(upstreamStatus, `{"id":765432,"alias":"accepted account"}`), nil
				})
				input := createInput(vendor, "credential-visibility-key-001")
				body := map[string]any{"flow_id": testCredentialFlowID, "vendor_id": input.VendorID, "protocol_version_id": input.ProtocolVersionID, "client_request_key": input.ClientRequestKey, "parameters": input.Parameters}
				w := request(t, a, "POST", "credential-flows/submit", body, cookie, csrf)
				requireStatus(t, w, http.StatusOK)
				first := submissionDetail(t, w.Body.Bytes())
				if strings.Contains(w.Body.String(), marker) {
					t.Fatal("disabled guided submission leaked fresh model diagnostics")
				}
				wantStatus := "accepted"
				if upstreamStatus == http.StatusBadRequest {
					wantStatus = "failed"
				}
				if first.Submission.Status != wantStatus || first.Submission.ID == "" {
					t.Fatal("guided submission identity or state changed", first.Submission)
				}
				if upstreamStatus == http.StatusBadRequest && (len(first.Executions) != 1 || !strings.Contains(first.Executions[0].ErrorBody, diagnostic)) {
					t.Fatal("disabled guided submission lost its safe failure diagnostic")
				}
				// Replayed submissions may contain older metrics/billing executions
				// saved before the visibility control was introduced.
				stored := `{"models":[{"model":"claude-model-visibility-marker","cost":1234,"input_tokens":99}]}`
				storedError := stored
				if upstreamStatus == http.StatusBadRequest {
					storedError = `{"detail":"账号重复录入,id:571510","models":[{"model":"claude-model-visibility-marker","cost":1234,"input_tokens":99}]}`
				}
				if err := a.store.DB.Model(&core.ProtocolExecution{}).Where("submission_id = ?", first.Submission.ID).Updates(map[string]any{"result_json": stored, "error_body": storedError}).Error; err != nil {
					t.Fatal(err)
				}
				for _, enabled := range []bool{false, true, false} {
					if err := a.store.PatchSettings(context.Background(), map[string]string{"user_overview_model_costs_enabled": strconv.FormatBool(enabled)}, admin.ID); err != nil {
						t.Fatal(err)
					}
					w = request(t, a, "POST", "credential-flows/submit", body, cookie, csrf)
					requireStatus(t, w, http.StatusOK)
					detail := submissionDetail(t, w.Body.Bytes())
					if detail.Submission.ID != first.Submission.ID || detail.Submission.Status != wantStatus || pushes.Load() != 1 {
						t.Fatal("visibility toggle changed guided submission idempotency or state")
					}
					if strings.Contains(w.Body.String(), marker) != enabled {
						t.Fatal("guided submission did not apply current model visibility", enabled, w.Body.String())
					}
					if upstreamStatus == http.StatusBadRequest && (len(detail.Executions) != 1 || !strings.Contains(detail.Executions[0].ErrorBody, diagnostic)) {
						t.Fatal("replayed guided submission lost its safe failure diagnostic", enabled)
					}
					if !enabled {
						for _, execution := range detail.Executions {
							if execution.Result != nil || upstreamStatus == http.StatusOK && execution.ErrorBody != "" || execution.RequestID == "" {
								t.Fatal("opaque model fields leaked or request metadata was lost")
							}
						}
					}
				}
				management := request(t, a, "GET", "admin/submissions/"+first.Submission.ID, nil, adminCookie, "")
				requireStatus(t, management, http.StatusOK)
				if !strings.Contains(management.Body.String(), marker) {
					t.Fatal("personal projection removed stored Management details")
				}
			})
		}
	}
}

func TestCredentialFailureSafetyLocksAndValidation(t *testing.T) {
	a, m, _ := fixture(t)
	u, cookie, csrf := vendorSession(t, a, "failure-member@example.com", "member")
	storedCredentialFlow(t, a, u, true)
	var calls atomic.Int32
	credentialHelperFixture(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		calls.Add(1)
		w.WriteHeader(502)
		_, _ = io.WriteString(w, `{"error":{"code":"upstream_rejected","message":"private-access-token private-user:private-password"}}`)
	})
	body := map[string]any{"flow_id": testCredentialFlowID}
	w := request(t, a, "POST", "credential-flows/export", body, cookie, csrf)
	requireStatus(t, w, 502)
	assertNoSecrets(t, w.Body.String())
	lockKey := credentialFlowKey(u.ID, testCredentialFlowID) + ":lease"
	if err := a.redis.Set(context.Background(), lockKey, "other-request", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, request(t, a, "POST", "credential-flows/export", body, cookie, csrf), 409)
	if calls.Load() != 1 {
		t.Fatal("locked flow reached helper")
	}
	for _, proxy := range []string{"", "file:///etc/passwd", "http://proxy.example:0", "http://proxy.example:65536", "http://proxy.example?token=bad", "http://proxy.example/path"} {
		requireStatus(t, request(t, a, "POST", "credential-flows/start", map[string]any{"proxy_url": proxy}, cookie, csrf), 400)
	}
	m.Close()
	requireStatus(t, request(t, a, "POST", "credential-flows/export", body, cookie, csrf), 503)
}

func TestCredentialHelperLoopbackAndHealth(t *testing.T) {
	for _, value := range []string{"https://127.0.0.1:3003", "http://example.com:3003", "http://127.0.0.1:3003/path", "http://secret@127.0.0.1:3003", "http://127.0.0.1:3003?x=y"} {
		t.Setenv("TK_REGISTER_CREDENTIAL_HELPER_URL", value)
		if _, err := credentialHelperAddress(); err == nil {
			t.Errorf("unsafe helper address accepted: %s", value)
		}
	}
	t.Setenv("TK_REGISTER_CREDENTIAL_HELPER_TOKEN", "")
	if !credentialHelperHealthy(context.Background()) {
		t.Fatal("standalone Go health unexpectedly requires helper")
	}
	var healthy atomic.Bool
	healthy.Store(true)
	credentialHelperFixture(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/healthz" || r.Method != "GET" {
			t.Error("unexpected health request")
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": healthy.Load()})
	})
	if !credentialHelperHealthy(context.Background()) {
		t.Fatal("healthy helper rejected")
	}
	healthy.Store(false)
	if credentialHelperHealthy(context.Background()) {
		t.Fatal("unhealthy helper accepted")
	}
}

func TestCredentialHelperRejectsMismatchedAndOversizedResults(t *testing.T) {
	a, _, _ := fixture(t)
	u, cookie, csrf := vendorSession(t, a, "response-owner@example.com", "member")
	var variant atomic.Int32
	credentialHelperFixture(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		result := credentialReply(u.ID, false)
		switch variant.Load() {
		case 0:
			result.OwnerID = strconv.FormatUint(u.ID+1, 10)
		case 1:
			result.ExpiresAt = time.Now().Add(-time.Minute)
		case 2:
			result.ExpiresAt = time.Now().Add(time.Hour)
		case 3:
			result.ProxyCheck.OK = false
		case 4:
			result.FlowID = "invalid-flow"
		case 5:
			_, _ = io.WriteString(w, strings.Repeat("x", credentialBodyLimit+1))
			return
		case 6:
			w.Header().Set("Location", "https://example.com/never-follow-this")
			w.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		_ = json.NewEncoder(w).Encode(result)
	})
	for i := 0; i < 7; i++ {
		variant.Store(int32(i))
		w := request(t, a, "POST", "credential-flows/start", map[string]any{"proxy_url": testCredentialProxy}, cookie, csrf)
		requireStatus(t, w, 502)
	}
	if a.redis.Exists(context.Background(), credentialFlowKey(u.ID, testCredentialFlowID)).Val() != 0 {
		t.Fatal("an invalid helper result created a usable flow")
	}
}

func TestCredentialIncompleteDraftCanExportButCannotSubmit(t *testing.T) {
	a, _, _ := fixture(t)
	u, cookie, csrf := vendorSession(t, a, "template-owner@example.com", "member")
	result := credentialReply(u.ID, true)
	result.Auth["refresh_token"] = ""
	result.Auth["organization_name"] = ""
	result.Notes = []string{"Complete missing fields before submitting this account."}
	if _, err := a.newCredentialFlow(context.Background(), u, result, testCredentialProxy, true); err != nil {
		t.Fatal(err)
	}
	var checks atomic.Int32
	credentialHelperFixture(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/verify" {
			t.Error("unexpected helper route")
		}
		checks.Add(1)
		_ = json.NewEncoder(w).Encode(credentialReply(u.ID, false))
	})
	w := request(t, a, "POST", "credential-flows/export", map[string]any{"flow_id": testCredentialFlowID}, cookie, csrf)
	requireStatus(t, w, 200)
	auth := data(t, w)["auth"].(map[string]any)
	if auth["refresh_token"] != "" || auth["proxy_url"] != testCredentialProxy || len(data(t, w)["notes"].([]any)) == 0 {
		t.Fatal("incomplete template or warning was not preserved")
	}
	w = request(t, a, "POST", "credential-flows/submit", map[string]any{"flow_id": testCredentialFlowID, "vendor_id": "1", "protocol_version_id": "1", "client_request_key": "partial-template-submit", "parameters": submissionParameters()}, cookie, csrf)
	requireStatus(t, w, 400)
	if !strings.Contains(w.Body.String(), "INVALID_AUTH_JSON") || checks.Load() != 1 {
		t.Fatal("incomplete submission must fail before contacting helper or supplier")
	}
}

func TestCredentialImportDocumentAndEnvelopeLimits(t *testing.T) {
	a, _, _ := fixture(t)
	u, cookie, csrf := vendorSession(t, a, "size-owner@example.com", "member")
	var calls atomic.Int32
	credentialHelperFixture(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(credentialReply(u.ID, true))
	})
	// This document is below 2 MB, but its proxy/JSON envelope exceeds 2 MB.
	body := map[string]any{"kind": "json", "proxy_url": testCredentialProxy, "document": map[string]any{"padding": strings.Repeat("x", credentialDocumentLimit-20)}}
	requireStatus(t, request(t, a, "POST", "credential-flows/import", body, cookie, csrf), 200)
	for _, document := range []any{strings.Repeat("x", credentialDocumentLimit+1), map[string]any{"padding": strings.Repeat("x", credentialDocumentLimit)}} {
		body["document"] = document
		requireStatus(t, request(t, a, "POST", "credential-flows/import", body, cookie, csrf), 400)
	}
	if calls.Load() != 1 {
		t.Fatal("oversized documents reached the helper")
	}
}

func TestCredentialProxyTestStatelessAuthenticationAndBudget(t *testing.T) {
	a, _, _ := fixture(t)
	u, cookie, csrf := vendorSession(t, a, "proxy-check-owner@example.com", "member")
	var calls atomic.Int32
	credentialHelperFixture(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		calls.Add(1)
		if r.URL.Path != "/proxy-test" || len(body) != 2 || body["owner_id"] != strconv.FormatUint(u.ID, 10) || body["proxy_url"] != testCredentialProxy {
			t.Error("connection test did not use the stateless owner-bound helper endpoint")
		}
		check := credentialReply(u.ID, false).ProxyCheck
		check.ElapsedMS = 0
		_ = json.NewEncoder(w).Encode(map[string]any{"owner_id": strconv.FormatUint(u.ID, 10), "proxy_check": check})
	})
	body := map[string]any{"proxy_url": testCredentialProxy}
	requireStatus(t, request(t, a, "POST", "credential-flows/proxy-test", body, nil, ""), 401)
	requireStatus(t, request(t, a, "POST", "credential-flows/proxy-test", body, cookie, "wrong-csrf"), 403)
	if calls.Load() != 0 {
		t.Fatal("unauthorized test contacted helper")
	}
	// Creating/importing drafts and editing drafts have separate budgets.
	for _, kind := range []string{"create", "update"} {
		if err := a.redis.Set(context.Background(), redisPrefix+"credential-rate:"+kind+":"+strconv.FormatUint(u.ID, 10), 99, time.Minute).Err(); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 30; i++ {
		w := request(t, a, "POST", "credential-flows/proxy-test", body, cookie, csrf)
		requireStatus(t, w, 200)
		result := data(t, w)
		if len(result) != 1 || result["proxy_check"].(map[string]any)["elapsed_ms"] != float64(0) {
			t.Fatal("connection test exposed extra state or rejected zero elapsed time")
		}
		assertNoSecrets(t, w.Body.String())
	}
	w := request(t, a, "POST", "credential-flows/proxy-test", body, cookie, csrf)
	requireStatus(t, w, 429)
	if calls.Load() != 30 || w.Header().Get("Retry-After") == "" {
		t.Fatal("connection tests did not use the independent 30/minute budget")
	}
	var submissions int64
	if err := a.store.DB.Model(&core.Submission{}).Count(&submissions).Error; err != nil || submissions != 0 {
		t.Fatal("connection test created a submission", err)
	}
	keys, err := a.redis.Keys(context.Background(), redisPrefix+"credential-flow:*").Result()
	if err != nil || len(keys) != 0 {
		t.Fatal("stateless proxy test created an authorization draft", err)
	}
}

func TestCredentialProxyTestFlowBindingAndExpiry(t *testing.T) {
	a, m, _ := fixture(t)
	u, cookie, csrf := vendorSession(t, a, "bound-check-owner@example.com", "member")
	_, otherCookie, otherCSRF := vendorSession(t, a, "bound-check-other@example.com", "member")
	_, adminCookie, adminCSRF := vendorSession(t, a, "bound-check-admin@example.com", "admin")
	original := storedCredentialFlow(t, a, u, true)
	var calls atomic.Int32
	credentialHelperFixture(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		calls.Add(1)
		if r.URL.Path != "/verify" || len(body) != 3 || body["flow_id"] != testCredentialFlowID || body["proxy_url"] != testCredentialProxy || body["owner_id"] != strconv.FormatUint(u.ID, 10) {
			t.Error("bound test did not use server-owned flow/proxy")
		}
		result := credentialReply(u.ID, false)
		result.ProxyCheck.ElapsedMS = 45
		if calls.Load() > 1 {
			result.ProxyCheck.IP = "203.0.113.25"
		}
		_ = json.NewEncoder(w).Encode(result)
	})
	body := map[string]any{"flow_id": testCredentialFlowID}
	for _, session := range []struct {
		cookie *http.Cookie
		csrf   string
	}{{otherCookie, otherCSRF}, {adminCookie, adminCSRF}} {
		requireStatus(t, request(t, a, "POST", "credential-flows/proxy-test", body, session.cookie, session.csrf), 404)
	}
	for _, invalid := range []map[string]any{{}, {"proxy_url": testCredentialProxy, "flow_id": testCredentialFlowID}, {"proxy_url": nil, "flow_id": testCredentialFlowID}, {"proxy_url": "", "flow_id": testCredentialFlowID}, {"flow_id": nil}, {"proxy_url": "file:///private/secret"}, {"flow_id": testCredentialFlowID, "owner_id": "1"}} {
		requireStatus(t, request(t, a, "POST", "credential-flows/proxy-test", invalid, cookie, csrf), 400)
	}
	if calls.Load() != 0 {
		t.Fatal("foreign or invalid request reached helper")
	}
	key := credentialFlowKey(u.ID, testCredentialFlowID)
	m.FastForward(5 * time.Minute)
	beforeTTL := m.TTL(key)
	w := request(t, a, "POST", "credential-flows/proxy-test", body, cookie, csrf)
	requireStatus(t, w, 200)
	if len(data(t, w)) != 1 || data(t, w)["proxy_check"] == nil {
		t.Fatal("bound test must return only the safe check")
	}
	assertNoSecrets(t, w.Body.String())
	current, ciphertext, err := a.loadCredentialFlow(context.Background(), u, testCredentialFlowID)
	if err != nil || current.ProxyCheck.ElapsedMS != 45 || current.ProxyURL != original.ProxyURL || current.Auth["access_token"] != original.Auth["access_token"] || !current.ExpiresAt.Equal(original.ExpiresAt) || m.TTL(key) > beforeTTL {
		t.Fatal("proxy test changed credentials or extended the draft lifetime", err)
	}
	w = request(t, a, "POST", "credential-flows/proxy-test", body, cookie, csrf)
	requireStatus(t, w, 409)
	if !strings.Contains(w.Body.String(), "PROXY_CHANGED") {
		t.Fatal("changed exit IP was not reported")
	}
	after, _ := m.Get(key)
	if ciphertext != after {
		t.Fatal("failed check replaced the saved successful draft")
	}
}

func TestCredentialProxyTestFailureMessages(t *testing.T) {
	a, _, _ := fixture(t)
	u, cookie, csrf := vendorSession(t, a, "failure-check-owner@example.com", "member")
	storedCredentialFlow(t, a, u, true)
	upstreamCode := ""
	credentialHelperFixture(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		w.WriteHeader(502)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": upstreamCode, "message": "private-access-token private-user:private-password unsafe upstream body"}})
	})
	for _, testcase := range []struct {
		upstream, code string
		status         int
	}{
		{"upstream_timeout", "PROXY_TEST_TIMEOUT", 504},
		{"proxy_authentication_failed", "PROXY_AUTHENTICATION_FAILED", 502},
		{"upstream_network_error", "PROXY_CONNECTION_FAILED", 502},
		{"proxy_check_failed", "PROXY_CONNECTION_FAILED", 502},
		{"proxy_forbidden", "INVALID_PROXY", 400},
		{"unexpected", "PROXY_CONNECTION_FAILED", 502},
	} {
		upstreamCode = testcase.upstream
		for _, body := range []map[string]any{{"proxy_url": testCredentialProxy}, {"flow_id": testCredentialFlowID}} {
			w := request(t, a, "POST", "credential-flows/proxy-test", body, cookie, csrf)
			requireStatus(t, w, testcase.status)
			assertNoSecrets(t, w.Body.String())
			if !strings.Contains(w.Body.String(), testcase.code) || strings.Contains(w.Body.String(), "authorization provider") || strings.Contains(w.Body.String(), "unsafe upstream body") {
				t.Fatal("proxy failure was not reported with a safe, specific error")
			}
		}
	}
}

func TestCredentialProxyTestResponseValidationAndTimeout(t *testing.T) {
	a, _, _ := fixture(t)
	u, cookie, csrf := vendorSession(t, a, "validation-check-owner@example.com", "member")
	storedCredentialFlow(t, a, u, true)
	var variant atomic.Int32
	credentialHelperFixture(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		check := credentialReply(u.ID, false).ProxyCheck
		owner := strconv.FormatUint(u.ID, 10)
		switch variant.Load() {
		case 0:
			owner = "0"
		case 1:
			check.IP = "not-an-ip"
		case 2:
			check.ElapsedMS = -1
		case 3:
			check.OK = false
		case 4:
			check.CheckedAt = time.Time{}
		case 5:
			check.ElapsedMS = 60001
		case 7:
			<-r.Context().Done()
			return
		}
		// No flow ID/expiry is valid for stateless checks only.
		_ = json.NewEncoder(w).Encode(map[string]any{"owner_id": owner, "proxy_check": check})
	})
	for i := 0; i < 6; i++ {
		variant.Store(int32(i))
		requireStatus(t, request(t, a, "POST", "credential-flows/proxy-test", map[string]any{"proxy_url": testCredentialProxy}, cookie, csrf), 502)
	}
	variant.Store(6)
	requireStatus(t, request(t, a, "POST", "credential-flows/proxy-test", map[string]any{"proxy_url": testCredentialProxy}, cookie, csrf), 200)
	requireStatus(t, request(t, a, "POST", "credential-flows/proxy-test", map[string]any{"flow_id": testCredentialFlowID}, cookie, csrf), 502)
	variant.Store(7)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := a.callCredentialHelper(ctx, u.ID, "/proxy-test", map[string]any{"proxy_url": testCredentialProxy})
	var failure *core.VendorError
	if !errors.As(err, &failure) || failure.Status != 504 || failure.Code != "PROXY_TEST_TIMEOUT" {
		t.Fatal("helper deadline did not produce a proxy timeout", err)
	}
}
