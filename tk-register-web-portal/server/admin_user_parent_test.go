package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"goapi-tk-register/server/internal/core"
)

func TestAdminUserParentHTTPGuardsAndVersion(t *testing.T) {
	a, _, _ := fixture(t)
	ctx := context.Background()
	admin, adminCookie, csrf := vendorSession(t, a, "operator@example.com", "admin")
	parent, memberCookie, memberCSRF := vendorSession(t, a, "parent@example.com", "member")
	child := core.User{Email: "child@example.com", PasswordHash: "hash", EmailVerifiedAt: time.Now()}
	if err := a.store.CreateMemberByUser(ctx, parent.ID, parent.AuthVersion, &child); err != nil {
		t.Fatal(err)
	}
	listing := request(t, a, "GET", "admin/users?search=child", nil, adminCookie, "")
	requireStatus(t, listing, 200)
	row := data(t, listing)["items"].([]any)[0].(map[string]any)
	if row["parent"].(map[string]any)["id"] != fmt.Sprint(parent.ID) || row["membership_version"] != "1" {
		t.Fatal(row)
	}
	body := map[string]string{"user_id": fmt.Sprint(child.ID), "parent_id": fmt.Sprint(parent.ID), "membership_version": "1"}
	route := "admin/users/unlink-parent"
	requireStatus(t, request(t, a, "POST", route, body, nil, ""), 401)
	requireStatus(t, request(t, a, "POST", route, body, memberCookie, memberCSRF), 403)
	requireStatus(t, request(t, a, "POST", route, body, adminCookie, ""), 403)
	requireStatus(t, request(t, a, "GET", route, nil, adminCookie, ""), 405)
	for _, field := range []string{"user_id", "parent_id", "membership_version"} {
		for _, invalid := range []string{"", "-1", "01", "18446744073709551616"} {
			input := map[string]string{"user_id": body["user_id"], "parent_id": body["parent_id"], "membership_version": "1"}
			input[field] = invalid
			requireStatus(t, request(t, a, "POST", route, input, adminCookie, csrf), 400)
		}
	}
	// Roles are checked from the database at mutation time, not the stale list.
	if err := a.store.DB.Model(&child).Update("role", "admin").Error; err != nil {
		t.Fatal(err)
	}
	requireStatus(t, request(t, a, "POST", route, body, adminCookie, csrf), 403)
	if err := a.store.DB.Model(&admin).Update("is_super_admin", true).Error; err != nil {
		t.Fatal(err)
	}
	response := request(t, a, "POST", route, body, adminCookie, csrf)
	requireStatus(t, response, 200)
	user := data(t, response)["user"].(map[string]any)
	if user["parent"] != nil || user["membership_version"] != "2" {
		t.Fatal(user)
	}
	requireStatus(t, request(t, a, "POST", route, body, adminCookie, csrf), 409)
	fresh := request(t, a, "GET", "admin/users?search=child", nil, adminCookie, "")
	requireStatus(t, fresh, 200)
	if data(t, fresh)["items"].([]any)[0].(map[string]any)["parent"] != nil {
		t.Fatal("parent remains in list")
	}
}
