package core

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"goapi-tk-register/server/common"
	"gorm.io/gorm/clause"
)

func syncFixture(t *testing.T) (*Store, User, Vendor, ProtocolVersion) {
	t.Helper()
	s := testStore(t)
	user := User{Email: "sync-owner@example.com", PasswordHash: "fake-test-hash", Role: "member", Status: "active", EmailVerifiedAt: time.Now().UTC()}
	if err := s.CreateUser(context.Background(), &user); err != nil {
		t.Fatal(err)
	}
	var vendor Vendor
	if err := s.DB.Where("code = ?", "tk-register-0").First(&vendor).Error; err != nil {
		t.Fatal(err)
	}
	var version ProtocolVersion
	if err := s.DB.First(&version).Error; err != nil {
		t.Fatal(err)
	}
	return s, user, vendor, version
}
func syncSubmission(t *testing.T, s *Store, userID, vendorID, versionID uint64, account, plan, status, remote string, at time.Time) Submission {
	t.Helper()
	r := Submission{UserID: userID, VendorID: vendorID, ProtocolVersionID: versionID, ClientRequestKey: fmt.Sprintf("sync-test-%d-%d", userID, at.UnixNano()), RequestHash: strings.Repeat("0", 64), Source: "web", Account: account, Plan: plan, ParametersJSON: "{}", SecretParametersCiphertext: "", Status: status, CreatedAt: at}
	if remote != "" {
		r.RemoteID = &remote
	}
	if err := s.DB.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	// Direct database fixtures bypass the submission lifecycle service.
	if err := s.EnsureSyncedAccounts(context.Background()); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSyncedAccountStableIdentityAndOwnership(t *testing.T) {
	s, owner, vendor, version := syncFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)
	accepted := syncSubmission(t, s, owner.ID, vendor.ID, version.ID, "same@example.com", "claude/cc-pro", "accepted", "101", base)
	latest := syncSubmission(t, s, owner.ID, vendor.ID, version.ID, " Same@Example.com ", "claude/cc-pro", "failed", "", base.Add(time.Minute))
	syncSubmission(t, s, owner.ID, vendor.ID, version.ID, "failed@example.com", "claude/cc-pro", "unknown", "", base.Add(2*time.Minute))
	other := User{Email: "other-owner@example.com", PasswordHash: "fake", Role: "member", Status: "active", EmailVerifiedAt: base}
	if err := s.CreateUser(ctx, &other); err != nil {
		t.Fatal(err)
	}
	syncSubmission(t, s, other.ID, vendor.ID, version.ID, "same@example.com", "claude/cc-pro", "accepted", "102", base.Add(3*time.Minute))
	for i := 0; i < 2; i++ {
		if err := s.EnsureSyncedAccounts(ctx); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.ListSyncedAccounts(ctx, owner.ID, 1, "same@", "unknown")
	if err != nil || page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("incorrect owner page: %+v, %v", page, err)
	}
	account := page.Items[0]
	if account.CurrentSubmissionID == nil || *account.CurrentSubmissionID != idString(accepted.ID) || account.LatestSubmissionID != idString(latest.ID) || account.SubmissionStatus != "failed" || account.Snapshot != nil {
		t.Fatalf("wrong current/latest split: %+v", account)
	}
	id, _ := ParseBusinessID(account.ID)
	if _, err = s.SyncedAccountForUser(ctx, other.ID, id); !errors.Is(err, ErrNotFound) {
		t.Fatal("another owner can read account", err)
	}
	if _, err = s.SyncedAccountForUser(ctx, 0, id); !errors.Is(err, ErrNotFound) {
		t.Fatal("zero owner accepted")
	}
	targets, err := s.CollectionTargets(ctx, owner.ID, 0)
	if err != nil || len(targets) != 1 || targets[0].ID != id {
		t.Fatal("unconfirmed or foreign accounts selected", targets, err)
	}
	var n int64
	if err = s.DB.Model(&SyncedAccount{}).Count(&n).Error; err != nil || n != 3 {
		t.Fatal("duplicate grouped accounts", n, err)
	}
	page2, err := s.ListSyncedAccounts(ctx, owner.ID, 1, "same@", "")
	if err != nil || page2.Items[0].ID != account.ID {
		t.Fatal("account ID changed on read")
	}
}

func TestAccountSnapshotsRetainSuccessAndNullableValues(t *testing.T) {
	s, user, vendor, version := syncFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Hour).Add(-32 * time.Hour)
	syncSubmission(t, s, user.ID, vendor.ID, version.ID, "account@example.com", "claude/cc-pro", "accepted", "201", base.Add(-time.Hour))
	targets, err := s.CollectionTargets(ctx, user.ID, 0)
	if err != nil || len(targets) != 1 {
		t.Fatal(targets, err)
	}
	account := targets[0]
	zeroTokens, zeroCost, currency, status, platformID := int64(0), float64(0), "CNY", "active", "13001"
	values := AccountSnapshotValues{InputTokensTotal: &zeroTokens, CostTotal: &zeroCost, Currency: &currency, AccountStatus: &status, PlatformAccountID: &platformID}
	if err = s.RecordAccountSnapshot(ctx, account.ID, account.IdentityKey, values, base, "Asia/Shanghai"); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordAccountSnapshot(ctx, account.ID, account.IdentityKey, values, base, "Asia/Shanghai"); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordAccountFailure(ctx, account.ID, account.IdentityKey, "Supplier login failed.", base.Add(8*time.Hour)); err != nil {
		t.Fatal(err)
	}
	dto, err := s.SyncedAccountForUser(ctx, user.ID, account.ID)
	if err != nil || dto.Snapshot == nil || dto.Snapshot.CostTotal == nil || *dto.Snapshot.CostTotal != 0 || dto.LastError == "" || !dto.LastSuccessAt.Equal(base) {
		t.Fatal("failure discarded good data", dto, err)
	}
	if dto.Snapshot.OutputTokensTotal != nil || dto.Snapshot.Usage5hPercent != nil {
		t.Fatal("absent data became zero")
	}
	if dto.PlatformAccountID == nil || *dto.PlatformAccountID != platformID {
		t.Fatal("website platform ID missing", dto)
	}
	raw, err := common.Marshal(dto.Snapshot)
	if err != nil || !strings.Contains(string(raw), `"output_tokens_total":null`) || !strings.Contains(string(raw), `"cost_total":0`) {
		t.Fatal("nullable API contract lost", err)
	}
	loc, _ := time.LoadLocation("Asia/Shanghai")
	if dto.Snapshot.SnapshotDate != base.In(loc).Format("2006-01-02") {
		t.Fatal("wrong timezone snapshot date")
	}
	snaps, err := s.AccountSnapshots(ctx, account.ID, 7)
	if err != nil || len(snaps) != 1 {
		t.Fatal("duplicate sample was inserted", len(snaps), err)
	}
	// Preserve all observations within a date, not just one row per day.
	for _, offset := range []time.Duration{16 * time.Hour, 24 * time.Hour} {
		if err = s.RecordAccountSnapshot(ctx, account.ID, account.IdentityKey, values, base.Add(offset), "Asia/Shanghai"); err != nil {
			t.Fatal(err)
		}
	}
	snaps, err = s.AccountSnapshots(ctx, account.ID, 7)
	if err != nil || len(snaps) != 3 || !snaps[0].CollectedAt.Before(snaps[2].CollectedAt) {
		t.Fatal("history was collapsed or unsorted", snaps, err)
	}
	dto, _ = s.SyncedAccountForUser(ctx, user.ID, account.ID)
	if dto.LastError != "" || !dto.LastSuccessAt.Equal(base.Add(24*time.Hour)) {
		t.Fatal("success did not recover freshness", dto)
	}
	if _, err = s.AccountSnapshots(ctx, account.ID, 365); !IsValidationError(err) {
		t.Fatal("unbounded history accepted")
	}
}

func TestAccountRemoteLifetimeChangeDoesNotInheritSnapshot(t *testing.T) {
	s, user, vendor, version := syncFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)
	syncSubmission(t, s, user.ID, vendor.ID, version.ID, "account@example.com", "claude/cc-pro", "accepted", "301", base)
	targets, err := s.CollectionTargets(ctx, user.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	old := targets[0]
	cost := float64(100)
	if err = s.RecordAccountSnapshot(ctx, old.ID, old.IdentityKey, AccountSnapshotValues{CostTotal: &cost}, base.Add(time.Minute), "UTC"); err != nil {
		t.Fatal(err)
	}
	syncSubmission(t, s, user.ID, vendor.ID, version.ID, "account@example.com", "claude/cc-max-20x", "accepted", "302", base.Add(2*time.Minute))
	targets, err = s.CollectionTargets(ctx, user.ID, 0)
	if err != nil || len(targets) != 1 || targets[0].ID != old.ID || targets[0].IdentityKey == old.IdentityKey {
		t.Fatal("wrong stable/lifetime identity", targets, err)
	}
	dto, err := s.SyncedAccountForUser(ctx, user.ID, old.ID)
	if err != nil || dto.Snapshot != nil || dto.LastSuccessAt != nil || dto.LastAttemptAt != nil {
		t.Fatal("previous lifetime exposed", dto, err)
	}
	snaps, err := s.AccountSnapshots(ctx, old.ID, 30)
	if err != nil || len(snaps) != 0 {
		t.Fatal("previous lifetime joined in trend", snaps, err)
	}
	if err = s.RecordAccountSnapshot(ctx, old.ID, old.IdentityKey, AccountSnapshotValues{CostTotal: &cost}, time.Now(), "UTC"); !errors.Is(err, ErrAccountIdentityChanged) {
		t.Fatal("in-flight old response accepted", err)
	}
	if err = s.RecordAccountFailure(ctx, old.ID, old.IdentityKey, "old failure", time.Now()); !errors.Is(err, ErrAccountIdentityChanged) {
		t.Fatal("old failure crossed lifetime", err)
	}
	var count int64
	_ = s.DB.Model(&AccountSnapshot{}).Count(&count).Error
	if count != 1 {
		t.Fatal("previous audit snapshot destroyed", count)
	}
	// Restoring an earlier plan/remote ID must not reconnect its old trend.
	syncSubmission(t, s, user.ID, vendor.ID, version.ID, "account@example.com", "claude/cc-pro", "accepted", "301", base.Add(4*time.Minute))
	targets, err = s.CollectionTargets(ctx, user.ID, 0)
	if err != nil || len(targets) != 1 || targets[0].IdentityKey == old.IdentityKey {
		t.Fatal("old lifetime was revived", targets, err)
	}
	dto, err = s.SyncedAccountForUser(ctx, user.ID, old.ID)
	if err != nil || dto.Snapshot != nil {
		t.Fatal("old trend reappeared", dto, err)
	}
	currentIdentity := targets[0].IdentityKey
	// A duplicate accepted submission for the same remote identity keeps the lifetime.
	syncSubmission(t, s, user.ID, vendor.ID, version.ID, "account@example.com", "claude/cc-pro", "accepted", "301", base.Add(5*time.Minute))
	targets, err = s.CollectionTargets(ctx, user.ID, 0)
	if err != nil || targets[0].IdentityKey != currentIdentity {
		t.Fatal("same identity unnecessarily reset", targets, err)
	}
}

func TestAccountSyncSettingsRequireCredentialsAndKeepSecretsPrivate(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	values, err := s.Settings(ctx)
	if err != nil || values.Bool("account_sync_enabled") || values.Int("account_sync_interval_seconds") != 300 {
		t.Fatal("unsafe sync defaults", err)
	}
	if err = s.PatchSettings(ctx, map[string]string{"account_sync_enabled": "true"}, 1); !IsValidationError(err) {
		t.Fatal("enabled without credentials", err)
	}
	if err = s.PatchSettings(ctx, map[string]string{"account_sync_website_username": "supplier-test"}, 1); !IsValidationError(err) {
		t.Fatal("username changed without password", err)
	}
	password := "fixture-secret-website-password"
	changes := map[string]string{"account_sync_enabled": "true", "account_sync_website_username": "supplier-test", "account_sync_website_password": password, "account_sync_interval_seconds": "28800"}
	if err = s.PatchSettings(ctx, changes, 1); err != nil {
		t.Fatal(err)
	}
	var saved Setting
	if err = s.DB.Where(clause.Eq{Column: "key", Value: "account_sync_website_password"}).First(&saved).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(saved.Value, "enc:v1:") || strings.Contains(saved.Value, password) {
		t.Fatal("website password not encrypted")
	}
	descriptors, err := s.Descriptors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range descriptors {
		if d.Key == "account_sync_website_password" && (d.Value != "" || !d.Configured || !d.Secret) {
			t.Fatal("website password disclosed")
		}
	}
	if err = s.PatchSettings(ctx, map[string]string{"account_sync_website_password": "", "account_sync_interval_seconds": "43200"}, 1); err != nil {
		t.Fatal(err)
	}
	values, _ = s.Settings(ctx)
	if values.String("account_sync_website_password") != password || values.Int("account_sync_interval_seconds") != 43200 {
		t.Fatal("blank replacement discarded password")
	}
	if err = s.PatchSettings(ctx, map[string]string{"account_sync_website_username": "new-user", "account_sync_interval_seconds": "86400"}, 1); !IsValidationError(err) {
		t.Fatal("changed username reused old password")
	}
	values, _ = s.Settings(ctx)
	if values.Int("account_sync_interval_seconds") != 43200 {
		t.Fatal("invalid update partially applied")
	}
	for _, changes := range []map[string]string{{"account_sync_interval_seconds": "0"}, {"account_sync_interval_seconds": "604801"}, {"account_sync_timezone": "Invalid/Zone"}} {
		if err = s.PatchSettings(ctx, changes, 1); !IsValidationError(err) {
			t.Fatal("invalid sync setting accepted", changes, err)
		}
	}
}

func TestAccountSnapshotRejectsInvalidNumbersAndLateSuccessKeepsNewerError(t *testing.T) {
	s, user, vendor, version := syncFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)
	syncSubmission(t, s, user.ID, vendor.ID, version.ID, "latency@example.com", "claude/cc-pro", "accepted", "401", base)
	targets, err := s.CollectionTargets(ctx, user.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	a := targets[0]
	bad := math.NaN()
	if err = s.RecordAccountSnapshot(ctx, a.ID, a.IdentityKey, AccountSnapshotValues{CostTotal: &bad}, base, "UTC"); !IsValidationError(err) {
		t.Fatal("NaN accepted", err)
	}
	if err = s.RecordAccountFailure(ctx, a.ID, a.IdentityKey, "Newer login failed.", base.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	cost := float64(20)
	if err = s.RecordAccountSnapshot(ctx, a.ID, a.IdentityKey, AccountSnapshotValues{CostTotal: &cost}, base.Add(time.Minute), "UTC"); err != nil {
		t.Fatal(err)
	}
	dto, err := s.SyncedAccountForUser(ctx, user.ID, a.ID)
	if err != nil || dto.LastError == "" || !dto.LastAttemptAt.Equal(base.Add(3*time.Minute)) || dto.LastSuccessAt == nil || !dto.LastSuccessAt.Equal(base.Add(time.Minute)) {
		t.Fatal("late success changed newer failure", dto, err)
	}
}

func TestAccountCollectionTargetsOnlyEnabledSupportedVendor(t *testing.T) {
	s, user, vendor, version := syncFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)
	syncSubmission(t, s, user.ID, vendor.ID, version.ID, "eligible@example.com", "claude/cc-pro", "accepted", "501", base)
	otherVendor := Vendor{Code: "another-vendor", Name: "Other vendor", BaseURL: "https://example.invalid", Enabled: true, Timezone: "UTC"}
	if err := s.DB.Create(&otherVendor).Error; err != nil {
		t.Fatal(err)
	}
	syncSubmission(t, s, user.ID, otherVendor.ID, version.ID, "eligible@example.com", "claude/cc-pro", "accepted", "502", base.Add(time.Minute))
	targets, err := s.CollectionTargets(ctx, user.ID, 0)
	if err != nil || len(targets) != 1 || targets[0].VendorID != vendor.ID {
		t.Fatal("unsupported vendor selected", targets, err)
	}
	if err = s.DB.Model(&Vendor{}).Where("id = ?", vendor.ID).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	targets, err = s.CollectionTargets(ctx, user.ID, 0)
	if err != nil || len(targets) != 0 {
		t.Fatal("disabled vendor selected", targets, err)
	}
}

func TestAdminSyncedAccountsUseOwnerSnapshotsAndSearchOwnerEmail(t *testing.T) {
	s, owner, vendor, version := syncFixture(t)
	ctx := context.Background()
	at := time.Now().UTC().Add(-time.Hour)
	other := User{Email: "other-owner@example.com", PasswordHash: "private-password-hash", Role: "member", Status: "active", EmailVerifiedAt: at}
	if err := s.CreateUser(ctx, &other); err != nil {
		t.Fatal(err)
	}
	syncSubmission(t, s, owner.ID, vendor.ID, version.ID, "shared@example.com", "claude/cc-pro", "accepted", "601", at)
	syncSubmission(t, s, other.ID, vendor.ID, version.ID, "shared@example.com", "claude/cc-pro", "accepted", "602", at.Add(time.Minute))
	ownerPage, err := s.ListSyncedAccounts(ctx, owner.ID, 1, "", "")
	if err != nil || len(ownerPage.Items) != 1 {
		t.Fatal(ownerPage, err)
	}
	if ownerPage.Items[0].UserID != "" || ownerPage.Items[0].UserEmail != "" {
		t.Fatal("owner view unexpectedly includes administrator fields")
	}
	id, _ := ParseBusinessID(ownerPage.Items[0].ID)
	var raw SyncedAccount
	if err = s.DB.First(&raw, id).Error; err != nil {
		t.Fatal(err)
	}
	active, currency := "active", "CNY"
	zero := float64(0)
	if err = s.RecordAccountSnapshot(ctx, id, raw.IdentityKey, AccountSnapshotValues{AccountStatus: &active, CostTotal: &zero, Currency: &currency}, at.Add(2*time.Minute), "UTC"); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListAdminSyncedAccounts(ctx, 1, "", "")
	if err != nil || page.Total != 2 || len(page.Items) != 2 || page.Items[0].UserEmail != other.Email {
		t.Fatal("admin list lost owner grouping or ordering", page, err)
	}
	page, err = s.ListAdminSyncedAccounts(ctx, 1, "OTHER-OWNER@", "unknown")
	if err != nil || page.Total != 1 || page.Items[0].UserID != idString(other.ID) {
		t.Fatal("owner email search failed", page, err)
	}
	page, err = s.ListAdminSyncedAccounts(ctx, 1, "shared@", "active")
	if err != nil || page.Total != 1 || page.Items[0].ID != idString(id) || page.Items[0].Snapshot == nil || page.Items[0].Snapshot.CostTotal == nil || *page.Items[0].Snapshot.CostTotal != 0 || page.Items[0].Snapshot.Usage5hPercent != nil {
		t.Fatal("admin view did not use the same nullable observation", page, err)
	}
	detail, err := s.SyncedAccountForAdmin(ctx, id)
	if err != nil || detail.UserEmail != owner.Email || detail.UserID != idString(owner.ID) || detail.Snapshot == nil || detail.Snapshot.ID != page.Items[0].Snapshot.ID {
		t.Fatal("admin detail diverges from list", detail, err)
	}
	serialized, err := common.Marshal(detail)
	if err != nil || strings.Contains(string(serialized), "password") || strings.Contains(string(serialized), "auth_version") {
		t.Fatal("administrator DTO includes private user details")
	}
	page, err = s.ListAdminSyncedAccounts(ctx, 2, "", "")
	if err != nil || page.Total != 2 || len(page.Items) != 0 {
		t.Fatal("admin pagination is incorrect", page, err)
	}
	page, err = s.ListAdminSyncedAccounts(ctx, 1, "%", "")
	if err != nil || page.Total != 0 {
		t.Fatal("search wildcard escaped incorrectly", page, err)
	}
	if _, err = s.ListAdminSyncedAccounts(ctx, 1, "", "accepted"); !IsValidationError(err) {
		t.Fatal("submission status was accepted as account status")
	}
	if _, err = s.SyncedAccountForAdmin(ctx, 0); !errors.Is(err, ErrNotFound) {
		t.Fatal("zero account ID accepted")
	}
}

func TestLookupSyncedAccountUsesExactOwnerVendorIdentityAndSavedSnapshot(t *testing.T) {
	s, owner, vendor, version := syncFixture(t)
	ctx := context.Background()
	at := time.Now().UTC().Add(-time.Hour)
	other := tokenUser(t, s, "lookup-other@example.com")
	otherVendor := Vendor{Code: "another-vendor", Name: "Other vendor", BaseURL: "https://example.invalid", Enabled: true, Timezone: "UTC"}
	if err := s.DB.Create(&otherVendor).Error; err != nil {
		t.Fatal(err)
	}
	accepted := syncSubmission(t, s, owner.ID, vendor.ID, version.ID, "same@example.com", "claude/cc-pro", "accepted", "701", at)
	latest := syncSubmission(t, s, owner.ID, vendor.ID, version.ID, "Same@Example.com", "claude/cc-pro", "failed", "", at.Add(time.Minute))
	syncSubmission(t, s, other.ID, vendor.ID, version.ID, "same@example.com", "claude/cc-pro", "accepted", "702", at.Add(2*time.Minute))
	syncSubmission(t, s, owner.ID, otherVendor.ID, version.ID, "same@example.com", "claude/cc-pro", "accepted", "703", at.Add(3*time.Minute))
	syncSubmission(t, s, other.ID, vendor.ID, version.ID, "foreign-only@example.com", "claude/cc-pro", "accepted", "704", at.Add(4*time.Minute))
	syncSubmission(t, s, owner.ID, vendor.ID, version.ID, "codex@example.com", "codex/pro", "accepted", "705", at.Add(5*time.Minute))
	syncSubmission(t, s, owner.ID, vendor.ID, version.ID, "unconfirmed@example.com", "claude/cc-pro", "unknown", "", at.Add(6*time.Minute))
	dto, err := s.LookupSyncedAccountForUser(ctx, owner.ID, vendor.Code, " SAME@example.com ")
	if err != nil || dto.CurrentSubmissionID == nil || *dto.CurrentSubmissionID != idString(accepted.ID) || dto.LatestSubmissionID != idString(latest.ID) || dto.RemoteID == nil || *dto.RemoteID != "701" || dto.Snapshot != nil {
		t.Fatal("lookup picked wrong grouped account or manufactured observation", dto, err)
	}
	id, _ := ParseBusinessID(dto.ID)
	var row SyncedAccount
	if err = s.DB.First(&row, id).Error; err != nil {
		t.Fatal(err)
	}
	zero, zeroTokens, active, currency := float64(0), int64(0), "active", "CNY"
	values := AccountSnapshotValues{CostTotal: &zero, InputTokensTotal: &zeroTokens, AccountStatus: &active, Currency: &currency}
	collected := at.Add(7 * time.Minute)
	if err = s.RecordAccountSnapshot(ctx, row.ID, row.IdentityKey, values, collected, "Asia/Shanghai"); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordAccountFailure(ctx, row.ID, row.IdentityKey, "Supplier request timed out.", collected.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	dto, err = s.LookupSyncedAccountForUser(ctx, owner.ID, vendor.Code, "same@example.com")
	if err != nil || dto.Snapshot == nil || dto.Snapshot.CostTotal == nil || *dto.Snapshot.CostTotal != 0 || dto.Snapshot.InputTokensTotal == nil || *dto.Snapshot.InputTokensTotal != 0 || dto.Snapshot.OutputTokensTotal != nil || dto.Snapshot.Usage5hPercent != nil || dto.LastSuccessAt == nil || !dto.LastSuccessAt.Equal(collected) || dto.LastError == "" || dto.SubmissionStatus != "failed" || *dto.Snapshot.AccountStatus != "active" {
		t.Fatal("lookup discarded nullable, zero or last-success observation", dto, err)
	}
	for _, lookup := range []struct {
		userID uint64
		vendor string
		remote string
	}{{other.ID, vendor.Code, "702"}, {owner.ID, otherVendor.Code, "703"}} {
		got, e := s.LookupSyncedAccountForUser(ctx, lookup.userID, lookup.vendor, "same@example.com")
		if e != nil || got.RemoteID == nil || *got.RemoteID != lookup.remote || got.ID == dto.ID || got.Snapshot != nil {
			t.Fatal("lookup crossed owner or vendor identity", got, e)
		}
	}
	for _, name := range []string{"foreign-only@example.com", "same", "%", "codex@example.com", "not-found@example.com"} {
		if _, e := s.LookupSyncedAccountForUser(ctx, owner.ID, vendor.Code, name); !errors.Is(e, ErrNotFound) {
			t.Fatal("lookup fell back to fuzzy, foreign or unsupported match", name, e)
		}
	}
	if _, e := s.LookupSyncedAccountForUser(ctx, 0, vendor.Code, "same@example.com"); !errors.Is(e, ErrNotFound) {
		t.Fatal("anonymous owner allowed", e)
	}
	if _, e := s.LookupSyncedAccountForUser(ctx, owner.ID, "missing-vendor", "same@example.com"); !errors.Is(e, ErrNotFound) {
		t.Fatal("unknown vendor fell back to another vendor", e)
	}
	for _, name := range []string{"", " ", "has space@example.com", "bad\x00@example.com", strings.Repeat("a", 255)} {
		if _, e := s.LookupSyncedAccountForUser(ctx, owner.ID, vendor.Code, name); !IsValidationError(e) {
			t.Fatal("invalid account lookup accepted", e)
		}
	}
	unconfirmed, err := s.LookupSyncedAccountForUser(ctx, owner.ID, vendor.Code, "unconfirmed@example.com")
	if err != nil || unconfirmed.Snapshot != nil || unconfirmed.LastSuccessAt != nil || unconfirmed.CurrentSubmissionID != nil || unconfirmed.SubmissionStatus != "unknown" {
		t.Fatal("unconfirmed account acquired invented data", unconfirmed, err)
	}
	// A later accepted remote lifetime must not expose the previous trend.
	syncSubmission(t, s, owner.ID, vendor.ID, version.ID, "same@example.com", "claude/cc-max-20x", "accepted", "706", collected.Add(2*time.Minute))
	changed, err := s.LookupSyncedAccountForUser(ctx, owner.ID, vendor.Code, "same@example.com")
	if err != nil || changed.ID != dto.ID || changed.Snapshot != nil || changed.LastSuccessAt != nil || changed.RemoteID == nil || *changed.RemoteID != "706" {
		t.Fatal("lookup mixed current remote lifetime and historical observation", changed, err)
	}
	var snapshots int64
	if err = s.DB.Model(&AccountSnapshot{}).Count(&snapshots).Error; err != nil || snapshots != 1 {
		t.Fatal("lookup created or erased snapshots", snapshots, err)
	}
}

func TestSnapshotReasonsAndFailureDetails(t *testing.T) {
	s, owner, vendor, version := syncFixture(t)
	ctx := context.Background()
	at := time.Now().UTC().Add(-time.Hour)
	syncSubmission(t, s, owner.ID, vendor.ID, version.ID, "reason@example.com", "claude/cc-pro", "accepted", "123", at)
	var account SyncedAccount
	if err := s.DB.First(&account).Error; err != nil {
		t.Fatal(err)
	}
	status, raw, reason := "banned", "ban", "Supplier disabled the account. access_token=secret-123 温州润晨"
	save := func(v AccountSnapshotValues, n int) {
		t.Helper()
		if err := s.RecordAccountSnapshot(ctx, account.ID, account.IdentityKey, v, at.Add(time.Duration(n)*time.Minute), "Asia/Shanghai"); err != nil {
			t.Fatal(err)
		}
	}
	latest := func() AccountSnapshot {
		t.Helper()
		var snap AccountSnapshot
		if err := s.DB.Where("account_id = ?", account.ID).Order("collected_at DESC").First(&snap).Error; err != nil {
			t.Fatal(err)
		}
		return snap
	}
	save(AccountSnapshotValues{AccountStatus: &status, UpstreamStatusRaw: &raw, UpstreamErrorMessage: &reason}, 1)
	snap := latest()
	if snap.UpstreamErrorMessage == nil || strings.Contains(*snap.UpstreamErrorMessage, "secret-123") || strings.Contains(*snap.UpstreamErrorMessage, "温州润晨") {
		t.Fatal("unsafe reason")
	}
	save(AccountSnapshotValues{AccountStatus: &status, UpstreamStatusRaw: &raw}, 2)
	if latest().UpstreamErrorMessage == nil {
		t.Fatal("scheduled collection lost last reason")
	}
	empty := ""
	save(AccountSnapshotValues{AccountStatus: &status, UpstreamStatusRaw: &raw, UpstreamErrorMessage: &empty}, 3)
	if latest().UpstreamErrorMessage == nil || *latest().UpstreamErrorMessage != "" {
		t.Fatal("explicit no reason not saved")
	}
	save(AccountSnapshotValues{AccountStatus: &status, UpstreamStatusRaw: &raw, UpstreamErrorMessage: &reason}, 4)
	recovered, using := "active", "using"
	save(AccountSnapshotValues{AccountStatus: &recovered, UpstreamStatusRaw: &using}, 5)
	if latest().UpstreamErrorMessage != nil {
		t.Fatal("recovery kept stale reason")
	}
	detail := strings.Repeat("Collection diagnostic. ", 30)
	if err := s.RecordAccountFailure(ctx, account.ID, account.IdentityKey, detail, at.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.First(&account, account.ID).Error; err != nil {
		t.Fatal(err)
	}
	if account.LastError != strings.TrimSpace(detail) {
		t.Fatal("failure detail was discarded")
	}
	if latest().AccountStatus == nil || *latest().AccountStatus != "active" {
		t.Fatal("collection failure changed account status")
	}
}
