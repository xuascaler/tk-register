package billing

import (
	"bytes"
	"github.com/xuri/excelize/v2"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func source(t *testing.T, rows ...[]string) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	all := append([][]string{sourceColumns}, rows...)
	for i, row := range all {
		for j, v := range row {
			cell, _ := excelize.CoordinatesToCellName(j+1, i+1)
			if e := f.SetCellStr("Sheet1", cell, v); e != nil {
				t.Fatal(e)
			}
		}
	}
	b, e := f.WriteToBuffer()
	if e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func bill(email, id, cost string) []string {
	return []string{email, id, "claude-test", "10", "20", "30", "40", cost, "private remark"}
}
func TestFilterExcludesOtherUsersAndGrandTotals(t *testing.T) {
	b, e := os.ReadFile("testdata/claude-bills.xlsx")
	if e != nil {
		t.Fatal(e)
	}
	r, e := Filter(b, []Account{{LocalAccountID: "1", Account: "one@example.com", PlatformAccountID: "15001"}})
	if e != nil {
		t.Fatal(e)
	}
	if r.TotalCNY != "12.34" || len(r.Details) != 1 || len(r.Columns) != 9 || r.Summary[0].Status != "matched" {
		t.Fatalf("unexpected report %+v", r)
	}
	for _, row := range r.Details {
		if strings.Contains(strings.Join(row, ","), "other@example") {
			t.Fatal("cross-owner leak")
		}
	}
}
func TestMissingIDsAndMappingConflictsAreNotZero(t *testing.T) {
	r, e := Filter(source(t, bill("wrong@example.com", "15001", "5"), bill("changed@example.com", "15003", "6")), []Account{{Account: "one@example.com", PlatformAccountID: "15001"}, {Account: "missing@example.com"}, {Account: "changed@example.com", PlatformAccountID: "15002"}, {Account: "empty@example.com", PlatformAccountID: "999"}})
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Review) != 3 || len(r.Details) != 0 || r.TotalCNY != "0.00" {
		t.Fatalf("bad conflicts %+v", r)
	}
	for _, s := range r.Summary[:3] {
		if s.AmountCNY != nil || s.Status != "needs_review" {
			t.Fatal("conflict became zero")
		}
	}
	if r.Summary[3].Status != "no_bills" || *r.Summary[3].AmountCNY != "0.00" {
		t.Fatal("no bills not distinguished")
	}
}
func TestMoneyExactAndWorkbookLiteralText(t *testing.T) {
	email := "=cmd@example.com"
	r, e := Filter(source(t, bill(email, "15001", "0.10000001"), bill(email, "15001", "0.2")), []Account{{Account: email, PlatformAccountID: "15001"}})
	if e != nil {
		t.Fatal(e)
	}
	if r.TotalCNY != "0.30000001" {
		t.Fatal(r.TotalCNY)
	}
	r.UserEmail = "owner@example.com"
	r.StartDate = "2026-11-01"
	r.EndDate = "2026-11-11"
	out, e := Workbook(r)
	if e != nil {
		t.Fatal(e)
	}
	f, e := excelize.OpenReader(bytes.NewReader(out))
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	formula, _ := f.GetCellFormula("Bill details", "A7")
	got, _ := f.GetCellValue("Bill details", "A7")
	amount, _ := f.GetCellValue("Account summary", "F7", excelize.Options{RawCellValue: true})
	if formula != "" || got != email || amount != "0.30000001" {
		t.Fatalf("literal/numeric mismatch %q %q %q", formula, got, amount)
	}
	if len(f.GetSheetList()) != 3 {
		t.Fatal("missing sheets")
	}
}
func TestUnsupportedOrInvalidSourceFails(t *testing.T) {
	bad := bill("one@example.com", "15001", "NaN")
	if _, e := Filter(source(t, bad), []Account{{Account: "one@example.com", PlatformAccountID: "15001"}}); e == nil {
		t.Fatal("invalid amount accepted")
	}
	if _, e := Filter([]byte("not xlsx"), nil); e == nil {
		t.Fatal("invalid file accepted")
	}
	f := excelize.NewFile()
	_ = f.SetCellStr("Sheet1", "A1", "Changed contract")
	b, _ := f.WriteToBuffer()
	_ = f.Close()
	if _, e := Filter(b.Bytes(), nil); e == nil {
		t.Fatal("unknown columns accepted")
	}
}
func TestDuplicatePlatformMappingExcluded(t *testing.T) {
	r, e := Filter(source(t, bill("one@example.com", "15001", "5")), []Account{{LocalAccountID: "1", Account: "one@example.com", PlatformAccountID: "15001"}, {LocalAccountID: "2", Account: "two@example.com", PlatformAccountID: "15001"}})
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Review) != 2 || len(r.Details) != 0 {
		t.Fatal("ambiguous match")
	}
}

func TestLargeTokenCountersStayExactInWorkbook(t *testing.T) {
	row := bill("one@example.com", "15001", "12.34")
	row[3] = "123456789012345678"
	report, err := Filter(source(t, row), []Account{{Account: "one@example.com", PlatformAccountID: "15001"}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Workbook(report)
	if err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := f.GetCellValue("Bill details", "E7", excelize.Options{RawCellValue: true})
	if err != nil || got != row[3] {
		t.Fatalf("token precision lost: %q %v", got, err)
	}
}

func TestSubmissionTimesAreBeijingDatesInBothWorkbookSheets(t *testing.T) {
	submitted, err := time.Parse(time.RFC3339, "2026-11-13T23:15:29-07:00")
	if err != nil {
		t.Fatal(err)
	}
	zero := time.Time{}
	r, err := Filter(source(t,
		bill("one@example.com", "15001", "0.10"),
		bill("one@example.com", "15001", "0.20"),
		bill("legacy@example.com", "15002", "0.30"),
		bill("zero@example.com", "15003", "0.40"),
	), []Account{
		{Account: "one@example.com", PlatformAccountID: "15001", LatestSubmittedAt: &submitted},
		{Account: "legacy@example.com", PlatformAccountID: "15002"},
		{Account: "zero@example.com", PlatformAccountID: "15003", LatestSubmittedAt: &zero},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantTime := "2026-11-14 14:15:29"
	if r.Columns[2] != "Submitted" || r.Details[0][2] != wantTime || r.Details[1][2] != wantTime ||
		r.Details[2][2] != "—" || r.Details[3][2] != "—" || r.TotalCNY != "1.00" {
		t.Fatalf("incorrect timestamp propagation or totals: %+v", r)
	}
	b, err := Workbook(r)
	if err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, sheet := range []string{"Account summary", "Bill details"} {
		header, err := f.GetCellValue(sheet, "C6")
		if err != nil || header != "Submitted" {
			t.Fatalf("%s missing Submitted header: %q %v", sheet, header, err)
		}
		formatted, err := f.GetCellValue(sheet, "C7")
		if err != nil || formatted != wantTime {
			t.Fatalf("%s timestamp changed timezone: %q %v", sheet, formatted, err)
		}
		raw, err := f.GetCellValue(sheet, "C7", excelize.Options{RawCellValue: true})
		if err != nil {
			t.Fatal(err)
		}
		serial, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			t.Fatalf("%s should have a sortable Excel date, not text: %q", sheet, raw)
		}
		date, err := excelize.ExcelDateToTime(serial, false)
		if err != nil || date.Format(submittedLayout) != wantTime {
			t.Fatalf("%s wrong Excel date serial: %q %v", sheet, date, err)
		}
	}
	for _, check := range []struct{ sheet, cell, want string }{
		{"Account summary", "C8", "—"}, {"Account summary", "C9", "—"},
		{"Bill details", "C8", wantTime}, {"Bill details", "C9", "—"}, {"Bill details", "C10", "—"},
		{"Bill details", "D7", "claude-test"}, {"Bill details", "E7", "10"},
		{"Bill details", "F7", "20"}, {"Bill details", "G7", "30"}, {"Bill details", "H7", "40"},
		{"Bill details", "I7", "0.10"}, {"Account summary", "F7", "0.30"},
		{"Account summary", "F11", "1.00"},
	} {
		value, err := f.GetCellValue(check.sheet, check.cell)
		if err != nil || value != check.want {
			t.Errorf("%s!%s: got %q want %q: %v", check.sheet, check.cell, value, check.want, err)
		}
	}
}
