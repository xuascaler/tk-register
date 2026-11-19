package diagnostics

import (
	"context"
)

// Credential diagnostics deliberately accept enums only. In particular, an
// upstream message, URL, credential, or response body is never a log field.
func CredentialStage(value string) string {
	switch value {
	case "request_validation", "proxy_check", "cookie_organizations", "cookie_authorize", "oauth_token", "profile", "auth_json_conversion", "helper_transport":
		return value
	}
	return ""
}

func CredentialUpstreamCode(value string) string {
	switch value {
	case "invalid_grant", "invalid_scope", "authentication_error", "permission_error", "invalid_request", "rate_limit_error", "overloaded_error", "invalid_client", "unauthorized_client", "unsupported_grant_type":
		return value
	}
	return ""
}

func CredentialResponseKind(value string) string {
	switch value {
	case "json", "html", "other":
		return value
	}
	return ""
}

func CredentialMethod(value string) string {
	return allowed(value, "oauth", "setup", "json", "cookie", "token", "unknown")
}

func CredentialEntry(value string) string {
	return allowed(value, "submit", "update", "deactivate")
}

func CredentialHelperCode(value string) string {
	switch value {
	case "flow_not_found", "session_expired", "flow_busy", "session_busy", "credential_busy", "too_many_sessions", "upstream_rate_limited",
		"proxy_forbidden", "invalid_proxy", "proxy_required", "proxy_ip_mismatch", "proxy_mismatch", "proxy_authentication_failed",
		"invalid_code", "code_mismatch", "state_mismatch", "code_required", "authorization_declined", "invalid_request", "invalid_owner",
		"invalid_mode", "invalid_setup_kind", "invalid_document", "access_token_required", "session_key_required", "invalid_session_key",
		"upstream_timeout", "upstream_network_error", "proxy_check_failed", "upstream_rejected", "upstream_invalid_response",
		"organization_missing", "authorization_response_invalid", "invalid_token_response", "invalid_access_token", "invalid_refresh_token",
		"invalid_token", "invalid_credentials", "unauthorized", "authorization_failed", "not_found", "internal_error":
		return value
	}
	return "unknown"
}

type CredentialAuthEvent struct {
	Entry, Operation, Method, Code, HelperCode string
	Stage, UpstreamCode, ResponseKind          string
	HelperStatus, UpstreamStatus               int
	DurationMS                                 int64
}

func credentialHTTPStatus(value int) int {
	if value >= 100 && value <= 599 {
		return value
	}
	return 0
}

func credentialPublicCode(value string) string {
	return allowed(value, "CREDENTIAL_FLOW_NOT_FOUND", "CREDENTIAL_FLOW_BUSY", "CREDENTIAL_RATE_LIMITED", "INVALID_PROXY", "PROXY_CHANGED",
		"INVALID_AUTHORIZATION_CODE", "INVALID_REQUEST", "AUTHORIZATION_CONNECTION_FAILED", "AUTHORIZATION_FAILED", "AUTHORIZATION_REJECTED",
		"AUTHORIZATION_ORGANIZATION_MISSING", "AUTHORIZATION_INVALID_RESPONSE", "INVALID_CREDENTIAL", "CREDENTIAL_HELPER_UNAVAILABLE",
		"CREDENTIAL_HELPER_INVALID_RESPONSE", "PROXY_TEST_UNAVAILABLE", "PROXY_TEST_TIMEOUT", "PROXY_AUTHENTICATION_FAILED",
		"PROXY_TEST_RATE_LIMITED", "PROXY_CONNECTION_FAILED")
}

// LogCredentialAuth emits exactly one safe event for each failed helper call.
// The same injected logger and server-generated trace used by supplier
// diagnostics make the event testable and correlate it with the API response.
func LogCredentialAuth(ctx context.Context, e CredentialAuthEvent) {
	emit(ctx, "TK_CREDENTIAL_AUTH", struct {
		TraceID        string `json:"trace_id,omitempty"`
		Entry          string `json:"entry"`
		Operation      string `json:"operation"`
		Method         string `json:"method"`
		Outcome        string `json:"outcome"`
		Code           string `json:"code"`
		HelperCode     string `json:"helper_code"`
		Stage          string `json:"stage,omitempty"`
		HelperStatus   int    `json:"helper_http_status,omitempty"`
		UpstreamStatus int    `json:"upstream_http_status,omitempty"`
		UpstreamCode   string `json:"upstream_code,omitempty"`
		ResponseKind   string `json:"response_kind,omitempty"`
		DurationMS     int64  `json:"duration_ms"`
	}{boundedID(TraceFrom(ctx).TraceID), CredentialEntry(e.Entry), allowed(e.Operation, "start", "import", "exchange", "verify", "proxy_test"),
		CredentialMethod(e.Method), "failed", credentialPublicCode(e.Code), CredentialHelperCode(e.HelperCode), CredentialStage(e.Stage),
		credentialHTTPStatus(e.HelperStatus), credentialHTTPStatus(e.UpstreamStatus), CredentialUpstreamCode(e.UpstreamCode), CredentialResponseKind(e.ResponseKind), max(int64(0), e.DurationMS)})
}
