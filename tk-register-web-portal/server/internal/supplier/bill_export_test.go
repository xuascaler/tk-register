package supplier

import (
	"context"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestBillExportReusesLoginAndReadRetry(t *testing.T) {
	f := &supplierFixture{t: t}
	base := f.client()
	original := base.transport
	exports := 0
	base.transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == apiPrefix+"/bill/claude-export" {
			exports++
			if r.Method != "GET" || r.URL.Query().Get("start_ds") != "20261101" || r.URL.Query().Get("end_ds") != "20261111" || r.Header.Get("Authorization") == "" {
				t.Fatal("bad export request")
			}
			if exports == 1 {
				return jsonResponse(401, nil), nil
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("PK\x03\x04test"))}, nil
		}
		return original.RoundTrip(r)
	})
	b, e := base.ExportClaudeBills(context.Background(), "supplier-user", "private-password-中文", "2026-11-01", "2026-11-11")
	if e != nil || len(b) != 8 || exports != 2 || f.logins != 2 {
		t.Fatalf("export failed %v calls%d logins%d", e, exports, f.logins)
	}
}
func TestBillExportRateLimitKeepsSession(t *testing.T) {
	mr := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer r.Close()
	base, e := NewCachedClient(r, strings.Repeat("x", 32))
	if e != nil {
		t.Fatal(e)
	}
	fixture := &supplierFixture{t: t}
	original := fixture.client().transport
	exports := 0
	base.transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == apiPrefix+"/bill/claude-export" {
			exports++
			res := jsonResponse(429, nil)
			res.Header.Set("Retry-After", "120")
			return res, nil
		}
		return original.RoundTrip(req)
	})
	for i := 0; i < 2; i++ {
		_, e = base.ExportClaudeBills(context.Background(), "supplier-user", "private-password-中文", "2026-11-01", "2026-11-11")
		if e == nil {
			t.Fatal("expected cooldown")
		}
	}
	if fixture.logins != 1 || exports != 1 {
		t.Fatalf("cooldown failed logins%d exports%d", fixture.logins, exports)
	}
	for _, k := range mr.Keys() {
		if strings.Contains(k, "cooldown:login") {
			t.Fatal("bill429 poisoned login")
		}
	}
}
func TestBillExportRejectsInvalidDatesWithoutLogin(t *testing.T) {
	f := &supplierFixture{t: t}
	_, e := f.client().ExportClaudeBills(context.Background(), "supplier-user", "private-password-中文", "2026-01-01", "2026-11-11")
	if e == nil || f.logins != 0 {
		t.Fatal("invalid range accepted")
	}
}
