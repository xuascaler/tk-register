package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"goapi-tk-register/server/common"
	"goapi-tk-register/server/internal/core"
)

func TestPersonalSubmissionDoesNotExposeSavedModelResponses(t *testing.T) {
	a, _, _ := fixture(t)
	admin, adminCookie, _ := vendorSession(t, a, "visibility-admin@example.com", "admin")
	_, cookie, csrf := vendorSession(t, a, "visibility-member@example.com", "member")
	vendor := configuredVendor(t, a, admin.ID)
	a.vendorTransport = vendorRoundTrip(func(*http.Request) (*http.Response, error) {
		return remoteResponse(200, `{"id":42,"alias":"account-alias"}`), nil
	})
	w := request(t, a, "POST", "submissions", createInput(vendor, "visibility-request-001"), cookie, csrf)
	requireStatus(t, w, 200)
	id := submissionDetail(t, w.Body.Bytes()).Submission.ID
	marker := `{"models":[{"model":"private-model-marker","cost":1234,"input_tokens":99}]}`
	if err := a.store.DB.Model(&core.ProtocolExecution{}).Where("submission_id = ?", id).Updates(map[string]any{"result_json": marker, "error_body": marker}).Error; err != nil {
		t.Fatal(err)
	}
	w = request(t, a, "GET", "submissions/"+id, nil, cookie, "")
	requireStatus(t, w, 200)
	if strings.Contains(w.Body.String(), "private-model-marker") {
		t.Fatal("saved opaque model usage leaked from personal history")
	}
	w = request(t, a, "GET", "admin/submissions/"+id, nil, adminCookie, "")
	requireStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), "private-model-marker") {
		t.Fatal("management response was unexpectedly restricted")
	}
	if err := a.store.PatchSettings(context.Background(), map[string]string{"user_overview_model_costs_enabled": "true"}, admin.ID); err != nil {
		t.Fatal(err)
	}
	w = request(t, a, "GET", "submissions/"+id, nil, cookie, "")
	requireStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), "private-model-marker") {
		t.Fatal("enabled personal details were not restored")
	}
	if err := a.store.PatchSettings(context.Background(), map[string]string{"user_overview_model_costs_enabled": "false"}, admin.ID); err != nil {
		t.Fatal(err)
	}
	w = request(t, a, "GET", "submissions/"+id, nil, cookie, "")
	requireStatus(t, w, 200)
	if strings.Contains(w.Body.String(), "private-model-marker") {
		t.Fatal("turning the setting off did not restrict old saved responses")
	}
}

func TestSubmissionAPIUsesIdentityProjectionWhenModelDetailsDisabled(t *testing.T) {
	a, _, _ := fixture(t)
	admin, _, _ := vendorSession(t, a, "visibility-api-admin@example.com", "admin")
	owner, _, _ := vendorSession(t, a, "visibility-api-owner@example.com", "member")
	configuredVendor(t, a, admin.ID)
	token, err := a.store.GenerateAPIToken(context.Background(), owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	a.vendorTransport = vendorRoundTrip(func(*http.Request) (*http.Response, error) {
		return remoteResponse(201, `{"id":9007199254740993,"alias":"claude-alias","extra":{"models":[{"model":"private-model-marker","cost":1234}]}}`), nil
	})
	raw, _ := common.Marshal(submissionParameters())
	w := apiRequest(t, a, "POST", claudeAccountsAPIPath, token.PlaintextToken, "application/json", string(raw))
	requireStatus(t, w, 201)
	var body map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if string(body["id"]) != "9007199254740993" || string(body["alias"]) != `"claude-alias"` || len(body) != 2 || w.Header().Get("X-Submission-ID") == "" || w.Header().Get("X-Request-ID") == "" {
		t.Fatal("identity or correlation fields were lost", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "private-model-marker") {
		t.Fatal("vendor response bypassed the model details setting")
	}
}

func TestPersonalSubmissionVisibilityFailsClosed(t *testing.T) {
	a, _, _ := fixture(t)
	if err := a.store.DB.Migrator().DropTable(&core.Setting{}); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", basePath+"/api/submissions/1", nil)
	a.writePersonalSubmission(w, r, core.SubmissionDetail{Executions: []core.ExecutionDTO{{Result: map[string]any{"model": "private-model-marker"}}}})
	requireStatus(t, w, 503)
	if strings.Contains(w.Body.String(), "private-model-marker") {
		t.Fatal("settings failure leaked an opaque response")
	}
}

func TestTotalsOnlySubmissionBodyDoesNotPassOpaqueOrNestedUsage(t *testing.T) {
	for _, raw := range []string{
		`{"data":{"id":9007199254740993,"models":{"secret":10}},"usage":{"secret":20}}`,
		`{"id":{"model":"secret"},"alias":["secret"],"error":{"code":"REJECTED","models":["secret"]}}`,
		`not-json secret`,
	} {
		body := totalsOnlySubmissionBody([]byte(raw), 400)
		if !json.Valid(body) || strings.Contains(string(body), "secret") {
			t.Fatal("unsafe projected response", string(body))
		}
	}
}
