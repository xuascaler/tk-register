package main

import (
	"context"
	"errors"
	"math"
	"math/big"
	"sort"

	"goapi-tk-register/server/internal/core"
)

// Read the saved setting for each request, including responses from an existing
// summary. Turning model visibility off never requires rebuilding the summary.
func (a *app) userModelCostsEnabled(ctx context.Context) (bool, error) {
	_, enabled, err := a.store.ModelUsageSettings(ctx)
	return enabled, err
}

func (a *app) modelUsageView(ctx context.Context, viewer *core.User, administrative, rawAccount bool) (context.Context, bool, error) {
	if rawAccount && viewer.IsSuperAdmin {
		return core.WithModelUsageSource(ctx, "raw"), true, nil
	}
	source, personal, err := a.store.ModelUsageSettings(ctx)
	if !viewer.IsSuperAdmin {
		source = "modified"
	}
	return core.WithModelUsageSource(ctx, source), administrative || personal, err
}

type dailyUsageTotal struct {
	Date             string  `json:"date"`
	CostCNY          string  `json:"cost_cny"`
	CostUSD          float64 `json:"cost_usd"`
	InputTokens      string  `json:"input_tokens"`
	OutputTokens     string  `json:"output_tokens"`
	CacheReadTokens  string  `json:"cache_read_tokens"`
	CacheWriteTokens string  `json:"cache_write_tokens"`
}

// Explicit outer fields shadow the embedded model fields during JSON encoding.
// Pointers distinguish an enabled, empty [] from a disabled, omitted field.
type personalOverviewResponse struct {
	core.AccountsOverview
	ModelCostsEnabled bool                      `json:"model_costs_enabled"`
	Rows              *[]core.DailyUsageRowDTO  `json:"rows,omitempty"`
	ModelCosts        *[]core.OverviewModelCost `json:"model_costs,omitempty"`
	DailyTotals       *[]dailyUsageTotal        `json:"daily_totals,omitempty"`
}

func personalOverview(out core.AccountsOverview, enabled bool) (personalOverviewResponse, error) {
	response := personalOverviewResponse{AccountsOverview: out, ModelCostsEnabled: enabled}
	if enabled {
		response.Rows, response.ModelCosts = &out.Rows, &out.ModelCosts
		return response, nil
	}
	totals, err := aggregateDailyTotals(out.Rows)
	if err != nil {
		return response, err
	}
	response.DailyTotals = &totals
	return response, nil
}

type personalDailyUsageResponse struct {
	core.DailyUsageDTO
	ModelCostsEnabled bool                     `json:"model_costs_enabled"`
	Rows              *[]core.DailyUsageRowDTO `json:"rows,omitempty"`
	DailyTotals       *[]dailyUsageTotal       `json:"daily_totals,omitempty"`
	TotalCostUSD      *float64                 `json:"total_cost_usd"`
}

func personalDailyUsage(out core.DailyUsageDTO, enabled bool) (personalDailyUsageResponse, error) {
	response := personalDailyUsageResponse{DailyUsageDTO: out, ModelCostsEnabled: enabled, TotalCostUSD: out.TotalCostUSD}
	if enabled {
		response.Rows = &out.Rows
		return response, nil
	}
	totals, err := aggregateDailyTotals(out.Rows)
	if err != nil {
		return response, err
	}
	response.DailyTotals = &totals
	return response, nil
}

// Token counters stay decimal strings throughout aggregation; float64 would
// silently lose counts above JavaScript's safe integer range.
func aggregateDailyTotals(rows []core.DailyUsageRowDTO) ([]dailyUsageTotal, error) {
	type accumulator struct {
		cost   *big.Rat
		cents  *big.Int
		tokens [4]*big.Int
	}
	days := map[string]*accumulator{}
	for _, row := range rows {
		day := days[row.UsageDate]
		if day == nil {
			day = &accumulator{cost: new(big.Rat), cents: new(big.Int), tokens: [4]*big.Int{new(big.Int), new(big.Int), new(big.Int), new(big.Int)}}
			days[row.UsageDate] = day
		}
		amount, valid := new(big.Rat).SetString(row.CostCNY)
		if !valid {
			return nil, errors.New("invalid daily billing amount")
		}
		day.cost.Add(day.cost, amount)
		day.cents.Add(day.cents, big.NewInt(int64(math.Round(row.CostUSD*100))))
		for i, raw := range []string{row.InputTokens, row.OutputTokens, row.CacheReadTokens, row.CacheWriteTokens} {
			if raw == "" {
				raw = "0"
			}
			value, valid := new(big.Int).SetString(raw, 10)
			if !valid {
				return nil, errors.New("invalid daily token count")
			}
			day.tokens[i].Add(day.tokens[i], value)
		}
	}
	keys := make([]string, 0, len(days))
	for date := range days {
		keys = append(keys, date)
	}
	sort.Strings(keys)
	totals := make([]dailyUsageTotal, 0, len(days))
	for _, date := range keys {
		day := days[date]
		if !day.cents.IsInt64() {
			return nil, errors.New("daily amount exceeds supported precision")
		}
		totals = append(totals, dailyUsageTotal{Date: date, CostCNY: day.cost.FloatString(8), CostUSD: float64(day.cents.Int64()) / 100, InputTokens: day.tokens[0].String(), OutputTokens: day.tokens[1].String(), CacheReadTokens: day.tokens[2].String(), CacheWriteTokens: day.tokens[3].String()})
	}
	return totals, nil
}
