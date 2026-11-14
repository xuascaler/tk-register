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
	}{
		{"scheduled", false, "ban", 200, true}, {"manual", true, "ban", 200, true}, {"offline", true, "offline", 200, true}, {"active", true, "using", 200, true}, {"unavailable", true, "ban", 429, true}, {"no-record", true, "ban", 200, false},
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
				if strings.HasSuffix(r.URL.Path, "/claude-status-reason") {
					reasonReads++
					if r.Method != "GET" || r.URL.Path != apiPrefix+"/accounts/77/claude-status-reason" || r.Header.Get("Authorization") != "Bearer token-1" {
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
			expected := 0
			if tc.manual && tc.status != "using" {
				expected = 1
			}
			if reasonReads != expected || fixture.logins != 1 {
				t.Fatal("wrong number of queries/logins")
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
	for _, path := range []string{apiPrefix + "/accounts/../claude-status-reason", apiPrefix + "/accounts/1/claude-status-reason?x=1", apiPrefix + "/accounts/0/claude-status-reason"} {
		if validStatusReasonPath(path) {
			t.Fatal(path)
		}
	}
}
