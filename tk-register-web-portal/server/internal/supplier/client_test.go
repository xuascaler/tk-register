package supplier

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"goapi-tk-register/server/common"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

var testKeyOnce sync.Once
var testPrivateKey *rsa.PrivateKey
var testPublicKey string

func keys(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	testKeyOnce.Do(func() {
		var err error
		testPrivateKey, err = rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		der, err := x509.MarshalPKIXPublicKey(&testPrivateKey.PublicKey)
		if err != nil {
			panic(err)
		}
		testPublicKey = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	})
	return testPrivateKey, testPublicKey
}

func jsonResponse(status int, value any) *http.Response {
	b, _ := common.Marshal(value)
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(b)))}
}

func decryptPassword(t *testing.T, value string) string {
	t.Helper()
	key, _ := keys(t)
	b, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]string
	if err := common.Unmarshal(b, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["__enc__"] != "hybrid-aesgcm" {
		t.Fatal("wrong encryption envelope")
	}
	decode := func(field string) []byte {
		b, err := base64.StdEncoding.DecodeString(envelope[field])
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	aesKey, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, key, decode("k"), nil)
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := gcm.Open(nil, decode("iv"), decode("ct"), nil)
	if err != nil {
		t.Fatal(err)
	}
	return string(plaintext)
}

type supplierFixture struct {
	t      *testing.T
	logins int
	keys   int
	reads  int
	read   func(*http.Request, int) *http.Response
}

func (f *supplierFixture) client() *Client {
	return &Client{transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme+"://"+r.URL.Host != Origin {
			f.t.Fatal("request escaped fixed origin")
		}
		switch r.URL.Path {
		case apiPrefix + "/pubkey":
			f.keys++
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "" {
				f.t.Fatal("bad public key request")
			}
			var payload map[string]string
			if json.NewDecoder(r.Body).Decode(&payload) != nil || payload["username"] != "supplier-user" || len(payload) != 1 {
				f.t.Fatal("bad key payload")
			}
			_, pub := keys(f.t)
			return jsonResponse(200, map[string]string{"public_key": pub}), nil
		case apiPrefix + "/login":
			f.logins++
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "" {
				f.t.Fatal("bad login request")
			}
			var payload map[string]string
			if json.NewDecoder(r.Body).Decode(&payload) != nil || payload["username"] != "supplier-user" || len(payload) != 2 {
				f.t.Fatal("bad login payload")
			}
			if decryptPassword(f.t, payload["enc_password"]) != "private-password-中文" {
				f.t.Fatal("encrypted password did not round-trip")
			}
			return jsonResponse(200, map[string]any{"token": fmt.Sprintf("token-%d", f.logins), "expires_in": 18000}), nil
		case apiPrefix + "/accounts":
			f.reads++
			if r.Method != http.MethodGet || r.Header.Get("Authorization") != fmt.Sprintf("Bearer token-%d", f.logins) {
				f.t.Fatal("wrong read method or auth")
			}
			q := r.URL.Query()
			if q.Get("provider") != "claude" || q.Get("account_type") != "2" || q.Get("status") != "all" || q.Get("page_size") != "100" {
				f.t.Fatal("bad account filters")
			}
			return f.read(r, f.reads), nil
		default:
			f.t.Fatalf("unexpected endpoint: %s", r.URL.Path)
			return nil, errors.New("unexpected request")
		}
	})}
}

func fixtureAccount(id int) map[string]any {
	return map[string]any{
		"id": id, "a1_id": "13152", "a1_status": "using",
		"auth_config": `{"account":"account@example.com","plan":"claude/cc-max-20x","password":"ACCOUNT_SECRET","auth_json":{"access_token":"TOKEN_SECRET"}}`,
		"extra":       `{"cpa_usage":{"input_tokens":2520055,"output_tokens":"11608461","cost":6800240000,"utilization":{"five_hour_used_percent":0.02,"week_used_percent":0.4,"five_hour_reset_at":1793131200,"week_reset_at":"1793469600"}}}`,
		"updated_at":  "2026-10-28 04:00:00", "api_key": "UNRELATED_SECRET",
	}
}

func TestCollectWebsiteContractPaginationAndUnits(t *testing.T) {
	f := &supplierFixture{t: t}
	f.read = func(r *http.Request, n int) *http.Response {
		if r.URL.Query().Get("page") != fmt.Sprint(n) {
			t.Fatal("bad pagination")
		}
		return jsonResponse(200, map[string]any{"total": "2", "list": []any{fixtureAccount(n)}, "counts": map[string]int{"using": 2}})
	}
	rows, err := f.client().Collect(context.Background(), "supplier-user", "private-password-中文")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || f.logins != 1 || f.keys != 1 || f.reads != 2 {
		t.Fatalf("bad counts: rows=%d keys=%d login=%d reads=%d", len(rows), f.keys, f.logins, f.reads)
	}
	a := rows[0]
	if a.ID != "1" || a.PlatformAccountID != "13152" || a.Account != "account@example.com" || a.Plan != "claude/cc-max-20x" || a.Status == nil || *a.Status != "active" {
		t.Fatalf("bad account: %+v", a)
	}
	if a.InputTokensTotal == nil || *a.InputTokensTotal != 2520055 || a.OutputTokensTotal == nil || *a.OutputTokensTotal != 11608461 {
		t.Fatal("token counts changed")
	}
	if a.CostTotal == nil || *a.CostTotal != 6800.24 || a.Currency == nil || *a.Currency != "CNY" {
		t.Fatal("incorrect cost units")
	}
	if a.Usage5HPercent == nil || *a.Usage5HPercent != 2 || a.Usage7DPercent == nil || *a.Usage7DPercent != 40 {
		t.Fatal("incorrect percentages")
	}
	if a.Reset5HAt == nil || a.Reset5HAt.Unix() != 1793131200 || a.Reset7DAt == nil || a.Reset7DAt.Unix() != 1793469600 {
		t.Fatal("incorrect reset timestamps")
	}
	if a.SourceUpdatedAt != nil {
		t.Fatal("account metadata time was misrepresented as usage time")
	}
	b, _ := common.Marshal(rows)
	if strings.Contains(string(b), "SECRET") || strings.Contains(string(b), "auth_json") || strings.Contains(string(b), "password") {
		t.Fatal("returned data leaked sensitive fields")
	}
}

func TestEveryCallAuthenticatesFresh(t *testing.T) {
	f := &supplierFixture{t: t, read: func(_ *http.Request, _ int) *http.Response {
		return jsonResponse(200, map[string]any{"list": []any{}, "total": 0})
	}}
	c := f.client()
	for range 2 {
		if _, err := c.Collect(context.Background(), "supplier-user", "private-password-中文"); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.TestLogin(context.Background(), "supplier-user", "private-password-中文"); err != nil {
		t.Fatal(err)
	}
	if f.logins != 3 || f.keys != 3 || f.reads != 2 {
		t.Fatal("session was reused or TestLogin read accounts")
	}
}

func TestExpiredSessionReauthenticatesOnce(t *testing.T) {
	f := &supplierFixture{t: t, read: func(_ *http.Request, n int) *http.Response {
		if n == 1 {
			return jsonResponse(401, map[string]string{"msg": "secret must not escape"})
		}
		return jsonResponse(200, map[string]any{"list": []any{fixtureAccount(1)}, "total": 1})
	}}
	rows, err := f.client().Collect(context.Background(), "supplier-user", "private-password-中文")
	if err != nil || len(rows) != 1 || f.logins != 2 || f.reads != 2 {
		t.Fatalf("reauth failed: rows=%d logins=%d reads=%d err=%v", len(rows), f.logins, f.reads, err)
	}
}

func TestSecondExpiryAnywhereInRunStops(t *testing.T) {
	f := &supplierFixture{t: t, read: func(_ *http.Request, n int) *http.Response {
		if n == 2 {
			return jsonResponse(200, map[string]any{"list": []any{fixtureAccount(1)}, "total": 2})
		}
		return jsonResponse(403, map[string]string{"msg": "UPSTREAM_SECRET"})
	}}
	rows, err := f.client().Collect(context.Background(), "supplier-user", "private-password-中文")
	assertCode(t, err, CodeSessionExpired)
	if rows != nil || f.logins != 2 || f.reads != 3 {
		t.Fatalf("unbounded retry or partial output: rows=%v logins=%d reads=%d", rows, f.logins, f.reads)
	}
}

func TestNullMetricsStayAbsentAndRealZeroIsPreserved(t *testing.T) {
	for _, extra := range []string{"", `{}`, `{"cpa_usage":null}`} {
		row := rawAccount{ID: json.RawMessage("1"), AuthConfig: json.RawMessage(`{"account":"a@example.com","plan":"claude/cc-pro"}`), Extra: json.RawMessage(strconvQuote(extra))}
		a, err := parseAccount(row)
		if err != nil {
			t.Fatal(err)
		}
		if a.InputTokensTotal != nil || a.CostTotal != nil || a.Currency != nil || a.Usage5HPercent != nil {
			t.Fatal("missing usage became zero")
		}
	}
	row := rawAccount{ID: json.RawMessage("1"), AuthConfig: json.RawMessage(`{"account":"a@example.com","plan":"claude/cc-pro"}`), Extra: json.RawMessage(`{"cpa_usage":{"input_tokens":0,"cost":0,"utilization":{"five_hour_used_percent":0,"week_used_percent":0,"five_hour_reset_at":1793131200}}}`)}
	a, err := parseAccount(row)
	if err != nil {
		t.Fatal(err)
	}
	if a.InputTokensTotal == nil || *a.InputTokensTotal != 0 || a.CostTotal == nil || *a.CostTotal != 0 || a.Usage5HPercent == nil || *a.Usage5HPercent != 0 {
		t.Fatal("real zero was dropped")
	}
	row.Extra = json.RawMessage(`{"cpa_usage":{"utilization":{"five_hour_used_percent":0,"week_used_percent":0,"five_hour_reset_at":0,"week_reset_at":0}}}`)
	a, err = parseAccount(row)
	if err != nil || a.Usage5HPercent != nil || a.Usage7DPercent != nil {
		t.Fatal("all-zero unavailable windows were displayed as real usage")
	}
}

func strconvQuote(v string) string { b, _ := common.Marshal(v); return string(b) }

func TestContractAndPaginationErrorsAreSafe(t *testing.T) {
	cases := []struct {
		name  string
		value any
	}{
		{"missing-list", map[string]any{"total": 1}},
		{"missing-total", map[string]any{"list": []any{}}},
		{"null-list", map[string]any{"list": nil, "total": 0}},
		{"too-many-pages", map[string]any{"list": []any{}, "total": 10001}},
		{"premature-empty", map[string]any{"list": []any{}, "total": 1}},
		{"bad-account", map[string]any{"list": []any{map[string]any{"id": 1, "auth_config": "PASSWORD_SECRET"}}, "total": 1}},
		{"bad-metric", map[string]any{"list": []any{map[string]any{"id": 1, "auth_config": `{"account":"a@example.com","plan":"claude/cc-pro"}`, "extra": `{"cpa_usage":{"cost":"NaN"}}`}}, "total": 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &supplierFixture{t: t, read: func(_ *http.Request, _ int) *http.Response { return jsonResponse(200, tc.value) }}
			_, err := f.client().Collect(context.Background(), "supplier-user", "private-password-中文")
			assertCode(t, err, CodeContractMismatch)
		})
	}
	f := &supplierFixture{t: t, read: func(_ *http.Request, _ int) *http.Response {
		return jsonResponse(200, map[string]any{"list": []any{fixtureAccount(1)}, "total": 2})
	}}
	_, err := f.client().Collect(context.Background(), "supplier-user", "private-password-中文")
	assertCode(t, err, CodeContractMismatch)
	if f.reads != 2 {
		t.Fatal("duplicate page was not rejected")
	}
}

type unreadableBody struct{}

func (unreadableBody) Read([]byte) (int, error) { panic("error body must never be read") }
func (unreadableBody) Close() error             { return nil }

func TestCredentialsAndTransportErrorsNeverExposeDetails(t *testing.T) {
	c := &Client{transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Header: make(http.Header), Body: unreadableBody{}}, nil
	})}
	assertCode(t, c.TestLogin(context.Background(), "user", "PASSWORD_SECRET"), CodeCredentialsInvalid)
	c.transport = roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return nil, errors.New("PASSWORD_SECRET in URL or TLS error")
	})
	assertCode(t, c.TestLogin(context.Background(), "user", "PASSWORD_SECRET"), CodeUnavailable)
}

func TestRedirectsNeverCarryCredentialsToAnotherHost(t *testing.T) {
	requests := 0
	c := &Client{transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.URL.Host != "120.233.254.152:8000" {
			t.Fatal("credentials sent to unexpected host")
		}
		return &http.Response{StatusCode: 307, Header: http.Header{"Location": {"https://attacker.invalid/login"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})}
	assertCode(t, c.TestLogin(context.Background(), "user", "password"), CodeUnavailable)
	if requests != 1 {
		t.Fatal("redirect was followed")
	}
}

func TestOversizedResponseIsRejected(t *testing.T) {
	c := &Client{transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(strings.Repeat("x", maxResponseBytes+1)))}, nil
	})}
	assertCode(t, c.TestLogin(context.Background(), "user", "password"), CodeContractMismatch)
}

func TestPasswordEncryptionIsRandomized(t *testing.T) {
	_, pub := keys(t)
	a, err := encryptPassword(pub, "private-password-中文")
	if err != nil {
		t.Fatal(err)
	}
	b, err := encryptPassword(pub, "private-password-中文")
	if err != nil {
		t.Fatal(err)
	}
	if a == b || decryptPassword(t, a) != "private-password-中文" || decryptPassword(t, b) != "private-password-中文" {
		t.Fatal("password encryption is not randomized or compatible")
	}
	if _, err := encryptPassword("not-a-key", "PRIVATE_SECRET"); err == nil || strings.Contains(err.Error(), "PRIVATE_SECRET") {
		t.Fatal("bad public key did not fail safely")
	}
}

func assertCode(t *testing.T, err error, code string) {
	t.Helper()
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
	if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "token-") || strings.Contains(err.Error(), "private-password") {
		t.Fatal("unsafe error")
	}
}

func TestSupplierStatusMatchesCanonicalFilters(t *testing.T) {
	cases := map[string]string{
		"using": "active", "ban": "banned", "offline": "offline",
		"wait_check": "pending", "init": "pending", "bound": "pending",
		"cooking": "pending", "ready": "pending", "re_login": "pending",
		"pending_activation": "pending", "login_failed": "unknown",
		"re_login_failed": "unknown", "cooking_failed": "unknown",
		"bound_failed": "unknown", "sync_failed": "sync_failed", "new_future_status": "unknown",
	}
	for input, want := range cases {
		a, err := parseAccount(rawAccount{
			ID: json.RawMessage("1"), Status: " " + input + " ",
			AuthConfig: json.RawMessage(`{"account":"a@example.com","plan":"claude/cc-pro"}`),
		})
		if err != nil || a.Status == nil || *a.Status != want {
			t.Fatalf("status %q: got %v, err %v; want %q", input, a.Status, err, want)
		}
	}
	a, err := parseAccount(rawAccount{ID: json.RawMessage("1"), AuthConfig: json.RawMessage(`{"account":"a@example.com","plan":"claude/cc-pro"}`)})
	if err != nil || a.Status != nil {
		t.Fatal("absent status was invented")
	}
}
