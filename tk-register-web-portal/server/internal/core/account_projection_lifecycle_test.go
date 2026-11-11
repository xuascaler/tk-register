package core

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestSubmissionLifecycleProjectsWithoutAccountReads(t *testing.T) {
	for _, source := range []string{"web", "api"} {
		for _, tc := range []struct {
			name, body, state string
			status            int
		}{
			{"accepted", `{"id":13201,"alias":"test"}`, "accepted", 201},
			{"failed", `{"error":"invalid account"}`, "failed", 400},
			{"unknown", `{"error":"unavailable"}`, "unknown", 503},
		} {
			t.Run(source+"/"+tc.name, func(t *testing.T) {
				service, owner, tokenID, params := apiExecutionFixture(t)
				ctx := context.Background()
				calls := 0
				var runningID uint64
				service.Transport = storageTransport(func(*http.Request) (*http.Response, error) {
					calls++
					var running SyncedAccount
					if err := service.Store.DB.Where("user_id = ? AND account = ?", owner.ID, params["account"]).First(&running).Error; err != nil {
						t.Fatal("account must exist before contacting the supplier", err)
					}
					if running.SubmissionStatus != "running" || running.CurrentSubmissionID != nil {
						t.Fatal("incorrect in-flight account projection", running)
					}
					runningID = running.ID
					return apiRemoteResponse(tc.status, tc.body), nil
				})
				var err error
				if source == "api" {
					_, err = service.CreateAPI(ctx, owner.ID, tokenID, params)
				} else {
					vendor, version, lookupErr := service.currentVersion(ctx, 1, "submit_account")
					if lookupErr != nil {
						t.Fatal(lookupErr)
					}
					_, err = service.Create(ctx, owner.ID, CreateSubmissionInput{VendorID: idString(vendor.ID), ProtocolVersionID: idString(version.ID), ClientRequestKey: "projection-web-lifecycle-0001", Parameters: params})
				}
				if err != nil || calls != 1 {
					t.Fatal("submission failed or was replayed", err, calls)
				}
				var projected SyncedAccount
				if err := service.Store.DB.First(&projected, runningID).Error; err != nil {
					t.Fatal(err)
				}
				if projected.SubmissionStatus != tc.state || projected.LatestSubmissionID == 0 || (projected.CurrentSubmissionID != nil) != (tc.state == "accepted") {
					t.Fatal("terminal result requires an account read to become visible", projected)
				}
				var jobs int64
				if err := service.Store.DB.Model(&PostSubmissionSyncJob{}).Count(&jobs).Error; err != nil || (jobs == 1) != (tc.state == "accepted") {
					t.Fatal("incorrect follow-up scheduling", jobs, err)
				}
			})
		}
	}
}

func TestSubmissionCreationRollsBackWhenAccountProjectionFails(t *testing.T) {
	service, owner, tokenID, params := apiExecutionFixture(t)
	failure := errors.New("test account projection unavailable")
	if err := service.Store.DB.Callback().Create().Before("gorm:create").Register("test:projection-create-failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "synced_accounts" {
			tx.AddError(failure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	service.Transport = storageTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return apiRemoteResponse(201, `{"id":13202}`), nil
	})
	if _, err := service.CreateAPI(context.Background(), owner.ID, tokenID, params); !errors.Is(err, failure) || calls != 0 {
		t.Fatal("supplier called without durable account projection", err, calls)
	}
	for _, model := range []any{&Submission{}, &ProtocolExecution{}, &SyncedAccount{}} {
		var count int64
		if err := service.Store.DB.Model(model).Count(&count).Error; err != nil || count != 0 {
			t.Fatal("partial submission transaction survived", count, err)
		}
	}
}

func TestInterruptedSubmissionRecoveryProjectsOnlyExpiredRunningAccounts(t *testing.T) {
	store, owner, vendor, version := syncFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	stale := syncSubmission(t, store, owner.ID, vendor.ID, version.ID, "stale@example.com", "claude/cc-pro", "running", "", now.Add(-3*time.Minute))
	fresh := syncSubmission(t, store, owner.ID, vendor.ID, version.ID, "fresh@example.com", "claude/cc-pro", "running", "", now)
	accepted := syncSubmission(t, store, owner.ID, vendor.ID, version.ID, "accepted@example.com", "claude/cc-pro", "accepted", "13203", now.Add(-4*time.Minute))
	for _, sub := range []Submission{stale, fresh, accepted} {
		operation := "push"
		if sub.Status == "accepted" {
			operation = "query"
		}
		execution := ProtocolExecution{SubmissionID: sub.ID, ProtocolVersionID: version.ID, RequestID: "recover-projection-" + idString(sub.ID), Operation: operation, Status: "running", RequestJSON: "{}", CreatedAt: sub.CreatedAt}
		if err := store.DB.Create(&execution).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := store.EnsureSyncedAccounts(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := store.RecoverVendorExecutions(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		sub       Submission
		state     string
		execState string
	}{{stale, "unknown", "unknown"}, {fresh, "running", "running"}, {accepted, "accepted", "unknown"}} {
		var sub Submission
		var account SyncedAccount
		var execution ProtocolExecution
		if err := store.DB.First(&sub, tc.sub.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := store.DB.Where("latest_submission_id = ?", sub.ID).First(&account).Error; err != nil {
			t.Fatal(err)
		}
		if err := store.DB.Where("submission_id = ?", sub.ID).First(&execution).Error; err != nil {
			t.Fatal(err)
		}
		if sub.Status != tc.state || account.SubmissionStatus != tc.state || execution.Status != tc.execState {
			t.Fatalf("recovery mismatch for submission %d: submission=%s account=%s execution=%s", sub.ID, sub.Status, account.SubmissionStatus, execution.Status)
		}
		if (account.CurrentSubmissionID != nil) != (tc.state == "accepted") {
			t.Fatal("recovery changed acceptance", account)
		}
	}
}

func TestMetricsCompletionProjectsPlatformIDWithoutAccountReads(t *testing.T) {
	service, owner, tokenID, params := apiExecutionFixture(t)
	service.Transport = storageTransport(func(*http.Request) (*http.Response, error) {
		return apiRemoteResponse(201, `{"id":13204}`), nil
	})
	result, err := service.CreateAPI(context.Background(), owner.ID, tokenID, params)
	if err != nil {
		t.Fatal(err)
	}
	var sub Submission
	if err := service.Store.DB.First(&sub, result.SubmissionID).Error; err != nil {
		t.Fatal(err)
	}
	execution := ProtocolExecution{SubmissionID: sub.ID, ProtocolVersionID: sub.ProtocolVersionID, RequestID: "metrics-projection-0001", Operation: "query", Status: "running", RequestJSON: "{}"}
	if err := service.Store.DB.Create(&execution).Error; err != nil {
		t.Fatal(err)
	}
	platformID, status := "18123", "active"
	metrics := &MetricResult{Account: &MetricAccount{AID: &platformID, Status: &status}, QueriedAt: time.Now().UTC()}
	if _, err := service.finish(owner.ID, sub, execution, executionResult{status: "succeeded", metrics: metrics, data: metrics}, nil, "test-vendor-credential"); err != nil {
		t.Fatal(err)
	}
	var account SyncedAccount
	if err := service.Store.DB.Where("current_submission_id = ?", sub.ID).First(&account).Error; err != nil || account.PlatformAccountID == nil || *account.PlatformAccountID != platformID || account.SubmissionStatus != "accepted" {
		t.Fatal("metrics platform ID not projected", account, err)
	}
}
