package supplier

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestBackgroundReasonUsesCachedSessionAndOneRead(t *testing.T) {
	c, f, _, _ := newCacheFixture(t)
	ctx := context.Background()
	if err := collectCached(t, c); err != nil {
		t.Fatal(err)
	}
	calls := 0
	c.transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		expected := "/claude-status-reason"
		if calls == 1 {
			expected = "/check-fail-reason"
		}
		if r.Method != "GET" || r.URL.Path != apiPrefix+"/accounts/99"+expected || r.Header.Get("Authorization") != "Bearer private-cache-token-1" {
			t.Fatal("unexpected request", r.Method, r.URL.Path)
		}
		return jsonResponse(200, map[string]any{"found": true, "content": "Login failed; password=private-password Bearer private-cache-token-1"}), nil
	})
	// A valid cached session can still be used during a LOGIN cooldown.
	_ = c.cache.recordCooldown(ctx, "login", cooldownRetryAfter("120", time.Now()))
	for _, status := range []string{"sync_failed", "login_failed", "banned"} {
		reason, err := c.ReadAccountReason(ctx, "private-supplier@example.com", "private-password", "99", status)
		if err != nil || reason == nil || !strings.Contains(*reason, "Login failed") || strings.Contains(*reason, "private-") {
			t.Fatal("unsafe or missing reason", err, reason)
		}
	}
	if calls != 3 || f.logins.Load() != 1 || f.reads.Load() != 1 {
		t.Fatal("background read logged in or searched accounts")
	}
}
func TestBackgroundReasonNeverLogsInOrRetriesExpiredSession(t *testing.T) {
	c, f, _, _ := newCacheFixture(t)
	ctx := context.Background()
	if _, err := c.ReadAccountReason(ctx, "private-supplier@example.com", "private-password", "99", "offline"); !errors.Is(err, ErrReasonSessionUnavailable) {
		t.Fatal(err)
	}
	if f.logins.Load() != 0 || f.keys.Load() != 0 || f.reads.Load() != 0 {
		t.Fatal("missing cache triggered network")
	}
	if err := collectCached(t, c); err != nil {
		t.Fatal(err)
	}
	calls := 0
	c.transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return jsonResponse(401, map[string]any{}), nil
	})
	for i := 0; i < 2; i++ {
		if _, err := c.ReadAccountReason(ctx, "private-supplier@example.com", "private-password", "99", "offline"); !errors.Is(err, ErrReasonSessionUnavailable) {
			t.Fatal(err)
		}
	}
	if calls != 1 || f.logins.Load() != 1 {
		t.Fatal("expired session retried or logged in")
	}
}
func TestBackgroundReasonQueryCooldownRetainsSession(t *testing.T) {
	c, f, m, _ := newCacheFixture(t)
	ctx := context.Background()
	if err := collectCached(t, c); err != nil {
		t.Fatal(err)
	}
	calls := 0
	c.transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			out := jsonResponse(429, map[string]any{})
			out.Header.Set("Retry-After", "120")
			return out, nil
		}
		return jsonResponse(200, map[string]any{"found": false, "content": nil}), nil
	})
	_, err := c.ReadAccountReason(ctx, "private-supplier@example.com", "private-password", "99", "offline")
	assertRetry(t, err, "accounts", false)
	_, err = c.ReadAccountReason(ctx, "private-supplier@example.com", "private-password", "99", "offline")
	assertRetry(t, err, "accounts", true)
	m.FastForward(121 * time.Second)
	reason, err := c.ReadAccountReason(ctx, "private-supplier@example.com", "private-password", "99", "offline")
	if err != nil || reason == nil || *reason != "" || calls != 2 || f.logins.Load() != 1 {
		t.Fatal("cooldown lost session or absent reason contract", err)
	}
}
func TestBackgroundReasonRejectsBadIdentityAndMalformedResult(t *testing.T) {
	c, f, _, _ := newCacheFixture(t)
	ctx := context.Background()
	for _, id := range []string{"", "0", "../99", "99?secret=x", "01"} {
		_, err := c.ReadAccountReason(ctx, "u", "p", id, "offline")
		requireSupplierCode(t, err, CodeContractMismatch)
	}
	if f.logins.Load() != 0 {
		t.Fatal("invalid IDs caused login")
	}
	if err := collectCached(t, c); err != nil {
		t.Fatal(err)
	}
	c.transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, map[string]any{"found": true}), nil
	})
	_, err := c.ReadAccountReason(ctx, "private-supplier@example.com", "private-password", "99", "offline")
	requireSupplierCode(t, err, CodeContractMismatch)
}
