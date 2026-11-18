package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
	"goapi-tk-register/server/common"
	"goapi-tk-register/server/internal/core"
)

const credentialFlowLifetime = 30 * time.Minute
const credentialBodyLimit = 3 << 20
const credentialDocumentLimit = 2 << 20
const credentialResponseLimit = 2 << 20

var credentialFlowID = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

type credentialProxyCheck struct {
	OK        bool      `json:"ok"`
	IP        string    `json:"ip"`
	CheckedAt time.Time `json:"checked_at"`
	ElapsedMS float64   `json:"elapsed_ms"`
}

type credentialFlowView struct {
	FlowID     string               `json:"flow_id"`
	AuthURL    string               `json:"auth_url,omitempty"`
	Auth       map[string]any       `json:"auth,omitempty"`
	Notes      []string             `json:"notes"`
	ProxyCheck credentialProxyCheck `json:"proxy_check"`
	ExpiresAt  time.Time            `json:"expires_at"`
	Status     string               `json:"status"`
}

type credentialHelperResult struct {
	OwnerID    string               `json:"owner_id"`
	FlowID     string               `json:"flow_id"`
	AuthURL    string               `json:"auth_url"`
	Auth       map[string]any       `json:"auth"`
	Notes      []string             `json:"notes"`
	ProxyCheck credentialProxyCheck `json:"proxy_check"`
	ExpiresAt  time.Time            `json:"expires_at"`
}

// A flow-bound connection test uses /verify, but reports connection failures
// independently of the authorization/exchange workflow.
type credentialProxyTestContextKey struct{}

type credentialFlowRecord struct {
	credentialFlowView
	OwnerID     uint64 `json:"owner_id"`
	AuthVersion uint64 `json:"auth_version"`
	ProxyURL    string `json:"proxy_url"`
}

func credentialFlowKey(owner uint64, id string) string {
	return redisPrefix + "credential-flow:" + strconv.FormatUint(owner, 10) + ":" + id
}

var replaceCredentialFlow = redis.NewScript(`
 if redis.call('GET',KEYS[1])~=ARGV[1] then return 0 end
 local ttl=redis.call('PTTL',KEYS[1]);if ttl<=0 then return 0 end
 local remaining=math.min(ttl,tonumber(ARGV[3]));if remaining<=0 then return 0 end
 redis.call('PSETEX',KEYS[1],remaining,ARGV[2]);return 1
`)

func credentialProblem(status int, code, message string) error {
	return &core.VendorError{Status: status, Code: code, Message: message}
}
func credentialMissing() error {
	return credentialProblem(404, "CREDENTIAL_FLOW_NOT_FOUND", "This authorization draft has expired or does not exist. Start again.")
}
func credentialInvalid() error {
	return credentialProblem(400, "INVALID_REQUEST", "Check the authorization request fields.")
}

func credentialHelperAddress() (string, error) {
	address := os.Getenv("TK_REGISTER_CREDENTIAL_HELPER_URL")
	if address == "" {
		address = "http://127.0.0.1:3003"
	}
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("invalid helper address")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() || u.Port() == "" {
		return "", errors.New("helper must be loopback")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return "", errors.New("invalid helper port")
	}
	return u.Scheme + "://" + u.Host, nil
}

var credentialHelperTransport = &http.Transport{
	// Never send the internal Bearer credential through environment proxies.
	Proxy:                 nil,
	DialContext:           (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	ResponseHeaderTimeout: 55 * time.Second,
	MaxIdleConnsPerHost:   4,
	IdleConnTimeout:       30 * time.Second,
}

func credentialHelperHealthy(ctx context.Context) bool {
	token := os.Getenv("TK_REGISTER_CREDENTIAL_HELPER_TOKEN")
	if token == "" {
		return true
	}
	address, err := credentialHelperAddress()
	if err != nil || len(token) < 32 || strings.ContainsAny(token, "\r\n") {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address+"/healthz", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Transport: credentialHelperTransport, Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var result struct {
		OK bool `json:"ok"`
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1024)).Decode(&result) == nil && result.OK
}

func (a *app) callCredentialHelper(ctx context.Context, owner uint64, endpoint string, payload map[string]any) (credentialHelperResult, error) {
	var result credentialHelperResult
	proxyTest := endpoint == "/proxy-test" || ctx.Value(credentialProxyTestContextKey{}) == true
	address, err := credentialHelperAddress()
	token := os.Getenv("TK_REGISTER_CREDENTIAL_HELPER_TOKEN")
	if err != nil || len(token) < 32 || strings.ContainsAny(token, "\r\n") {
		if proxyTest {
			return result, credentialProblem(503, "PROXY_TEST_UNAVAILABLE", "The connection test service is temporarily unavailable. Please try again later.")
		}
		return result, credentialProblem(503, "CREDENTIAL_HELPER_UNAVAILABLE", "Authorization is temporarily unavailable. Please try again later.")
	}
	payload["owner_id"] = strconv.FormatUint(owner, 10)
	raw, err := json.Marshal(payload)
	if err != nil || len(raw) > credentialBodyLimit {
		return result, credentialInvalid()
	}
	ctx, cancel := context.WithTimeout(ctx, 55*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, address+endpoint, bytes.NewReader(raw))
	if err != nil {
		return result, credentialInvalid()
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: credentialHelperTransport, Timeout: 55 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		if proxyTest {
			var timeout net.Error
			if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
				return result, credentialProxyFailure("upstream_timeout")
			}
			return result, credentialProblem(503, "PROXY_TEST_UNAVAILABLE", "The connection test service could not be reached. Please try again.")
		}
		return result, credentialProblem(503, "CREDENTIAL_HELPER_UNAVAILABLE", "Authorization could not complete. Please try again.")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, credentialResponseLimit+1))
	if err != nil || len(data) > credentialResponseLimit {
		if proxyTest {
			if errors.Is(err, context.DeadlineExceeded) {
				return result, credentialProxyFailure("upstream_timeout")
			}
			return result, credentialProxyFailure("proxy_check_failed")
		}
		return result, credentialProblem(502, "CREDENTIAL_HELPER_INVALID_RESPONSE", "The authorization service returned an invalid response.")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(data, &failure)
		if proxyTest {
			return result, credentialProxyFailure(failure.Error.Code)
		}
		return result, credentialHelperFailure(failure.Error.Code)
	}
	invalidResponse := json.Unmarshal(data, &result) != nil || result.OwnerID != strconv.FormatUint(owner, 10) ||
		!result.ProxyCheck.OK || net.ParseIP(result.ProxyCheck.IP) == nil || result.ProxyCheck.CheckedAt.IsZero() ||
		result.ProxyCheck.CheckedAt.After(time.Now().Add(time.Minute)) || result.ProxyCheck.ElapsedMS < 0 || result.ProxyCheck.ElapsedMS > 60000
	// Only stateless /proxy-test responses have no flow identity or expiry.
	if endpoint != "/proxy-test" && (!credentialFlowID.MatchString(result.FlowID) || !result.ExpiresAt.After(time.Now()) || result.ExpiresAt.After(time.Now().Add(credentialFlowLifetime+time.Minute))) {
		invalidResponse = true
	}
	if invalidResponse {
		if proxyTest {
			return result, credentialProxyFailure("proxy_check_failed")
		}
		return result, credentialProblem(502, "CREDENTIAL_HELPER_INVALID_RESPONSE", "The authorization service returned an invalid response.")
	}
	if len(result.Notes) > 30 {
		return result, credentialInvalid()
	}
	for _, note := range result.Notes {
		if len(note) > 2048 {
			return result, credentialInvalid()
		}
	}
	return result, nil
}

func credentialProxyFailure(code string) error {
	switch code {
	case "upstream_timeout":
		return credentialProblem(504, "PROXY_TEST_TIMEOUT", "The proxy connection test timed out. Check your proxy and try again.")
	case "proxy_authentication_failed":
		return credentialProblem(502, "PROXY_AUTHENTICATION_FAILED", "The proxy rejected its username or password. Check the proxy credentials.")
	case "invalid_proxy", "proxy_required", "proxy_forbidden":
		return credentialProblem(400, "INVALID_PROXY", "Enter a supported, reachable public proxy URL.")
	case "proxy_ip_mismatch", "proxy_mismatch":
		return credentialProblem(409, "PROXY_CHANGED", "The proxy exit IP has changed. Start a new authorization with the intended proxy.")
	case "flow_not_found":
		return credentialMissing()
	case "flow_busy", "session_busy", "credential_busy":
		return credentialProblem(409, "CREDENTIAL_FLOW_BUSY", "A request for this draft is already running. Please wait.")
	case "too_many_sessions", "upstream_rate_limited":
		return credentialProblem(429, "PROXY_TEST_RATE_LIMITED", "Too many connection tests. Please wait before trying again.")
	case "unauthorized":
		return credentialProblem(503, "PROXY_TEST_UNAVAILABLE", "The connection test service is temporarily unavailable. Please try again later.")
	default:
		return credentialProblem(502, "PROXY_CONNECTION_FAILED", "Could not connect through this proxy. Check the address and port and try again.")
	}
}

func credentialHelperFailure(code string) error {
	switch code {
	case "flow_not_found":
		return credentialMissing()
	case "flow_busy", "session_busy", "credential_busy":
		return credentialProblem(409, "CREDENTIAL_FLOW_BUSY", "This authorization is still being processed. Please wait.")
	case "too_many_sessions", "upstream_rate_limited":
		return credentialProblem(429, "CREDENTIAL_RATE_LIMITED", "Too many authorization requests. Please wait before trying again.")
	case "proxy_forbidden", "invalid_proxy", "proxy_required":
		return credentialProblem(400, "INVALID_PROXY", "Enter a supported, reachable proxy URL.")
	case "proxy_ip_mismatch", "proxy_mismatch":
		return credentialProblem(409, "PROXY_CHANGED", "The proxy no longer matches this authorization. Start again with the intended proxy.")
	case "invalid_code", "code_mismatch", "state_mismatch", "code_required", "authorization_declined":
		return credentialProblem(400, "INVALID_AUTHORIZATION_CODE", "The authorization code is invalid or does not match this flow. Check the code or start again.")
	case "invalid_request", "invalid_owner", "invalid_mode", "invalid_setup_kind", "invalid_document", "access_token_required", "session_key_required", "invalid_session_key":
		return credentialInvalid()
	case "upstream_timeout", "upstream_network_error", "proxy_check_failed":
		return credentialProblem(502, "AUTHORIZATION_CONNECTION_FAILED", "The authorization provider could not be reached. Please try again.")
	default:
		return credentialProblem(502, "AUTHORIZATION_FAILED", "Authorization could not be completed. Check your credentials or start again.")
	}
}

func validCredentialProxy(value string) bool {
	if len(value) == 0 || len(value) > 4096 || strings.ContainsAny(value, "\r\n\x00\t ") {
		return false
	}
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return false
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return false
	}
	if p := u.Port(); p != "" {
		n, e := strconv.Atoi(p)
		if e != nil || n < 1 || n > 65535 {
			return false
		}
	}
	return true
}

func credentialAuthShape(auth map[string]any, proxy string) error {
	allowed := map[string]bool{"id_token": true, "access_token": true, "refresh_token": true, "last_refresh": true, "email": true, "account_uuid": true, "organization_uuid": true, "organization_name": true, "claude_device_ids": true, "type": true, "expired": true, "proxy_url": true, "verification_status": true, "disabled": true}
	if auth == nil || len(auth) > len(allowed) || auth["proxy_url"] != proxy || auth["verification_status"] != "verified" {
		return credentialInvalid()
	}
	// Exported templates retain the complete standard field layout even when
	// official credentials cannot supply every value. Submit validates values.
	for name := range allowed {
		if name != "disabled" {
			if _, present := auth[name]; !present {
				return credentialInvalid()
			}
		}
	}
	for name, value := range auth {
		if !allowed[name] {
			return credentialInvalid()
		}
		if name == "disabled" {
			if _, ok := value.(bool); !ok {
				return credentialInvalid()
			}
			continue
		}
		if name == "claude_device_ids" {
			items, ok := value.([]any)
			if !ok || len(items) > 32 {
				return credentialInvalid()
			}
			for _, item := range items {
				s, ok := item.(string)
				if !ok || len(s) > 256 {
					return credentialInvalid()
				}
			}
			continue
		}
		s, ok := value.(string)
		if !ok || len(s) > 65536 || strings.ContainsRune(s, '\x00') {
			return credentialInvalid()
		}
	}
	if value, ok := auth["type"].(string); ok && value != "" && value != "claude" {
		return credentialInvalid()
	}
	encoded, _ := json.Marshal(auth)
	if len(encoded) > 512<<10 {
		return credentialInvalid()
	}
	return nil
}

func (a *app) loadCredentialFlow(ctx context.Context, user *core.User, id string) (credentialFlowRecord, string, error) {
	var flow credentialFlowRecord
	if !credentialFlowID.MatchString(id) {
		return flow, "", credentialMissing()
	}
	value, err := a.redis.Get(ctx, credentialFlowKey(user.ID, id)).Result()
	if errors.Is(err, redis.Nil) {
		return flow, "", credentialMissing()
	}
	if err != nil {
		return flow, "", err
	}
	plain, err := a.store.OpenCredentialFlow(user.ID, id, value)
	if err != nil {
		return flow, "", credentialMissing()
	}
	if json.Unmarshal([]byte(plain), &flow) != nil || flow.OwnerID != user.ID || flow.AuthVersion != user.AuthVersion || flow.FlowID != id || !flow.ExpiresAt.After(time.Now()) {
		return flow, "", credentialMissing()
	}
	return flow, value, nil
}

func (a *app) saveCredentialFlow(ctx context.Context, flow credentialFlowRecord, previous string) error {
	ttl := time.Until(flow.ExpiresAt)
	if ttl <= 0 {
		return credentialMissing()
	}
	if ttl > credentialFlowLifetime {
		ttl = credentialFlowLifetime
	}
	encoded, err := json.Marshal(flow)
	if err != nil {
		return err
	}
	encrypted, err := a.store.SealCredentialFlow(flow.OwnerID, flow.FlowID, string(encoded))
	if err != nil {
		return err
	}
	key := credentialFlowKey(flow.OwnerID, flow.FlowID)
	if previous == "" {
		saved, e := a.redis.SetNX(ctx, key, encrypted, ttl).Result()
		if e != nil {
			return e
		}
		if !saved {
			return credentialProblem(409, "CREDENTIAL_FLOW_CONFLICT", "Start a new authorization flow.")
		}
		return nil
	}
	saved, err := replaceCredentialFlow.Run(ctx, a.redis, []string{key}, previous, encrypted, ttl.Milliseconds()).Int()
	if err != nil {
		return err
	}
	if saved != 1 {
		return credentialMissing()
	}
	return nil
}

func (a *app) lockCredentialFlow(ctx context.Context, user *core.User, id string) (credentialFlowRecord, string, func(), error) {
	flow, previous, err := a.loadCredentialFlow(ctx, user, id)
	if err != nil {
		return flow, previous, nil, err
	}
	token, err := randomToken()
	if err != nil {
		return flow, previous, nil, err
	}
	key := credentialFlowKey(user.ID, id) + ":lease"
	acquired, err := a.redis.SetNX(ctx, key, token, 100*time.Second).Result()
	if err != nil {
		return flow, previous, nil, err
	}
	if !acquired {
		return flow, previous, nil, credentialProblem(409, "CREDENTIAL_FLOW_BUSY", "This authorization is still being processed. Please wait.")
	}
	release := func() { a.releaseDeletionLease(key, token) }
	flow, previous, err = a.loadCredentialFlow(ctx, user, id)
	if err != nil {
		release()
		return flow, previous, nil, err
	}
	return flow, previous, release, nil
}

func (a *app) credentialFlowRoutes(w http.ResponseWriter, r *http.Request, route string) bool {
	if !strings.HasPrefix(route, "credential-flows/") {
		return false
	}
	user, _, good := a.authenticated(w, r, r.Method != "GET")
	if !good {
		return true
	}
	action := strings.TrimPrefix(route, "credential-flows/")
	if r.Method != "GET" && r.Method != "POST" {
		fail(w, 405, "METHOD_NOT_ALLOWED", "This method is not supported.")
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	if r.Method == "GET" {
		if !a.credentialRate(w, r, user.ID, "read", 120) {
			return true
		}
		flow, _, err := a.loadCredentialFlow(ctx, user, action)
		if err != nil {
			vendorFailure(w, err)
		} else {
			ok(w, flow.credentialFlowView)
		}
		return true
	}
	if action == "submit" {
		if !a.limitVendorAction(w, r, user.ID, "push", 100) {
			return true
		}
	} else {
		kind, limit := "update", 30
		if action == "start" || action == "import" {
			kind, limit = "create", 10
		} else if action == "proxy-test" {
			kind = "proxy-test"
		}
		if !a.credentialRate(w, r, user.ID, kind, limit) {
			return true
		}
	}
	switch action {
	case "proxy-test":
		a.testCredentialProxy(w, r, user)
	case "start":
		a.startCredentialFlow(w, r, user)
	case "import":
		a.importCredentialFlow(w, r, user)
	case "exchange", "export", "submit":
		a.updateCredentialFlow(w, r, user, action)
	default:
		parts := strings.Split(action, "/")
		if len(parts) == 2 && parts[1] == "draft" {
			a.patchCredentialFlow(w, r, user, parts[0])
		} else {
			fail(w, 404, "NOT_FOUND", "This endpoint does not exist.")
		}
	}
	return true
}

func (a *app) testCredentialProxy(w http.ResponseWriter, r *http.Request, user *core.User) {
	var input struct {
		ProxyURL json.RawMessage `json:"proxy_url"`
		FlowID   json.RawMessage `json:"flow_id"`
	}
	if !decodeCredential(w, r, &input) {
		return
	}
	// Presence is checked separately from value so an explicit null/empty
	// proxy cannot bypass the rule forbidding overrides on an existing flow.
	if (len(input.ProxyURL) > 0) == (len(input.FlowID) > 0) {
		vendorFailure(w, credentialInvalid())
		return
	}
	ctx, cancel := context.WithTimeout(context.WithValue(r.Context(), credentialProxyTestContextKey{}, true), 20*time.Second)
	defer cancel()
	if len(input.ProxyURL) > 0 {
		var proxy string
		if json.Unmarshal(input.ProxyURL, &proxy) != nil || !validCredentialProxy(proxy) {
			vendorFailure(w, credentialProxyFailure("invalid_proxy"))
			return
		}
		result, err := a.callCredentialHelper(ctx, user.ID, "/proxy-test", map[string]any{"proxy_url": proxy})
		if err != nil {
			vendorFailure(w, err)
			return
		}
		ok(w, map[string]any{"proxy_check": result.ProxyCheck})
		return
	}
	var id string
	if json.Unmarshal(input.FlowID, &id) != nil || id == "" {
		vendorFailure(w, credentialInvalid())
		return
	}
	flow, previous, release, err := a.lockCredentialFlow(ctx, user, id)
	if err != nil {
		vendorFailure(w, err)
		return
	}
	defer release()
	result, err := a.callCredentialHelper(ctx, user.ID, "/verify", map[string]any{"flow_id": flow.FlowID, "proxy_url": flow.ProxyURL})
	if err != nil {
		vendorFailure(w, err)
		return
	}
	if result.FlowID != flow.FlowID || result.ProxyCheck.IP != flow.ProxyCheck.IP {
		vendorFailure(w, credentialProxyFailure("proxy_ip_mismatch"))
		return
	}
	flow.ProxyCheck = result.ProxyCheck
	if result.ExpiresAt.Before(flow.ExpiresAt) {
		flow.ExpiresAt = result.ExpiresAt
	}
	if err = a.saveCredentialFlow(ctx, flow, previous); err != nil {
		vendorFailure(w, err)
		return
	}
	ok(w, map[string]any{"proxy_check": flow.ProxyCheck})
}

func (a *app) credentialRate(w http.ResponseWriter, r *http.Request, owner uint64, kind string, limit int) bool {
	keys := []string{redisPrefix + "credential-rate:" + kind + ":" + strconv.FormatUint(owner, 10)}
	if kind == "create" {
		keys = append(keys, redisPrefix+"credential-ip:"+digest(clientIP(r)))
	}
	retry, err := loginAttempt.Run(r.Context(), a.redis, keys, limit, 60).Int()
	if err != nil {
		internal(w)
		return false
	}
	if retry > 0 {
		rateFailure(w, retry)
		return false
	}
	return true
}

func decodeCredential(w http.ResponseWriter, r *http.Request, value any) bool {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		fail(w, 415, "INVALID_CONTENT_TYPE", "Use application/json.")
		return false
	}
	if err := common.DecodeJson(http.MaxBytesReader(w, r.Body, credentialBodyLimit), value); err != nil {
		vendorFailure(w, credentialInvalid())
		return false
	}
	return true
}

func (a *app) newCredentialFlow(ctx context.Context, user *core.User, result credentialHelperResult, proxy string, ready bool) (credentialFlowRecord, error) {
	expires := result.ExpiresAt
	if max := time.Now().Add(credentialFlowLifetime); expires.After(max) {
		expires = max
	}
	flow := credentialFlowRecord{OwnerID: user.ID, AuthVersion: user.AuthVersion, ProxyURL: proxy, credentialFlowView: credentialFlowView{FlowID: result.FlowID, AuthURL: result.AuthURL, Notes: result.Notes, ProxyCheck: result.ProxyCheck, ExpiresAt: expires, Status: "awaiting_code"}}
	if flow.Notes == nil {
		flow.Notes = []string{}
	}
	if ready {
		if err := credentialAuthShape(result.Auth, proxy); err != nil {
			return flow, err
		}
		flow.Auth = result.Auth
		flow.Status = "ready"
	} else {
		u, err := url.Parse(result.AuthURL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
			return flow, credentialProblem(502, "CREDENTIAL_HELPER_INVALID_RESPONSE", "The authorization service returned an invalid response.")
		}
	}
	err := a.saveCredentialFlow(ctx, flow, "")
	var problem *core.VendorError
	if ready && errors.As(err, &problem) && problem.Code == "CREDENTIAL_FLOW_CONFLICT" {
		// The helper caches imports by owner and input. Restore the same draft
		// without replacing manually completed fields or extending its expiry.
		existing, _, loadErr := a.loadCredentialFlow(ctx, user, flow.FlowID)
		if loadErr == nil && existing.Status == "ready" && existing.ProxyURL == proxy {
			return existing, nil
		}
	}
	return flow, err
}

func (a *app) startCredentialFlow(w http.ResponseWriter, r *http.Request, user *core.User) {
	var input struct {
		ProxyURL  string `json:"proxy_url"`
		Mode      string `json:"mode"`
		SetupKind string `json:"setup_kind"`
	}
	if !decodeCredential(w, r, &input) {
		return
	}
	if input.Mode == "" {
		input.Mode = "oauth"
	}
	if input.SetupKind == "" {
		input.SetupKind = "full"
	}
	if !validCredentialProxy(input.ProxyURL) || (input.Mode != "oauth" && input.Mode != "setup") || (input.SetupKind != "full" && input.SetupKind != "official") {
		vendorFailure(w, credentialInvalid())
		return
	}
	result, err := a.callCredentialHelper(r.Context(), user.ID, "/start", map[string]any{"proxy_url": input.ProxyURL, "mode": input.Mode, "setup_kind": input.SetupKind})
	if err != nil {
		vendorFailure(w, err)
		return
	}
	flow, err := a.newCredentialFlow(r.Context(), user, result, input.ProxyURL, false)
	if err != nil {
		vendorFailure(w, err)
		return
	}
	ok(w, flow.credentialFlowView)
}

func (a *app) importCredentialFlow(w http.ResponseWriter, r *http.Request, user *core.User) {
	var input struct {
		ProxyURL     string `json:"proxy_url"`
		Kind         string `json:"kind"`
		Document     any    `json:"document"`
		AccountIndex *int   `json:"account_index"`
		SessionKey   string `json:"session_key"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresAt    string `json:"expires_at"`
	}
	if !decodeCredential(w, r, &input) {
		return
	}
	if !validCredentialProxy(input.ProxyURL) || (input.Kind != "json" && input.Kind != "cookie" && input.Kind != "token") || (input.AccountIndex != nil && (*input.AccountIndex < 0 || *input.AccountIndex > 10000)) {
		vendorFailure(w, credentialInvalid())
		return
	}
	if len(input.SessionKey) > 65536 || len(input.AccessToken) > 65536 || len(input.RefreshToken) > 65536 || len(input.ExpiresAt) > 64 {
		vendorFailure(w, credentialInvalid())
		return
	}
	if input.Document != nil {
		switch input.Document.(type) {
		case string, map[string]any, []any:
		default:
			vendorFailure(w, credentialInvalid())
			return
		}
		var documentSize int
		if text, isString := input.Document.(string); isString {
			documentSize = len(text)
		} else {
			encoded, encodeErr := json.Marshal(input.Document)
			if encodeErr != nil {
				vendorFailure(w, credentialInvalid())
				return
			}
			documentSize = len(encoded)
		}
		if documentSize > credentialDocumentLimit {
			vendorFailure(w, credentialProblem(400, "INVALID_DOCUMENT", "Credential JSON must not exceed 2 MB."))
			return
		}
	}
	payload := map[string]any{"proxy_url": input.ProxyURL, "kind": input.Kind}
	if input.Document != nil {
		payload["document"] = input.Document
	}
	if input.AccountIndex != nil {
		payload["account_index"] = *input.AccountIndex
	}
	if input.SessionKey != "" {
		payload["session_key"] = input.SessionKey
	}
	if input.AccessToken != "" {
		payload["access_token"] = input.AccessToken
	}
	if input.RefreshToken != "" {
		payload["refresh_token"] = input.RefreshToken
	}
	if input.ExpiresAt != "" {
		payload["expires_at"] = input.ExpiresAt
	}
	result, err := a.callCredentialHelper(r.Context(), user.ID, "/import", payload)
	if err != nil {
		vendorFailure(w, err)
		return
	}
	flow, err := a.newCredentialFlow(r.Context(), user, result, input.ProxyURL, true)
	if err != nil {
		vendorFailure(w, err)
		return
	}
	ok(w, flow.credentialFlowView)
}

func (a *app) updateCredentialFlow(w http.ResponseWriter, r *http.Request, user *core.User, action string) {
	var input struct {
		FlowID            string         `json:"flow_id"`
		Code              string         `json:"code,omitempty"`
		VendorID          string         `json:"vendor_id,omitempty"`
		ProtocolVersionID string         `json:"protocol_version_id,omitempty"`
		ClientRequestKey  string         `json:"client_request_key,omitempty"`
		Parameters        map[string]any `json:"parameters,omitempty"`
	}
	if !decodeCredential(w, r, &input) {
		return
	}
	if action == "exchange" && (strings.TrimSpace(input.Code) == "" || len(input.Code) > 16384) {
		vendorFailure(w, credentialInvalid())
		return
	}
	flow, previous, release, err := a.lockCredentialFlow(r.Context(), user, input.FlowID)
	if err != nil {
		vendorFailure(w, err)
		return
	}
	defer release()
	if action == "exchange" {
		if flow.Status == "ready" {
			ok(w, flow.credentialFlowView)
			return
		}
		result, err := a.callCredentialHelper(r.Context(), user.ID, "/exchange", map[string]any{"flow_id": flow.FlowID, "code": input.Code})
		if err != nil {
			vendorFailure(w, err)
			return
		}
		if result.FlowID != flow.FlowID || credentialAuthShape(result.Auth, flow.ProxyURL) != nil {
			vendorFailure(w, credentialInvalid())
			return
		}
		flow.Auth = result.Auth
		flow.Notes = result.Notes
		if flow.Notes == nil {
			flow.Notes = []string{}
		}
		flow.ProxyCheck = result.ProxyCheck
		flow.Status = "ready"
		if result.ExpiresAt.Before(flow.ExpiresAt) {
			flow.ExpiresAt = result.ExpiresAt
		}
		if err = a.saveCredentialFlow(r.Context(), flow, previous); err != nil {
			vendorFailure(w, err)
			return
		}
		ok(w, flow.credentialFlowView)
		return
	}
	if flow.Status != "ready" || flow.Auth == nil {
		vendorFailure(w, credentialProblem(409, "CREDENTIAL_DRAFT_NOT_READY", "Finish authorization before exporting or submitting this account."))
		return
	}
	if err = credentialAuthShape(flow.Auth, flow.ProxyURL); err != nil {
		vendorFailure(w, err)
		return
	}
	if action == "submit" {
		if _, err = core.ValidateClaudeAuthJSON(flow.Auth); err != nil {
			vendorFailure(w, err)
			return
		}
		account, good := input.Parameters["account"].(string)
		if !good || account == "" || account != flow.Auth["email"] {
			vendorFailure(w, credentialProblem(400, "AUTH_JSON_EMAIL_MISMATCH", "The account must exactly match the authorized account email."))
			return
		}
		// The submitted body can never replace the server-owned credentials.
		input.Parameters["auth_json"] = flow.Auth
		input.Parameters["account"] = flow.Auth["email"]
		if _, _, err = core.ValidateSubmissionInput(input.Parameters); err != nil {
			vendorFailure(w, err)
			return
		}
	}
	result, err := a.callCredentialHelper(r.Context(), user.ID, "/verify", map[string]any{"flow_id": flow.FlowID, "proxy_url": flow.ProxyURL})
	if err != nil {
		vendorFailure(w, err)
		return
	}
	if result.FlowID != flow.FlowID || result.ProxyCheck.IP != flow.ProxyCheck.IP {
		vendorFailure(w, credentialProblem(409, "PROXY_CHANGED", "The proxy no longer matches this authorization. Start again with the intended proxy."))
		return
	}
	flow.ProxyCheck = result.ProxyCheck
	if result.ExpiresAt.Before(flow.ExpiresAt) {
		flow.ExpiresAt = result.ExpiresAt
	}
	if err = a.saveCredentialFlow(r.Context(), flow, previous); err != nil {
		vendorFailure(w, err)
		return
	}
	if action == "export" {
		ok(w, flow.credentialFlowView)
		return
	}
	// Recheck revocation after a potentially slow proxy verification.
	current, err := a.store.GetUserByID(r.Context(), user.ID)
	if err != nil || current.Status != "active" || current.AuthVersion != user.AuthVersion || current.Role != user.Role {
		fail(w, 401, "UNAUTHENTICATED", "Please sign in again.")
		return
	}
	if err = a.store.RecoverVendorExecutions(r.Context()); err != nil {
		internal(w)
		return
	}
	service := a.submissionService()
	detail, err := service.Create(r.Context(), user.ID, core.CreateSubmissionInput{VendorID: input.VendorID, ProtocolVersionID: input.ProtocolVersionID, ClientRequestKey: input.ClientRequestKey, Parameters: input.Parameters})
	if err != nil {
		vendorFailure(w, err)
		return
	}
	a.writePersonalSubmission(w, r, detail)
}

func emptyCredentialValue(value any) bool {
	if value == nil {
		return true
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v) == ""
	case []any:
		return len(v) == 0
	}
	return false
}

func (a *app) patchCredentialFlow(w http.ResponseWriter, r *http.Request, user *core.User, id string) {
	var input struct {
		AuthPatch map[string]any `json:"auth_patch"`
	}
	if !decodeCredential(w, r, &input) {
		return
	}
	if len(input.AuthPatch) == 0 {
		vendorFailure(w, credentialInvalid())
		return
	}
	flow, previous, release, err := a.lockCredentialFlow(r.Context(), user, id)
	if err != nil {
		vendorFailure(w, err)
		return
	}
	defer release()
	if flow.Status != "ready" || flow.Auth == nil {
		vendorFailure(w, credentialProblem(409, "CREDENTIAL_DRAFT_NOT_READY", "Finish authorization before editing this draft."))
		return
	}
	for name, value := range input.AuthPatch {
		if name == "proxy_url" || name == "verification_status" || name == "disabled" {
			vendorFailure(w, credentialProblem(400, "CREDENTIAL_FIELD_LOCKED", "Verified authorization fields cannot be changed. Start a new flow instead."))
			return
		}
		if existing, ok := flow.Auth[name]; ok && !emptyCredentialValue(existing) && !reflect.DeepEqual(existing, value) {
			vendorFailure(w, credentialProblem(400, "CREDENTIAL_FIELD_LOCKED", "Existing credentials cannot be replaced. Start a new flow instead."))
			return
		}
		flow.Auth[name] = value
	}
	if err = credentialAuthShape(flow.Auth, flow.ProxyURL); err != nil {
		vendorFailure(w, err)
		return
	}
	if err = a.saveCredentialFlow(r.Context(), flow, previous); err != nil {
		vendorFailure(w, err)
		return
	}
	ok(w, flow.credentialFlowView)
}
