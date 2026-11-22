package billing

import (
	"bytes"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

func TestExportCountsInheritedAccountOnceWithAllModelRows(t *testing.T) {
	account := Account{LocalAccountID: "10", IdentityKey: "same-lifetime", Username: "child@example.com", Account: "claude@example.com", PlatformAccountID: "32501"}
	second := bill(account.Account, account.PlatformAccountID, "2.20")
	second[2] = "second-model"
	report, err := Filter(source(t,
		bill(account.Account, account.PlatformAccountID, "1.10"), second,
		bill(account.Account, account.PlatformAccountID, "3.30"),
	), []Account{account, account})
	if err != nil || len(report.Summary) != 1 || len(report.Details) != 3 || report.TotalCNY != "6.60" {
		t.Fatalf("inherited visibility duplicated or dropped bill rows: %+v, %v", report, err)
	}
	if report.Summary[0].Username != account.Username || report.Columns[0] != "Username" {
		t.Fatal("missing actual owner")
	}
	for _, row := range report.Details {
		if row[0] != account.Username || row[1] != account.Account {
			t.Fatal("username confused with Claude account email", row)
		}
	}
	report.UserEmail, report.IncludeSubUsers = "parent@example.com", true
	data, err := Workbook(report)
	if err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, sheet := range []string{"Account summary", "Bill details", "Needs review"} {
		header, _ := f.GetCellValue(sheet, "A6")
		scope, _ := f.GetCellValue(sheet, "B4")
		if header != "Username" || !strings.Contains(scope, "all levels of sub-users") {
			t.Fatalf("missing scope/owner header in %s: %q, %q", sheet, header, scope)
		}
	}
	for _, sheet := range []string{"Account summary", "Bill details"} {
		username, _ := f.GetCellValue(sheet, "A7")
		if username != account.Username {
			t.Fatalf("%s owner = %q", sheet, username)
		}
	}
}

func TestStoredUsageDeduplicatesVisibilityButKeepsDatesAndModels(t *testing.T) {
	account := Account{LocalAccountID: "10", IdentityKey: "one", Username: "grandchild@example.com", Account: "claude@example.com"}
	rows := []StoredUsageRow{
		{LocalAccountID: "10", UsageDate: "2026-11-20", DailyRow: DailyRow{Model: "one", CostCNY: "1.10", InputTokens: "10", OutputTokens: "20", CacheReadTokens: "30", CacheWriteTokens: "40"}},
		{LocalAccountID: "10", UsageDate: "2026-11-21", DailyRow: DailyRow{Model: "one", CostCNY: "2.20", InputTokens: "10", OutputTokens: "20", CacheReadTokens: "30", CacheWriteTokens: "40"}},
		{LocalAccountID: "10", UsageDate: "2026-11-21", DailyRow: DailyRow{Model: "two", CostCNY: "3.30", InputTokens: "10", OutputTokens: "20", CacheReadTokens: "30", CacheWriteTokens: "40"}},
	}
	report, err := StoredUsageReport([]Account{account, account}, rows)
	if err != nil || report.TotalCNY != "6.60" || len(report.Summary) != 1 || len(report.Details) != 2 {
		t.Fatalf("wrong account/model period sums: %+v, %v", report, err)
	}
	if report.Details[0][0] != account.Username || report.Details[0][4] != "one" || report.Details[0][5] != "20" || report.Details[0][9] != "3.30" {
		t.Fatal("missing owner or daily contributions", report.Details)
	}
}

func TestStoredUsageExcludesAmbiguousOwnershipFromTotal(t *testing.T) {
	accounts := []Account{
		{LocalAccountID: "1", Username: "parent@example.com", Account: "shared@example.com", PlatformAccountID: "32501"},
		{LocalAccountID: "2", Username: "child@example.com", Account: "shared@example.com", PlatformAccountID: "32501"},
		{LocalAccountID: "3", Username: "grandchild@example.com", Account: "valid@example.com", PlatformAccountID: "32502"},
	}
	rows := []StoredUsageRow{}
	for _, account := range accounts {
		rows = append(rows, StoredUsageRow{LocalAccountID: account.LocalAccountID, UsageDate: "2026-11-21", DailyRow: DailyRow{Model: "one", CostCNY: "5", InputTokens: "1", OutputTokens: "2", CacheReadTokens: "3", CacheWriteTokens: "4"}})
	}
	report, err := StoredUsageReport(accounts, rows)
	if err != nil || report.TotalCNY != "5.00" || len(report.Details) != 1 || len(report.Review) != 2 || len(report.Summary) != 3 {
		t.Fatalf("ambiguous ownership counted: %+v, %v", report, err)
	}
	for i, review := range report.Review {
		if review.Username != accounts[i].Username || report.Summary[i].AmountCNY != nil {
			t.Fatal("review must preserve each recorded owner without assigning an amount", report)
		}
	}
}
