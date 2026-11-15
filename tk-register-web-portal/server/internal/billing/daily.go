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
			row = &DailyRow{Model: model, InputTokens: "0", OutputTokens: "0", CacheReadTokens: "0", CacheWriteTokens: "0", CostCNY: "0"}
			grouped[id][model] = row
		}
		for _, pair := range []struct {
			dst *string
			src string
		}{{&row.InputTokens, line[4]}, {&row.OutputTokens, line[5]}, {&row.CacheReadTokens, line[6]}, {&row.CacheWriteTokens, line[7]}} {
			a, ok := new(big.Int).SetString(*pair.dst, 10)
			if !ok {
				return nil, ErrFormat
			}
			b, ok := new(big.Int).SetString(pair.src, 10)
			if !ok {
				return nil, ErrFormat
			}
			*pair.dst = a.Add(a, b).String()
			if len(*pair.dst) > 30 {
				return nil, ErrFormat
			}
		}
		a, _ := new(big.Rat).SetString(row.CostCNY)
		b, _ := new(big.Rat).SetString(line[8])
		row.CostCNY = amount(a.Add(a, b))
		if len(row.CostCNY) > 30 {
			return nil, ErrFormat
		}
	}
	for id, models := range grouped {
		for _, row := range models {
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
