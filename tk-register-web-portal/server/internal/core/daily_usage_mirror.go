package core

import (
	"errors"
	"math/big"
	"math/rand/v2"
	"sort"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DayID identifies a particular account identity and billing date. A row also
// records a successful empty bill, distinct from a day predating this feature.
type AccountDailyFableRatio struct {
	DayID      uint64          `gorm:"primaryKey;autoIncrement:false"`
	Day        AccountUsageDay `gorm:"foreignKey:DayID;constraint:OnDelete:RESTRICT"`
	FableRatio string          `gorm:"type:decimal(31,30);not null"`
}

// Token counts may grow when an inexpensive model receives a larger allocation.
type AccountDailyUsageModified struct {
	ID               uint64          `gorm:"primaryKey"`
	DayID            uint64          `gorm:"not null;uniqueIndex:idx_modified_daily_model,priority:1"`
	Day              AccountUsageDay `gorm:"foreignKey:DayID;constraint:OnDelete:RESTRICT"`
	Model            string          `gorm:"size:254;not null;uniqueIndex:idx_modified_daily_model,priority:2"`
	InputTokens      string          `gorm:"size:70;not null"`
	OutputTokens     string          `gorm:"size:70;not null"`
	CacheReadTokens  string          `gorm:"size:70;not null"`
	CacheWriteTokens string          `gorm:"size:70;not null"`
	CostCNY          string          `gorm:"type:decimal(30,8);not null"`
}

func (AccountDailyUsageModified) TableName() string { return "account_daily_usages_modifiled" }

type fableAllocationRules struct {
	trigger, minimum, maximum *big.Rat
}

func readFableRules(tx *gorm.DB) (fableAllocationRules, error) {
	keys := []string{"fable_trigger_percent", "fable_min_percent", "fable_max_percent"}
	var settings []Setting
	if err := tx.Select("key", "value").Where(clause.IN{Column: "key", Values: stringInterfaces(keys)}).Find(&settings).Error; err != nil {
		return fableAllocationRules{}, err
	}
	ratios := make(map[string]*big.Rat, len(keys))
	for _, row := range settings {
		percent, ok := new(big.Rat).SetString(row.Value)
		if !ok || percent.Sign() < 0 || percent.Cmp(big.NewRat(100, 1)) > 0 {
			return fableAllocationRules{}, errors.New("invalid Fable allocation percentage")
		}
		ratios[row.Key] = percent.Quo(percent, big.NewRat(100, 1))
	}
	rules := fableAllocationRules{ratios[keys[0]], ratios[keys[1]], ratios[keys[2]]}
	if len(ratios) != len(keys) || rules.minimum.Cmp(rules.trigger) >= 0 || rules.minimum.Cmp(rules.maximum) >= 0 {
		return fableAllocationRules{}, errors.New("invalid Fable allocation bounds")
	}
	return rules, nil
}

const mirrorIntervals int64 = 1_000_000

func saveDailyUsageMirror(tx *gorm.DB, dayID uint64, raw []AccountDailyUsage) error {
	rules, err := readFableRules(tx)
	if err != nil {
		return err
	}
	// Exclude both endpoints. Billing allocation does not require a cryptographic draw.
	slot := 1 + rand.Int64N(mirrorIntervals-1)
	ratio, rows, err := allocateDailyMirror(raw, rules, slot)
	if err != nil {
		return err
	}
	if err = tx.Create(&AccountDailyFableRatio{DayID: dayID, FableRatio: ratio.FloatString(30)}).Error; err != nil {
		return err
	}
	if len(rows) > 0 {
		return tx.CreateInBatches(rows, 100).Error
	}
	return nil
}

// Persisted money has eight fractional digits. Split integer units within each
// group so amounts tie out at database precision, even for very large bills.
func allocateDailyMirror(raw []AccountDailyUsage, rules fableAllocationRules, slot int64) (*big.Rat, []AccountDailyUsageModified, error) {
	const fable, others = 0, 1
	scale := big.NewRat(100_000_000, 1)
	subtotals := [2]*big.Int{new(big.Int), new(big.Int)}
	var groups [2][]mirrorCostPart
	rows := make([]AccountDailyUsageModified, len(raw))
	for i, row := range raw {
		rows[i] = AccountDailyUsageModified(row)
		rows[i].ID = 0
		amount, ok := new(big.Rat).SetString(row.CostCNY)
		if !ok || amount.Sign() < 0 || !amount.Mul(amount, scale).IsInt() {
			return nil, nil, errors.New("invalid daily bill amount")
		}
		if amount.Sign() == 0 {
			// A free model can still have usage. Its costs and tokens stay intact.
			continue
		}
		group := others
		for _, segment := range strings.FieldsFunc(strings.ToLower(row.Model), func(r rune) bool {
			return r == '-' || r == '_' || r == '/' || r == '.' || r == ':'
		}) {
			if segment == "fable" {
				group = fable
				break
			}
		}
		units := amount.Num()
		subtotals[group].Add(subtotals[group], units)
		groups[group] = append(groups[group], mirrorCostPart{index: i, units: units})
	}
	total := new(big.Int).Add(subtotals[fable], subtotals[others])
	if total.Sign() == 0 {
		return new(big.Rat), rows, nil
	}
	share := new(big.Rat).SetFrac(subtotals[fable], total)
	if subtotals[others].Sign() == 0 || share.Cmp(rules.trigger) <= 0 {
		return share, rows, nil
	}
	upper := rules.maximum
	if upper.Cmp(share) > 0 {
		upper = share
	}
	portion := big.NewRat(slot, mirrorIntervals)
	targetShare := new(big.Rat).Sub(upper, rules.minimum)
	targetShare.Mul(targetShare, portion).Add(targetShare, rules.minimum)
	// Apply the exact decimal that we store in the ratio table.
	targetShare.SetString(targetShare.FloatString(30))
	fableTarget := roundOverviewCents(new(big.Rat).Mul(new(big.Rat).SetInt(total), targetShare))
	targets := [2]*big.Int{fableTarget, new(big.Int).Sub(total, fableTarget)}
	for i, parts := range groups {
		if err := distributeMirrorGroup(parts, subtotals[i], targets[i], rows); err != nil {
			return nil, nil, err
		}
	}
	return targetShare, rows, nil
}

type mirrorCostPart struct {
	index     int
	units     *big.Int
	allocated *big.Int
	remainder *big.Int
}

func distributeMirrorGroup(parts []mirrorCostPart, subtotal, target *big.Int, rows []AccountDailyUsageModified) error {
	left := new(big.Int).Set(target)
	for i := range parts {
		part := &parts[i]
		part.allocated = new(big.Int).Mul(part.units, target)
		part.remainder = new(big.Int)
		part.allocated.QuoRem(part.allocated, subtotal, part.remainder)
		left.Sub(left, part.allocated)
	}
	sort.Slice(parts, func(i, j int) bool {
		if cmp := parts[i].remainder.Cmp(parts[j].remainder); cmp != 0 {
			return cmp > 0
		}
		return rows[parts[i].index].Model < rows[parts[j].index].Model
	})
	// The sum of the discarded fractions is an integer smaller than len(parts).
	for i := 0; i < int(left.Int64()); i++ {
		parts[i].allocated.Add(parts[i].allocated, big.NewInt(1))
	}
	for _, part := range parts {
		row := &rows[part.index]
		row.CostCNY = new(big.Rat).SetFrac(part.allocated, big.NewInt(100_000_000)).FloatString(8)
		ratio := new(big.Rat).SetFrac(part.allocated, part.units)
		for _, counter := range []*string{&row.InputTokens, &row.OutputTokens, &row.CacheReadTokens, &row.CacheWriteTokens} {
			value, ok := new(big.Int).SetString(*counter, 10)
			if !ok || value.Sign() < 0 {
				return errors.New("invalid daily bill token count")
			}
			scaled := new(big.Rat).Mul(new(big.Rat).SetInt(value), ratio)
			*counter = roundOverviewCents(scaled).String()
		}
	}
	return nil
}
