package supplier

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func restoreInput() map[string]any {
	return map[string]any{"account": "account@example.com", "plan": "claude/cc-max-20x", "password": "NEW_PASSWORD", "auth_json": map[string]any{"email": "account@example.com", "access_token": "NEW_ACCESS"}, "register_location": "SG", "register_time": "2026-10-01", "plan_start_time": "2026-11-05", "plan_end_time": "2026-12-05", "remark": "new remark"}
}
func TestRestoreWritesNewCredentialsAndDatesOnce(t *testing.T) {
	row := editableFixture(99, "account@example.com")
	row["alias"] = "claude-old-alias"
	row["proxy_type"], row["proxy_region"] = "residential", "SG"
	puts, guards := 0, 0
	client, f := authClient(t, oneRecord(row), func(r *http.Request) (*http.Response, error) {
		puts++
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if json.Unmarshal(raw, &body) != nil {
			t.Fatal("invalid body")
		}
		if body["cpa_plan"] != "claude/cc-max-20x" || body["cpa_plan_start_time"] != "2026-11-05" || body["cpa_plan_end_time"] != "2026-12-05" || body["cpa_register_location"] != "SG" || body["remark"] != "new remark" || body["proxy_type"] != "residential" {
			t.Fatal("metadata mismatch", body)
		}
		if decryptPassword(t, body["enc_api_key"].(string)) != "NEW_PASSWORD" || decryptPassword(t, body["enc_adc_config"].(string)) != "" {
			t.Fatal("credential encryption mismatch")
		}
		if !strings.Contains(body["cpa_auth_json"].(string), "NEW_ACCESS") || strings.Contains(string(raw), "OLD_") || strings.Contains(string(raw), "NEW_PASSWORD") {
			t.Fatal("wrong/plain credentials forwarded")
		}
		if _, ok := body["cpa_tags"]; ok {
			t.Fatal("edit sent unsupported tags")
		}
		if body["cpa_payment_method"] != "physical_credit_card" || body["cpa_balance"] != "0" {
			t.Fatal("omitted optional fields lost")
		}
		if guards != 1 {
			t.Fatal("write occurred before durable guard")
		}
		return jsonResponse(200, map[string]any{"success": true}), nil
	})
	result, err := client.RestoreAccount(context.Background(), "supplier-user", "private-password-中文", Target{Account: "account@example.com", RecordID: "99", BeforeUpdate: func(context.Context, EditInfo) error { guards++; return nil }}, restoreInput())
	if err != nil || !result.Confirmed || !result.Found || !result.WriteAttempted || result.RecordID != "99" || result.Alias != "claude-old-alias" || puts != 1 || f.reads != 1 {
		t.Fatal(result, err, puts, f.reads)
	}
}
func TestRestoreOnlyReturnsAbsentForCompleteValidSearch(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response *http.Response
		want     string
	}{
		{"empty", jsonResponse(200, map[string]any{"total": 0, "list": []any{}}), ""},
		{"lookup404", jsonResponse(404, map[string]any{"error": "not found"}), CodeContractMismatch},
		{"lookup500", jsonResponse(500, map[string]any{"error": "down"}), CodeUnavailable},
		{"missing_total", jsonResponse(200, map[string]any{"list": []any{}}), CodeContractMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := authClient(t, func(*http.Request, int) *http.Response { return tc.response }, nil)
			result, err := client.RestoreAccount(context.Background(), "supplier-user", "private-password-中文", Target{Account: "account@example.com"}, restoreInput())
			if tc.want == "" {
				if err != nil || result.Found || result.WriteAttempted {
					t.Fatal(result, err)
				}
			} else {
				if err == nil || result.WriteAttempted {
					t.Fatal("lookup error treated as absence", result, err)
				}
			}
		})
	}
}
func TestRestoreRejectsChangedIdentityTagsAndLockedCredentials(t *testing.T) {
	for _, name := range []string{"wrong-id", "locked", "active", "different-tags", "mismatched-auth"} {
		t.Run(name, func(t *testing.T) {
			row := editableFixture(99, "account@example.com")
			input := restoreInput()
			target := Target{Account: "account@example.com", RecordID: "99", BeforeUpdate: func(context.Context, EditInfo) error { return nil }}
			switch name {
			case "wrong-id":
				target.RecordID = "98"
			case "locked":
				row["is_locked"] = 1
			case "active":
				row["a1_status"], row["a1_id"] = "using", 123
			case "different-tags":
				input["tags"] = []any{"速刷"}
			case "mismatched-auth":
				input["auth_json"] = map[string]any{"email": "someone-else@example.com"}
			}
			client, _ := authClient(t, oneRecord(row), nil)
			result, err := client.RestoreAccount(context.Background(), "supplier-user", "private-password-中文", target, input)
			if err == nil || result.WriteAttempted || !result.Found {
				t.Fatal(result, err)
			}
		})
	}
}
func TestRestoreNeverRetriesUncertainPUT(t *testing.T) {
	puts := 0
	client, f := authClient(t, oneRecord(editableFixture(99, "account@example.com")), func(*http.Request) (*http.Response, error) { puts++; return nil, io.ErrUnexpectedEOF })
	result, err := client.RestoreAccount(context.Background(), "supplier-user", "private-password-中文", Target{Account: "account@example.com", RecordID: "99", BeforeUpdate: func(context.Context, EditInfo) error { return nil }}, restoreInput())
	requireSupplierCode(t, err, CodeUpdateUnconfirmed)
	if !result.WriteAttempted || result.Confirmed || puts != 1 || f.logins != 1 {
		t.Fatal(result, puts, f.logins)
	}
}
