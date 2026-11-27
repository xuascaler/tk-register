package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
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

func beginHistoryUpdate(t *testing.T, s *Store, target SyncedAccount, request string, expected *string) AccountAuthJSONAudit {
	t.Helper()
	raw, _ := json.Marshal(credentialTestAuth())
	audit, err := s.BeginAuthJSONUpdateWithMethod(context.Background(), target, target.UserID, request, "web", nil, nil, "manual", "", AuthJSONCredentialWrite{Raw: raw, ExpectedRevision: expected})
	if err != nil {
		t.Fatal(err)
	}
	return audit
}

func finishHistoryUpdate(t *testing.T, s *Store, audit AccountAuthJSONAudit, status string) {
	t.Helper()
	if _, err := s.FinishAuthJSONUpdate(context.Background(), audit, status, "350001", "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

func TestHistoryCredentialLegacyFallbackIsConservative(t *testing.T) {
	for _, outcome := range []string{"none", "updated", "unknown", "pending", "rejected", "failed", "timestamp-only", "unreadable-history", "legacy-null-submission"} {
		t.Run(outcome, func(t *testing.T) {
			s, user, target := authAuditFixture(t)
			ctx := context.Background()
			seedCredentialSubmission(t, s, target)
			want := error(nil)
			switch outcome {
			case "unreadable-history":
				s.DB.Model(&Submission{}).Where("id = ?", *target.CurrentSubmissionID).Update("secret_parameters_ciphertext", "invalid")
				want = ErrCurrentCredentialMissing
			case "timestamp-only":
				s.DB.Model(&SyncedAccount{}).Where("id = ?", target.ID).Update("auth_json_updated_at", time.Now().UTC())
				want = ErrCurrentCredentialMissing
			case "none":
			default:
				audit, err := s.BeginAuthJSONUpdate(ctx, target, user.ID, "legacy")
				if err != nil {
					t.Fatal(err)
				}
				status := outcome
				if outcome == "legacy-null-submission" {
					status = "updated"
					s.DB.Model(&audit).Update("submission_id", nil)
				}
				if status != "pending" {
					finishHistoryUpdate(t, s, audit, status)
				}
				if status == "updated" {
					want = ErrCurrentCredentialMissing
				}
				if status == "unknown" || status == "pending" {
					want = ErrCurrentCredentialUnknown
				}
			}
			for range 2 {
				info, err := s.AccountProxyInfo(ctx, target)
				if !errors.Is(err, want) {
					t.Fatalf("got %v, want %v", err, want)
				}
				if want == nil && (len(info.Revision) != 64 || strings.Contains(info.ProxyURLMasked, "private")) {
					t.Fatal("invalid proxy metadata")
				}
			}
			if s.DB.Migrator().HasTable("account_credentials") {
				t.Fatal("obsolete credential table was created")
			}
		})
	}
}

func TestHistoryProxyPreservesFieldsAndRechecksVersion(t *testing.T) {
	s, _, target := authAuditFixture(t)
	ctx := context.Background()
	seedCredentialSubmission(t, s, target)
	info, err := s.AccountProxyInfo(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := s.ReplaceAccountProxy(ctx, target, info.Revision, "socks5://next:password@next.example:1080")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(credentialTestAuth())
	var before, after map[string]json.RawMessage
	json.Unmarshal(want, &before)
	json.Unmarshal(raw, &after)
	delete(before, "proxy_url")
	delete(after, "proxy_url")
	if !reflect.DeepEqual(before, after) || !strings.Contains(string(raw), "9007199254740993") {
		t.Fatal("proxy update changed other fields")
	}
	audit := beginHistoryUpdate(t, s, target, "concurrent-auth-update", nil)
	finishHistoryUpdate(t, s, audit, "updated")
	// This rechecks after resolving the old proxy body, inside the account lock.
	_, err = s.BeginAuthJSONUpdateWithMethod(ctx, target, target.UserID, "stale-proxy", "web", nil, nil, "proxy", "", AuthJSONCredentialWrite{Raw: raw, ExpectedRevision: &info.Revision})
	if !errors.Is(err, ErrCurrentCredentialChanged) {
		t.Fatal("stale prepared body accepted", err)
	}
	if _, err := s.ReplaceAccountProxy(ctx, target, info.Revision, "http://next.example"); !errors.Is(err, ErrCurrentCredentialChanged) {
		t.Fatal("old dialog accepted", err)
	}
	var count int64
	s.DB.Model(&AccountAuthJSONAudit{}).Count(&count)
	if count != 1 {
		t.Fatal("rejected version inserted an operation")
	}
	target.UserID++
	if _, err := s.AccountProxyInfo(ctx, target); !errors.Is(err, ErrAccountIdentityChanged) {
		t.Fatal("foreign owner accepted", err)
	}
}

func TestHistoryPayloadEncryptedImmutableAndNotSerialized(t *testing.T) {
	s, _, target := authAuditFixture(t)
	ctx := context.Background()
	audit := beginHistoryUpdate(t, s, target, "complete-credentials", nil)
	if err := s.CheckPendingCredentialUpdate(ctx, target, audit.ID); err != nil {
		t.Fatal(err)
	}
	if audit.AuthJSONCiphertext == nil || strings.Contains(*audit.AuthJSONCiphertext, "saved-access") {
		t.Fatal("unencrypted payload")
	}
	encrypted := *audit.AuthJSONCiphertext
	encoded, _ := json.Marshal(audit)
	if strings.Contains(string(encoded), encrypted) || strings.Contains(string(encoded), "auth_json_ciphertext") || strings.Contains(string(encoded), "AuthJSONCiphertext") {
		t.Fatal("audit serialized credentials")
	}
	if _, err := s.AccountProxyInfo(ctx, target); !errors.Is(err, ErrCurrentCredentialUnknown) {
		t.Fatal("pending payload reused", err)
	}
	raw, _ := json.Marshal(credentialTestAuth())
	_, err := s.BeginAuthJSONUpdateWithMethod(ctx, target, target.UserID, "busy", "web", nil, nil, "manual", "", AuthJSONCredentialWrite{Raw: raw})
	if !errors.Is(err, ErrCurrentCredentialRunning) {
		t.Fatal("parallel update allowed", err)
	}
	finishHistoryUpdate(t, s, audit, "updated")
	info, err := s.AccountProxyInfo(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	finishHistoryUpdate(t, s, audit, "failed") // Duplicate completion must be inert.
	var saved AccountAuthJSONAudit
	if err := s.DB.First(&saved, audit.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Status != "updated" || saved.AuthJSONCiphertext == nil || *saved.AuthJSONCiphertext != encrypted {
		t.Fatal("history was overwritten")
	}
	current, err := s.currentCredential(ctx, target)
	if err != nil || string(current.raw) != string(raw) || current.revision != info.Revision {
		t.Fatal("history payload changed", err)
	}
	// A transplanted ciphertext cannot be decrypted under another operation ID/AAD.
	s.DB.Model(&saved).Update("request_id", "tampered-history-identity")
	if _, err := s.AccountProxyInfo(ctx, target); !errors.Is(err, ErrCurrentCredentialMissing) {
		t.Fatal("transplanted payload accepted", err)
	}
}

func TestHistoryUnknownBarrierSurvivesLaterFailures(t *testing.T) {
	for _, first := range []string{"unknown", "pending"} {
		t.Run(first, func(t *testing.T) {
			s, _, target := authAuditFixture(t)
			ctx := context.Background()
			seedCredentialSubmission(t, s, target)
			audit := beginHistoryUpdate(t, s, target, "uncertain", nil)
			if first == "unknown" {
				finishHistoryUpdate(t, s, audit, first)
			} else {
				s.DB.Model(&audit).Update("created_at", time.Now().UTC().Add(-2*time.Minute))
			}
			for _, status := range []string{"failed", "rejected"} {
				later := beginHistoryUpdate(t, s, target, status, nil)
				finishHistoryUpdate(t, s, later, status)
				if _, err := s.AccountProxyInfo(ctx, target); !errors.Is(err, ErrCurrentCredentialUnknown) {
					t.Fatal("failure erased earlier uncertain outcome", err)
				}
			}
			recovered := beginHistoryUpdate(t, s, target, "explicit-recovery", nil)
			finishHistoryUpdate(t, s, recovered, "updated")
			info, err := s.AccountProxyInfo(ctx, target)
			if err != nil {
				t.Fatal(err)
			}
			rejected := beginHistoryUpdate(t, s, target, "later-rejection", nil)
			finishHistoryUpdate(t, s, rejected, "rejected")
			after, err := s.AccountProxyInfo(ctx, target)
			if err != nil || after.Revision != info.Revision {
				t.Fatal("confirmed history lost after rejection", err)
			}
		})
	}
}

func TestHistoryNeverReusesAnotherSubmissionLifetime(t *testing.T) {
	s, user, target := authAuditFixture(t)
	ctx := context.Background()
	seedCredentialSubmission(t, s, target)
	audit := beginHistoryUpdate(t, s, target, "old-lifetime", nil)
	var original Submission
	s.DB.First(&original, *target.CurrentSubmissionID)
	newer := syncSubmission(t, s, user.ID, target.VendorID, original.ProtocolVersionID, target.Account, target.Plan, "accepted", "301", time.Now().UTC())
	if _, err := s.CollectionTargets(ctx, user.ID, 0); err != nil {
		t.Fatal(err)
	}
	var current SyncedAccount
	s.DB.First(&current, target.ID)
	if current.CurrentSubmissionID == nil || *current.CurrentSubmissionID != newer.ID {
		t.Fatal("new accepted lifetime missing")
	}
	seedCredentialSubmission(t, s, current)
	if err := s.CheckPendingCredentialUpdate(ctx, target, audit.ID); !errors.Is(err, ErrAccountIdentityChanged) {
		t.Fatal("old operation remained eligible", err)
	}
	if changed, err := s.FinishAuthJSONUpdate(ctx, audit, "updated", "350001", "", time.Now().UTC()); err != nil || changed {
		t.Fatal("old completion changed current identity", err)
	}
	info, err := s.AccountProxyInfo(ctx, current)
	if err != nil || info.Revision != historyCredentialRevision(current, "submission", newer.ID) {
		t.Fatal("old history replaced resubmission", err)
	}
}

func TestSubmissionHistorySuppliesCredentialsWithoutExtraWrites(t *testing.T) {
	for _, source := range []string{"web", "api"} {
		for _, accepted := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/accepted=%t", source, accepted), func(t *testing.T) {
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
					if _, err = service.Create(ctx, user.ID, CreateSubmissionInput{VendorID: "1", ProtocolVersionID: idString(version.ID), ClientRequestKey: "credential-initial-submit", Parameters: params}); err != nil {
						t.Fatal(err)
					}
				}
				if service.Store.DB.Migrator().HasTable("account_credentials") {
					t.Fatal("obsolete table recreated")
				}
				var count int64
				service.Store.DB.Model(&AccountAuthJSONAudit{}).Count(&count)
				if count != 0 {
					t.Fatal("Submit created an extra credential update history")
				}
				var target SyncedAccount
				if err := service.Store.DB.First(&target).Error; err != nil {
					t.Fatal(err)
				}
				snapshot, err := service.Store.currentCredential(ctx, target)
				if !accepted {
					if err == nil {
						t.Fatal("failed Submit used as credentials")
					}
					return
				}
				want, _ := json.Marshal(params["auth_json"])
				if err != nil || string(snapshot.raw) != string(want) {
					t.Fatal("accepted submission unavailable", err)
				}
				if _, err := service.Store.AccountProxyInfo(ctx, target); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestHistoryMigrationDoesNotReadOrMigrateObsoleteCredentials(t *testing.T) {
	s, _, target := authAuditFixture(t)
	legacy, err := s.BeginAuthJSONUpdate(context.Background(), target, target.UserID, "old-metadata-only")
	if err != nil {
		t.Fatal(err)
	}
	finishHistoryUpdate(t, s, legacy, "updated")
	if err := s.DB.Exec("ALTER TABLE account_auth_json_audits DROP COLUMN auth_json_ciphertext").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Migrator().DropIndex(&AccountAuthJSONAudit{}, "idx_auth_history_lifetime"); err != nil {
		t.Fatal(err)
	}
	// A deliberately incompatible obsolete table catches any remaining reads.
	if err := s.DB.Exec("CREATE TABLE account_credentials (legacy_value TEXT)").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Exec("INSERT INTO account_credentials VALUES ('unused-legacy-payload')").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.migrateAccountSync(); err != nil {
		t.Fatal(err)
	}
	var count int64
	s.DB.Model(&AccountAuthJSONAudit{}).Count(&count)
	if count != 1 {
		t.Fatal("migration backfilled history")
	}
	var migrated AccountAuthJSONAudit
	if err := s.DB.First(&migrated, legacy.ID).Error; err != nil || migrated.AuthJSONCiphertext != nil || migrated.Status != "updated" {
		t.Fatal("legacy history changed", err)
	}
	if !s.DB.Migrator().HasIndex(&AccountAuthJSONAudit{}, "idx_auth_history_lifetime") {
		t.Fatal("history lookup index missing")
	}
	if _, err := s.AccountProxyInfo(context.Background(), target); !errors.Is(err, ErrCurrentCredentialMissing) {
		t.Fatal("obsolete data reused", err)
	}
	audit := beginHistoryUpdate(t, s, target, "fresh-history", nil)
	finishHistoryUpdate(t, s, audit, "updated")
	if _, err := s.AccountProxyInfo(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	var value string
	if err := s.DB.Table("account_credentials").Select("legacy_value").Scan(&value).Error; err != nil || value != "unused-legacy-payload" {
		t.Fatal("obsolete table mutated", err)
	}
}

func TestRestoredSubmissionHistoryRemainsUsable(t *testing.T) {
	s, _, target := authAuditFixture(t)
	seedCredentialSubmission(t, s, target)
	var sub Submission
	s.DB.First(&sub, *target.CurrentSubmissionID)
	at := time.Now().UTC().Truncate(time.Millisecond)
	if err := s.DB.Model(&sub).Update("submit_mode", "restore_update").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Model(&target).Update("auth_json_updated_at", at).Error; err != nil {
		t.Fatal(err)
	}
	exec := ProtocolExecution{SubmissionID: sub.ID, ProtocolVersionID: sub.ProtocolVersionID, RequestID: "restored-confirmed", Operation: "push", Status: "succeeded", RequestJSON: "{}", FinishedAt: &at}
	if err := s.DB.Create(&exec).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.AccountProxyInfo(context.Background(), target); err != nil {
		t.Fatal("restored submission incorrectly blocked", err)
	}
	// A timestamp newer than that confirmed restore still indicates missing history.
	s.DB.Model(&target).Update("auth_json_updated_at", at.Add(time.Minute))
	if _, err := s.AccountProxyInfo(context.Background(), target); !errors.Is(err, ErrCurrentCredentialMissing) {
		t.Fatal("unrecorded later update ignored", err)
	}
}
