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
	if job.Status != "pending" || job.UserID != owner.ID || job.NextRunAt.Sub(job.CreatedAt) != 10*time.Second {
		t.Fatalf("incorrect queued job: %+v", job)
	}
	path := lookupURL(owner.Email, "supplier-account@example.com")
	before := apiRequest(t, a, "GET", path, token.PlaintextToken, "", "")
	requireStatus(t, before, 200)
	if lookupData(t, before).Snapshot != nil {
		t.Fatal("API acceptance fabricated usage before supplier sync")
	}
	if err = a.processPostSubmissionSync(context.Background(), job.NextRunAt.Add(-time.Millisecond)); err != nil || fake.calls != 0 {
		t.Fatal("automatic sync ran before the ten-second delay", err)
	}
	for attempt := 1; attempt <= 5; attempt++ {
		next := readPostSubmissionJob(t, a, job.ID)
		if err = a.processPostSubmissionSync(context.Background(), next.NextRunAt); err != nil {
			t.Fatal(err)
		}
		if attempt < 5 && readPostSubmissionJob(t, a, job.ID).Status != "pending" {
			t.Fatal("series did not continue after a failed or successful round")
		}
	}
	after := apiRequest(t, a, "GET", path, token.PlaintextToken, "", "")
	requireStatus(t, after, 200)
	data := lookupData(t, after)
	if data.Snapshot == nil || data.Snapshot.AccountStatus == nil || *data.Snapshot.AccountStatus != "active" || data.Snapshot.CostTotal == nil || *data.Snapshot.CostTotal != 123.45 || data.Snapshot.CostTotalUSD == nil || data.Snapshot.Usage7dPercent == nil || *data.Snapshot.Usage7dPercent != 42 || data.LastSuccessAt == nil {
		t.Fatalf("lookup did not receive the existing account snapshot fields: %+v", data)
	}
	if readPostSubmissionJob(t, a, job.ID).Status != "succeeded" || readPostSubmissionJob(t, a, job.ID).AttemptCount != 5 {
		t.Fatal("automatic job did not finish")
	}
	if err = a.processPostSubmissionSync(context.Background(), job.NextRunAt.Add(time.Minute)); err != nil || fake.calls != 5 || pushes != 1 {
		t.Fatal("automatic sync repeated or resubmitted the account", err)
	}
}

func TestPostSubmissionSyncWebAcceptanceQueuesFiveRoundsWithoutRepeatingPush(t *testing.T) {
	a, _, _ := fixture(t)
	admin, _, _ := vendorSession(t, a, "web-sync-admin@example.com", "admin")
	owner, cookie, csrf := vendorSession(t, a, "web-sync-owner@example.com", "member")
	vendor := configuredVendor(t, a, admin.ID)
	configureSync(t, a, admin.ID, false)
	pushes := 0
	a.vendorTransport = vendorRoundTrip(func(r *http.Request) (*http.Response, error) {
		pushes++
		return remoteResponse(http.StatusCreated, `{"id":450001,"alias":"test"}`), nil
	})
	fake := &mockPageCollector{}
	fake.run = func(ctx context.Context, targets []supplier.Target) ([]supplier.AccountResult, error) {
		return []supplier.AccountResult{pageRecord(targets[0])}, nil
	}
	a.accountPageCollector = fake
	input := createInput(vendor, "web-five-observations")
	w := request(t, a, "POST", "submissions", input, cookie, csrf)
	requireStatus(t, w, 200)
	detail := submissionDetail(t, w.Body.Bytes())
	var job core.PostSubmissionSyncJob
	if err := a.store.DB.Where("submission_id = ?", detail.Submission.ID).First(&job).Error; err != nil {
		t.Fatal(err)
	}
	if job.UserID != owner.ID || job.NextRunAt.Sub(job.CreatedAt) != 10*time.Second || fake.calls != 0 || pushes != 1 {
		t.Fatal("web submission did not queue independently of its response")
	}
	// Replaying the same web submission must not enqueue another series.
	requireStatus(t, request(t, a, "POST", "submissions", input, cookie, csrf), 200)
	var jobs int64
	if err := a.store.DB.Model(&core.PostSubmissionSyncJob{}).Count(&jobs).Error; err != nil || jobs != 1 || pushes != 1 {
		t.Fatal("idempotent web replay created another push or sync series", err)
	}
	for round := 1; round <= 5; round++ {
		if err := a.processPostSubmissionSync(context.Background(), readPostSubmissionJob(t, a, job.ID).NextRunAt); err != nil {
			t.Fatal(err)
		}
	}
	saved := readPostSubmissionJob(t, a, job.ID)
	if saved.Status != "succeeded" || saved.AttemptCount != 5 || saved.SuccessCount != 5 || fake.calls != 5 || pushes != 1 {
		t.Fatalf("web series did not finish five reads after one push: %+v", saved)
	}
}
