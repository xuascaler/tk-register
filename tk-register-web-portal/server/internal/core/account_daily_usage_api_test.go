package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const batchUsageDate = "2026-11-21"

type batchUsageFixture struct {
	s       *Store
	owner   User
	vendor  Vendor
	version ProtocolVersion
	next    int
}

func newBatchUsageFixture(t *testing.T) *batchUsageFixture {
	t.Helper()
	s, owner, vendor, version := syncFixture(t)
	return &batchUsageFixture{s: s, owner: owner, vendor: vendor, version: version}
}

func (f *batchUsageFixture) account(t *testing.T, owner User, name string) SyncedAccount {
	t.Helper()
	f.next++
	syncSubmission(t, f.s, owner.ID, f.vendor.ID, f.version.ID, name, "claude/cc-pro", "accepted", fmt.Sprint(1000+f.next), time.Date(2026, 11, 20, 0, f.next, 0, 0, time.UTC))
	var row SyncedAccount
	if err := f.s.DB.Where("account_key = ?", accountDigest(idString(owner.ID), idString(f.vendor.ID), name)).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

func batchUsageDay(t *testing.T, s *Store, account SyncedAccount, identity, date string, success bool, amounts map[string]string) AccountUsageDay {
	t.Helper()
	now := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)
	day := AccountUsageDay{AccountID: account.ID, IdentityKey: identity, UsageDate: date, LastAttemptAt: now}
	if success {
		day.LastSuccessAt = &now
	}
	if err := s.DB.Create(&day).Error; err != nil {
		t.Fatal(err)
	}
	for model, cost := range amounts {
		row := AccountDailyUsageModified{DayID: day.ID, Model: model, InputTokens: "0", OutputTokens: "0", CacheReadTokens: "0", CacheWriteTokens: "0", CostCNY: cost}
		if err := s.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	return day
}

func batchUsageAmount(t *testing.T, got *string, want string) {
	t.Helper()
	if got == nil || *got != want {
		t.Fatalf("amount=%v want %s", got, want)
	}
}

func TestAccountDailyUsageBatchOwnerIdentityAndAvailability(t *testing.T) {
	f := newBatchUsageFixture(t)
	ctx := context.Background()
	child := hierarchyChild(t, f.s, f.owner, "batch-child@example.com")
	accounts := map[string]SyncedAccount{}
	for _, name := range []string{"paid", "zero", "raw-only", "failed", "old-identity", "deleted", "wrong-family", "wrong-vendor"} {
		accounts[name] = f.account(t, f.owner, name+"@example.com")
	}
	childAccount := f.account(t, child, "child@example.com")
	for name, account := range accounts {
		identity := account.IdentityKey
		if name == "old-identity" {
			identity = "previous-lifetime"
		}
		amounts := map[string]string{"claude-a": "72.00000000"}
		if name == "zero" {
			amounts["claude-a"] = "0"
		}
		if name == "raw-only" {
			amounts = nil
		}
		day := batchUsageDay(t, f.s, account, identity, batchUsageDate, name != "failed", amounts)
		if name == "raw-only" {
			if err := f.s.DB.Create(&AccountDailyUsage{DayID: day.ID, Model: "secret-raw-model", InputTokens: "9", OutputTokens: "8", CacheReadTokens: "7", CacheWriteTokens: "6", CostCNY: "9000"}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	batchUsageDay(t, f.s, childAccount, childAccount.IdentityKey, batchUsageDate, true, map[string]string{"child-model": "1000"})
	// Neither role nor a context/global Raw selector expands this API's scope.
	if err := f.s.DB.Model(&User{}).Where("id = ?", f.owner.ID).Updates(map[string]any{"role": "admin", "is_super_admin": true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.s.DB.Model(&Setting{}).Where("key = ?", "model_usage_source").Update("value", "raw").Error; err != nil {
		t.Fatal(err)
	}
	if err := f.s.DB.Model(&SyncedAccount{}).Where("id = ?", accounts["deleted"].ID).Update("deleted_at", time.Now()).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.s.DB.Model(&SyncedAccount{}).Where("id = ?", accounts["wrong-family"].ID).Update("plan", "other/cc-pro").Error; err != nil {
		t.Fatal(err)
	}
	otherVendor := Vendor{Code: "another-vendor", Name: "Other", BaseURL: "https://example.invalid", Timezone: "Asia/Shanghai", Enabled: true}
	if err := f.s.DB.Create(&otherVendor).Error; err != nil {
		t.Fatal(err)
	}
	wrongVendor := accounts["wrong-vendor"]
	if err := f.s.DB.Model(&SyncedAccount{}).Where("id = ?", wrongVendor.ID).Updates(map[string]any{"vendor_id": otherVendor.ID, "account_key": accountDigest(idString(f.owner.ID), idString(otherVendor.ID), wrongVendor.Account)}).Error; err != nil {
		t.Fatal(err)
	}
	names := []string{"  PAID@example.com ", "zero@example.com", "paid@example.com", "raw-only@example.com", "failed@example.com", "old-identity@example.com", "deleted@example.com", "wrong-family@example.com", "wrong-vendor@example.com", "child@example.com", "unknown@example.com"}
	out, err := f.s.AccountDailyUsageBatch(WithModelUsageSource(ctx, "raw"), f.owner.ID, f.vendor.Code, names, batchUsageDate, true)
	if err != nil {
		t.Fatal(err)
	}
	if out.Date != batchUsageDate || out.Timezone != "Asia/Shanghai" || out.ExchangeRate != "7.2" || out.Summary.Status != "partial" || out.Summary.RequestedAccounts != 10 || out.Summary.AvailableAccounts != 2 || out.Summary.NoDataAccounts != 3 || out.Summary.NotFoundAccounts != 5 {
		t.Fatalf("wrong availability: %+v", out)
	}
	batchUsageAmount(t, out.Summary.CostCNY, "72.00000000")
	batchUsageAmount(t, out.Summary.CostUSD, "10.00")
	wantStatus := []string{"ok", "ok", "no_data", "no_data", "no_data", "not_found", "not_found", "not_found", "not_found", "not_found"}
	for i, item := range out.Accounts {
		if item.Status != wantStatus[i] || item.Models == nil {
			t.Fatalf("account %d: %+v", i, item)
		}
		if i >= 2 && (item.CostCNY != nil || item.CostUSD != nil || len(*item.Models) != 0) {
			t.Fatalf("missing data manufactured a bill: %+v", item)
		}
	}
	if out.Accounts[0].Account != "paid@example.com" || out.Accounts[1].Account != "zero@example.com" {
		t.Fatal("deduplication changed order", out.Accounts)
	}
	batchUsageAmount(t, out.Accounts[1].CostUSD, "0.00")
	if out.ModelTotals == nil || len(*out.ModelTotals) != 1 || (*out.ModelTotals)[0].Model != "claude-a" {
		t.Fatal("foreign or historical models leaked", out.ModelTotals)
	}
	zero, err := f.s.AccountDailyUsageBatch(ctx, f.owner.ID, f.vendor.Code, []string{"zero@example.com"}, batchUsageDate, true)
	if err != nil || zero.Summary.Status != "complete" || zero.Summary.AvailableAccounts != 1 {
		t.Fatal("a confirmed zero was treated as missing", zero, err)
	}
	batchUsageAmount(t, zero.Summary.CostUSD, "0.00")
	// Reading another owner is not enabled merely by selecting their account name.
	if err := f.s.DB.Model(&SyncedAccount{}).Where("id = ?", accounts["paid"].ID).Update("user_id", child.ID).Error; err != nil {
		t.Fatal(err)
	}
	out, err = f.s.AccountDailyUsageBatch(ctx, f.owner.ID, f.vendor.Code, []string{"paid@example.com"}, batchUsageDate, true)
	if err != nil || out.Accounts[0].Status != "not_found" || out.Summary.CostUSD != nil {
		t.Fatal("transferred account remained readable", out, err)
	}
}

func TestAccountDailyUsageBatchExactRoundingAndModelVisibility(t *testing.T) {
	f := newBatchUsageFixture(t)
	names := []string{"a@example.com", "b@example.com", "c@example.com"}
	for i, name := range names {
		account := f.account(t, f.owner, name)
		amounts := map[string]string{"claude-a": "0.03600000"}
		if i == 0 {
			amounts["claude-b"] = "0.03600000"
		}
		day := batchUsageDay(t, f.s, account, account.IdentityKey, batchUsageDate, true, amounts)
		if i == 0 {
			// A failed later collection must not discard saved successful amounts.
			if err := f.s.DB.Model(&AccountUsageDay{}).Where("id = ?", day.ID).Update("last_error", "later attempt failed").Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	out, err := f.s.AccountDailyUsageBatch(context.Background(), f.owner.ID, f.vendor.Code, names, batchUsageDate, true)
	if err != nil {
		t.Fatal(err)
	}
	if out.Summary.Status != "complete" || out.Summary.AvailableAccounts != 3 {
		t.Fatal(out.Summary)
	}
	batchUsageAmount(t, out.Summary.CostCNY, "0.14400000")
	// Each account rounds independently: 0.01 + 0.01 + 0.01. Re-converting the
	// combined CNY would give 0.02 and disagree with the returned account amounts.
	batchUsageAmount(t, out.Summary.CostUSD, "0.03")
	for _, item := range out.Accounts {
		batchUsageAmount(t, item.CostUSD, "0.01")
	}
	models := *out.Accounts[0].Models
	if len(models) != 2 || models[0].Model != "claude-a" || models[0].CostUSD != "0.01" || models[1].CostUSD != "0.00" {
		t.Fatal("model remainder allocation is not stable", models)
	}
	if len(*out.ModelTotals) != 2 || (*out.ModelTotals)[0].CostUSD != "0.03" || (*out.ModelTotals)[1].CostUSD != "0.00" {
		t.Fatal("model totals do not reconcile", out.ModelTotals)
	}
	hidden, err := f.s.AccountDailyUsageBatch(context.Background(), f.owner.ID, f.vendor.Code, names, batchUsageDate, false)
	if err != nil {
		t.Fatal(err)
	}
	batchUsageAmount(t, hidden.Summary.CostUSD, "0.03")
	payload, err := json.Marshal(hidden)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{`"models"`, `"model_totals"`, `"claude-a"`, `"model_usage_source"`, `"day_id"`} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("hidden field %s leaked: %s", forbidden, payload)
		}
	}
	if hidden.ModelCostsEnabled {
		t.Fatal("visibility flag enabled")
	}
	if err := f.s.DB.Model(&Setting{}).Where("key = ?", exchangeRateSettingKey).Update("value", "0.1").Error; err != nil {
		t.Fatal(err)
	}
	changed, err := f.s.AccountDailyUsageBatch(context.Background(), f.owner.ID, f.vendor.Code, names, batchUsageDate, true)
	if err != nil || changed.ExchangeRate != "0.1" {
		t.Fatal(changed, err)
	}
	batchUsageAmount(t, changed.Summary.CostUSD, "1.44")
	if (*changed.Accounts[0].Models)[0].CostUSD != "0.36" || (*changed.Accounts[0].Models)[1].CostUSD != "0.36" {
		t.Fatal("updated exchange rate not applied consistently", changed)
	}
}

type batchUsageSQLLog struct {
	logger.Interface
	statements []string
}

func (l *batchUsageSQLLog) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	sql, _ := fc()
	l.statements = append(l.statements, sql)
}

func TestAccountDailyUsageBatchMissingDaysBoundsAndBoundedQueries(t *testing.T) {
	f := newBatchUsageFixture(t)
	account := f.account(t, f.owner, "missing@example.com")
	// A bill on a different date must not serve as fallback for the requested day.
	batchUsageDay(t, f.s, account, account.IdentityKey, "2026-11-20", true, map[string]string{"claude-a": "100"})
	capture := &batchUsageSQLLog{Interface: logger.Default.LogMode(logger.Silent)}
	f.s.DB = f.s.DB.Session(&gorm.Session{Logger: capture})
	out, err := f.s.AccountDailyUsageBatch(context.Background(), f.owner.ID, f.vendor.Code, []string{account.Account, "not-found@example.com"}, batchUsageDate, true)
	if err != nil || out.Summary.Status != "no_data" || out.Summary.AvailableAccounts != 0 || out.Summary.NoDataAccounts != 1 || out.Summary.NotFoundAccounts != 1 || out.Summary.CostCNY != nil || out.Summary.CostUSD != nil || out.ModelTotals == nil || len(*out.ModelTotals) != 0 {
		t.Fatal("missing day is not null", out, err)
	}
	forbidden := regexp.MustCompile(`(?i)\b(account_daily_usages|overview_summaries|overview_summaries_modified|account_snapshots)\b`)
	for _, sql := range capture.statements {
		if forbidden.MatchString(sql) {
			t.Fatal("queried a forbidden monetary source", sql)
		}
	}
	if len(capture.statements) > 5 {
		t.Fatal("unexpected query count", len(capture.statements))
	}
	names := []string{account.Account}
	for i := 1; i < 100; i++ {
		names = append(names, fmt.Sprintf("not-found-%d@example.com", i))
	}
	capture.statements = nil
	out, err = f.s.AccountDailyUsageBatch(context.Background(), f.owner.ID, f.vendor.Code, names, batchUsageDate, false)
	if err != nil || len(out.Accounts) != 100 || len(capture.statements) > 5 {
		t.Fatal("batch expanded into per-account queries", len(out.Accounts), len(capture.statements), err)
	}
	names = append(names, account.Account)
	if _, err = f.s.AccountDailyUsageBatch(context.Background(), f.owner.ID, f.vendor.Code, names, batchUsageDate, false); err != nil {
		t.Fatal("duplicates counted against unique limit", err)
	}
	for _, invalid := range [][]string{nil, {""}, {"bad name"}, {"control\x00name"}, {"invalid\xff@example.com"}, append(names, "one-too-many@example.com")} {
		if _, err = f.s.AccountDailyUsageBatch(context.Background(), f.owner.ID, f.vendor.Code, invalid, batchUsageDate, false); err == nil {
			t.Fatal("invalid account selection accepted", invalid)
		}
	}
	for _, date := range []string{"2026-2-01", "2026-02-29", "2026-11-21T00:00:00Z", ""} {
		if _, err = f.s.AccountDailyUsageBatch(context.Background(), f.owner.ID, f.vendor.Code, []string{account.Account}, date, false); err == nil {
			t.Fatal("invalid date accepted", date)
		}
	}
	if _, err = f.s.AccountDailyUsageBatch(context.Background(), 0, f.vendor.Code, []string{account.Account}, batchUsageDate, false); !errors.Is(err, ErrNotFound) {
		t.Fatal("zero owner authorized", err)
	}
	out, err = f.s.AccountDailyUsageBatch(context.Background(), f.owner.ID, "no-such-vendor", []string{account.Account}, batchUsageDate, false)
	if err != nil || out.Summary.Status != "no_data" || out.Summary.NotFoundAccounts != 1 || out.Summary.CostUSD != nil {
		t.Fatal("unknown vendor fell back to another source", out, err)
	}
}
