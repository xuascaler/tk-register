package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"goapi-tk-register/server/common"
	"goapi-tk-register/server/internal/core"
	"goapi-tk-register/server/internal/supplier"
)

type mockAuthEditor struct {
	info                            supplier.EditInfo
	record                          supplier.Account
	checkErr, updateErr, refreshErr error
	checks, updates, refreshes      int
	target                          supplier.Target
	raw                             json.RawMessage
	onUpdate                        func()
}

func (m *mockAuthEditor) CheckAuthJSON(_ context.Context, u, p string, target supplier.Target) (supplier.EditInfo, error) {
	m.checks++
	m.target = target
	return m.info, m.checkErr
}
func (m *mockAuthEditor) UpdateAuthJSON(ctx context.Context, u, p string, target supplier.Target, raw json.RawMessage) (supplier.EditInfo, error) {
	m.updates++
	m.target = target
	m.raw = raw
	if m.onUpdate != nil {
		m.onUpdate()
	}
	if target.BeforeUpdate != nil {
		if err := target.BeforeUpdate(ctx, m.info); err != nil {
			return m.info, err
		}
	}
	return m.info, m.updateErr
}
func (m *mockAuthEditor) CollectAccount(_ context.Context, u, p string, target supplier.Target) (supplier.Account, error) {
	m.refreshes++
	m.target = target
	return m.record, m.refreshErr
}

func setupAuthUpdate(t *testing.T) (*app, *core.User, *http.Cookie, string, core.SyncedAccount, *mockAuthEditor) {
	t.Helper()
	a, _, _ := fixture(t)
	admin, _, _ := vendorSession(t, a, "admin@example.com", "admin")
	owner, cookie, csrf := vendorSession(t, a, "owner@example.com", "member")
	vendor := configuredVendor(t, a, admin.ID)
	seedAdminSubmission(t, a, owner.ID, vendor, "account@example.com", "accepted", time.Now().UTC().Add(-time.Hour))
	configureSync(t, a, admin.ID, false)
	target := syncTarget(t, a, owner.ID)
	active, currency := "active", "CNY"
	cost := 12.5
	tokens := int64(42)
	five := 2.0
	seven := 40.0
	fake := &mockAuthEditor{info: supplier.EditInfo{RecordID: "350001", Account: target.Account, Plan: "claude/cc-max-5x", Editable: true}, record: supplier.Account{ID: "350001", PlatformAccountID: "15001", Account: target.Account, Plan: "claude/cc-max-5x", Status: &active, CostTotal: &cost, Currency: &currency, InputTokensTotal: &tokens, Usage5HPercent: &five, Usage7DPercent: &seven}}
	a.accountAuthEditor = fake
	return a, owner, cookie, csrf, target, fake
}
func authInput() map[string]any {
	credentials := submissionParameters()["auth_json"].(map[string]any)
	credentials["email"] = "account@example.com"
	return map[string]any{"auth_json": credentials}
}
func authRoute(id uint64) string { return fmt.Sprintf("accounts/%d/auth-json", id) }
func authAudits(t *testing.T, a *app) []core.AccountAuthJSONAudit {
	t.Helper()
	var rows []core.AccountAuthJSONAudit
	if err := a.store.DB.Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	b, _ := common.Marshal(rows)
	assertNoSecrets(t, string(b))
	return rows
}

func TestAuthJSONOwnershipCSRFAndValidation(t *testing.T) {
	a, owner, cookie, csrf, target, fake := setupAuthUpdate(t)
	_, other, _ := vendorSession(t, a, "other@example.com", "member")
	route := authRoute(target.ID)
	requireStatus(t, request(t, a, "GET", route, nil, nil, ""), 401)
	requireStatus(t, request(t, a, "GET", route, nil, other, ""), 404)
	requireStatus(t, request(t, a, "POST", route, authInput(), other, csrf), 403)
	requireStatus(t, request(t, a, "POST", route, authInput(), cookie, ""), 403)
	requireStatus(t, request(t, a, "GET", fmt.Sprintf("admin/synced-accounts/%d/auth-json", target.ID), nil, cookie, ""), 403)
	requireStatus(t, request(t, a, "DELETE", route, nil, cookie, csrf), 405)
	for _, body := range []any{map[string]any{}, map[string]any{"auth_json": "not-json"}, map[string]any{"auth_json": []any{}}, map[string]any{"auth_json": map[string]any{"access_token": "private-access-token"}}, map[string]any{"auth_json": submissionParameters()["auth_json"], "account": "other@example.com"}} {
		w := request(t, a, "POST", route, body, cookie, csrf)
		requireStatus(t, w, 400)
		assertNoSecrets(t, w.Body.String())
	}
	if fake.checks+fake.updates+fake.refreshes != 0 || len(authAudits(t, a)) != 0 {
		t.Fatal("guard or validation contacted supplier")
	}
	// Member must not use own valid CSRF/session to update another owner's row.
	otherVendor := configuredVendor(t, a, owner.ID)
	u, _, _ := vendorSession(t, a, "third@example.com", "member")
	seedAdminSubmission(t, a, u.ID, otherVendor, "third-account@example.com", "accepted", time.Now())
	third := syncTarget(t, a, u.ID)
	requireStatus(t, request(t, a, "POST", authRoute(third.ID), authInput(), cookie, csrf), 404)
}

func TestAuthJSONUpdatePersistsMetadataAndImmediatelyRefreshes(t *testing.T) {
	a, owner, cookie, csrf, target, fake := setupAuthUpdate(t)
	before := core.Submission{}
	a.store.DB.First(&before, *target.CurrentSubmissionID)
	w := request(t, a, "GET", authRoute(target.ID), nil, cookie, "")
	requireStatus(t, w, 200)
	if data(t, w)["editable"] != true || data(t, w)["supplier_record_id"] != "350001" {
		t.Fatal(w.Body.String())
	}
	// Historical RemoteID isn't used; only preflight's confirmed website row ID.
	if fake.target.RecordID != "" {
		t.Fatal("trusted an unconfirmed historical remote ID")
	}
	// Confirming the same row twice remains successful on unchanged MySQL rows.
	requireStatus(t, request(t, a, "GET", authRoute(target.ID), nil, cookie, ""), 200)
	if fake.target.RecordID != "350001" {
		t.Fatal("verified mapping was not retained")
	}

	raw, _ := common.Marshal(authInput()["auth_json"])
	w = request(t, a, "POST", authRoute(target.ID), map[string]any{"auth_json": string(raw)}, cookie, csrf)
	requireStatus(t, w, 200)
	assertNoSecrets(t, w.Body.String())
	if data(t, w)["updated"] != true || data(t, w)["refresh_status"] != "succeeded" || fake.updates != 1 || fake.refreshes != 1 || fake.target.RecordID != "350001" {
		t.Fatal(w.Body.String(), fake)
	}
	account, err := a.store.SyncedAccountForUser(context.Background(), owner.ID, target.ID)
	if err != nil || account.AuthJSONUpdatedAt == nil || account.SupplierRecordID == nil || *account.SupplierRecordID != "350001" || account.Snapshot == nil || *account.Snapshot.CostTotal != 12.5 || *account.Snapshot.Currency != "CNY" || *account.Snapshot.Usage5hPercent != 2 {
		t.Fatalf("state %+v %v", account, err)
	}
	snapshots, _ := a.store.AccountSnapshots(context.Background(), target.ID, 7)
	if len(snapshots) != 1 {
		t.Fatal("immediate snapshot missing")
	}
	after := core.Submission{}
	a.store.DB.First(&after, *target.CurrentSubmissionID)
	if before.ParametersJSON != after.ParametersJSON || before.SecretParametersCiphertext != after.SecretParametersCiphertext {
		t.Fatal("changed immutable submission credentials")
	}
	audits := authAudits(t, a)
	if len(audits) != 1 || audits[0].Status != "updated" || audits[0].RefreshStatus != "succeeded" || audits[0].ActorID != owner.ID || audits[0].RequestID == "" {
		t.Fatalf("audit %+v", audits)
	}
	if a.redis.Exists(context.Background(), accountSyncLeaseKey).Val() != 0 {
		t.Fatal("global collection lease leaked")
	}
}

func TestAuthJSONRefreshFailurePreservesLastSuccess(t *testing.T) {
	a, owner, cookie, csrf, target, fake := setupAuthUpdate(t)
	at := time.Now().UTC().Add(-time.Hour)
	cost := 8.0
	currency := "CNY"
	status := "offline"
	if err := a.store.RecordAccountSnapshot(context.Background(), target.ID, target.IdentityKey, core.AccountSnapshotValues{CostTotal: &cost, Currency: &currency, AccountStatus: &status}, at, "Asia/Shanghai"); err != nil {
		t.Fatal(err)
	}
	fake.refreshErr = errors.New("private-access-token private-account-password")
	w := request(t, a, "POST", authRoute(target.ID), authInput(), cookie, csrf)
	requireStatus(t, w, 200)
	assertNoSecrets(t, w.Body.String())
	if data(t, w)["updated"] != true || data(t, w)["refresh_status"] != "failed" {
		t.Fatal(w.Body.String())
	}
	account, _ := a.store.SyncedAccountForUser(context.Background(), owner.ID, target.ID)
	if account.AuthJSONUpdatedAt == nil || account.Snapshot == nil || *account.Snapshot.CostTotal != 8 || !account.LastSuccessAt.Equal(at) || account.LastError == "" {
		t.Fatalf("lost old data %+v", account)
	}
	if authAudits(t, a)[0].RefreshStatus != "failed" {
		t.Fatal("refresh audit absent")
	}
}

func TestAuthJSONUnconfirmedOrRejectedNeverRetriesOrRefreshes(t *testing.T) {
	for _, tc := range []struct {
		code    string
		status  int
		outcome string
	}{{supplier.CodeUpdateUnconfirmed, 504, "unknown"}, {supplier.CodeUpdateRejected, 502, "rejected"}, {supplier.CodeNotEditable, 409, "rejected"}, {supplier.CodeUnavailable, 502, "failed"}} {
		t.Run(tc.code, func(t *testing.T) {
			a, owner, cookie, csrf, target, fake := setupAuthUpdate(t)
			fake.updateErr = &supplier.Error{Code: tc.code, Message: "private-access-token"}
			w := request(t, a, "POST", authRoute(target.ID), authInput(), cookie, csrf)
			requireStatus(t, w, tc.status)
			assertNoSecrets(t, w.Body.String())
			if fake.updates != 1 || fake.refreshes != 0 {
				t.Fatal("retried mutation or refreshed rejected write")
			}
			account, _ := a.store.SyncedAccountForUser(context.Background(), owner.ID, target.ID)
			if account.AuthJSONUpdatedAt != nil || account.Snapshot != nil {
				t.Fatal("false update success")
			}
			rows := authAudits(t, a)
			if len(rows) != 1 || rows[0].Status != tc.outcome || rows[0].FinishedAt == nil {
				t.Fatal(rows)
			}
		})
	}
}

func TestAuthJSONAdminUnsupportedAndLockGuards(t *testing.T) {
	a, _, cookie, csrf, target, fake := setupAuthUpdate(t)
	admin, ac, acsrf := vendorSession(t, a, "second-admin@example.com", "admin")
	adminRoute := fmt.Sprintf("admin/synced-accounts/%d/auth-json", target.ID)
	fake.info.Editable = false
	fake.info.Reason = "This account is currently active."
	w := request(t, a, "GET", adminRoute, nil, ac, "")
	requireStatus(t, w, 200)
	if data(t, w)["editable"] != false {
		t.Fatal(w.Body.String())
	}
	fake.info.Editable = true
	a.redis.Set(context.Background(), accountSyncLeaseKey, "another-collector", time.Minute)
	requireStatus(t, request(t, a, "POST", authRoute(target.ID), authInput(), cookie, csrf), 409)
	a.redis.Del(context.Background(), accountSyncLeaseKey)
	key := redisPrefix + "account-auth-json:lease:" + digest(fmt.Sprint(target.VendorID)+":"+strings.ToLower(target.Account))
	a.redis.Set(context.Background(), key, "another-editor", time.Minute)
	requireStatus(t, request(t, a, "POST", authRoute(target.ID), authInput(), cookie, csrf), 409)
	a.redis.Del(context.Background(), key)
	if fake.updates != 0 {
		t.Fatal("lock did not block update")
	}
	requireStatus(t, request(t, a, "POST", adminRoute, authInput(), ac, acsrf), 200)
	audits := authAudits(t, a)
	if audits[0].ActorID != admin.ID || audits[0].OwnerID == admin.ID {
		t.Fatal("admin actor/owner mixed")
	}
	a.store.DB.Model(&core.Vendor{}).Where("id = ?", target.VendorID).Update("enabled", false)
	requireStatus(t, request(t, a, "GET", authRoute(target.ID), nil, cookie, ""), 409)
	a.store.DB.Model(&core.Vendor{}).Where("id = ?", target.VendorID).Updates(map[string]any{"enabled": true, "base_url": "https://another.example"})
	requireStatus(t, request(t, a, "GET", authRoute(target.ID), nil, cookie, ""), 409)
}

func TestAuthJSONIdentityChangeDoesNotModifyNewLifetime(t *testing.T) {
	a, owner, cookie, csrf, target, fake := setupAuthUpdate(t)
	fake.onUpdate = func() {
		var sub core.Submission
		a.store.DB.First(&sub, *target.CurrentSubmissionID)
		sub.ID = 0
		sub.ClientRequestKey = "new-identity"
		sub.CreatedAt = time.Now().UTC()
		sub.UpdatedAt = sub.CreatedAt
		remote := "987654321"
		sub.RemoteID = &remote
		if err := a.store.DB.Create(&sub).Error; err != nil {
			t.Fatal(err)
		}
		if err := a.store.EnsureSyncedAccounts(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	w := request(t, a, "POST", authRoute(target.ID), authInput(), cookie, csrf)
	requireStatus(t, w, 200)
	if data(t, w)["updated"] != true || data(t, w)["refresh_status"] != "failed" || fake.refreshes != 0 {
		t.Fatal(w.Body.String())
	}
	account, _ := a.store.SyncedAccountForUser(context.Background(), owner.ID, target.ID)
	if account.AuthJSONUpdatedAt != nil || account.SupplierRecordID != nil || account.Snapshot != nil {
		t.Fatal("old result contaminated new identity")
	}
	if authAudits(t, a)[0].Status != "updated" {
		t.Fatal("lost confirmed remote result")
	}
}
