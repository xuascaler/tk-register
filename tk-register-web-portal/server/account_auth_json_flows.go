package main

import (
	"context"
	"net/http"
	"strings"
	"time"

	"goapi-tk-register/server/internal/core"
	"goapi-tk-register/server/internal/supplier"
)

// This binding stays in the encrypted Redis draft, never in caller-controlled
// parameters. Actor identity/auth-version remains enforced by the flow owner.
type authJSONFlowTarget struct {
	AccountID   uint64 `json:"account_id"`
	OwnerID     uint64 `json:"owner_id"`
	VendorID    uint64 `json:"vendor_id"`
	IdentityKey string `json:"identity_key"`
	Account     string `json:"account"`
	Admin       bool   `json:"admin"`
}
type authJSONFlowContextKey struct{}
type authJSONFlowMethodKey struct{}

func withAuthJSONFlowMethod(ctx context.Context, method string) context.Context {
	return context.WithValue(ctx, authJSONFlowMethodKey{}, method)
}

func authJSONTarget(account core.SyncedAccount, admin bool) authJSONFlowTarget {
	return authJSONFlowTarget{AccountID: account.ID, OwnerID: account.UserID, VendorID: account.VendorID, IdentityKey: account.IdentityKey, Account: core.NormalizeEmail(account.Account), Admin: admin}
}

func sameAuthJSONFlowTarget(left, right *authJSONFlowTarget) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func (a *app) validateAuthJSONFlowTarget(ctx context.Context, user *core.User, binding authJSONFlowTarget) (core.SyncedAccount, error) {
	var target core.SyncedAccount
	current, err := a.store.GetUserByID(ctx, user.ID)
	if err != nil || current.Status != "active" || current.AuthVersion != user.AuthVersion || current.Role != user.Role {
		return target, credentialProblem(401, "UNAUTHENTICATED", "Please sign in again.")
	}
	if binding.Admin && current.Role != "admin" {
		return target, core.ErrNotFound
	}
	query := a.store.DB.WithContext(ctx).Where("id = ?", binding.AccountID)
	if !binding.Admin {
		query = query.Where("user_id = ?", user.ID)
	}
	if err = query.First(&target).Error; err != nil {
		return target, core.ErrNotFound
	}
	if target.DeletedAt != nil {
		return target, core.ErrNotFound
	}
	if !target.AvailableForMutation() {
		return target, credentialProblem(409, "ACCOUNT_DELETING", "This account is being deleted. Please wait.")
	}
	if target.CurrentSubmissionID == nil {
		return target, credentialProblem(409, "ACCOUNT_NOT_ACCEPTED", "Only an accepted account can have its credentials updated.")
	}
	if binding != authJSONTarget(target, binding.Admin) {
		return target, credentialProblem(409, "ACCOUNT_CHANGED", "This account changed. Reload the page before updating.")
	}
	var vendor core.Vendor
	if err = a.store.DB.WithContext(ctx).First(&vendor, target.VendorID).Error; err != nil {
		return target, err
	}
	if !vendor.Enabled || vendor.Code != "tk-register-0" || vendor.BaseURL != supplier.Origin || !strings.HasPrefix(target.Plan, "claude/") {
		return target, credentialProblem(409, "AUTH_JSON_UNSUPPORTED", "Auth JSON updates are unavailable for this account or vendor.")
	}
	if err = a.store.CheckAuthJSONOwnership(ctx, target); err != nil {
		return target, err
	}
	return target, nil
}

func (a *app) authorizeCredentialFlowTarget(ctx context.Context, user *core.User, flow credentialFlowRecord) error {
	binding, scoped := ctx.Value(authJSONFlowContextKey{}).(authJSONFlowTarget)
	if flow.UpdateTarget == nil {
		if scoped {
			return credentialMissing()
		}
		return nil
	}
	if !scoped || !sameAuthJSONFlowTarget(flow.UpdateTarget, &binding) {
		return credentialMissing()
	}
	_, err := a.validateAuthJSONFlowTarget(ctx, user, binding)
	return err
}

func (a *app) accountAuthJSONFlows(w http.ResponseWriter, r *http.Request, user *core.User, id uint64, admin bool, action string) {
	if r.Method != "GET" && r.Method != "POST" {
		fail(w, 405, "METHOD_NOT_ALLOWED", "This method is not supported.")
		return
	}
	if r.Method == "POST" && action != "start" && action != "import" && action != "exchange" && action != "proxy-test" && !(strings.HasSuffix(action, "/draft") && len(strings.Split(action, "/")) == 2) {
		fail(w, 404, "NOT_FOUND", "This endpoint does not exist.")
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
	if r.Method == "POST" && (action == "start" || action == "import") {
		if !a.credentialRate(w, r, user.ID, "auth-update-preflight", 30) {
			return
		}
		values, e := a.store.Settings(r.Context())
		if e != nil {
			internal(w)
			return
		}
		username, password := values.String("account_sync_website_username"), values.String("account_sync_website_password")
		if username == "" || password == "" {
			fail(w, 503, "SYNC_NOT_CONFIGURED", "Website login has not been configured by an administrator.")
			return
		}
		selection := supplier.Target{Account: target.Account}
		if target.SupplierRecordID != nil {
			selection.RecordID = *target.SupplierRecordID
		}
		ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
		info, e := a.authEditor().CheckAuthJSON(ctx, username, password, selection)
		cancel()
		if e != nil {
			if !supplierWaitFailure(w, e) {
				authJSONFail(w, authJSONError(e))
			}
			return
		}
		if !strings.EqualFold(strings.TrimSpace(info.Account), strings.TrimSpace(target.Account)) {
			fail(w, 409, "ACCOUNT_CHANGED", "The supplier account identity changed. Reload the page.")
			return
		}
		if !info.Editable {
			fail(w, 409, "AUTH_JSON_NOT_EDITABLE", "The supplier does not allow Auth JSON updates for this account's current status.")
			return
		}
	}
	r = r.WithContext(context.WithValue(r.Context(), authJSONFlowContextKey{}, binding))
	a.credentialFlowRoutes(w, r, "credential-flows/"+action)
}

type resolvedAuthJSONFlow struct {
	flow       credentialFlowRecord
	previous   string
	release    func()
	dispatched bool
}

func (a *app) resolveAuthJSONFlow(ctx context.Context, user *core.User, target core.SyncedAccount, admin bool, id string) (*resolvedAuthJSONFlow, error) {
	binding := authJSONTarget(target, admin)
	ctx = context.WithValue(ctx, authJSONFlowContextKey{}, binding)
	flow, previous, release, err := a.lockCredentialFlow(ctx, user, id)
	if err != nil {
		return nil, err
	}
	resolved := &resolvedAuthJSONFlow{flow: flow, previous: previous, release: release}
	if flow.Status == "updated" && flow.UpdateResult != nil {
		return resolved, nil
	}
	if flow.Status == "updating" || flow.Status == "unknown" || flow.Status == "rejected" || flow.Status == "failed" {
		release()
		return nil, credentialProblem(409, "CREDENTIAL_FLOW_UPDATE_FINISHED", "This authorization has already been used for an update. Check the account before trying again.")
	}
	if flow.Status != "ready" || flow.Auth == nil {
		release()
		return nil, credentialProblem(409, "CREDENTIAL_DRAFT_NOT_READY", "Finish authorization before updating this account.")
	}
	if _, err = core.ValidateClaudeAccountAuthJSON(flow.Auth, target.Account); err != nil {
		release()
		return nil, err
	}
	result, err := a.callCredentialHelper(ctx, user.ID, "/verify", map[string]any{"flow_id": flow.FlowID, "proxy_url": flow.ProxyURL})
	if err != nil {
		release()
		return nil, err
	}
	if result.FlowID != flow.FlowID || result.ProxyCheck.IP != flow.ProxyCheck.IP {
		release()
		return nil, credentialProblem(409, "PROXY_CHANGED", "The proxy no longer matches this authorization. Start again with the intended proxy.")
	}
	return resolved, nil
}

// Record dispatch before the real PUT. If persistence or the response is lost,
// this draft stays consumed instead of risking an automatic credential replay.
func (f *resolvedAuthJSONFlow) beforeWrite(ctx context.Context, a *app, user *core.User) error {
	if _, err := a.validateAuthJSONFlowTarget(ctx, user, *f.flow.UpdateTarget); err != nil {
		return err
	}
	f.flow.Status = "updating"
	if err := a.saveCredentialFlow(ctx, f.flow, f.previous); err != nil {
		return err
	}
	bindingCtx := context.WithValue(ctx, authJSONFlowContextKey{}, *f.flow.UpdateTarget)
	_, previous, err := a.loadCredentialFlow(bindingCtx, user, f.flow.FlowID)
	if err != nil {
		return err
	}
	f.previous = previous
	f.dispatched = true
	return nil
}

func (f *resolvedAuthJSONFlow) finish(ctx context.Context, a *app, status string, result map[string]any) {
	if !f.dispatched {
		return
	}
	f.flow.Status = status
	f.flow.UpdateResult = result
	_ = a.saveCredentialFlow(ctx, f.flow, f.previous)
}
