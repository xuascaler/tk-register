package main

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"goapi-tk-register/server/common"
	"goapi-tk-register/server/internal/core"
	"goapi-tk-register/server/internal/supplier"
)

type accountAuthEditor interface {
	CheckAuthJSON(context.Context, string, string, supplier.Target) (supplier.EditInfo, error)
	UpdateAuthJSON(context.Context, string, string, supplier.Target, json.RawMessage) (supplier.EditInfo, error)
	CollectAccount(context.Context, string, string, supplier.Target) (supplier.Account, error)
}

func (a *app) authEditor() accountAuthEditor {
	if a.accountAuthEditor != nil {
		return a.accountAuthEditor
	}
	if a.supplierClient != nil {
		return a.supplierClient
	}
	return supplier.NewClient()
}

type authJSONProblem struct {
	status        int
	code, message string
}

func authJSONError(err error) authJSONProblem {
	if problem, ok := accountCredentialProblem(err); ok {
		return problem
	}
	if errors.Is(err, core.ErrAccountOwnershipConflict) {
		return authJSONProblem{409, "ACCOUNT_OWNERSHIP_CONFLICT", "This account has conflicting ownership records. An administrator must resolve them before its credentials can be updated."}
	}
	var upstream *supplier.Error
	if errors.As(err, &upstream) {
		switch upstream.Code {
		case supplier.CodeAccountNotFound:
			return authJSONProblem{404, "AUTH_JSON_ACCOUNT_NOT_FOUND", "This account was not found on the supplier website."}
		case supplier.CodeAccountAmbiguous:
			return authJSONProblem{409, "AUTH_JSON_ACCOUNT_AMBIGUOUS", "More than one supplier record matches this email. An administrator must check the account identity."}
		case supplier.CodeNotEditable:
			return authJSONProblem{409, "AUTH_JSON_NOT_EDITABLE", "The supplier does not allow Auth JSON updates for this account's current status."}
		case supplier.CodeUpdateRejected:
			return authJSONProblem{502, "AUTH_JSON_UPDATE_REJECTED", "The supplier rejected the credential update. Your account data has been retained."}
		case supplier.CodeUpdateUnconfirmed:
			return authJSONProblem{504, "AUTH_JSON_UPDATE_UNCONFIRMED", "We could not confirm whether the update completed. Check the account before trying again."}
		case supplier.CodeInvalidAuthJSON:
			return authJSONProblem{400, "INVALID_AUTH_JSON", "Please check the Auth JSON format and required fields."}
		case supplier.CodeCredentialsInvalid, supplier.CodeSessionExpired:
			return authJSONProblem{502, "AUTH_JSON_LOGIN_FAILED", "The supplier website login failed. Ask an administrator to check Account sync settings."}
		}
	}
	// No arbitrary error body or exception is returned or recorded.
	return authJSONProblem{502, "AUTH_JSON_UNAVAILABLE", "The supplier website could not be reached or returned an unsupported response. No update was confirmed."}
}
func authJSONFail(w http.ResponseWriter, p authJSONProblem) { fail(w, p.status, p.code, p.message) }

// Both the cookie-authenticated editor and the personal-token API use the same
// validation, locks, vendor write and audit. The API
// adapter supplies parsed input and a public response projection, never a fake
// session. Both responses read saved account data without an inline collection.
type authJSONOperation struct {
	Token         *core.UserAPIToken
	Input         any
	FormatAccount func(core.AccountDTO) any
	Proxy         bool
	ProxyTest     bool
}

func (a *app) accountAuthJSON(w http.ResponseWriter, r *http.Request, user *core.User, id uint64, admin bool, lookup func(uint64) (core.AccountDTO, error)) {
	a.runAccountAuthJSON(w, r, user, id, admin, lookup, authJSONOperation{})
}

func (a *app) runAccountAuthJSON(w http.ResponseWriter, r *http.Request, user *core.User, id uint64, admin bool, lookup func(uint64) (core.AccountDTO, error), operation authJSONOperation) {
	if r.Method != "GET" && r.Method != "POST" {
		fail(w, 405, "METHOD_NOT_ALLOWED", "This method is not supported.")
		return
	}
	// The adapter has authenticated the caller. Re-read the scoped row instead
	// of trusting any client-supplied ID. Personal tokens never get admin scope.
	query := a.store.DB.WithContext(r.Context()).Where("id = ?", id)
	if !admin {
		query = query.Where("user_id = ?", user.ID)
	}
	var target core.SyncedAccount
	if err := query.First(&target).Error; err != nil {
		vendorFailure(w, err)
		return
	}
	if target.DeletedAt != nil {
		fail(w, 404, "ACCOUNT_NOT_FOUND", "The requested account was not found.")
		return
	}
	if !target.AvailableForMutation() {
		fail(w, 409, "ACCOUNT_DELETING", "This account is being deleted. Please wait.")
		return
	}
	var vendor core.Vendor
	if err := a.store.DB.WithContext(r.Context()).First(&vendor, target.VendorID).Error; err != nil {
		internal(w)
		return
	}
	if target.CurrentSubmissionID == nil {
		fail(w, 409, "ACCOUNT_NOT_ACCEPTED", "Only an accepted account can have its credentials updated.")
		return
	}
	if !vendor.Enabled || vendor.Code != "tk-register-0" || vendor.BaseURL != supplier.Origin || !strings.HasPrefix(target.Plan, "claude/") {
		fail(w, 409, "AUTH_JSON_UNSUPPORTED", "Auth JSON updates are unavailable for this account or vendor.")
		return
	}
	if err := a.store.CheckAuthJSONOwnership(r.Context(), target); err != nil {
		if errors.Is(err, core.ErrAccountOwnershipConflict) {
			fail(w, 409, "ACCOUNT_OWNERSHIP_CONFLICT", "This account has conflicting ownership records. An administrator must resolve them before its credentials can be updated.")
		} else {
			internal(w)
		}
		return
	}
	var proxyInfo core.AccountProxyInfo
	if operation.Proxy {
		var err error
		proxyInfo, err = a.store.AccountProxyInfo(r.Context(), target)
		if err != nil {
			authJSONFail(w, authJSONError(err))
			return
		}
	}
	diagnosticSource := "auth_json_check"
	if r.Method == "POST" {
		diagnosticSource = "auth_json_update"
	}
	r = r.WithContext(accountSyncTrace(r.Context(), diagnosticSource, user.ID, target.ID))
	values, err := a.store.Settings(r.Context())
	if err != nil {
		internal(w)
		return
	}
	username, password := values.String("account_sync_website_username"), values.String("account_sync_website_password")
	if username == "" || password == "" {
		fail(w, 503, "SYNC_NOT_CONFIGURED", "Website login has not been configured by an administrator.")
		return
	}
	limit := 300
	if r.Method == "POST" {
		limit = 100
	}
	retry, err := loginAttempt.Run(r.Context(), a.redis, []string{redisPrefix + "account-auth-json:rate:" + r.Method + ":" + strconv.FormatUint(user.ID, 10)}, limit, 60).Int()
	if err != nil {
		internal(w)
		return
	}
	if retry > 0 {
		syncDiagnosticGate(r.Context(), "local_rate_limit", limit, 60, retry)
		rateFailure(w, retry)
		return
	}
	selection := supplier.Target{Account: target.Account}
	if target.SupplierRecordID != nil {
		selection.RecordID = *target.SupplierRecordID
	}
	editor := a.authEditor()
	if r.Method == "GET" || operation.ProxyTest {
		ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
		defer cancel()
		info, err := editor.CheckAuthJSON(ctx, username, password, selection)
		if err != nil {
			if supplierWaitFailure(w, err) {
				return
			}
			authJSONFail(w, authJSONError(err))
			return
		}
		if !strings.EqualFold(strings.TrimSpace(info.Account), strings.TrimSpace(target.Account)) {
			authJSONFail(w, authJSONError(errors.New("identity mismatch")))
			return
		}
		if err = a.store.ConfirmSupplierRecord(ctx, target.ID, target.IdentityKey, info.RecordID); err != nil {
			fail(w, 409, "ACCOUNT_CHANGED", "This account changed while it was being checked. Reload the page.")
			return
		}
		if operation.ProxyTest {
			if !info.Editable {
				authJSONFail(w, authJSONError(&supplier.Error{Code: supplier.CodeNotEditable}))
				return
			}
			a.testAccountProxy(w, r, user, target, proxyInfo)
			return
		}
		response := map[string]any{"account": info.Account, "plan": info.Plan, "editable": info.Editable, "reason": info.Reason, "supplier_record_id": info.RecordID, "updated_at": target.AuthJSONUpdatedAt}
		if operation.Proxy {
			response["revision"], response["proxy_url_masked"], response["updated_at"] = proxyInfo.Revision, proxyInfo.ProxyURLMasked, proxyInfo.UpdatedAt
		}
		ok(w, response)
		return
	}
	contentType, _, parseErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if parseErr != nil || contentType != "application/json" {
		fail(w, 415, "INVALID_CONTENT_TYPE", "Use application/json.")
		return
	}
	inputAuthJSON := operation.Input
	var resolved *resolvedAuthJSONFlow
	if !operation.Proxy && operation.Token == nil && operation.Input == nil {
		var input struct {
			AuthJSON json.RawMessage `json:"auth_json"`
			FlowID   json.RawMessage `json:"flow_id"`
		}
		if err = common.DecodeJson(http.MaxBytesReader(w, r.Body, 3<<20), &input); err != nil {
			fail(w, 400, "INVALID_REQUEST", "Provide auth_json as a JSON object or JSON string.")
			return
		}
		if (len(input.AuthJSON) > 0) == (len(input.FlowID) > 0) {
			fail(w, 400, "INVALID_REQUEST", "Provide either auth_json or flow_id, not both.")
			return
		}
		if len(input.FlowID) > 0 {
			var flowID string
			if json.Unmarshal(input.FlowID, &flowID) != nil || !credentialFlowID.MatchString(flowID) {
				vendorFailure(w, credentialInvalid())
				return
			}
			resolved, err = a.resolveAuthJSONFlow(r.Context(), user, target, admin, flowID)
			if err != nil {
				vendorFailure(w, err)
				return
			}
			defer resolved.release()
			if resolved.flow.Status == "updated" && resolved.flow.UpdateResult != nil {
				if auditID, valid := resolved.flow.UpdateResult["audit_id"].(string); valid {
					w.Header().Set("X-Auth-JSON-Audit-ID", auditID)
				}
				ok(w, resolved.flow.UpdateResult)
				return
			}
			inputAuthJSON = resolved.flow.Auth
		} else if json.Unmarshal(input.AuthJSON, &inputAuthJSON) != nil {
			fail(w, 400, "INVALID_REQUEST", "Provide auth_json as a JSON object or JSON string.")
			return
		}
	}
	var raw []byte
	var expectedRevision *string
	if operation.Proxy {
		raw, expectedRevision, err = a.resolveAccountProxy(r, w, user, target)
		if err != nil {
			proxyFailure(w, err)
			return
		}
	} else {
		normalized, validationErr := core.ValidateClaudeAccountAuthJSON(inputAuthJSON, target.Account)
		if validationErr != nil {
			vendorFailure(w, validationErr)
			return
		}
		raw, err = common.Marshal(normalized)
		if err != nil {
			internal(w)
			return
		}
	}
	// The lock is keyed by vendor+normalized email, not owner, since two local
	// histories can refer to the same supplier row. A global collection lease
	// also prevents a pre-update collection overwriting the credential update.
	lockKey := redisPrefix + "account-auth-json:lease:" + digest(strconv.FormatUint(target.VendorID, 10)+":"+strings.ToLower(strings.TrimSpace(target.Account)))
	token, err := randomToken()
	if err != nil {
		internal(w)
		return
	}
	locked, err := a.redis.SetNX(r.Context(), lockKey, token, 90*time.Second).Result()
	if err != nil {
		internal(w)
		return
	}
	if !locked {
		syncDiagnosticGate(r.Context(), "sync_lock_busy", 0, 0, 0)
		fail(w, 409, "AUTH_JSON_UPDATE_RUNNING", "An update for this account is already running. Please wait.")
		return
	}
	release := func(key string) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = releaseSend.Run(ctx, a.redis, []string{key}, token).Err()
	}
	defer release(lockKey)
	locked, err = a.redis.SetNX(r.Context(), accountSyncLeaseKey, token, 90*time.Second).Result()
	if err != nil {
		internal(w)
		return
	}
	if !locked {
		syncDiagnosticGate(r.Context(), "sync_lock_busy", 0, 0, 0)
		fail(w, 409, "SYNC_RUNNING", "An account sync is running. Please try again shortly.")
		return
	}
	defer release(accountSyncLeaseKey)
	// After authorization and lock acquisition, finish the bounded mutation and
	// persist its outcome even when the browser navigates away or disconnects.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 55*time.Second)
	defer cancel()
	source := "web"
	var tokenID, tokenRevision *uint64
	if operation.Token != nil {
		source = "api"
		tokenID, tokenRevision = &operation.Token.ID, &operation.Token.Revision
	}
	method, flowID := "manual", ""
	if operation.Proxy {
		method = "proxy"
	}
	if resolved != nil {
		method, flowID = resolved.flow.Method, resolved.flow.FlowID
	}
	audit, err := a.store.BeginAuthJSONUpdateWithMethod(ctx, target, user.ID, w.Header().Get("X-Request-ID"), source, tokenID, tokenRevision, method, flowID, core.AuthJSONCredentialWrite{Raw: raw, ExpectedRevision: expectedRevision})
	if err != nil {
		if problem, known := accountCredentialProblem(err); known {
			authJSONFail(w, problem)
		} else if errors.Is(err, core.ErrAccountIdentityChanged) {
			fail(w, 409, "ACCOUNT_CHANGED", "This account changed. Reload the page before updating.")
		} else if errors.Is(err, core.ErrInvalidAPIToken) {
			bearerFailure(w)
		} else if errors.Is(err, core.ErrAPITokenScope) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="tk-register", error="insufficient_scope", scope="`+core.ClaudeAuthJSONUpdateScope+`"`)
			fail(w, 403, "INSUFFICIENT_SCOPE", "This API token does not have the required permission.")
		} else if errors.Is(err, core.ErrAccountOwnershipConflict) {
			fail(w, 409, "ACCOUNT_OWNERSHIP_CONFLICT", "This account has conflicting ownership records. An administrator must resolve them before its credentials can be updated.")
		} else {
			internal(w)
		}
		return
	}
	auditID := strconv.FormatUint(audit.ID, 10)
	w.Header().Set("X-Auth-JSON-Audit-ID", auditID)
	// A newly resolved website ID can conflict even when no ID was saved on
	// this local row. Recheck after resolution and before the supplier PUT.
	selection.BeforeUpdate = func(checkCtx context.Context, info supplier.EditInfo) error {
		if !strings.EqualFold(strings.TrimSpace(info.Account), strings.TrimSpace(target.Account)) {
			return errors.New("supplier account identity mismatch")
		}
		confirmed := target
		confirmed.SupplierRecordID = &info.RecordID
		if err := a.store.CheckAuthJSONOwnership(checkCtx, confirmed); err != nil {
			return err
		}
		if err := a.store.CheckPendingCredentialUpdate(checkCtx, target, audit.ID); err != nil {
			return err
		}
		if resolved != nil {
			return resolved.beforeWrite(checkCtx, a, user)
		}
		return nil
	}
	updateCtx, updateCancel := context.WithTimeout(ctx, 35*time.Second)
	info, updateErr := editor.UpdateAuthJSON(updateCtx, username, password, selection, json.RawMessage(raw))
	updateCancel()
	at := time.Now().UTC()
	status, code := "updated", ""
	problem := authJSONProblem{}
	if updateErr != nil {
		problem = authJSONError(updateErr)
		if seconds, _, _, _ := supplier.RetryDetails(updateErr); seconds > 0 {
			problem = authJSONProblem{429, "SUPPLIER_RATE_LIMITED", supplierWaitMessage(updateErr)}
		}
		code = problem.code
		status = "failed"
		if code == "AUTH_JSON_UPDATE_UNCONFIRMED" {
			status = "unknown"
		} else if code == "AUTH_JSON_UPDATE_REJECTED" || code == "AUTH_JSON_NOT_EDITABLE" || code == "ACCOUNT_OWNERSHIP_CONFLICT" {
			status = "rejected"
		}
	}
	saveCtx, saveCancel := context.WithTimeout(context.Background(), 5*time.Second)
	unchanged, persistErr := a.store.FinishAuthJSONUpdate(saveCtx, audit, status, info.RecordID, code, at)
	saveCancel()
	if updateErr != nil {
		if resolved != nil {
			resolved.finish(ctx, a, status, nil)
		}
		if supplierWaitFailure(w, updateErr) {
			return
		}
		authJSONFail(w, problem)
		return
	}
	message := "Auth JSON updated. Account status is updated by scheduled background checks."
	if persistErr != nil {
		message = "Auth JSON updated, but local metadata could not be saved. Reload the page to check the account."
	} else if !unchanged {
		message = "Auth JSON updated, but this local account changed during the request. Reload the page to view its current data."
	}
	// Read saved state only. Credential updates never enqueue or perform an
	// additional status/usage collection; the normal background process owns it.
	account, lookupErr := lookup(id)
	var result any
	if lookupErr == nil {
		result = account
		if operation.FormatAccount != nil {
			result = operation.FormatAccount(account)
		}
	} else {
		message = "Auth JSON updated, but saved account data could not be loaded. Reload the page."
	}
	response := map[string]any{"updated": true, "audit_id": auditID, "refresh_status": "not_requested", "refresh_message": message, "account": result, "updated_at": at}
	if resolved != nil {
		resolved.finish(ctx, a, "updated", response)
	}
	ok(w, response)
}
