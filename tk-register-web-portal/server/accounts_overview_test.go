package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
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
