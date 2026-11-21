package core

import (
	"context"
	"errors"
	"math/big"
	"strconv"
)

type modelUsageSourceKey struct{}

// WithModelUsageSource binds the authorized view selected by the application to a read.
func WithModelUsageSource(ctx context.Context, source string) context.Context {
	if source != "raw" {
		source = "modified"
	}
	return context.WithValue(ctx, modelUsageSourceKey{}, source)
}

func reportSource(ctx context.Context) string {
	// Existing internal callers without a view read the original upstream records.
	if source, ok := ctx.Value(modelUsageSourceKey{}).(string); ok {
		return source
	}
	return "raw"
}

func usageLineTable(source string) string {
	if source == "modified" {
		return "account_daily_usages_modifiled"
	}
	return "account_daily_usages"
}

type DailyBillCost struct {
	cents     int64
	Date      string   `json:"date"`
	CostUSD   *float64 `json:"cost_usd"`
	Collected int64    `json:"collected"`
	Expected  int64    `json:"expected"`
}

type UsageBillTotals struct {
	ModelUsageSource string          `json:"model_usage_source"`
	TotalCostUSD     *float64        `json:"total_cost_usd"`
	TotalCostDays    int             `json:"total_cost_days"`
	DailyCosts       []DailyBillCost `json:"daily_costs"`
}

type upstreamPeriodCosts struct{ dates, sources map[string]*big.Rat }

func newUpstreamPeriodCosts() upstreamPeriodCosts {
	return upstreamPeriodCosts{dates: map[string]*big.Rat{}, sources: map[string]*big.Rat{}}
}

func addUsageCost(totals map[string]*big.Rat, key, raw string) error {
	amount, ok := new(big.Rat).SetString(raw)
	if !ok {
		return errors.New("invalid upstream billing amount")
	}
	if totals[key] == nil {
		totals[key] = new(big.Rat)
	}
	totals[key].Add(totals[key], amount)
	return nil
}

// Allocate daily and per-owner totals from the same unmodified bill. Model line
// coverage can be shorter than cost coverage during and after the cutover.
func usageCents(totals map[string]*big.Rat, rate float64) (map[string]int64, int64, error) {
	exchange, ok := new(big.Rat).SetString(strconv.FormatFloat(rate, 'f', -1, 64))
	if !ok || exchange.Sign() <= 0 {
		return nil, 0, errors.New("invalid exchange rate")
	}
	parts := make([]overviewMoneyPart, 0, len(totals))
	sum := new(big.Rat)
	for key, amount := range totals {
		cents := new(big.Rat).Mul(new(big.Rat).Quo(amount, exchange), big.NewRat(100, 1))
		parts = append(parts, overviewMoneyPart{key: key, amount: cents})
		sum.Add(sum, cents)
	}
	target := roundOverviewCents(sum)
	allocated, err := allocateOverviewCents(parts, target)
	if err != nil {
		return nil, 0, err
	}
	if !target.IsInt64() {
		return nil, 0, errors.New("billing total exceeds supported precision")
	}
	out := make(map[string]int64, len(parts))
	for i, p := range parts {
		out[p.key] = allocated[i]
	}
	return out, target.Int64(), nil
}

func (out *UsageBillTotals) setDailyCosts(costs map[string]*big.Rat, days []OverviewDay, rate float64) error {
	amounts, total, err := usageCents(costs, rate)
	if err != nil {
		return err
	}
	out.TotalCostDays = 0
	out.TotalCostUSD = nil
	out.DailyCosts = make([]DailyBillCost, 0, len(days))
	totalUSD := float64(total) / 100
	for _, d := range days {
		bill := DailyBillCost{Date: d.Date, Expected: d.Expected, Collected: d.Collected}
		if d.Collected > 0 {
			bill.cents = amounts[d.Date]
			v := float64(bill.cents) / 100
			bill.CostUSD = &v
			out.TotalCostDays++
			out.TotalCostUSD = &totalUSD
		}
		out.DailyCosts = append(out.DailyCosts, bill)
	}
	return nil
}

func (out *AccountsOverview) setUpstreamCosts(costs upstreamPeriodCosts, days []OverviewDay) error {
	if err := out.setDailyCosts(costs.dates, days, out.ExchangeRate); err != nil {
		return err
	}
	amounts, _, err := usageCents(costs.sources, out.ExchangeRate)
	if err != nil {
		return err
	}
	out.allModelSources = make(map[string]float64, len(out.Sources))
	for _, s := range out.Sources {
		if s.TotalCostDays > 0 || s.hasUpstreamBill {
			out.allModelSources[s.ID] = float64(amounts[s.ID]) / 100
		}
	}
	return nil
}

// Assign the displayed daily dollars across the model rows, keeping the
// original eight-decimal CNY amounts intact for subsequent calculations.
func (out *DailyUsageDTO) allocateModelCents() error {
	exchange, ok := new(big.Rat).SetString(strconv.FormatFloat(out.ExchangeRate, 'f', -1, 64))
	if !ok || exchange.Sign() <= 0 {
		return errors.New("invalid exchange rate")
	}
	byDate := map[string][]int{}
	for i, row := range out.Rows {
		byDate[row.UsageDate] = append(byDate[row.UsageDate], i)
	}
	for _, bill := range out.DailyCosts {
		indices := byDate[bill.Date]
		if len(indices) == 0 {
			continue
		}
		parts := make([]overviewMoneyPart, len(indices))
		for j, i := range indices {
			row := out.Rows[i]
			value, ok := new(big.Rat).SetString(row.CostCNY)
			if !ok {
				return errors.New("invalid model cost")
			}
			parts[j] = overviewMoneyPart{key: row.Model, amount: value.Mul(value.Quo(value, exchange), big.NewRat(100, 1))}
		}
		cents, err := allocateOverviewCents(parts, big.NewInt(bill.cents))
		if err != nil {
			return err
		}
		for j, i := range indices {
			out.Rows[i].CostUSD = float64(cents[j]) / 100
		}
	}
	return nil
}
