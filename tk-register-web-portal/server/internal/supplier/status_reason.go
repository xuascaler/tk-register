package supplier

import (
	"context"
	"encoding/json"
	"strings"

	"goapi-tk-register/server/common"
	"goapi-tk-register/server/internal/safemessage"
)

type statusReasonsKey struct{}

// WithStatusReasons opts explicit manual Sync into the supplementary read.
// Scheduled and post-write collection never opt in or perform these extra calls.
func WithStatusReasons(ctx context.Context) context.Context {
	return context.WithValue(ctx, statusReasonsKey{}, true)
}
func validStatusReasonPath(path string) bool {
	const prefix = apiPrefix + "/accounts/"
	const suffix = "/claude-status-reason"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	return id != "" && positiveID(json.RawMessage(id)) == id
}
func (b *targetBatch) statusReason(ctx context.Context, raw rawAccount, id, username, password string) (*string, error) {
	status, body, err := b.read(ctx, username, password, apiPrefix+"/accounts/"+id+"/claude-status-reason")
	if err != nil {
		return nil, err
	}
	if err = readStatus(status); err != nil {
		return nil, err
	}
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
	text = safemessage.Text(text, secrets...)
	return &text, nil
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
