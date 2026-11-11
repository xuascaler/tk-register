package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"
	"time"
)

// All bill rows, users and databases are synthetic. Never contacts the vendor.
func TestBillExportBrowserFixture(t *testing.T) {
	address := os.Getenv("TK_BILL_EXPORT_BROWSER_TEST_ADDRESS")
	if address == "" {
		t.Skip("opt-in bill export browser fixture")
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("loopback only")
	}
	a, _, _, _, rows, _ := setupPageSync(t)
	admin := addUser(t, a, "bills-admin@example.com", "admin")
	raw, err := os.ReadFile("internal/billing/testdata/claude-bills.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	a.billExporter = &fakeBillExporter{raw: raw}
	if err = a.store.DB.Model(&rows[0]).Update("platform_account_id", "15001").Error; err != nil {
		t.Fatal(err)
	}
	if err = a.store.DB.Model(&rows[1]).Update("platform_account_id", "15002").Error; err != nil {
		t.Fatal(err)
	}
	a.origin, a.secure, a.assets = "http://"+address, false, "../dist"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.runBillExports(ctx)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__test/admin" {
			token, _, e := a.newSession(ctx, admin.ID, admin.Role, 28800, 1800)
			if e != nil {
				http.Error(w, "session", 500)
				return
			}
			a.cookie(w, token, 28800)
			http.Redirect(w, r, "/tk-register#/config/bill_exports", http.StatusSeeOther)
			return
		}
		a.Handler().ServeHTTP(w, r)
	})
	server := &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	t.Cleanup(func() { server.Close() })
	fmt.Printf("Bill export browser fixture: http://%s/__test/admin\n", address)
	if err = server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}
