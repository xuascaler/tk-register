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

	"github.com/xuri/excelize/v2"
)

const MaxRows = 50000
const maxInputBytes = 16 << 20
const maxUnzippedBytes = 64 << 20

var ErrFormat = errors.New("The supplier bill export has an unsupported format. No report was generated.")
var sourceColumns = []string{"邮箱", "平台账号ID", "模型", "输入Token", "输出Token", "缓存读Token", "缓存写Token", "消费(人民币/元)", "备注"}
var decimalPattern = regexp.MustCompile(`^[0-9]{1,15}(\.[0-9]{1,8})?$`)
var integerPattern = regexp.MustCompile(`^[0-9]{1,18}$`)

type Account struct {
	LocalAccountID    string
	Account           string
	Plan              string
	PlatformAccountID string
	SupplierRecordID  string
	RemoteID          string
	IdentityKey       string
}
type Summary struct {
	Account           string  `json:"account"`
	PlatformAccountID string  `json:"platform_account_id"`
	Plan              string  `json:"plan"`
	RowCount          int     `json:"row_count"`
	AmountCNY         *string `json:"amount_cny"`
	Status            string  `json:"status"`
	Reason            string  `json:"reason,omitempty"`
}
type Review struct {
	Account           string `json:"account"`
	PlatformAccountID string `json:"platform_account_id"`
	Reason            string `json:"reason"`
}
type Report struct {
	UserEmail string     `json:"user_email"`
	StartDate string     `json:"start_date"`
	EndDate   string     `json:"end_date"`
	Summary   []Summary  `json:"summary"`
	Details   [][]string `json:"details"`
	Columns   []string   `json:"columns"`
	Review    []Review   `json:"review"`
	TotalCNY  string     `json:"total_cny"`
	Currency  string     `json:"currency"`
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

func Filter(data []byte, accounts []Account) (Report, error) {
	out := Report{Summary: []Summary{}, Details: [][]string{}, Columns: append([]string(nil), sourceColumns[:8]...), Review: []Review{}, TotalCNY: "0.00", Currency: "CNY"}
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
	// Keep IDs distinct from submission IDs. No fuzzy email or alias fallback.
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
		if !validID(id) {
			conflicts[i] = "Platform account ID is missing or invalid."
			continue
		}
		if other, ok := idOwner[id]; ok {
			conflicts[i] = "Multiple local accounts share this platform account ID."
			conflicts[other] = conflicts[i]
		} else {
			idOwner[id] = i
		}
		if other, ok := emailOwner[email]; ok {
			conflicts[i] = "Multiple local account records share this email."
			conflicts[other] = conflicts[i]
		} else {
			emailOwner[email] = i
		}
	}
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
		out.Details = append(out.Details, row)
	}
	for i, a := range selected {
		s := Summary{Account: a.Account, PlatformAccountID: strings.TrimSpace(a.PlatformAccountID), Plan: a.Plan, RowCount: counts[i]}
		if reason := conflicts[i]; reason != "" {
			s.Status = "needs_review"
			s.Reason = reason
			out.Review = append(out.Review, Review{s.Account, s.PlatformAccountID, reason})
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
	numeric := func(sheet, cell, value string) error {
		if _, e := decimal(value); e != nil {
			return e
		}
		return f.SetCellDefault(sheet, cell, value)
	}
	setup := func(sheet string, headers []string) error {
		if e := write(sheet, 1, []string{"Claude bill export"}); e != nil {
			return e
		}
		if e := f.SetCellStyle(sheet, "A1", "A1", titleStyle); e != nil {
			return e
		}
		if e := write(sheet, 2, []string{"User", r.UserEmail}); e != nil {
			return e
		}
		if e := write(sheet, 3, []string{"Dates", r.StartDate + " — " + r.EndDate, "Timezone", "Asia/Shanghai"}); e != nil {
			return e
		}
		if e := write(sheet, 4, []string{"Scope", "Accounts owned by this user when the export was requested.", "Currency", "CNY"}); e != nil {
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
		if e := f.SetColWidth(sheet, "B", "B", 22); e != nil {
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
	if err = setup("Account summary", []string{"Account", "Platform account ID", "Plan", "Bill rows", "Total (CNY)", "Result", "Notes"}); err != nil {
		return nil, err
	}
	for i, s := range r.Summary {
		row := i + 7
		v := ""
		if s.AmountCNY != nil {
			v = *s.AmountCNY
		}
		if err = write("Account summary", row, []string{s.Account, s.PlatformAccountID, s.Plan, fmt.Sprint(s.RowCount), v, s.Status, s.Reason}); err != nil {
			return nil, err
		}
		if v != "" {
			if err = numeric("Account summary", fmt.Sprintf("E%d", row), v); err != nil {
				return nil, err
			}
			_ = f.SetCellStyle("Account summary", fmt.Sprintf("E%d", row), fmt.Sprintf("E%d", row), moneyStyle)
		}
	}
	totalRow := len(r.Summary) + 8
	if err = write("Account summary", totalRow, []string{"Filtered total (CNY)"}); err != nil {
		return nil, err
	}
	if err = numeric("Account summary", fmt.Sprintf("E%d", totalRow), r.TotalCNY); err != nil {
		return nil, err
	}
	_ = f.SetCellStyle("Account summary", fmt.Sprintf("E%d", totalRow), fmt.Sprintf("E%d", totalRow), moneyStyle)
	if err = setup("Bill details", r.Columns); err != nil {
		return nil, err
	}
	for i, row := range r.Details {
		if len(row) != 8 {
			return nil, ErrFormat
		}
		if err = write("Bill details", i+7, row); err != nil {
			return nil, err
		}
		for c := 3; c < 8; c++ {
			cell, _ := excelize.CoordinatesToCellName(c+1, i+7)
			if c < 7 && len(row[c]) > 15 {
				continue
			}
			if err = numeric("Bill details", cell, row[c]); err != nil {
				return nil, err
			}
			if c == 7 {
				_ = f.SetCellStyle("Bill details", cell, cell, moneyStyle)
			}
		}
	}
	if err = setup("Needs review", []string{"Account", "Platform account ID", "Reason"}); err != nil {
		return nil, err
	}
	_ = f.SetColWidth("Needs review", "C", "C", 80)
	for i, v := range r.Review {
		if err = write("Needs review", i+7, []string{v.Account, v.PlatformAccountID, v.Reason}); err != nil {
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
