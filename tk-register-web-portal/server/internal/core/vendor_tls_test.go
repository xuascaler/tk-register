package core

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// The root is trusted so failures exercise expiry and hostname validation,
// rather than merely rejecting an unknown certificate authority.
func vendorTLSFixture(t *testing.T, invalidity string, handler http.Handler) (*httptest.Server, *x509.CertPool) {
	t.Helper()
	now := time.Now()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Vendor TLS test CA"},
		NotBefore: now.Add(-48 * time.Hour), NotAfter: now.Add(48 * time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, root, root, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	rootCert, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(2), NotBefore: now.Add(-24 * time.Hour), NotAfter: now.Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("120.233.254.152")},
	}
	switch invalidity {
	case "expired":
		leaf.NotAfter = now.Add(-time.Hour)
	case "hostname":
		leaf.IPAddresses = nil
		leaf.DNSNames = []string{"different-vendor.example"}
	default:
		t.Fatal("unsupported certificate fixture")
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, rootCert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER, rootDER}, PrivateKey: key}}}
	server.StartTLS()
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(rootCert)
	return server, roots
}

func localVendorTLSClient(t *testing.T, vendor Vendor, protocol string, server *httptest.Server, roots *x509.CertPool) *http.Client {
	t.Helper()
	service := &VendorService{}
	client := service.client(vendor, protocol)
	transport := client.Transport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig.RootCAs = roots
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	client.Transport = transport
	t.Cleanup(transport.CloseIdleConnections)
	return client
}

func TestVendorTLSExceptionIsLimitedToApprovedProtocols(t *testing.T) {
	for _, invalidity := range []string{"expired", "hostname"} {
		t.Run(invalidity, func(t *testing.T) {
			var received atomic.Int32
			server, roots := vendorTLSFixture(t, invalidity, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				received.Add(1)
				w.WriteHeader(http.StatusNoContent)
			}))
			cases := []struct {
				name, vendorCode, origin, protocol string
				allow                              bool
			}{
				{"approved submission", "tk-register-0", VendorOrigin, "submit_account", true},
				{"approved status query", "tk-register-0", VendorOrigin, "metrics", true},
				{"approved billing query", "tk-register-0", VendorOrigin, "bills", true},
				{"unknown operation", "tk-register-0", VendorOrigin, "other", false},
				{"different vendor", "another-vendor", VendorOrigin, "submit_account", false},
				{"different origin", "tk-register-0", "https://other.example", "submit_account", false},
				{"status different vendor", "another-vendor", VendorOrigin, "metrics", false},
				{"status different origin", "tk-register-0", "https://other.example", "metrics", false},
				{"billing different vendor", "another-vendor", VendorOrigin, "bills", false},
				{"billing different origin", "tk-register-0", "https://other.example", "bills", false},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					client := localVendorTLSClient(t, Vendor{Code: tc.vendorCode, BaseURL: tc.origin}, tc.protocol, server, roots)
					// All sockets connect locally; keeping the production URL exercises
					// the hostname check without reaching the actual vendor.
					response, err := client.Get(VendorOrigin + "/tls-test")
					if response != nil {
						defer response.Body.Close()
					}
					if tc.allow {
						if err != nil || response.StatusCode != http.StatusNoContent {
							t.Fatalf("approved vendor request failed: response=%v err=%v", response, err)
						}
						return
					}
					if err == nil {
						t.Fatal("certificate exception leaked outside approved vendor protocols")
					}
					if invalidity == "expired" {
						var certErr x509.CertificateInvalidError
						if !errors.As(err, &certErr) || certErr.Reason != x509.Expired {
							t.Fatalf("expected expired certificate error, got %v", err)
						}
					} else {
						var hostErr x509.HostnameError
						if !errors.As(err, &hostErr) {
							t.Fatalf("expected hostname error, got %v", err)
						}
					}
				})
			}
			if got := received.Load(); got != 3 {
				t.Fatalf("only approved submission, status, and billing queries should reach HTTP handler, got %d requests", got)
			}
		})
	}
}

func TestVendorTLSExceptionDoesNotFollowRedirects(t *testing.T) {
	for _, protocol := range []string{"submit_account", "metrics", "bills"} {
		t.Run(protocol, func(t *testing.T) {
			var received atomic.Int32
			server, roots := vendorTLSFixture(t, "expired", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received.Add(1)
				http.Redirect(w, r, "https://other.example/credential-target", http.StatusTemporaryRedirect)
			}))
			client := localVendorTLSClient(t, Vendor{Code: "tk-register-0", BaseURL: VendorOrigin}, protocol, server, roots)
			request, err := http.NewRequest(http.MethodPost, VendorOrigin+"/submit-test", nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", "Bearer dummy-test-token")
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusTemporaryRedirect || received.Load() != 1 {
				t.Fatalf("redirect was followed: status=%d requests=%d", response.StatusCode, received.Load())
			}
		})
	}
}

func TestVendorTLSExceptionPreservesInjectedTransport(t *testing.T) {
	var called bool
	transport := storageTransport(func(_ *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
	})
	client := (&VendorService{Transport: transport}).client(Vendor{Code: "tk-register-0", BaseURL: VendorOrigin}, "submit_account")
	response, err := client.Get(VendorOrigin + "/injected-test")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if !called {
		t.Fatal("explicit transport was replaced by production transport")
	}
}
