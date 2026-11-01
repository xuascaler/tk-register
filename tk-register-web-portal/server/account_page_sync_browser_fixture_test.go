package main

// Isolated opt-in fixture: no supplier, SMTP or production database access.
import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"goapi-tk-register/server/internal/core"
	"goapi-tk-register/server/internal/supplier"
)

type browserPageCollector struct {
	mu    sync.Mutex
	calls [][]string
}

func (b *browserPageCollector) CollectTargets(ctx context.Context, u, p string, targets []supplier.Target) ([]supplier.AccountResult, error) {
	b.mu.Lock()
	emails := []string{}
	for _, target := range targets {
		emails = append(emails, target.Account)
	}
	b.calls = append(b.calls, emails)
	b.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(200 * time.Millisecond):
	}
	out := []supplier.AccountResult{}
	for i, target := range targets {
		if target.Account == "sync-failed@example.com" {
			out = append(out, supplier.AccountResult{Target: target, Err: &supplier.Error{Code: supplier.CodeAccountNotFound}})
			continue
		}
		record := pageRecord(target)
		record.Account.ID = fmt.Sprint(450001 + i)
		status := "active"
		record.Account.Status = &status
		cost := 6800.24 + float64(i)
		record.Account.CostTotal = &cost
		out = append(out, record)
	}
	return out, nil
}
func TestPageSyncBrowserFixture(t *testing.T) {
	address := os.Getenv("TK_PAGE_SYNC_BROWSER_TEST_ADDRESS")
	if address == "" {
		t.Skip("opt-in page sync browser fixture")
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil || (host != "127.0.0.1" && host != "::1") {
		t.Fatal("fixture must listen on loopback")
	}
	a, _, _ := fixture(t)
	a.origin, a.secure, a.assets = "http://"+address, false, "../dist"
	admin := addUser(t, a, "admin@example.com", "admin")
	member := addUser(t, a, "member@example.com", "member")
	vendor := configuredVendor(t, a, admin.ID)
	configureSync(t, a, admin.ID, false)
	fake := &browserPageCollector{}
	a.accountPageCollector = fake
	a.accountCollector = &mockAccountCollector{err: &supplier.Error{Code: supplier.CodeUnavailable}}
	a.accountAuthEditor = &browserAuthEditor{updated: make(map[string]bool)}
	for i := 1; i <= 22; i++ {
		email := fmt.Sprintf("account-%02d@example.com", i)
		status := "accepted"
		if i == 21 {
			email = "sync-failed@example.com"
		}
		if i == 22 {
			email = "not-accepted@example.com"
			status = "failed"
		}
		seedAdminSubmission(t, a, member.ID, vendor, email, status, time.Now().Add(time.Duration(i)*time.Second-time.Hour))
	}
	targets, err := a.store.CollectionTargets(context.Background(), member.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		status, currency := "offline", "CNY"
		cost := 5600.0
		if err = a.store.RecordAccountSnapshot(context.Background(), target.ID, target.IdentityKey, core.AccountSnapshotValues{AccountStatus: &status, CostTotal: &cost, Currency: &currency}, time.Now().Add(-2*time.Hour), "Asia/Shanghai"); err != nil {
			t.Fatal(err)
		}
	}
	stop := make(chan struct{}, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/__test/member", "/__test/admin":
			user := member
			if strings.HasSuffix(r.URL.Path, "admin") {
				user = admin
			}
			token, _, err := a.newSession(r.Context(), user.ID, user.Role, 28800, 1800)
			if err != nil {
				http.Error(w, "fixture session failed", 500)
				return
			}
			a.cookie(w, token, 28800)
			dest := "/tk-register#/accounts"
			if user.Role == "admin" {
				dest = "/tk-register#/account-pool/accounts"
			}
			http.Redirect(w, r, dest, http.StatusSeeOther)
		case "/__test/stats":
			fake.mu.Lock()
			defer fake.mu.Unlock()
			ok(w, map[string]any{"calls": fake.calls, "call_count": len(fake.calls)})
		case "/__test/stop":
			ok(w, map[string]bool{"stopped": true})
			select {
			case stop <- struct{}{}:
			default:
			}
		default:
			a.Handler().ServeHTTP(w, r)
		}
	})
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	errs := make(chan error, 1)
	go func() { errs <- server.Serve(listener) }()
	t.Logf("Page sync fixture ready: %s/__test/member", a.origin)
	select {
	case <-stop:
	case err := <-errs:
		if err != http.ErrServerClosed {
			t.Error(err)
		}
	case <-time.After(30 * time.Minute):
		t.Error("fixture timed out")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	server.Shutdown(ctx)
}
