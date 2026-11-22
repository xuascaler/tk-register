package core

import (
	"context"
	"database/sql"
	"errors"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gorm.io/gorm"
)

const MaxDailyUsageBatchAccounts = 100

// These public types intentionally contain neither internal row IDs nor a data
// source selector. Model fields are omitted entirely when visibility is off.
type DailyUsageBatchModelDTO struct {
	Model   string `json:"model"`
	CostCNY string `json:"cost_cny"`
	CostUSD string `json:"cost_usd"`
}

type DailyUsageBatchAccountDTO struct {
	Account string                     `json:"account"`
	Status  string                     `json:"status"`
	CostCNY *string                    `json:"cost_cny"`
	CostUSD *string                    `json:"cost_usd"`
	Models  *[]DailyUsageBatchModelDTO `json:"models,omitempty"`
}

type DailyUsageBatchSummaryDTO struct {
	Status            string  `json:"status"`
	RequestedAccounts int     `json:"requested_accounts"`
	AvailableAccounts int     `json:"available_accounts"`
	NoDataAccounts    int     `json:"no_data_accounts"`
	NotFoundAccounts  int     `json:"not_found_accounts"`
	CostCNY           *string `json:"cost_cny"`
	CostUSD           *string `json:"cost_usd"`
}

type DailyUsageBatchDTO struct {
	Date              string                      `json:"date"`
	Timezone          string                      `json:"timezone"`
	ExchangeRate      string                      `json:"exchange_rate_cny_per_usd"`
	ModelCostsEnabled bool                        `json:"model_costs_enabled"`
	Summary           DailyUsageBatchSummaryDTO   `json:"summary"`
	Accounts          []DailyUsageBatchAccountDTO `json:"accounts"`
	ModelTotals       *[]DailyUsageBatchModelDTO  `json:"model_totals,omitempty"`
}

// AccountDailyUsageBatch reads only the caller's directly owned Claude accounts.
// The HTTP layer authenticates the caller, validates the date is complete, and
// supplies the effective model visibility setting. All ownership and money are
// read from one snapshot; reading never initiates collection or revises a bill.
func (s *Store) AccountDailyUsageBatch(ctx context.Context, userID uint64, vendorCode string, accounts []string, date string, modelDetails bool) (DailyUsageBatchDTO, error) {
	var out DailyUsageBatchDTO
	if userID == 0 {
		return out, ErrNotFound
	}
	day, err := time.ParseInLocation("2006-01-02", date, dailyZone)
	if err != nil || day.Format("2006-01-02") != date {
		return out, validationErrorf("enter a date in YYYY-MM-DD format")
	}
	// Keep this boundary safe for callers other than HTTP as well. Deduplication
	// happens before the size check and preserves the first occurrence's order.
	names := make([]string, 0, len(accounts))
	seen := make(map[string]bool, len(accounts))
	for _, input := range accounts {
		if !utf8.ValidString(input) {
			return out, validationErrorf("enter valid account names")
		}
		name := strings.ToLower(strings.TrimSpace(input))
		if name == "" || len(name) > 254 || strings.ContainsFunc(name, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
			return out, validationErrorf("enter valid account names")
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	if len(names) == 0 || len(names) > MaxDailyUsageBatchAccounts {
		return out, validationErrorf("choose between 1 and 100 unique accounts")
	}
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		scoped := *s
		scoped.DB = tx
		var readErr error
		out, readErr = scoped.readAccountDailyUsageBatch(ctx, userID, vendorCode, names, date, modelDetails)
		return readErr
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: s.DB.Dialector.Name() != "sqlite"})
	return out, err
}

func (s *Store) readAccountDailyUsageBatch(ctx context.Context, userID uint64, vendorCode string, names []string, date string, modelDetails bool) (DailyUsageBatchDTO, error) {
	out := DailyUsageBatchDTO{
		Date: date, Timezone: "Asia/Shanghai", ModelCostsEnabled: modelDetails,
		Summary:  DailyUsageBatchSummaryDTO{Status: "no_data", RequestedAccounts: len(names)},
		Accounts: make([]DailyUsageBatchAccountDTO, 0, len(names)),
	}
	if modelDetails {
		models := []DailyUsageBatchModelDTO{}
		out.ModelTotals = &models
	}
	rate, err := s.ExchangeRate(ctx)
	if err != nil {
		return out, err
	}
	out.ExchangeRate = strconv.FormatFloat(rate, 'f', -1, 64)
	exchange, valid := new(big.Rat).SetString(out.ExchangeRate)
	if !valid || exchange.Sign() <= 0 {
		return out, errors.New("invalid exchange rate")
	}
	var vendor Vendor
	if err := s.DB.WithContext(ctx).Select("id").Where("code = ?", vendorCode).First(&vendor).Error; err != nil && !errors.Is(err, ErrNotFound) {
		return out, err
	}
	keys := make([]string, len(names))
	for i, name := range names {
		keys[i] = accountDigest(idString(userID), idString(vendor.ID), name)
	}
	var owned []SyncedAccount
	if vendor.ID != 0 {
		if err := s.DB.WithContext(ctx).Select("id, account_key, plan").
			Where("account_key IN ? AND user_id = ? AND vendor_id = ? AND deleted_at IS NULL", keys, userID, vendor.ID).
			Find(&owned).Error; err != nil {
			return out, err
		}
	}
	byKey := make(map[string]uint64, len(owned))
	ids := make([]uint64, 0, len(owned))
	for _, account := range owned {
		if !strings.HasPrefix(account.Plan, "claude/") {
			continue
		}
		byKey[account.AccountKey] = account.ID
		ids = append(ids, account.ID)
	}
	type line struct {
		AccountID uint64
		Model     string
		CostCNY   string
	}
	var rows []line
	if len(ids) > 0 {
		// The table is fixed independently of global settings or context. Joining
		// the current identity excludes a previous lifetime's successful bills.
		err = s.DB.WithContext(ctx).Table("account_daily_usages_modifiled AS u").
			Joins("JOIN account_usage_days d ON d.id = u.day_id").
			Joins("JOIN synced_accounts a ON a.id = d.account_id AND a.identity_key = d.identity_key").
			Where("a.id IN ? AND a.user_id = ? AND a.vendor_id = ? AND a.deleted_at IS NULL AND d.usage_date = ? AND d.last_success_at IS NOT NULL", ids, userID, vendor.ID, date).
			Select("a.id AS account_id, u.model, u.cost_cny").Order("a.id, u.model").Limit(10001).Scan(&rows).Error
		if err != nil {
			return out, err
		}
		if len(rows) > 10000 {
			return out, errors.New("daily usage selection exceeds supported size")
		}
	}
	byAccount := make(map[uint64][]line, len(ids))
	for _, row := range rows {
		byAccount[row.AccountID] = append(byAccount[row.AccountID], row)
	}
	totalCNY, totalCents := new(big.Rat), new(big.Int)
	modelCNY, modelCents := map[string]*big.Rat{}, map[string]*big.Int{}
	for i, name := range names {
		item := DailyUsageBatchAccountDTO{Account: name, Status: "not_found"}
		if modelDetails {
			models := []DailyUsageBatchModelDTO{}
			item.Models = &models
		}
		id, found := byKey[keys[i]]
		if !found {
			out.Summary.NotFoundAccounts++
		} else if len(byAccount[id]) == 0 {
			item.Status = "no_data"
			out.Summary.NoDataAccounts++
		} else {
			accountRows := byAccount[id]
			amounts := make([]*big.Rat, len(accountRows))
			parts := make([]overviewMoneyPart, len(accountRows))
			accountCNY := new(big.Rat)
			for j, row := range accountRows {
				amount, ok := new(big.Rat).SetString(row.CostCNY)
				if !ok || !new(big.Rat).Mul(amount, big.NewRat(100_000_000, 1)).IsInt() {
					return out, errors.New("invalid daily bill amount")
				}
				amounts[j] = amount
				accountCNY.Add(accountCNY, amount)
				parts[j] = overviewMoneyPart{key: row.Model, amount: new(big.Rat).Mul(new(big.Rat).Quo(amount, exchange), big.NewRat(100, 1))}
			}
			accountCents := roundOverviewCents(new(big.Rat).Mul(new(big.Rat).Quo(accountCNY, exchange), big.NewRat(100, 1)))
			item.Status = "ok"
			cny, usd := accountCNY.FloatString(8), dailyUsageBatchUSD(accountCents)
			item.CostCNY, item.CostUSD = &cny, &usd
			out.Summary.AvailableAccounts++
			totalCNY.Add(totalCNY, accountCNY)
			totalCents.Add(totalCents, accountCents)
			if modelDetails {
				cents, err := allocateOverviewCents(parts, accountCents)
				if err != nil {
					return out, err
				}
				for j, row := range accountRows {
					value := big.NewInt(cents[j])
					*item.Models = append(*item.Models, DailyUsageBatchModelDTO{Model: row.Model, CostCNY: amounts[j].FloatString(8), CostUSD: dailyUsageBatchUSD(value)})
					if modelCNY[row.Model] == nil {
						modelCNY[row.Model], modelCents[row.Model] = new(big.Rat), new(big.Int)
					}
					modelCNY[row.Model].Add(modelCNY[row.Model], amounts[j])
					modelCents[row.Model].Add(modelCents[row.Model], value)
				}
			}
		}
		out.Accounts = append(out.Accounts, item)
	}
	if out.Summary.AvailableAccounts > 0 {
		out.Summary.Status = "partial"
		if out.Summary.AvailableAccounts == out.Summary.RequestedAccounts {
			out.Summary.Status = "complete"
		}
		cny, usd := totalCNY.FloatString(8), dailyUsageBatchUSD(totalCents)
		out.Summary.CostCNY, out.Summary.CostUSD = &cny, &usd
	}
	if modelDetails {
		names := make([]string, 0, len(modelCNY))
		for model := range modelCNY {
			names = append(names, model)
		}
		sort.Strings(names)
		for _, model := range names {
			*out.ModelTotals = append(*out.ModelTotals, DailyUsageBatchModelDTO{Model: model, CostCNY: modelCNY[model].FloatString(8), CostUSD: dailyUsageBatchUSD(modelCents[model])})
		}
	}
	return out, nil
}

func dailyUsageBatchUSD(cents *big.Int) string {
	return new(big.Rat).SetFrac(cents, big.NewInt(100)).FloatString(2)
}
