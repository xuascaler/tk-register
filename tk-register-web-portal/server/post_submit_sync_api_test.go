package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"goapi-tk-register/server/internal/core"
	"goapi-tk-register/server/internal/supplier"
)

func TestPostSubmissionSyncAPIToSavedLookup(t *testing.T) {
	a, _, _ := fixture(t)
	admin, _, _ := vendorSession(t, a, "sync-admin@example.com", "admin")
	owner, _, _ := vendorSession(t, a, "sync-api-owner@example.com", "member")
	configuredVendor(t, a, admin.ID)
	// The post-submit read is independent of the periodic collection switch.
	configureSync(t, a, admin.ID, false)
	token := lookupToken(t, a, owner.ID)
	pushes := 0
	a.vendorTransport = vendorRoundTrip(func(r *http.Request) (*http.Response, error) {
		pushes++
		return remoteResponse(http.StatusCreated, `{"id":450001,"alias":"claude-温州润晨-test"}`), nil
	})
	fake := &mockPageCollector{}
	fake.run = func(ctx context.Context, targets []supplier.Target) ([]supplier.AccountResult, error) {
		if len(targets) != 1 || targets[0].Account != "supplier-account@example.com" {
			t.Fatalf("wrong automatic sync target: %+v", targets)
		}
		if fake.calls < 3 {
			return nil, &supplier.Error{Code: supplier.CodeUnavailable}
		}
		return []supplier.AccountResult{pageRecord(targets[0])}, nil
	}
	a.accountPageCollector = fake
	body, err := json.Marshal(submissionParameters())
	if err != nil {
		t.Fatal(err)
	}
	w := apiRequest(t, a, "POST", claudeAccountsAPIPath, token.PlaintextToken, "application/json", string(body))
	requireStatus(t, w, http.StatusCreated)
	if pushes != 1 || fake.calls != 0 || w.Header().Get("X-Submission-ID") == "" || w.Header().Get("X-Request-ID") == "" || !strings.Contains(w.Body.String(), "claude-xxxxx-test") {
		t.Fatal("API did not return the existing upstream response independently of sync", w.Body.String())
	}
	var job core.PostSubmissionSyncJob
	if err = a.store.DB.Where("submission_id = ?", w.Header().Get("X-Submission-ID")).First(&job).Error; err != nil {
		t.Fatal(err)
	}
	if job.Status != "pending" || job.UserID != owner.ID || job.NextRunAt.Sub(job.CreatedAt) != 3*time.Second {
		t.Fatalf("incorrect queued job: %+v", job)
	}
	path := lookupURL(owner.Email, "supplier-account@example.com")
	before := apiRequest(t, a, "GET", path, token.PlaintextToken, "", "")
	requireStatus(t, before, 200)
	if lookupData(t, before).Snapshot != nil {
		t.Fatal("API acceptance fabricated usage before supplier sync")
	}
	if err = a.processPostSubmissionSync(context.Background(), job.NextRunAt.Add(-time.Millisecond)); err != nil || fake.calls != 0 {
		t.Fatal("automatic sync ran before the three-second delay", err)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		next := readPostSubmissionJob(t, a, job.ID)
		if err = a.processPostSubmissionSync(context.Background(), next.NextRunAt); err != nil {
			t.Fatal(err)
		}
		if attempt < 3 && readPostSubmissionJob(t, a, job.ID).Status != "pending" {
			t.Fatal("transient supplier failure did not schedule a retry")
		}
	}
	after := apiRequest(t, a, "GET", path, token.PlaintextToken, "", "")
	requireStatus(t, after, 200)
	data := lookupData(t, after)
	if data.Snapshot == nil || data.Snapshot.AccountStatus == nil || *data.Snapshot.AccountStatus != "active" || data.Snapshot.CostTotal == nil || *data.Snapshot.CostTotal != 123.45 || data.Snapshot.CostTotalUSD == nil || data.Snapshot.Usage7dPercent == nil || *data.Snapshot.Usage7dPercent != 42 || data.LastSuccessAt == nil {
		t.Fatalf("lookup did not receive the existing account snapshot fields: %+v", data)
	}
	if readPostSubmissionJob(t, a, job.ID).Status != "succeeded" || readPostSubmissionJob(t, a, job.ID).AttemptCount != 3 {
		t.Fatal("automatic job did not finish")
	}
	if err = a.processPostSubmissionSync(context.Background(), job.NextRunAt.Add(time.Minute)); err != nil || fake.calls != 3 || pushes != 1 {
		t.Fatal("automatic sync repeated or resubmitted the account", err)
	}
}
