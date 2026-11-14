package main

import (
	"context"
	"fmt"
	"goapi-tk-register/server/internal/core"
	"goapi-tk-register/server/internal/supplier"
	"strings"
	"testing"
	"time"
)

func TestMemberCreationVerifiedAndSessionPreserved(t *testing.T) {
	a, _, mail := fixture(t)
	actor, cookie, csrf := vendorSession(t, a, "creator@example.com", "member")
	ctx := context.Background()
	email := "new-member@example.com"
	a.adminEmail = email
	body := map[string]string{"email": email, "username": email, "password": "correct-password", "code": "not-sent"}
	requireStatus(t, request(t, a, "POST", "members", body, cookie, csrf), 400)
	requireStatus(t, request(t, a, "POST", "members/registration-code", map[string]string{"email": email}, cookie, csrf), 200)
	body["code"] = mail.code(email, "register")
	body["role"] = "admin"
	requireStatus(t, request(t, a, "POST", "members", body, cookie, csrf), 400)
	delete(body, "role")
	body["parent_user_id"] = "99"
	requireStatus(t, request(t, a, "POST", "members", body, cookie, csrf), 400)
	delete(body, "parent_user_id")
	body["username"] = "mismatch@example.com"
	requireStatus(t, request(t, a, "POST", "members", body, cookie, csrf), 400)
	body["username"] = email
	w := request(t, a, "POST", "members", body, cookie, csrf)
	requireStatus(t, w, 200)
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("login replaced")
	}
	u, err := a.store.GetUserByEmail(ctx, email)
	if err != nil || u.Role != "member" || u.ParentUserID == nil || *u.ParentUserID != actor.ID {
		t.Fatal(u, err)
	}
	if a.redis.Exists(ctx, indexKey(u.ID)).Val() != 0 {
		t.Fatal("new user got session")
	}
	me := request(t, a, "GET", "auth/me", nil, cookie, "")
	requireStatus(t, me, 200)
	if data(t, me)["user"].(map[string]any)["email"] != actor.Email {
		t.Fatal("creator changed")
	}
	requireStatus(t, request(t, a, "POST", "members", body, cookie, csrf), 409)
	direct := request(t, a, "GET", "members", nil, cookie, "")
	requireStatus(t, direct, 200)
	if data(t, direct)["total"].(float64) != 1 {
		t.Fatal("missing member")
	}
}
func TestMemberCreationAuthorizationAndDeliveryFailure(t *testing.T) {
	a, _, mail := fixture(t)
	_, cookie, csrf := vendorSession(t, a, "member@example.com", "member")
	body := map[string]string{"email": "target@example.com"}
	requireStatus(t, request(t, a, "POST", "members/registration-code", body, nil, ""), 401)
	requireStatus(t, request(t, a, "POST", "members/registration-code", body, cookie, ""), 403)
	if mail.count != 0 {
		t.Fatal("unauthorized email sent")
	}
	mail.fail = true
	requireStatus(t, request(t, a, "POST", "members/registration-code", body, cookie, csrf), 503)
	input := map[string]string{"email": body["email"], "username": body["email"], "password": "correct-password", "code": "ABC123"}
	requireStatus(t, request(t, a, "POST", "members", input, cookie, csrf), 400)
}
func TestMemberHTTPReadScopeAndMutationIsolation(t *testing.T) {
	a, _, _ := fixture(t)
	parent, cookie, csrf := vendorSession(t, a, "parent@example.com", "member")
	ctx := context.Background()
	child := core.User{Email: "child@example.com", PasswordHash: "fake", EmailVerifiedAt: time.Now()}
	if err := a.store.CreateMemberByUser(ctx, parent.ID, parent.AuthVersion, &child); err != nil {
		t.Fatal(err)
	}
	grand := core.User{Email: "hidden-grand@example.com", PasswordHash: "fake", EmailVerifiedAt: time.Now()}
	if err := a.store.CreateMemberByUser(ctx, child.ID, child.AuthVersion, &grand); err != nil {
		t.Fatal(err)
	}
	v := configuredVendor(t, a, parent.ID)
	seedAdminSubmission(t, a, grand.ID, v, "grand-account@example.com", "accepted", time.Now())
	row := syncTarget(t, a, grand.ID)
	for _, route := range []string{"accounts", fmt.Sprintf("accounts/%d", row.ID), fmt.Sprintf("accounts/%d/snapshots", row.ID), "submissions"} {
		w := request(t, a, "GET", route, nil, cookie, "")
		requireStatus(t, w, 200)
		if strings.Contains(w.Body.String(), grand.Email) {
			t.Fatal("true owner leaked")
		}
	}
	for _, method := range []string{"GET", "POST", "PUT"} {
		requireStatus(t, request(t, a, method, fmt.Sprintf("accounts/%d/auth-json", row.ID), map[string]any{}, cookie, csrf), 404)
	}
	requireStatus(t, request(t, a, "POST", fmt.Sprintf("accounts/%d/delete", row.ID), map[string]any{"request_id": "member-delete-forbidden-0001"}, cookie, csrf), 404)
	requireStatus(t, request(t, a, "GET", fmt.Sprintf("accounts?sources=%d", grand.ID), nil, cookie, ""), 400)
	_, otherCookie, _ := vendorSession(t, a, "outsider@example.com", "member")
	requireStatus(t, request(t, a, "GET", fmt.Sprintf("accounts/%d", row.ID), nil, otherCookie, ""), 404)
	// A manual ancestor sync is authorized, but remains attributed to its actor.
	configureSync(t, a, parent.ID, false)
	fake := &mockPageCollector{run: func(ctx context.Context, targets []supplier.Target) ([]supplier.AccountResult, error) {
		return []supplier.AccountResult{pageRecord(targets[0])}, nil
	}}
	a.accountPageCollector = fake
	w := request(t, a, "POST", "accounts/sync", pageInput(row), cookie, csrf)
	requireStatus(t, w, 200)
	if fake.calls != 1 || strings.Contains(w.Body.String(), grand.Email) {
		t.Fatal("ancestor sync or response scope failed")
	}
	requireStatus(t, request(t, a, "POST", "accounts/sync", pageInput(row), otherCookie, ""), 403)
	// Browser-only scope does not grant administrator access.
	requireStatus(t, request(t, a, "GET", "admin/users", nil, cookie, ""), 403)
}
