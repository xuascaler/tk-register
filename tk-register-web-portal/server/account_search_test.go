package main

import (
	"context"
	"net/url"
	"testing"
	"time"

	"goapi-tk-register/server/internal/core"
)

func TestAdminAccountSearchHierarchyRoutesAndPermissions(t *testing.T) {
	a, _, _ := fixture(t)
	root, rootCookie, _ := vendorSession(t, a, core.InitialSuperAdminEmail, "admin")
	admin, adminCookie, _ := vendorSession(t, a, "search-admin@example.test", "admin")
	owner, ownerCookie, _ := vendorSession(t, a, "search-parent@example.test", "member")
	ctx := context.Background()
	child := core.User{Email: "search-child@example.test", PasswordHash: "test-hash", EmailVerifiedAt: time.Now().UTC()}
	if err := a.store.CreateMemberByUser(ctx, owner.ID, owner.AuthVersion, &child); err != nil {
		t.Fatal(err)
	}
	leaf := core.User{Email: "search-leaf@example.test", PasswordHash: "test-hash", EmailVerifiedAt: time.Now().UTC()}
	if err := a.store.CreateMemberByUser(ctx, child.ID, child.AuthVersion, &leaf); err != nil {
		t.Fatal(err)
	}
	vendor := configuredVendor(t, a, admin.ID)
	for i, user := range []core.User{*owner, child, leaf, *admin} {
		seedAdminSubmission(t, a, user.ID, vendor, user.Email+".claude", "accepted", time.Now().UTC().Add(time.Duration(i)*time.Minute))
	}
	if err := a.store.EnsureSyncedAccounts(ctx); err != nil {
		t.Fatal(err)
	}
	fake := &mockAccountCollector{}
	a.accountCollector = fake
	query := "?search=" + url.QueryEscape(owner.Email)
	for _, route := range []string{"admin/synced-accounts", "admin/raw-accounts"} {
		requireStatus(t, request(t, a, "GET", route+query, nil, nil, ""), 401)
		requireStatus(t, request(t, a, "GET", route+query, nil, ownerCookie, ""), 403)
		cookie := adminCookie
		if route == "admin/raw-accounts" {
			requireStatus(t, request(t, a, "GET", route+query, nil, adminCookie, ""), 403)
			cookie = rootCookie
		}
		response := request(t, a, "GET", route+query, nil, cookie, "")
		requireStatus(t, response, 200)
		result := data(t, response)
		items := result["items"].([]any)
		if result["total"] != float64(3) || len(items) != 3 {
			t.Fatal("missing hierarchy results", route, result)
		}
		for i, email := range []string{owner.Email, leaf.Email, child.Email} {
			if items[i].(map[string]any)["user_email"] != email {
				t.Fatal("wrong priority or actual owner", route, items)
			}
		}
	}
	// A signed-in member's personal endpoint still uses account-email matching,
	// so searching the parent only returns the parent's own matching account.
	response := request(t, a, "GET", "accounts"+query, nil, ownerCookie, "")
	requireStatus(t, response, 200)
	personal := data(t, response)
	if personal["total"] != float64(1) {
		t.Fatal("personal search was expanded", personal)
	}
	if fake.collectCalls != 0 || fake.loginCalls != 0 {
		t.Fatal("search contacted the supplier")
	}
	if !root.IsSuperAdmin {
		t.Fatal("raw route was not tested with a super administrator")
	}
}
