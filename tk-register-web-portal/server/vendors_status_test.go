package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"goapi-tk-register/server/common"
	"goapi-tk-register/server/internal/core"
)

func TestVendorStatusRefreshOwnershipCacheAndPersistence(t *testing.T) {
	a, _, _ := fixture(t)
	admin, _, _ := vendorSession(t, a, "status-admin@example.com", "admin")
	owner, cookie, csrf := vendorSession(t, a, "status-owner@example.com", "member")
	_, other, otherCSRF := vendorSession(t, a, "status-other@example.com", "member")
	vendor := configuredVendor(t, a, admin.ID)
	var pushes, queries atomic.Int32
	responseBody := `{"accounts":[{"id":12345,"status":"online","aid":999}]}`
	a.vendorTransport = vendorRoundTrip(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/accounts") {
			pushes.Add(1)
			return remoteResponse(200, `{"id":12345,"alias":"status-account"}`), nil
		}
		queries.Add(1)
		if !strings.Contains(r.URL.Path, "metrics") {
			t.Errorf("status refresh called another protocol: %s", r.URL.Path)
		}
		var body struct {
			IDs       []uint64 `json:"account_ids"`
			BeginTime string   `json:"begin_time"`
			EndTime   string   `json:"end_time"`
		}
		raw, _ := io.ReadAll(r.Body)
		if e := common.Unmarshal(raw, &body); e != nil {
			t.Fatal(e)
		}
		if len(body.IDs) != 1 || body.IDs[0] != 12345 {
			t.Errorf("status query did not use the stored remote ID: %v", body.IDs)
		}
		loc, _ := time.LoadLocation("Asia/Shanghai")
		begin, e1 := time.ParseInLocation("2006-01-02 15:04:05", body.BeginTime, loc)
		end, e2 := time.ParseInLocation("2006-01-02 15:04:05", body.EndTime, loc)
		if e1 != nil || e2 != nil || end.Sub(begin) != 24*time.Hour || time.Since(end) < 0 || time.Since(end) > 10*time.Second {
			t.Errorf("status query range is not the recent 24 hours in vendor time: %s – %s", body.BeginTime, body.EndTime)
		}
		return remoteResponse(200, responseBody), nil
	})
	created := request(t, a, "POST", "submissions", createInput(vendor, "status-submission-0001"), cookie, csrf)
	requireStatus(t, created, 200)
	detail := submissionDetail(t, created.Body.Bytes())
	id := detail.Submission.ID
	idNumber, _ := core.ParseBusinessID(id)
	endpoint := "submissions/" + id + "/status"
	if detail.Submission.StatusCheckedAt != nil {
		t.Fatal("account submission fabricated a status-check time")
	}
	requireStatus(t, request(t, a, "GET", "submissions", nil, cookie, ""), 200)
	requireStatus(t, request(t, a, "POST", endpoint, map[string]any{}, nil, ""), 401)
	requireStatus(t, request(t, a, "POST", endpoint, map[string]any{}, cookie, ""), 403)
	requireStatus(t, request(t, a, "POST", endpoint, map[string]any{}, other, otherCSRF), 404)
	requireStatus(t, request(t, a, "POST", endpoint, map[string]any{"account_ids": []int{1}}, cookie, csrf), 400)
	if queries.Load() != 0 {
		t.Fatal("unauthorized/read-only requests reached the vendor")
	}
	first := request(t, a, "POST", endpoint, map[string]any{}, cookie, csrf)
	requireStatus(t, first, 200)
	detail = submissionDetail(t, first.Body.Bytes())
	assertNoSecrets(t, first.Body.String())
	if detail.Submission.RemoteStatus == nil || *detail.Submission.RemoteStatus != "online" || detail.Submission.StatusCheckedAt == nil || detail.Metrics == nil || !detail.Submission.StatusCheckedAt.Equal(detail.Metrics.QueriedAt) {
		t.Fatal("fresh platform status and check timestamp not returned")
	}
	record, e := a.store.OwnedSubmission(context.Background(), owner.ID, idNumber)
	if e != nil || record.RemoteStatus == nil || *record.RemoteStatus != "online" || record.MetricsJSON == "" {
		t.Fatal("status refresh was not persisted", e)
	}
	cached := request(t, a, "POST", endpoint, map[string]any{"force": false}, cookie, csrf)
	requireStatus(t, cached, 200)
	if queries.Load() != 1 || !submissionDetail(t, cached.Body.Bytes()).Submission.StatusCheckedAt.Equal(*detail.Submission.StatusCheckedAt) {
		t.Fatal("automatic refresh did not reuse the recent metrics check")
	}
	responseBody = `{"accounts":[{"id":12345,"status":"disabled"}]}`
	forced := request(t, a, "POST", endpoint, map[string]any{"force": true}, cookie, csrf)
	requireStatus(t, forced, 200)
	detail = submissionDetail(t, forced.Body.Bytes())
	if queries.Load() != 2 || detail.Submission.RemoteStatus == nil || *detail.Submission.RemoteStatus != "disabled" {
		t.Fatal("manual force refresh reused the cache")
	}
	// An old successful snapshot, unlike a recent one, must be queried again.
	detail.Metrics.QueriedAt = time.Now().UTC().Add(-31 * time.Second)
	oldJSON, e := common.Marshal(detail.Metrics)
	if e != nil {
		t.Fatal(e)
	}
	if e = a.store.DB.Model(&core.Submission{}).Where("id = ?", idNumber).Update("metrics_json", string(oldJSON)).Error; e != nil {
		t.Fatal(e)
	}
	requireStatus(t, request(t, a, "POST", endpoint, map[string]any{}, cookie, csrf), 200)
	if queries.Load() != 3 {
		t.Fatal("expired status snapshot was reused")
	}
	for _, body := range []string{`{"accounts":[]}`, `{"accounts":[{"id":12345}]}`} {
		responseBody = body
		missing := request(t, a, "POST", endpoint, map[string]any{"force": true}, cookie, csrf)
		requireStatus(t, missing, 200)
		got := submissionDetail(t, missing.Body.Bytes())
		if got.Submission.RemoteStatus != nil || got.Submission.StatusCheckedAt == nil {
			t.Fatal("missing upstream status left a stale status or lost the check timestamp")
		}
		stored, e := a.store.OwnedSubmission(context.Background(), owner.ID, idNumber)
		if e != nil || stored.RemoteStatus != nil {
			t.Fatal("stale platform status remained in the database", e)
		}
	}
	if pushes.Load() != 1 {
		t.Fatal("a status refresh retried account submission")
	}
}

func TestVendorStatusRefusesUnconfirmedAccountsAndUsesQueryRateLimit(t *testing.T) {
	a, _, _ := fixture(t)
	admin, _, _ := vendorSession(t, a, "status-limit-admin@example.com", "admin")
	_, cookie, csrf := vendorSession(t, a, "status-limit-owner@example.com", "member")
	vendor := configuredVendor(t, a, admin.ID)
	var calls atomic.Int32
	a.vendorTransport = vendorRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return remoteResponse(503, `{"message":"temporarily unavailable"}`), nil
	})
	created := request(t, a, "POST", "submissions", createInput(vendor, "status-unconfirmed-0001"), cookie, csrf)
	requireStatus(t, created, 200)
	id := submissionDetail(t, created.Body.Bytes()).Submission.ID
	endpoint := "submissions/" + id + "/status"
	for _, state := range []string{"unknown", "running", "failed", "accepted"} {
		if e := a.store.DB.Model(&core.Submission{}).Where("id = ?", id).Update("status", state).Error; e != nil {
			t.Fatal(e)
		}
		requireStatus(t, request(t, a, "POST", endpoint, map[string]any{"force": true}, cookie, csrf), 409)
	}
	if calls.Load() != 1 {
		t.Fatal("status refresh queried or resubmitted an account without a remote ID")
	}
	// The first four requests already consumed the existing query-rate budget.
	for i := 4; i < 30; i++ {
		requireStatus(t, request(t, a, "POST", endpoint, map[string]any{}, cookie, csrf), 409)
	}
	requireStatus(t, request(t, a, "POST", endpoint, map[string]any{}, cookie, csrf), 429)
}
