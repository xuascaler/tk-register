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
	admin, adminCookie, _ := vendorSession(t, a, core.InitialSuperAdminEmail, "admin")
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
	if strings.Contains(w.Body.String(), "private-model-marker") {
		t.Fatal("model visibility exposed an opaque supplier result")
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
	a.writePersonalSubmission(w, r, &core.User{}, core.SubmissionDetail{Executions: []core.ExecutionDTO{{Result: map[string]any{"model": "private-model-marker"}}}}, nil)
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

func TestTotalsOnlyErrorBodyKeepsDiagnosticsAndOmitsUsage(t *testing.T) {
	for _, test := range []struct {
		name, body, want string
	}{
		{"actual duplicate response", `{"detail":"账号重复录入,id:571510"}`, "账号重复录入,id:571510"},
		{"mixed error and usage", `{"detail":"账号重复录入,id:571510","models":[{"model":"private-model-marker","cost_usd":1234,"input_tokens":"999999"}],"input":{"password":"echoed-secret"}}`, "账号重复录入,id:571510"},
		{"validation", `{"detail":[{"loc":["body","account",0],"type":"value_error","msg":"Account already exists","input":"echoed-secret","ctx":{"model":"private-model-marker"}}]}`, "Account already exists"},
		{"nested error", `{"error":{"code":"DUPLICATE_ACCOUNT","message":"Account already exists","usage":{"model":"private-model-marker"}}}`, "DUPLICATE_ACCOUNT"},
		{"embedded JSON", `{"message":"{\"detail\":\"Account already exists\",\"models\":[{\"model\":\"private-model-marker\",\"cost\":1234}]}"}`, "Account already exists"},
		{"ordinary text", "Unsupported register_location MY\nPlease use US", "Unsupported register_location MY"},
		{"authentication error", "Invalid token", "Invalid token"},
		{"generic tokens error", "Invalid tokens", "Invalid tokens"},
		{"generic model error", `{"detail":"Unsupported model","code":"UNSUPPORTED_MODEL"}`, "Unsupported model"},
		{"reflected credential", `{"detail":"Invalid password: echoed-secret"}`, "Invalid password"},
		{"usage only", `{"models":[{"model":"private-model-marker","cost_usd":1234}]}`, ""},
		{"usage inside message", `{"detail":"private-model-marker cost_usd:1234 input_tokens:999999"}`, ""},
		{"plain usage", "model=private-model-marker cost=1234", ""},
		{"model cost matrix", `{"detail":[["claude-opus-5","12.50"]]}`, ""},
		{"model cost string array", `{"errors":["claude-opus-5","12.50"]}`, ""},
		{"embedded matrix", `{"message":"{\"detail\":[[\"claude-opus-5\",\"12.50\"]]}"}`, ""},
		{"model matrix location", `{"detail":[{"loc":["claude-opus-5","12.50"]}]}`, ""},
		{"model pair inside message", `{"detail":"claude-opus-5 12.50"}`, ""},
		{"malformed JSON", `{"detail":"bad","models":["private-model-marker"`, ""},
		{"embedded malformed JSON", `{"detail":"{\"models\":[\"private-model-marker\""}`, ""},
		{"unstructured HTML", `<html><h1>private-model-marker</h1></html>`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := totalsOnlyErrorBody(test.body)
			if test.want == "" && body != "" || test.want != "" && !strings.Contains(body, test.want) {
				t.Fatalf("diagnostic projection = %q, expected %q", body, test.want)
			}
			for _, private := range []string{"private-model-marker", "claude-opus-5", "12.50", "1234", "999999", "echoed-secret", "input_tokens", "cost_usd"} {
				if strings.Contains(body, private) {
					t.Fatalf("private response content leaked: %q", body)
				}
			}
		})
	}
}

func TestPersonalSubmissionProjectionPreservesDiagnosticMetadata(t *testing.T) {
	detail := core.SubmissionDetail{Executions: []core.ExecutionDTO{{
		ErrorBody:          `{"detail":"账号重复录入,id:571510","models":["private-model-marker"]}`,
		ErrorBodyTruncated: true, ErrorBodyIncomplete: true, ErrorBodyOmittedReason: "MODEL_DETAILS_DISABLED",
		Result: map[string]any{"models": "private-model-marker"},
	}}}
	before, _ := json.Marshal(detail)
	projected := personalSubmissionDetail(detail, false).Executions[0]
	if projected.Result != nil || !strings.Contains(projected.ErrorBody, "账号重复录入,id:571510") || strings.Contains(projected.ErrorBody, "private-model-marker") || projected.ErrorBodyOmittedReason != "" {
		t.Fatal("safe diagnostic not restored independently of usage")
	}
	if !projected.ErrorBodyTruncated || !projected.ErrorBodyIncomplete {
		t.Fatal("diagnostic completeness metadata was discarded")
	}
	after, _ := json.Marshal(detail)
	if string(before) != string(after) {
		t.Fatal("projection mutated the original detail")
	}
	detail.Executions[0].ErrorBody = ""
	detail.Executions[0].ErrorBodyOmittedReason = "incomplete_or_oversized_response"
	projected = personalSubmissionDetail(detail, false).Executions[0]
	if projected.ErrorBodyOmittedReason != "incomplete_or_oversized_response" || !projected.ErrorBodyTruncated || !projected.ErrorBodyIncomplete {
		t.Fatal("original omission metadata changed")
	}
}

func TestPersonalSavedFailureKeepsSafeErrorWithoutRequeryingVendor(t *testing.T) {
	for _, role := range []string{"member", "admin"} {
		t.Run(role, func(t *testing.T) {
			a, _, _ := fixture(t)
			manager, managerCookie, _ := vendorSession(t, a, core.InitialSuperAdminEmail, "admin")
			_, cookie, csrf := vendorSession(t, a, "diagnostic-owner@example.com", role)
			vendor := configuredVendor(t, a, manager.ID)
			calls := 0
			a.vendorTransport = vendorRoundTrip(func(*http.Request) (*http.Response, error) {
				calls++
				return remoteResponse(400, `{"detail":"账号重复录入,id:571510","models":[{"model":"private-model-marker","cost_usd":1234,"input_tokens":"999999"}],"input":{"password":"echoed-secret"}}`), nil
			})
			created := request(t, a, "POST", "submissions", createInput(vendor, "saved-failure-diagnostic-001"), cookie, csrf)
			requireStatus(t, created, 200)
			first := submissionDetail(t, created.Body.Bytes())
			if first.Submission.Status != "failed" || len(first.Executions) != 1 || !strings.Contains(first.Executions[0].ErrorBody, "账号重复录入,id:571510") {
				t.Fatal("new submission did not retain the actionable error")
			}
			id := first.Submission.ID
			if err := a.store.DB.Model(&core.ProtocolExecution{}).Where("submission_id = ?", id).Update("result_json", `{"models":[{"model":"private-model-marker","cost":1234}]}`).Error; err != nil {
				t.Fatal(err)
			}
			var beforeRows []core.ProtocolExecution
			if err := a.store.DB.Where("submission_id = ?", id).Find(&beforeRows).Error; err != nil {
				t.Fatal(err)
			}
			before, _ := json.Marshal(beforeRows)
			for _, enabled := range []bool{false, true, false} {
				value := "false"
				if enabled {
					value = "true"
				}
				if err := a.store.PatchSettings(context.Background(), map[string]string{"user_overview_model_costs_enabled": value}, manager.ID); err != nil {
					t.Fatal(err)
				}
				read := request(t, a, "GET", "submissions/"+id, nil, cookie, "")
				requireStatus(t, read, 200)
				execution := submissionDetail(t, read.Body.Bytes()).Executions[0]
				if !strings.Contains(execution.ErrorBody, "账号重复录入,id:571510") || execution.RequestID == "" || execution.HTTPStatus == nil || *execution.HTTPStatus != 400 || execution.ErrorCode == nil || *execution.ErrorCode != "UPSTREAM_REJECTED" {
					t.Fatal("read lost failure diagnostics or correlation")
				}
				if strings.Contains(read.Body.String(), "private-model-marker") {
					t.Fatal("model visibility allowed an opaque supplier response")
				}
				if execution.Result != nil {
					t.Fatal("opaque result leaked with model details disabled")
				}
				if strings.Contains(read.Body.String(), "echoed-secret") {
					t.Fatal("credential echo leaked")
				}
			}
			management := request(t, a, "GET", "admin/submissions/"+id, nil, managerCookie, "")
			requireStatus(t, management, 200)
			if !strings.Contains(management.Body.String(), "private-model-marker") || !strings.Contains(management.Body.String(), "账号重复录入,id:571510") {
				t.Fatal("management details were restricted")
			}
			var afterRows []core.ProtocolExecution
			if err := a.store.DB.Where("submission_id = ?", id).Find(&afterRows).Error; err != nil {
				t.Fatal(err)
			}
			after, _ := json.Marshal(afterRows)
			if calls != 1 || string(before) != string(after) {
				t.Fatal("reading old failures requeried the vendor or changed persisted executions")
			}
		})
	}
}
