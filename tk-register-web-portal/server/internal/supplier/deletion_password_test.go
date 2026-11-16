package supplier

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

func TestChangePasswordForDeletionPreservesFreshMetadata(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "active", true: "already-expired"}[expired], func(t *testing.T) {
			row := editableFixture(99, "account@example.com")
			row["a1_status"], row["a1_id"] = "using", 13152
			row["region"], row["proxy_type"], row["proxy_region"] = "current-region", "current-proxy", "current-proxy-region"
			start, end := "2026-11-01", "2027-01-01"
			if expired {
				start, end = ExpiredPlanDate, ExpiredPlanDate
				setPlanDates(t, row, start, end)
			}
			puts, guards, journalWrites := 0, 0, 0
			client, f := authClient(t, oneRecord(row), func(r *http.Request) (*http.Response, error) {
				puts++
				if guards != 1 || journalWrites != 1 {
					t.Fatal("password request preceded ownership guard or durable intent")
				}
				if r.URL.Path != apiPrefix+"/accounts/99" || r.Header.Get("Authorization") != "Bearer token-1" || r.GetBody != nil {
					t.Fatal("wrong or replayable mutation request")
				}
				data, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(data), "OLD_") || strings.Contains(string(data), "qazwsxedc") {
					t.Fatal("request exposed stored credentials or the replacement plaintext")
				}
				var payload map[string]any
				if json.Unmarshal(data, &payload) != nil {
					t.Fatal("invalid update payload")
				}
				for key, want := range map[string]any{
					"provider": "claude", "model": "latest-model", "account_type": float64(2), "account_id": "account@example.com",
					"remark": "Current vendor remark — keep this", "cpa_plan": "claude/cc-pro", "cpa_register_time": "2026-09-01",
					"cpa_plan_start_time": start, "cpa_plan_end_time": end, "cpa_register_location": "MY",
					"cpa_balance": "0", "cpa_payment_method": "physical_credit_card", "cpa_register_device": "Mac",
					"cpa_recharge_time": "2026-11-01", "cpa_warranty_period": "7", "region": "current-region",
					"proxy_type": "current-proxy", "proxy_region": "current-proxy-region", "cpa_auth_json": "", "api_key": "",
				} {
					if payload[key] != want {
						t.Errorf("field %s: got %v want %v", key, payload[key], want)
					}
				}
				for _, key := range []string{"enc_api_secret", "password", "auth_json", "auth_config", "cpa_tags", "tags", "extra"} {
					if _, exists := payload[key]; exists {
						t.Errorf("unexpected sensitive or unrelated field: %s", key)
					}
				}
				passwordEnvelope, ok := payload["enc_api_key"].(string)
				if !ok || decryptPassword(t, passwordEnvelope) != "qazwsxedc****" {
					t.Fatal("replacement password did not use the exact encrypted supplier contract")
				}
				adcEnvelope, ok := payload["enc_adc_config"].(string)
				if !ok || decryptPassword(t, adcEnvelope) != "" {
					t.Fatal("unchanged ADC marker was not preserved")
				}
				return jsonResponse(200, map[string]any{"msg": "修改成功"}), nil
			})
			target := Target{Account: "account@example.com", RecordID: "99", BeforeUpdate: func(_ context.Context, info EditInfo) error {
				guards++
				if info.RecordID != "99" || info.Plan != "claude/cc-pro" || info.Editable {
					t.Fatal("incorrect active-account guard input")
				}
				return nil
			}}
			result, err := client.ChangePasswordForDeletion(context.Background(), "supplier-user", "private-password-中文", target, true, func(_ context.Context, info ExpireInfo) error {
				journalWrites++
				if info.RecordID != "99" || info.PlanStartTime != start || info.PlanEndTime != end {
					t.Fatal("journal lost current supplier metadata")
				}
				return nil
			})
			if err != nil || !result.Found || !result.Confirmed || !result.WriteAttempted || puts != 1 || f.reads != 1 || f.logins != 1 {
				t.Fatalf("unexpected result=%+v error=%v puts=%d reads=%d", result, err, puts, f.reads)
			}
			if result.Info.PlanStartTime != start || result.Info.PlanEndTime != end {
				t.Fatal("password update changed the reported dates")
			}
			encoded, _ := json.Marshal(result)
			if strings.Contains(string(encoded), "password") || strings.Contains(string(encoded), "qazwsxedc") || strings.Contains(string(encoded), "OLD_") {
				t.Fatal("result exposed credentials")
			}
		})
	}
}

func TestChangePasswordForDeletionAbsentRequiresCompleteSearch(t *testing.T) {
	for _, allowWrite := range []bool{false, true} {
		client, f := authClient(t, func(_ *http.Request, n int) *http.Response {
			return jsonResponse(200, map[string]any{"total": 2, "list": []any{editableFixture(n, "prefix-account@example.com")}})
		}, nil)
		result, err := client.ChangePasswordForDeletion(context.Background(), "supplier-user", "private-password-中文", Target{Account: "account@example.com", RecordID: "99"}, allowWrite, func(context.Context, ExpireInfo) error {
			t.Fatal("absent target reached mutation callback")
			return nil
		})
		if err != nil || !result.Confirmed || result.Found || result.WriteAttempted || f.reads != 2 {
			t.Fatalf("bad absence result: %+v %v reads=%d", result, err, f.reads)
		}
	}
}

func TestChangePasswordForDeletionFailedSearchIsNotAbsence(t *testing.T) {
	for _, status := range []int{401, 403, 404, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			client, _ := authClient(t, func(*http.Request, int) *http.Response {
				return jsonResponse(status, map[string]any{"error": "PRIVATE_UPSTREAM_SECRET"})
			}, nil)
			result, err := client.ChangePasswordForDeletion(context.Background(), "supplier-user", "private-password-中文", Target{Account: "account@example.com"}, true, nil)
			if err == nil || result.Confirmed || result.WriteAttempted || strings.Contains(err.Error(), "PRIVATE_UPSTREAM_SECRET") {
				t.Fatalf("failed search confirmed absence: %+v %v", result, err)
			}
		})
	}
}

func TestChangePasswordForDeletionRejectsChangedIdentityLocksAndIncompleteMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		alter      func(map[string]any)
	}{
		{"changed-record", CodeAccountIdentityChanged, func(row map[string]any) { row["id"] = 100 }},
		{"locked", CodeDeletionPasswordLocked, func(row map[string]any) { row["is_locked"] = 1 }},
		{"lock-missing", CodeContractMismatch, func(row map[string]any) { delete(row, "is_locked") }},
		{"incomplete-config", CodeContractMismatch, func(row map[string]any) {
			row["auth_config"] = `{"account":"account@example.com","plan":"claude/cc-pro"}`
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := editableFixture(99, "account@example.com")
			tc.alter(row)
			client, _ := authClient(t, oneRecord(row), nil)
			result, err := client.ChangePasswordForDeletion(context.Background(), "supplier-user", "private-password-中文", Target{Account: "account@example.com", RecordID: "99"}, true, func(context.Context, ExpireInfo) error {
				t.Fatal("invalid target reached durable intent callback")
				return nil
			})
			requireSupplierCode(t, err, tc.code)
			if result.Confirmed || result.WriteAttempted {
				t.Fatal("invalid target was changed or confirmed")
			}
		})
	}
}

func TestChangePasswordForDeletionRequiresDurableAndOwnershipGuards(t *testing.T) {
	guardErr := errors.New("ownership changed")
	for _, scenario := range []string{"ownership", "journal", "missing-journal"} {
		t.Run(scenario, func(t *testing.T) {
			client, _ := authClient(t, oneRecord(editableFixture(99, "account@example.com")), nil)
			target := Target{Account: "account@example.com", RecordID: "99"}
			var callback func(context.Context, ExpireInfo) error
			if scenario == "ownership" {
				target.BeforeUpdate = func(context.Context, EditInfo) error { return guardErr }
				callback = func(context.Context, ExpireInfo) error { t.Fatal("journal after failed ownership"); return nil }
			} else if scenario == "journal" {
				callback = func(context.Context, ExpireInfo) error { return guardErr }
			}
			result, err := client.ChangePasswordForDeletion(context.Background(), "supplier-user", "private-password-中文", target, true, callback)
			if scenario == "missing-journal" {
				requireSupplierCode(t, err, CodeContractMismatch)
			} else if !errors.Is(err, guardErr) {
				t.Fatalf("wrong guard error: %v", err)
			}
			if result.WriteAttempted || result.Confirmed {
				t.Fatal("guard failure allowed mutation")
			}
		})
	}
}

func TestChangePasswordForDeletionReadOnlyCannotConfirmPassword(t *testing.T) {
	for _, scenario := range []string{"active", "expired", "masked", "matching-password"} {
		t.Run(scenario, func(t *testing.T) {
			row := editableFixture(99, "account@example.com")
			row["a1_status"], row["a1_id"] = "using", 13152
			if scenario == "expired" {
				setPlanDates(t, row, ExpiredPlanDate, ExpiredPlanDate)
			}
			if scenario == "masked" || scenario == "matching-password" {
				value := "********"
				if scenario == "matching-password" {
					value = "qazwsxedc****"
				}
				row["auth_config"] = strings.ReplaceAll(row["auth_config"].(string), "OLD_PASSWORD_MUST_NOT_LEAK", value)
			}
			client, f := authClient(t, oneRecord(row), nil)
			result, err := client.ChangePasswordForDeletion(context.Background(), "supplier-user", "private-password-中文", Target{Account: "account@example.com", RecordID: "99"}, false, func(context.Context, ExpireInfo) error {
				t.Fatal("read-only attempt reached durable intent callback")
				return nil
			})
			if err != nil || !result.Found || result.Confirmed || result.WriteAttempted || f.reads != 1 {
				t.Fatalf("metadata falsely confirmed password: %+v %v", result, err)
			}
		})
	}
}

func TestChangePasswordForDeletionClassifiesWritesAndNeverReplays(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		status     int
		unsent     bool
	}{
		{"refused", CodeDeletionPasswordRejected, 400, false},
		{"expired-session", CodeDeletionPasswordRejected, 401, false},
		{"forbidden", CodeDeletionPasswordRejected, 403, false},
		{"rate-limited", CodeDeletionPasswordRejected, 429, false},
		{"server-failure", CodeDeletionPasswordUnconfirmed, 500, false},
		{"negative-ack", CodeDeletionPasswordRejected, 200, false},
		{"transport", CodeDeletionPasswordUnconfirmed, 0, false},
		{"deadline", CodeDeletionPasswordUnconfirmed, 0, false},
		{"body-read", CodeDeletionPasswordUnconfirmed, 200, false},
		{"dial", CodeDeletionPasswordNotSent, 0, true},
		{"dns", CodeDeletionPasswordNotSent, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := editableFixture(99, "account@example.com")
			puts := 0
			client, f := authClient(t, oneRecord(row), func(*http.Request) (*http.Response, error) {
				puts++
				setPlanDates(t, row, ExpiredPlanDate, ExpiredPlanDate)
				switch tc.name {
				case "transport":
					return nil, io.ErrUnexpectedEOF
				case "deadline":
					return nil, context.DeadlineExceeded
				case "dial":
					return nil, &net.OpError{Op: "dial", Err: errors.New("SECRET_CONNECTION_DETAILS")}
				case "dns":
					return nil, &net.DNSError{Err: "SECRET_DNS_DETAILS", Name: "supplier.invalid"}
				case "body-read":
					return &http.Response{StatusCode: 200, Body: planDatesBrokenBody{}, Header: make(http.Header)}, nil
				case "negative-ack":
					return jsonResponse(200, map[string]any{"success": false, "message": "PRIVATE_UPSTREAM_SECRET"}), nil
				default:
					return jsonResponse(tc.status, map[string]any{"error": "PRIVATE_UPSTREAM_SECRET"}), nil
				}
			})
			target := Target{Account: "account@example.com", RecordID: "99"}
			result, err := client.ChangePasswordForDeletion(context.Background(), "supplier-user", "private-password-中文", target, true, func(context.Context, ExpireInfo) error { return nil })
			requireSupplierCode(t, err, tc.code)
			var problem *Error
			if !errors.As(err, &problem) || problem.RequestNotSent != tc.unsent || result.WriteAttempted == tc.unsent || result.Confirmed || puts != 1 || f.reads != 1 || f.logins != 1 {
				t.Fatalf("wrong failure outcome: result=%+v error=%+v puts=%d", result, problem, puts)
			}
			result, err = client.ChangePasswordForDeletion(context.Background(), "supplier-user", "private-password-中文", target, false, nil)
			if err != nil || !result.Found || result.Confirmed || result.WriteAttempted || puts != 1 || f.reads != 2 {
				t.Fatalf("read-only resume replayed or falsely confirmed update: %+v %v", result, err)
			}
		})
	}
}

func TestChangePasswordForDeletionCancellationAfterJournalIsNotSent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client, f := authClient(t, oneRecord(editableFixture(99, "account@example.com")), nil)
	journal := 0
	result, err := client.ChangePasswordForDeletion(ctx, "supplier-user", "private-password-中文", Target{Account: "account@example.com", RecordID: "99"}, true, func(context.Context, ExpireInfo) error {
		journal++
		cancel()
		return nil
	})
	requireSupplierCode(t, err, CodeDeletionPasswordNotSent)
	var problem *Error
	if !errors.As(err, &problem) || !problem.RequestNotSent || journal != 1 || f.reads != 1 || result.WriteAttempted || result.Confirmed {
		t.Fatalf("wrong cancellation outcome: result=%+v error=%+v", result, problem)
	}
}

func TestChangePasswordForDeletionRequiresPositiveAcknowledgement(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   any
		code   string
	}{
		{"supplier-contract", 200, map[string]any{"msg": "修改成功"}, ""},
		{"success", 200, map[string]any{"success": true}, ""},
		{"ok", 200, map[string]any{"ok": true}, ""},
		{"zero-code", 200, map[string]any{"code": 0}, ""},
		{"success-code", 200, map[string]any{"code": "200"}, ""},
		{"no-content", 204, nil, ""},
		{"empty-object", 200, map[string]any{}, CodeDeletionPasswordUnconfirmed},
		{"unknown-object", 200, map[string]any{"result": "unknown"}, CodeDeletionPasswordUnconfirmed},
		{"unknown-status", 200, map[string]any{"status": "mystery", "success": true}, CodeDeletionPasswordUnconfirmed},
		{"queued-status", 200, map[string]any{"status": "queued", "success": true}, CodeDeletionPasswordUnconfirmed},
		{"queued-code", 200, map[string]any{"code": 202, "success": true}, CodeDeletionPasswordUnconfirmed},
		{"queued-http", 202, map[string]any{"success": true}, CodeDeletionPasswordUnconfirmed},
		{"queued-http-message", 202, map[string]any{"msg": "修改成功"}, CodeDeletionPasswordUnconfirmed},
		{"failure-status", 200, map[string]any{"status": "error"}, CodeDeletionPasswordRejected},
		{"failure-message", 200, map[string]any{"msg": "修改失败"}, CodeDeletionPasswordRejected},
		{"failure-details", 200, map[string]any{"message": "修改失败：PRIVATE_UPSTREAM_SECRET"}, CodeDeletionPasswordRejected},
		{"failure-code", 200, map[string]any{"code": 400}, CodeDeletionPasswordRejected},
		{"conflicting-booleans", 200, map[string]any{"success": true, "ok": false}, CodeDeletionPasswordRejected},
		{"conflicting-message", 200, map[string]any{"success": true, "msg": "修改失败"}, CodeDeletionPasswordRejected},
		{"conflicting-status", 200, map[string]any{"code": 0, "status": "failed"}, CodeDeletionPasswordRejected},
		{"conflicting-code", 200, map[string]any{"code": 400, "msg": "修改成功"}, CodeDeletionPasswordRejected},
		{"conflicting-error", 200, map[string]any{"success": true, "error": "PRIVATE_UPSTREAM_SECRET"}, CodeDeletionPasswordRejected},
		{"rejection-over-malformed-marker", 200, map[string]any{"success": "unknown", "status": "error"}, CodeDeletionPasswordRejected},
		{"rejection-over-unknown-status", 200, map[string]any{"status": "mystery", "ok": false}, CodeDeletionPasswordRejected},
		{"null-marker", 200, map[string]any{"success": nil}, CodeDeletionPasswordUnconfirmed},
		{"non-object", 200, "PRIVATE_UPSTREAM_SECRET", CodeDeletionPasswordUnconfirmed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			puts := 0
			client, f := authClient(t, oneRecord(editableFixture(99, "account@example.com")), func(*http.Request) (*http.Response, error) {
				puts++
				return jsonResponse(tc.status, tc.body), nil
			})
			result, err := client.ChangePasswordForDeletion(context.Background(), "supplier-user", "private-password-中文", Target{Account: "account@example.com", RecordID: "99"}, true, func(context.Context, ExpireInfo) error { return nil })
			if tc.code != "" {
				requireSupplierCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			}
			if result.Confirmed != (tc.code == "") || !result.Found || !result.WriteAttempted || puts != 1 || f.reads != 1 || f.logins != 1 {
				t.Fatalf("incorrect acknowledgement: result=%+v error=%v puts=%d reads=%d", result, err, puts, f.reads)
			}
		})
	}
}

func TestNonDeletionUpdatePreservesExistingAcknowledgementContract(t *testing.T) {
	for _, status := range []int{200, 202} {
		client := &Client{transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return jsonResponse(status, map[string]any{}), nil
		})}
		s := client.newSession()
		defer s.http.CloseIdleConnections()
		if err := s.updateRequest(context.Background(), "99", map[string]any{}); err != nil {
			t.Fatalf("legacy HTTP %d acknowledgement changed: %v", status, err)
		}
	}
}
