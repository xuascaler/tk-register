package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"goapi-tk-register/server/internal/core"
	"goapi-tk-register/server/internal/supplier"
)

func authFlowRoute(id uint64, action string) string { return authRoute(id) + "/flows/" + action }

func authFlowHelper(t *testing.T, owner uint64, email string, calls *atomic.Int32) {
	t.Helper()
	credentialHelperFixture(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		calls.Add(1)
		ready := r.URL.Path == "/exchange" || r.URL.Path == "/import"
		reply := credentialReply(owner, ready)
		if reply.Auth != nil {
			reply.Auth["email"] = email
		}
		_ = json.NewEncoder(w).Encode(reply)
	})
}

func TestAuthJSONFlowBoundToTargetCannotSubmitOrReplay(t *testing.T) {
	a, owner, cookie, csrf, target, editor := setupAuthUpdate(t)
	_, otherCookie, _ := vendorSession(t, a, "unrelated@example.com", "member")
	var calls atomic.Int32
	authFlowHelper(t, owner.ID, target.Account, &calls)
	start := map[string]any{"proxy_url": testCredentialProxy, "mode": "oauth"}
	requireStatus(t, request(t, a, "POST", authFlowRoute(target.ID, "start"), start, nil, ""), 401)
	requireStatus(t, request(t, a, "POST", authFlowRoute(target.ID, "start"), start, otherCookie, csrf), 403)
	requireStatus(t, request(t, a, "GET", authFlowRoute(target.ID, testCredentialFlowID), nil, otherCookie, ""), 404)
	if calls.Load() != 0 {
		t.Fatal("unauthorized request reached helper")
	}
	requireStatus(t, request(t, a, "POST", authFlowRoute(target.ID, "start"), start, cookie, csrf), 200)
	for _, route := range []string{"credential-flows/" + testCredentialFlowID, "credential-flows/export", "credential-flows/submit"} {
		method := "POST"
		var body any = map[string]any{"flow_id": testCredentialFlowID}
		if strings.HasSuffix(route, testCredentialFlowID) {
			method = "GET"
			body = nil
		}
		requireStatus(t, request(t, a, method, route, body, cookie, csrf), 404)
	}
	requireStatus(t, request(t, a, "POST", authFlowRoute(target.ID, "exchange"), map[string]any{"flow_id": testCredentialFlowID, "code": "private-code"}, cookie, csrf), 200)
	beforeCalls := calls.Load()
	w := request(t, a, "POST", authRoute(target.ID), map[string]any{"flow_id": testCredentialFlowID}, cookie, csrf)
	requireStatus(t, w, 200)
	if data(t, w)["updated"] != true || data(t, w)["refresh_status"] != "not_requested" || editor.writes != 1 || editor.refreshes != 0 {
		t.Fatal(w.Body.String(), editor.writes, editor.refreshes)
	}
	assertNoSecrets(t, w.Body.String())
	rows := authAudits(t, a)
	if len(rows) != 1 || rows[0].Method != "oauth" || rows[0].CredentialFlowID != testCredentialFlowID || rows[0].RefreshStatus != "not_requested" {
		t.Fatal(rows)
	}
	// A lost browser response can be retried safely: return the saved outcome,
	// not another supplier write, audit, collection, or helper verification.
	w = request(t, a, "POST", authRoute(target.ID), map[string]any{"flow_id": testCredentialFlowID}, cookie, csrf)
	requireStatus(t, w, 200)
	if w.Header().Get("X-Auth-JSON-Audit-ID") != fmt.Sprint(data(t, w)["audit_id"]) {
		t.Fatal("replayed outcome lost its audit identifier")
	}
	if editor.writes != 1 || calls.Load() != beforeCalls+1 || len(authAudits(t, a)) != 1 {
		t.Fatal("successful update replayed")
	}
	requireStatus(t, request(t, a, "POST", authFlowRoute(target.ID, "exchange"), map[string]any{"flow_id": testCredentialFlowID, "code": "private-code"}, cookie, csrf), 409)
	if calls.Load() != beforeCalls+1 {
		t.Fatal("exchange reset a consumed update")
	}
	requireStatus(t, request(t, a, "POST", authFlowRoute(target.ID, testCredentialFlowID+"/draft"), map[string]any{"auth_patch": map[string]any{"id_token": ""}}, cookie, csrf), 409)
	// The helper caches imports. Receiving the same helper ID must not replace
	// the terminal Redis draft with fresh ready credentials.
	requireStatus(t, request(t, a, "POST", authFlowRoute(target.ID, "import"), map[string]any{"proxy_url": testCredentialProxy, "kind": "json", "document": authInput()["auth_json"]}, cookie, csrf), 409)
	w = request(t, a, "GET", authFlowRoute(target.ID, testCredentialFlowID), nil, cookie, "")
	requireStatus(t, w, 200)
	if data(t, w)["status"] != "updated" {
		t.Fatal("terminal flow was reopened", w.Body.String())
	}
	var jobs, submissions int64
	a.store.DB.Model(&core.PostSubmissionSyncJob{}).Count(&jobs)
	a.store.DB.Model(&core.Submission{}).Count(&submissions)
	if jobs != 0 || submissions != 1 {
		t.Fatal("update created submission or collection", jobs, submissions)
	}
}

func TestAuthJSONFlowWrongEmailAndBindingChangesNeverWrite(t *testing.T) {
	for _, scenario := range []string{"email", "identity", "owner", "deleting", "revoked", "another-account"} {
		t.Run(scenario, func(t *testing.T) {
			a, owner, cookie, csrf, target, editor := setupAuthUpdate(t)
			var calls atomic.Int32
			email := target.Account
			if scenario == "email" {
				email = "another@example.com"
			}
			authFlowHelper(t, owner.ID, email, &calls)
			requireStatus(t, request(t, a, "POST", authFlowRoute(target.ID, "start"), map[string]any{"proxy_url": testCredentialProxy}, cookie, csrf), 200)
			requireStatus(t, request(t, a, "POST", authFlowRoute(target.ID, "exchange"), map[string]any{"flow_id": testCredentialFlowID, "code": "private-code"}, cookie, csrf), 200)
			route := authRoute(target.ID)
			switch scenario {
			case "identity":
				a.store.DB.Model(&core.SyncedAccount{}).Where("id = ?", target.ID).Update("identity_key", "changed-lifetime")
			case "owner":
				other, _, _ := vendorSession(t, a, "new-owner@example.com", "member")
				a.store.DB.Model(&core.SyncedAccount{}).Where("id = ?", target.ID).Update("user_id", other.ID)
			case "deleting":
				a.store.DB.Model(&core.SyncedAccount{}).Where("id = ?", target.ID).Update("deletion_status", "pending")
			case "revoked":
				a.store.DB.Model(&core.User{}).Where("id = ?", owner.ID).Update("auth_version", owner.AuthVersion+1)
			case "another-account":
				vendor, err := a.store.VendorDetails(context.Background(), target.VendorID)
				if err != nil {
					t.Fatal(err)
				}
				seedAdminSubmission(t, a, owner.ID, vendor, "different@example.com", "accepted", time.Now().UTC())
				if err := a.store.EnsureSyncedAccounts(context.Background()); err != nil {
					t.Fatal(err)
				}
				var other core.SyncedAccount
				if err := a.store.DB.Where("account = ?", "different@example.com").First(&other).Error; err != nil {
					t.Fatal(err)
				}
				route = authRoute(other.ID)
			}
			w := request(t, a, "POST", route, map[string]any{"flow_id": testCredentialFlowID}, cookie, csrf)
			if w.Code < 400 || editor.writes != 0 || len(authAudits(t, a)) != 0 {
				t.Fatal("invalid binding reached write", w.Code, w.Body.String(), editor.writes)
			}
			assertNoSecrets(t, w.Body.String())
		})
	}
}

func TestAuthJSONFlowUnknownOutcomeCannotBeResetOrRetried(t *testing.T) {
	a, owner, cookie, csrf, target, editor := setupAuthUpdate(t)
	var calls atomic.Int32
	authFlowHelper(t, owner.ID, target.Account, &calls)
	requireStatus(t, request(t, a, "POST", authFlowRoute(target.ID, "start"), map[string]any{"proxy_url": testCredentialProxy}, cookie, csrf), 200)
	requireStatus(t, request(t, a, "POST", authFlowRoute(target.ID, "exchange"), map[string]any{"flow_id": testCredentialFlowID, "code": "private-code"}, cookie, csrf), 200)
	editor.updateErr = &supplier.Error{Code: supplier.CodeUpdateUnconfirmed}
	requireStatus(t, request(t, a, "POST", authRoute(target.ID), map[string]any{"flow_id": testCredentialFlowID}, cookie, csrf), 504)
	requireStatus(t, request(t, a, "POST", authFlowRoute(target.ID, "exchange"), map[string]any{"flow_id": testCredentialFlowID, "code": "private-code"}, cookie, csrf), 409)
	requireStatus(t, request(t, a, "POST", authRoute(target.ID), map[string]any{"flow_id": testCredentialFlowID}, cookie, csrf), 409)
	if editor.writes != 1 || editor.refreshes != 0 {
		t.Fatal("unknown result replayed", editor.writes, editor.refreshes)
	}
}

func TestAuthJSONFlowAdminImportAndManualInputExclusivity(t *testing.T) {
	a, _, _, _, target, editor := setupAuthUpdate(t)
	admin, cookie, csrf := vendorSession(t, a, "flow-admin@example.com", "admin")
	var calls atomic.Int32
	authFlowHelper(t, admin.ID, target.Account, &calls)
	base := fmt.Sprintf("admin/synced-accounts/%d/auth-json", target.ID)
	requireStatus(t, request(t, a, "POST", base+"/flows/import", map[string]any{"proxy_url": testCredentialProxy, "kind": "json", "document": authInput()["auth_json"]}, cookie, csrf), 200)
	input := map[string]any{"flow_id": testCredentialFlowID, "auth_json": authInput()["auth_json"]}
	requireStatus(t, request(t, a, "POST", base, input, cookie, csrf), 400)
	if editor.writes != 0 {
		t.Fatal("ambiguous body wrote credentials")
	}
	w := request(t, a, "POST", base, map[string]any{"flow_id": testCredentialFlowID}, cookie, csrf)
	requireStatus(t, w, 200)
	rows := authAudits(t, a)
	if len(rows) != 1 || rows[0].ActorID != admin.ID || rows[0].OwnerID != target.UserID || rows[0].Method != "json" {
		t.Fatal(rows)
	}
}
