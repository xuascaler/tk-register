package supplier

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestStatusReasonsOnlyManualAndPreserveSuccessfulMetrics(t *testing.T) {
	for _, tc := range []struct {
		name   string
		manual bool
		status string
		code   int
		found  bool
		reads  int
	}{
		{"scheduled-banned", false, "ban", 200, true, 0},
		{"manual-banned", true, "ban", 200, true, 1},
		{"offline", true, "offline", 200, true, 1},
		{"manual-sync-failed", true, "sync_failed", 200, true, 1},
		{"manual-login-failed", true, "login_failed", 200, true, 1},
		{"manual-re-login-failed", true, "re_login_failed", 200, true, 1},
		{"manual-bound-failed", true, "bound_failed", 200, true, 1},
		{"manual-cooking-failed", true, "cooking_failed", 200, true, 1},
		{"scheduled-sync-failed", false, "sync_failed", 200, true, 0},
		{"scheduled-login-failed", false, "login_failed", 200, true, 0},
		{"scheduled-re-login-failed", false, "re_login_failed", 200, true, 0},
		{"scheduled-bound-failed", false, "bound_failed", 200, true, 0},
		{"scheduled-cooking-failed", false, "cooking_failed", 200, true, 0},
		{"active", true, "using", 200, true, 0},
		{"pending", true, "pending_activation", 200, true, 0},
		{"unknown", true, "unknown-state", 200, true, 0},
		{"unavailable", true, "sync_failed", 429, true, 1},
		{"no-record", true, "login_failed", 200, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := editableFixture(77, "account@example.com")
			row["auth_config"] = fixtureAccount(77)["auth_config"]
			row["a1_status"] = tc.status
			client, fixture := targetClient(t, func(_ *http.Request, _ int) *http.Response {
				return jsonResponse(200, map[string]any{"total": 1, "list": []any{row}})
			})
			base := client.transport
			reasonReads := 0
			client.transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, "/claude-status-reason") || strings.HasSuffix(r.URL.Path, "/check-fail-reason") {
					reasonReads++
					suffix := "/claude-status-reason"
					if tc.status == "sync_failed" {
						suffix = "/check-fail-reason"
					}
					if r.Method != "GET" || r.URL.Path != apiPrefix+"/accounts/77"+suffix || r.Header.Get("Authorization") != "Bearer token-1" {
						t.Fatal("wrong identity or authentication")
					}
					return jsonResponse(tc.code, map[string]any{"found": tc.found, "content": "Account disabled; TOKEN_SECRET ACCOUNT_SECRET private-password-中文 token-1 温州润晨"}), nil
				}
				return base.RoundTrip(r)
			})
			ctx := context.Background()
			if tc.manual {
				ctx = WithStatusReasons(ctx)
			}
			rows, err := client.CollectTargets(ctx, "supplier-user", "private-password-中文", []Target{{Account: "account@example.com"}})
			if err != nil || len(rows) != 1 || rows[0].Err != nil || rows[0].Account == nil || rows[0].Account.CostTotal == nil {
				t.Fatalf("lost successful data: %+v %v", rows, err)
			}
			if reasonReads != tc.reads || fixture.logins != 1 {
				t.Fatalf("wrong queries/logins: reason reads=%d, want=%d; logins=%d", reasonReads, tc.reads, fixture.logins)
			}
			a := rows[0].Account
			if a.UpstreamStatusRaw == nil || *a.UpstreamStatusRaw != tc.status {
				t.Fatal("raw status lost")
			}
			if tc.code == 429 && rows[0].ReasonError == nil {
				t.Fatal("reason error missing")
			}
			if reasonReads > 0 && tc.code == 200 {
				if a.UpstreamErrorMessage == nil {
					t.Fatal("missing explicit reason result")
				}
				for _, secret := range []string{"TOKEN_SECRET", "ACCOUNT_SECRET", "private-password-中文", "token-1", "温州润晨"} {
					if strings.Contains(*a.UpstreamErrorMessage, secret) {
						t.Fatal("secret retained")
					}
				}
				if !tc.found && *a.UpstreamErrorMessage != "" {
					t.Fatal("not-found did not clear reason")
				}
			}
		})
	}
}
func TestReasonPathRestriction(t *testing.T) {
	for _, suffix := range []string{"/claude-status-reason", "/check-fail-reason"} {
		for _, id := range []string{"1", "466243"} {
			if !validStatusReasonPath(apiPrefix + "/accounts/" + id + suffix) {
				t.Fatal("valid reason path rejected")
			}
		}
		for _, path := range []string{apiPrefix + "/accounts/.." + suffix, apiPrefix + "/accounts/1" + suffix + "?x=1", apiPrefix + "/accounts/0" + suffix, apiPrefix + "/accounts/1/2" + suffix, apiPrefix + "/accounts/1/" + suffix} {
			if validStatusReasonPath(path) {
				t.Fatal(path)
			}
		}
	}
}
