package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"goapi-tk-register/server/internal/core"
)

func TestAccountsOverviewSessionAndReadOnlyScope(t *testing.T) {
	a, owner, cookie, csrf, _, fake := setupPageSync(t)
	parent, parentCookie, _ := vendorSession(t, a, "overview-parent@example.com", "member")
	stranger, strangerCookie, _ := vendorSession(t, a, "overview-stranger@example.com", "member")
	if _, err := a.store.LinkExistingMember(context.Background(), parent.ID, parent.AuthVersion, owner.ID, owner.AuthVersion); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, request(t, a, "GET", "accounts/overview", nil, nil, ""), 401)
	for _, c := range []struct {
		name   string
		parent bool
	}{{"self", false}, {"parent", true}} {
		session := cookie
		if c.parent {
			session = parentCookie
		}
		w := request(t, a, "GET", "accounts/overview", nil, session, "")
		requireStatus(t, w, 200)
		if strings.Contains(w.Body.String(), "password") || strings.Contains(w.Body.String(), "identity_key") {
			t.Fatal("sensitive fields leaked")
		}
	}
	w := request(t, a, "GET", "accounts/overview?sources=none", nil, strangerCookie, "")
	requireStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"sources":[]`) {
		t.Fatal(w.Body.String())
	}
	requireStatus(t, request(t, a, "GET", fmt.Sprintf("accounts/overview?sources=%d", stranger.ID), nil, cookie, ""), 400)
	requireStatus(t, request(t, a, "GET", "accounts/overview?start_date=bad&end_date=bad", nil, cookie, ""), 400)
	requireStatus(t, request(t, a, "POST", "accounts/overview", map[string]any{}, cookie, csrf), 405)
	if fake.calls != 0 {
		t.Fatal("overview contacted supplier instead of reading saved data")
	}
}

func TestAdminAccountsOverviewAuthorizationAndReadOnly(t *testing.T) {
	a, owner, memberCookie, memberCSRF, accounts, fake := setupPageSync(t)
	admin, adminCookie, adminCSRF := vendorSession(t, a, "overview-admin@example.com", "admin")
	_, _, _ = vendorSession(t, a, "empty-admin@example.com", "admin")
	// The other administrative account routes recover expired executions. These
	// two Overview routes must leave pending submissions untouched.
	if err := a.store.DB.Model(&core.Submission{}).Where("id = ?", accounts[2].LatestSubmissionID).Updates(map[string]any{"status": "running", "created_at": time.Now().Add(-time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"admin/accounts/overview", "admin/accounts/overview/sources"} {
		requireStatus(t, request(t, a, "GET", route, nil, nil, ""), 401)
		requireStatus(t, request(t, a, "GET", route, nil, memberCookie, ""), 403)
		requireStatus(t, request(t, a, "POST", route, map[string]any{}, memberCookie, memberCSRF), 403)
		requireStatus(t, request(t, a, "POST", route, map[string]any{}, adminCookie, adminCSRF), 405)
		requireStatus(t, request(t, a, "DELETE", route, nil, adminCookie, adminCSRF), 405)
		w := request(t, a, "GET", route, nil, adminCookie, "")
		requireStatus(t, w, 200)
		if strings.Contains(w.Body.String(), "password") || strings.Contains(w.Body.String(), "identity_key") {
			t.Fatal("sensitive account data leaked", w.Body.String())
		}
	}
	w := request(t, a, "GET", "admin/accounts/overview/sources", nil, adminCookie, "")
	sources := data(t, w)["sources"].([]any)
	if sources[0].(map[string]any)["id"] != "myself" || sources[0].(map[string]any)["label"] != "myself" || len(sources) != 4 {
		t.Fatalf("incorrect administrative sources: %+v", sources)
	}
	for _, source := range sources[1:] {
		if source.(map[string]any)["id"] == fmt.Sprint(admin.ID) {
			t.Fatal("admin appeared twice in sources")
		}
	}
	w = request(t, a, "GET", fmt.Sprintf("admin/accounts/overview?sources=%d", owner.ID), nil, adminCookie, "")
	requireStatus(t, w, 200)
	sources = data(t, w)["sources"].([]any)
	if len(sources) != 1 || sources[0].(map[string]any)["total"] != float64(3) {
		t.Fatal("admin could not inspect another direct owner", w.Body.String())
	}
	for _, query := range []string{"sources=999999999", "sources=myself,all", "range=unsupported", "start_date=bad&end_date=bad"} {
		requireStatus(t, request(t, a, "GET", "admin/accounts/overview?"+query, nil, adminCookie, ""), 400)
	}
	w = request(t, a, "GET", "admin/accounts/overview?sources=none", nil, adminCookie, "")
	requireStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"sources":[]`) || !strings.Contains(w.Body.String(), `"rows":[]`) {
		t.Fatal("empty owner selection returned accounts", w.Body.String())
	}
	var unchanged core.Submission
	if err := a.store.DB.First(&unchanged, accounts[2].LatestSubmissionID).Error; err != nil || unchanged.Status != "running" {
		t.Fatal("overview recovered a submission instead of remaining read-only", unchanged.Status, err)
	}
	if fake.calls != 0 {
		t.Fatal("admin overview contacted the supplier")
	}
	// A bearer token alone cannot access these session-only administrator routes.
	r := httptest.NewRequest(http.MethodGet, basePath+"/api/admin/accounts/overview", nil)
	r.Header.Set("Authorization", "Bearer arbitrary-api-token")
	w = httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	requireStatus(t, w, 401)
	// Existing sessions are checked against the user's current role on each call.
	if err := a.store.DB.Model(&core.User{}).Where("id = ?", admin.ID).Update("role", "member").Error; err != nil {
		t.Fatal(err)
	}
	requireStatus(t, request(t, a, "GET", "admin/accounts/overview", nil, adminCookie, ""), 401)
}
