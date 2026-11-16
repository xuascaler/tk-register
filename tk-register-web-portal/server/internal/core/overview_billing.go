package core

import (
	"errors"
	"math"
	"math/big"
	"sort"
	"strconv"
)

// ProjectOverviewBilling gives every Overview monetary value the same selected
// billing-period basis. It runs after either the live query or the v1 cache read,
// before any model filter. Cached original CNY and account statistics stay intact.
// USD is reconciled to cents so displayed source, model and daily totals add up.
func ProjectOverviewBilling(out *AccountsOverview) error {
	rate, valid := new(big.Rat).SetString(strconv.FormatFloat(out.ExchangeRate, 'f', -1, 64))
	if !valid || rate.Sign() <= 0 {
		return errors.New("invalid overview exchange rate")
	}
	sources := make(map[string]int64, len(out.Sources))
	for _, source := range out.Sources {
		sources[source.ID] = source.CollectedDays
	}
	modelCNY := map[string]*big.Rat{}
	cellsByModel := map[string][]int{}
	toCents := func(amount *big.Rat) *big.Rat {
		return new(big.Rat).Mul(new(big.Rat).Quo(amount, rate), big.NewRat(100, 1))
	}
	cells := make([]overviewMoneyPart, len(out.ModelCosts))
	for i, cell := range out.ModelCosts {
		collected, found := sources[cell.SourceID]
		if !found || collected <= 0 {
			return errors.New("overview bill refers to an unavailable source")
		}
		amount, ok := new(big.Rat).SetString(cell.CostCNY)
		if !ok {
			return errors.New("invalid overview billing amount")
		}
		if modelCNY[cell.Model] == nil {
			modelCNY[cell.Model] = new(big.Rat)
		}
		modelCNY[cell.Model].Add(modelCNY[cell.Model], amount)
		cellsByModel[cell.Model] = append(cellsByModel[cell.Model], i)
		cells[i] = overviewMoneyPart{key: cell.SourceID, amount: toCents(amount)}
	}
	// First allocate the rounded grand total across models; then allocate each
	// model's target independently across sources and dates. Every level stays
	// within one cent of its exact amount, including net-zero/refund models.
	models := make([]overviewMoneyPart, 0, len(modelCNY))
	total := new(big.Rat)
	for model, amount := range modelCNY {
		cents := toCents(amount)
		models = append(models, overviewMoneyPart{key: model, amount: cents})
		total.Add(total, cents)
	}
	allocated, err := allocateOverviewCents(models, roundOverviewCents(total))
	if err != nil {
		return err
	}
	modelCents := map[string]*big.Int{}
	for i, model := range models {
		target := big.NewInt(allocated[i])
		modelCents[model.key] = target
		indices := cellsByModel[model.key]
		parts := make([]overviewMoneyPart, len(indices))
		for j, index := range indices {
			parts[j] = cells[index]
		}
		cents, e := allocateOverviewCents(parts, target)
		if e != nil {
			return e
		}
		for j, index := range indices {
			out.ModelCosts[index].CostUSD = float64(cents[j]) / 100
		}
	}
	rowsByModel := map[string][]int{}
	for i, row := range out.Rows {
		rowsByModel[row.Model] = append(rowsByModel[row.Model], i)
	}
	for model, indices := range rowsByModel {
		target, ok := modelCents[model]
		if !ok {
			return errors.New("overview daily bill has no model total")
		}
		parts := make([]overviewMoneyPart, len(indices))
		rowSum := new(big.Rat)
		for j, i := range indices {
			row := out.Rows[i]
			amount, valid := new(big.Rat).SetString(row.CostCNY)
			if !valid {
				return errors.New("invalid overview daily billing amount")
			}
			rowSum.Add(rowSum, amount)
			parts[j] = overviewMoneyPart{key: row.UsageDate, amount: toCents(amount)}
		}
		if rowSum.Cmp(modelCNY[model]) != 0 {
			return errors.New("overview daily and model billing totals do not match")
		}
		cents, e := allocateOverviewCents(parts, target)
		if e != nil {
			return e
		}
		for j, i := range indices {
			out.Rows[i].CostUSD = float64(cents[j]) / 100
		}
	}
	for model, cost := range modelCNY {
		if cost.Sign() != 0 && len(rowsByModel[model]) == 0 {
			return errors.New("overview model total has no daily bills")
		}
	}
	updateOverviewSourceBilling(out)
	return nil
}

// FilterOverviewBillingModels retains the all-model rounding allocation. Both
// frontend and server selections therefore return the same monetary values.
// nil selects all; an empty map selects none. Account counts/statuses are unchanged.
func FilterOverviewBillingModels(out *AccountsOverview, models map[string]bool) {
	if models != nil {
		rows := make([]DailyUsageRowDTO, 0, len(out.Rows))
		costs := make([]OverviewModelCost, 0, len(out.ModelCosts))
		for _, row := range out.Rows {
			if models[row.Model] {
				rows = append(rows, row)
			}
		}
		for _, cost := range out.ModelCosts {
			if models[cost.Model] {
				costs = append(costs, cost)
			}
		}
		out.Rows, out.ModelCosts = rows, costs
	}
	updateOverviewSourceBilling(out)
	if models != nil && len(models) == 0 {
		for i := range out.Sources {
			out.Sources[i].CostUSD = nil
		}
	}
}

func updateOverviewSourceBilling(out *AccountsOverview) {
	bySource := make(map[string]int64, len(out.Sources))
	for _, cell := range out.ModelCosts {
		bySource[cell.SourceID] += int64(math.Round(cell.CostUSD * 100))
	}
	for i := range out.Sources {
		source := &out.Sources[i]
		source.CostUSD = nil
		if source.CollectedDays > 0 {
			amount := float64(bySource[source.ID]) / 100
			source.CostUSD = &amount
		}
	}
}

type overviewMoneyPart struct {
	key    string
	amount *big.Rat
}

// Round half away from zero without binary floating point currency arithmetic.
func roundOverviewCents(amount *big.Rat) *big.Int {
	positive := new(big.Rat).Abs(amount)
	positive.Add(positive, big.NewRat(1, 2))
	result := new(big.Int).Quo(positive.Num(), positive.Denom())
	if amount.Sign() < 0 {
		result.Neg(result)
	}
	return result
}

// Largest remainder allocation, with stable keys breaking equal remainders.
// Negative adjustments/refunds work too: floor keeps each remainder in [0,1).
func allocateOverviewCents(parts []overviewMoneyPart, target *big.Int) ([]int64, error) {
	type remainder struct {
		index  int
		amount *big.Rat
	}
	out := make([]int64, len(parts))
	remaining := new(big.Int).Set(target)
	order := make([]remainder, len(parts))
	for i, part := range parts {
		floor, rest := new(big.Int), new(big.Int)
		floor.QuoRem(part.amount.Num(), part.amount.Denom(), rest)
		if rest.Sign() < 0 {
			floor.Sub(floor, big.NewInt(1))
		}
		if !floor.IsInt64() {
			return nil, errors.New("overview amount exceeds supported precision")
		}
		out[i] = floor.Int64()
		remaining.Sub(remaining, floor)
		order[i] = remainder{i, new(big.Rat).Sub(part.amount, new(big.Rat).SetInt(floor))}
	}
	if remaining.Sign() < 0 || !remaining.IsInt64() || remaining.Int64() > int64(len(parts)) {
		return nil, errors.New("overview money cannot be reconciled")
	}
	sort.SliceStable(order, func(i, j int) bool {
		if c := order[i].amount.Cmp(order[j].amount); c != 0 {
			return c > 0
		}
		return parts[order[i].index].key < parts[order[j].index].key
	})
	for i := int64(0); i < remaining.Int64(); i++ {
		if out[order[i].index] == math.MaxInt64 {
			return nil, errors.New("overview amount exceeds supported precision")
		}
		out[order[i].index]++
	}
	return out, nil
}
