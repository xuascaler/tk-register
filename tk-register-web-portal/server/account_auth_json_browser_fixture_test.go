package main

// Isolated opt-in UI fixture. Fake supplier results, SQLite and miniredis only;
// no production credentials, vendor writes or email delivery are involved.
import (
	"context"
	"encoding/json"
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

type browserAuthEditor struct {
	mu      sync.Mutex
	updated map[string]bool
}

func (b *browserAuthEditor) info(target supplier.Target) supplier.EditInfo {
	return supplier.EditInfo{RecordID: map[string]string{"alice@example.com": "350001", "active@example.com": "350002", "refresh-failed@example.com": "350003", "unconfirmed@example.com": "350004"}[target.Account], Account: target.Account, Plan: "claude/cc-max-20x", Editable: target.Account != "active@example.com" && !b.updated[target.Account], Reason: ""}
}
func (b *browserAuthEditor) CheckAuthJSON(ctx context.Context, u, p string, target supplier.Target) (supplier.EditInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	info := b.info(target)
	if !info.Editable {
		info.Reason = "Auth JSON cannot be updated while this account is active."
	}
	return info, nil
}
func (b *browserAuthEditor) UpdateAuthJSON(ctx context.Context, u, p string, target supplier.Target, raw json.RawMessage) (supplier.EditInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	info := b.info(target)
	if !info.Editable {
		return info, &supplier.Error{Code: supplier.CodeNotEditable}
	}
	if target.Account == "unconfirmed@example.com" {
		return info, &supplier.Error{Code: supplier.CodeUpdateUnconfirmed}
	}
	b.updated[target.Account] = true
	return info, nil
}
func (b *browserAuthEditor) CollectAccount(ctx context.Context, u, p string, target supplier.Target) (supplier.Account, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	info := b.info(target)
	if target.Account == "refresh-failed@example.com" {
		return supplier.Account{}, &supplier.Error{Code: supplier.CodeUnavailable}
	}
	status, currency := "active", "CNY"
	cost := 6800.24
	input, output := int64(2520055), int64(11608461)
	five, seven := 2.0, 40.0
	reset := time.Now().Add(2 * time.Hour).UTC()
	return supplier.Account{ID: info.RecordID, PlatformAccountID: "13152", Account: target.Account, Plan: info.Plan, Status: &status, CostTotal: &cost, Currency: &currency, InputTokensTotal: &input, OutputTokensTotal: &output, Usage5HPercent: &five, Usage7DPercent: &seven, Reset5HAt: &reset}, nil
}
func TestAuthJSONBrowserFixture(t *testing.T) {
	address := os.Getenv("TK_AUTH_JSON_BROWSER_TEST_ADDRESS")
	if address == "" {
		t.Skip("opt-in auth JSON browser fixture")
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
	a.accountAuthEditor = &browserAuthEditor{updated: make(map[string]bool)}
	a.accountCollector = &mockAccountCollector{err: &supplier.Error{Code: supplier.CodeUnavailable}}
	for i, email := range []string{"alice@example.com", "active@example.com", "refresh-failed@example.com", "unconfirmed@example.com"} {
		seedAdminSubmission(t, a, member.ID, vendor, email, "accepted", time.Now().Add(time.Duration(i)*time.Second-time.Hour))
	}
	targets, err := a.store.CollectionTargets(context.Background(), member.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		status := "offline"
		if target.Account == "active@example.com" {
			status = "active"
		}
		currency := "CNY"
		cost := 5600.0
		if err = a.store.RecordAccountSnapshot(context.Background(), target.ID, target.IdentityKey, core.AccountSnapshotValues{AccountStatus: &status, CostTotal: &cost, Currency: &currency}, time.Now().Add(-2*time.Hour), "Asia/Shanghai"); err != nil {
			t.Fatal(err)
		}
	}
	stop := make(chan struct{}, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/__test/admin", "/__test/member":
			user := admin
			if strings.HasSuffix(r.URL.Path, "member") {
				user = member
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
		case "/__test/auth-json":
			ok(w, submissionParameters()["auth_json"])
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
	t.Log(fmt.Sprintf("Auth JSON fixture ready: %s/__test/member", a.origin))
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
