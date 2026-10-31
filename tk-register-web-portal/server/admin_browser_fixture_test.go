package main

// Opt-in browser fixture. All users, email, Redis, database and vendor responses
// are isolated test data; this file is excluded from production builds.
import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"goapi-tk-register/server/internal/core"
)

func TestAdminBrowserFixture(t *testing.T) {
	address := os.Getenv("TK_ADMIN_BROWSER_TEST_ADDRESS")
	if address == "" {
		t.Skip("opt-in admin browser fixture")
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil || (host != "127.0.0.1" && host != "::1") {
		t.Fatal("browser fixture must listen on loopback")
	}
	a, _, _ := fixture(t)
	a.origin, a.secure, a.assets = "http://"+address, false, "../dist"
	ctx := context.Background()
	admin := addUser(t, a, "admin@example.com", "admin")
	member := addUser(t, a, "li.wei@example.com", "member")
	other := addUser(t, a, "maya.chen@example.com", "member")
	addUser(t, a, "sam.tan@example.com", "member")
	addUser(t, a, "alex.wong@example.com", "admin")
	vendors, err := a.store.Vendors(ctx)
	if err != nil || len(vendors) == 0 {
		t.Fatal("missing fixture vendor", err)
	}
	vid, _ := core.ParseBusinessID(vendors[0].ID)
	token := browserVendorToken
	vendor, err := a.store.PatchVendor(ctx, vid, admin.ID, core.VendorPatch{AccessToken: &token})
	if err != nil {
		t.Fatal(err)
	}
	transport := newBrowserVendorFixture(t, a)
	service := &core.VendorService{Store: a.store, Transport: transport}
	for i, account := range []struct {
		owner uint64
		email string
	}{{member.ID, "alice@example.com"}, {member.ID, "robin@example.com"}, {other.ID, "alice@example.com"}, {other.ID, "avery.very.long.account.identifier@example.com"}} {
		input := createInput(vendor, fmt.Sprintf("admin-browser-fixture-%d", i))
		input.Parameters["account"] = account.email
		input.Parameters["auth_json"].(map[string]any)["email"] = account.email
		detail, err := service.Create(ctx, account.owner, input)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := core.ParseBusinessID(detail.Submission.ID)
		if _, err = service.RefreshStatus(ctx, account.owner, id, true); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			old, err := a.store.OwnedSubmission(ctx, account.owner, id)
			if err != nil {
				t.Fatal(err)
			}
			// A later failed submission must not erase the accepted account status.
			old.ID, old.ClientRequestKey, old.ActiveAccountKey = 0, "admin-browser-fixture-failed", nil
			old.Status, old.RemoteID, old.RemoteStatus, old.RemoteAlias = "failed", nil, nil, nil
			old.PlatformAccountID, old.LastCheckedAt, old.MetricsJSON, old.BillsJSON = nil, nil, "", ""
			old.CreatedAt, old.UpdatedAt = time.Now().Add(time.Second), time.Now().Add(time.Second)
			if err := a.store.DB.Create(&old).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	stopped := make(chan struct{}, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if transport.handle(w, r) {
			return
		}
		switch r.URL.Path {
		case "/__test/admin", "/__test/member":
			user := admin
			if r.URL.Path == "/__test/member" {
				user = member
			}
			fresh, err := a.store.GetUserByID(ctx, user.ID)
			if err != nil {
				http.Error(w, "fixture user unavailable", 500)
				return
			}
			value, _, err := a.newSession(ctx, fresh.ID, fresh.Role, 28800, 1800)
			if err != nil {
				http.Error(w, "fixture session failed", 500)
				return
			}
			a.cookie(w, value, 28800)
			http.Redirect(w, r, "/tk-register#/account-pool/accounts", http.StatusSeeOther)
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
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()
	t.Logf("admin browser fixture ready at %s/__test/admin", a.origin)
	select {
	case <-stopped:
	case err := <-serveErrors:
		if err != http.ErrServerClosed {
			t.Error(err)
		}
	case <-time.After(30 * time.Minute):
		t.Error("browser fixture timed out")
	}
	shutdown, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	server.Shutdown(shutdown)
}
