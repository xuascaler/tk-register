package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"goapi-tk-register/server/internal/core"
)

// A Deactivate authorization generates credentials for the existing account.
// Its independent purpose never permits supplier submission or credential PUT.
type deactivateFlowContextKey struct{}

func (a *app) authorizeDeactivateFlowTarget(ctx context.Context, user *core.User, flow credentialFlowRecord) error {
	binding, scoped := ctx.Value(deactivateFlowContextKey{}).(authJSONFlowTarget)
	if !scoped || flow.UpdateTarget != nil || !sameAuthJSONFlowTarget(flow.DeactivateTarget, &binding) {
		return credentialMissing()
	}
	if _, updateScope := ctx.Value(authJSONFlowContextKey{}).(authJSONFlowTarget); updateScope {
		return credentialMissing()
	}
	_, err := a.validateAuthJSONFlowTarget(ctx, user, binding)
	return err
}

func (a *app) accountDeactivate(w http.ResponseWriter, r *http.Request, user *core.User, id uint64, admin bool, action string) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		fail(w, 405, "METHOD_NOT_ALLOWED", "This method is not supported.")
		return
	}
	var initial core.SyncedAccount
	query := a.store.DB.WithContext(r.Context()).Where("id = ?", id)
	if !admin {
		query = query.Where("user_id = ?", user.ID)
	}
	if err := query.First(&initial).Error; err != nil {
		vendorFailure(w, core.ErrNotFound)
		return
	}
	binding := authJSONTarget(initial, admin)
	target, err := a.validateAuthJSONFlowTarget(r.Context(), user, binding)
	if err != nil {
		vendorFailure(w, err)
		return
	}
	if action == "" {
		if r.Method != http.MethodGet {
			fail(w, 405, "METHOD_NOT_ALLOWED", "Use the account authorization flow to generate Auth JSON.")
			return
		}
		ok(w, map[string]any{"available": true, "account": target.Account, "plan": target.Plan})
		return
	}
	parts := strings.Split(action, "/")
	if r.Method == http.MethodPost && len(parts) == 2 && parts[1] == "export" {
		if !a.credentialRate(w, r, user.ID, "deactivate-export", 30) {
			return
		}
		var input struct{}
		if !decodeCredential(w, r, &input) {
			return
		}
		ctx, cancel := context.WithTimeout(context.WithValue(r.Context(), deactivateFlowContextKey{}, binding), 90*time.Second)
		defer cancel()
		a.exportAccountDeactivate(w, r.WithContext(ctx), user, binding, parts[0])
		return
	}
	if r.Method == http.MethodGet {
		if len(parts) != 1 || !credentialFlowID.MatchString(parts[0]) {
			fail(w, 404, "NOT_FOUND", "This endpoint does not exist.")
			return
		}
	} else if action != "start" && action != "import" && action != "exchange" && action != "proxy-test" && !(len(parts) == 2 && parts[1] == "draft") {
		fail(w, 404, "NOT_FOUND", "This endpoint does not exist.")
		return
	}
	// No website login or upstream status/editability check belongs here:
	// Active accounts are eligible, and no supplier mutation will follow.
	r = r.WithContext(context.WithValue(r.Context(), deactivateFlowContextKey{}, binding))
	a.credentialFlowRoutes(w, r, "credential-flows/"+action)
}

func (a *app) exportAccountDeactivate(w http.ResponseWriter, r *http.Request, user *core.User, binding authJSONFlowTarget, id string) {
	flow, previous, release, err := a.lockCredentialFlow(r.Context(), user, id)
	if err != nil {
		vendorFailure(w, err)
		return
	}
	defer release()
	r = r.WithContext(withAuthJSONFlowMethod(r.Context(), flow.Method))
	if flow.Status != "ready" && flow.Status != "exported" || flow.Auth == nil {
		vendorFailure(w, credentialProblem(409, "CREDENTIAL_DRAFT_NOT_READY", "Finish authorization before generating Auth JSON."))
		return
	}
	if err = credentialAuthShape(flow.Auth, flow.ProxyURL); err != nil {
		vendorFailure(w, err)
		return
	}
	target, err := a.validateAuthJSONFlowTarget(r.Context(), user, binding)
	if err != nil {
		vendorFailure(w, err)
		return
	}
	if _, err = core.ValidateClaudeAccountAuthJSON(flow.Auth, target.Account); err != nil {
		vendorFailure(w, err)
		return
	}
	verified, err := a.callCredentialHelper(r.Context(), user.ID, "/verify", map[string]any{"flow_id": flow.FlowID, "proxy_url": flow.ProxyURL})
	if err != nil {
		vendorFailure(w, err)
		return
	}
	if verified.FlowID != flow.FlowID || verified.ProxyCheck.IP != flow.ProxyCheck.IP {
		vendorFailure(w, credentialProblem(409, "PROXY_CHANGED", "The proxy no longer matches this authorization. Start again with the intended proxy."))
		return
	}
	if verified.ExpiresAt.Before(flow.ExpiresAt) {
		flow.ExpiresAt = verified.ExpiresAt
	}
	if !flow.ExpiresAt.After(time.Now()) {
		vendorFailure(w, credentialMissing())
		return
	}
	// Recheck role/auth-version, owner, account lifetime, local vendor policy
	// and conflicting ownership after the potentially slow proxy verification.
	target, err = a.validateAuthJSONFlowTarget(r.Context(), user, binding)
	if err != nil {
		vendorFailure(w, err)
		return
	}
	audit, err := a.store.RecordAuthJSONGeneration(r.Context(), target, *user, binding.Admin, flow.FlowID, flow.Method, w.Header().Get("X-Request-ID"))
	if err != nil {
		vendorFailure(w, err)
		return
	}
	flow.ProxyCheck = verified.ProxyCheck
	flow.Status = "exported"
	flow.GeneratedAt = &audit.CreatedAt
	flow.AuditID = strconv.FormatUint(audit.ID, 10)
	// Persist the terminal draft before returning it. If this CAS/response is
	// lost, another export safely reuses the same metadata audit and credentials.
	if err = a.saveCredentialFlow(r.Context(), flow, previous); err != nil {
		vendorFailure(w, err)
		return
	}
	w.Header().Set("X-Credential-Generation-Audit-ID", flow.AuditID)
	ok(w, flow.credentialFlowView)
}
