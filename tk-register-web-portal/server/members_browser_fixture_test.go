package main

// Opt-in, loopback-only test server. No production users, mail, or upstream calls.
import (
	"context"
	"goapi-tk-register/server/internal/core"
	"goapi-tk-register/server/internal/supplier"
	"net"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestMembersBrowserFixture(t *testing.T) {
	address := os.Getenv("TK_MEMBERS_BROWSER_TEST_ADDRESS")
	if address == "" {
		t.Skip("opt-in members fixture")
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("loopback required")
	}
	a, _, mail := fixture(t)
	ctx := context.Background()
	a.origin = "http://" + address
	a.secure = false
	a.assets = "../dist"
	root := addUser(t, a, "alex@example.com", "member")
	create := func(parent *core.User, email string) *core.User {
		u := &core.User{Email: email, PasswordHash: "fake", EmailVerifiedAt: time.Now()}
		if err := a.store.CreateMemberByUser(ctx, parent.ID, parent.AuthVersion, u); err != nil {
			t.Fatal(err)
		}
		return u
	}
	b := create(root, "ben@example.com")
	c := create(root, "clara@example.com")
	d := create(b, "hidden-descendant@example.com")
	admin := addUser(t, a, "admin@example.com", "admin")
	v := configuredVendor(t, a, admin.ID)
	configureSync(t, a, admin.ID, false)
	for i, item := range []struct {
		u     *core.User
		email string
	}{{root, "liam.wu@example.com"}, {b, "nora.chen@example.com"}, {c, "emma.liu@example.com"}, {d, "oliver.lin@example.com"}} {
		seedAdminSubmission(t, a, item.u.ID, v, item.email, "accepted", time.Now().Add(-time.Duration(i+1)*time.Hour))
	}
	if err := a.store.EnsureSyncedAccounts(ctx); err != nil {
		t.Fatal(err)
	}
	var rows []core.SyncedAccount
	if err := a.store.DB.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		status, currency, cost, five, seven := "active", "CNY", 1320.0, 38.0, 16.0
		values := core.AccountSnapshotValues{AccountStatus: &status, CostTotal: &cost, Currency: &currency, Usage5hPercent: &five, Usage7dPercent: &seven}
		if err := a.store.RecordAccountSnapshot(ctx, row.ID, row.IdentityKey, values, time.Now(), "Asia/Shanghai"); err != nil {
			t.Fatal(err)
		}
	}
	a.accountPageCollector = &mockPageCollector{run: func(ctx context.Context, targets []supplier.Target) ([]supplier.AccountResult, error) {
		out := []supplier.AccountResult{}
		for _, target := range targets {
			record := pageRecord(target)
			record.Account.ID = target.RecordID
			out = append(out, record)
		}
		return out, nil
	}}
	stopped := make(chan struct{}, 1)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/__test/login", "/__test/admin":
			u := root
			if r.URL.Path == "/__test/admin" {
				u = admin
			}
			value, _, err := a.newSession(ctx, u.ID, u.Role, 28800, 1800)
			if err != nil {
				http.Error(w, "fixture login failed", 500)
				return
			}
			a.cookie(w, value, 28800)
			http.Redirect(w, r, "/tk-register#/accounts", 303)
		case "/__test/code":
			ok(w, map[string]string{"code": mail.code(r.URL.Query().Get("email"), "register")})
		case "/__test/stop":
			ok(w, map[string]bool{"stopped": true})
			select {
			case stopped <- struct{}{}:
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
	server := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	go server.Serve(listener)
	t.Logf("members browser fixture ready at %s", a.origin)
	select {
	case <-stopped:
	case <-time.After(30 * time.Minute):
		t.Error("fixture timed out")
	}
	shutdown, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	server.Shutdown(shutdown)
}
