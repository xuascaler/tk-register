package billing

import (
	"math/big"
	"sort"
)

// Daily rows must come from a single-day export. Cumulative observations cannot
// establish per-model daily usage, and are never used as a fallback here.
type DailyRow struct {
	Model            string `json:"model"`
	InputTokens      string `json:"input_tokens"`
	OutputTokens     string `json:"output_tokens"`
	CacheReadTokens  string `json:"cache_read_tokens"`
	CacheWriteTokens string `json:"cache_write_tokens"`
	CostCNY          string `json:"cost_cny"`
}
type DailyAccount struct {
	LocalAccountID, IdentityKey, Error string
	Rows                               []DailyRow
}

func newDailyRow(model string) *DailyRow {
	return &DailyRow{Model: model, InputTokens: "0", OutputTokens: "0", CacheReadTokens: "0", CacheWriteTokens: "0", CostCNY: "0"}
}

func (row *DailyRow) add(other DailyRow) error {
	for _, counter := range []struct {
		dst *string
		src string
	}{{&row.InputTokens, other.InputTokens}, {&row.OutputTokens, other.OutputTokens}, {&row.CacheReadTokens, other.CacheReadTokens}, {&row.CacheWriteTokens, other.CacheWriteTokens}} {
		total, _ := new(big.Int).SetString(*counter.dst, 10)
		n, ok := new(big.Int).SetString(counter.src, 10)
		if !ok || n.Sign() < 0 {
			return ErrFormat
		}
		*counter.dst = total.Add(total, n).String()
	}
	total, _ := new(big.Rat).SetString(row.CostCNY)
	value, ok := new(big.Rat).SetString(other.CostCNY)
	if !ok || value.Sign() < 0 {
		return ErrFormat
	}
	row.CostCNY = amount(total.Add(total, value))
	return nil
}

func Daily(data []byte, accounts []Account) ([]DailyAccount, error) {
	report, err := Filter(data, accounts)
	if err != nil {
		return nil, err
	}
	byID := map[string][]DailyRow{}
	// Combine duplicate date/model lines exactly (including counters > 2^53).
	grouped := map[string]map[string]*DailyRow{}
	for _, line := range report.Details {
		id, model := line[1], line[3]
		if len(model) > 254 {
			return nil, ErrFormat
		}
		if grouped[id] == nil {
			grouped[id] = map[string]*DailyRow{}
		}
		row := grouped[id][model]
		if row == nil {
			row = newDailyRow(model)
			grouped[id][model] = row
		}
		if err := row.add(DailyRow{InputTokens: line[4], OutputTokens: line[5], CacheReadTokens: line[6], CacheWriteTokens: line[7], CostCNY: line[8]}); err != nil {
			return nil, err
		}
	}
	for id, models := range grouped {
		for _, row := range models {
			if len(row.CostCNY) > 30 || len(row.InputTokens) > 30 || len(row.OutputTokens) > 30 || len(row.CacheReadTokens) > 30 || len(row.CacheWriteTokens) > 30 {
				return nil, ErrFormat
			}
			byID[id] = append(byID[id], *row)
		}
		sort.Slice(byID[id], func(i, j int) bool { return byID[id][i].Model < byID[id][j].Model })
	}
	out := make([]DailyAccount, 0, len(accounts))
	// Filter preserves input order after local identity deduplication.
	seen := map[string]bool{}
	index := 0
	for _, a := range accounts {
		key := a.LocalAccountID + ":" + a.IdentityKey
		if seen[key] {
			continue
		}
		seen[key] = true
		summary := report.Summary[index]
		index++
		result := DailyAccount{LocalAccountID: a.LocalAccountID, IdentityKey: a.IdentityKey, Rows: []DailyRow{}}
		if summary.Status == "needs_review" {
			result.Error = summary.Reason
		} else {
			result.Rows = append(result.Rows, byID[summary.PlatformAccountID]...)
		}
		out = append(out, result)
	}
	return out, nil
}
