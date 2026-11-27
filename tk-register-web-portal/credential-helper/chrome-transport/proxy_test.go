package main

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func tunnel(client, upstream net.Conn) {
	defer client.Close()
	defer upstream.Close()
	done := make(chan struct{})
	go func() { _, _ = io.Copy(client, upstream); _ = client.Close(); close(done) }()
	_, _ = io.Copy(upstream, client)
	_ = upstream.Close()
	<-done
}

// Only test code redirects the dialer's validated public proxy IP to loopback.
func localProxy(t *testing.T, scheme, upstream string) (string, string, <-chan string) {
	t.Helper()
	observed := make(chan string, 8)
	if scheme == "http" || scheme == "https" {
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "CONNECT" || r.Host != "example.com:443" || r.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("demo:password")) {
				http.Error(w, "bad tunnel", 407)
				return
			}
			observed <- r.Host
			target, err := net.Dial("tcp", upstream)
			if err != nil {
				http.Error(w, "bad tunnel", 502)
				return
			}
			conn, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				target.Close()
				return
			}
			_, _ = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
			_ = rw.Flush()
			tunnel(conn, target)
		})
		server := httptest.NewUnstartedServer(h)
		if scheme == "https" {
			server.StartTLS()
		} else {
			server.Start()
		}
		t.Cleanup(server.Close)
		_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
		return scheme + "://demo:password@example.com:" + port, server.Listener.Addr().String(), observed
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	connections := map[net.Conn]bool{}
	t.Cleanup(func() {
		listener.Close()
		mu.Lock()
		defer mu.Unlock()
		for c := range connections {
			c.Close()
		}
	})
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			connections[conn] = true
			mu.Unlock()
			go func() {
				defer func() { conn.Close(); mu.Lock(); delete(connections, conn); mu.Unlock() }()
				r := bufio.NewReader(conn)
				header := make([]byte, 2)
				if _, err := io.ReadFull(r, header); err != nil || header[0] != 5 {
					return
				}
				if _, err := io.CopyN(io.Discard, r, int64(header[1])); err != nil {
					return
				}
				_, _ = conn.Write([]byte{5, 2})
				if _, err := io.ReadFull(r, header); err != nil || header[0] != 1 {
					return
				}
				username := make([]byte, int(header[1]))
				if _, err := io.ReadFull(r, username); err != nil {
					return
				}
				length, err := r.ReadByte()
				if err != nil {
					return
				}
				password := make([]byte, int(length))
				if _, err := io.ReadFull(r, password); err != nil {
					return
				}
				if string(username) != "demo" || string(password) != "password" {
					_, _ = conn.Write([]byte{1, 1})
					return
				}
				_, _ = conn.Write([]byte{1, 0})
				command := make([]byte, 5)
				if _, err := io.ReadFull(r, command); err != nil || command[3] != 3 {
					return
				}
				host := make([]byte, int(command[4])+2)
				if _, err := io.ReadFull(r, host); err != nil {
					return
				}
				observed <- fmt.Sprintf("%s:%d", host[:len(host)-2], binary.BigEndian.Uint16(host[len(host)-2:]))
				target, err := net.Dial("tcp", upstream)
				if err != nil {
					return
				}
				_, _ = conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
				tunnel(conn, target)
			}()
		}
	}()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	return scheme + "://demo:password@example.com:" + port, listener.Addr().String(), observed
}

func TestChromeTLSAndHeadersThroughEachProxy(t *testing.T) {
	for _, scheme := range []string{"http", "https", "socks5", "socks5h"} {
		t.Run(scheme, func(t *testing.T) {
			type capture struct {
				method, path, proto, cookie, ua, accept, language, referer, origin, cache, contentType string
				body                                                                                   []byte
			}
			requests := make(chan capture, 4)
			hellos := make(chan bool, 8)
			origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				requests <- capture{r.Method, r.URL.Path, r.Proto, r.Header.Get("Cookie"), r.UserAgent(), r.Header.Get("Accept"), r.Header.Get("Accept-Language"), r.Referer(), r.Header.Get("Origin"), r.Header.Get("Cache-Control"), r.Header.Get("Content-Type"), body}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Set-Cookie", "unrelated-cookie=must-not-persist")
				w.Header().Set("Content-Encoding", "gzip")
				compressed := gzip.NewWriter(w)
				_, _ = io.WriteString(compressed, `{"ok":true}`)
				_ = compressed.Close()
			}))
			origin.EnableHTTP2 = true
			origin.TLS = &tls.Config{GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
				grease := slices.ContainsFunc(hello.CipherSuites, func(v uint16) bool { return v&0x0f0f == 0x0a0a && byte(v>>8) == byte(v) })
				hellos <- hello.ServerName == "example.com" && slices.Contains(hello.SupportedProtos, "h2") && grease
				return nil, nil
			}}
			origin.StartTLS()
			defer origin.Close()
			proxyRaw, localAddress, tunnels := localProxy(t, scheme, origin.Listener.Addr().String())
			proxy, pinned, err := proxyTarget(input{ProxyURL: proxyRaw, ProxyAddress: "8.8.8.8"})
			if err != nil {
				t.Fatal(err)
			}
			c := newClient(proxy, pinned, 3*time.Second)
			defer c.GetClient().CloseIdleConnections()
			roots := x509.NewCertPool()
			roots.AddCert(origin.Certificate())
			c.SetTLSClientConfig(&tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})
			c.SetDial(pinnedDial(proxy, pinned, func(ctx context.Context, network, address string) (net.Conn, error) {
				if address != pinned {
					return nil, fmt.Errorf("unpinned dial")
				}
				return (&net.Dialer{}).DialContext(ctx, network, localAddress)
			}))
			for _, operation := range []string{"organizations", "authorize", "token"} {
				in := input{Operation: operation, SessionKey: "sk-ant-sid-test", TimeoutMS: 3000}
				path, method := "/api/organizations", "GET"
				if operation == "authorize" {
					path, method = "/v1/oauth/org-test/authorize", "POST"
					in.OrganizationID = "org-test"
					in.Body = json.RawMessage(`{"organization_uuid":"org-test","code_challenge":"test"}`)
				}
				if operation == "token" {
					path, method = "/v1/oauth/token", "POST"
					in.SessionKey = ""
					in.Body = json.RawMessage(`{"code":"private-code","grant_type":"authorization_code"}`)
				}
				response, err := prepare(c, context.Background(), in).Send(method, "https://example.com"+path)
				if err != nil {
					t.Fatal(err)
				}
				if result := readReply(response.Response); !result.OK {
					t.Fatalf("request failed: %+v", result.Error)
				}
				got := <-requests
				if got.method != method || got.path != path || got.proto != "HTTP/2.0" {
					t.Fatalf("wrong request protocol: %+v", got)
				}
				if operation == "token" {
					if got.cookie != "" || got.ua != "axios/1.13.6" || got.accept != "application/json, text/plain, */*" {
						t.Fatal("token profile differs from SubAPI")
					}
				} else if got.cookie != "sessionKey=sk-ant-sid-test" || !strings.Contains(got.ua, "Chrome/120.") {
					t.Fatal("Cookie/Chrome profile differs from SubAPI")
				}
				if operation == "authorize" && (got.accept != "application/json" || got.language != "en-US,en;q=0.9" || got.referer != "https://claude.ai/new" || got.origin != "https://claude.ai" || got.cache != "no-cache") {
					t.Fatal("authorization headers differ from SubAPI")
				}
				if method == "POST" && (string(got.body) != string(in.Body) || got.contentType != "application/json") {
					t.Fatal("authorization payload changed")
				}
			}
			select {
			case good := <-hellos:
				if !good {
					t.Fatal("Chrome ClientHello or SNI missing")
				}
			default:
				t.Fatal("no TLS handshake observed")
			}
			select {
			case target := <-tunnels:
				if target != "example.com:443" {
					t.Fatal("proxy did not receive fixed target hostname")
				}
			default:
				t.Fatal("proxy was bypassed")
			}
		})
	}
}

func TestRejectedProxyDoesNotReachOriginOrFallback(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; http.Error(w, "private-password", 407) }))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	c := newClient(u, u.Host, time.Second)
	defer c.GetClient().CloseIdleConnections()
	_, err := prepare(c, context.Background(), input{Operation: "organizations", SessionKey: "sk-ant-sid-test"}).Get("https://example.com/api/organizations")
	if !errors.Is(err, errProxyAuth) || calls != 1 {
		t.Fatal("proxy rejection retried or ignored")
	}
}

func TestTLSVerificationAndRedirectBoundary(t *testing.T) {
	for _, trusted := range []bool{false, true} {
		t.Run(fmt.Sprint("trusted=", trusted), func(t *testing.T) {
			var calls int
			origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Location", "https://example.com/unexpected")
				w.WriteHeader(http.StatusFound)
			}))
			defer origin.Close()
			raw, localAddress, _ := localProxy(t, "http", origin.Listener.Addr().String())
			proxy, pinned, err := proxyTarget(input{ProxyURL: raw, ProxyAddress: "8.8.8.8"})
			if err != nil {
				t.Fatal(err)
			}
			c := newClient(proxy, pinned, time.Second)
			defer c.GetClient().CloseIdleConnections()
			c.SetDial(pinnedDial(proxy, pinned, func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, localAddress)
			}))
			if trusted {
				roots := x509.NewCertPool()
				roots.AddCert(origin.Certificate())
				c.SetTLSClientConfig(&tls.Config{RootCAs: roots})
			}
			response, err := prepare(c, context.Background(), input{Operation: "organizations", SessionKey: "sk-ant-sid-test"}).Get("https://example.com/api/organizations")
			if !trusted {
				if err == nil || calls != 0 {
					t.Fatal("untrusted TLS accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			result := readReply(response.Response)
			if calls != 1 || result.Error == nil || result.Error.UpstreamStatus != 302 {
				t.Fatal("redirect followed or not reported")
			}
		})
	}
}
