package core

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Overview is a read-only projection of stable accounts and collected bills.
// Account counts never join bill lines: multiple dates/models cannot inflate them.
type OverviewSource struct {
	AccountSource
	Total         int64            `json:"total"`
	Statuses      map[string]int64 `json:"statuses"`
	CostUSD       *float64         `json:"cost_usd"`
	CostKnown     int64            `json:"cost_known"`
	CollectedDays int64            `json:"collected_days"`
}
type OverviewModelCost struct {
	SourceID string  `json:"source_id"`
	Model    string  `json:"model"`
	CostCNY  string  `json:"cost_cny"`
	CostUSD  float64 `json:"cost_usd"`
}
type OverviewDay struct {
	Date      string `json:"date"`
	Status    string `json:"status"`
	Expected  int64  `json:"expected"`
	Collected int64  `json:"collected"`
}
type AccountsOverview struct {
	SummaryAvailable   *bool               `json:"summary_available,omitempty"`
	SummaryGeneratedAt *time.Time          `json:"summary_generated_at,omitempty"`
	SummaryPeriod      string              `json:"summary_period,omitempty"`
	StartDate          string              `json:"start_date"`
	EndDate            string              `json:"end_date"`
	EarliestDate       *string             `json:"earliest_date"`
	Timezone           string              `json:"timezone"`
	CollectionTime     string              `json:"collection_time"`
	ExchangeRate       float64             `json:"exchange_rate_cny_per_usd"`
	LatestAccountData  *time.Time          `json:"latest_account_data"`
	Sources            []OverviewSource    `json:"sources"`
	Rows               []DailyUsageRowDTO  `json:"rows"`
	ModelCosts         []OverviewModelCost `json:"model_costs"`
	Days               []OverviewDay       `json:"days"`
}

func overviewSourceID(id uint64) string {
	if id == 0 {
		return "myself"
	}
	return idString(id)
}

// The range uses completed calendar days in the same timezone as collection.
// Unlike individual-account pagination, All time is not capped at 90 days.
func ValidateOverviewRange(start, end string, now time.Time) error {
	a, ea := time.ParseInLocation("2006-01-02", start, dailyZone)
	b, eb := time.ParseInLocation("2006-01-02", end, dailyZone)
	if ea != nil || eb != nil || a.Format("2006-01-02") != start || b.Format("2006-01-02") != end || a.After(b) || end >= now.In(dailyZone).Format("2006-01-02") || a.Year() < 1970 {
		return &ValidationError{Message: "Choose valid start and end dates, ending no later than yesterday."}
	}
	return nil
}

// overviewScope supplies only the account ownership rules. Both views use the
// same aggregation, currency conversion, date range, and coverage semantics.
type overviewScope struct {
	sources    []AccountSource
	base       func() *gorm.DB
	sourceExpr string
}

func (s *Store) AccountsOverview(ctx context.Context, viewerID uint64, rawSources, start, end string, allTime bool, now time.Time) (AccountsOverview, error) {
	return s.readLiveAccountsOverview(ctx, viewerID, rawSources, start, end, allTime, now, false)
}

// All live aggregates must observe the same successful bill version. Daily
// collection can replace bill lines between SQL queries; a consistent read
// prevents source/model/daily totals from being taken from different versions.
func (s *Store) readLiveAccountsOverview(ctx context.Context, viewerID uint64, rawSources, start, end string, allTime bool, now time.Time, administrative bool) (AccountsOverview, error) {
	var out AccountsOverview
	options := &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: s.DB.Dialector.Name() != "sqlite"}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		scoped := *s
		scoped.DB = tx
		var scope overviewScope
		var err error
		if administrative {
			scope, err = scoped.adminOverviewScope(ctx, viewerID, rawSources)
		} else {
			scope, err = scoped.personalOverviewScope(ctx, viewerID, rawSources)
		}
		if err != nil {
			return err
		}
		out, err = scoped.aggregateAccountsOverview(ctx, scope, start, end, allTime, now)
		return err
	}, options)
	return out, err
}

func (s *Store) personalOverviewScope(ctx context.Context, viewerID uint64, rawSources string) (overviewScope, error) {
	visibility, err := s.ResolveVisibility(ctx, viewerID, rawSources)
	if err != nil {
		return overviewScope{}, err
	}
	sources, err := s.MemberSources(ctx, viewerID)
	if err != nil {
		return overviewScope{}, err
	}
	selected := map[string]bool{"myself": visibility.Self}
	for _, id := range visibility.Sources {
		selected[idString(id)] = true
	}
	scope := overviewScope{sourceExpr: "COALESCE(h.source_user_id, 0)"}
	for _, source := range sources {
		if visibility.All || selected[source.ID] {
			scope.sources = append(scope.sources, source)
		}
	}
	scope.base = func() *gorm.DB {
		return s.DB.WithContext(ctx).Table("synced_accounts a").
			Joins("JOIN user_hierarchy h ON h.descendant_user_id = a.user_id AND h.ancestor_user_id = ?", viewerID).
			Where("a.deleted_at IS NULL AND a.user_id IN (?)", visibility.Owners(s.DB))
	}
	return scope, nil
}

func (s *Store) aggregateAccountsOverview(ctx context.Context, scope overviewScope, start, end string, allTime bool, now time.Time) (AccountsOverview, error) {
	out := AccountsOverview{Timezone: "Asia/Shanghai", CollectionTime: "03:00", Sources: []OverviewSource{}, Rows: []DailyUsageRowDTO{}, ModelCosts: []OverviewModelCost{}, Days: []OverviewDay{}}
	var err error
	out.ExchangeRate, err = s.ExchangeRate(ctx)
	if err != nil {
		return out, err
	}
	bySource := map[string]int{}
	for _, source := range scope.sources {
		bySource[source.ID] = len(out.Sources)
		out.Sources = append(out.Sources, OverviewSource{AccountSource: source, Statuses: map[string]int64{}})
	}
	base, sourceExpr := scope.base, scope.sourceExpr
	var stats []struct {
		Source uint64
		Status string
		Total  int64
		Cost   sql.NullFloat64
		Known  int64
	}
	const knownCost = "p.cost_total IS NOT NULL AND UPPER(TRIM(p.currency)) = 'CNY'"
	err = base().Joins("LEFT JOIN account_snapshots p ON p.id = (SELECT x.id FROM account_snapshots x WHERE x.account_id = a.id AND x.identity_key = a.identity_key ORDER BY x.collected_at DESC, x.id DESC LIMIT 1)").
		Select(sourceExpr + " AS source, COALESCE(NULLIF(a.snapshot_status, ''), 'unknown') AS status, COUNT(*) AS total, SUM(CASE WHEN " + knownCost + " THEN p.cost_total ELSE NULL END) AS cost, SUM(CASE WHEN " + knownCost + " THEN 1 ELSE 0 END) AS known").
		Group(sourceExpr + ", a.snapshot_status").Scan(&stats).Error
	if err != nil {
		return out, err
	}
	for _, stat := range stats {
		i, ok := bySource[overviewSourceID(stat.Source)]
		if !ok {
			continue
		}
		row := &out.Sources[i]
		row.Total += stat.Total
		row.Statuses[stat.Status] += stat.Total
		row.CostKnown += stat.Known
		if stat.Cost.Valid {
			if row.CostUSD == nil {
				row.CostUSD = new(float64)
			}
			*row.CostUSD += stat.Cost.Float64 / out.ExchangeRate
		}
	}
	var latest []struct{ LastSuccessAt *time.Time }
	if err = base().Select("a.last_success_at").Where("a.last_success_at IS NOT NULL").Order("a.last_success_at DESC").Limit(1).Scan(&latest).Error; err != nil {
		return out, err
	}
	if len(latest) > 0 {
		out.LatestAccountData = latest[0].LastSuccessAt
	}
	days := func() *gorm.DB {
		return base().Joins("JOIN account_usage_days d ON d.account_id = a.id AND d.identity_key = a.identity_key")
	}
	var earliest struct{ Value *string }
	if err = days().Select("MIN(d.usage_date) AS value").Where("d.last_success_at IS NOT NULL").Scan(&earliest).Error; err != nil {
		return out, err
	}
	out.EarliestDate = earliest.Value
	yesterday := now.In(dailyZone).AddDate(0, 0, -1)
	if allTime {
		end = yesterday.Format("2006-01-02")
		start = end
		if earliest.Value != nil {
			start = *earliest.Value
		}
	} else if start == "" && end == "" {
		end = yesterday.Format("2006-01-02")
		start = yesterday.AddDate(0, 0, -6).Format("2006-01-02")
	}
	if err = ValidateOverviewRange(start, end, now); err != nil {
		return out, err
	}
	out.StartDate, out.EndDate = start, end
	bills := func() *gorm.DB {
		return days().Joins("JOIN account_daily_usages u ON u.day_id = d.id").Where("d.last_success_at IS NOT NULL AND d.usage_date BETWEEN ? AND ?", start, end)
	}
	// Explicit DECIMAL casts preserve integer token counts above JavaScript's safe
	// range on MySQL. The JSON representation remains strings, as in Daily usage.
	fields := []string{"d.usage_date", "u.model", "SUM(u.cost_cny) AS cost_cny"}
	for _, name := range []string{"input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens"} {
		fields = append(fields, "SUM(CAST(u."+name+" AS DECIMAL(65,0))) AS "+name)
	}
	if err = bills().Select(strings.Join(fields, ", ")).Group("d.usage_date, u.model").Order("d.usage_date ASC, u.model ASC").Scan(&out.Rows).Error; err != nil {
		return out, err
	}
	for i := range out.Rows {
		cost, e := strconv.ParseFloat(out.Rows[i].CostCNY, 64)
		if e != nil {
			return out, e
		}
		out.Rows[i].CostUSD = cost / out.ExchangeRate
	}
	var costs []struct {
		Source  uint64
		Model   string
		CostCNY string
	}
	if err = bills().Select(sourceExpr + " AS source, u.model, SUM(u.cost_cny) AS cost_cny").Group(sourceExpr + ", u.model").Order("u.model ASC").Scan(&costs).Error; err != nil {
		return out, err
	}
	for _, c := range costs {
		value, e := strconv.ParseFloat(c.CostCNY, 64)
		if e != nil {
			return out, e
		}
		out.ModelCosts = append(out.ModelCosts, OverviewModelCost{SourceID: overviewSourceID(c.Source), Model: c.Model, CostCNY: c.CostCNY, CostUSD: value / out.ExchangeRate})
	}
	var collectedSources []struct {
		Source uint64
		Count  int64
	}
	if err = days().Select(sourceExpr+" AS source, COUNT(*) AS count").Where("d.last_success_at IS NOT NULL AND d.usage_date BETWEEN ? AND ?", start, end).Group(sourceExpr).Scan(&collectedSources).Error; err != nil {
		return out, err
	}
	for _, row := range collectedSources {
		if i, ok := bySource[overviewSourceID(row.Source)]; ok {
			out.Sources[i].CollectedDays = row.Count
		}
	}
	// Collection markers include successful days with no bill lines. Any saved
	// successful collection makes a day available; another account's failure must
	// not hide that day's collected totals. Expected counts are informational only.
	dateExpr := "DATE_FORMAT(DATE_ADD(a.created_at, INTERVAL 8 HOUR), '%Y-%m-%d')"
	if s.DB.Dialector.Name() == "sqlite" {
		dateExpr = "DATE(a.created_at, '+8 hours')"
	}
	if s.DB.Dialector.Name() == "postgres" {
		dateExpr = "TO_CHAR(a.created_at + INTERVAL '8 hours', 'YYYY-MM-DD')"
	}
	var cohorts []struct {
		Date  string
		Count int64
	}
	if err = base().Select(dateExpr+" AS date, COUNT(*) AS count").Where("a.current_submission_id IS NOT NULL AND a.plan LIKE ?", "claude/%").Group(dateExpr).Order("date ASC").Scan(&cohorts).Error; err != nil {
		return out, err
	}
	var coverage []struct {
		Date      string
		Collected int64
	}
	if err = days().Select("d.usage_date AS date, SUM(CASE WHEN d.last_success_at IS NOT NULL THEN 1 ELSE 0 END) AS collected").Where("d.usage_date BETWEEN ? AND ?", start, end).Group("d.usage_date").Scan(&coverage).Error; err != nil {
		return out, err
	}
	coverageByDate := map[string]int{}
	for i, c := range coverage {
		coverageByDate[c.Date] = i
	}
	first, _ := time.ParseInLocation("2006-01-02", start, dailyZone)
	last, _ := time.ParseInLocation("2006-01-02", end, dailyZone)
	var expected int64
	cohortIndex := 0
	for date := first; !date.After(last); date = date.AddDate(0, 0, 1) {
		key := date.Format("2006-01-02")
		for cohortIndex < len(cohorts) && cohorts[cohortIndex].Date <= key {
			expected += cohorts[cohortIndex].Count
			cohortIndex++
		}
		day := OverviewDay{Date: key, Status: "missing", Expected: expected}
		if i, ok := coverageByDate[key]; ok {
			c := coverage[i]
			day.Collected = c.Collected
			if c.Collected > 0 {
				day.Status = "complete"
			}
		}
		out.Days = append(out.Days, day)
	}
	if len(out.Days) == 0 {
		return out, fmt.Errorf("empty overview range")
	}
	return out, nil
}
