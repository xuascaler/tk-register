package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// A successful daily collection publishes all four ranges together for a user
// and view. These rows intentionally freeze ownership and account observations
// until the next scheduled build; request handlers must never rebuild them.
type OverviewSummary struct {
	ID           uint64    `gorm:"primaryKey"`
	UserID       uint64    `gorm:"not null;uniqueIndex:idx_overview_user_view_period,priority:1"`
	User         User      `gorm:"foreignKey:UserID;constraint:OnDelete:RESTRICT"`
	ViewType     string    `gorm:"size:16;not null;uniqueIndex:idx_overview_user_view_period,priority:2"`
	Period       string    `gorm:"size:16;not null;uniqueIndex:idx_overview_user_view_period,priority:3"`
	StartDate    string    `gorm:"size:10;not null"`
	EndDate      string    `gorm:"size:10;not null"`
	Timezone     string    `gorm:"size:64;not null"`
	PayloadJSON  string    `gorm:"type:longtext;not null"`
	BatchID      string    `gorm:"size:64;not null"`
	GeneratedAt  time.Time `gorm:"not null"`
	AccountsAsOf time.Time `gorm:"not null"`
}

func (s *Store) migrateOverviewSummaries() error { return s.DB.AutoMigrate(&OverviewSummary{}) }

var overviewSummaryPeriods = []string{"day", "week", "month", "all_time"}

func overviewViewType(administrative bool) string {
	if administrative {
		return "admin"
	}
	return "personal"
}

func overviewPeriodStart(period, end string, earliest *string) (string, error) {
	date, err := time.ParseInLocation("2006-01-02", end, dailyZone)
	if err != nil || date.Format("2006-01-02") != end {
		return "", errors.New("invalid overview end date")
	}
	switch period {
	case "day":
		return end, nil
	case "week":
		return date.AddDate(0, 0, -6).Format("2006-01-02"), nil
	case "month":
		return date.AddDate(0, 0, -29).Format("2006-01-02"), nil
	case "all_time":
		if earliest != nil && *earliest < end {
			return *earliest, nil
		}
		return end, nil
	default:
		return "", &ValidationError{Message: "Choose day, week, month, or all_time."}
	}
}

// Payloads contain original CNY values and integer counters as decimal strings.
// No converted USD amount is persisted or used to reconstruct an original cost.
type overviewSummarySource struct {
	AccountSource
	Total         int64            `json:"total"`
	Statuses      map[string]int64 `json:"statuses"`
	CostCNY       *string          `json:"cost_cny"`
	CostKnown     int64            `json:"cost_known"`
	CollectedDays int64            `json:"collected_days"`
}
type overviewSummaryUsage struct {
	UsageDate        string `json:"usage_date"`
	Model            string `json:"model"`
	CostCNY          string `json:"cost_cny"`
	InputTokens      string `json:"input_tokens"`
	OutputTokens     string `json:"output_tokens"`
	CacheReadTokens  string `json:"cache_read_tokens"`
	CacheWriteTokens string `json:"cache_write_tokens"`
}
type overviewSummaryModelCost struct {
	SourceID string `json:"source_id"`
	Model    string `json:"model"`
	CostCNY  string `json:"cost_cny"`
}
type overviewSummaryPayload struct {
	Version           int                        `json:"version"`
	EarliestDate      *string                    `json:"earliest_date"`
	LatestAccountData *time.Time                 `json:"latest_account_data"`
	Sources           []overviewSummarySource    `json:"sources"`
	Rows              []overviewSummaryUsage     `json:"rows"`
	ModelCosts        []overviewSummaryModelCost `json:"model_costs"`
	Days              []OverviewDay              `json:"days"`
}

type overviewSummaryAccount struct {
	Source              uint64
	Status              string
	Plan                string
	CurrentSubmissionID *uint64
	CreatedAt           time.Time
	LastSuccessAt       *time.Time
	Cost                sql.NullString
}
type overviewSummaryCollection struct {
	Source uint64
	Date   string
	Count  int64
}
type overviewSummaryUsageKey struct{ Source, Date, Model string }
type overviewSummaryInput struct {
	sources     []overviewSummarySource
	accounts    []overviewSummaryAccount
	collections []overviewSummaryCollection
	usage       map[overviewSummaryUsageKey]*overviewExactUsage
	earliest    *string
	latest      *time.Time
}

// Exact accumulation also avoids SQLite's integer overflow/float coercion in
// tests and preserves counters larger than JavaScript's safe integer range.
type overviewExactUsage struct {
	cost                                 big.Rat
	input, output, cacheRead, cacheWrite big.Int
}

func (u *overviewExactUsage) add(row overviewSummaryUsage) error {
	cost, valid := new(big.Rat).SetString(row.CostCNY)
	if !valid {
		return errors.New("invalid saved overview CNY amount")
	}
	u.cost.Add(&u.cost, cost)
	for _, pair := range []struct {
		dst   *big.Int
		value string
	}{{&u.input, row.InputTokens}, {&u.output, row.OutputTokens}, {&u.cacheRead, row.CacheReadTokens}, {&u.cacheWrite, row.CacheWriteTokens}} {
		value, ok := new(big.Int).SetString(pair.value, 10)
		if !ok || value.Sign() < 0 {
			return errors.New("invalid saved overview token count")
		}
		pair.dst.Add(pair.dst, value)
	}
	return nil
}
func overviewDecimal(value *big.Rat) (string, error) {
	denom := new(big.Int).Set(value.Denom())
	two, five, remainder := big.NewInt(2), big.NewInt(5), new(big.Int)
	scale := 0
	for _, prime := range []*big.Int{two, five} {
		count := 0
		for {
			remainder.Mod(denom, prime)
			if remainder.Sign() != 0 {
				break
			}
			denom.Quo(denom, prime)
			count++
		}
		if count > scale {
			scale = count
		}
	}
	if denom.Cmp(big.NewInt(1)) != 0 {
		return "", errors.New("non-decimal saved overview amount")
	}
	result := value.FloatString(scale)
	if strings.Contains(result, ".") {
		result = strings.TrimRight(strings.TrimRight(result, "0"), ".")
	}
	if result == "-0" {
		result = "0"
	}
	return result, nil
}
func (u *overviewExactUsage) row(date, model string) (overviewSummaryUsage, error) {
	cost, err := overviewDecimal(&u.cost)
	return overviewSummaryUsage{UsageDate: date, Model: model, CostCNY: cost, InputTokens: u.input.String(), OutputTokens: u.output.String(), CacheReadTokens: u.cacheRead.String(), CacheWriteTokens: u.cacheWrite.String()}, err
}

func (s *Store) loadOverviewSummaryInput(ctx context.Context, scope overviewScope, end string) (overviewSummaryInput, error) {
	out := overviewSummaryInput{sources: []overviewSummarySource{}, usage: map[overviewSummaryUsageKey]*overviewExactUsage{}}
	bySource := map[string]int{}
	costs := map[string]*big.Rat{}
	for _, source := range scope.sources {
		bySource[source.ID] = len(out.sources)
		out.sources = append(out.sources, overviewSummarySource{AccountSource: source, Statuses: map[string]int64{}})
	}
	// The original cumulative observation is read directly, never from CostUSD.
	err := scope.base().Joins("LEFT JOIN account_snapshots p ON p.id = (SELECT x.id FROM account_snapshots x WHERE x.account_id = a.id AND x.identity_key = a.identity_key ORDER BY x.collected_at DESC, x.id DESC LIMIT 1)").Select(scope.sourceExpr + " AS source, COALESCE(NULLIF(a.snapshot_status,''),'unknown') AS status, a.plan, a.current_submission_id, a.created_at, a.last_success_at, CASE WHEN p.cost_total IS NOT NULL AND UPPER(TRIM(p.currency))='CNY' THEN p.cost_total ELSE NULL END AS cost").Scan(&out.accounts).Error
	if err != nil {
		return out, err
	}
	for _, account := range out.accounts {
		id := overviewSourceID(account.Source)
		i, ok := bySource[id]
		if !ok {
			return out, errors.New("overview account source was not selected")
		}
		source := &out.sources[i]
		source.Total++
		source.Statuses[account.Status]++
		if account.LastSuccessAt != nil && (out.latest == nil || account.LastSuccessAt.After(*out.latest)) {
			stamp := *account.LastSuccessAt
			out.latest = &stamp
		}
		if account.Cost.Valid {
			value, valid := new(big.Rat).SetString(account.Cost.String)
			if !valid {
				return out, errors.New("invalid cumulative CNY amount")
			}
			if costs[id] == nil {
				costs[id] = new(big.Rat)
			}
			costs[id].Add(costs[id], value)
			source.CostKnown++
		}
	}
	for id, cost := range costs {
		raw, err := overviewDecimal(cost)
		if err != nil {
			return out, err
		}
		out.sources[bySource[id]].CostCNY = &raw
	}
	days := func() *gorm.DB {
		return scope.base().Joins("JOIN account_usage_days d ON d.account_id=a.id AND d.identity_key=a.identity_key").Where("d.last_success_at IS NOT NULL AND d.usage_date <= ?", end)
	}
	if err = days().Select(scope.sourceExpr + " AS source, d.usage_date AS date, COUNT(*) AS count").Group(scope.sourceExpr + ", d.usage_date").Order("d.usage_date ASC").Scan(&out.collections).Error; err != nil {
		return out, err
	}
	if len(out.collections) > 0 {
		first := out.collections[0].Date
		out.earliest = &first
	}
	// Stream bill lines once. Only aggregated source/date/model values stay in
	// memory; deriving day/week/month/all_time does not rescan account tables.
	rows, err := days().Joins("JOIN account_daily_usages u ON u.day_id=d.id").Select(scope.sourceExpr + " AS source, d.usage_date, u.model, u.cost_cny, u.input_tokens, u.output_tokens, u.cache_read_tokens, u.cache_write_tokens").Rows()
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var source uint64
		var row overviewSummaryUsage
		if err = rows.Scan(&source, &row.UsageDate, &row.Model, &row.CostCNY, &row.InputTokens, &row.OutputTokens, &row.CacheReadTokens, &row.CacheWriteTokens); err != nil {
			return out, err
		}
		key := overviewSummaryUsageKey{overviewSourceID(source), row.UsageDate, row.Model}
		if out.usage[key] == nil {
			out.usage[key] = &overviewExactUsage{}
		}
		if err = out.usage[key].add(row); err != nil {
			return out, err
		}
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	return out, ctx.Err()
}

func (input overviewSummaryInput) period(start, end string) (overviewSummaryPayload, error) {
	out := overviewSummaryPayload{Version: 1, EarliestDate: input.earliest, LatestAccountData: input.latest, Sources: append([]overviewSummarySource{}, input.sources...), Rows: []overviewSummaryUsage{}, ModelCosts: []overviewSummaryModelCost{}, Days: []OverviewDay{}}
	bySource := map[string]int{}
	for i, source := range out.Sources {
		bySource[source.ID] = i
	}
	coverage := map[string]int64{}
	for _, collection := range input.collections {
		if collection.Date < start || collection.Date > end {
			continue
		}
		coverage[collection.Date] += collection.Count
		out.Sources[bySource[overviewSourceID(collection.Source)]].CollectedDays += collection.Count
	}
	rows := map[overviewSummaryUsageKey]*overviewExactUsage{}
	costs := map[overviewSummaryUsageKey]*big.Rat{}
	for key, value := range input.usage {
		if key.Date < start || key.Date > end {
			continue
		}
		row, err := value.row(key.Date, key.Model)
		if err != nil {
			return out, err
		}
		dailyKey := overviewSummaryUsageKey{Date: key.Date, Model: key.Model}
		if rows[dailyKey] == nil {
			rows[dailyKey] = &overviewExactUsage{}
		}
		if err = rows[dailyKey].add(row); err != nil {
			return out, err
		}
		modelKey := overviewSummaryUsageKey{Source: key.Source, Model: key.Model}
		if costs[modelKey] == nil {
			costs[modelKey] = new(big.Rat)
		}
		costs[modelKey].Add(costs[modelKey], &value.cost)
	}
	for key, value := range rows {
		row, err := value.row(key.Date, key.Model)
		if err != nil {
			return out, err
		}
		out.Rows = append(out.Rows, row)
	}
	sort.Slice(out.Rows, func(i, j int) bool {
		if out.Rows[i].UsageDate != out.Rows[j].UsageDate {
			return out.Rows[i].UsageDate < out.Rows[j].UsageDate
		}
		return out.Rows[i].Model < out.Rows[j].Model
	})
	for key, cost := range costs {
		raw, err := overviewDecimal(cost)
		if err != nil {
			return out, err
		}
		out.ModelCosts = append(out.ModelCosts, overviewSummaryModelCost{SourceID: key.Source, Model: key.Model, CostCNY: raw})
	}
	sort.Slice(out.ModelCosts, func(i, j int) bool {
		if out.ModelCosts[i].SourceID != out.ModelCosts[j].SourceID {
			return out.ModelCosts[i].SourceID < out.ModelCosts[j].SourceID
		}
		return out.ModelCosts[i].Model < out.ModelCosts[j].Model
	})
	cohorts := []string{}
	for _, account := range input.accounts {
		if account.CurrentSubmissionID != nil && strings.HasPrefix(account.Plan, "claude/") {
			cohorts = append(cohorts, account.CreatedAt.In(dailyZone).Format("2006-01-02"))
		}
	}
	sort.Strings(cohorts)
	first, _ := time.ParseInLocation("2006-01-02", start, dailyZone)
	last, _ := time.ParseInLocation("2006-01-02", end, dailyZone)
	var expected int
	for day := first; !day.After(last); day = day.AddDate(0, 0, 1) {
		date := day.Format("2006-01-02")
		for expected < len(cohorts) && cohorts[expected] <= date {
			expected++
		}
		status := "missing"
		if coverage[date] > 0 {
			status = "complete"
		}
		out.Days = append(out.Days, OverviewDay{Date: date, Status: status, Expected: int64(expected), Collected: coverage[date]})
	}
	return out, nil
}

// OverviewSummaryClaim fences publication against an expired daily-worker lease.
// The optional form permits deterministic offline rebuild tests; production
// workers always supply the claim returned by ClaimOverviewSummaryJob.
type OverviewSummaryClaim struct {
	JobID uint64
	Token string
}

// BuildOverviewSummaries is called only by the daily-collection worker. A
// failed build or cancelled context preserves the previous four-row batch.
func (s *Store) BuildOverviewSummaries(ctx context.Context, viewerID uint64, administrative bool, usageDate, batchID string, now time.Time, claims ...OverviewSummaryClaim) error {
	started := time.Now()
	if viewerID == 0 || len(batchID) == 0 || len(batchID) > 64 || len(claims) > 1 {
		return errors.New("invalid overview build identity")
	}
	if len(claims) == 1 && (claims[0].JobID == 0 || claims[0].Token == "") {
		return errors.New("invalid overview build claim")
	}
	if err := ValidateOverviewRange(usageDate, usageDate, now); err != nil {
		return err
	}
	var input overviewSummaryInput
	// A consistent read captures ownership, state and successful bill lines from
	// one database snapshot, without holding write locks while aggregating.
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		scoped := *s
		scoped.DB = tx
		var scope overviewScope
		var err error
		if administrative {
			scope, err = scoped.adminOverviewScope(ctx, viewerID, "all")
		} else {
			scope, err = scoped.personalOverviewScope(ctx, viewerID, "all")
		}
		if err != nil {
			return err
		}
		input, err = scoped.loadOverviewSummaryInput(ctx, scope, usageDate)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return err
	}
	records := make([]OverviewSummary, 0, 4)
	for _, period := range overviewSummaryPeriods {
		if err = ctx.Err(); err != nil {
			return err
		}
		start, err := overviewPeriodStart(period, usageDate, input.earliest)
		if err != nil {
			return err
		}
		if err = ValidateOverviewRange(start, usageDate, now); err != nil {
			return err
		}
		payload, err := input.period(start, usageDate)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		records = append(records, OverviewSummary{UserID: viewerID, ViewType: overviewViewType(administrative), Period: period, StartDate: start, EndDate: usageDate, Timezone: "Asia/Shanghai", PayloadJSON: string(raw), BatchID: batchID, GeneratedAt: now.UTC(), AccountsAsOf: now.UTC()})
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		publishedAt := now.Add(time.Since(started)).UTC()
		if len(claims) == 1 {
			var job DailyUsageJob
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&job, claims[0].JobID).Error; err != nil {
				return err
			}
			if job.Status != "succeeded" || job.OverviewState != "running" || job.OverviewRunToken != claims[0].Token || job.OverviewLeaseUntil == nil || !job.OverviewLeaseUntil.After(publishedAt) || job.UsageDate != usageDate {
				return errors.New("overview build claim is no longer active")
			}
		}
		// Serialize publication for this user, including the first four inserts.
		// A late recovery job can never replace a newer successful daily batch.
		var user User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").First(&user, viewerID).Error; err != nil {
			return err
		}
		var newer int64
		if err := tx.Model(&OverviewSummary{}).Where("user_id = ? AND view_type = ? AND end_date > ?", viewerID, overviewViewType(administrative), usageDate).Count(&newer).Error; err != nil {
			return err
		}
		if newer > 0 {
			return errors.New("a newer overview summary is already published")
		}
		for i := range records {
			records[i].GeneratedAt = publishedAt
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}, {Name: "view_type"}, {Name: "period"}}, DoUpdates: clause.AssignmentColumns([]string{"start_date", "end_date", "timezone", "payload_json", "batch_id", "generated_at", "accounts_as_of"})}).Omit("User").Create(&records).Error
	})
}

func overviewUSD(value string, rate float64) (float64, error) {
	amount, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(amount) || math.IsInf(amount, 0) {
		return 0, errors.New("invalid saved overview amount")
	}
	return amount / rate, nil
}

// ReadOverviewSummary only reads this table and the current exchange rate. A
// missing summary is an explicit empty result, never an on-demand rebuild.
func (s *Store) ReadOverviewSummary(ctx context.Context, viewerID uint64, administrative bool, period string, now time.Time) (AccountsOverview, error) {
	end := now.In(dailyZone).AddDate(0, 0, -1).Format("2006-01-02")
	start, err := overviewPeriodStart(period, end, nil)
	if err != nil {
		return AccountsOverview{}, err
	}
	available := false
	out := AccountsOverview{StartDate: start, EndDate: end, Timezone: "Asia/Shanghai", CollectionTime: "03:00", SummaryAvailable: &available, SummaryPeriod: period, Sources: []OverviewSource{}, Rows: []DailyUsageRowDTO{}, ModelCosts: []OverviewModelCost{}, Days: []OverviewDay{}}
	out.ExchangeRate, err = s.ExchangeRate(ctx)
	if err != nil {
		return out, err
	}
	var saved OverviewSummary
	err = s.DB.WithContext(ctx).Where("user_id = ? AND view_type = ? AND period = ?", viewerID, overviewViewType(administrative), period).First(&saved).Error
	if errors.Is(err, ErrNotFound) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	var payload overviewSummaryPayload
	if err = json.Unmarshal([]byte(saved.PayloadJSON), &payload); err != nil {
		return out, err
	}
	if payload.Version != 1 {
		return out, fmt.Errorf("unsupported overview summary version %d", payload.Version)
	}
	available = true
	generated := saved.GeneratedAt.UTC()
	out.SummaryGeneratedAt = &generated
	out.StartDate, out.EndDate, out.Timezone = saved.StartDate, saved.EndDate, saved.Timezone
	out.EarliestDate, out.LatestAccountData = payload.EarliestDate, payload.LatestAccountData
	for _, source := range payload.Sources {
		row := OverviewSource{AccountSource: source.AccountSource, Total: source.Total, Statuses: source.Statuses, CostKnown: source.CostKnown, CollectedDays: source.CollectedDays}
		if row.Statuses == nil {
			row.Statuses = map[string]int64{}
		}
		if source.CostCNY != nil {
			value, e := overviewUSD(*source.CostCNY, out.ExchangeRate)
			if e != nil {
				return out, e
			}
			row.CostUSD = &value
		}
		out.Sources = append(out.Sources, row)
	}
	for _, row := range payload.Rows {
		value, e := overviewUSD(row.CostCNY, out.ExchangeRate)
		if e != nil {
			return out, e
		}
		out.Rows = append(out.Rows, DailyUsageRowDTO{UsageDate: row.UsageDate, CostUSD: value, AccountDailyUsage: AccountDailyUsage{Model: row.Model, CostCNY: row.CostCNY, InputTokens: row.InputTokens, OutputTokens: row.OutputTokens, CacheReadTokens: row.CacheReadTokens, CacheWriteTokens: row.CacheWriteTokens}})
	}
	for _, cost := range payload.ModelCosts {
		value, e := overviewUSD(cost.CostCNY, out.ExchangeRate)
		if e != nil {
			return out, e
		}
		out.ModelCosts = append(out.ModelCosts, OverviewModelCost{SourceID: cost.SourceID, Model: cost.Model, CostCNY: cost.CostCNY, CostUSD: value})
	}
	out.Days = append(out.Days, payload.Days...)
	return out, nil
}
