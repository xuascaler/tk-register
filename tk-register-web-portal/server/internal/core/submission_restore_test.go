package core

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"goapi-tk-register/server/internal/supplier"
)

type restoreFunc func(context.Context, string, string, supplier.Target, map[string]any) (supplier.RestoreResult, error)

func (f restoreFunc) RestoreAccount(c context.Context, u, p string, t supplier.Target, i map[string]any) (supplier.RestoreResult, error) {
	return f(c, u, p, t, i)
}

func archivedSubmissionFixture(t *testing.T) (*VendorService, User, uint64, map[string]any, Submission, SyncedAccount) {
	t.Helper()
	service, user, token, params := apiExecutionFixture(t)
	ctx := context.Background()
	service.Transport = storageTransport(func(*http.Request) (*http.Response, error) {
		return apiRemoteResponse(200, `{"id":123,"alias":"claude-fixture"}`), nil
	})
	response, err := service.CreateAPI(ctx, user.ID, token, params)
	if err != nil {
		t.Fatal(err)
	}
	var old Submission
	if err = service.Store.DB.First(&old, response.SubmissionID).Error; err != nil {
		t.Fatal(err)
	}
	targets, err := service.Store.CollectionTargets(ctx, user.ID, 0)
	if err != nil || len(targets) != 1 {
		t.Fatal(targets, err)
	}
	target := targets[0]
	now := time.Now().UTC()
	op, _, err := service.Store.BeginAccountDeletion(ctx, target.ID, user.ID, false, "delete-restore-fixture", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Store.ConfirmAccountDeletion(ctx, op.ID, "upstream_dates_updated", "123", now); err != nil {
		t.Fatal(err)
	}
	if err = service.Store.FinishAccountDeletion(ctx, op.ID, now); err != nil {
		t.Fatal(err)
	}
	if err = service.Store.PatchSettings(ctx, map[string]string{"account_sync_website_username": "supplier-user", "account_sync_website_password": "supplier-password"}, user.ID); err != nil {
		t.Fatal(err)
	}
	return service, user, token, params, old, target
}

func TestRestoreCrossUserPreservesHistoryAndChangesCurrentOwner(t *testing.T) {
	service, owner, _, params, old, oldAccount := archivedSubmissionFixture(t)
	ctx := context.Background()
	newUser := User{Email: "new-owner@example.com", PasswordHash: "fixture-hash", Role: "member", Status: "active", EmailVerifiedAt: time.Now().UTC()}
	if err := service.Store.CreateUser(ctx, &newUser); err != nil {
		t.Fatal(err)
	}
	generated, err := service.Store.GenerateAPIToken(ctx, newUser.ID)
	if err != nil {
		t.Fatal(err)
	}
	tokenID, _ := ParseBusinessID(generated.Token.ID)
	var updates atomic.Int32
	service.Transport = storageTransport(func(*http.Request) (*http.Response, error) { t.Fatal("restore called create"); return nil, nil })
	service.Restorer = restoreFunc(func(c context.Context, u, p string, target supplier.Target, input map[string]any) (supplier.RestoreResult, error) {
		updates.Add(1)
		if target.RecordID != "123" || u != "supplier-user" || input["password"] != params["password"] {
			t.Fatal("wrong restore target")
		}
		if err := target.BeforeUpdate(c, supplier.EditInfo{RecordID: "123", Account: target.Account}); err != nil {
			return supplier.RestoreResult{}, err
		}
		return supplier.RestoreResult{Found: true, Confirmed: true, WriteAttempted: true, RecordID: "123", Alias: "claude-温州润晨-test"}, nil
	})
	result, err := service.CreateAPI(ctx, newUser.ID, tokenID, params)
	if err != nil || result.HTTPStatus != 200 || result.SubmitMode != "restore_update" {
		t.Fatal(result, err)
	}
	if strings.Contains(string(result.Body), "温州润晨") || !strings.Contains(string(result.Body), `"id":123`) {
		t.Fatal("API compatibility/masking", string(result.Body))
	}
	row, err := service.Store.LookupSyncedAccountForUser(ctx, newUser.ID, "tk-register-0", params["account"].(string))
	if err != nil || row.RemoteID == nil || *row.RemoteID != "123" {
		t.Fatal(row, err)
	}
	if _, err = service.Store.SyncedAccountForUser(ctx, owner.ID, oldAccount.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("old owner gained current account", err)
	}
	if _, err = service.RefreshStatus(ctx, owner.ID, old.ID, true); err == nil {
		t.Fatal("old owner queried reassigned supplier account")
	}
	if _, err = service.Query(ctx, owner.ID, old.ID, "bills", QueryInput{}); err == nil {
		t.Fatal("old owner queried reassigned billing")
	}
	var before Submission
	service.Store.DB.First(&before, old.ID)
	if before.UserID != owner.ID || before.Status != "accepted" {
		t.Fatal("history rewritten")
	}
	var archive DeletedAccount
	service.Store.DB.First(&archive)
	if archive.OwnerUserID != owner.ID {
		t.Fatal("archive transferred")
	}
	var current Submission
	service.Store.DB.First(&current, result.SubmissionID)
	if current.RestoredFromDeletedID == nil || current.UserID != newUser.ID || current.Status != "accepted" {
		t.Fatal(current)
	}
	var account SyncedAccount
	service.Store.DB.Where("current_submission_id = ?", current.ID).First(&account)
	if account.IdentityKey == oldAccount.IdentityKey || account.SupplierRecordID == nil || *account.SupplierRecordID != "123" {
		t.Fatal("new lifetime/mapping missing", account)
	}
	var jobs int64
	service.Store.DB.Model(&PostSubmissionSyncJob{}).Where("submission_id = ?", current.ID).Count(&jobs)
	if jobs != 1 {
		t.Fatal("missing collection job", jobs)
	}
	if _, err = service.CreateAPI(ctx, owner.ID, tokenID, params); err == nil {
		t.Fatal("restored active account submitted again")
	}
	if updates.Load() != 1 {
		t.Fatal("duplicate update")
	}
}

func TestRestoreFallbackOnlyOnConfirmedAbsence(t *testing.T) {
	for _, tc := range []struct {
		name        string
		result      supplier.RestoreResult
		problem     error
		posts       int
		state, mode string
	}{
		{"absent", supplier.RestoreResult{}, nil, 1, "accepted", "create_fallback"},
		{"read_timeout", supplier.RestoreResult{}, &supplier.Error{Code: supplier.CodeTimeout, Message: "timeout"}, 0, "failed", "restore_update"},
		{"auth_failure", supplier.RestoreResult{}, &supplier.Error{Code: supplier.CodeCredentialsInvalid, Message: "login failed"}, 0, "failed", "restore_update"},
		{"ambiguous", supplier.RestoreResult{}, &supplier.Error{Code: supplier.CodeAccountAmbiguous, Message: "ambiguous"}, 0, "failed", "restore_update"},
		{"rejected", supplier.RestoreResult{Found: true, WriteAttempted: true}, &supplier.Error{Code: supplier.CodeUpdateRejected, Message: "rejected"}, 0, "failed", "restore_update"},
		{"write_timeout", supplier.RestoreResult{Found: true, WriteAttempted: true}, &supplier.Error{Code: supplier.CodeUpdateUnconfirmed, Message: "uncertain"}, 0, "unknown", "restore_update"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, user, token, params, _, account := archivedSubmissionFixture(t)
			posts := 0
			service.Transport = storageTransport(func(*http.Request) (*http.Response, error) {
				posts++
				return apiRemoteResponse(201, `{"id":456,"alias":"new"}`), nil
			})
			service.Restorer = restoreFunc(func(context.Context, string, string, supplier.Target, map[string]any) (supplier.RestoreResult, error) {
				return tc.result, tc.problem
			})
			result, err := service.CreateAPI(context.Background(), user.ID, token, params)
			if err != nil {
				t.Fatal(err)
			}
			var saved Submission
			service.Store.DB.First(&saved, result.SubmissionID)
			if posts != tc.posts || saved.Status != tc.state || saved.SubmitMode != tc.mode {
				t.Fatal(posts, saved.Status, saved.SubmitMode, result)
			}
			service.Store.DB.First(&account, account.ID)
			if tc.state != "accepted" && account.DeletedAt == nil {
				t.Fatal("failed/uncertain update restored account")
			}
			if tc.state == "unknown" {
				if saved.ActiveAccountKey == nil {
					t.Fatal("uncertain restoration lost durable lock")
				}
				if err := service.Store.migrateVendors(); err != nil {
					t.Fatal(err)
				}
				if _, err = service.CreateAPI(context.Background(), user.ID, token, params); err == nil {
					t.Fatal("uncertain update replayed")
				}
			}
		})
	}
}

func TestRestoreWebReplayAndInterruptedExecution(t *testing.T) {
	service, user, _, params, _, _ := archivedSubmissionFixture(t)
	ctx := context.Background()
	_, version, err := service.currentVersion(ctx, 1, "submit_account")
	if err != nil {
		t.Fatal(err)
	}
	input := CreateSubmissionInput{VendorID: "1", ProtocolVersionID: idString(version.ID), ClientRequestKey: "restore-web-replay-00001", Parameters: params}
	calls := 0
	service.Restorer = restoreFunc(func(ctx context.Context, _, _ string, target supplier.Target, _ map[string]any) (supplier.RestoreResult, error) {
		calls++
		if e := target.BeforeUpdate(ctx, supplier.EditInfo{Account: target.Account, RecordID: "123"}); e != nil {
			return supplier.RestoreResult{}, e
		}
		return supplier.RestoreResult{Found: true, Confirmed: true, RecordID: "123"}, nil
	})
	first, err := service.Create(ctx, user.ID, input)
	if err != nil || first.Submission.Status != "accepted" {
		t.Fatal(first, err)
	}
	second, err := service.Create(ctx, user.ID, input)
	if err != nil || second.Submission.ID != first.Submission.ID || calls != 1 {
		t.Fatal("idempotency failed", second, err, calls)
	}
	// Simulate a process interrupted after a new durable restore record was saved.
	saved := Submission{}
	service.Store.DB.First(&saved, first.Submission.ID)
	service.Store.DB.Model(&saved).Updates(map[string]any{"status": "running", "created_at": time.Now().UTC().Add(-3 * time.Minute)})
	if err := service.Store.RecoverVendorExecutions(ctx); err != nil {
		t.Fatal(err)
	}
	service.Store.DB.First(&saved, saved.ID)
	if saved.Status != "unknown" || saved.ActiveAccountKey == nil {
		t.Fatal("interrupted restoration unlocked", saved)
	}
}

func TestConcurrentRestoreAndCaseVariantCannotWriteTwice(t *testing.T) {
	service, user, token, params, _, _ := archivedSubmissionFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	service.Restorer = restoreFunc(func(ctx context.Context, _, _ string, target supplier.Target, _ map[string]any) (supplier.RestoreResult, error) {
		calls.Add(1)
		if err := target.BeforeUpdate(ctx, supplier.EditInfo{RecordID: "123", Account: target.Account}); err != nil {
			return supplier.RestoreResult{}, err
		}
		close(entered)
		<-release
		return supplier.RestoreResult{Found: true, Confirmed: true, WriteAttempted: true, RecordID: "123"}, nil
	})
	done := make(chan error, 1)
	go func() { _, err := service.CreateAPI(context.Background(), user.ID, token, params); done <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first restore did not start")
	}
	variant := map[string]any{}
	for k, v := range params {
		variant[k] = v
	}
	variant["account"] = strings.ToUpper(params["account"].(string))
	_, err := service.CreateAPI(context.Background(), user.ID, token, variant)
	close(release)
	if err == nil {
		t.Fatal("concurrent case-variant restore was accepted")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("duplicate restore calls", calls.Load())
	}
}

func TestFallbackUnknownCannotReplayAfterRestart(t *testing.T) {
	service, user, token, params, _, _ := archivedSubmissionFixture(t)
	service.Restorer = restoreFunc(func(context.Context, string, string, supplier.Target, map[string]any) (supplier.RestoreResult, error) {
		return supplier.RestoreResult{}, nil
	})
	calls := 0
	service.Transport = storageTransport(func(*http.Request) (*http.Response, error) { calls++; return nil, context.DeadlineExceeded })
	result, err := service.CreateAPI(context.Background(), user.ID, token, params)
	if err != nil {
		t.Fatal(err)
	}
	var saved Submission
	service.Store.DB.First(&saved, result.SubmissionID)
	if saved.Status != "unknown" || saved.SubmitMode != "create_fallback" || saved.ActiveAccountKey == nil {
		t.Fatal(saved)
	}
	if err := service.Store.migrateVendors(); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateAPI(context.Background(), user.ID, token, params); err == nil {
		t.Fatal("uncertain fallback replayed")
	}
	if calls != 1 {
		t.Fatal("duplicate POST", calls)
	}
}
