package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"goapi-tk-register/server/common"
	"goapi-tk-register/server/internal/core"
	"goapi-tk-register/server/internal/diagnostics"
	"goapi-tk-register/server/internal/supplier"
)

func waitingSupplier(stage string, deferred bool, until time.Time) *supplier.Error {
	return &supplier.Error{Code: supplier.CodeRateLimited, Message: "private-access-token", RetryAfterSeconds: 240, RetryAt: until, Stage: stage, Deferred: deferred}
}

func TestSupplierSessionsSharedClientAndSettingsInvalidation(t *testing.T) {
	a, _, _ := fixture(t)
	client, err := supplier.NewCachedClient(a.redis, a.secret)
	if err != nil {
		t.Fatal(err)
	}
	a.supplierClient = client
	if a.collector() != client || a.pageCollector() != client || a.authEditor() != client {
		t.Fatal("supplier entry points do not share the cached client")
	}
	_, cookie, csrf := vendorSession(t, a, "admin@example.com", "admin")
	// Prove Redis is part of actual credential changes while blank password and
	// unrelated changes remain valid (a blank secret preserves the stored value).
	requireStatus(t, request(t, a, "PUT", "admin/account-sync", map[string]any{"website_username": "supplier-user", "website_password": "secret-supplier-password"}, cookie, csrf), 200)
	keysBefore, err := a.redis.Keys(context.Background(), redisPrefix+"supplier-auth:*").Result()
	if err != nil || len(keysBefore) == 0 {
		t.Fatal("credential save did not create an invalidation revision", err)
	}
	requireStatus(t, request(t, a, "PUT", "admin/settings", map[string]any{"settings": map[string]string{"account_sync_website_password": "new-password"}}, cookie, csrf), 200)
	values, err := a.store.Settings(context.Background())
	if err != nil || values.String("account_sync_website_password") != "new-password" {
		t.Fatal("generic settings did not update credentials", err)
	}
	requireStatus(t, request(t, a, "PUT", "admin/account-sync", map[string]any{"website_password": ""}, cookie, csrf), 200)
	values, _ = a.store.Settings(context.Background())
	if values.String("account_sync_website_password") != "new-password" {
		t.Fatal("blank password lost saved secret")
	}
	// Session cache initialization or persistence failures cannot save credentials
	// while silently leaving the previous generation usable.
	a.redis.Close()
	if err := a.invalidateSupplierCredentials(context.Background(), map[string]string{"account_sync_website_password": "different"}); err == nil {
		t.Fatal("credential invalidation hid Redis failure")
	}
}

func TestSupplierSessionsPageWaitMetadataPreservesPartialSuccess(t *testing.T) {
	a, _, cookie, csrf, rows, fake := setupPageSync(t)
	until := time.Now().UTC().Add(240 * time.Second)
	fake.run = func(ctx context.Context, targets []supplier.Target) ([]supplier.AccountResult, error) {
		return []supplier.AccountResult{pageRecord(targets[0]), {Target: targets[1], Err: waitingSupplier("accounts", false, until)}}, nil
	}
	w := request(t, a, "POST", "accounts/sync", pageInput(rows[0], rows[1]), cookie, csrf)
	requireStatus(t, w, 200)
	result := readPageResponse(t, w.Body.Bytes())
	if result.UpdatedCount != 1 || result.FailedCount != 1 || result.Results[0].RetryAfter != 0 || result.Results[1].RetryAfter < 238 || result.Results[1].RetryAfter > 240 || result.Results[1].RetryStage != "accounts" {
		t.Fatalf("invalid mixed results: %+v", result)
	}
	if strings.Contains(w.Body.String(), "private-access-token") || !strings.Contains(result.Results[1].Message, "account queries") {
		t.Fatal("unsafe or unclear wait message")
	}
}

func TestSupplierSessionsDetailAndTestLoginReturnWait(t *testing.T) {
	a, _, cookie, csrf, rows, _ := setupPageSync(t)
	a.accountCollector = &mockAccountCollector{err: waitingSupplier("login", true, time.Now().UTC().Add(240*time.Second))}
	w := request(t, a, "POST", fmt.Sprintf("accounts/%d/sync", rows[0].ID), map[string]any{}, cookie, csrf)
	requireStatus(t, w, http.StatusTooManyRequests)
	var failure envelope
	if err := common.Unmarshal(w.Body.Bytes(), &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Code != "SUPPLIER_RATE_LIMITED" || failure.RetryAfter < 238 || failure.RetryStage != "login" || w.Header().Get("Retry-After") == "" {
		t.Fatalf("missing wait metadata: %+v", failure)
	}
	if strings.Contains(w.Body.String(), "private-access-token") {
		t.Fatal("upstream error leaked")
	}
	_, adminCookie, adminCSRF := vendorSession(t, a, "anotheradmin@example.com", "admin")
	w = request(t, a, "POST", "admin/account-sync/test-login", map[string]string{"website_username": "supplier-user", "website_password": "secret-supplier-password"}, adminCookie, adminCSRF)
	requireStatus(t, w, 429)
	if w.Header().Get("Retry-After") == "" || !strings.Contains(w.Body.String(), `"retry_stage":"login"`) {
		t.Fatal("test-login wait omitted")
	}
}

func TestSupplierSessionsAuthJSONWaitDoesNotReplayWrite(t *testing.T) {
	a, _, cookie, csrf, target, fake := setupAuthUpdate(t)
	fake.checkErr = waitingSupplier("accounts", true, time.Now().UTC().Add(240*time.Second))
	w := request(t, a, "GET", authRoute(target.ID), nil, cookie, "")
	requireStatus(t, w, 429)
	if fake.updates != 0 || w.Header().Get("Retry-After") == "" {
		t.Fatal("eligibility gate wrote or omitted wait")
	}
	fake.checkErr = nil
	fake.updateErr = waitingSupplier("login", true, time.Now().UTC().Add(240*time.Second))
	w = request(t, a, "POST", authRoute(target.ID), authInput(), cookie, csrf)
	requireStatus(t, w, 429)
	if fake.updates != 1 || fake.refreshes != 0 {
		t.Fatal("failed update retried or refreshed")
	}
	audits := authAudits(t, a)
	if len(audits) != 1 || audits[0].Status != "failed" || audits[0].ErrorCode != "SUPPLIER_RATE_LIMITED" {
		t.Fatalf("incorrect wait audit: %+v", audits)
	}
	// A known successful write followed by a read cooldown must remain successful.
	fake.updateErr = nil
	fake.refreshErr = waitingSupplier("accounts", false, time.Now().UTC().Add(240*time.Second))
	w = request(t, a, "POST", authRoute(target.ID), authInput(), cookie, csrf)
	requireStatus(t, w, 200)
	result := data(t, w)
	if result["updated"] != true || result["refresh_status"] != "failed" || result["refresh_retry_stage"] != "accounts" || result["refresh_retry_after"].(float64) < 238 || fake.updates != 2 || fake.refreshes != 1 {
		t.Fatal("post-write refresh wait changed write outcome", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "private-access-token") {
		t.Fatal("private upstream text leaked")
	}
}

func TestSupplierSessionsJobDeferralPreservesAttemptAndDueTime(t *testing.T) {
	a, target, job, fake, now := setupPostSubmissionSync(t)
	until := time.Now().UTC().Add(240 * time.Second)
	fake.run = func(context.Context, []supplier.Target) ([]supplier.AccountResult, error) {
		return nil, waitingSupplier("login", true, until)
	}
	if err := a.processPostSubmissionSync(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	saved := readPostSubmissionJob(t, a, job.ID)
	if saved.Status != "pending" || saved.AttemptCount != 0 || saved.StartedAt != nil || !saved.NextRunAt.Equal(until) || saved.FinishedAt != nil {
		t.Fatalf("local cooldown consumed attempt: %+v", saved)
	}
	account, err := a.store.SyncedAccountForUser(context.Background(), target.UserID, target.ID)
	if err != nil || account.LastAttemptAt != nil || account.Snapshot != nil {
		t.Fatal("local deferral changed account data", err)
	}
	if err := a.processPostSubmissionSync(context.Background(), until.Add(-time.Millisecond)); err != nil || fake.calls != 1 {
		t.Fatal("deferred task ran early", err)
	}
	fake.run = func(ctx context.Context, targets []supplier.Target) ([]supplier.AccountResult, error) {
		return []supplier.AccountResult{pageRecord(targets[0])}, nil
	}
	if err := a.processPostSubmissionSync(context.Background(), until); err != nil {
		t.Fatal(err)
	}
	saved = readPostSubmissionJob(t, a, job.ID)
	if saved.Status != "succeeded" || saved.AttemptCount != 1 {
		t.Fatalf("resumed job did not use first actual attempt: %+v", saved)
	}
	// A stale deferral cannot reopen a terminal job.
	if err := a.store.DeferClaimedPostSubmissionSyncJob(context.Background(), job.ID, 1, until, until.Add(time.Hour), time.Now().UTC(), "waiting"); err != nil {
		t.Fatal(err)
	}
	if readPostSubmissionJob(t, a, job.ID).Status != "succeeded" {
		t.Fatal("late deferral reopened completed job")
	}
}

func TestSupplierSessionsActual429RetryBudget(t *testing.T) {
	a, _, job, fake, _ := setupPostSubmissionSync(t)
	until := time.Now().UTC().Add(240 * time.Second)
	fake.run = func(ctx context.Context, _ []supplier.Target) ([]supplier.AccountResult, error) {
		return nil, waitingSupplier("accounts", false, until)
	}
	for attempt := 1; attempt <= 4; attempt++ {
		if err := a.processPostSubmissionSync(context.Background(), readPostSubmissionJob(t, a, job.ID).NextRunAt); err != nil {
			t.Fatal(err)
		}
		saved := readPostSubmissionJob(t, a, job.ID)
		if saved.AttemptCount != attempt {
			t.Fatalf("actual HTTP failure lost attempt: %+v", saved)
		}
		if attempt < 4 && (saved.Status != "pending" || saved.NextRunAt.Before(until)) {
			t.Fatalf("cooldown deadline ignored: %+v", saved)
		}
	}
	if saved := readPostSubmissionJob(t, a, job.ID); saved.Status != "failed" || saved.FinishedAt == nil || fake.calls != 4 {
		t.Fatalf("retry exhaustion changed: %+v calls=%d", saved, fake.calls)
	}
}

func TestSupplierSessionsLocalWaitAfterSessionHTTPDoesNotConsumeAttempt(t *testing.T) {
	for _, httpStatus := range []int{200, 401} {
		t.Run(fmt.Sprint(httpStatus), func(t *testing.T) {
			a, target, job, fake, now := setupPostSubmissionSync(t)
			until := time.Now().UTC().Add(240 * time.Second)
			fake.run = func(ctx context.Context, _ []supplier.Target) ([]supplier.AccountResult, error) {
				diagnostics.LogRequest(ctx, diagnostics.RequestEvent{StartedAt: time.Now(), Method: "GET", Path: "/supplier-account/api/supplier/accounts", HTTPStatus: httpStatus, Outcome: "succeeded"})
				return nil, waitingSupplier("accounts", true, until)
			}
			if err := a.processPostSubmissionSync(context.Background(), now); err != nil {
				t.Fatal(err)
			}
			saved := readPostSubmissionJob(t, a, job.ID)
			if saved.AttemptCount != 0 || saved.Status != "pending" || !saved.NextRunAt.Equal(until) {
				t.Fatalf("local wait consumed retry after session HTTP: %+v", saved)
			}
			account, err := a.store.SyncedAccountForUser(context.Background(), target.UserID, target.ID)
			if err != nil || account.LastAttemptAt != nil {
				t.Fatal("local wait wrote account failure", err)
			}
		})
	}
}

func TestSupplierSessionsDeferredClaimCAS(t *testing.T) {
	a, _, job, _, now := setupPostSubmissionSync(t)
	now = now.Truncate(time.Millisecond).Add(time.Millisecond + 321*time.Microsecond)
	claimed, err := a.store.ClaimPostSubmissionSyncJob(context.Background(), job.ID, now)
	if err != nil || !claimed {
		t.Fatal(err)
	}
	if saved := readPostSubmissionJob(t, a, job.ID); saved.StartedAt == nil || !saved.StartedAt.Equal(now.Truncate(time.Millisecond)) {
		t.Fatal("claim timestamp was not normalized for MySQL precision")
	}
	for _, wrong := range []struct {
		attempt int
		started time.Time
	}{{2, now}, {1, now.Add(time.Second)}} {
		if err := a.store.DeferClaimedPostSubmissionSyncJob(context.Background(), job.ID, wrong.attempt, wrong.started, now.Add(time.Minute), now, "waiting"); err != nil {
			t.Fatal(err)
		}
		if saved := readPostSubmissionJob(t, a, job.ID); saved.Status != "running" || saved.AttemptCount != 1 {
			t.Fatal("stale claim changed job")
		}
	}
	// Exact normalized claim can defer despite an original sub-millisecond input.
	if err := a.store.DeferClaimedPostSubmissionSyncJob(context.Background(), job.ID, 1, now, now.Add(time.Minute), now, "waiting"); err != nil {
		t.Fatal(err)
	}
	if saved := readPostSubmissionJob(t, a, job.ID); saved.Status != "pending" || saved.AttemptCount != 0 {
		t.Fatal("normalized claim did not defer")
	}
	now = now.Add(time.Minute)
	if claimed, err := a.store.ClaimPostSubmissionSyncJob(context.Background(), job.ID, now); err != nil || !claimed {
		t.Fatal("reclaim failed", err)
	}
	// Existing retry finish guard remains conditional on running status.
	if err := a.store.FinishPostSubmissionSyncJobAfter(context.Background(), job.ID, "succeeded", "", now, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := a.store.DeferClaimedPostSubmissionSyncJob(context.Background(), job.ID, 1, now, now.Add(time.Minute), now, "waiting"); err != nil {
		t.Fatal(err)
	}
	var stored core.PostSubmissionSyncJob
	if err := a.store.DB.First(&stored, job.ID).Error; err != nil || stored.Status != "succeeded" {
		t.Fatal("finished job reopened", err)
	}
}
