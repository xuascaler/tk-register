package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"goapi-tk-register/server/common"
	"goapi-tk-register/server/internal/core"
	"goapi-tk-register/server/internal/supplier"
)

func authAPIBody(t *testing.T, username, account string, asString bool) string {
	t.Helper()
	auth := submissionParameters()["auth_json"].(map[string]any)
	auth["email"] = strings.TrimSpace(account)
	var input any = auth
	if asString {
		raw, err := common.Marshal(auth)
		if err != nil {
			t.Fatal(err)
		}
		input = string(raw)
	}
	raw, err := common.Marshal(map[string]any{"username": username, "account": account, "auth_json": input})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func authAPIResult(t *testing.T, w *httptest.ResponseRecorder) struct {
	Updated        bool                 `json:"updated"`
	AuditID        string               `json:"audit_id"`
	UpdatedAt      time.Time            `json:"updated_at"`
	RefreshStatus  string               `json:"refresh_status"`
	RefreshMessage string               `json:"refresh_message"`
	Account        *accountLookupResult `json:"account"`
} {
	t.Helper()
	var envelope struct {
		Data struct {
			Updated        bool                 `json:"updated"`
			AuditID        string               `json:"audit_id"`
			UpdatedAt      time.Time            `json:"updated_at"`
			RefreshStatus  string               `json:"refresh_status"`
			RefreshMessage string               `json:"refresh_message"`
			Account        *accountLookupResult `json:"account"`
		} `json:"data"`
	}
	if err := common.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

func TestAuthJSONAPISuccessRefreshAndAuditContract(t *testing.T) {
	for _, asString := range []bool{false, true} {
		t.Run(fmt.Sprintf("JSON-string=%t", asString), func(t *testing.T) {
			a, owner, _, _, target, fake := setupAuthUpdate(t)
			token := lookupToken(t, a, owner.ID)
			var before core.Submission
			if err := a.store.DB.First(&before, *target.CurrentSubmissionID).Error; err != nil {
				t.Fatal(err)
			}
			body := authAPIBody(t, " OWNER@EXAMPLE.COM ", " ACCOUNT@EXAMPLE.COM ", asString)
			w := apiRequest(t, a, "POST", claudeAccountAuthJSONAPIPath, token.PlaintextToken, "application/json; charset=utf-8", body)
			requireStatus(t, w, 200)
			out := authAPIResult(t, w)
			if !out.Updated || out.AuditID == "" || out.UpdatedAt.IsZero() || out.RefreshStatus != "succeeded" || out.Account == nil || out.RefreshMessage == "" {
				t.Fatal(w.Body.String())
			}
			if fake.updates != 1 || fake.refreshes != 1 || fake.target.RecordID != "350001" {
				t.Fatal("expected exactly one write followed by fresh account collection", fake)
			}
			account := out.Account
			if account.ID != fmt.Sprint(target.ID) || account.Username != owner.Email || account.Account != target.Account || account.Vendor != "tk-register-0" || account.IntervalHours != 2 {
				t.Fatalf("unsafe or incorrect public account attribution: %+v", account)
			}
			if account.Snapshot == nil || account.Snapshot.CostTotal == nil || *account.Snapshot.CostTotal != 12.5 || account.Snapshot.CostTotalUSD == nil || math.Abs(*account.Snapshot.CostTotalUSD-12.5/7.2) > 1e-12 || account.Snapshot.ExchangeRateCNYPerUSD != 7.2 || account.Snapshot.Currency == nil || *account.Snapshot.Currency != "CNY" {
				t.Fatalf("fresh usage or currency contract lost: %+v", account.Snapshot)
			}
			if w.Header().Get("X-Request-ID") == "" || w.Header().Get("X-Auth-JSON-Audit-ID") != out.AuditID || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("missing correlation/private headers")
			}
			for _, private := range []string{"identity_key", "current_submission_id", "supplier_record_id", "secret_parameters", "auth_json", token.PlaintextToken} {
				if strings.Contains(w.Body.String(), private) {
					t.Fatal("public response exposed an internal/credential field", private)
				}
			}
			assertNoSecrets(t, w.Body.String())
			rows := authAudits(t, a)
			tokenID, _ := core.ParseBusinessID(token.Token.ID)
			if len(rows) != 1 {
				t.Fatalf("expected one update audit, got %+v", rows)
			}
			audit := rows[0]
			if fmt.Sprint(audit.ID) != out.AuditID || audit.Source != "api" || audit.APITokenID == nil || *audit.APITokenID != tokenID || audit.APITokenRevision == nil || *audit.APITokenRevision != token.Token.Revision || audit.ActorID != owner.ID || audit.OwnerID != owner.ID || audit.AccountID != target.ID || audit.VendorID != target.VendorID || audit.RequestID != w.Header().Get("X-Request-ID") || audit.Status != "updated" || audit.RefreshStatus != "succeeded" || audit.FinishedAt == nil {
				t.Fatalf("incomplete API audit metadata: %+v", audit)
			}
			var after core.Submission
			a.store.DB.First(&after, before.ID)
			var count int64
			a.store.DB.Model(&core.Submission{}).Count(&count)
			if count != 1 || after.ParametersJSON != before.ParametersJSON || after.SecretParametersCiphertext != before.SecretParametersCiphertext {
				t.Fatal("credential update created or changed a submission")
			}
			if a.redis.Exists(context.Background(), accountSyncLeaseKey).Val() != 0 {
				t.Fatal("collection lease leaked after update")
			}
		})
	}
}

func TestAuthJSONAPIAuthorizationDoesNotEscalateAdminTokens(t *testing.T) {
	a, owner, cookie, csrf, target, fake := setupAuthUpdate(t)
	token := lookupToken(t, a, owner.ID)
	admin, _, _ := vendorSession(t, a, "api-admin@example.com", "admin")
	adminToken := lookupToken(t, a, admin.ID)
	body := authAPIBody(t, owner.Email, target.Account, false)
	for _, raw := range []string{"", "invalid"} {
		requireStatus(t, apiRequest(t, a, "POST", claudeAccountAuthJSONAPIPath, raw, "application/json", body), 401)
	}
	r := httptest.NewRequest("POST", claudeAccountAuthJSONAPIPath, strings.NewReader(body))
	r.Header.Add("Authorization", "Bearer "+token.PlaintextToken)
	r.Header.Add("Authorization", "Bearer "+token.PlaintextToken)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	requireStatus(t, w, 401)
	// Browser credentials cannot authorize the personal-token route.
	requireStatus(t, request(t, a, "POST", strings.TrimPrefix(claudeAccountAuthJSONAPIPath, basePath+"/api/"), json.RawMessage(body), cookie, csrf), 401)
	for _, tc := range []struct{ username, account, bearer string }{
		{"someone-else@example.com", target.Account, token.PlaintextToken},
		{owner.Email, "missing@example.com", token.PlaintextToken},
		{owner.Email, target.Account, adminToken.PlaintextToken},
		{admin.Email, target.Account, adminToken.PlaintextToken},
	} {
		w = apiRequest(t, a, "POST", claudeAccountAuthJSONAPIPath, tc.bearer, "application/json", authAPIBody(t, tc.username, tc.account, false))
		requireStatus(t, w, 404)
		if !strings.Contains(w.Body.String(), `"code":"ACCOUNT_NOT_FOUND"`) {
			t.Fatal(w.Body.String())
		}
	}
	if err := a.store.DB.Model(&core.UserAPIToken{}).Where("user_id = ?", owner.ID).Update("scope", core.ClaudeSubmitScope+" "+core.AccountsReadScope).Error; err != nil {
		t.Fatal(err)
	}
	w = apiRequest(t, a, "POST", claudeAccountAuthJSONAPIPath, token.PlaintextToken, "application/json", body)
	requireStatus(t, w, 403)
	if !strings.Contains(w.Body.String(), `"code":"INSUFFICIENT_SCOPE"`) || !strings.Contains(w.Header().Get("WWW-Authenticate"), core.ClaudeAuthJSONUpdateScope) {
		t.Fatal("explicitly restricted token did not receive a scope-specific rejection", w.Body.String())
	}
	if fake.updates+fake.refreshes+fake.checks != 0 || len(authAudits(t, a)) != 0 {
		t.Fatal("authorization failure contacted supplier or created update audit")
	}
	// Explicit rotation restores default scope after a later restriction and invalidates the old secret.
	id, _ := core.ParseBusinessID(token.Token.ID)
	rotated, err := a.store.RotateAPIToken(context.Background(), owner.ID, id, token.Token.Revision)
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, apiRequest(t, a, "POST", claudeAccountAuthJSONAPIPath, token.PlaintextToken, "application/json", body), 401)
	requireStatus(t, apiRequest(t, a, "POST", claudeAccountAuthJSONAPIPath, rotated.PlaintextToken, "application/json", body), 200)
	rows := authAudits(t, a)
	if len(rows) != 1 || rows[0].APITokenRevision == nil || *rows[0].APITokenRevision != rotated.Token.Revision {
		t.Fatal("audit did not preserve the token revision used", rows)
	}
}

func TestAuthJSONAPIRequestValidationPreventsSupplierWrites(t *testing.T) {
	type testCase struct {
		name, method, suffix, contentType string
		body                              func(string) string
		status                            int
		code                              string
	}
	cases := []testCase{
		{name: "GET", method: "GET", status: 405},
		{name: "PUT", method: "PUT", status: 405},
		{name: "query", suffix: "?account=another@example.com", status: 400},
		{name: "wrong content type", contentType: "text/plain", status: 415},
		{name: "fake JSON media type", contentType: "application/json-bad", status: 415},
		{name: "missing username", body: func(string) string { return `{"account":"account@example.com","auth_json":{}}` }, status: 400},
		{name: "wrong username type", body: func(s string) string {
			return strings.Replace(s, `"username":"owner@example.com"`, `"username":123`, 1)
		}, status: 400},
		{name: "account spaces", body: func(s string) string { return strings.ReplaceAll(s, "account@example.com", "account @example.com") }, status: 400},
		{name: "invalid auth object", body: func(string) string {
			return `{"username":"owner@example.com","account":"account@example.com","auth_json":[]}`
		}, status: 400},
		{name: "unknown field", body: func(s string) string { return strings.TrimSuffix(s, "}") + `,"supplier_record_id":"999"}` }, status: 400},
		{name: "duplicate username", body: func(s string) string { return strings.TrimSuffix(s, "}") + `,"username":"other@example.com"}` }, status: 400},
		{name: "multiple JSON documents", body: func(s string) string { return s + `{}` }, status: 400},
		{name: "null body", body: func(string) string { return `null` }, status: 400},
		{name: "mismatched auth email", body: func(s string) string {
			return strings.Replace(s, `"email":"account@example.com"`, `"email":"different@example.com"`, 1)
		}, status: 400, code: "AUTH_JSON_EMAIL_MISMATCH"},
		{name: "oversize request", body: func(string) string { return `{"remark":"` + strings.Repeat("x", 3<<20) + `"}` }, status: 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, owner, _, _, target, fake := setupAuthUpdate(t)
			token := lookupToken(t, a, owner.ID)
			body := authAPIBody(t, owner.Email, target.Account, false)
			if tc.body != nil {
				body = tc.body(body)
			}
			method, mime := tc.method, tc.contentType
			if method == "" {
				method = "POST"
			}
			if mime == "" {
				mime = "application/json"
			}
			w := apiRequest(t, a, method, claudeAccountAuthJSONAPIPath+tc.suffix, token.PlaintextToken, mime, body)
			requireStatus(t, w, tc.status)
			if tc.code != "" && !strings.Contains(w.Body.String(), `"code":"`+tc.code+`"`) {
				t.Fatal(w.Body.String())
			}
			if tc.status == 405 && w.Header().Get("Allow") != "POST" {
				t.Fatal("incorrect Allow header")
			}
			if fake.updates+fake.refreshes+fake.checks != 0 || len(authAudits(t, a)) != 0 || w.Header().Get("X-Auth-JSON-Audit-ID") != "" {
				t.Fatal("invalid request started an audited vendor update")
			}
			assertNoSecrets(t, w.Body.String())
		})
	}
}

func TestAuthJSONAPIUpdateAndRefreshOutcomesRemainSeparate(t *testing.T) {
	t.Run("confirmed write but failed refresh", func(t *testing.T) {
		a, owner, _, _, target, fake := setupAuthUpdate(t)
		token := lookupToken(t, a, owner.ID)
		at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
		cost, currency, state := 72.0, "CNY", "offline"
		if err := a.store.RecordAccountSnapshot(context.Background(), target.ID, target.IdentityKey, core.AccountSnapshotValues{CostTotal: &cost, Currency: &currency, AccountStatus: &state}, at, "Asia/Shanghai"); err != nil {
			t.Fatal(err)
		}
		fake.refreshErr = errors.New("private-access-token private-account-password")
		w := apiRequest(t, a, "POST", claudeAccountAuthJSONAPIPath, token.PlaintextToken, "application/json", authAPIBody(t, owner.Email, target.Account, false))
		requireStatus(t, w, 200)
		out := authAPIResult(t, w)
		if !out.Updated || out.RefreshStatus != "failed" || out.Account == nil || out.Account.Snapshot == nil || out.Account.LastSuccessAt == nil || !out.Account.LastSuccessAt.Equal(at) || *out.Account.Snapshot.CostTotalUSD != 10 || *out.Account.Snapshot.AccountStatus != "offline" {
			t.Fatal("lost successful credential update or last successful snapshot", w.Body.String())
		}
		rows := authAudits(t, a)
		if len(rows) != 1 || rows[0].Status != "updated" || rows[0].RefreshStatus != "failed" || fake.updates != 1 || fake.refreshes != 1 {
			t.Fatal(rows, fake)
		}
		assertNoSecrets(t, w.Body.String())
	})
	for _, tc := range []struct {
		code, auditStatus string
		httpStatus        int
	}{
		{supplier.CodeUpdateUnconfirmed, "unknown", 504},
		{supplier.CodeUpdateRejected, "rejected", 502},
		{supplier.CodeNotEditable, "rejected", 409},
		{supplier.CodeCredentialsInvalid, "failed", 502},
	} {
		t.Run(tc.code, func(t *testing.T) {
			a, owner, _, _, target, fake := setupAuthUpdate(t)
			token := lookupToken(t, a, owner.ID)
			fake.updateErr = &supplier.Error{Code: tc.code, Message: "private-access-token"}
			w := apiRequest(t, a, "POST", claudeAccountAuthJSONAPIPath, token.PlaintextToken, "application/json", authAPIBody(t, owner.Email, target.Account, false))
			requireStatus(t, w, tc.httpStatus)
			rows := authAudits(t, a)
			if len(rows) != 1 || rows[0].Status != tc.auditStatus || rows[0].Source != "api" || rows[0].RefreshStatus != "" || rows[0].FinishedAt == nil || fmt.Sprint(rows[0].ID) != w.Header().Get("X-Auth-JSON-Audit-ID") || rows[0].RequestID != w.Header().Get("X-Request-ID") {
				t.Fatal("failed write did not retain its audit/correlation result", rows, w.Body.String())
			}
			if fake.updates != 1 || fake.refreshes != 0 {
				t.Fatal("write was retried or a failed write was refreshed", fake)
			}
			account, err := a.store.SyncedAccountForUser(context.Background(), owner.ID, target.ID)
			if err != nil || account.AuthJSONUpdatedAt != nil || account.Snapshot != nil {
				t.Fatal("failure incorrectly marked credentials updated", err)
			}
			assertNoSecrets(t, w.Body.String())
		})
	}
}

func TestAuthJSONAPISharesWebLeasesAndRateLimit(t *testing.T) {
	a, owner, cookie, csrf, target, fake := setupAuthUpdate(t)
	token := lookupToken(t, a, owner.ID)
	body := authAPIBody(t, owner.Email, target.Account, false)
	ctx := context.Background()
	accountKey := redisPrefix + "account-auth-json:lease:" + digest(fmt.Sprint(target.VendorID)+":"+strings.ToLower(target.Account))
	for _, key := range []string{accountKey, accountSyncLeaseKey} {
		a.redis.Set(ctx, key, "existing-work", time.Minute)
		w := apiRequest(t, a, "POST", claudeAccountAuthJSONAPIPath, token.PlaintextToken, "application/json", body)
		requireStatus(t, w, 409)
		if a.redis.Get(ctx, key).Val() != "existing-work" {
			t.Fatal("failed acquisition released another operation's lock")
		}
		a.redis.Del(ctx, key)
	}
	if fake.updates != 0 || len(authAudits(t, a)) != 0 {
		t.Fatal("lock failure reached vendor")
	}
	// A fresh minute allows exactly ten combined web/API update attempts. Token
	// rotation does not reset the owner-scoped limit.
	a.redis.Del(ctx, redisPrefix+"account-auth-json:rate:POST:"+fmt.Sprint(owner.ID))
	fake.updateErr = &supplier.Error{Code: supplier.CodeUpdateRejected}
	for i := 0; i < 10; i++ {
		if i%2 == 0 {
			requireStatus(t, request(t, a, "POST", authRoute(target.ID), authInput(), cookie, csrf), 502)
		} else {
			requireStatus(t, apiRequest(t, a, "POST", claudeAccountAuthJSONAPIPath, token.PlaintextToken, "application/json", body), 502)
		}
	}
	w := apiRequest(t, a, "POST", claudeAccountAuthJSONAPIPath, token.PlaintextToken, "application/json", body)
	requireStatus(t, w, 429)
	if w.Header().Get("Retry-After") == "" || fake.updates != 10 {
		t.Fatal("missing shared rate limit or Retry-After")
	}
	id, _ := core.ParseBusinessID(token.Token.ID)
	rotated, err := a.store.RotateAPIToken(ctx, owner.ID, id, token.Token.Revision)
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, apiRequest(t, a, "POST", claudeAccountAuthJSONAPIPath, rotated.PlaintextToken, "application/json", body), 429)
	rows := authAudits(t, a)
	if len(rows) != 10 || rows[0].Source != "web" || rows[0].APITokenID != nil || rows[1].Source != "api" {
		t.Fatal("web/API audit sources or count incorrect", rows)
	}
}

func TestAuthJSONAPIOwnershipConflictPreventsAmbiguousMutation(t *testing.T) {
	a, owner, _, _, target, fake := setupAuthUpdate(t)
	token := lookupToken(t, a, owner.ID)
	other, _, _ := vendorSession(t, a, "conflicting-owner@example.com", "member")
	vendor := configuredVendor(t, a, owner.ID)
	seedAdminSubmission(t, a, other.ID, vendor, " ACCOUNT@EXAMPLE.COM ", "accepted", time.Now().UTC())
	if err := a.store.EnsureSyncedAccounts(context.Background()); err != nil {
		t.Fatal(err)
	}
	w := apiRequest(t, a, "POST", claudeAccountAuthJSONAPIPath, token.PlaintextToken, "application/json", authAPIBody(t, owner.Email, target.Account, false))
	requireStatus(t, w, 409)
	if !strings.Contains(w.Body.String(), `"code":"ACCOUNT_OWNERSHIP_CONFLICT"`) || fake.updates != 0 || fake.refreshes != 0 {
		t.Fatal("same supplier identity was mutated through conflicting local ownership", w.Body.String())
	}
}
