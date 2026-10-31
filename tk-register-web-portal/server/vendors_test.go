package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goapi-tk-register/server/common"
	"goapi-tk-register/server/internal/core"
)

type vendorRoundTrip func(*http.Request) (*http.Response, error)

func (f vendorRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func remoteResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}
func vendorSession(t *testing.T, a *app, email, role string) (*core.User, *http.Cookie, string) {
	t.Helper()
	u := addUser(t, a, email, role)
	token, session, e := a.newSession(context.Background(), u.ID, u.Role, 28800, 1800)
	if e != nil {
		t.Fatal(e)
	}
	return u, &http.Cookie{Name: cookieName, Value: token}, session.CSRF
}
func submissionParameters() map[string]any {
	return map[string]any{
		"plan": "claude/cc-max-20x", "account": "supplier-account@example.com", "password": "private-account-password",
		"auth_json":         map[string]any{"id_token": "", "access_token": "private-access-token", "refresh_token": "private-refresh-token", "last_refresh": "2026-10-20T10:00:00Z", "email": "supplier-account@example.com", "account_uuid": "private-account-uuid", "organization_uuid": "private-organization-uuid", "organization_name": "organization", "claude_device_ids": []any{"private-device-id"}, "type": "claude", "expired": "2026-11-20T10:00:00Z", "proxy_url": "http://private-user:private-password@proxy.example:8080", "verification_status": "verified"},
		"register_location": "US", "register_time": "2026-10-01", "plan_start_time": "2026-10-01", "plan_end_time": "2026-11-01", "payment_method": "third_party_payment", "register_device": "Windows", "balance": float64(0), "warranty_period": "7", "tags": []any{"慢蒸"}, "remark": "test record"}
}
func configuredVendor(t *testing.T, a *app, admin uint64) core.VendorDTO {
	t.Helper()
	vendors, e := a.store.Vendors(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	id, _ := core.ParseBusinessID(vendors[0].ID)
	token := "mock-platform-access-token"
	result, e := a.store.PatchVendor(context.Background(), id, admin, core.VendorPatch{AccessToken: &token})
	if e != nil {
		t.Fatal(e)
	}
	return result
}
func createInput(v core.VendorDTO, key string) core.CreateSubmissionInput {
	var id string
	for _, p := range v.Protocols {
		if p.Code == "submit_account" {
			id = p.VersionID
		}
	}
	return core.CreateSubmissionInput{VendorID: v.ID, ProtocolVersionID: id, ClientRequestKey: key, Parameters: submissionParameters()}
}
func submissionDetail(t *testing.T, wBody []byte) core.SubmissionDetail {
	t.Helper()
	var response struct {
		Data core.SubmissionDetail `json:"data"`
	}
	if e := common.Unmarshal(wBody, &response); e != nil {
		t.Fatal(e)
	}
	return response.Data
}
func assertNoSecrets(t *testing.T, body string) {
	t.Helper()
	for _, secret := range []string{"private-account-password", "private-access-token", "private-refresh-token", "private-account-uuid", "private-user:private-password", "mock-platform-access-token"} {
		if strings.Contains(body, secret) {
			t.Fatalf("secret leaked: %s", secret)
		}
	}
}

func TestVendorSubmissionPersistenceAndRedaction(t *testing.T) {
	a, _, _ := fixture(t)
	admin, _, _ := vendorSession(t, a, "vendor-admin@example.com", "admin")
	member, cookie, csrf := vendorSession(t, a, "vendor-member@example.com", "member")
	vendor := configuredVendor(t, a, admin.ID)
	var calls atomic.Int32
	a.vendorTransport = vendorRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.Scheme != "https" || r.URL.Host != "120.233.254.152:8000" || r.URL.Path != "/supplier-account/api/openapi/claude/accounts" {
			t.Error("wrong endpoint", r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer mock-platform-access-token" {
			t.Error("missing credential")
		}
		var got map[string]any
		raw, _ := io.ReadAll(r.Body)
		if common.Unmarshal(raw, &got) != nil {
			t.Error("invalid JSON")
		}
		if got["balance"] != float64(0) || got["password"] != "private-account-password" || got["payment_method"] != "third_party_payment" {
			t.Error("parameters changed", got["balance"])
		}
		return remoteResponse(200, `{"id":9007199254740993,"alias":"private-account-password / mock-platform-access-token","password":"private-account-password","msg":"do not expose"}`), nil
	})
	input := createInput(vendor, "request-key-000000001")
	w := request(t, a, "POST", "submissions", input, cookie, csrf)
	requireStatus(t, w, 200)
	assertNoSecrets(t, w.Body.String())
	detail := submissionDetail(t, w.Body.Bytes())
	if detail.Submission.Status != "accepted" || *detail.Submission.RemoteID != "9007199254740993" || detail.Submission.Parameters["password"] != "[REDACTED]" {
		t.Fatal("incorrect submission", detail.Submission.Status)
	}
	id, _ := core.ParseBusinessID(detail.Submission.ID)
	db, e := a.store.OwnedSubmission(context.Background(), member.ID, id)
	if e != nil {
		t.Fatal(e)
	}
	if db.ParametersJSON == "" || db.Account != input.Parameters["account"] || db.SecretParametersCiphertext == "" || !strings.HasPrefix(db.SecretParametersCiphertext, "enc:v1:") {
		t.Fatal("submission snapshot not encrypted/persisted")
	}
	assertNoSecrets(t, db.ParametersJSON+db.SecretParametersCiphertext)
	var ex core.ProtocolExecution
	if e = a.store.DB.Where("submission_id = ?", id).First(&ex).Error; e != nil {
		t.Fatal(e)
	}
	if ex.Status != "succeeded" || ex.HTTPStatus == nil || *ex.HTTPStatus != 200 || ex.RequestID == "" {
		t.Fatal("missing execution metadata")
	}
	assertNoSecrets(t, ex.RequestJSON+ex.ResultJSON)
	replay := request(t, a, "POST", "submissions", input, cookie, csrf)
	requireStatus(t, replay, 200)
	if calls.Load() != 1 {
		t.Fatal("idempotent replay resent to vendor")
	}
	input.Parameters["remark"] = "changed"
	requireStatus(t, request(t, a, "POST", "submissions", input, cookie, csrf), 409)
	input.ClientRequestKey = "request-key-000000002"
	requireStatus(t, request(t, a, "POST", "submissions", input, cookie, csrf), 409)
	list := request(t, a, "GET", "submissions?search=supplier-account", nil, cookie, "")
	requireStatus(t, list, 200)
	if data(t, list)["total"] != float64(1) {
		t.Fatal("history filter failed")
	}
}
func TestVendorAuthorizationMissingCredentialAndConfigRotation(t *testing.T) {
	a, _, _ := fixture(t)
	admin, adminCookie, adminCSRF := vendorSession(t, a, "config-admin@example.com", "admin")
	_, cookie, csrf := vendorSession(t, a, "config-member@example.com", "member")
	requireStatus(t, request(t, a, "GET", "vendors", nil, nil, ""), 401)
	vs, e := a.store.Vendors(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if vs[0].Configured || len(vs[0].Protocols) != 3 {
		t.Fatal("seeded token or missing protocols")
	}
	requireStatus(t, request(t, a, "POST", "submissions", createInput(vs[0], "no-credential-00001"), cookie, csrf), 409)
	var count int64
	a.store.DB.Model(&core.Submission{}).Count(&count)
	if count != 0 {
		t.Fatal("unconfigured submission persisted")
	}
	requireStatus(t, request(t, a, "PUT", "admin/vendors/"+vs[0].ID, map[string]any{"access_token": "token-value"}, cookie, csrf), 403)
	requireStatus(t, request(t, a, "PUT", "admin/vendors/"+vs[0].ID, map[string]any{"access_token": "token-value"}, adminCookie, ""), 403)
	v := configuredVendor(t, a, admin.ID)
	oldVersion := v.Protocols[0].VersionID
	w := request(t, a, "PUT", "admin/vendors/"+v.ID, map[string]any{"access_token": "rotated-access-token", "timezone": "UTC", "enabled": true}, adminCookie, adminCSRF)
	requireStatus(t, w, 200)
	if strings.Contains(w.Body.String(), "rotated-access-token") {
		t.Fatal("config secret leaked")
	}
	var versions []core.ProtocolVersion
	a.store.DB.Order("id").Find(&versions)
	if len(versions) != 9 {
		t.Fatalf("expected immutable seeded and two published versions; got %d", len(versions))
	}
	oldID, _ := core.ParseBusinessID(oldVersion)
	var old core.ProtocolVersion
	a.store.DB.First(&old, oldID)
	if old.Version != 2 {
		t.Fatal("published version changed")
	}
	var creds []core.VendorCredential
	a.store.DB.Find(&creds)
	if len(creds) != 2 || creds[0].SecretCiphertext == "mock-platform-access-token" {
		t.Fatal("credential storage invalid")
	}
	requireStatus(t, request(t, a, "PUT", "admin/vendors/"+v.ID, map[string]any{"base_url": "https://evil.example"}, adminCookie, adminCSRF), 400)
	requireStatus(t, request(t, a, "PUT", "admin/vendors/"+v.ID, map[string]any{"timezone": "invalid/timezone"}, adminCookie, adminCSRF), 400)
}
func TestVendorOwnerIsolationAndQueries(t *testing.T) {
	a, _, _ := fixture(t)
	admin, _, _ := vendorSession(t, a, "query-admin@example.com", "admin")
	_, cookie, csrf := vendorSession(t, a, "query-owner@example.com", "member")
	_, other, otherCSRF := vendorSession(t, a, "query-other@example.com", "member")
	vendor := configuredVendor(t, a, admin.ID)
	var calls atomic.Int32
	var nullMetrics atomic.Bool
	a.vendorTransport = vendorRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if strings.HasSuffix(r.URL.Path, "/accounts") {
			return remoteResponse(200, `{"id":12345,"alias":"claude-test"}`), nil
		}
		var body struct {
			IDs       []uint64 `json:"account_ids"`
			Aggregate *bool    `json:"aggregate"`
		}
		raw, _ := io.ReadAll(r.Body)
		common.Unmarshal(raw, &body)
		if len(body.IDs) != 1 || body.IDs[0] != 12345 {
			t.Error("foreign account IDs sent")
		}
		if strings.Contains(r.URL.Path, "metrics") {
			if body.Aggregate == nil || *body.Aggregate {
				t.Error("metrics aggregate must be false")
			}
			if nullMetrics.Load() {
				return remoteResponse(200, `{"accounts":[]}`), nil
			}
			return remoteResponse(200, `{"accounts":[{"id":999,"aid":999},{"id":12345,"aid":98765,"account_alias":"claude-test","status":"online","requests":0,"cost":99999,"success_rate":null,"prompt_tokens":1000,"completion_tokens":null}]}`), nil
		}
		return remoteResponse(200, `{"list":[{"id":12345,"request_count":3,"prompt_tokens":1000,"completion_tokens":200,"cache_read_input_tokens":100,"cache_write_input_tokens":0,"cost":12.34}],"range":{"start_ds":"2026-10-01","end_ds":20261002}}`), nil
	})
	created := request(t, a, "POST", "submissions", createInput(vendor, "owned-submission-0001"), cookie, csrf)
	requireStatus(t, created, 200)
	id := submissionDetail(t, created.Body.Bytes()).Submission.ID
	requireStatus(t, request(t, a, "GET", "submissions/"+id, nil, other, ""), 404)
	now := time.Now().UTC().Add(-time.Minute)
	metrics := map[string]any{"begin_time": now.Add(-time.Hour).Format("2006-01-02 15:04:05"), "end_time": now.Format("2006-01-02 15:04:05")}
	requireStatus(t, request(t, a, "POST", "submissions/"+id+"/metrics", metrics, other, otherCSRF), 404)
	requireStatus(t, request(t, a, "POST", "submissions/"+id+"/metrics", metrics, cookie, ""), 403)
	metrics["account_ids"] = []int{999}
	requireStatus(t, request(t, a, "POST", "submissions/"+id+"/metrics", metrics, cookie, csrf), 400)
	delete(metrics, "account_ids")
	queried := request(t, a, "POST", "submissions/"+id+"/metrics", metrics, cookie, csrf)
	requireStatus(t, queried, 200)
	detail := submissionDetail(t, queried.Body.Bytes())
	if detail.Metrics == nil || detail.Metrics.Account == nil || detail.Metrics.Account.Requests == nil || *detail.Metrics.Account.Requests != 0 || detail.Metrics.Account.CompletionTokens != nil || detail.Metrics.Account.SuccessRate != nil || *detail.Submission.PlatformAccountID != "98765" {
		t.Fatal("metrics null/zero/aid handling invalid")
	}
	if strings.Contains(queried.Body.String(), "99999") {
		t.Fatal("obsolete metrics cost exposed")
	}
	dates := map[string]string{"start_ds": now.AddDate(0, 0, -1).Format("2006-01-02"), "end_ds": now.Format("2006-01-02")}
	bills := request(t, a, "POST", "submissions/"+id+"/bills", dates, cookie, csrf)
	requireStatus(t, bills, 200)
	detail = submissionDetail(t, bills.Body.Bytes())
	if detail.Bills == nil || detail.Bills.Currency != "CNY" || detail.Bills.Account == nil || *detail.Bills.Account.Cost != 12.34 || detail.Bills.StartDS != "2026-10-01" || detail.Bills.EndDS != "2026-10-02" {
		t.Fatal("billing range/currency wrong")
	}
	nullMetrics.Store(true)
	nullResponse := request(t, a, "POST", "submissions/"+id+"/metrics", metrics, cookie, csrf)
	requireStatus(t, nullResponse, 200)
	detail = submissionDetail(t, nullResponse.Body.Bytes())
	if detail.Metrics == nil || detail.Metrics.Account != nil {
		t.Fatal("missing metrics fabricated")
	}
	list := request(t, a, "GET", "submissions", nil, other, "")
	requireStatus(t, list, 200)
	if data(t, list)["total"] != float64(0) {
		t.Fatal("foreign submission listed")
	}
	if calls.Load() != 4 {
		t.Fatal("unauthorized request reached upstream", calls.Load())
	}
}
func TestVendorUncertainFailureAllowsExplicitRetry(t *testing.T) {
	for _, mode := range []string{"network", "partial", "500", "redirect", "malformed", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			a, _, _ := fixture(t)
			admin, _, _ := vendorSession(t, a, "uncertain-admin@example.com", "admin")
			_, cookie, csrf := vendorSession(t, a, "uncertain-member@example.com", "member")
			vendor := configuredVendor(t, a, admin.ID)
			var calls atomic.Int32
			a.vendorTransport = vendorRoundTrip(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				switch mode {
				case "network":
					return nil, errors.New("remote transport closed after send")
				case "partial":
					res := remoteResponse(200, "")
					res.Body = &brokenVendorBody{}
					return res, nil
				case "500":
					return remoteResponse(500, `{"error":"private-access-token"}`), nil
				case "redirect":
					res := remoteResponse(302, "")
					res.Header.Set("Location", "https://evil.example")
					return res, nil
				case "malformed":
					return remoteResponse(200, `{"message":"private-account-password"}`), nil
				default:
					return remoteResponse(200, strings.Repeat("x", 300<<10)), nil
				}
			})
			input := createInput(vendor, "uncertain-request-001")
			w := request(t, a, "POST", "submissions", input, cookie, csrf)
			requireStatus(t, w, 200)
			detail := submissionDetail(t, w.Body.Bytes())
			if detail.Submission.Status != "unknown" || detail.Executions[0].Status != "unknown" {
				t.Fatal("uncertain send marked confirmed")
			}
			assertNoSecrets(t, w.Body.String())
			requireStatus(t, request(t, a, "POST", "submissions", input, cookie, csrf), 200)
			if calls.Load() != 1 {
				t.Fatal("same request key replay or automatic retry sent another request")
			}
			input.ClientRequestKey = "uncertain-request-002"
			retry := request(t, a, "POST", "submissions", input, cookie, csrf)
			requireStatus(t, retry, 200)
			retried := submissionDetail(t, retry.Body.Bytes())
			if retried.Submission.ID == detail.Submission.ID || retried.Submission.Status != "unknown" || calls.Load() != 2 {
				t.Fatal("explicit retry must create a new unknown record and exactly one request")
			}
			var saved core.Submission
			if err := a.store.DB.First(&saved, detail.Submission.ID).Error; err != nil || saved.Status != "unknown" || saved.ActiveAccountKey != nil {
				t.Fatal("original uncertain outcome was overwritten or retained a retry lock", err)
			}
		})
	}
}

type brokenVendorBody struct{}

func (*brokenVendorBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (*brokenVendorBody) Close() error             { return nil }
func TestVendorConcurrentIdempotency(t *testing.T) {
	a, _, _ := fixture(t)
	admin, _, _ := vendorSession(t, a, "race-admin@example.com", "admin")
	member, _, _ := vendorSession(t, a, "race-member@example.com", "member")
	vendor := configuredVendor(t, a, admin.ID)
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	transport := vendorRoundTrip(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-release
		return remoteResponse(200, `{"id":12345,"alias":"test"}`), nil
	})
	service := core.VendorService{Store: a.store, Transport: transport}
	input := createInput(vendor, "concurrent-key-000001")
	first := make(chan error, 1)
	go func() { _, e := service.Create(context.Background(), member.ID, input); first <- e }()
	<-entered
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, e := service.Create(context.Background(), member.ID, input)
			if e == nil && d.Submission.Status != "running" {
				e = fmt.Errorf("expected running replay, got %s", d.Submission.Status)
			}
			errs <- e
		}()
	}
	wg.Wait()
	close(release)
	if e := <-first; e != nil {
		t.Fatal(e)
	}
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("multiple upstream requests")
	}
	var submissions, executions int64
	a.store.DB.Model(&core.Submission{}).Count(&submissions)
	a.store.DB.Model(&core.ProtocolExecution{}).Count(&executions)
	if submissions != 1 || executions != 1 {
		t.Fatal("duplicate durable records")
	}
}
func TestVendorValidationDoesNotReachRemote(t *testing.T) {
	cases := map[string]func(map[string]any){"wrong plan": func(m map[string]any) { m["plan"] = "codex/pro" }, "bad auth": func(m map[string]any) { m["auth_json"] = "{}" }, "empty auth": func(m map[string]any) { m["auth_json"] = nil }, "unknown key": func(m map[string]any) { m["provider"] = "evil" }, "account spaces": func(m map[string]any) { m["account"] = "has space" }, "mutually exclusive tags": func(m map[string]any) { m["tags"] = []any{"速刷", "慢蒸"} }, "invalid date": func(m map[string]any) { m["register_time"] = "2026-02-30" }, "invalid balance": func(m map[string]any) { m["balance"] = "NaN" }, "invalid warranty": func(m map[string]any) { m["warranty_period"] = float64(0) }}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := submissionParameters()
			mutate(p)
			if _, _, e := core.ValidateSubmissionInput(p); e == nil {
				t.Fatal("invalid parameters accepted")
			}
		})
	}
	p := submissionParameters()
	auth, _ := common.Marshal(p["auth_json"])
	p["auth_json"] = string(auth)
	normalized, safe, e := core.ValidateSubmissionInput(p)
	if e != nil {
		t.Fatal(e)
	}
	if normalized["balance"] != float64(0) || safe["auth_json"] != "[REDACTED]" {
		t.Fatal("string auth or zero rejected")
	}
}
func TestVendorDefiniteFailureAndRecovery(t *testing.T) {
	a, _, _ := fixture(t)
	admin, _, _ := vendorSession(t, a, "recovery-admin@example.com", "admin")
	member, cookie, csrf := vendorSession(t, a, "recovery-member@example.com", "member")
	vendor := configuredVendor(t, a, admin.ID)
	a.vendorTransport = vendorRoundTrip(func(*http.Request) (*http.Response, error) {
		return remoteResponse(400, `{"error":"private-account-password"}`), nil
	})
	w := request(t, a, "POST", "submissions", createInput(vendor, "definite-failure-0001"), cookie, csrf)
	requireStatus(t, w, 200)
	detail := submissionDetail(t, w.Body.Bytes())
	assertNoSecrets(t, w.Body.String())
	if detail.Submission.Status != "failed" || detail.Executions[0].Status != "failed" {
		t.Fatal("definite rejection not recorded")
	}
	id, _ := core.ParseBusinessID(detail.Submission.ID)
	old := time.Now().UTC().Add(-3 * time.Minute)
	key := "recovered-active-key"
	a.store.DB.Model(&core.Submission{}).Where("id = ?", id).Updates(map[string]any{"status": "running", "created_at": old, "active_account_key": key})
	a.store.DB.Model(&core.ProtocolExecution{}).Where("submission_id = ?", id).Updates(map[string]any{"status": "running", "created_at": old, "finished_at": nil})
	if e := a.store.RecoverVendorExecutions(context.Background()); e != nil {
		t.Fatal(e)
	}
	after, e := a.store.SubmissionDetail(context.Background(), member.ID, id)
	if e != nil {
		t.Fatal(e)
	}
	if after.Submission.Status != "unknown" || after.Executions[0].Status != "unknown" {
		t.Fatal("interrupted execution not preserved as unknown")
	}
	var saved core.Submission
	if e = a.store.DB.First(&saved, id).Error; e != nil || saved.ActiveAccountKey != nil {
		t.Fatal("interrupted execution still prevents a manual retry", e)
	}
}
