package main

import (
	"bytes"
	"encoding/json"
	"net/http"

	"goapi-tk-register/server/internal/core"
)

// Supplier results are opaque JSON, including old responses saved before the
// visibility setting existed. Keep their structured account-level projections,
// but do not serialize arbitrary result or error bodies in totals-only mode.
func personalSubmissionDetail(detail core.SubmissionDetail, modelCostsEnabled bool) core.SubmissionDetail {
	if modelCostsEnabled {
		return detail
	}
	detail.Executions = append([]core.ExecutionDTO{}, detail.Executions...)
	for i := range detail.Executions {
		execution := &detail.Executions[i]
		execution.Result = nil
		if execution.ErrorBody != "" {
			execution.ErrorBodyOmittedReason = "MODEL_DETAILS_DISABLED"
		}
		execution.ErrorBody = ""
		execution.ErrorBodyTruncated = false
		execution.ErrorBodyIncomplete = false
	}
	return detail
}

func (a *app) writePersonalSubmission(w http.ResponseWriter, r *http.Request, detail core.SubmissionDetail) {
	enabled, err := a.userModelCostsEnabled(r.Context())
	if err != nil {
		internal(w)
		return
	}
	ok(w, personalSubmissionDetail(detail, enabled))
}

// Submission API responses normally contain identity fields only. A vendor may
// add arbitrary usage data to a response, so totals-only clients receive an
// explicit identity/status projection rather than a recursively filtered body.
// json.RawMessage keeps identifiers larger than JavaScript's safe integer range.
func totalsOnlySubmissionBody(body []byte, status int) []byte {
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) != nil || object == nil {
		object = map[string]json.RawMessage{}
	}
	var project func(map[string]json.RawMessage) map[string]json.RawMessage
	project = func(input map[string]json.RawMessage) map[string]json.RawMessage {
		out := map[string]json.RawMessage{}
		for _, key := range []string{"id", "alias", "account_id", "platform_account_id", "status", "success", "code", "type"} {
			value := bytes.TrimSpace(input[key])
			if len(value) == 0 || value[0] == '{' || value[0] == '[' {
				continue
			}
			out[key] = value
		}
		for _, key := range []string{"data", "error"} {
			var nested map[string]json.RawMessage
			if json.Unmarshal(input[key], &nested) == nil && nested != nil {
				nestedBody, _ := json.Marshal(project(nested))
				out[key] = nestedBody
			}
		}
		return out
	}
	out := project(object)
	if status >= 400 {
		out["message"] = json.RawMessage(`"The supplier rejected this request. Check the submission history for its status."`)
	}
	result, _ := json.Marshal(out)
	return result
}
