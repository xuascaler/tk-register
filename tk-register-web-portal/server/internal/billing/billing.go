// Package billing filters the supplier's Claude export against an immutable
// selection of locally owned accounts. Raw supplier workbooks are never returned.
package billing

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
)

const MaxRows = 50000
const maxInputBytes = 16 << 20
const maxUnzippedBytes = 64 << 20
const submittedLayout = "2006-01-02 15:04:05"

var beijing = time.FixedZone("Asia/Shanghai", 8*60*60)

var ErrFormat = errors.New("The supplier bill export has an unsupported format. No report was generated.")
var sourceColumns = []string{"邮箱", "平台账号ID", "模型", "输入Token", "输出Token", "缓存读Token", "缓存写Token", "消费(人民币/元)", "备注"}
var decimalPattern = regexp.MustCompile(`^[0-9]{1,15}(\.[0-9]{1,8})?$`)
var exportNumberPattern = regexp.MustCompile(`^[0-9]{1,70}(\.[0-9]{1,8})?$`)
var integerPattern = regexp.MustCompile(`^[0-9]{1,18}$`)

type Account struct {
	LocalAccountID    string
	Username          string
	Account           string
	Plan              string
	PlatformAccountID string
	SupplierRecordID  string
	RemoteID          string
	IdentityKey       string
	LatestSubmittedAt *time.Time
}
type Summary struct {
	Username          string     `json:"username"`
	Account           string     `json:"account"`
	PlatformAccountID string     `json:"platform_account_id"`
	LatestSubmittedAt *time.Time `json:"latest_submitted_at"`
	Plan              string     `json:"plan"`
	RowCount          int        `json:"row_count"`
	AmountCNY         *string    `json:"amount_cny"`
	Status            string     `json:"status"`
	Reason            string     `json:"reason,omitempty"`
}
type Review struct {
	Username          string `json:"username"`
	Account           string `json:"account"`
	PlatformAccountID string `json:"platform_account_id"`
	Reason            string `json:"reason"`
}
type Report struct {
	IncludeSubUsers bool       `json:"include_sub_users"`
	UserEmail       string     `json:"user_email"`
	UserEmails      []string   `json:"user_emails,omitempty"`
	StartDate       string     `json:"start_date"`
	EndDate         string     `json:"end_date"`
	Summary         []Summary  `json:"summary"`
	Details         [][]string `json:"details"`
	Columns         []string   `json:"columns"`
	Review          []Review   `json:"review"`
	TotalCNY        string     `json:"total_cny"`
	Currency        string     `json:"currency"`
}

func normalized(v string) string { return strings.ToLower(strings.TrimSpace(v)) }
func validID(v string) bool      { return integerPattern.MatchString(v) && strings.TrimLeft(v, "0") != "" }
func decimal(s string) (*big.Rat, error) {
	if !decimalPattern.MatchString(s) {
		return nil, ErrFormat
	}
	v, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, ErrFormat
	}
	return v, nil
}
func amount(v *big.Rat) string {
	s := v.FloatString(8)
	s = strings.TrimRight(s, "0")
	if strings.HasSuffix(s, ".") {
		return s + "00"
	}
	if len(s)-strings.LastIndex(s, ".") == 2 {
		return s + "0"
	}
	return s
}

func submittedTime(value *time.Time) string {
	if value == nil || value.IsZero() {
		return "—"
	}
	return value.In(beijing).Format(submittedLayout)
}

func emptyReport() Report {
	columns := append([]string{"Username", sourceColumns[0], sourceColumns[1], "Submitted"}, sourceColumns[2:8]...)
	return Report{Summary: []Summary{}, Details: [][]string{}, Columns: columns, Review: []Review{}, TotalCNY: "0.00", Currency: "CNY"}
}

// selectAccounts counts inherited visibility once and excludes conflicting local
// ownership. Missing upstream IDs do not prevent exports of local ledger lines.
func selectAccounts(accounts []Account, requirePlatformID bool) ([]Account, map[string]int, map[string]int, map[int]string) {
	idOwner := map[string]int{}
	emailOwner := map[string]int{}
	seenLocal := map[string]bool{}
	selected := []Account{}
	conflicts := map[int]string{}
	for _, a := range accounts {
		key := a.LocalAccountID + ":" + a.IdentityKey
		if a.LocalAccountID != "" && seenLocal[key] {
			continue
		}
		seenLocal[key] = true
		i := len(selected)
		selected = append(selected, a)
		email := normalized(a.Account)
		id := strings.TrimSpace(a.PlatformAccountID)
		if !validID(id) && requirePlatformID {
			conflicts[i] = "Platform account ID is missing or invalid."
		}
		if validID(id) {
			if other, ok := idOwner[id]; ok {
				conflicts[i] = "Multiple local accounts share this platform account ID."
				conflicts[other] = conflicts[i]
			} else {
				idOwner[id] = i
			}
		}
		if other, ok := emailOwner[email]; ok {
			conflicts[i] = "Multiple local account records share this email."
			conflicts[other] = conflicts[i]
		} else {
			emailOwner[email] = i
		}
	}
	return selected, idOwner, emailOwner, conflicts
}

func Filter(data []byte, accounts []Account) (Report, error) {
	out := emptyReport()
	if len(data) == 0 || len(data) > maxInputBytes {
		return out, ErrFormat
	}
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(z.File) > 1000 {
		return out, ErrFormat
	}
	var size uint64
	for _, f := range z.File {
		if f.UncompressedSize64 > maxUnzippedBytes {
			return out, ErrFormat
		}
		size += f.UncompressedSize64
		if size > maxUnzippedBytes {
			return out, ErrFormat
		}
	}
	f, err := excelize.OpenReader(bytes.NewReader(data), excelize.Options{RawCellValue: true, UnzipSizeLimit: maxUnzippedBytes, UnzipXMLSizeLimit: maxUnzippedBytes})
	if err != nil {
		return out, ErrFormat
	}
	defer f.Close()
	sheets := f.GetSheetList()
	if len(sheets) != 1 {
		return out, ErrFormat
	}
	rows, err := f.Rows(sheets[0])
	if err != nil {
		return out, ErrFormat
	}
	defer rows.Close()
	if !rows.Next() {
		return out, ErrFormat
	}
	headers, err := rows.Columns(excelize.Options{RawCellValue: true})
	if err != nil || len(headers) != len(sourceColumns) {
		return out, ErrFormat
	}
	for i, v := range headers {
		if strings.TrimSpace(v) != sourceColumns[i] {
			return out, ErrFormat
		}
	}
	type entry struct {
		row       []string
		id, email string
	}
	entries := []entry{}
	for n := 0; rows.Next(); n++ {
		if n >= MaxRows {
			return out, ErrFormat
		}
		cells, e := rows.Columns(excelize.Options{RawCellValue: true})
		if e != nil || len(cells) > 9 {
			return out, ErrFormat
		}
		row := make([]string, 9)
		copy(row, cells)
		empty := true
		for _, v := range row {
			if len(v) > 2048 {
				return out, ErrFormat
			}
			if strings.TrimSpace(v) != "" {
				empty = false
			}
		}
		if empty {
			continue
		}
		// Only known footer labels are ignored; original supplier grand totals must
		// not survive filtering or count towards the selected user's total.
		label := normalized(row[0])
		if (label == "合计" || label == "总计" || label == "total" || label == "grand total") && strings.TrimSpace(row[1]) == "" {
			continue
		}
		id := strings.TrimSpace(row[1])
		email := normalized(row[0])
		if !validID(id) || email == "" {
			return out, ErrFormat
		}
		entries = append(entries, entry{row, id, email})
	}
	if rows.Error() != nil {
		return out, ErrFormat
	}
	selected, idOwner, emailOwner, conflicts := selectAccounts(accounts, true)
	for _, e := range entries {
		if i, ok := idOwner[e.id]; ok && normalized(selected[i].Account) != e.email {
			conflicts[i] = "The supplier email does not match the saved platform account ID."
		}
		if i, ok := emailOwner[e.email]; ok && strings.TrimSpace(selected[i].PlatformAccountID) != e.id {
			conflicts[i] = "The supplier export contains a different platform account ID for this email."
		}
	}
	totals := make([]*big.Rat, len(selected))
	counts := make([]int, len(selected))
	total := new(big.Rat)
	for _, e := range entries {
		i, ok := idOwner[e.id]
		if !ok || conflicts[i] != "" {
			continue
		}
		if e.email != normalized(selected[i].Account) {
			return out, ErrFormat
		}
		row := append([]string(nil), e.row[:8]...)
		for col := 3; col <= 6; col++ {
			if !integerPattern.MatchString(strings.TrimSpace(row[col])) {
				return out, ErrFormat
			}
			row[col] = strings.TrimSpace(row[col])
		}
		value, err := decimal(strings.TrimSpace(row[7]))
		if err != nil {
			return out, err
		}
		if strings.TrimSpace(row[2]) == "" {
			return out, ErrFormat
		}
		// Use the frozen local identity, never arbitrary spreadsheet account text.
		row[0] = selected[i].Account
		row[1] = strings.TrimSpace(selected[i].PlatformAccountID)
		row[2] = strings.ReplaceAll(row[2], "温州润晨", "xxxxx")
		row[7] = amount(value)
		if totals[i] == nil {
			totals[i] = new(big.Rat)
		}
		totals[i].Add(totals[i], value)
		total.Add(total, value)
		counts[i]++
		// Add local metadata only after validating the unchanged supplier contract.
		row = append([]string{selected[i].Username, row[0], row[1], submittedTime(selected[i].LatestSubmittedAt)}, row[2:]...)
		out.Details = append(out.Details, row)
	}
	for i, a := range selected {
		s := Summary{Username: a.Username, Account: a.Account, PlatformAccountID: strings.TrimSpace(a.PlatformAccountID), LatestSubmittedAt: a.LatestSubmittedAt, Plan: a.Plan, RowCount: counts[i]}
		if reason := conflicts[i]; reason != "" {
			s.Status = "needs_review"
			s.Reason = reason
			out.Review = append(out.Review, Review{Username: s.Username, Account: s.Account, PlatformAccountID: s.PlatformAccountID, Reason: reason})
		} else if counts[i] == 0 {
			s.Status = "no_bills"
			s.Reason = "No bill rows were returned for this account in the selected dates."
			v := "0.00"
			s.AmountCNY = &v
		} else {
			s.Status = "matched"
			v := amount(totals[i])
			s.AmountCNY = &v
		}
		out.Summary = append(out.Summary, s)
	}
	out.TotalCNY = amount(total)
	return out, nil
}

// Workbook creates a new file from filtered data only. All account/model text is
// written as literal strings, so supplier-controlled text cannot become formulas.
func Workbook(r Report) ([]byte, error) {
	if r.Currency != "CNY" || len(r.Details) > MaxRows {
		return nil, ErrFormat
	}
	f := excelize.NewFile()
	defer f.Close()
	if err := f.SetSheetName("Sheet1", "Account summary"); err != nil {
		return nil, err
	}
	for _, sheet := range []string{"Bill details", "Needs review"} {
		if _, err := f.NewSheet(sheet); err != nil {
			return nil, err
		}
	}
	headerStyle, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Color: "FFFFFF", Size: 11}, Fill: excelize.Fill{Type: "pattern", Color: []string{"1C426F"}, Pattern: 1}, Alignment: &excelize.Alignment{Vertical: "center"}})
	if err != nil {
		return nil, err
	}
	moneyFormat := "#,##0.00######"
	moneyStyle, err := f.NewStyle(&excelize.Style{CustomNumFmt: &moneyFormat})
	if err != nil {
		return nil, err
	}
	dateFormat := "yyyy-mm-dd hh:mm:ss"
	dateStyle, err := f.NewStyle(&excelize.Style{CustomNumFmt: &dateFormat})
	if err != nil {
		return nil, err
	}
	titleStyle, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Color: "1C426F", Size: 15}})
	if err != nil {
		return nil, err
	}
	write := func(sheet string, row int, values []string) error {
		for i, v := range values {
			cell, e := excelize.CoordinatesToCellName(i+1, row)
			if e != nil {
				return e
			}
			if e = f.SetCellStr(sheet, cell, v); e != nil {
				return e
			}
		}
		return nil
	}
	users := r.UserEmails
	if len(users) == 0 {
		users = []string{r.UserEmail}
	}
	userLabel := "User"
	if len(users) > 1 {
		userLabel = fmt.Sprintf("Users (%d)", len(users))
	}
	// Large selections get one email per row on a separate sheet. This keeps
	// headers readable and avoids Excel's row-height and cell-text limits.
	separateUsers := len(users) > 8
	userHeader := strings.Join(users, "\n")
	if separateUsers {
		userHeader = "See Selected users sheet."
	}
	numeric := func(sheet, cell, value string) error {
		if !exportNumberPattern.MatchString(value) {
			return ErrFormat
		}
		digits := strings.Trim(strings.ReplaceAll(value, ".", ""), "0")
		if len(digits) > 15 {
			return f.SetCellStr(sheet, cell, value) // Excel numeric cells have 15 significant digits.
		}
		return f.SetCellDefault(sheet, cell, value)
	}
	dateCell := func(sheet, cell, value string) error {
		if value == "—" {
			return nil // Already written as a literal missing value.
		}
		// Excel dates have no timezone. Store the already-converted Beijing wall
		// clock as a numeric date so sorting works without another UTC conversion.
		wallClock, e := time.Parse(submittedLayout, value)
		if e != nil {
			return ErrFormat
		}
		if e = f.SetCellValue(sheet, cell, wallClock); e != nil {
			return e
		}
		return f.SetCellStyle(sheet, cell, cell, dateStyle)
	}
	setup := func(sheet string, headers []string) error {
		if e := write(sheet, 1, []string{"Claude bill export"}); e != nil {
			return e
		}
		if e := f.SetCellStyle(sheet, "A1", "A1", titleStyle); e != nil {
			return e
		}
		if e := write(sheet, 2, []string{userLabel, userHeader}); e != nil {
			return e
		}
		if len(users) > 1 && !separateUsers {
			userStyle, e := f.NewStyle(&excelize.Style{Alignment: &excelize.Alignment{WrapText: true, Vertical: "top"}})
			if e != nil {
				return e
			}
			if e = f.MergeCell(sheet, "B2", "D2"); e != nil {
				return e
			}
			if e = f.SetCellStyle(sheet, "B2", "D2", userStyle); e != nil {
				return e
			}
			if e = f.SetRowHeight(sheet, 2, float64(len(users)*15)); e != nil {
				return e
			}
		}
		if e := write(sheet, 3, []string{"Dates", r.StartDate + " — " + r.EndDate, "Timezone", "Asia/Shanghai"}); e != nil {
			return e
		}
		scope := "Accounts owned by the selected user when the export was requested."
		if len(users) > 1 {
			scope = "Accounts owned by the selected users when the export was requested. Each account is counted once."
		}
		if r.IncludeSubUsers {
			scope = "Accounts owned by the selected users and all levels of sub-users when the export was requested. Each account is counted once."
		}
		if e := write(sheet, 4, []string{"Scope", scope, "Currency", "CNY"}); e != nil {
			return e
		}
		if e := write(sheet, 6, headers); e != nil {
			return e
		}
		end, _ := excelize.CoordinatesToCellName(len(headers), 6)
		if e := f.SetCellStyle(sheet, "A6", end, headerStyle); e != nil {
			return e
		}
		if e := f.SetRowHeight(sheet, 6, 24); e != nil {
			return e
		}
		if e := f.SetColWidth(sheet, "A", "A", 34); e != nil {
			return e
		}
		if e := f.SetColWidth(sheet, "B", "B", 34); e != nil {
			return e
		}
		if len(headers) > 2 {
			last, _ := excelize.ColumnNumberToName(len(headers))
			if e := f.SetColWidth(sheet, "C", last, 22); e != nil {
				return e
			}
		}
		return f.SetPanes(sheet, &excelize.Panes{Freeze: true, YSplit: 6, TopLeftCell: "A7", ActivePane: "bottomLeft"})
	}
	if separateUsers {
		const sheet = "Selected users"
		if _, err = f.NewSheet(sheet); err != nil {
			return nil, err
		}
		if err = setup(sheet, []string{"User email"}); err != nil {
			return nil, err
		}
		if err = f.SetColWidth(sheet, "A", "A", 60); err != nil {
			return nil, err
		}
		for i, email := range users {
			if err = write(sheet, i+7, []string{email}); err != nil {
				return nil, err
			}
		}
	}
	if err = setup("Account summary", []string{"Username", "Account", "Platform account ID", "Submitted", "Plan", "Bill rows", "Total (CNY)", "Result", "Notes"}); err != nil {
		return nil, err
	}
	for i, s := range r.Summary {
		row := i + 7
		v := ""
		if s.AmountCNY != nil {
			v = *s.AmountCNY
		}
		if err = write("Account summary", row, []string{s.Username, s.Account, s.PlatformAccountID, submittedTime(s.LatestSubmittedAt), s.Plan, fmt.Sprint(s.RowCount), v, s.Status, s.Reason}); err != nil {
			return nil, err
		}
		if err = dateCell("Account summary", fmt.Sprintf("D%d", row), submittedTime(s.LatestSubmittedAt)); err != nil {
			return nil, err
		}
		if v != "" {
			if err = numeric("Account summary", fmt.Sprintf("G%d", row), v); err != nil {
				return nil, err
			}
			_ = f.SetCellStyle("Account summary", fmt.Sprintf("G%d", row), fmt.Sprintf("G%d", row), moneyStyle)
		}
	}
	totalRow := len(r.Summary) + 8
	if err = write("Account summary", totalRow, []string{"Filtered total (CNY)"}); err != nil {
		return nil, err
	}
	if r.TotalCNY != "" {
		if err = numeric("Account summary", fmt.Sprintf("G%d", totalRow), r.TotalCNY); err != nil {
			return nil, err
		}
	}
	_ = f.SetCellStyle("Account summary", fmt.Sprintf("G%d", totalRow), fmt.Sprintf("G%d", totalRow), moneyStyle)
	if err = setup("Bill details", r.Columns); err != nil {
		return nil, err
	}
	for i, row := range r.Details {
		if len(row) != 10 {
			return nil, ErrFormat
		}
		if err = write("Bill details", i+7, row); err != nil {
			return nil, err
		}
		if err = dateCell("Bill details", fmt.Sprintf("D%d", i+7), row[3]); err != nil {
			return nil, err
		}
		for c := 5; c < 10; c++ {
			cell, _ := excelize.CoordinatesToCellName(c+1, i+7)
			if c < 9 && len(row[c]) > 15 {
				continue
			}
			if err = numeric("Bill details", cell, row[c]); err != nil {
				return nil, err
			}
			if c == 9 {
				_ = f.SetCellStyle("Bill details", cell, cell, moneyStyle)
			}
		}
	}
	if err = setup("Needs review", []string{"Username", "Account", "Platform account ID", "Reason"}); err != nil {
		return nil, err
	}
	_ = f.SetColWidth("Needs review", "D", "D", 80)
	for i, v := range r.Review {
		if err = write("Needs review", i+7, []string{v.Username, v.Account, v.PlatformAccountID, v.Reason}); err != nil {
			return nil, err
		}
	}
	if len(r.Review) == 0 {
		if err = write("Needs review", 7, []string{"No accounts need review."}); err != nil {
			return nil, err
		}
	}
	b, err := f.WriteToBuffer()
	if err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
