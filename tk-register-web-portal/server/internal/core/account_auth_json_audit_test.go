package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"goapi-tk-register/server/common"
)

func authAuditFixture(t *testing.T) (*Store, User, SyncedAccount) {
	t.Helper()
	s, user, vendor, version := syncFixture(t)
	syncSubmission(t, s, user.ID, vendor.ID, version.ID, "account@example.com", "claude/cc-max-20x", "accepted", "301", time.Now().UTC().Add(-time.Hour))
	targets, err := s.CollectionTargets(context.Background(), user.ID, 0)
	if err != nil || len(targets) != 1 {
		t.Fatal("fixture account missing", err)
	}
	return s, user, targets[0]
}

func TestAuthJSONAuditCapturesSourceAndTokenRevision(t *testing.T) {
	s, user, target := authAuditFixture(t)
	ctx := context.Background()
	generated, err := s.GenerateAPIToken(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	tokenID, _ := ParseBusinessID(generated.Token.ID)
	revision := generated.Token.Revision
	apiAudit, err := s.BeginAuthJSONUpdateWithSource(ctx, target, user.ID, "api-audit-request", "api", &tokenID, &revision)
	if err != nil {
		t.Fatal(err)
	}
	webAudit, err := s.BeginAuthJSONUpdate(ctx, target, user.ID, "web-audit-request")
	if err != nil {
		t.Fatal(err)
	}
	if webAudit.Source != "web" || webAudit.APITokenID != nil || webAudit.APITokenRevision != nil {
		t.Fatal("web audit acquired API identity")
	}
	if apiAudit.Source != "api" || apiAudit.APITokenID == nil || *apiAudit.APITokenID != tokenID || apiAudit.APITokenRevision == nil || *apiAudit.APITokenRevision != 1 || apiAudit.ActorID != user.ID || apiAudit.OwnerID != user.ID {
		t.Fatal("API audit lacks identity metadata")
	}
	if _, err := s.RotateAPIToken(ctx, user.ID, tokenID, revision); err != nil {
		t.Fatal(err)
	}
	var saved AccountAuthJSONAudit
	if err := s.DB.First(&saved, apiAudit.ID).Error; err != nil {
		t.Fatal(err)
	}
	if *saved.APITokenRevision != 1 {
		t.Fatal("rotation rewrote historical token revision")
	}
	encoded, _ := common.Marshal(saved)
	if strings.Contains(string(encoded), generated.PlaintextToken) {
		t.Fatal("audit contains credential")
	}
	if _, err := s.BeginAuthJSONUpdateWithSource(ctx, target, user.ID, "stale-token-request", "api", &tokenID, &revision); !errors.Is(err, ErrInvalidAPIToken) {
		t.Fatal("audit accepted stale token revision", err)
	}
	// Existing metadata/outcome semantics remain the same for API writes.
	now := time.Now().UTC()
	unchanged, err := s.FinishAuthJSONUpdate(ctx, apiAudit, "updated", "13152", "", now)
	if err != nil || !unchanged {
		t.Fatal("could not complete API update audit", err)
	}
	if err := s.FinishAuthJSONRefresh(ctx, apiAudit.ID, false); err != nil {
		t.Fatal(err)
	}
	saved = AccountAuthJSONAudit{}
	if err := s.DB.First(&saved, apiAudit.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Status != "updated" || saved.RefreshStatus != "failed" || saved.FinishedAt == nil || saved.Source != "api" || *saved.APITokenRevision != 1 {
		t.Fatal("refresh failure changed update result or actor metadata")
	}
}

func TestAuthJSONAuditRejectsInvalidTokenReferencesAndSource(t *testing.T) {
	s, owner, target := authAuditFixture(t)
	ctx := context.Background()
	other := tokenUser(t, s, "audit-other@example.com")
	generated, err := s.GenerateAPIToken(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	tokenID, _ := ParseBusinessID(generated.Token.ID)
	revision := generated.Token.Revision
	foreign, err := s.GenerateAPIToken(ctx, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	foreignID, _ := ParseBusinessID(foreign.Token.ID)
	cases := []struct {
		name, source string
		actor        uint64
		id, revision *uint64
	}{
		{"invalid source", "cli", owner.ID, nil, nil},
		{"web token", "web", owner.ID, &tokenID, &revision},
		{"API missing token", "api", owner.ID, nil, nil},
		{"API wrong actor", "api", other.ID, &foreignID, &revision},
		{"API foreign token", "api", owner.ID, &foreignID, &revision},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.BeginAuthJSONUpdateWithSource(ctx, target, tc.actor, tc.name, tc.source, tc.id, tc.revision); err == nil {
				t.Fatal("invalid audit identity accepted")
			}
		})
	}
	if err := s.DB.Model(&UserAPIToken{}).Where("id = ?", tokenID).Update("scope", legacyReadAPITokenScope).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginAuthJSONUpdateWithSource(ctx, target, owner.ID, "missing-scope", "api", &tokenID, &revision); !errors.Is(err, ErrAPITokenScope) {
		t.Fatal("read-only token permitted update audit", err)
	}
	if err := s.DB.Model(&UserAPIToken{}).Where("id = ?", tokenID).Updates(map[string]any{"scope": DefaultAPITokenScope, "revoked_at": time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginAuthJSONUpdateWithSource(ctx, target, owner.ID, "revoked-token", "api", &tokenID, &revision); !errors.Is(err, ErrInvalidAPIToken) {
		t.Fatal("revoked token permitted update audit", err)
	}
	var count int64
	if err := s.DB.Model(&AccountAuthJSONAudit{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("rejected token metadata was audited as executable update", count, err)
	}
}

func TestAuthJSONOwnershipChecksUnmaterializedAcceptedSubmissions(t *testing.T) {
	s, owner, target := authAuditFixture(t)
	ctx := context.Background()
	other := tokenUser(t, s, "conflicting-owner@example.com")
	var version ProtocolVersion
	if err := s.DB.First(&version).Error; err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC()
	failed := syncSubmission(t, s, other.ID, target.VendorID, version.ID, " ACCOUNT@EXAMPLE.COM ", target.Plan, "failed", "", base)
	if err := s.CheckAuthJSONOwnership(ctx, target); err != nil {
		t.Fatal("failed submission claimed ownership", err)
	}
	if err := s.DB.Model(&failed).Updates(map[string]any{"status": "accepted", "remote_id": "302"}).Error; err != nil {
		t.Fatal(err)
	}
	// Do not materialize the other owner's synced account before checking.
	if err := s.CheckAuthJSONOwnership(ctx, target); !errors.Is(err, ErrAccountOwnershipConflict) {
		t.Fatal("normalized accepted email collision accepted", err)
	}
	if _, err := s.BeginAuthJSONUpdate(ctx, target, owner.ID, "conflicting-owner-write"); !errors.Is(err, ErrAccountOwnershipConflict) {
		t.Fatal("audit start did not recheck conflicting ownership", err)
	}
}

func TestAuthJSONOwnershipChecksSupplierIDWithinVendor(t *testing.T) {
	s, _, target := authAuditFixture(t)
	ctx := context.Background()
	other := tokenUser(t, s, "supplier-id-owner@example.com")
	var version ProtocolVersion
	if err := s.DB.First(&version).Error; err != nil {
		t.Fatal(err)
	}
	syncSubmission(t, s, other.ID, target.VendorID, version.ID, "different@example.com", target.Plan, "accepted", "302", time.Now().UTC())
	targets, err := s.CollectionTargets(ctx, other.ID, 0)
	if err != nil || len(targets) != 1 {
		t.Fatal("other account missing", err)
	}
	recordID := "13152"
	if err := s.ConfirmSupplierRecord(ctx, targets[0].ID, targets[0].IdentityKey, recordID); err != nil {
		t.Fatal(err)
	}
	target.SupplierRecordID = &recordID
	if err := s.CheckAuthJSONOwnership(ctx, target); !errors.Is(err, ErrAccountOwnershipConflict) {
		t.Fatal("shared supplier ID accepted", err)
	}
	target.VendorID++
	if err := s.CheckAuthJSONOwnership(ctx, target); err != nil {
		t.Fatal("supplier ID matched across vendors", err)
	}
}

func TestClaudeAuthJSONUpdateChecksTargetEmailForObjectsAndStrings(t *testing.T) {
	auth := map[string]any{"id_token": "", "access_token": "test-access", "refresh_token": "test-refresh", "last_refresh": "2026-10-01T00:00:00Z", "email": " Account@Example.com ", "account_uuid": "test-uuid", "organization_uuid": "test-org", "organization_name": "test", "claude_device_ids": []any{"device"}, "type": "claude", "expired": "2026-11-01T00:00:00Z", "proxy_url": "http://user:password@proxy.example:80", "verification_status": "verified"}
	raw, err := common.Marshal(auth)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []any{auth, string(raw)} {
		if _, err := ValidateClaudeAccountAuthJSON(input, "account@example.com"); err != nil {
			t.Fatal("normalized account equality rejected", err)
		}
		_, err := ValidateClaudeAccountAuthJSON(input, "another@example.com")
		var problem *VendorError
		if !errors.As(err, &problem) || problem.Code != "AUTH_JSON_EMAIL_MISMATCH" || problem.Status != 400 {
			t.Fatal("credential for another email was accepted", err)
		}
		if strings.Contains(problem.Message, "another@example.com") || strings.Contains(problem.Message, "test-access") {
			t.Fatal("validation exposed account or secret")
		}
	}
	if _, err := ValidateClaudeAccountAuthJSON(map[string]any{}, "account@example.com"); err == nil {
		t.Fatal("incomplete credentials accepted")
	}
}
