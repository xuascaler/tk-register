package supplier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

func editableFixture(id int, account string) map[string]any {
	row := fixtureAccount(id)
	row["provider"], row["account_type"], row["model"], row["is_locked"] = "claude", 2, "latest-model", 0
	row["remark"], row["a1_status"] = "Current vendor remark — keep this", "login_failed"
	config := map[string]any{
		"account": account, "plan": "claude/cc-pro", "register_time": "2026-09-01",
		"plan_start_time": "2026-11-01", "plan_end_time": "2027-01-01", "register_location": "MY",
		"balance": 0, "payment_method": "physical_credit_card", "register_device": "Mac",
		"recharge_time": "2026-11-01", "warranty_period": 7, "tags": []string{"慢蒸"},
		"password": "OLD_PASSWORD_MUST_NOT_LEAK", "auth_json": map[string]string{"access_token": "OLD_SECRET_MUST_NOT_LEAK"},
	}
	encoded, _ := json.Marshal(config)
	row["auth_config"] = string(encoded)
	return row
}

func authClient(t *testing.T, read func(*http.Request, int) *http.Response, put func(*http.Request) (*http.Response, error)) (*Client, *supplierFixture) {
	t.Helper()
	f := &supplierFixture{t: t, read: read}
	base := f.client().transport
	client := &Client{transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPut {
			if r.URL.Scheme+"://"+r.URL.Host != Origin || !strings.HasPrefix(r.URL.Path, apiPrefix+"/accounts/") {
				t.Fatal("write escaped the fixed supplier endpoint")
			}
			if put == nil {
				t.Fatal("unexpected PUT")
			}
			return put(r)
		}
		if r.URL.Path == apiPrefix+"/accounts" {
			q := r.URL.Query()
			if q.Get("keyword") != "account@example.com" || q.Has("model") {
				t.Fatal("targeted lookup did not use full email or incorrectly filtered plan as model")
			}
		}
		return base.RoundTrip(r)
	})}
	return client, f
}

func oneRecord(row map[string]any) func(*http.Request, int) *http.Response {
	return func(*http.Request, int) *http.Response {
		return jsonResponse(200, map[string]any{"total": 1, "list": []any{row}})
	}
}

func requireSupplierCode(t *testing.T, err error, code string) {
	t.Helper()
	var safe *Error
	if !errors.As(err, &safe) || safe.Code != code {
		t.Fatalf("got %v, want error code %s", err, code)
	}
	if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "TOKEN") {
		t.Fatal("error leaked supplier data")
	}
}

func TestTargetedLookupExactEmailAndChangedPlan(t *testing.T) {
	client, f := authClient(t, func(r *http.Request, n int) *http.Response {
		if r.URL.Query().Get("page") != fmt.Sprint(n) {
			t.Fatal("incorrect result-page pagination")
		}
		if n == 1 {
			return jsonResponse(200, map[string]any{"total": 2, "list": []any{editableFixture(88, "prefix-account@example.com")}})
		}
		return jsonResponse(200, map[string]any{"total": 2, "list": []any{editableFixture(99, " ACCOUNT@EXAMPLE.COM ")}})
	}, nil)
	info, err := client.CheckAuthJSON(context.Background(), "supplier-user", "private-password-中文", Target{Account: "account@example.com", RecordID: "old-id"})
	if err != nil {
		t.Fatal(err)
	}
	if info.RecordID != "99" || info.Plan != "claude/cc-pro" || !info.Editable || f.reads != 2 {
		t.Fatalf("incorrect exact match: %+v reads=%d", info, f.reads)
	}
	b, _ := json.Marshal(info)
	if strings.Contains(string(b), "SECRET") || strings.Contains(string(b), "password") {
		t.Fatal("preflight exposed private data")
	}
}

func TestTargetedLookupAmbiguityAndConfirmedID(t *testing.T) {
	for _, tc := range []struct{ name, recordID, want string }{
		{"unmapped", "", CodeAccountAmbiguous}, {"platform-id-is-not-record-id", "13152", CodeAccountAmbiguous}, {"confirmed-row", "42", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := authClient(t, func(*http.Request, int) *http.Response {
				return jsonResponse(200, map[string]any{"total": 2, "list": []any{editableFixture(41, "account@example.com"), editableFixture(42, "account@example.com")}})
			}, nil)
			info, err := client.CheckAuthJSON(context.Background(), "supplier-user", "private-password-中文", Target{Account: "account@example.com", RecordID: tc.recordID})
			if tc.want != "" {
				requireSupplierCode(t, err, tc.want)
			} else if err != nil || info.RecordID != "42" {
				t.Fatalf("wrong record: %+v %v", info, err)
			}
		})
	}
}

func TestTargetedLookupNotFoundAndBoundedPagination(t *testing.T) {
	for _, tc := range []struct {
		name  string
		total any
		rows  []any
		code  string
	}{
		{"no-exact-match", 1, []any{editableFixture(1, "not-account@example.com")}, CodeAccountNotFound},
		{"empty", 0, []any{}, CodeAccountNotFound},
		{"ignored-filter-too-large", 1001, []any{editableFixture(1, "account@example.com")}, CodeContractMismatch},
		{"missing-row-count", nil, []any{}, CodeContractMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, f := authClient(t, func(*http.Request, int) *http.Response {
				return jsonResponse(200, map[string]any{"total": tc.total, "list": tc.rows})
			}, nil)
			_, err := client.CheckAuthJSON(context.Background(), "supplier-user", "private-password-中文", Target{Account: "account@example.com"})
			requireSupplierCode(t, err, tc.code)
			if f.reads != 1 {
				t.Fatal("fallback to account enumeration")
			}
		})
	}
}

func TestAuthJSONEligibilityMatchesSupplierRawStateAndLock(t *testing.T) {
	for _, tc := range []struct {
		name, status     string
		platform, locked any
		editable         bool
	}{
		{"active", "using", 13152, 0, false}, {"login failed", "login_failed", 13152, 0, true},
		{"banned", "ban", 13152, 0, true}, {"offline", "offline", 13152, 0, true},
		{"sync_failed-with-platform", "sync_failed", 13152, 0, false}, {"not-synced", "sync_failed", 0, 0, true},
		{"locked", "login_failed", 13152, 1, false}, {"locked-before-sync", "", 0, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := editableFixture(99, "account@example.com")
			row["a1_status"], row["a1_id"], row["is_locked"] = tc.status, tc.platform, tc.locked
			client, _ := authClient(t, oneRecord(row), nil)
			info, err := client.CheckAuthJSON(context.Background(), "supplier-user", "private-password-中文", Target{Account: "account@example.com"})
			if err != nil || info.Editable != tc.editable {
				t.Fatalf("unexpected eligibility: %+v %v", info, err)
			}
			if !tc.editable && info.Reason == "" {
				t.Fatal("missing explanation")
			}
		})
	}
}

func TestAuthJSONUpdatePreservesFreshMetadataAndDoesNotResendSecrets(t *testing.T) {
	puts := 0
	row := editableFixture(99, "account@example.com")
	client, f := authClient(t, oneRecord(row), func(r *http.Request) (*http.Response, error) {
		puts++
		if r.URL.Path != apiPrefix+"/accounts/99" || r.Header.Get("Authorization") != "Bearer token-1" || r.Header.Get("Content-Type") != "application/json" {
			t.Fatal("invalid edit request")
		}
		if r.GetBody != nil {
			t.Fatal("mutating request is replayable")
		}
		data, _ := io.ReadAll(r.Body)
		if strings.Contains(string(data), "OLD_") || strings.Contains(string(data), "UNRELATED_SECRET") {
			t.Fatal("old supplier secrets were forwarded")
		}
		var payload map[string]any
		if json.Unmarshal(data, &payload) != nil {
			t.Fatal("invalid payload")
		}
		for k, want := range map[string]any{"provider": "claude", "model": "latest-model", "account_type": float64(2), "account_id": "account@example.com", "remark": "Current vendor remark — keep this", "cpa_plan": "claude/cc-pro", "cpa_register_time": "2026-09-01", "cpa_plan_start_time": "2026-11-01", "cpa_plan_end_time": "2027-01-01", "cpa_register_location": "MY", "cpa_balance": "0", "cpa_payment_method": "physical_credit_card", "cpa_register_device": "Mac", "cpa_recharge_time": "2026-11-01", "cpa_warranty_period": "7", "cpa_auth_json": `{"access_token":"NEW_SECRET"}`, "api_key": ""} {
			if payload[k] != want {
				t.Errorf("field %s changed: got %v want %v", k, payload[k], want)
			}
		}
		for _, k := range []string{"enc_api_key", "enc_api_secret", "password", "auth_json", "cpa_tags", "tags", "extra"} {
			if _, ok := payload[k]; ok {
				t.Errorf("unexpected field %s", k)
			}
		}
		if decryptPassword(t, payload["enc_adc_config"].(string)) != "" {
			t.Fatal("ADC envelope must encrypt empty text, not old auth JSON")
		}
		return jsonResponse(200, map[string]any{"msg": "修改成功"}), nil
	})
	info, err := client.UpdateAuthJSON(context.Background(), "supplier-user", "private-password-中文", Target{Account: "account@example.com"}, json.RawMessage(`{"access_token":"NEW_SECRET"}`))
	if err != nil || info.RecordID != "99" || puts != 1 || f.reads != 1 || f.logins != 1 {
		t.Fatalf("bad update info=%+v error=%v writes=%d", info, err, puts)
	}
}

func TestAuthJSONUpdateRechecksEligibilityInsteadOfTrustingPreflight(t *testing.T) {
	client, f := authClient(t, func(_ *http.Request, n int) *http.Response {
		row := editableFixture(99, "account@example.com")
		if n > 1 {
			row["a1_status"] = "using"
		}
		return jsonResponse(200, map[string]any{"total": 1, "list": []any{row}})
	}, nil)
	target := Target{Account: "account@example.com"}
	info, err := client.CheckAuthJSON(context.Background(), "supplier-user", "private-password-中文", target)
	if err != nil || !info.Editable {
		t.Fatal("preflight should be editable")
	}
	_, err = client.UpdateAuthJSON(context.Background(), "supplier-user", "private-password-中文", target, json.RawMessage(`{"access_token":"new"}`))
	requireSupplierCode(t, err, CodeNotEditable)
	if f.logins != 2 || f.reads != 2 {
		t.Fatal("update reused stale session or row")
	}
}

func TestAuthJSONUpdateRejectsIncompleteMetadataBeforePUT(t *testing.T) {
	row := editableFixture(99, "account@example.com")
	row["auth_config"] = `{"account":"account@example.com","plan":"claude/cc-pro"}`
	client, _ := authClient(t, oneRecord(row), nil)
	_, err := client.UpdateAuthJSON(context.Background(), "supplier-user", "private-password-中文", Target{Account: "account@example.com"}, json.RawMessage(`{"access_token":"new"}`))
	requireSupplierCode(t, err, CodeContractMismatch)
}

func TestAuthJSONUpdateInvalidJSONDoesNotContactSupplier(t *testing.T) {
	client := &Client{transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid input reached supplier")
		return nil, nil
	})}
	for _, raw := range []string{"", "null", "{}", "[]", `"{}"`, `{"broken":`, strings.Repeat("a", maxAuthJSONBytes+1)} {
		_, err := client.UpdateAuthJSON(context.Background(), "supplier-user", "private-password-中文", Target{Account: "account@example.com"}, json.RawMessage(raw))
		requireSupplierCode(t, err, CodeInvalidAuthJSON)
	}
}

func TestTargetedReadReauthenticatesOnce(t *testing.T) {
	client, f := authClient(t, func(_ *http.Request, n int) *http.Response {
		if n == 1 {
			return jsonResponse(401, map[string]any{"msg": "TOKEN_SECRET"})
		}
		return jsonResponse(200, map[string]any{"list": []any{editableFixture(99, "account@example.com")}, "total": 1})
	}, nil)
	if _, err := client.CheckAuthJSON(context.Background(), "supplier-user", "private-password-中文", Target{Account: "account@example.com"}); err != nil {
		t.Fatal(err)
	}
	if f.logins != 2 || f.reads != 2 {
		t.Fatal("read retry not bounded as intended")
	}
}

func TestAuthJSONUpdateOutcomeAndNoWriteRetries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   any
		err    error
		want   string
	}{
		{"accepted", 200, map[string]any{"success": true}, nil, ""},
		{"no-content", 204, nil, nil, ""},
		{"rejected-status", 400, map[string]any{"msg": "SECRET"}, nil, CodeUpdateRejected},
		{"expired-session", 401, map[string]any{"msg": "TOKEN_SECRET"}, nil, CodeUpdateRejected},
		{"body-false", 200, map[string]any{"success": false}, nil, CodeUpdateRejected},
		{"body-code-error", 200, map[string]any{"code": 400, "message": "SECRET"}, nil, CodeUpdateRejected},
		{"bad-success-body", 200, "unexpected SECRET", nil, CodeUpdateUnconfirmed},
		{"server-error", 500, map[string]any{"msg": "SECRET"}, nil, CodeUpdateUnconfirmed},
		{"timeout", 0, nil, context.DeadlineExceeded, CodeUpdateUnconfirmed},
		{"transport-unknown", 0, nil, errors.New("secret request body SECRET"), CodeUpdateUnconfirmed},
		{"dial-failed", 0, nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("SECRET")}, CodeUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writes := 0
			client, f := authClient(t, oneRecord(editableFixture(99, "account@example.com")), func(*http.Request) (*http.Response, error) {
				writes++
				if tc.err != nil {
					return nil, tc.err
				}
				return jsonResponse(tc.status, tc.body), nil
			})
			info, err := client.UpdateAuthJSON(context.Background(), "supplier-user", "private-password-中文", Target{Account: "account@example.com"}, json.RawMessage(`{"access_token":"new"}`))
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				requireSupplierCode(t, err, tc.want)
			}
			if writes != 1 || f.logins != 1 || info.RecordID != "99" {
				t.Fatalf("write retried or audit ID lost: writes=%d logins=%d info=%+v", writes, f.logins, info)
			}
		})
	}
}

func TestCollectAccountRefreshesTargetUsageWithFreshLogin(t *testing.T) {
	client, f := authClient(t, oneRecord(editableFixture(99, "account@example.com")), nil)
	a, err := client.CollectAccount(context.Background(), "supplier-user", "private-password-中文", Target{Account: "account@example.com", RecordID: "99"})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "99" || a.CostTotal == nil || *a.CostTotal != 6800.24 || a.Currency == nil || *a.Currency != "CNY" || a.Usage7DPercent == nil || *a.Usage7DPercent != 40 || f.logins != 1 || f.reads != 1 {
		t.Fatalf("incorrect immediate refresh: %+v", a)
	}
}

func TestAuthJSONUpdateChecksFreshResolvedIdentityBeforePUT(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(fmt.Sprintf("reject=%t", reject), func(t *testing.T) {
			puts, checks := 0, 0
			guardErr := errors.New("local ownership conflict")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client, fixture := authClient(t, oneRecord(editableFixture(99, "account@example.com")), func(r *http.Request) (*http.Response, error) {
				puts++
				if checks != 1 {
					t.Fatal("PUT ran before local ownership guard")
				}
				return jsonResponse(200, map[string]bool{"success": true}), nil
			})
			target := Target{Account: "account@example.com", RecordID: "88", BeforeUpdate: func(gotCtx context.Context, info EditInfo) error {
				checks++
				if gotCtx != ctx || info.RecordID != "99" || info.Account != "account@example.com" || !info.Editable {
					t.Fatal("guard did not receive fresh exact-match identity and bounded context")
				}
				if reject {
					return guardErr
				}
				return nil
			}}
			if _, err := client.CheckAuthJSON(ctx, "supplier-user", "private-password-中文", target); err != nil || checks != 0 {
				t.Fatal("read-only preflight invoked mutation guard", err)
			}
			info, err := client.UpdateAuthJSON(ctx, "supplier-user", "private-password-中文", target, json.RawMessage(`{"access_token":"new"}`))
			if checks != 1 || info.RecordID != "99" || fixture.logins != 2 || fixture.reads != 2 {
				t.Fatal("guard skipped, repeated, or added extra supplier lookup")
			}
			if reject {
				if puts != 0 || !errors.Is(err, guardErr) {
					t.Fatal("ownership rejection allowed PUT or lost typed error", err)
				}
			} else if puts != 1 || err != nil {
				t.Fatal("approved write did not run exactly once", err)
			}
		})
	}
}

func TestAuthJSONUpdateDoesNotRunOwnershipGuardForIneligibleRecord(t *testing.T) {
	row := editableFixture(99, "account@example.com")
	row["is_locked"] = 1
	client, _ := authClient(t, oneRecord(row), nil)
	target := Target{Account: "account@example.com", BeforeUpdate: func(context.Context, EditInfo) error {
		t.Fatal("guard ran for supplier-rejected account")
		return nil
	}}
	_, err := client.UpdateAuthJSON(context.Background(), "supplier-user", "private-password-中文", target, json.RawMessage(`{"access_token":"new"}`))
	requireSupplierCode(t, err, CodeNotEditable)
}
