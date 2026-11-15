package supplier

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"goapi-tk-register/server/common"
	"goapi-tk-register/server/internal/diagnostics"
	"goapi-tk-register/server/internal/safemessage"
)

type statusReasonsKey struct{}

// WithStatusReasons opts explicit manual Sync into the supplementary read.
// Scheduled and post-write collection never opt in or perform these extra calls.
func WithStatusReasons(ctx context.Context) context.Context {
	return context.WithValue(ctx, statusReasonsKey{}, true)
}

// Failed states may expose additional details in a supplementary reason read.
// Keep this explicit so pending, healthy, and unknown states do not add calls.
func needsStatusReason(status *string) bool {
	if status == nil {
		return false
	}
	switch *status {
	case "banned", "offline", "sync_failed", "login_failed", "re_login_failed", "bound_failed", "cooking_failed":
		return true
	default:
		return false
	}
}

func validStatusReasonPath(path string) bool {
	const prefix = apiPrefix + "/accounts/"
	suffix := "/claude-status-reason"
	if strings.HasSuffix(path, "/check-fail-reason") {
		suffix = "/check-fail-reason"
	}
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	return id != "" && positiveID(json.RawMessage(id)) == id
}
func (b *targetBatch) statusReason(ctx context.Context, raw rawAccount, id, username, password string) (*string, error) {
	// The supplier website uses verification failures for sync_failed, and
	// a separate Claude status log for the other account failure states.
	suffix := "/claude-status-reason"
	if strings.EqualFold(strings.TrimSpace(raw.Status), "sync_failed") {
		suffix = "/check-fail-reason"
	}
	status, body, err := b.read(ctx, username, password, apiPrefix+"/accounts/"+id+suffix)
	if err != nil {
		return nil, err
	}
	if err = readStatus(status); err != nil {
		return nil, err
	}
	// The account row already fetched for identity resolution may contain secrets.
	// Redact those exact values as well as generic credential patterns.
	secrets := []string{password}
	var config any
	if nestedObject(raw.AuthConfig, &config) == nil {
		secrets = append(secrets, secretLeaves(config, false)...)
	}
	b.auth.Lock()
	secrets = append(secrets, b.token)
	b.auth.Unlock()
	return parseStatusReason(body, secrets...)
}
func secretLeaves(value any, sensitive bool) []string {
	var out []string
	switch v := value.(type) {
	case map[string]any:
		for k, child := range v {
			key := strings.ToLower(k)
			secret := sensitive || strings.Contains(key, "token") || strings.Contains(key, "password") || strings.Contains(key, "secret") || strings.Contains(key, "auth_json") || strings.Contains(key, "api_key") || key == "proxy_url" || key == "cookie"
			out = append(out, secretLeaves(child, secret)...)
		}
	case []any:
		for _, child := range v {
			out = append(out, secretLeaves(child, sensitive)...)
		}
	case string:
		if sensitive && v != "" {
			out = append(out, v)
		}
		if strings.HasPrefix(strings.TrimSpace(v), "{") {
			var child any
			if common.Unmarshal([]byte(v), &child) == nil {
				out = append(out, secretLeaves(child, sensitive)...)
			}
		}
	}
	return out
}

func parseStatusReason(body []byte, secrets ...string) (*string, error) {
	var response struct {
		Found   *bool   `json:"found"`
		Content *string `json:"content"`
	}
	if common.Unmarshal(body, &response) != nil || response.Found == nil || (*response.Found && response.Content == nil) {
		return nil, safeError(CodeContractMismatch)
	}
	text := ""
	if *response.Found {
		text = *response.Content
	}
	text = safemessage.Text(text, secrets...)
	return &text, nil
}

var ErrReasonSessionUnavailable = errors.New("supplier session is not available for background reason reads")

// ReadAccountReason is a low-priority, reason-only read using an already cached
// session. It never logs in, waits for the login lock, searches accounts, or
// retries HTTP calls. Normal collection/manual operations own session renewal.
func (c *Client) ReadAccountReason(ctx context.Context, username, password, recordID, status string) (*string, error) {
	if recordID == "" || positiveID(json.RawMessage(recordID)) != recordID || !needsStatusReason(&status) {
		return nil, safeError(CodeContractMismatch)
	}
	if c.cache == nil {
		return nil, ErrReasonSessionUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if err := c.cache.cooldown(ctx, "accounts"); err != nil {
		return nil, err
	}
	key, err := c.cache.credentialKey(ctx, username, password)
	if err != nil {
		return nil, err
	}
	entry, raw, err := c.cache.read(ctx, key)
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return nil, ErrReasonSessionUnavailable
	}
	s := c.newSession()
	defer s.http.CloseIdleConnections()
	s.cacheKey = key
	s.applySession(entry, raw)
	s.remaining = 1
	s.classifyReadErrors = true
	diagnostics.LogSession(ctx, "cache_hit", "login", 0)
	suffix := "/claude-status-reason"
	if status == "sync_failed" {
		suffix = "/check-fail-reason"
	}
	code, body, err := s.request(ctx, http.MethodGet, apiPrefix+"/accounts/"+recordID+suffix, nil)
	if err != nil {
		return nil, err
	}
	if code == http.StatusUnauthorized || code == http.StatusForbidden {
		return nil, ErrReasonSessionUnavailable
	}
	if err = readStatus(code); err != nil {
		return nil, err
	}
	return parseStatusReason(body, password, s.token)
}
