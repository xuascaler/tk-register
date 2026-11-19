package main

import (
	"context"
	"encoding/json"
	"errors"

	"goapi-tk-register/server/internal/core"
	"goapi-tk-register/server/internal/diagnostics"
)

// This is an authorization-specific wrapper: supplier errors retain their
// existing contract and errors.As still recognizes the underlying VendorError.
type credentialAuthorizationError struct {
	problem     *core.VendorError
	diagnostics credentialAuthorizationDiagnostics
}

type credentialAuthorizationDiagnostics struct {
	RequestID      string `json:"request_id"`
	Stage          string `json:"stage,omitempty"`
	UpstreamStatus int    `json:"upstream_status,omitempty"`
	UpstreamCode   string `json:"upstream_code,omitempty"`
	ResponseKind   string `json:"response_kind,omitempty"`
}

func (e *credentialAuthorizationError) Error() string { return e.problem.Error() }
func (e *credentialAuthorizationError) Unwrap() error { return e.problem }

func (d credentialAuthorizationDiagnostics) safe() credentialAuthorizationDiagnostics {
	d.RequestID = safeDiagnosticRequestID(d.RequestID)
	d.Stage = diagnostics.CredentialStage(d.Stage)
	if d.UpstreamStatus < 100 || d.UpstreamStatus > 599 {
		d.UpstreamStatus = 0
	}
	d.UpstreamCode = diagnostics.CredentialUpstreamCode(d.UpstreamCode)
	d.ResponseKind = diagnostics.CredentialResponseKind(d.ResponseKind)
	return d
}

// An empty/malformed field never invalidates other safe fields, and no helper
// message or arbitrary error string is decoded or exposed.
func credentialHelperDiagnostic(data []byte) (string, credentialAuthorizationDiagnostics) {
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &envelope) != nil || json.Unmarshal(envelope.Error, &fields) != nil {
		return "unknown", credentialAuthorizationDiagnostics{}
	}
	var code string
	var d credentialAuthorizationDiagnostics
	_ = json.Unmarshal(fields["code"], &code)
	_ = json.Unmarshal(fields["stage"], &d.Stage)
	_ = json.Unmarshal(fields["upstream_status"], &d.UpstreamStatus)
	_ = json.Unmarshal(fields["upstream_code"], &d.UpstreamCode)
	_ = json.Unmarshal(fields["response_kind"], &d.ResponseKind)
	return diagnostics.CredentialHelperCode(code), d.safe()
}

func credentialCallEntry(ctx context.Context) string {
	if _, scoped := ctx.Value(deactivateFlowContextKey{}).(authJSONFlowTarget); scoped {
		return "deactivate"
	}
	if _, scoped := ctx.Value(authJSONFlowContextKey{}).(authJSONFlowTarget); scoped {
		return "update"
	}
	return "submit"
}

func credentialCallMethod(ctx context.Context, endpoint string, payload map[string]any) string {
	method, _ := ctx.Value(authJSONFlowMethodKey{}).(string)
	if endpoint == "/start" {
		method, _ = payload["mode"].(string)
	} else if endpoint == "/import" {
		method, _ = payload["kind"].(string)
	}
	return diagnostics.CredentialMethod(method)
}

func credentialCallOperation(endpoint string) string {
	switch endpoint {
	case "/start":
		return "start"
	case "/import":
		return "import"
	case "/exchange":
		return "exchange"
	case "/verify":
		return "verify"
	case "/proxy-test":
		return "proxy_test"
	}
	return "unknown"
}

func credentialErrorProblem(err error) *core.VendorError {
	var problem *core.VendorError
	if errors.As(err, &problem) {
		return problem
	}
	return &core.VendorError{Status: 503, Code: "CREDENTIAL_HELPER_UNAVAILABLE", Message: "Authorization is temporarily unavailable. Please try again later."}
}
