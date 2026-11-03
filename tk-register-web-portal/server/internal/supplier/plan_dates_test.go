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

func setPlanDates(t *testing.T, row map[string]any, start, end string) {
	t.Helper()
	var config map[string]any
	if json.Unmarshal([]byte(row["auth_config"].(string)), &config) != nil {
		t.Fatal("invalid fixture")
	}
	config["plan_start_time"], config["plan_end_time"] = start, end
	b, _ := json.Marshal(config)
	row["auth_config"] = string(b)
}

func TestExpireAccountUsesFreshConfigurationAndPreservesCredentials(t *testing.T) {
	row := editableFixture(99, "account@example.com")
	// Active accounts lock credential editing, but the supplier keeps plan
	// date controls editable. This operation must not inherit that Auth JSON rule.
	row["a1_status"], row["a1_id"] = "using", 13152
	row["region"], row["proxy_type"], row["proxy_region"] = "current-region", "current-proxy", "current-proxy-region"
	puts, guards, journalWrites := 0, 0, 0
	client, f := authClient(t, oneRecord(row), func(r *http.Request) (*http.Response, error) {
		puts++
		if guards != 1 || journalWrites != 1 {
			t.Fatal("request was sent before identity guard and durable journal callback")
		}
		if r.URL.Path != apiPrefix+"/accounts/99" || r.Header.Get("Authorization") != "Bearer token-1" || r.GetBody != nil {
			t.Fatal("wrong/replayable mutation request")
		}
		data, _ := io.ReadAll(r.Body)
		if strings.Contains(string(data), "OLD_") {
			t.Fatal("old credentials were forwarded")
		}
		var payload map[string]any
		if json.Unmarshal(data, &payload) != nil {
			t.Fatal("invalid update payload")
		}
		for key, want := range map[string]any{
			"provider": "claude", "model": "latest-model", "account_type": float64(2), "account_id": "account@example.com",
			"remark": "Current vendor remark — keep this", "cpa_plan": "claude/cc-pro", "cpa_register_time": "2026-09-01",
			"cpa_plan_start_time": ExpiredPlanDate, "cpa_plan_end_time": ExpiredPlanDate, "cpa_register_location": "MY",
			"cpa_balance": "0", "cpa_payment_method": "physical_credit_card", "cpa_register_device": "Mac",
			"cpa_recharge_time": "2026-11-01", "cpa_warranty_period": "7", "region": "current-region",
			"proxy_type": "current-proxy", "proxy_region": "current-proxy-region", "cpa_auth_json": "",
		} {
			if payload[key] != want {
				t.Errorf("field %s: got %v want %v", key, payload[key], want)
			}
		}
		for _, key := range []string{"enc_api_key", "enc_api_secret", "password", "auth_json", "auth_config", "cpa_tags", "tags", "extra"} {
			if _, exists := payload[key]; exists {
				t.Errorf("unexpected sensitive/unrelated field: %s", key)
			}
		}
		if decryptPassword(t, payload["enc_adc_config"].(string)) != "" {
			t.Fatal("metadata edit must use the supplier's unchanged ADC marker")
		}
		setPlanDates(t, row, ExpiredPlanDate, ExpiredPlanDate)
		return jsonResponse(200, map[string]any{"msg": "修改成功"}), nil
	})
	target := Target{Account: "account@example.com", RecordID: "99", BeforeUpdate: func(_ context.Context, info EditInfo) error {
		guards++
		if info.RecordID != "99" || info.Plan != "claude/cc-pro" {
			t.Fatal("identity guard received wrong record")
		}
		return nil
	}}
	result, err := client.ExpireAccount(context.Background(), "supplier-user", "private-password-中文", target, true, func(_ context.Context, info ExpireInfo) error {
		journalWrites++
		if info.PlanStartTime != "2026-11-01" || info.PlanEndTime != "2027-01-01" {
			t.Fatal("journal did not receive original supplier dates")
		}
		return nil
	})
	if err != nil || !result.Found || !result.Confirmed || !result.WriteAttempted || puts != 1 || f.reads != 2 || f.logins != 1 {
		t.Fatalf("unexpected result=%+v error=%v puts=%d reads=%d", result, err, puts, f.reads)
	}
	if result.Info.PlanStartTime != "2026-11-01" || result.Info.PlanEndTime != "2027-01-01" {
		t.Fatal("verification overwrote before-change metadata")
	}
}

func TestExpireAccountAbsentOrAlreadyExpiredDoesNotWrite(t *testing.T) {
	for _, absent := range []bool{true, false} {
		t.Run(map[bool]string{true: "absent", false: "already-expired"}[absent], func(t *testing.T) {
			row := editableFixture(99, "account@example.com")
			setPlanDates(t, row, ExpiredPlanDate, ExpiredPlanDate)
			client, f := authClient(t, func(*http.Request, int) *http.Response {
				if absent {
					return jsonResponse(200, map[string]any{"total": 0, "list": []any{}})
				}
				return jsonResponse(200, map[string]any{"total": 1, "list": []any{row}})
			}, nil)
			result, err := client.ExpireAccount(context.Background(), "supplier-user", "private-password-中文", Target{Account: "account@example.com", RecordID: "99"}, true, func(context.Context, ExpireInfo) error {
				t.Fatal("unneeded journal/mutation callback")
				return nil
			})
			if err != nil || !result.Confirmed || result.Found == absent || result.WriteAttempted || f.reads != 1 {
				t.Fatalf("unexpected no-write result: %+v %v", result, err)
			}
		})
	}
}

func TestExpireAccountReadFailureIsNotAbsence(t *testing.T) {
	for _, status := range []int{401, 403, 404, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			client, _ := authClient(t, func(*http.Request, int) *http.Response {
				return jsonResponse(status, map[string]any{"error": "PRIVATE_UPSTREAM_SECRET"})
			}, nil)
			result, err := client.ExpireAccount(context.Background(), "supplier-user", "private-password-中文", Target{Account: "account@example.com"}, true, nil)
			if err == nil || result.Confirmed || result.WriteAttempted || strings.Contains(err.Error(), "PRIVATE_UPSTREAM_SECRET") {
				t.Fatalf("failed query was treated as confirmed absence: %+v %v", result, err)
			}
		})
	}
}

func TestExpireAccountRejectsChangedIdentityLocksAndIncompleteMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		alter      func(map[string]any)
	}{
		{"changed-record", CodeAccountIdentityChanged, func(row map[string]any) { row["id"] = 100 }},
		{"locked", CodePlanDatesLocked, func(row map[string]any) { row["is_locked"] = 1 }},
		{"lock-missing", CodeContractMismatch, func(row map[string]any) { delete(row, "is_locked") }},
		{"incomplete-config", CodeContractMismatch, func(row map[string]any) {
			row["auth_config"] = `{"account":"account@example.com","plan":"claude/cc-pro"}`
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := editableFixture(99, "account@example.com")
			tc.alter(row)
			client, _ := authClient(t, oneRecord(row), nil)
			result, err := client.ExpireAccount(context.Background(), "supplier-user", "private-password-中文", Target{Account: "account@example.com", RecordID: "99"}, true, func(context.Context, ExpireInfo) error {
				t.Fatal("invalid target reached durable write callback")
				return nil
			})
			requireSupplierCode(t, err, tc.code)
			if result.Confirmed || result.WriteAttempted {
				t.Fatal("invalid target was written/confirmed")
			}
		})
	}
}

func TestExpireAccountRequiresDurableAndOwnershipGuards(t *testing.T) {
	guardErr := errors.New("ownership changed")
	for _, tc := range []string{"ownership", "journal", "missing-journal"} {
		t.Run(tc, func(t *testing.T) {
			client, _ := authClient(t, oneRecord(editableFixture(99, "account@example.com")), nil)
			target := Target{Account: "account@example.com", RecordID: "99"}
			var callback func(context.Context, ExpireInfo) error
			if tc == "ownership" {
				target.BeforeUpdate = func(context.Context, EditInfo) error { return guardErr }
				callback = func(context.Context, ExpireInfo) error { t.Fatal("journal after failed ownership"); return nil }
			} else if tc == "journal" {
				callback = func(context.Context, ExpireInfo) error { return guardErr }
			}
			result, err := client.ExpireAccount(context.Background(), "supplier-user", "private-password-中文", target, true, callback)
			if tc == "missing-journal" {
				requireSupplierCode(t, err, CodeContractMismatch)
			} else if !errors.Is(err, guardErr) {
				t.Fatalf("wrong guard error: %v", err)
			}
			if result.WriteAttempted || result.Confirmed {
				t.Fatal("guard failure did not prevent update")
			}
		})
	}
}

func TestExpireAccountUncertainWriteAndResumeNeverReplay(t *testing.T) {
	for _, persisted := range []bool{false, true} {
		t.Run(map[bool]string{false: "not-observed", true: "observed"}[persisted], func(t *testing.T) {
			row := editableFixture(99, "account@example.com")
			puts := 0
			client, f := authClient(t, oneRecord(row), func(*http.Request) (*http.Response, error) {
				puts++
				if persisted {
					setPlanDates(t, row, ExpiredPlanDate, ExpiredPlanDate)
				}
				return nil, io.ErrUnexpectedEOF
			})
			target := Target{Account: "account@example.com", RecordID: "99"}
			result, err := client.ExpireAccount(context.Background(), "supplier-user", "private-password-中文", target, true, func(context.Context, ExpireInfo) error { return nil })
			requireSupplierCode(t, err, CodePlanDatesUnconfirmed)
			if !result.WriteAttempted || result.Confirmed || puts != 1 || f.reads != 1 {
				t.Fatal("uncertain update retried or confirmed")
			}
			result, err = client.ExpireAccount(context.Background(), "supplier-user", "private-password-中文", target, false, nil)
			if err != nil || result.Confirmed != persisted || result.WriteAttempted || puts != 1 || f.reads != 2 {
				t.Fatalf("resume replayed update: %+v %v", result, err)
			}
		})
	}
}

func TestExpireAccountAcknowledgementRequiresReadback(t *testing.T) {
	for _, tc := range []struct {
		name       string
		readStatus int
		missing    bool
		code       string
		confirmed  bool
	}{
		{"unchanged", 200, false, "", false},
		{"removed", 200, true, "", true},
		{"query-failed", 500, false, CodeUnavailable, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := editableFixture(99, "account@example.com")
			puts := 0
			client, _ := authClient(t, func(_ *http.Request, n int) *http.Response {
				if n > 1 {
					if tc.missing {
						return jsonResponse(200, map[string]any{"total": 0, "list": []any{}})
					}
					if tc.readStatus != 200 {
						return jsonResponse(tc.readStatus, map[string]any{})
					}
				}
				return jsonResponse(200, map[string]any{"total": 1, "list": []any{row}})
			}, func(*http.Request) (*http.Response, error) {
				puts++
				return jsonResponse(200, map[string]any{"success": true}), nil
			})
			result, err := client.ExpireAccount(context.Background(), "supplier-user", "private-password-中文", Target{Account: "account@example.com", RecordID: "99"}, true, func(context.Context, ExpireInfo) error { return nil })
			if tc.code != "" {
				requireSupplierCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			}
			if result.Confirmed != tc.confirmed || !result.WriteAttempted || puts != 1 {
				t.Fatalf("bad verification: %+v", result)
			}
			if result.Info.PlanStartTime != "2026-11-01" {
				t.Fatal("lost original configuration")
			}
		})
	}
}

func TestExpireAccountWriteRejectionIsDistinctFromUncertainty(t *testing.T) {
	for _, status := range []int{400, 401, 403, 409, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			puts := 0
			client, f := authClient(t, oneRecord(editableFixture(99, "account@example.com")), func(*http.Request) (*http.Response, error) {
				puts++
				return jsonResponse(status, map[string]any{"error": "PRIVATE_UPSTREAM_SECRET"}), nil
			})
			result, err := client.ExpireAccount(context.Background(), "supplier-user", "private-password-中文", Target{Account: "account@example.com", RecordID: "99"}, true, func(context.Context, ExpireInfo) error { return nil })
			want := CodePlanDatesRejected
			if status >= 500 {
				want = CodePlanDatesUnconfirmed
			}
			requireSupplierCode(t, err, want)
			if result.Confirmed || !result.WriteAttempted || puts != 1 || f.reads != 1 || f.logins != 1 {
				t.Fatal("rejected write was replayed")
			}
		})
	}
}

func TestExpireAccountJournalCancellationBeforeSendIsDefinite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client, f := authClient(t, oneRecord(editableFixture(99, "account@example.com")), nil)
	journal := 0
	result, err := client.ExpireAccount(ctx, "supplier-user", "private-password-中文", Target{Account: "account@example.com", RecordID: "99"}, true, func(context.Context, ExpireInfo) error {
		journal++
		// Simulate cancellation immediately after the durable intent commits.
		cancel()
		return nil
	})
	requireSupplierCode(t, err, CodePlanDatesNotSent)
	var problem *Error
	if !errors.As(err, &problem) || !problem.RequestNotSent || journal != 1 || f.reads != 1 || result.WriteAttempted || result.Confirmed {
		t.Fatalf("definite pre-send failure misclassified: result=%+v error=%+v", result, problem)
	}
}

type planDatesBrokenBody struct{}

func (planDatesBrokenBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (planDatesBrokenBody) Close() error             { return nil }

func TestExpireAccountDistinguishesUnsentFromDispatchedFailures(t *testing.T) {
	for _, scenario := range []string{"dial", "dns", "deadline-after-dispatch", "response-body-read"} {
		t.Run(scenario, func(t *testing.T) {
			attempts := 0
			client, f := authClient(t, oneRecord(editableFixture(99, "account@example.com")), func(*http.Request) (*http.Response, error) {
				attempts++
				switch scenario {
				case "dial":
					return nil, &net.OpError{Op: "dial", Err: errors.New("SECRET_CONNECTION_DETAILS")}
				case "dns":
					return nil, &net.DNSError{Err: "SECRET_DNS_DETAILS", Name: "supplier.invalid"}
				case "deadline-after-dispatch":
					return nil, context.DeadlineExceeded
				default:
					return &http.Response{StatusCode: 200, Body: planDatesBrokenBody{}, Header: make(http.Header)}, nil
				}
			})
			result, err := client.ExpireAccount(context.Background(), "supplier-user", "private-password-中文", Target{Account: "account@example.com", RecordID: "99"}, true, func(context.Context, ExpireInfo) error { return nil })
			unsent := scenario == "dial" || scenario == "dns"
			code := CodePlanDatesUnconfirmed
			if unsent {
				code = CodePlanDatesNotSent
			}
			requireSupplierCode(t, err, code)
			var problem *Error
			if !errors.As(err, &problem) || problem.RequestNotSent != unsent || result.WriteAttempted == unsent || result.Confirmed || attempts != 1 || f.reads != 1 || f.logins != 1 {
				t.Fatalf("wrong write outcome: result=%+v error=%+v attempts=%d", result, problem, attempts)
			}
		})
	}
}

func TestUpdateRequestPreSendFailuresKeepAuthJSONErrorCodes(t *testing.T) {
	for _, scenario := range []string{"cancelled", "budget", "invalid-id", "serialization"} {
		t.Run(scenario, func(t *testing.T) {
			client := &Client{transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("local failure reached network")
				return nil, nil
			})}
			s := client.newSession()
			defer s.http.CloseIdleConnections()
			ctx := context.Background()
			id, payload := "99", map[string]any{}
			want := CodeContractMismatch
			switch scenario {
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
				want = CodeUnavailable
			case "budget":
				s.remaining = 0
			case "invalid-id":
				id = "not-an-id"
			case "serialization":
				payload["unsupported"] = make(chan int)
			}
			err := s.updateRequest(ctx, id, payload)
			requireSupplierCode(t, err, want)
			var problem *Error
			if !errors.As(err, &problem) || !problem.RequestNotSent {
				t.Fatal("missing definite no-send classification")
			}
		})
	}
}
