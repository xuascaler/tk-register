package core

import (
	"log"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"goapi-tk-register/server/common"
)

const vendorErrorBodyLimit = 8 << 10

var (
	logCredentialMarker      = regexp.MustCompile(`(?i)(authorization|bearer|password|passwd|passphrase|secret|token|auth[_ -]?json|cookie|credential|api[_ -]?key|proxy[_ -]?url)`)
	logBearerValue           = regexp.MustCompile(`(?i)\bbearer\s+[^\s"'<>;,}]+`)
	logCredentialValue       = regexp.MustCompile(`(?i)\b(password|passwd|passphrase|secret|(?:access_|refresh_|id_)?token|authorization|cookie|api[_-]?key|proxy[_-]?url)\b\s*[:=]\s*(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|[^\s,;}\]]+)`)
	logURLCredentials        = regexp.MustCompile(`(?i)(https?://)[^/\s:@]+:[^@\s/]+@`)
	logRedactedCredential    = regexp.MustCompile(`(?i)\b(?:password|passwd|passphrase|secret|(?:access[_ -]|refresh[_ -]|id[_ -])?token|authorization|bearer|cookie|api[_ -]?key|proxy[_ -]?url)\b\s*(?:[:=]\s*)?\[REDACTED\]`)
	logSimpleCredentialError = regexp.MustCompile(`(?i)^(?:(?:invalid|missing|expired|incorrect|unsupported) (?:access |refresh |id |api )?(?:token|password|credentials?|authorization|api key)|(?:access |refresh |id |api )?(?:token|password|credentials?|authorization|api key) (?:is |has )?(?:invalid|missing|expired|incorrect|unsupported|required))[.!]?$`)
)

// Error bodies are operational diagnostics, not client response data. Never log
// request headers/bodies or credential snapshots. The Docker log driver applies
// the deployment's configured rotation to these single-line records.
func (v *VendorService) logExecutionFailure(record Submission, execution ProtocolExecution, result executionResult, input map[string]any, token string) {
	if result.status == "succeeded" {
		return
	}
	body, truncated, omitted := safeVendorErrorBody(result, input, token)
	event := map[string]any{
		"vendor_id": record.VendorID, "submission_id": record.ID,
		"execution_id": execution.ID, "protocol_version_id": execution.ProtocolVersionID,
		"request_id": execution.RequestID, "operation": execution.Operation,
		"http_status": result.httpStatus, "error_code": result.code,
		"body": body, "body_bytes_received": len(result.responseBody),
		"body_truncated": truncated, "body_incomplete": result.responseIncomplete,
	}
	if omitted != "" {
		event["body_omitted_reason"] = omitted
	}
	encoded, err := common.Marshal(event)
	if err != nil {
		return
	}
	logger := v.ErrorLogger
	if logger == nil {
		logger = log.Default()
	}
	logger.Printf("TK_REGISTER_VENDOR_ERROR %s", encoded)
}

func safeVendorErrorBody(result executionResult, input map[string]any, token string) (string, bool, string) {
	// A partial body can cut through a credential, making exact-value redaction
	// unreliable. Retain metadata but omit its contents in that case.
	if result.responseTruncated || result.responseIncomplete {
		return "", result.responseTruncated, "incomplete_or_oversized_response"
	}
	if len(result.responseBody) == 0 {
		return "", false, "empty_response"
	}
	if !utf8.Valid(result.responseBody) {
		return "", false, "non_text_response"
	}
	secrets := []string{token}
	collectSecretStrings(input["password"], &secrets)
	collectSecretStrings(input["auth_json"], &secrets)
	// Some callers supply Auth JSON encoded as a string. Include its leaf values,
	// not just the whole JSON document, when redacting a reflected validation error.
	if auth, ok := input["auth_json"].(string); ok {
		var decoded any
		if common.Unmarshal([]byte(auth), &decoded) == nil {
			collectSecretStrings(decoded, &secrets)
		}
	}
	replace := logSecretReplacer(secrets)
	var decoded any
	var body string
	if common.Unmarshal(result.responseBody, &decoded) == nil {
		safe := sanitizeLogValue(decoded, replace, 0)
		encoded, err := common.Marshal(safe)
		if err != nil {
			return "", false, "unreadable_response"
		}
		body = string(encoded)
	} else {
		// Unstructured errors still retain ordinary diagnostic text. Malformed
		// credential-bearing documents are omitted rather than guessed at.
		body = replace.Replace(string(result.responseBody))
		trimmed := strings.TrimSpace(body)
		if logCredentialMarker.MatchString(body) && (strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "<")) {
			return "", false, "unstructured_sensitive_response"
		}
		body = redactLogText(body)
		if logCredentialMarker.MatchString(logRedactedCredential.ReplaceAllString(body, "")) && !logSimpleCredentialError.MatchString(strings.TrimSpace(body)) {
			return "", false, "unstructured_sensitive_response"
		}
	}
	if len(body) <= vendorErrorBodyLimit {
		return body, false, ""
	}
	end := vendorErrorBodyLimit
	for end > 0 && !utf8.ValidString(body[:end]) {
		end--
	}
	return body[:end], true, ""
}

func logSecretReplacer(secrets []string) *strings.Replacer {
	unique := map[string]bool{}
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		unique[secret] = true
		unique[url.QueryEscape(secret)] = true
		unique[url.PathEscape(secret)] = true
		if escaped, err := common.Marshal(secret); err == nil && len(escaped) >= 2 {
			unique[string(escaped[1:len(escaped)-1])] = true
		}
	}
	values := make([]string, 0, len(unique))
	for value := range unique {
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	pairs := make([]string, 0, len(values)*2)
	for _, value := range values {
		pairs = append(pairs, value, "[REDACTED]")
	}
	return strings.NewReplacer(pairs...)
}

func sensitiveLogKey(key string) bool {
	normal := strings.ToLower(strings.NewReplacer("_", "", "-", "", " ", "").Replace(key))
	return strings.Contains(normal, "password") || strings.Contains(normal, "passwd") ||
		strings.Contains(normal, "passphrase") || strings.Contains(normal, "secret") ||
		strings.Contains(normal, "credential") || strings.HasSuffix(normal, "token") ||
		strings.HasSuffix(normal, "apikey") || strings.Contains(normal, "authorization") ||
		strings.Contains(normal, "cookie") || normal == "authjson" || normal == "proxyurl"
}

func sanitizeLogValue(value any, replace *strings.Replacer, depth int) any {
	if depth > 64 {
		return "[REDACTED deeply nested content]"
	}
	switch item := value.(type) {
	case map[string]any:
		// Validation errors may report a secret field in `loc` and echo only its
		// value under a generic `input` key (for example FastAPI/Pydantic errors).
		sensitiveInput := false
		if location, ok := item["loc"].([]any); ok {
			for _, part := range location {
				if name, ok := part.(string); ok && sensitiveLogKey(name) {
					sensitiveInput = true
				}
			}
		}
		out := make(map[string]any, len(item))
		for key, val := range item {
			safeKey := replace.Replace(key)
			if sensitiveLogKey(key) || (key == "input" && sensitiveInput) {
				out[safeKey] = "[REDACTED]"
			} else {
				out[safeKey] = sanitizeLogValue(val, replace, depth+1)
			}
		}
		return out
	case []any:
		out := make([]any, len(item))
		for i, val := range item {
			out[i] = sanitizeLogValue(val, replace, depth+1)
		}
		return out
	case string:
		text := replace.Replace(item)
		var embedded any
		trimmed := strings.TrimSpace(text)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			if common.Unmarshal([]byte(trimmed), &embedded) == nil {
				encoded, err := common.Marshal(sanitizeLogValue(embedded, replace, depth+1))
				if err == nil {
					return string(encoded)
				}
			} else if logCredentialMarker.MatchString(text) {
				return "[REDACTED unstructured credential content]"
			}
		}
		return redactLogText(text)
	default:
		return value
	}
}

func redactLogText(text string) string {
	text = logBearerValue.ReplaceAllString(text, "Bearer [REDACTED]")
	text = logCredentialValue.ReplaceAllString(text, "$1=[REDACTED]")
	return logURLCredentials.ReplaceAllString(text, "${1}[REDACTED]@")
}
