package billing

import (
	"math/big"
	"sort"
)

// StoredUsageRow belongs to a successful day for a frozen export account and identity.
type StoredUsageRow struct {
	LocalAccountID string
	UsageDate      string
	DailyRow
}

// StoredUsageReport uses the same account/model period layout as the supplier
// workbook. Only persisted model lines count; a recorded zero still gets a row.
func StoredUsageReport(accounts []Account, rows []StoredUsageRow) (Report, error) {
	out := emptyReport()
	out.TotalCNY = ""
	if len(rows) > MaxRows {
		return out, ErrFormat
	}
	accounts, _, _, conflicts := selectAccounts(accounts, false)
	byAccount := make(map[string]map[string]*DailyRow, len(accounts))
	for _, account := range accounts {
		byAccount[account.LocalAccountID] = make(map[string]*DailyRow)
	}
	for _, row := range rows {
		models, ok := byAccount[row.LocalAccountID]
		if !ok || row.Model == "" {
			return out, ErrFormat
		}
		value := models[row.Model]
		if value == nil {
			value = newDailyRow(row.Model)
			models[row.Model] = value
		}
		if err := value.add(row.DailyRow); err != nil {
			return out, err
		}
	}
	grandTotal := new(big.Rat)
	for i, account := range accounts {
		models := byAccount[account.LocalAccountID]
		if len(models) == 0 {
			continue
		}
		if reason := conflicts[i]; reason != "" {
			out.Summary = append(out.Summary, Summary{Username: account.Username, Account: account.Account, PlatformAccountID: account.PlatformAccountID, LatestSubmittedAt: account.LatestSubmittedAt, Plan: account.Plan, Status: "needs_review", Reason: reason})
			out.Review = append(out.Review, Review{Username: account.Username, Account: account.Account, PlatformAccountID: account.PlatformAccountID, Reason: reason})
			continue
		}
		names := make([]string, 0, len(models))
		for name := range models {
			names = append(names, name)
		}
		sort.Strings(names)
		subtotal := new(big.Rat)
		for _, name := range names {
			row := models[name]
			value, _ := new(big.Rat).SetString(row.CostCNY)
			subtotal.Add(subtotal, value)
			out.Details = append(out.Details, []string{account.Username, account.Account, account.PlatformAccountID, submittedTime(account.LatestSubmittedAt), name, row.InputTokens, row.OutputTokens, row.CacheReadTokens, row.CacheWriteTokens, row.CostCNY})
		}
		cost := amount(subtotal)
		out.Summary = append(out.Summary, Summary{Username: account.Username, Account: account.Account, PlatformAccountID: account.PlatformAccountID, LatestSubmittedAt: account.LatestSubmittedAt, Plan: account.Plan, RowCount: len(models), AmountCNY: &cost, Status: "matched"})
		grandTotal.Add(grandTotal, subtotal)
	}
	if len(out.Details) > 0 {
		out.TotalCNY = amount(grandTotal)
	}
	return out, nil
}
