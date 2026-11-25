package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

func credentialTestAuth() map[string]any {
	return map[string]any{"id_token": "", "access_token": "saved-access-secret", "refresh_token": "saved-refresh-secret", "last_refresh": "2026-10-01T00:00:00Z", "email": "account@example.com", "account_uuid": "account-uuid", "organization_uuid": "organization-uuid", "organization_name": "Example", "claude_device_ids": []any{"device-one", "device-two"}, "type": "claude", "expired": "2026-12-01T00:00:00Z", "proxy_url": "http://private-user:private-pass@proxy.example:8080", "verification_status": "verified", "extension": map[string]any{"counter": json.Number("9007199254740993"), "flag": true}}
}

func seedCredentialSubmission(t *testing.T, s *Store, target SyncedAccount) {
	t.Helper()
	var submission Submission
	if err := s.DB.First(&submission, *target.CurrentSubmissionID).Error; err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"auth_json": credentialTestAuth(), "password": "submission-password"})
	encrypted, err := s.encrypt("submission:"+idString(submission.UserID)+":"+submission.ClientRequestKey, string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DB.Model(&submission).Update("secret_parameters_ciphertext", encrypted).Error; err != nil {
		t.Fatal(err)
	}
}

func TestAccountCredentialLegacyInitializationIsConservativeAndRepeatable(t *testing.T) {
	for _, outcome := range []string{"none", "updated", "unknown", "pending", "rejected", "failed", "timestamp-only", "unreadable-history"} {
		t.Run(outcome, func(t *testing.T) {
			s, user, target := authAuditFixture(t)
			ctx := context.Background()
			seedCredentialSubmission(t, s, target)
			if outcome == "unreadable-history" {
				if err := s.DB.Model(&Submission{}).Where("id = ?", *target.CurrentSubmissionID).Update("secret_parameters_ciphertext", "unreadable historical ciphertext").Error; err != nil {
					t.Fatal(err)
				}
			} else if outcome == "timestamp-only" {
				if err := s.DB.Model(&SyncedAccount{}).Where("id = ?", target.ID).Update("auth_json_updated_at", time.Now().UTC()).Error; err != nil {
					t.Fatal(err)
				}
			} else if outcome != "none" {
				audit, err := s.BeginAuthJSONUpdate(ctx, target, user.ID, "legacy-update")
				if err != nil {
					t.Fatal(err)
				}
				if outcome != "pending" {
					if _, err = s.FinishAuthJSONUpdate(ctx, audit, outcome, "350001", "", time.Now().UTC()); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := s.InitializeAccountCredentials(ctx); err != nil {
				t.Fatal(err)
			}
			if err := s.InitializeAccountCredentials(ctx); err != nil {
				t.Fatal(err)
			}
			info, err := s.AccountProxyInfo(ctx, target)
			eligible := outcome == "none" || outcome == "rejected" || outcome == "failed"
			if !eligible {
				if !errors.Is(err, ErrCurrentCredentialMissing) {
					t.Fatal("old submission unexpectedly used after credential update", err)
				}
				var count int64
				s.DB.Model(&AccountCredential{}).Count(&count)
				if count != 0 {
					t.Fatal("ineligible legacy account must not be initialized")
				}
				return
			}
			if err != nil || info.Revision != "1" || strings.Contains(info.ProxyURLMasked, "private") {
				t.Fatal("invalid initialized proxy metadata", err)
			}
			var row AccountCredential
			if err = s.DB.First(&row, "account_id = ?", target.ID).Error; err != nil {
				t.Fatal(err)
			}
			if row.State != "ready" || strings.Contains(row.AuthJSONCiphertext, "saved-access") || row.ConfirmedAt == nil {
				t.Fatal("credential not encrypted/confirmed")
			}
			raw, err := s.ReplaceAccountProxy(ctx, target, 1, "http://next.example:8080")
			if err != nil || !strings.Contains(string(raw), "9007199254740993") {
				t.Fatal("legacy initialization lost an extension field's precision", err)
			}
		})
	}
}

func TestAccountCredentialSubmissionInitializesAndProxyPreservesOtherFields(t *testing.T) {
	s, _, target := authAuditFixture(t)
	ctx := context.Background()
	var submission Submission
	if err := s.DB.First(&submission, *target.CurrentSubmissionID).Error; err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"auth_json": credentialTestAuth()}
	if err := s.DB.Transaction(func(tx *gorm.DB) error { return s.saveSubmittedCredentialTx(tx, submission, input, time.Now().UTC()) }); err != nil {
		t.Fatal(err)
	}
	raw, err := s.ReplaceAccountProxy(ctx, target, 1, "socks5://next:password@next.example:1080")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(credentialTestAuth())
	var before, after map[string]json.RawMessage
	json.Unmarshal(want, &before)
	json.Unmarshal(raw, &after)
	delete(before, "proxy_url")
	delete(after, "proxy_url")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("proxy update changed credential extension fields or tokens")
	}
	if !strings.Contains(string(raw), "9007199254740993") {
		t.Fatal("large number lost precision")
	}
	if _, err = s.ReplaceAccountProxy(ctx, target, 2, "http://next.example"); !errors.Is(err, ErrCurrentCredentialChanged) {
		t.Fatal("stale revision accepted", err)
	}
	// A different owner/lifetime cannot decrypt or reuse the saved document.
	target.UserID++
	if _, err = s.ReplaceAccountProxy(ctx, target, 1, "http://next.example"); !errors.Is(err, ErrAccountIdentityChanged) {
		t.Fatal("foreign identity accepted", err)
	}
}

func TestAccountCredentialSuccessfulSubmissionsSaveInitialAuth(t *testing.T) {
	for _, source := range []string{"web", "api"} {
		for _, accepted := range []bool{true, false} {
			t.Run(source+"/"+map[bool]string{true: "accepted", false: "rejected"}[accepted], func(t *testing.T) {
				service, user, tokenID, params := apiExecutionFixture(t)
				service.Transport = storageTransport(func(*http.Request) (*http.Response, error) {
					if accepted {
						return apiRemoteResponse(201, `{"id":13152}`), nil
					}
					return apiRemoteResponse(400, `{"error":"invalid parameters"}`), nil
				})
				ctx := context.Background()
				if source == "api" {
					if _, err := service.CreateAPI(ctx, user.ID, tokenID, params); err != nil {
						t.Fatal(err)
					}
				} else {
					_, version, err := service.currentVersion(ctx, 1, "submit_account")
					if err != nil {
						t.Fatal(err)
					}
					_, err = service.Create(ctx, user.ID, CreateSubmissionInput{VendorID: "1", ProtocolVersionID: idString(version.ID), ClientRequestKey: "credential-initial-submit", Parameters: params})
					if err != nil {
						t.Fatal(err)
					}
				}
				var rows []AccountCredential
				if err := service.Store.DB.Find(&rows).Error; err != nil {
					t.Fatal(err)
				}
				if !accepted {
					if len(rows) != 0 {
						t.Fatal("rejected submission created current credentials")
					}
					return
				}
				if len(rows) != 1 || rows[0].State != "ready" || rows[0].Revision != 1 || rows[0].OwnerID != user.ID {
					t.Fatal("successful submission did not create confirmed credentials")
				}
				plain, err := service.Store.decrypt(credentialEncryptionKey(rows[0]), rows[0].AuthJSONCiphertext)
				want, _ := json.Marshal(params["auth_json"])
				if err != nil || plain != string(want) {
					t.Fatal("initial credentials differ from the submitted Auth JSON", err)
				}
			})
		}
	}
}

func TestAccountCredentialUpdatesRetainConfirmedAndTrackUncertain(t *testing.T) {
	for _, status := range []string{"updated", "rejected", "failed", "unknown"} {
		t.Run(status, func(t *testing.T) {
			s, user, target := authAuditFixture(t)
			ctx := context.Background()
			seedCredentialSubmission(t, s, target)
			if err := s.InitializeAccountCredentials(ctx); err != nil {
				t.Fatal(err)
			}
			var before AccountCredential
			s.DB.First(&before, "account_id = ?", target.ID)
			raw, err := s.ReplaceAccountProxy(ctx, target, 1, "https://new-user:new-pass@new.example:443")
			if err != nil {
				t.Fatal(err)
			}
			revision := uint64(1)
			audit, err := s.BeginAuthJSONUpdateWithMethod(ctx, target, user.ID, "proxy-update", "web", nil, nil, "proxy", "", AuthJSONCredentialWrite{raw, &revision})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.AccountProxyInfo(ctx, target); !errors.Is(err, ErrCurrentCredentialUnknown) {
				t.Fatal("pending candidate was treated as current", err)
			}
			if _, err = s.BeginAuthJSONUpdateWithMethod(ctx, target, user.ID, "concurrent", "web", nil, nil, "manual", "", AuthJSONCredentialWrite{Raw: raw}); !errors.Is(err, ErrCurrentCredentialRunning) {
				t.Fatal("pending write did not block concurrent update", err)
			}
			if _, err = s.FinishAuthJSONUpdate(ctx, audit, status, "350001", "", time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			var after AccountCredential
			s.DB.First(&after, "account_id = ?", target.ID)
			if after.Revision <= before.Revision {
				t.Fatal("revision did not advance")
			}
			if status == "updated" {
				info, err := s.AccountProxyInfo(ctx, target)
				if err != nil || !strings.Contains(info.ProxyURLMasked, "new.example") || strings.Contains(info.ProxyURLMasked, "new-pass") {
					t.Fatal("confirmed proxy metadata incorrect", err)
				}
			} else if after.AuthJSONCiphertext != before.AuthJSONCiphertext {
				t.Fatal("non-success overwrote confirmed credentials")
			}
			if status == "unknown" {
				if after.PendingCiphertext == "" || after.State != "unknown" {
					t.Fatal("uncertain candidate lost")
				}
				if _, err = s.AccountProxyInfo(ctx, target); !errors.Is(err, ErrCurrentCredentialUnknown) {
					t.Fatal("uncertain state reused original submission", err)
				}
				if err = s.InitializeAccountCredentials(ctx); err != nil {
					t.Fatal(err)
				}
				if _, err = s.AccountProxyInfo(ctx, target); !errors.Is(err, ErrCurrentCredentialUnknown) {
					t.Fatal("backfill overwrote uncertain state", err)
				}
			} else if after.PendingCiphertext != "" || after.PendingAuditID != nil {
				t.Fatal("terminal candidate not cleared")
			}
			// Historical submissions remain immutable.
			var submission Submission
			s.DB.First(&submission, *target.CurrentSubmissionID)
			plain, err := s.decrypt("submission:"+idString(user.ID)+":"+submission.ClientRequestKey, submission.SecretParametersCiphertext)
			if err != nil || strings.Contains(plain, "new.example") {
				t.Fatal("submission history changed", err)
			}
		})
	}
}

func TestAccountCredentialOldUpdateCannotOverwriteResubmissionWithSameIdentity(t *testing.T) {
	s, user, old := authAuditFixture(t)
	ctx := context.Background()
	seedCredentialSubmission(t, s, old)
	if err := s.InitializeAccountCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	raw, err := s.ReplaceAccountProxy(ctx, old, 1, "http://old-pending.example")
	if err != nil {
		t.Fatal(err)
	}
	revision := uint64(1)
	audit, err := s.BeginAuthJSONUpdateWithMethod(ctx, old, user.ID, "old-lifetime-update", "web", nil, nil, "proxy", "", AuthJSONCredentialWrite{raw, &revision})
	if err != nil {
		t.Fatal(err)
	}
	var original Submission
	if err = s.DB.First(&original, *old.CurrentSubmissionID).Error; err != nil {
		t.Fatal(err)
	}
	// Reusing the same remote ID can retain IdentityKey; the accepted submission
	// must still invalidate the old dialog, candidate and delayed completion.
	newer := syncSubmission(t, s, user.ID, old.VendorID, original.ProtocolVersionID, old.Account, old.Plan, "accepted", *original.RemoteID, time.Now().UTC())
	current := old
	current.CurrentSubmissionID = &newer.ID
	if err = s.DB.Model(&SyncedAccount{}).Where("id = ?", old.ID).Update("current_submission_id", newer.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReplaceAccountProxy(ctx, old, 1, "http://stale.example"); !errors.Is(err, ErrAccountIdentityChanged) {
		t.Fatal("old dialog reused a new accepted submission", err)
	}
	if err = s.CheckPendingCredentialUpdate(ctx, old, audit.ID); !errors.Is(err, ErrAccountIdentityChanged) {
		t.Fatal("old candidate may still dispatch", err)
	}
	input := map[string]any{"auth_json": credentialTestAuth()}
	input["auth_json"].(map[string]any)["proxy_url"] = "http://new-submission.example"
	if err = s.DB.Transaction(func(tx *gorm.DB) error { return s.saveSubmittedCredentialTx(tx, newer, input, time.Now().UTC()) }); err != nil {
		t.Fatal(err)
	}
	unchanged, err := s.FinishAuthJSONUpdate(ctx, audit, "updated", "350001", "", time.Now().UTC())
	if err != nil || unchanged {
		t.Fatal("old completion was associated with the new submission", err)
	}
	info, err := s.AccountProxyInfo(ctx, current)
	if err != nil || !strings.Contains(info.ProxyURLMasked, "new-submission.example") {
		t.Fatal("old completion replaced new credentials", err)
	}
}
