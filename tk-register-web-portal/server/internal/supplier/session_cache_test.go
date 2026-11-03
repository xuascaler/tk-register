package supplier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
)

const cacheTestKey = "supplier-cache-encryption-test-key-with-32-characters"

type cacheFixture struct {
	t                                   *testing.T
	publicKey                           string
	logins, keys, reads, puts           atomic.Int32
	loginStatus, queryStatus, putStatus atomic.Int32
	rejectFirst                         atomic.Bool
	lifetime                            any
	retry                               string
}

func newCacheFixture(t *testing.T) (*Client, *cacheFixture, *miniredis.Miniredis, *redis.Client) {
	t.Helper()
	m := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: m.Addr()})
	t.Cleanup(func() { r.Close() })
	c, err := NewCachedClient(r, cacheTestKey)
	if err != nil {
		t.Fatal(err)
	}
	_, key := keys(t)
	f := &cacheFixture{t: t, publicKey: key, lifetime: 18000, retry: "120"}
	f.loginStatus.Store(200)
	f.queryStatus.Store(200)
	f.putStatus.Store(200)
	c.transport = roundTripFunc(f.roundTrip)
	return c, f, m, r
}
func (f *cacheFixture) roundTrip(req *http.Request) (*http.Response, error) {
	var response *http.Response
	switch {
	case req.URL.Path == apiPrefix+"/pubkey":
		f.keys.Add(1)
		return jsonResponse(200, map[string]string{"public_key": f.publicKey}), nil
	case req.URL.Path == apiPrefix+"/login":
		n := f.logins.Add(1)
		status := int(f.loginStatus.Load())
		response = jsonResponse(status, map[string]any{"token": fmt.Sprintf("private-cache-token-%d", n), "expires_in": f.lifetime})
	case req.Method == http.MethodGet && req.URL.Path == apiPrefix+"/accounts":
		f.reads.Add(1)
		status := int(f.queryStatus.Load())
		if f.rejectFirst.Load() && req.Header.Get("Authorization") == "Bearer private-cache-token-1" {
			status = 401
		}
		response = jsonResponse(status, map[string]any{"total": 1, "list": []any{editableFixture(99, "account@example.com")}})
	case req.Method == http.MethodPut:
		f.puts.Add(1)
		response = jsonResponse(int(f.putStatus.Load()), map[string]any{"success": true})
	default:
		return nil, errors.New("unexpected fixture path")
	}
	if response.StatusCode == 429 {
		response.Header.Set("Retry-After", f.retry)
	}
	return response, nil
}
func collectCached(t *testing.T, c *Client) error {
	t.Helper()
	_, err := c.CollectAccount(context.Background(), "private-supplier@example.com", "private-password", Target{Account: "account@example.com"})
	return err
}
func assertRetry(t *testing.T, err error, stage string, deferred bool) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Stage != stage || e.Deferred != deferred || e.RetryAfterSeconds < 1 || e.RetryAt.IsZero() {
		t.Fatalf("incorrect retry metadata: %#v", err)
	}
}
func TestCachedSessionReusedAcrossClientsAndEncrypted(t *testing.T) {
	c, f, m, r := newCacheFixture(t)
	if err := collectCached(t, c); err != nil {
		t.Fatal(err)
	}
	other, err := NewCachedClient(r, cacheTestKey)
	if err != nil {
		t.Fatal(err)
	}
	other.transport = c.transport
	if err := collectCached(t, other); err != nil {
		t.Fatal(err)
	}
	if f.logins.Load() != 1 || f.keys.Load() != 1 || f.reads.Load() != 2 {
		t.Fatal("session not shared across clients")
	}
	found := false
	for _, key := range m.Keys() {
		if !strings.HasPrefix(key, sessionPrefix) {
			t.Fatal("incorrect key namespace")
		}
		raw, _ := m.Get(key)
		for _, secret := range []string{"private-cache-token", "private-supplier@example.com", "private-password", f.publicKey} {
			if strings.Contains(key+raw, secret) {
				t.Fatal("plaintext credentials/session in Redis")
			}
		}
		if strings.Contains(key, ":session:") {
			found = true
			entry, err := c.cache.open(key, raw)
			if err != nil || entry.PublicKey != f.publicKey || entry.Token == "" {
				t.Fatal("token/key pair not preserved")
			}
			if m.TTL(key) > 5*time.Hour || m.TTL(key) < 4*time.Hour {
				t.Fatal("expires_in not honored")
			}
		}
	}
	if !found {
		t.Fatal("no cached session")
	}
	m.FastForward(5 * time.Hour)
	if err := collectCached(t, other); err != nil {
		t.Fatal(err)
	}
	if f.logins.Load() != 2 {
		t.Fatal("expired Redis entry reused")
	}
}
func TestCachedSessionConcurrentClientsLoginOnce(t *testing.T) {
	c, f, _, r := newCacheFixture(t)
	start := make(chan struct{})
	errs := make(chan error, 12)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			other, err := NewCachedClient(r, cacheTestKey)
			if err != nil {
				errs <- err
				return
			}
			other.transport = c.transport
			<-start
			_, err = other.CollectAccount(context.Background(), "private-supplier@example.com", "private-password", Target{Account: "account@example.com"})
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if f.logins.Load() != 1 || f.reads.Load() != 12 {
		t.Fatalf("login storm: %d logins, %d reads", f.logins.Load(), f.reads.Load())
	}
}
func TestCachedSessionLoginCooldownAllowsExistingTokenAndForcedTest(t *testing.T) {
	c, f, _, _ := newCacheFixture(t)
	if err := collectCached(t, c); err != nil {
		t.Fatal(err)
	}
	f.loginStatus.Store(429)
	err := c.TestLogin(context.Background(), "private-supplier@example.com", "wrong-input-password")
	assertRetry(t, err, "login", false)
	if f.logins.Load() != 2 {
		t.Fatal("test-login incorrectly reused cache")
	}
	if err := collectCached(t, c); err != nil {
		t.Fatal("login cooldown blocked cached query", err)
	}
	_, err = c.CollectAccount(context.Background(), "another-user", "another-password", Target{Account: "account@example.com"})
	assertRetry(t, err, "login", true)
	if f.logins.Load() != 2 || f.reads.Load() != 2 {
		t.Fatal("local login cooldown made supplier requests")
	}
}
func TestCachedSessionQueryCooldownPreservesTokenAndPreventsLogin(t *testing.T) {
	c, f, m, _ := newCacheFixture(t)
	f.queryStatus.Store(429)
	err := collectCached(t, c)
	assertRetry(t, err, "accounts", false)
	if f.logins.Load() != 1 || f.reads.Load() != 1 {
		t.Fatal("query429 caused extra login")
	}
	err = collectCached(t, c)
	assertRetry(t, err, "accounts", true)
	_, err = c.CollectAccount(context.Background(), "new-user", "new-password", Target{Account: "account@example.com"})
	assertRetry(t, err, "accounts", true)
	if f.logins.Load() != 1 || f.reads.Load() != 1 {
		t.Fatal("query gate did not run before login")
	}
	f.queryStatus.Store(200)
	m.FastForward(121 * time.Second)
	if err = collectCached(t, c); err != nil {
		t.Fatal(err)
	}
	if f.logins.Load() != 1 || f.reads.Load() != 2 {
		t.Fatal("query429 invalidated cached token")
	}
}
func TestCachedSessionExpiryRefreshAndLateInvalidation(t *testing.T) {
	c, f, _, _ := newCacheFixture(t)
	ctx := context.Background()
	old := c.newSession()
	if err := old.authenticate(ctx, "private-supplier@example.com", "private-password", false); err != nil {
		t.Fatal(err)
	}
	f.rejectFirst.Store(true)
	if err := collectCached(t, c); err != nil {
		t.Fatal(err)
	}
	if f.logins.Load() != 2 || f.reads.Load() != 2 {
		t.Fatal("expired read not retried exactly once")
	}
	if err := old.invalidateCurrent(ctx); err != nil {
		t.Fatal(err)
	}
	if err := collectCached(t, c); err != nil {
		t.Fatal(err)
	}
	if f.logins.Load() != 2 {
		t.Fatal("late old-session invalidation deleted replacement")
	}
	f.queryStatus.Store(401)
	err := collectCached(t, c)
	requireSupplierCode(t, err, CodeSessionExpired)
	if f.logins.Load() != 3 {
		t.Fatal("expired read caused more than one login")
	}
	key, _ := c.cache.credentialKey(ctx, "private-supplier@example.com", "private-password")
	_, raw, e := c.cache.read(ctx, key)
	if e != nil || raw != "" {
		t.Fatal("second expired session remained cached")
	}
}
func TestCachedSessionCredentialRevisionAndTestLoginDoNotReviveOldSession(t *testing.T) {
	c, f, _, _ := newCacheFixture(t)
	ctx := context.Background()
	if err := collectCached(t, c); err != nil {
		t.Fatal(err)
	}
	keyA, _ := c.cache.credentialKey(ctx, "private-supplier@example.com", "private-password")
	if err := c.TestLogin(ctx, "private-supplier@example.com", "private-password"); err != nil {
		t.Fatal(err)
	}
	if err := collectCached(t, c); err != nil {
		t.Fatal(err)
	}
	if f.logins.Load() != 2 {
		t.Fatal("forced test or preserved cache incorrect")
	}
	if err := c.InvalidateSessions(ctx); err != nil {
		t.Fatal(err)
	}
	keyB, _ := c.cache.credentialKey(ctx, "private-supplier@example.com", "private-password")
	if keyA == keyB {
		t.Fatal("revision did not change namespace")
	}
	if err := collectCached(t, c); err != nil {
		t.Fatal(err)
	}
	if f.logins.Load() != 3 {
		t.Fatal("credential revision revived stale session")
	}
	_, err := c.CollectAccount(ctx, "changed-user", "changed-password", Target{Account: "account@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if f.logins.Load() != 4 {
		t.Fatal("different credentials shared same session")
	}
}
func TestCachedSessionAuthJSONPutNotReplayedAndPublicKeyReused(t *testing.T) {
	c, f, _, _ := newCacheFixture(t)
	ctx := context.Background()
	if err := collectCached(t, c); err != nil {
		t.Fatal(err)
	}
	f.putStatus.Store(401)
	_, err := c.UpdateAuthJSON(ctx, "private-supplier@example.com", "private-password", Target{Account: "account@example.com"}, json.RawMessage(`{"access_token":"new-private-auth"}`))
	requireSupplierCode(t, err, CodeUpdateRejected)
	if f.logins.Load() != 1 || f.puts.Load() != 1 {
		t.Fatal("write was replayed or cached public key not reused")
	}
	f.putStatus.Store(200)
	if err = collectCached(t, c); err != nil {
		t.Fatal(err)
	}
	if f.logins.Load() != 2 {
		t.Fatal("rejected expired write did not invalidate cache for next operation")
	}
}
func TestCachedSessionRedisFailureDoesNotBypassLogin(t *testing.T) {
	c, f, m, _ := newCacheFixture(t)
	m.Close()
	err := collectCached(t, c)
	requireSupplierCode(t, err, CodeUnavailable)
	if f.keys.Load() != 0 || f.logins.Load() != 0 || f.reads.Load() != 0 {
		t.Fatal("Redis failure bypassed distributed protection")
	}
}
func TestCachedSessionLockWaitAndCooldownParsing(t *testing.T) {
	c, f, m, r := newCacheFixture(t)
	ctx := context.Background()
	if err := r.Set(ctx, sessionPrefix+"login-lock", "another-worker", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	// A cached session still works even while a forced test owns the login lease.
	key, _ := c.cache.credentialKey(ctx, "private-supplier@example.com", "private-password")
	entry := cachedSession{Token: "private-cache-token", PublicKey: f.publicKey, ExpiresAt: time.Now().Add(time.Minute)}
	raw, _ := c.cache.seal(key, entry)
	r.Set(ctx, key, raw, time.Minute)
	if err := collectCached(t, c); err != nil {
		t.Fatal(err)
	}
	r.Del(ctx, key)
	err := collectCached(t, c)
	assertRetry(t, err, "login_lock", true)
	if f.logins.Load() != 0 {
		t.Fatal("login lease bypassed")
	}
	m.Del(sessionPrefix + "login-lock")
	f.loginStatus.Store(429)
	f.retry = "invalid private header"
	err = collectCached(t, c)
	assertRetry(t, err, "login", false)
	secs, _, _, _ := RetryDetails(err)
	if secs < 59 || secs > 60 {
		t.Fatal("missing Retry-After fallback incorrect")
	}
}
func TestCachedSessionDiagnosticsContainNoSecrets(t *testing.T) {
	t.Setenv("TK_REGISTER_SUPPLIER_DIAGNOSTICS", "all")
	c, _, _, _ := newCacheFixture(t)
	var out bytes.Buffer
	ctx := diagnosticContext(&out)
	for i := 0; i < 2; i++ {
		_, err := c.CollectAccount(ctx, "private-supplier@example.com", "private-password", Target{Account: "account@example.com"})
		if err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(out.String(), `"event":"cache_hit"`) {
		t.Fatal("cache hit missing")
	}
	for _, secret := range []string{"private-cache-token", "private-supplier@example.com", "private-password", "account@example.com", "BEGIN PUBLIC KEY"} {
		if strings.Contains(out.String(), secret) {
			t.Fatal("secret leaked in diagnostics")
		}
	}
}
func TestCachedSessionLifetimeBounds(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{{`18000`, 5*time.Hour - time.Minute}, {`null`, 29 * time.Minute}, {`-1`, 29 * time.Minute}, {`"bad"`, 29 * time.Minute}, {`99999999999`, 29 * time.Minute}, {`1`, 900 * time.Millisecond}, {`604800`, 24*time.Hour - time.Minute}} {
		if got := sessionExpiry(json.RawMessage(tc.raw), now).Sub(now); got != tc.want {
			t.Fatalf("expiry %s: %v", tc.raw, got)
		}
	}
}

func TestCachedSessionSharedAcrossEverySupplierEntryPoint(t *testing.T) {
	c, f, _, _ := newCacheFixture(t)
	ctx := context.Background()
	username, password := "private-supplier@example.com", "private-password"
	target := Target{Account: "account@example.com"}
	if _, err := c.Collect(ctx, username, password); err != nil {
		t.Fatal(err)
	}
	results, err := c.CollectTargets(ctx, username, password, []Target{target})
	if err != nil || len(results) != 1 || results[0].Err != nil {
		t.Fatal("target collection failed", err)
	}
	if _, err = c.CheckAuthJSON(ctx, username, password, target); err != nil {
		t.Fatal(err)
	}
	if _, err = c.UpdateAuthJSON(ctx, username, password, target, json.RawMessage(`{"access_token":"replacement-secret"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = c.CollectAccount(ctx, username, password, target); err != nil {
		t.Fatal(err)
	}
	if f.logins.Load() != 1 || f.keys.Load() != 1 || f.reads.Load() != 5 || f.puts.Load() != 1 {
		t.Fatalf("entrypoints did not share session: logins=%d keys=%d reads=%d puts=%d", f.logins.Load(), f.keys.Load(), f.reads.Load(), f.puts.Load())
	}
}
func TestCachedSessionCanceled429StillSharesCooldown(t *testing.T) {
	c, f, _, _ := newCacheFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.queryStatus.Store(429)
	original := c.transport
	c.transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		response, err := original.RoundTrip(req)
		if req.Method == http.MethodGet {
			cancel()
		}
		return response, err
	})
	_, err := c.CollectAccount(ctx, "private-supplier@example.com", "private-password", Target{Account: "account@example.com"})
	assertRetry(t, err, "accounts", false)
	c.transport = original
	err = collectCached(t, c)
	assertRetry(t, err, "accounts", true)
	if f.logins.Load() != 1 || f.reads.Load() != 1 {
		t.Fatal("canceled response lost shared cooldown")
	}
}
func TestCachedSessionBatchRevisionRefreshInvalidatesCorrectEntry(t *testing.T) {
	c, f, _, _ := newCacheFixture(t)
	ctx := context.Background()
	original := c.transport
	var reads atomic.Int32
	c.transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodGet {
			if reads.Add(1) == 1 {
				if err := c.InvalidateSessions(ctx); err != nil {
					return nil, err
				}
			}
			return jsonResponse(401, map[string]any{}), nil
		}
		return original.RoundTrip(req)
	})
	results, err := c.CollectTargets(ctx, "private-supplier@example.com", "private-password", []Target{{Account: "account@example.com"}})
	if err != nil || len(results) != 1 {
		t.Fatal("unexpected batch error", err)
	}
	requireSupplierCode(t, results[0].Err, CodeSessionExpired)
	if f.logins.Load() != 2 || reads.Load() != 2 {
		t.Fatal("batch expiry not bounded")
	}
	key, _ := c.cache.credentialKey(ctx, "private-supplier@example.com", "private-password")
	_, raw, e := c.cache.read(ctx, key)
	if e != nil || raw != "" {
		t.Fatal("new revision's failed replacement remained cached")
	}
}
func TestCachedSessionRetryAfterClamp(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		value string
		want  int64
	}{{"604801", 604800}, {"9999999999999999999999999", 604800}, {"0", 0}, {"12", 12}, {now.Add(8 * 24 * time.Hour).UTC().Format(http.TimeFormat), 604800}} {
		got := cooldownRetryAfter(tc.value, now)
		if got == nil || *got != tc.want {
			t.Fatalf("wrong clamp for %q", tc.value)
		}
	}
	if cooldownRetryAfter("private arbitrary header", now) != nil {
		t.Fatal("invalid header accepted")
	}
}
