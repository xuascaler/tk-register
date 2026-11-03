package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"goapi-tk-register/server/common"
	"goapi-tk-register/server/internal/core"
	"goapi-tk-register/server/internal/supplier"
	"gorm.io/gorm"
)

type mockAccountExpirer struct {
	calls       int
	writes      int
	allowWrites []bool
	run         func(context.Context, supplier.Target, bool, func(context.Context, supplier.ExpireInfo) error) (supplier.ExpireResult, error)
}

func (m *mockAccountExpirer) ExpireAccount(ctx context.Context, u, p string, target supplier.Target, allow bool, before func(context.Context, supplier.ExpireInfo) error) (supplier.ExpireResult, error) {
	m.calls++
	m.allowWrites = append(m.allowWrites, allow)
	if m.run != nil {
		return m.run(ctx, target, allow, before)
	}
	info := supplier.ExpireInfo{RecordID: "450001", Account: target.Account, Plan: "claude/cc-max-20x", PlanStartTime: "2026-11-01", PlanEndTime: "2026-12-01"}
	if allow {
		if err := before(ctx, info); err != nil {
			return supplier.ExpireResult{Found: true, Info: info}, err
		}
		m.writes++
	}
	return supplier.ExpireResult{Found: true, Confirmed: true, WriteAttempted: allow, Info: info}, nil
}

func readDeletionResponse(t *testing.T, body []byte) accountDeletionResponse {
	t.Helper()
	var response struct {
		Data accountDeletionResponse `json:"data"`
	}
	if err := common.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.OperationID == "" {
		t.Fatalf("missing operation: %s", body)
	}
	return response.Data
}

func beginDeletionTest(t *testing.T, a *app, cookie *http.Cookie, csrf string, row core.SyncedAccount, requestID string) core.AccountDeletionOperation {
	t.Helper()
	w := request(t, a, "POST", fmt.Sprintf("accounts/%d/delete", row.ID), map[string]any{"request_id": requestID}, cookie, csrf)
	requireStatus(t, w, 202)
	response := readDeletionResponse(t, w.Body.Bytes())
	var op core.AccountDeletionOperation
	if err := a.store.DB.First(&op, response.OperationID).Error; err != nil {
		t.Fatal(err)
	}
	return op
}

func TestAccountDeletionOwnerCSRFAndReplay(t *testing.T) {
	a, owner, cookie, csrf, rows, _ := setupPageSync(t)
	fake := &mockAccountExpirer{}
	a.accountExpirer = fake
	path := fmt.Sprintf("accounts/%d/delete", rows[0].ID)
	body := map[string]any{"request_id": "delete-owner-request-0001"}
	requireStatus(t, request(t, a, "POST", path, body, nil, ""), 401)
	requireStatus(t, request(t, a, "POST", path, body, cookie, ""), 403)
	_, otherCookie, otherCSRF := vendorSession(t, a, "foreign-delete@example.com", "member")
	requireStatus(t, request(t, a, "POST", path, body, otherCookie, otherCSRF), 404)
	requireStatus(t, request(t, a, "POST", fmt.Sprintf("admin/synced-accounts/%d/delete", rows[0].ID), body, cookie, csrf), 403)
	for _, invalid := range []any{map[string]any{}, map[string]any{"request_id": "short"}, map[string]any{"request_id": "delete-owner-request-0001", "owner_id": owner.ID}, map[string]any{"request_id": "delete-owner-request-0001", "supplier_record_id": "450001"}} {
		requireStatus(t, request(t, a, "POST", path, invalid, cookie, csrf), 400)
	}
	op := beginDeletionTest(t, a, cookie, csrf, rows[0], "delete-owner-request-0001")
	w := request(t, a, "POST", path, body, cookie, csrf)
	requireStatus(t, w, 202)
	if readDeletionResponse(t, w.Body.Bytes()).OperationID != fmt.Sprint(op.ID) {
		t.Fatal("replay created another operation")
	}
	if fake.calls != 0 {
		t.Fatal("request performed an upstream mutation inline")
	}
	get := path + "?request_id=delete-owner-request-0001"
	requireStatus(t, request(t, a, "GET", get, nil, cookie, ""), 200)
	requireStatus(t, request(t, a, "GET", get, nil, otherCookie, ""), 404)
	requireStatus(t, request(t, a, "GET", fmt.Sprintf("accounts/%d/delete?operation_id=%d", rows[1].ID, op.ID), nil, cookie, ""), 404)
	if err := a.processAccountDeletion(context.Background(), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if fake.calls != 1 || fake.writes != 1 {
		t.Fatal("wrong upstream count", fake.calls, fake.writes)
	}
	w = request(t, a, "GET", fmt.Sprintf("%s?operation_id=%d", path, op.ID), nil, cookie, "")
	requireStatus(t, w, 200)
	if readDeletionResponse(t, w.Body.Bytes()).Status != "completed" {
		t.Fatal(w.Body.String())
	}
	if strings.Contains(w.Body.String(), "2020") || strings.Contains(w.Body.String(), "plan_start") {
		t.Fatal("implementation details leaked into UI response")
	}
	requireStatus(t, request(t, a, "GET", fmt.Sprintf("accounts/%d", rows[0].ID), nil, cookie, ""), 404)
	requireStatus(t, request(t, a, "POST", fmt.Sprintf("accounts/%d/auth-json", rows[0].ID), map[string]any{"auth_json": map[string]any{}}, cookie, csrf), 404)
	w = request(t, a, "POST", path, body, cookie, csrf)
	requireStatus(t, w, 200)
	if readDeletionResponse(t, w.Body.Bytes()).Status != "completed" {
		t.Fatal("completed replay changed result")
	}
	var archives int64
	a.store.DB.Model(&core.DeletedAccount{}).Where("account_id = ?", rows[0].ID).Count(&archives)
	if archives != 1 {
		t.Fatal("archive was not idempotent", archives)
	}
	var history int64
	a.store.DB.Model(&core.Submission{}).Where("id = ?", rows[0].LatestSubmissionID).Count(&history)
	if history != 1 {
		t.Fatal("submission history deleted")
	}
}

func TestAccountDeletionMissingVersusQueryFailure(t *testing.T) {
	for _, scenario := range []string{"missing", "query_failure", "locked", "ambiguous", "failed_local"} {
		t.Run(scenario, func(t *testing.T) {
			a, _, cookie, csrf, rows, _ := setupPageSync(t)
			row := rows[0]
			if scenario == "failed_local" {
				row = rows[2]
			}
			fake := &mockAccountExpirer{run: func(ctx context.Context, target supplier.Target, allow bool, before func(context.Context, supplier.ExpireInfo) error) (supplier.ExpireResult, error) {
				switch scenario {
				case "missing":
					return supplier.ExpireResult{Confirmed: true}, nil
				case "locked":
					return supplier.ExpireResult{Found: true}, &supplier.Error{Code: supplier.CodePlanDatesLocked, Message: "PRIVATE_BODY_MUST_NOT_LEAK"}
				case "ambiguous":
					return supplier.ExpireResult{}, &supplier.Error{Code: supplier.CodeAccountAmbiguous, Message: "PRIVATE_BODY_MUST_NOT_LEAK"}
				default:
					return supplier.ExpireResult{}, &supplier.Error{Code: supplier.CodeTimeout, Message: "PRIVATE_BODY_MUST_NOT_LEAK"}
				}
			}}
			a.accountExpirer = fake
			op := beginDeletionTest(t, a, cookie, csrf, row, "delete-missing-request-0001")
			if err := a.processAccountDeletion(context.Background(), time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			var current core.SyncedAccount
			a.store.DB.First(&current, row.ID)
			w := request(t, a, "GET", fmt.Sprintf("accounts/%d/delete?operation_id=%d", row.ID, op.ID), nil, cookie, "")
			requireStatus(t, w, 200)
			result := readDeletionResponse(t, w.Body.Bytes())
			if strings.Contains(w.Body.String(), "PRIVATE_BODY") {
				t.Fatal("unsafe upstream error returned")
			}
			if scenario == "missing" || scenario == "failed_local" {
				if current.DeletedAt == nil || result.Status != "completed" {
					t.Fatal("deletion did not complete", result, current.DeletedAt)
				}
			} else if current.DeletedAt != nil || result.Status != "failed" || current.DeletionStatus != "" {
				t.Fatal("unconfirmed account was deleted", result, current)
			}
			if scenario == "failed_local" && fake.calls != 0 {
				t.Fatal("local failed record touched another owner's supplier account")
			}
		})
	}
}

func TestAccountDeletionTimeoutResumesReadsOnly(t *testing.T) {
	a, _, cookie, csrf, rows, _ := setupPageSync(t)
	fake := &mockAccountExpirer{}
	fake.run = func(ctx context.Context, target supplier.Target, allow bool, before func(context.Context, supplier.ExpireInfo) error) (supplier.ExpireResult, error) {
		info := supplier.ExpireInfo{RecordID: "450001", Account: target.Account, Plan: "claude/cc-max-20x", PlanStartTime: "2026-11-01", PlanEndTime: "2026-12-01"}
		if allow {
			if err := before(ctx, info); err != nil {
				t.Fatal(err)
			}
			fake.writes++
			return supplier.ExpireResult{Found: true, WriteAttempted: true, Info: info}, &supplier.Error{Code: supplier.CodePlanDatesUnconfirmed, Message: "private socket payload"}
		}
		return supplier.ExpireResult{Found: true, Confirmed: true, Info: info}, nil
	}
	a.accountExpirer = fake
	op := beginDeletionTest(t, a, cookie, csrf, rows[0], "delete-timeout-request-0001")
	if err := a.processAccountDeletion(context.Background(), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	a.store.DB.First(&op, op.ID)
	if op.Status != "awaiting_verification" {
		t.Fatal(op.Status)
	}
	var row core.SyncedAccount
	a.store.DB.First(&row, rows[0].ID)
	if row.DeletedAt != nil {
		t.Fatal("timed-out mutation deleted local account")
	}
	// The new process sees persisted awaiting_verification and may only read.
	if err := a.processAccountDeletion(context.Background(), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	a.store.DB.First(&op, op.ID)
	if op.Status != "completed" || fake.writes != 1 || len(fake.allowWrites) != 2 || fake.allowWrites[1] {
		t.Fatal("uncertain PUT was replayed", op.Status, fake.allowWrites)
	}
}

func TestAccountDeletionSharedLeaseAndSupplierCooldown(t *testing.T) {
	a, _, cookie, csrf, rows, _ := setupPageSync(t)
	fake := &mockAccountExpirer{run: func(ctx context.Context, target supplier.Target, allow bool, before func(context.Context, supplier.ExpireInfo) error) (supplier.ExpireResult, error) {
		return supplier.ExpireResult{}, &supplier.Error{Code: supplier.CodeRateLimited, RetryAfterSeconds: 80, RetryAt: time.Now().UTC().Add(80 * time.Second), Deferred: true}
	}}
	a.accountExpirer = fake
	op := beginDeletionTest(t, a, cookie, csrf, rows[0], "delete-cooldown-request-0001")
	a.redis.Set(context.Background(), accountSyncLeaseKey, "another-sync", time.Minute)
	if err := a.processAccountDeletion(context.Background(), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if fake.calls != 0 {
		t.Fatal("bypassed collection lock")
	}
	if a.redis.Get(context.Background(), accountSyncLeaseKey).Val() != "another-sync" {
		t.Fatal("deleted foreign lease")
	}
	a.redis.Del(context.Background(), accountSyncLeaseKey)
	if err := a.processAccountDeletion(context.Background(), time.Now().Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	a.store.DB.First(&op, op.ID)
	if op.Status != "pending" || op.NextRunAt.Before(time.Now().Add(70*time.Second)) {
		t.Fatal("cooldown not retained", op.Status, op.NextRunAt)
	}
	if err := a.processAccountDeletion(context.Background(), time.Now().Add(15*time.Second)); err != nil {
		t.Fatal(err)
	}
	if fake.calls != 1 {
		t.Fatal("queried during cooldown")
	}
}

func TestAccountDeletionExplicitRejectionAndManualRetry(t *testing.T) {
	a, _, cookie, csrf, rows, _ := setupPageSync(t)
	fake := &mockAccountExpirer{run: func(ctx context.Context, target supplier.Target, allow bool, before func(context.Context, supplier.ExpireInfo) error) (supplier.ExpireResult, error) {
		info := supplier.ExpireInfo{RecordID: "450001", Account: target.Account, PlanStartTime: "2026-11-01", PlanEndTime: "2026-12-01"}
		if err := before(ctx, info); err != nil {
			t.Fatal(err)
		}
		return supplier.ExpireResult{Found: true, WriteAttempted: true, Info: info}, &supplier.Error{Code: supplier.CodePlanDatesRejected}
	}}
	a.accountExpirer = fake
	op := beginDeletionTest(t, a, cookie, csrf, rows[0], "delete-rejected-request-0001")
	if err := a.processAccountDeletion(context.Background(), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	a.store.DB.First(&op, op.ID)
	if op.Status != "failed" {
		t.Fatal("explicit rejection stayed busy", op.Status)
	}
	w := request(t, a, "POST", fmt.Sprintf("accounts/%d/delete", rows[0].ID), map[string]any{"request_id": "delete-rejected-request-0001"}, cookie, csrf)
	requireStatus(t, w, 200)
	if readDeletionResponse(t, w.Body.Bytes()).Status != "failed" {
		t.Fatal("same request retried a write")
	}
	op2 := beginDeletionTest(t, a, cookie, csrf, rows[0], "delete-rejected-request-0002")
	if op2.ID == op.ID {
		t.Fatal("manual retry erased earlier operation")
	}
}

func TestAccountDeletionPersistenceFailureDoesNotRepeatSupplierWrite(t *testing.T) {
	a, _, cookie, csrf, rows, _ := setupPageSync(t)
	fake := &mockAccountExpirer{}
	a.accountExpirer = fake
	op := beginDeletionTest(t, a, cookie, csrf, rows[0], "delete-persist-request-0001")
	if err := a.store.DB.Callback().Create().Before("gorm:create").Register("fail-delete-archive-once", func(tx *gorm.DB) {
		if tx.Statement.Table == "deleted_account" {
			tx.AddError(errors.New("simulated persistence failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.processAccountDeletion(context.Background(), time.Now().Add(time.Second)); err == nil {
		t.Fatal("expected local transaction failure")
	}
	a.store.DB.Callback().Create().Remove("fail-delete-archive-once")
	a.store.DB.First(&op, op.ID)
	if op.Status != "upstream_confirmed" {
		t.Fatal("lost confirmed mutation", op.Status)
	}
	var row core.SyncedAccount
	a.store.DB.First(&row, rows[0].ID)
	if row.DeletedAt != nil {
		t.Fatal("partial transaction hid account")
	}
	if op.NextRunAt.Before(time.Now().Add(20 * time.Second)) {
		t.Fatal("failed operation was not deferred", op.NextRunAt)
	}
	if err := a.processAccountDeletion(context.Background(), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if fake.calls != 1 || fake.writes != 1 {
		t.Fatal("retried supplier after local failure", fake.calls, fake.writes)
	}
	a.store.DB.First(&op, op.ID)
	if op.Status != "completed" {
		t.Fatal(op.Status)
	}
}

func TestAccountDeletionUnsentWriteAllowsExplicitRetry(t *testing.T) {
	a, _, cookie, csrf, rows, _ := setupPageSync(t)
	fake := &mockAccountExpirer{run: func(ctx context.Context, target supplier.Target, allow bool, before func(context.Context, supplier.ExpireInfo) error) (supplier.ExpireResult, error) {
		info := supplier.ExpireInfo{RecordID: "450001", Account: target.Account, PlanStartTime: "2026-11-01", PlanEndTime: "2026-12-01"}
		if err := before(ctx, info); err != nil {
			t.Fatal(err)
		}
		return supplier.ExpireResult{Found: true, Info: info}, &supplier.Error{Code: supplier.CodePlanDatesNotSent, RequestNotSent: true}
	}}
	a.accountExpirer = fake
	op := beginDeletionTest(t, a, cookie, csrf, rows[0], "delete-unsent-request-0001")
	if err := a.processAccountDeletion(context.Background(), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	a.store.DB.First(&op, op.ID)
	var row core.SyncedAccount
	a.store.DB.First(&row, rows[0].ID)
	if op.Status != "failed" || row.DeletedAt != nil || row.DeletionStatus != "" {
		t.Fatal("definitely-unsent write remained busy", op.Status, row.DeletionStatus)
	}
	if err := a.processAccountDeletion(context.Background(), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if fake.calls != 1 {
		t.Fatal("unsent write automatically replayed")
	}
	op2 := beginDeletionTest(t, a, cookie, csrf, rows[0], "delete-unsent-request-0002")
	if op.ID == op2.ID {
		t.Fatal("explicit retry reused failed operation")
	}
}
