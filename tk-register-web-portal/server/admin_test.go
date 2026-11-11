package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"goapi-tk-register/server/common"
	"goapi-tk-register/server/internal/core"
)

func TestAdminUsersGuardsAndRoleRevocation(t *testing.T) {
	a, _, mail := fixture(t)
	_, adminCookie, adminCSRF := vendorSession(t, a, "admin@example.com", "admin")
	target, memberCookie, memberCSRF := vendorSession(t, a, "member@example.com", "member")
	second, secondCookie, secondCSRF := vendorSession(t, a, "second@example.com", "admin")
	for _, route := range []string{"admin/users", "admin/accounts", "admin/accounts/1", "admin/submissions", "admin/submissions/1"} {
		requireStatus(t, request(t, a, "GET", route, nil, memberCookie, ""), 403)
		requireStatus(t, request(t, a, "GET", route, nil, nil, ""), 401)
	}
	input := map[string]string{"email": target.Email, "role": "admin"}
	requireStatus(t, request(t, a, "POST", "admin/users/role", input, memberCookie, memberCSRF), 403)
	requireStatus(t, request(t, a, "POST", "admin/users/role", input, adminCookie, ""), 403)
	for _, op := range []string{"status", "bills"} {
		requireStatus(t, request(t, a, "POST", "admin/submissions/1/"+op, map[string]any{}, memberCookie, memberCSRF), 403)
		requireStatus(t, request(t, a, "POST", "admin/submissions/1/"+op, map[string]any{}, adminCookie, ""), 403)
	}
	r := httptest.NewRequest("POST", basePath+"/api/admin/users/role", strings.NewReader(`{"email":"member@example.com","role":"admin"}`))
	r.Header.Set("Origin", "https://evil.example")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", adminCSRF)
	r.AddCookie(adminCookie)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	requireStatus(t, w, 403)
	w = request(t, a, "POST", "admin/users/role", map[string]string{"email": " MEMBER@EXAMPLE.COM ", "role": "admin"}, adminCookie, adminCSRF)
	requireStatus(t, w, 200)
	if data(t, w)["user"].(map[string]any)["id"] != fmt.Sprint(target.ID) {
		t.Fatal("IDs must be strings")
	}
	requireStatus(t, request(t, a, "GET", "auth/me", nil, memberCookie, ""), 401)
	promoted, e := a.store.GetUserByID(context.Background(), target.ID)
	if e != nil || promoted.AuthVersion != 1 || promoted.Role != "admin" {
		t.Fatalf("role persistence: %+v %v", promoted, e)
	}
	// A promoted user's password alone must not establish an admin session.
	pw := password(t, a, target.Email)
	requireStatus(t, pw, 200)
	challengeID := data(t, pw)["challenge_id"].(string)
	code := mail.code(target.Email, "admin_login:"+digest(challengeID))
	if len(pw.Result().Cookies()) != 0 {
		t.Fatal("password-only admin session established")
	}
	oldAdminToken, oldAdminSession, e := a.newSession(context.Background(), target.ID, "admin", 28800, 1800, promoted.AuthVersion)
	if e != nil {
		t.Fatal(e)
	}
	oldAdminCookie := &http.Cookie{Name: cookieName, Value: oldAdminToken}
	requireStatus(t, request(t, a, "GET", "admin/users", nil, oldAdminCookie, ""), 200)
	requireStatus(t, request(t, a, "POST", "admin/users/role", map[string]string{"email": target.Email, "role": "member"}, oldAdminCookie, oldAdminSession.CSRF), 409)
	requireStatus(t, request(t, a, "POST", "admin/users/role", map[string]string{"email": "admin@example.com", "role": "member"}, secondCookie, secondCSRF), 409)
	requireStatus(t, request(t, a, "POST", "admin/users/role", map[string]string{"email": second.Email, "role": "admin"}, adminCookie, adminCSRF), 409)
	requireStatus(t, request(t, a, "POST", "admin/users/role", map[string]string{"email": "nobody@example.com", "role": "admin"}, adminCookie, adminCSRF), 404)
	requireStatus(t, request(t, a, "POST", "admin/users/role", map[string]string{"email": "not-an-email", "role": "admin"}, adminCookie, adminCSRF), 400)
	for _, role := range []string{"member", "admin"} {
		requireStatus(t, request(t, a, "POST", "admin/users/role", map[string]string{"email": target.Email, "role": role}, adminCookie, adminCSRF), 200)
	}
	// Demote/re-promote must not resurrect either a session or first-factor challenge.
	requireStatus(t, request(t, a, "GET", "admin/users", nil, oldAdminCookie, ""), 401)
	requireStatus(t, request(t, a, "POST", "auth/admin/verify", map[string]string{"challenge_id": challengeID, "code": code}, nil, ""), 401)
	requireStatus(t, request(t, a, "POST", "auth/admin/resend", map[string]string{"challenge_id": challengeID}, nil, ""), 401)
	if _, _, e = a.newSession(context.Background(), target.ID, "admin", 28800, 1800, promoted.AuthVersion); e == nil {
		t.Fatal("stale in-flight login minted a valid session")
	}
	var audits []core.AdminRoleAudit
	if e = a.store.DB.Order("id").Find(&audits).Error; e != nil || len(audits) != 3 {
		t.Fatalf("audit rows %+v %v", audits, e)
	}
	if audits[0].FromRole != "member" || audits[0].ToRole != "admin" || audits[1].ToRole != "member" {
		t.Fatal("incorrect audit transitions")
	}
	listing := request(t, a, "GET", "admin/users?search=member", nil, adminCookie, "")
	requireStatus(t, listing, 200)
	if data(t, listing)["total"].(float64) != 1 {
		t.Fatal("user search")
	}
	for _, secret := range []string{"password_hash", "auth_version", "email_verified_at", "correct-password"} {
		if strings.Contains(listing.Body.String(), secret) {
			t.Fatal("user list exposed unnecessary authentication data", secret)
		}
	}
}

func seedAdminSubmission(t *testing.T, a *app, owner uint64, vendor core.VendorDTO, account, status string, created time.Time) core.Submission {
	t.Helper()
	vid, _ := core.ParseBusinessID(vendor.ID)
	var version uint64
	for _, p := range vendor.Protocols {
		if p.Code == "submit_account" {
			version, _ = core.ParseBusinessID(p.VersionID)
		}
	}
	r := core.Submission{UserID: owner, VendorID: vid, ProtocolVersionID: version, ClientRequestKey: fmt.Sprintf("fixture-%d", time.Now().UnixNano()), RequestHash: "fixture", Account: account, Plan: "claude/cc-max-20x", ParametersJSON: `{"register_location":"US","password":{"configured":true},"auth_json":{"configured":true}}`, SecretParametersCiphertext: "private-access-token", Status: status, CreatedAt: created, UpdatedAt: created}
	if status == "accepted" {
		remote := fmt.Sprint(created.UnixNano())
		r.RemoteID = &remote
	}
	if e := a.store.DB.Create(&r).Error; e != nil {
		t.Fatal(e)
	}
	// Imported fixtures explicitly build the account projection; GET is read-only.
	if e := a.store.EnsureSyncedAccounts(context.Background()); e != nil {
		t.Fatal(e)
	}
	return r
}
func decodeAdminPage(t *testing.T, w *httptest.ResponseRecorder) core.AdminAccountPage {
	t.Helper()
	var e struct {
		Data core.AdminAccountPage `json:"data"`
	}
	if err := common.Unmarshal(w.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	return e.Data
}
func TestAdminAccountGroupingLatestAndCurrentStatus(t *testing.T) {
	a, _, _ := fixture(t)
	admin, ac, _ := vendorSession(t, a, "admin@example.com", "admin")
	owner, oc, _ := vendorSession(t, a, "owner@example.com", "member")
	other, _, _ := vendorSession(t, a, "other@example.com", "member")
	vendor := configuredVendor(t, a, admin.ID)
	now := time.Now().UTC().Truncate(time.Millisecond)
	prior := seedAdminSubmission(t, a, owner.ID, vendor, " Alice@Example.com ", "accepted", now.Add(-2*time.Hour))
	active := "active"
	metrics, _ := common.Marshal(core.MetricResult{QueriedAt: now.Add(-time.Hour)})
	if e := a.store.DB.Model(&prior).Updates(map[string]any{"remote_status": active, "metrics_json": string(metrics)}).Error; e != nil {
		t.Fatal(e)
	}
	failed := seedAdminSubmission(t, a, owner.ID, vendor, "alice@example.com", "failed", now)
	tie := seedAdminSubmission(t, a, owner.ID, vendor, "ALICE@example.com", "unknown", now)
	// A greater ID with an older timestamp must not become the latest submission.
	seedAdminSubmission(t, a, owner.ID, vendor, "alice@example.com", "failed", now.Add(-time.Minute))
	seedAdminSubmission(t, a, other.ID, vendor, "alice@example.com", "accepted", now)
	v2 := core.Vendor{Code: "other-vendor", Name: "Other Vendor", BaseURL: core.VendorOrigin, Enabled: true, Timezone: "Asia/Shanghai"}
	if e := a.store.DB.Create(&v2).Error; e != nil {
		t.Fatal(e)
	}
	otherVendor := vendor
	otherVendor.ID = fmt.Sprint(v2.ID)
	seedAdminSubmission(t, a, owner.ID, otherVendor, "alice@example.com", "accepted", now)
	failedOnly := seedAdminSubmission(t, a, owner.ID, vendor, "failed-only@example.com", "failed", now)
	w := request(t, a, "GET", "admin/accounts", nil, ac, "")
	requireStatus(t, w, 200)
	assertNoSecrets(t, w.Body.String())
	page := decodeAdminPage(t, w)
	if page.Total != 4 || len(page.Items) != 4 {
		t.Fatalf("expected four distinct owner/vendor/account groups: %+v", page)
	}
	var group core.AdminAccount
	for _, item := range page.Items {
		if item.User.ID == fmt.Sprint(owner.ID) && item.VendorID == vendor.ID && strings.EqualFold(strings.TrimSpace(item.Account), "alice@example.com") {
			group = item
		}
		if item.ID == fmt.Sprint(failedOnly.ID) && item.CurrentSubmission != nil {
			t.Fatal("fabricated status for never accepted account")
		}
	}
	if group.ID != fmt.Sprint(tie.ID) || group.LatestSubmission.Status != "unknown" || group.SubmissionCount != 4 || group.CurrentSubmission == nil || group.CurrentSubmission.ID != fmt.Sprint(prior.ID) || group.CurrentSubmission.RemoteStatus == nil || *group.CurrentSubmission.RemoteStatus != "active" {
		t.Fatalf("latest submit/current account status mixed up: %+v", group)
	}
	for _, anchor := range []uint64{prior.ID, failed.ID, tie.ID} {
		detail := request(t, a, "GET", fmt.Sprintf("admin/accounts/%d", anchor), nil, ac, "")
		requireStatus(t, detail, 200)
		var e struct {
			Data core.AdminAccountDetail `json:"data"`
		}
		if err := common.Unmarshal(detail.Body.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		if e.Data.Account.ID != group.ID || e.Data.History.Total != 4 {
			t.Fatal("old anchor did not resolve complete group")
		}
	}
	requireStatus(t, request(t, a, "GET", fmt.Sprintf("submissions/%d", prior.ID), nil, ac, ""), 404)
	requireStatus(t, request(t, a, "GET", fmt.Sprintf("admin/submissions/%d", prior.ID), nil, oc, ""), 403)
	history := request(t, a, "GET", fmt.Sprintf("admin/submissions?account_id=%d", failed.ID), nil, ac, "")
	requireStatus(t, history, 200)
	if data(t, history)["total"].(float64) != 4 {
		t.Fatal("history grouping")
	}
	filtered := request(t, a, "GET", "admin/accounts?status=accepted", nil, ac, "")
	requireStatus(t, filtered, 200)
	if decodeAdminPage(t, filtered).Total != 2 {
		t.Fatal("filter used historical status instead of latest status")
	}
	ownerSearch := request(t, a, "GET", "admin/accounts?search=other%40example.com", nil, ac, "")
	requireStatus(t, ownerSearch, 200)
	if decodeAdminPage(t, ownerSearch).Total != 1 {
		t.Fatal("owner search")
	}
	// A new successful empty metrics response clears prior status, even if an
	// unrelated bills query later advances last_checked_at on the old snapshot.
	newer := seedAdminSubmission(t, a, owner.ID, vendor, "alice@example.com", "accepted", now.Add(-30*time.Minute))
	fresh, e := a.store.AdminAccountDetail(context.Background(), prior.ID, 1)
	if e != nil {
		t.Fatal(e)
	}
	if fresh.Account.CurrentSubmission == nil || fresh.Account.CurrentSubmission.ID != fmt.Sprint(newer.ID) || fresh.Account.CurrentSubmission.StatusCheckedAt != nil {
		t.Fatal("newly accepted remote ID must become refresh target even without a snapshot")
	}
	empty, _ := common.Marshal(core.MetricResult{QueriedAt: now})
	if e := a.store.DB.Model(&newer).Update("metrics_json", string(empty)).Error; e != nil {
		t.Fatal(e)
	}
	if e := a.store.DB.Model(&prior).Update("last_checked_at", now.Add(time.Minute)).Error; e != nil {
		t.Fatal(e)
	}
	result, e := a.store.AdminAccountDetail(context.Background(), prior.ID, 1)
	if e != nil {
		t.Fatal(e)
	}
	if result.Account.CurrentSubmission == nil || result.Account.CurrentSubmission.ID != fmt.Sprint(newer.ID) || result.Account.CurrentSubmission.RemoteStatus != nil {
		t.Fatal("new empty metrics snapshot fell back to stale positive status")
	}
}

func TestAdminListsPaginationAndLiteralSearch(t *testing.T) {
	a, _, _ := fixture(t)
	admin, cookie, _ := vendorSession(t, a, "admin@example.com", "admin")
	vendor := configuredVendor(t, a, admin.ID)
	now := time.Now().UTC()
	for i := 0; i < 23; i++ {
		u := addUser(t, a, fmt.Sprintf("member-%02d@example.com", i), "member")
		seedAdminSubmission(t, a, u.ID, vendor, fmt.Sprintf("account-%02d@example.com", i), "failed", now.Add(time.Duration(i)*time.Second))
	}
	seedAdminSubmission(t, a, admin.ID, vendor, "literal_%@example.com", "failed", now)
	for _, route := range []string{"admin/accounts", "admin/submissions", "admin/users"} {
		first := request(t, a, "GET", route, nil, cookie, "")
		second := request(t, a, "GET", route+"?page=2", nil, cookie, "")
		requireStatus(t, first, 200)
		requireStatus(t, second, 200)
		d1, d2 := data(t, first), data(t, second)
		if len(d1["items"].([]any)) != 20 || len(d2["items"].([]any)) != 4 || d1["total"].(float64) != 24 {
			t.Fatal("pagination", route, d1["total"], d2["total"])
		}
		requireStatus(t, request(t, a, "GET", route+"?page=0", nil, cookie, ""), 400)
	}
	literal := request(t, a, "GET", "admin/accounts?search=_%25", nil, cookie, "")
	requireStatus(t, literal, 200)
	if decodeAdminPage(t, literal).Total != 1 {
		t.Fatal("LIKE wildcards were not escaped")
	}
	requireStatus(t, request(t, a, "GET", "admin/submissions?status=made-up", nil, cookie, ""), 400)
	requireStatus(t, request(t, a, "GET", "admin/submissions?account_id=999999", nil, cookie, ""), 404)
}

func TestAdminRemoteQueriesUseStoredOwnerAndActorRateLimit(t *testing.T) {
	a, _, _ := fixture(t)
	admin, ac, csrf := vendorSession(t, a, "admin@example.com", "admin")
	owner, oc, ocsrf := vendorSession(t, a, "owner@example.com", "member")
	vendor := configuredVendor(t, a, admin.ID)
	calls := 0
	a.vendorTransport = vendorRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Path == "/supplier-account/api/openapi/claude/accounts" {
			return remoteResponse(200, `{"id":123,"account_alias":"mock"}`), nil
		}
		if strings.Contains(r.URL.Path, "metrics") {
			var body struct {
				IDs []uint64 `json:"account_ids"`
			}
			raw, _ := io.ReadAll(r.Body)
			if common.Unmarshal(raw, &body) != nil || len(body.IDs) != 1 || body.IDs[0] != 123 {
				t.Fatal("did not query stored remote ID")
			}
			return remoteResponse(200, `{"accounts":[{"id":123,"status":"active"}]}`), nil
		}
		return remoteResponse(200, `{"list":[{"id":123,"cost":12.5}]}`), nil
	})
	created := request(t, a, "POST", "submissions", createInput(vendor, "admin-query-fixture"), oc, ocsrf)
	requireStatus(t, created, 200)
	id := submissionDetail(t, created.Body.Bytes()).Submission.ID
	updated := request(t, a, "POST", "admin/submissions/"+id+"/status", map[string]any{"force": true}, ac, csrf)
	requireStatus(t, updated, 200)
	assertNoSecrets(t, updated.Body.String())
	if submissionDetail(t, updated.Body.Bytes()).Metrics == nil || submissionDetail(t, updated.Body.Bytes()).Metrics.Account == nil {
		t.Fatal("status query did not return account metrics", updated.Body.String())
	}
	if data(t, updated)["user"].(map[string]any)["id"] != fmt.Sprint(owner.ID) || data(t, updated)["current_submission"] == nil {
		t.Fatal("lost owner/current metadata")
	}
	now := time.Now().In(time.FixedZone("Asia/Shanghai", 8*60*60))
	dates := map[string]string{"start_ds": now.Add(-24 * time.Hour).Format("2006-01-02"), "end_ds": now.Format("2006-01-02")}
	bills := request(t, a, "POST", "admin/submissions/"+id+"/bills", dates, ac, csrf)
	requireStatus(t, bills, 200)
	assertNoSecrets(t, bills.Body.String())
	if submissionDetail(t, bills.Body.Bytes()).Bills == nil {
		t.Fatal("bills query did not return parsed bills", bills.Body.String())
	}
	if data(t, bills)["account_id"] != id {
		t.Fatal("lost account metadata")
	}
	// Clients cannot override ownership, even with a valid admin session.
	requireStatus(t, request(t, a, "POST", "admin/submissions/"+id+"/status", map[string]any{"force": true, "user_id": admin.ID}, ac, csrf), 400)
	if calls != 3 {
		t.Fatal("unexpected network requests", calls)
	}
	key := redisPrefix + "vendor-action:query:" + fmt.Sprint(admin.ID)
	if e := a.redis.Set(context.Background(), key, "30", time.Minute).Err(); e != nil {
		t.Fatal(e)
	}
	requireStatus(t, request(t, a, "POST", "admin/submissions/"+id+"/status", map[string]any{"force": true}, ac, csrf), 429)
	if calls != 3 {
		t.Fatal("rate-limited query reached vendor")
	}
	if n := a.redis.Exists(context.Background(), redisPrefix+"vendor-action:query:"+fmt.Sprint(owner.ID)).Val(); n != 0 {
		t.Fatal("admin consumed owner's query limit")
	}
}

func TestAdminReadsRecoverInterruptedSubmissions(t *testing.T) {
	a, _, _ := fixture(t)
	admin, cookie, _ := vendorSession(t, a, "admin@example.com", "admin")
	vendor := configuredVendor(t, a, admin.ID)
	stale := seedAdminSubmission(t, a, admin.ID, vendor, "stale@example.com", "running", time.Now().UTC().Add(-3*time.Minute))
	w := request(t, a, "GET", "admin/accounts", nil, cookie, "")
	requireStatus(t, w, 200)
	page := decodeAdminPage(t, w)
	if len(page.Items) != 1 || page.Items[0].LatestSubmission.Status != "unknown" {
		t.Fatal("admin pool retained interrupted running state")
	}
	var stored core.Submission
	if e := a.store.DB.First(&stored, stale.ID).Error; e != nil || stored.Status != "unknown" {
		t.Fatal("recovery was not persisted", e)
	}
}

func TestConcurrentAdminDemotionsKeepAnActiveAdministrator(t *testing.T) {
	a, _, _ := fixture(t)
	first, fc, fcsrf := vendorSession(t, a, "first-admin@example.com", "admin")
	second, sc, scsrf := vendorSession(t, a, "second-admin@example.com", "admin")
	var wg sync.WaitGroup
	outcomes := make(chan int, 2)
	for _, item := range []struct {
		email  string
		cookie *http.Cookie
		csrf   string
	}{{second.Email, fc, fcsrf}, {first.Email, sc, scsrf}} {
		wg.Add(1)
		go func(email string, cookie *http.Cookie, csrf string) {
			defer wg.Done()
			w := request(t, a, "POST", "admin/users/role", map[string]string{"email": email, "role": "member"}, cookie, csrf)
			outcomes <- w.Code
		}(item.email, item.cookie, item.csrf)
	}
	wg.Wait()
	close(outcomes)
	successes := 0
	for status := range outcomes {
		if status == 200 {
			successes++
		} else if status != 401 && status != 403 {
			t.Fatal("unexpected concurrent demotion result", status)
		}
	}
	var remaining int64
	if e := a.store.DB.Model(&core.User{}).Where("role = ? AND status = ?", "admin", "active").Count(&remaining).Error; e != nil {
		t.Fatal(e)
	}
	if successes != 1 || remaining != 1 {
		t.Fatalf("concurrent role change invariant: success=%d remaining=%d", successes, remaining)
	}
}
