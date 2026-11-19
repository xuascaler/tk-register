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

	"goapi-tk-register/server/common"
	"goapi-tk-register/server/internal/core"
)

func deactivateRoute(id uint64, action string) string {
	base := fmt.Sprintf("accounts/%d/deactivate", id)
	if action != "" {
		return base + "/flows/" + action
	}
	return base
}

func generationAudits(t *testing.T, a *app) []core.AccountAuthJSONGenerationAudit {
	t.Helper()
	var rows []core.AccountAuthJSONGenerationAudit
	if err := a.store.DB.Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	encoded, err := common.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecrets(t, string(encoded))
	return rows
}

func TestDeactivateMethodsGenerateWithoutAnySupplierWriteOrAccountMutation(t *testing.T) {
	for _, method := range []string{"oauth", "setup", "json", "cookie", "token"} {
		t.Run(method, func(t *testing.T) {
			a, owner, cookie, csrf, target, editor := setupAuthUpdate(t)
			// Active is intentionally allowed, independent of the upstream's
			// credential-editing restrictions or website login availability.
			editor.info.Editable = false
			a.store.DB.Model(&core.SyncedAccount{}).Where("id = ?", target.ID).Update("snapshot_status", "active")
			var before core.SyncedAccount
			if err := a.store.DB.First(&before, target.ID).Error; err != nil {
				t.Fatal(err)
			}
			var exchanges, imports, verifies atomic.Int32
			credentialHelperFixture(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
				switch r.URL.Path {
				case "/exchange":
					exchanges.Add(1)
				case "/import":
					imports.Add(1)
				case "/verify":
					verifies.Add(1)
				}
				reply := credentialReply(owner.ID, r.URL.Path == "/exchange" || r.URL.Path == "/import")
				if reply.Auth != nil {
					reply.Auth["email"] = target.Account
				}
				_ = json.NewEncoder(w).Encode(reply)
			})
			preflight := request(t, a, "GET", deactivateRoute(target.ID, ""), nil, cookie, "")
			requireStatus(t, preflight, 200)
			if data(t, preflight)["available"] != true || editor.checks != 0 {
				t.Fatal("generation was gated by supplier editability")
			}
			if method == "oauth" || method == "setup" {
				requireStatus(t, request(t, a, "POST", deactivateRoute(target.ID, "start"), map[string]any{"proxy_url": testCredentialProxy, "mode": method}, cookie, csrf), 200)
				requireStatus(t, request(t, a, "POST", deactivateRoute(target.ID, "exchange"), map[string]any{"flow_id": testCredentialFlowID, "code": "private-code"}, cookie, csrf), 200)
			} else {
				requireStatus(t, request(t, a, "POST", deactivateRoute(target.ID, "import"), map[string]any{"proxy_url": testCredentialProxy, "kind": method, "document": authInput()["auth_json"], "access_token": "private-access", "refresh_token": "private-refresh", "session_key": "private-session"}, cookie, csrf), 200)
			}
			export := deactivateRoute(target.ID, testCredentialFlowID+"/export")
			first := request(t, a, "POST", export, map[string]any{}, cookie, csrf)
			requireStatus(t, first, 200)
			firstData := data(t, first)
			if firstData["status"] != "exported" || firstData["auth"] == nil || firstData["generated_at"] == nil || firstData["audit_id"] == nil || first.Header().Get("X-Credential-Generation-Audit-ID") != firstData["audit_id"] {
				t.Fatal("missing generated result metadata", firstData)
			}
			second := request(t, a, "POST", export, map[string]any{}, cookie, csrf)
			requireStatus(t, second, 200)
			if data(t, second)["generated_at"] != firstData["generated_at"] || data(t, second)["audit_id"] != firstData["audit_id"] || verifies.Load() != 2 || exchanges.Load()+imports.Load() != 1 {
				t.Fatal("download retry regenerated credentials or changed metadata")
			}
			saved := request(t, a, "GET", deactivateRoute(target.ID, testCredentialFlowID), nil, cookie, "")
			requireStatus(t, saved, 200)
			if data(t, saved)["status"] != "exported" || data(t, saved)["generated_at"] != firstData["generated_at"] {
				t.Fatal("saved generation cannot be restored")
			}
			rows := generationAudits(t, a)
			if len(rows) != 1 || rows[0].ActorID != owner.ID || rows[0].OwnerID != target.UserID || rows[0].AccountID != target.ID || rows[0].Method != method || rows[0].Status != "generated" || rows[0].Purpose != "deactivate" || rows[0].RequestID != first.Header().Get("X-Request-ID") {
				t.Fatal(rows)
			}
			var after core.SyncedAccount
			if err := a.store.DB.First(&after, target.ID).Error; err != nil {
				t.Fatal(err)
			}
			if after.AuthJSONUpdatedAt != nil || after.SnapshotStatus == nil || *after.SnapshotStatus != "active" || !before.UpdatedAt.Equal(after.UpdatedAt) {
				t.Fatal("generation changed the saved account")
			}
			var jobs, submissions, updates int64
			a.store.DB.Model(&core.PostSubmissionSyncJob{}).Count(&jobs)
			a.store.DB.Model(&core.Submission{}).Count(&submissions)
			a.store.DB.Model(&core.AccountAuthJSONAudit{}).Count(&updates)
			if jobs != 0 || submissions != 1 || updates != 0 || editor.checks != 0 || editor.writes != 0 || editor.updates != 0 || editor.refreshes != 0 {
				t.Fatal("generation reached supplier, submission or synchronization", jobs, submissions, updates, editor)
			}
			// Consumed drafts remain readable/downloadable, but cannot be
			// exchanged again, edited, or reopened through a cached import.
			requireStatus(t, request(t, a, "POST", deactivateRoute(target.ID, "exchange"), map[string]any{"flow_id": testCredentialFlowID, "code": "private-code"}, cookie, csrf), 409)
			requireStatus(t, request(t, a, "POST", deactivateRoute(target.ID, testCredentialFlowID+"/draft"), map[string]any{"auth_patch": map[string]any{"organization_name": "replacement"}}, cookie, csrf), 409)
			requireStatus(t, request(t, a, "POST", deactivateRoute(target.ID, "import"), map[string]any{"proxy_url": testCredentialProxy, "kind": "json", "document": authInput()["auth_json"]}, cookie, csrf), 409)
			if len(generationAudits(t, a)) != 1 || exchanges.Load()+imports.Load() != 2 {
				t.Fatal("terminal flow was reopened")
			}
		})
	}
}

func TestDeactivatePurposeCannotCrossGenericUpdateAccountOrAdminScope(t *testing.T) {
	a, owner, cookie, csrf, target, editor := setupAuthUpdate(t)
	_, otherCookie, otherCSRF := vendorSession(t, a, "other-generation@example.com", "member")
	var calls atomic.Int32
	authFlowHelper(t, owner.ID, target.Account, &calls)
	start := map[string]any{"proxy_url": testCredentialProxy}
	requireStatus(t, request(t, a, "POST", deactivateRoute(target.ID, "start"), start, nil, ""), 401)
	requireStatus(t, request(t, a, "POST", deactivateRoute(target.ID, "start"), start, cookie, "wrong-csrf"), 403)
	requireStatus(t, request(t, a, "POST", deactivateRoute(target.ID, "start"), start, otherCookie, otherCSRF), 404)
	if calls.Load() != 0 {
		t.Fatal("unauthorized request reached helper")
	}
	requireStatus(t, request(t, a, "POST", deactivateRoute(target.ID, "start"), start, cookie, csrf), 200)
	for _, route := range []string{"credential-flows/" + testCredentialFlowID, authFlowRoute(target.ID, testCredentialFlowID), fmt.Sprintf("admin/synced-accounts/%d/deactivate/flows/%s", target.ID, testCredentialFlowID)} {
		w := request(t, a, "GET", route, nil, cookie, "")
		if w.Code < 400 {
			t.Fatal("purpose or admin scope bypass", route)
		}
	}
	for _, route := range []string{"credential-flows/export", "credential-flows/submit", authRoute(target.ID)} {
		requireStatus(t, request(t, a, "POST", route, map[string]any{"flow_id": testCredentialFlowID}, cookie, csrf), 404)
	}
	// The independently encrypted namespace also prevents older deployments
	// from treating a Deactivate draft as a generic submission authorization.
	sealed, err := a.redis.Get(context.Background(), deactivateFlowKey(owner.ID, testCredentialFlowID)).Result()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.store.OpenCredentialFlow(owner.ID, testCredentialFlowID, sealed); err == nil {
		t.Fatal("generation ciphertext opened under the legacy purpose")
	}
	a.redis.Set(context.Background(), credentialFlowKey(owner.ID, testCredentialFlowID), sealed, time.Minute)
	requireStatus(t, request(t, a, "GET", "credential-flows/"+testCredentialFlowID, nil, cookie, ""), 404)
	a.redis.Del(context.Background(), credentialFlowKey(owner.ID, testCredentialFlowID))
	legacy, err := a.store.SealCredentialFlow(owner.ID, testCredentialFlowID, `{"flow_id":"`+testCredentialFlowID+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.store.OpenAccountDeactivateFlow(owner.ID, testCredentialFlowID, legacy); err == nil {
		t.Fatal("legacy ciphertext opened under the generation purpose")
	}
	a.redis.Set(context.Background(), deactivateFlowKey(owner.ID, testCredentialFlowID), legacy, time.Minute)
	requireStatus(t, request(t, a, "GET", deactivateRoute(target.ID, testCredentialFlowID), nil, cookie, ""), 404)
	a.redis.Set(context.Background(), deactivateFlowKey(owner.ID, testCredentialFlowID), sealed, 20*time.Minute)
	// A helper cache hit may safely create a separate generic authorization;
	// it cannot replace, read or consume the account-bound generation draft.
	requireStatus(t, request(t, a, "POST", "credential-flows/import", map[string]any{"proxy_url": testCredentialProxy, "kind": "json", "document": authInput()["auth_json"]}, cookie, csrf), 200)
	if editor.writes != 0 || len(generationAudits(t, a)) != 0 {
		t.Fatal("cross-purpose draft was consumed")
	}
	// The same actor may own several accounts, but a draft is still bound to
	// one account identity and cannot be read or exported under another row.
	vendor, err := a.store.VendorDetails(context.Background(), target.VendorID)
	if err != nil {
		t.Fatal(err)
	}
	seedAdminSubmission(t, a, owner.ID, vendor, "second-generation@example.com", "accepted", time.Now().UTC())
	if err := a.store.EnsureSyncedAccounts(context.Background()); err != nil {
		t.Fatal(err)
	}
	var second core.SyncedAccount
	a.store.DB.Where("account = ?", "second-generation@example.com").First(&second)
	requireStatus(t, request(t, a, "GET", deactivateRoute(second.ID, testCredentialFlowID), nil, cookie, ""), 404)
	requireStatus(t, request(t, a, "POST", deactivateRoute(second.ID, testCredentialFlowID+"/export"), map[string]any{}, cookie, csrf), 404)
}

func TestDeactivateMissingOnlyCompletionAndEmailBinding(t *testing.T) {
	for _, email := range []string{"account@example.com", "another@example.com"} {
		t.Run(email, func(t *testing.T) {
			a, owner, cookie, csrf, target, editor := setupAuthUpdate(t)
			credentialHelperFixture(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
				reply := credentialReply(owner.ID, r.URL.Path == "/import")
				if reply.Auth != nil {
					reply.Auth["email"] = email
					reply.Auth["organization_name"] = ""
				}
				_ = json.NewEncoder(w).Encode(reply)
			})
			requireStatus(t, request(t, a, "POST", deactivateRoute(target.ID, "import"), map[string]any{"proxy_url": testCredentialProxy, "kind": "json", "document": authInput()["auth_json"]}, cookie, csrf), 200)
			patchRoute := deactivateRoute(target.ID, testCredentialFlowID+"/draft")
			for key, value := range map[string]any{"email": "replacement@example.com", "access_token": "replacement", "proxy_url": "http://different.example:8080", "verification_status": "verified", "disabled": false} {
				requireStatus(t, request(t, a, "POST", patchRoute, map[string]any{"auth_patch": map[string]any{key: value}}, cookie, csrf), 400)
			}
			exportRoute := deactivateRoute(target.ID, testCredentialFlowID+"/export")
			requireStatus(t, request(t, a, "POST", exportRoute, map[string]any{}, cookie, csrf), 400)
			requireStatus(t, request(t, a, "POST", patchRoute, map[string]any{"auth_patch": map[string]any{"organization_name": "Verified organization"}}, cookie, csrf), 200)
			want := 200
			if email != target.Account {
				want = 400
			}
			w := request(t, a, "POST", exportRoute, map[string]any{}, cookie, csrf)
			requireStatus(t, w, want)
			if email != target.Account {
				if dataErrorCode(t, w) != "AUTH_JSON_EMAIL_MISMATCH" || len(generationAudits(t, a)) != 0 {
					t.Fatal("another account's credentials exported")
				}
			}
			if editor.writes != 0 {
				t.Fatal("completion reached supplier")
			}
		})
	}
}

func TestDeactivateParentVisibilityDoesNotGrantCredentialAccessAndAdminCanExport(t *testing.T) {
	a, owner, _, _, target, editor := setupAuthUpdate(t)
	parent, parentCookie, parentCSRF := vendorSession(t, a, "generation-parent@example.com", "member")
	admin, adminCookie, adminCSRF := vendorSession(t, a, "generation-admin@example.com", "admin")
	// A real membership link creates both the immutable direct parent and its
	// closure edge. Visibility-source decoration deliberately requires both.
	if err := a.store.DB.Model(&core.User{}).Where("id = ?", owner.ID).Update("parent_user_id", parent.ID).Error; err != nil {
		t.Fatal(err)
	}
	link := core.UserHierarchy{AncestorUserID: parent.ID, DescendantUserID: owner.ID, SourceUserID: &owner.ID, Depth: 1}
	if err := a.store.DB.Omit("AncestorUser", "DescendantUser", "SourceUser").Create(&link).Error; err != nil {
		t.Fatal(err)
	}
	requireStatus(t, request(t, a, "GET", fmt.Sprintf("accounts/%d", target.ID), nil, parentCookie, ""), 200)
	requireStatus(t, request(t, a, "GET", deactivateRoute(target.ID, ""), nil, parentCookie, ""), 404)
	requireStatus(t, request(t, a, "POST", deactivateRoute(target.ID, "start"), map[string]any{"proxy_url": testCredentialProxy}, parentCookie, parentCSRF), 404)
	var calls atomic.Int32
	authFlowHelper(t, admin.ID, target.Account, &calls)
	base := fmt.Sprintf("admin/synced-accounts/%d/deactivate", target.ID)
	requireStatus(t, request(t, a, "GET", base, nil, adminCookie, ""), 200)
	requireStatus(t, request(t, a, "POST", base+"/flows/import", map[string]any{"proxy_url": testCredentialProxy, "kind": "json", "document": authInput()["auth_json"]}, adminCookie, adminCSRF), 200)
	requireStatus(t, request(t, a, "POST", base+"/flows/"+testCredentialFlowID+"/export", map[string]any{}, adminCookie, adminCSRF), 200)
	rows := generationAudits(t, a)
	if len(rows) != 1 || rows[0].ActorID != admin.ID || rows[0].OwnerID != owner.ID || !rows[0].Admin || editor.writes != 0 {
		t.Fatal("admin generation attribution or scope is wrong", rows)
	}
}

func dataErrorCode(t *testing.T, w interface{ Result() *http.Response }) string {
	t.Helper()
	defer w.Result().Body.Close()
	var body struct {
		Code string `json:"code"`
	}
	_ = json.NewDecoder(w.Result().Body).Decode(&body)
	return body.Code
}

func TestDeactivateSlowVerifyRechecksOwnershipRoleExpiryAndProxy(t *testing.T) {
	for _, scenario := range []string{"identity", "owner", "deleting", "deleted", "auth-version", "role", "vendor", "expiry", "proxy"} {
		t.Run(scenario, func(t *testing.T) {
			a, owner, cookie, csrf, target, _ := setupAuthUpdate(t)
			other, _, _ := vendorSession(t, a, "changed-generation@example.com", "member")
			credentialHelperFixture(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
				reply := credentialReply(owner.ID, r.URL.Path == "/import")
				if reply.Auth != nil {
					reply.Auth["email"] = target.Account
				}
				if r.URL.Path == "/verify" {
					switch scenario {
					case "identity":
						a.store.DB.Model(&core.SyncedAccount{}).Where("id = ?", target.ID).Update("identity_key", "another-lifetime")
					case "owner":
						a.store.DB.Model(&core.SyncedAccount{}).Where("id = ?", target.ID).Update("user_id", other.ID)
					case "deleting":
						a.store.DB.Model(&core.SyncedAccount{}).Where("id = ?", target.ID).Update("deletion_status", "pending")
					case "deleted":
						a.store.DB.Model(&core.SyncedAccount{}).Where("id = ?", target.ID).Update("deleted_at", time.Now().UTC())
					case "auth-version":
						a.store.DB.Model(&core.User{}).Where("id = ?", owner.ID).Update("auth_version", owner.AuthVersion+1)
					case "role":
						a.store.DB.Model(&core.User{}).Where("id = ?", owner.ID).Update("role", "admin")
					case "vendor":
						a.store.DB.Model(&core.Vendor{}).Where("id = ?", target.VendorID).Update("enabled", false)
					case "expiry":
						reply.ExpiresAt = time.Now().Add(-time.Second)
					case "proxy":
						reply.ProxyCheck.IP = "203.0.113.99"
					}
				}
				_ = json.NewEncoder(w).Encode(reply)
			})
			requireStatus(t, request(t, a, "POST", deactivateRoute(target.ID, "import"), map[string]any{"proxy_url": testCredentialProxy, "kind": "json", "document": authInput()["auth_json"]}, cookie, csrf), 200)
			w := request(t, a, "POST", deactivateRoute(target.ID, testCredentialFlowID+"/export"), map[string]any{}, cookie, csrf)
			if w.Code < 400 || len(generationAudits(t, a)) != 0 {
				t.Fatal("stale or unverified generation exported", scenario, w.Code)
			}
			assertNoSecrets(t, w.Body.String())
		})
	}
}

func TestDeactivateSlowAuthorizationCannotReturnCredentialsAfterRevocation(t *testing.T) {
	for _, action := range []string{"start", "import", "exchange", "proxy-test", "bound-proxy-test"} {
		for _, change := range []string{"owner", "auth-version"} {
			t.Run(action+"/"+change, func(t *testing.T) {
				a, owner, cookie, csrf, target, _ := setupAuthUpdate(t)
				other, _, _ := vendorSession(t, a, "after-helper@example.com", "member")
				var mutate atomic.Bool
				credentialHelperFixture(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
					reply := credentialReply(owner.ID, r.URL.Path == "/import" || r.URL.Path == "/exchange")
					if reply.Auth != nil {
						reply.Auth["email"] = target.Account
					}
					if mutate.Load() {
						if change == "owner" {
							a.store.DB.Model(&core.SyncedAccount{}).Where("id = ?", target.ID).Update("user_id", other.ID)
						} else {
							a.store.DB.Model(&core.User{}).Where("id = ?", owner.ID).Update("auth_version", owner.AuthVersion+1)
						}
					}
					_ = json.NewEncoder(w).Encode(reply)
				})
				if action == "exchange" || action == "bound-proxy-test" {
					requireStatus(t, request(t, a, "POST", deactivateRoute(target.ID, "start"), map[string]any{"proxy_url": testCredentialProxy}, cookie, csrf), 200)
				}
				mutate.Store(true)
				routeAction := action
				body := map[string]any{"proxy_url": testCredentialProxy}
				if action == "import" {
					body["kind"] = "json"
					body["document"] = authInput()["auth_json"]
				} else if action == "exchange" {
					body = map[string]any{"flow_id": testCredentialFlowID, "code": "private-code"}
				} else if action == "bound-proxy-test" {
					routeAction = "proxy-test"
					body = map[string]any{"flow_id": testCredentialFlowID}
				}
				w := request(t, a, "POST", deactivateRoute(target.ID, routeAction), body, cookie, csrf)
				if w.Code < 400 || strings.Contains(w.Body.String(), "private-access") || strings.Contains(w.Body.String(), "claude-xxxxx-test") {
					t.Fatal("revoked authorization returned credentials", action, change, w.Code)
				}
				if len(generationAudits(t, a)) != 0 {
					t.Fatal("revoked authorization created a generation audit")
				}
			})
		}
	}
}

func TestDeactivateExportHonorsEarlierHelperAndDraftExpiry(t *testing.T) {
	for _, scenario := range []string{"helper-shortens", "draft-elapses"} {
		t.Run(scenario, func(t *testing.T) {
			a, owner, cookie, csrf, target, _ := setupAuthUpdate(t)
			credentialHelperFixture(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
				reply := credentialReply(owner.ID, r.URL.Path == "/import")
				if reply.Auth != nil {
					reply.Auth["email"] = target.Account
				}
				if r.URL.Path == "/verify" {
					if scenario == "helper-shortens" {
						reply.ExpiresAt = time.Now().Add(2 * time.Minute)
					} else {
						time.Sleep(750 * time.Millisecond)
					}
				}
				_ = json.NewEncoder(w).Encode(reply)
			})
			requireStatus(t, request(t, a, "POST", deactivateRoute(target.ID, "import"), map[string]any{"proxy_url": testCredentialProxy, "kind": "json", "document": authInput()["auth_json"]}, cookie, csrf), 200)
			if scenario == "draft-elapses" {
				key := deactivateFlowKey(owner.ID, testCredentialFlowID)
				value, err := a.redis.Get(context.Background(), key).Result()
				if err != nil {
					t.Fatal(err)
				}
				plain, err := a.store.OpenAccountDeactivateFlow(owner.ID, testCredentialFlowID, value)
				if err != nil {
					t.Fatal(err)
				}
				var flow credentialFlowRecord
				if json.Unmarshal([]byte(plain), &flow) != nil {
					t.Fatal("invalid stored flow")
				}
				flow.ExpiresAt = time.Now().Add(500 * time.Millisecond)
				encoded, _ := json.Marshal(flow)
				sealed, err := a.store.SealAccountDeactivateFlow(owner.ID, testCredentialFlowID, string(encoded))
				if err != nil {
					t.Fatal(err)
				}
				a.redis.Set(context.Background(), key, sealed, time.Minute)
			}
			w := request(t, a, "POST", deactivateRoute(target.ID, testCredentialFlowID+"/export"), map[string]any{}, cookie, csrf)
			if scenario == "draft-elapses" {
				if w.Code < 400 || len(generationAudits(t, a)) != 0 {
					t.Fatal("draft expired during helper verification was exported")
				}
			} else {
				requireStatus(t, w, 200)
				expires, err := time.Parse(time.RFC3339Nano, data(t, w)["expires_at"].(string))
				if err != nil || expires.After(time.Now().Add(3*time.Minute)) {
					t.Fatal("earlier helper expiry was discarded")
				}
			}
		})
	}
}

func TestDeactivateExportCASLossReusesAuditWithoutExchangingAgain(t *testing.T) {
	a, owner, cookie, csrf, target, _ := setupAuthUpdate(t)
	var verifies, imports atomic.Int32
	credentialHelperFixture(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		reply := credentialReply(owner.ID, r.URL.Path == "/import")
		if reply.Auth != nil {
			reply.Auth["email"] = target.Account
			imports.Add(1)
		}
		if r.URL.Path == "/verify" && verifies.Add(1) == 1 {
			// Simulate losing the optimistic persistence race after metadata
			// was read. The replacement retains exactly the same draft.
			key := deactivateFlowKey(owner.ID, testCredentialFlowID)
			value, err := a.redis.Get(context.Background(), key).Result()
			if err != nil {
				t.Error(err)
			}
			plain, err := a.store.OpenAccountDeactivateFlow(owner.ID, testCredentialFlowID, value)
			if err != nil {
				t.Error(err)
			}
			value, err = a.store.SealAccountDeactivateFlow(owner.ID, testCredentialFlowID, plain)
			if err != nil {
				t.Error(err)
			}
			a.redis.Set(context.Background(), key, value, 20*time.Minute)
		}
		_ = json.NewEncoder(w).Encode(reply)
	})
	requireStatus(t, request(t, a, "POST", deactivateRoute(target.ID, "import"), map[string]any{"proxy_url": testCredentialProxy, "kind": "json", "document": authInput()["auth_json"]}, cookie, csrf), 200)
	route := deactivateRoute(target.ID, testCredentialFlowID+"/export")
	first := request(t, a, "POST", route, map[string]any{}, cookie, csrf)
	if first.Code < 400 || len(generationAudits(t, a)) != 1 {
		t.Fatal("CAS failure was acknowledged as success or metadata lost")
	}
	second := request(t, a, "POST", route, map[string]any{}, cookie, csrf)
	requireStatus(t, second, 200)
	if len(generationAudits(t, a)) != 1 || imports.Load() != 1 || verifies.Load() != 2 {
		t.Fatal("CAS recovery regenerated credentials or duplicated the audit")
	}
	if !strings.Contains(second.Body.String(), "\"status\":\"exported\"") {
		t.Fatal("retry did not restore the generated result")
	}
}
