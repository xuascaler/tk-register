// tk-credential-request performs one bounded request over stdin/stdout. Secrets
// never appear in command arguments, files, or stderr. Rejection diagnostics
// travel only over private IPC and are redacted by Node before server logging.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/imroc/req/v3"
)

const maxBody = 2 * 1024 * 1024

type input struct {
	Operation      string          `json:"operation"`
	OrganizationID string          `json:"organization_id,omitempty"`
	SessionKey     string          `json:"session_key,omitempty"`
	Body           json.RawMessage `json:"body,omitempty"`
	ProxyURL       string          `json:"proxy_url"`
	ProxyAddress   string          `json:"proxy_address"`
	TimeoutMS      int64           `json:"timeout_ms"`
}

type problem struct {
	Code           string `json:"code"`
	Status         int    `json:"status"`
	UpstreamStatus int    `json:"upstream_status,omitempty"`
	UpstreamCode   string `json:"upstream_code,omitempty"`
	ResponseKind   string `json:"response_kind,omitempty"`
}

type reply struct {
	OK         bool             `json:"ok"`
	Data       json.RawMessage  `json:"data,omitempty"`
	Error      *problem         `json:"error,omitempty"`
	Diagnostic *rejectionDetail `json:"diagnostic,omitempty"`
}

type rejectionDetail struct {
	Message string `json:"message"`
}

// This is private IPC, not a public error field. Never include the whole body,
// HTML, extra error fields, or a partial/truncated secret. Node sanitizes it with
// the actual request credentials before adding it to server-only diagnostics.
func rejectionMessage(body []byte) *rejectionDetail {
	var document struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if json.Unmarshal(body, &document) != nil {
		return nil
	}
	var detail struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(document.Error, &detail)
	message := detail.Message
	if message == "" {
		message = document.Message
	}
	if message == "" || len(message) > 8192 {
		return nil
	}
	return &rejectionDetail{Message: message}
}

func failure(code string, status int) reply {
	return reply{Error: &problem{Code: code, Status: status}}
}

var orgID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)
var errProxyAuth = errors.New("proxy authentication failed")

// Match the helper's conservative public-address policy, including IPv6
// transition and documentation ranges. DNS has already been checked by Node.
func publicAddress(value string) bool {
	a, err := netip.ParseAddr(value)
	if err != nil || a.Zone() != "" || a.Is4In6() {
		return false
	}
	denied := []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4"}
	if a.Is6() {
		if !netip.MustParsePrefix("2000::/3").Contains(a) {
			return false
		}
		denied = []string{"2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20"}
	}
	for _, prefix := range denied {
		if netip.MustParsePrefix(prefix).Contains(a) {
			return false
		}
	}
	return true
}

func proxyTarget(in input) (*url.URL, string, error) {
	u, err := url.Parse(in.ProxyURL)
	if err != nil || u.Hostname() == "" || u.Opaque != "" || strings.Contains(u.Hostname(), "%") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, "", errors.New("invalid proxy")
	}
	port := u.Port()
	switch u.Scheme {
	case "http":
		if port == "" {
			port = "80"
		}
	case "https":
		if port == "" {
			port = "443"
		}
	case "socks5", "socks5h":
		if port == "" {
			port = "1080"
		}
	default:
		return nil, "", errors.New("invalid proxy")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return nil, "", errors.New("invalid proxy")
	}
	if u.User != nil {
		username := u.User.Username()
		password, _ := u.User.Password()
		if (username == "" && password != "") || len(username) > 255 || len(password) > 255 || strings.ContainsAny(username+password, "\r\n\x00") {
			return nil, "", errors.New("invalid proxy")
		}
	}
	if !publicAddress(in.ProxyAddress) {
		return nil, "", errors.New("invalid proxy address")
	}
	// Explicit port avoids alternate defaults in proxy implementations.
	u.Host = net.JoinHostPort(u.Hostname(), port)
	return u, net.JoinHostPort(in.ProxyAddress, port), nil
}

func destination(in input) (string, string, error) {
	if in.TimeoutMS < 1 || in.TimeoutMS > 60000 || len(in.Body) > maxBody {
		return "", "", errors.New("invalid request")
	}
	if in.Operation == "organizations" || in.Operation == "authorize" {
		if !strings.HasPrefix(in.SessionKey, "sk-ant-sid") || len(in.SessionKey) > 65536 || strings.ContainsAny(in.SessionKey, "\r\n\x00; \t\"") {
			return "", "", errors.New("invalid session key")
		}
	} else if in.SessionKey != "" {
		return "", "", errors.New("unexpected cookie")
	}
	switch in.Operation {
	case "organizations":
		if len(in.Body) != 0 || in.OrganizationID != "" {
			return "", "", errors.New("unexpected body")
		}
		return "https://claude.ai/api/organizations", http.MethodGet, nil
	case "authorize", "token":
		var body map[string]json.RawMessage
		if json.Unmarshal(in.Body, &body) != nil || body == nil {
			return "", "", errors.New("invalid body")
		}
		if in.Operation == "token" {
			if in.OrganizationID != "" {
				return "", "", errors.New("unexpected organization")
			}
			return "https://platform.claude.com/v1/oauth/token", http.MethodPost, nil
		}
		var organization string
		if !orgID.MatchString(in.OrganizationID) || json.Unmarshal(body["organization_uuid"], &organization) != nil || organization != in.OrganizationID {
			return "", "", errors.New("invalid organization")
		}
		return "https://claude.ai/v1/oauth/" + in.OrganizationID + "/authorize", http.MethodPost, nil
	}
	return "", "", errors.New("unknown operation")
}

type dialFunc func(context.Context, string, string) (net.Conn, error)

func pinnedDial(proxy *url.URL, address string, dial dialFunc) dialFunc {
	return func(ctx context.Context, network, target string) (net.Conn, error) {
		if network != "tcp" || !strings.EqualFold(target, proxy.Host) {
			return nil, errors.New("direct connection forbidden")
		}
		return dial(ctx, network, address)
	}
}

func newClient(proxy *url.URL, address string, timeout time.Duration) *req.Client {
	// Same req version and Chrome profile as SubAPI createReqClient. Retain TK's
	// stricter network boundary, bounded response reads, and no automatic replay.
	c := req.C().SetTimeout(timeout).ImpersonateChrome().SetCookieJar(nil).
		SetProxyURL(proxy.String()).SetRedirectPolicy(req.NoRedirectPolicy()).
		SetCommonRetryCount(0).DisableAutoReadResponse().DisableAutoDecode().SetLogger(nil)
	c.SetDial(pinnedDial(proxy, address, (&net.Dialer{Timeout: timeout}).DialContext))
	c.GetTransport().OnProxyConnectResponse = func(_ context.Context, _ *url.URL, _ *http.Request, response *http.Response) error {
		if response.StatusCode == http.StatusProxyAuthRequired {
			return errProxyAuth
		}
		return nil
	}
	return c
}

func prepare(c *req.Client, ctx context.Context, in input) *req.Request {
	r := c.R().SetContext(ctx).SetRetryCount(0)
	if in.SessionKey != "" {
		r.SetCookies(&http.Cookie{Name: "sessionKey", Value: in.SessionKey})
	}
	switch in.Operation {
	case "authorize":
		r.SetHeader("Accept", "application/json").SetHeader("Accept-Language", "en-US,en;q=0.9").
			SetHeader("Cache-Control", "no-cache").SetHeader("Origin", "https://claude.ai").
			SetHeader("Referer", "https://claude.ai/new").SetContentType("application/json").SetBodyBytes(in.Body)
	case "token":
		r.SetHeader("Accept", "application/json, text/plain, */*").SetHeader("User-Agent", "axios/1.13.6").
			SetContentType("application/json").SetBodyBytes(in.Body)
	}
	return r
}

func safeUpstreamCode(body []byte) string {
	var document struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &document) != nil {
		return ""
	}
	var code string
	if json.Unmarshal(document.Error, &code) != nil {
		var nested struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(document.Error, &nested)
		code = nested.Type
	}
	switch code {
	case "invalid_grant", "invalid_scope", "invalid_request", "authentication_error", "permission_error", "rate_limit_error", "overloaded_error", "invalid_client", "unauthorized_client", "unsupported_grant_type":
		return code
	}
	return ""
}

func readReply(response *http.Response) reply {
	defer response.Body.Close()
	p := &problem{UpstreamStatus: response.StatusCode, ResponseKind: "other"}
	if strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/html") {
		p.ResponseKind = "html"
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil {
		p.Code, p.Status = "upstream_network_error", 502
		var netErr net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
			p.Code, p.Status = "upstream_timeout", 504
		}
		return reply{Error: p}
	}
	if len(body) > maxBody {
		p.Code, p.Status = "upstream_invalid_response", 502
		return reply{Error: p}
	}
	validJSON := json.Valid(body)
	if validJSON {
		p.ResponseKind = "json"
	} else {
		lower := strings.ToLower(strings.TrimSpace(string(body)))
		if strings.HasPrefix(lower, "<!doctype html") || strings.HasPrefix(lower, "<html") {
			p.ResponseKind = "html"
		}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		p.Code, p.Status, p.UpstreamCode = "upstream_rejected", 502, safeUpstreamCode(body)
		diagnostic := rejectionMessage(body)
		// Only this explicit provider rejection means a new Claude sign-in is
		// required. Other permission errors must not be reported as stale sessions.
		if response.StatusCode == http.StatusForbidden && p.UpstreamCode == "permission_error" && diagnostic != nil &&
			strings.TrimSpace(diagnostic.Message) == "Session is not fresh enough to grant elevated access. Sign in again to continue." {
			p.Code = "upstream_session_not_fresh"
		}
		if response.StatusCode == 429 {
			p.Code, p.Status = "upstream_rate_limited", 429
		}
		return reply{Error: p, Diagnostic: diagnostic}
	}
	if !validJSON {
		p.Code, p.Status = "upstream_invalid_response", 502
		return reply{Error: p}
	}
	return reply{OK: true, Data: body}
}

func perform(in input) reply {
	target, method, err := destination(in)
	if err != nil {
		return failure("invalid_request", 400)
	}
	proxy, address, err := proxyTarget(in)
	if err != nil {
		return failure("proxy_forbidden", 400)
	}
	timeout := time.Duration(in.TimeoutMS) * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c := newClient(proxy, address, timeout)
	defer c.GetClient().CloseIdleConnections()
	response, err := prepare(c, ctx, in).Send(method, target)
	if err != nil {
		var netErr net.Error
		if ctx.Err() != nil || (errors.As(err, &netErr) && netErr.Timeout()) {
			return failure("upstream_timeout", 504)
		}
		if errors.Is(err, errProxyAuth) || strings.Contains(err.Error(), "username/password authentication failed") || strings.Contains(err.Error(), "no acceptable authentication methods") {
			return failure("proxy_authentication_failed", 502)
		}
		return failure("upstream_network_error", 502)
	}
	return readReply(response.Response)
}

func run(stdin io.Reader, stdout io.Writer) {
	// Raw panics and errors may contain credentials. Output only this fixed schema.
	defer func() {
		if recover() != nil {
			_ = json.NewEncoder(stdout).Encode(failure("internal_error", 502))
		}
	}()
	body, err := io.ReadAll(io.LimitReader(stdin, 3*1024*1024+1))
	if err != nil || len(body) > 3*1024*1024 {
		_ = json.NewEncoder(stdout).Encode(failure("invalid_request", 400))
		return
	}
	var in input
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil || d.Decode(&struct{}{}) != io.EOF {
		_ = json.NewEncoder(stdout).Encode(failure("invalid_request", 400))
		return
	}
	_ = json.NewEncoder(stdout).Encode(perform(in))
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--check" {
		_, _ = io.WriteString(os.Stdout, "{\"ok\":true,\"profile\":\"req-v3.59.0-chrome\"}\n")
		return
	}
	if len(os.Args) != 1 {
		_ = json.NewEncoder(os.Stdout).Encode(failure("invalid_request", 400))
		return
	}
	run(os.Stdin, os.Stdout)
}
