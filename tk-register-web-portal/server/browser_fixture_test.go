package main

// This opt-in fixture exists only in the test binary. It uses isolated SQLite,
// miniredis and fake SMTP, so browser acceptance tests never contact real users.
import (
	"context"
	"net"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestBrowserFixture(t *testing.T) {
	address := os.Getenv("TK_BROWSER_TEST_ADDRESS")
	if address == "" {
		t.Skip("opt-in browser fixture")
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil || (host != "127.0.0.1" && host != "::1") {
		t.Fatal("browser fixture must listen on loopback")
	}
	a, mini, mail := fixture(t)
	a.origin = "http://" + address
	a.secure = false
	a.assets = "../dist"
	vendor := newBrowserVendorFixture(t, a)
	stopped := make(chan struct{}, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if vendor.handle(w, r) {
			return
		}
		switch r.URL.Path {
		case "/__test/code":
			ok(w, map[string]string{"code": mail.code(r.URL.Query().Get("email"), r.URL.Query().Get("purpose"))})
		case "/__test/advance":
			seconds, err := strconv.Atoi(r.URL.Query().Get("seconds"))
			if err != nil || seconds < 0 || seconds > 86400 {
				w.WriteHeader(400)
				return
			}
			mini.FastForward(time.Duration(seconds) * time.Second)
			ok(w, map[string]bool{"advanced": true})
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
	errors := make(chan error, 1)
	go func() { errors <- server.Serve(listener) }()
	t.Logf("browser fixture ready at %s/tk-register", a.origin)
	select {
	case <-stopped:
	case err := <-errors:
		if err != http.ErrServerClosed {
			t.Error(err)
		}
	case <-time.After(10 * time.Minute):
		t.Error("browser fixture timed out")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	server.Shutdown(ctx)
}
