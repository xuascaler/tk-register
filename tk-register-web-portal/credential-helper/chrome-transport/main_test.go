package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

func TestRequestBoundary(t *testing.T) {
	valid := input{Operation: "organizations", SessionKey: "sk-ant-sid-test", TimeoutMS: 1000}
	if target, method, err := destination(valid); err != nil || target != "https://claude.ai/api/organizations" || method != "GET" {
		t.Fatal("valid organization request rejected")
	}
	for _, mutate := range []func(*input){
		func(i *input) { i.Operation = "https://evil.example" },
		func(i *input) { i.TimeoutMS = 60001 },
		func(i *input) { i.SessionKey += "\r\nHost: evil.example" },
		func(i *input) { i.Operation = "token"; i.Body = json.RawMessage(`{"code":"fake"}`) },
		func(i *input) {
			i.Operation = "authorize"
			i.OrganizationID = "../token"
			i.Body = json.RawMessage(`{"organization_uuid":"../token"}`)
		},
		func(i *input) {
			i.Operation = "authorize"
			i.OrganizationID = "org-1"
			i.Body = json.RawMessage(`{"organization_uuid":"org-2"}`)
		},
	} {
		bad := valid
		mutate(&bad)
		if _, _, err := destination(bad); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
}

func TestProxyBoundary(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.1.1.1", "169.254.169.254", "192.168.1.1", "198.18.0.1", "203.0.113.1", "::1", "::ffff:8.8.8.8", "64:ff9b::808:808", "2001:db8::1", "2002::1", "3fff::1"} {
		if publicAddress(ip) {
			t.Fatalf("nonpublic address accepted: %s", ip)
		}
	}
	for _, ip := range []string{"8.8.8.8", "2606:4700:4700::1111"} {
		if !publicAddress(ip) {
			t.Fatal("public address rejected")
		}
	}
	for _, raw := range []string{"", "file:///tmp/proxy", "ftp://proxy.example", "http://proxy.example?password=private", "http://proxy.example/path", "http://proxy.example:0", "http://u%0ap:p@proxy.example"} {
		if _, _, err := proxyTarget(input{ProxyURL: raw, ProxyAddress: "8.8.8.8"}); err == nil {
			t.Fatal("invalid proxy accepted")
		}
	}
	u, address, err := proxyTarget(input{ProxyURL: "socks5h://user:pass@proxy.example", ProxyAddress: "8.8.8.8"})
	if err != nil || address != "8.8.8.8:1080" || u.Host != "proxy.example:1080" {
		t.Fatal("proxy was not normalized")
	}
	var dialed string
	dial := pinnedDial(u, address, func(_ context.Context, _, target string) (net.Conn, error) { dialed = target; return nil, nil })
	_, _ = dial(context.Background(), "tcp", u.Host)
	if dialed != address {
		t.Fatal("proxy address was not pinned")
	}
	dialed = ""
	if _, err := dial(context.Background(), "tcp", "claude.ai:443"); err == nil || dialed != "" {
		t.Fatal("direct fallback allowed")
	}
}

func TestSafeResponse(t *testing.T) {
	for _, tc := range []struct {
		status                        int
		body, contentType, code, kind string
	}{
		{403, "<!doctype html><html>private-cookie</html>", "text/html", "upstream_rejected", "html"},
		{401, `{"error":{"type":"authentication_error","message":"private-cookie"}}`, "application/json", "upstream_rejected", "json"},
		{429, `{"error":"rate_limit_error"}`, "application/json", "upstream_rate_limited", "json"},
		{302, "private-cookie", "text/plain", "upstream_rejected", "other"},
		{200, "not JSON private-cookie", "text/plain", "upstream_invalid_response", "other"},
		{200, strings.Repeat("x", maxBody+1), "text/plain", "upstream_invalid_response", "other"},
	} {
		result := readReply(&http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": {tc.contentType}}, Body: io.NopCloser(strings.NewReader(tc.body))})
		if result.OK || result.Error == nil || result.Error.Code != tc.code || result.Error.ResponseKind != tc.kind || result.Error.UpstreamStatus != tc.status {
			t.Fatalf("wrong safe response: %+v", result.Error)
		}
		encoded, _ := json.Marshal(result.Error)
		if bytes.Contains(encoded, []byte("private-cookie")) {
			t.Fatal("upstream body leaked")
		}
	}
	result := readReply(&http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`[{"uuid":"org-1"}]`))})
	if !result.OK || string(result.Data) != `[{"uuid":"org-1"}]` {
		t.Fatal("successful JSON changed")
	}
}

func TestSessionFreshnessRejection(t *testing.T) {
	const message = "Session is not fresh enough to grant elevated access. Sign in again to continue."
	for _, tc := range []struct {
		name, providerCode, message, want string
		status                            int
	}{
		{"freshness", "permission_error", message, "upstream_session_not_fresh", 403},
		{"whitespace", "permission_error", " " + message + "\n", "upstream_session_not_fresh", 403},
		{"other permission", "permission_error", "Access to this organization is denied.", "upstream_rejected", 403},
		{"other type", "authentication_error", message, "upstream_rejected", 403},
		{"other status", "permission_error", message, "upstream_rejected", 401},
		{"additional private text", "permission_error", message + " private-cookie", "upstream_rejected", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{"error": map[string]string{"type": tc.providerCode, "message": tc.message}})
			result := readReply(&http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(body))})
			if result.OK || result.Error == nil || result.Error.Code != tc.want || result.Error.Status != 502 || result.Error.UpstreamStatus != tc.status || result.Error.ResponseKind != "json" {
				t.Fatalf("incorrect freshness classification: %+v", result.Error)
			}
			public, _ := json.Marshal(result.Error)
			if bytes.Contains(public, []byte(tc.message)) {
				t.Fatal("raw provider message leaked into public error")
			}
		})
	}
}

func TestRejectionMessageStaysSeparateFromPublicError(t *testing.T) {
	body := `{"error":{"type":"permission_error","message":"OAuth is not enabled for this organization.","credential":"private-token"}}`
	result := readReply(&http.Response{StatusCode: 403, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))})
	if result.Diagnostic == nil || result.Diagnostic.Message != "OAuth is not enabled for this organization." {
		t.Fatal("provider rejection message lost")
	}
	encoded, _ := json.Marshal(result)
	if bytes.Contains(encoded, []byte("private-token")) {
		t.Fatal("unrelated error field leaked")
	}
	public, _ := json.Marshal(result.Error)
	if bytes.Contains(public, []byte("OAuth is not enabled")) {
		t.Fatal("diagnostic added to public error")
	}
	for _, raw := range []string{`<html>private-token</html>`, `{"error":{"message":{"token":"private-token"}}}`, `{"error":{"message":"` + strings.Repeat("x", 8193) + `"}}`} {
		if rejectionMessage([]byte(raw)) != nil {
			t.Fatal("unsafe diagnostic accepted")
		}
	}
}

func TestStdinRejectsUnknownFieldsBeforeNetworking(t *testing.T) {
	var out bytes.Buffer
	run(strings.NewReader(`{"operation":"organizations","url":"https://evil.example","session_key":"private-secret"}`), &out)
	if strings.Contains(out.String(), "private-secret") || !strings.Contains(out.String(), `"code":"invalid_request"`) {
		t.Fatal("unsafe malformed input result")
	}
	// No credentials may be accepted from environment URLs or a missing proxy.
	if result := perform(input{Operation: "organizations", SessionKey: "sk-ant-sid-test", TimeoutMS: 1000}); result.Error.Code != "proxy_forbidden" {
		t.Fatal("missing proxy accepted")
	}
}
