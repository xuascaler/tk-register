package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"goapi-tk-register/server/internal/core"
	"goapi-tk-register/server/internal/safemessage"
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
			execution.ErrorBody = totalsOnlyErrorBody(execution.ErrorBody)
			if execution.ErrorBody == "" {
				if execution.ErrorBodyOmittedReason == "" {
					execution.ErrorBodyOmittedReason = "MODEL_DETAILS_DISABLED"
				}
			} else if execution.ErrorBodyOmittedReason == "MODEL_DETAILS_DISABLED" {
				execution.ErrorBodyOmittedReason = ""
			}
		}
	}
	return detail
}

// ErrorBody was already credential-redacted before persistence. Preserve only
// diagnostic fields here, excluding arbitrary response extensions and usage.
// Treat JSON embedded inside a diagnostic string exactly like outer JSON.
func totalsOnlyErrorBody(body string) string {
	body = strings.TrimSpace(body)
	if body == "" || len(body) > 8<<10 || !utf8.ValidString(body) {
		return ""
	}
	var value any
	if json.Unmarshal([]byte(body), &value) != nil {
		text, _ := diagnosticText(body, 0).(string)
		return text
	}
	projected := diagnosticValue(value, 0)
	if projected == nil {
		return ""
	}
	raw, err := json.Marshal(projected)
	if err != nil || len(raw) > 8<<10 {
		return ""
	}
	return string(raw)
}

var (
	diagnosticUsageText       = regexp.MustCompile(`(?i)(?:\b(?:models?|usage|billing|costs?|prices?|charges?|consumption|tokens|usd|cny)\b|(?:input|output|prompt|completion|cache[_ -]?(?:read|write))[_ -]?tokens?|[$¥￥]|模型|费用|计费|消耗|用量)`)
	diagnosticGenericError    = regexp.MustCompile(`(?i)^(?:unsupported model|invalid tokens?)[.!]?$`)
	diagnosticModelIdentifier = regexp.MustCompile(`(?i)\b(?:claude|gpt|gemini)[-_ ]+[a-z0-9]`)
	diagnosticLocationField   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

func diagnosticText(text string, depth int) any {
	text = strings.TrimSpace(text)
	if text == "" || depth > 16 {
		return nil
	}
	if strings.ContainsAny(text[:1], "{[\"") {
		var nested any
		if json.Unmarshal([]byte(text), &nested) != nil {
			return nil
		}
		projected := diagnosticValue(nested, depth+1)
		if projected == nil {
			return nil
		}
		raw, err := json.Marshal(projected)
		if err != nil {
			return nil
		}
		return string(raw)
	}
	// Plain diagnostics have no field boundaries. Omit model/usage-shaped text
	// rather than interpreting it as a safe error merely because it is a string.
	normalized := strings.NewReplacer("_", " ", "-", " ").Replace(text)
	if strings.HasPrefix(text, "<") || strings.ContainsAny(text, "{}") || diagnosticModelIdentifier.MatchString(text) || !diagnosticGenericError.MatchString(normalized) && diagnosticUsageText.MatchString(normalized) {
		return nil
	}
	if text = safemessage.Text(text); text != "" {
		return text
	}
	return nil
}

func diagnosticValue(value any, depth int) any {
	if depth > 16 {
		return nil
	}
	switch value := value.(type) {
	case string:
		return diagnosticText(value, depth)
	case map[string]any:
		out := map[string]any{}
		for key, item := range value {
			key = strings.ToLower(key)
			switch key {
			case "message", "msg", "error_description", "code", "type":
				if text, ok := item.(string); ok {
					if projected := diagnosticText(text, depth+1); projected != nil {
						out[key] = projected
					}
				} else if key == "code" {
					if number, ok := item.(float64); ok {
						out[key] = number
					}
				}
			case "detail", "error", "errors", "validation":
				if projected := diagnosticValue(item, depth+1); projected != nil {
					out[key] = projected
				}
			case "loc":
				if parts, ok := item.([]any); ok {
					location := make([]any, 0, len(parts))
					for _, part := range parts {
						if text, ok := part.(string); ok && diagnosticLocationField.MatchString(text) {
							if safe := diagnosticText(text, depth+1); safe != nil {
								location = append(location, safe)
							}
						} else if number, ok := part.(float64); ok && number >= 0 && number <= 10000 && number == float64(int64(number)) {
							location = append(location, number)
						}
					}
					if len(location) != 0 {
						out[key] = location
					}
				}
			}
		}
		if len(out) != 0 {
			return out
		}
	case []any:
		out := make([]any, 0, len(value))
		for _, item := range value {
			// Validation arrays contain diagnostic objects. Unstructured strings
			// or nested arrays could instead be a model/cost matrix.
			if object, ok := item.(map[string]any); ok {
				if projected := diagnosticValue(object, depth+1); projected != nil {
					out = append(out, projected)
				}
			}
		}
		if len(out) != 0 {
			return out
		}
	}
	return nil
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
