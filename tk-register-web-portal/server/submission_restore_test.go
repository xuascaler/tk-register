package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	"goapi-tk-register/server/common"
	"goapi-tk-register/server/internal/core"
	"goapi-tk-register/server/internal/supplier"
)

type restoreRouteFixture struct{ calls int }

func (f *restoreRouteFixture) RestoreAccount(ctx context.Context, _, _ string, target supplier.Target, params map[string]any) (supplier.RestoreResult, error) {
	f.calls++
	if err := target.BeforeUpdate(ctx, supplier.EditInfo{RecordID: "98765", Account: target.Account}); err != nil {
		return supplier.RestoreResult{}, err
	}
	return supplier.RestoreResult{Found: true, Confirmed: true, WriteAttempted: true, RecordID: "98765", Alias: "claude-fixture"}, nil
}

func TestBrowserAndAPIRestoreUseSameServiceAndLease(t *testing.T) {
	for _, via := range []string{"web", "api"} {
		t.Run(via, func(t *testing.T) {
			a, _, _ := fixture(t)
			ctx := context.Background()
			owner, cookie, csrf := vendorSession(t, a, "old-owner@example.com", "admin")
			vendor := configuredVendor(t, a, owner.ID)
			a.vendorTransport = vendorRoundTrip(func(*http.Request) (*http.Response, error) {
				return remoteResponse(200, `{"id":98765,"alias":"old"}`), nil
			})
			w := request(t, a, "POST", "submissions", createInput(vendor, "initial-create-restore-001"), cookie, csrf)
			requireStatus(t, w, 200)
			targets, err := a.store.CollectionTargets(ctx, owner.ID, 0)
			if err != nil || len(targets) != 1 {
				t.Fatal(targets, err)
			}
			old := targets[0]
			now := time.Now().UTC()
			op, _, err := a.store.BeginAccountDeletion(ctx, old.ID, owner.ID, false, "delete-before-restore-route", now)
			if err != nil {
				t.Fatal(err)
			}
			if err = a.store.ConfirmAccountDeletion(ctx, op.ID, "upstream_dates_updated", "98765", now); err != nil {
				t.Fatal(err)
			}
			if err = a.store.FinishAccountDeletion(ctx, op.ID, now); err != nil {
				t.Fatal(err)
			}
			if err = a.store.PatchSettings(ctx, map[string]string{"account_sync_website_username": "supplier-user", "account_sync_website_password": "supplier-password"}, owner.ID); err != nil {
				t.Fatal(err)
			}
			current, currentCookie, currentCSRF := vendorSession(t, a, "new-owner@example.com", "member")
			token, err := a.store.GenerateAPIToken(ctx, current.ID)
			if err != nil {
				t.Fatal(err)
			}
			fake := &restoreRouteFixture{}
			a.accountRestorer = fake
			a.vendorTransport = vendorRoundTrip(func(*http.Request) (*http.Response, error) {
				t.Fatal("restore unexpectedly created account")
				return nil, nil
			})
			raw, _ := common.Marshal(submissionParameters())
			invoke := func() int {
				if via == "api" {
					w = apiRequest(t, a, "POST", claudeAccountsAPIPath, token.PlaintextToken, "application/json", string(raw))
					if w.Code == 200 && w.Header().Get("X-Submission-Mode") != "restore_update" {
						t.Fatal("missing API mode")
					}
				} else {
					w = request(t, a, "POST", "submissions", createInput(vendor, "restore-route-request-0001"), currentCookie, currentCSRF)
					if w.Code == 200 && submissionDetail(t, w.Body.Bytes()).Submission.SubmitMode != "restore_update" {
						t.Fatal("missing web mode")
					}
				}
				return w.Code
			}
			key := accountMutationLeaseKey(old)
			if err = a.redis.Set(ctx, key, "deletion-lease", time.Minute).Err(); err != nil {
				t.Fatal(err)
			}
			if invoke() != 409 || fake.calls != 0 {
				t.Fatal("shared deletion lease ignored", w.Body.String())
			}
			a.redis.Del(ctx, key)
			if invoke() != 200 || fake.calls != 1 {
				t.Fatal("restore route failed", w.Body.String(), fake.calls)
			}
			assertNoSecrets(t, w.Body.String())
			if _, err = a.store.LookupSyncedAccountForUser(ctx, current.ID, "tk-register-0", old.Account); err != nil {
				t.Fatal(err)
			}
			var archives int64
			a.store.DB.Model(&core.DeletedAccount{}).Where("owner_user_id = ?", owner.ID).Count(&archives)
			if archives != 1 {
				t.Fatal("old archive missing")
			}
		})
	}
}
