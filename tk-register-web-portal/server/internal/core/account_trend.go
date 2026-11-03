package core

import (
	"context"
	"math"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const accountTrendPointLimit = 1000

type AccountTrendPoint struct {
	SnapshotID  string    `json:"snapshot_id"`
	CollectedAt time.Time `json:"collected_at"`
	Value       float64   `json:"value"`
}

type AccountTrendData struct {
	Trend         map[string][][]AccountTrendPoint
	Reduced       bool
	SnapshotCount int64
}

type AccountSnapshotPage struct {
	Items    []AccountSnapshotDTO `json:"items"`
	Total    int64                `json:"total"`
	Page     int                  `json:"page"`
	PageSize int                  `json:"page_size"`
}

// These methods are internal: callers must first check owner/admin access.
func (s *Store) accountSnapshotQuery(ctx context.Context, accountID uint64, days int) (*gorm.DB, error) {
	if days != 7 && days != 30 && days != 90 {
		return nil, validationErrorf("select 7, 30, or 90 days")
	}
	var account SyncedAccount
	if err := s.DB.WithContext(ctx).First(&account, accountID).Error; err != nil {
		return nil, err
	}
	return s.DB.WithContext(ctx).Model(&AccountSnapshot{}).Where("account_id = ? AND identity_key = ? AND collected_at >= ?", accountID, account.IdentityKey, time.Now().UTC().AddDate(0, 0, -days)), nil
}

// Page one contains the newest observations, displayed chronologically within
// each page. Historical observations are never deleted or rewritten for charts.
func (s *Store) AccountSnapshotPage(ctx context.Context, accountID uint64, days, page, pageSize int) (AccountSnapshotPage, error) {
	out := AccountSnapshotPage{Items: []AccountSnapshotDTO{}, Page: page, PageSize: pageSize}
	if page < 1 || page > 100000 || pageSize < 1 || pageSize > 100 {
		return out, validationErrorf("page must be 1–100000 and page_size must be 1–100")
	}
	query, err := s.accountSnapshotQuery(ctx, accountID, days)
	if err != nil {
		return out, err
	}
	if err = query.Count(&out.Total).Error; err != nil {
		return out, err
	}
	var rows []AccountSnapshot
	if err = query.Order("collected_at DESC, id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		return out, err
	}
	for i := len(rows) - 1; i >= 0; i-- {
		out.Items = append(out.Items, snapshotDTO(rows[i]))
	}
	return out, nil
}

// AccountTrend streams only the required columns and keeps bounded working
// buffers. Each metric is segmented BEFORE reduction so missing values, long
// collection gaps and counter resets never become a fabricated connecting line.
func (s *Store) AccountTrend(ctx context.Context, accountID uint64, days, intervalSeconds int) (AccountTrendData, error) {
	out := AccountTrendData{Trend: map[string][][]AccountTrendPoint{}}
	if intervalSeconds < 60 || intervalSeconds > 604800 {
		return out, validationErrorf("invalid collection interval")
	}
	query, err := s.accountSnapshotQuery(ctx, accountID, days)
	if err != nil {
		return out, err
	}
	// The retired hours setting is the best available cadence for observations
	// saved before per-observation cadence was introduced. It is never editable.
	legacyInterval := intervalSeconds
	var legacy Setting
	if e := s.DB.WithContext(ctx).Where(clause.Eq{Column: "key", Value: "account_sync_interval_hours"}).Limit(1).Find(&legacy).Error; e != nil {
		return out, e
	}
	if hours := (Values{"hours": legacy.Value}).Int("hours"); hours >= 1 && hours <= 168 {
		legacyInterval = hours * 3600
	}
	rows, err := query.Select("id, collected_at, cost_total, currency, input_tokens_total, output_tokens_total, collection_interval_seconds").Order("collected_at ASC, id ASC").Rows()
	if err != nil {
		return out, err
	}
	defer rows.Close()
	metrics := []string{"cost_total_usd", "input_tokens_total", "output_tokens_total"}
	series := make([]trendAccumulator, len(metrics))
	for rows.Next() {
		var row AccountSnapshot
		if err = s.DB.ScanRows(rows, &row); err != nil {
			return out, err
		}
		out.SnapshotCount++
		cadence := legacyInterval
		if row.CollectionIntervalSeconds != nil && *row.CollectionIntervalSeconds >= 60 && *row.CollectionIntervalSeconds <= 604800 {
			cadence = *row.CollectionIntervalSeconds
		}
		gap := time.Duration(cadence) * time.Second * 5 / 2
		values := []*float64{snapshotDTO(row).CostTotalUSD, tokenTrendValue(row.InputTokensTotal), tokenTrendValue(row.OutputTokensTotal)}
		for i, value := range values {
			series[i].add(row.ID, row.CollectedAt, value, gap)
		}
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	for i, metric := range metrics {
		series[i].reduce(accountTrendPointLimit)
		if series[i].segments == nil {
			series[i].segments = [][]AccountTrendPoint{}
		}
		out.Trend[metric] = series[i].segments
		out.Reduced = out.Reduced || series[i].reduced
	}
	return out, nil
}

func tokenTrendValue(value *int64) *float64 {
	if value == nil {
		return nil
	}
	v := float64(*value)
	return &v
}

type trendAccumulator struct {
	segments [][]AccountTrendPoint
	previous AccountTrendPoint
	active   bool
	count    int
	reduced  bool
	gap      time.Duration
}

func (s *trendAccumulator) add(id uint64, at time.Time, value *float64, gap time.Duration) {
	if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 {
		s.active = false
		return
	}
	point := AccountTrendPoint{SnapshotID: idString(id), CollectedAt: at.UTC(), Value: *value}
	if !s.active || !at.After(s.previous.CollectedAt) || at.Sub(s.previous.CollectedAt) > max(gap, s.gap) || *value < s.previous.Value {
		s.segments = append(s.segments, []AccountTrendPoint{})
	}
	i := len(s.segments) - 1
	s.segments[i] = append(s.segments[i], point)
	s.previous, s.active = point, true
	s.gap = gap
	s.count++
	if s.count > accountTrendPointLimit*2 {
		s.reduce(accountTrendPointLimit)
	}
}

func (s *trendAccumulator) reduce(limit int) {
	if s.count <= limit {
		return
	}
	s.reduced = true
	minimum := 0
	for _, segment := range s.segments {
		minimum += min(2, len(segment))
	}
	// If interruptions alone exceed the budget, omit oldest complete segments;
	// never join them together or invent interpolated observations.
	for minimum > limit && len(s.segments) > 1 {
		minimum -= min(2, len(s.segments[0]))
		s.count -= len(s.segments[0])
		s.segments = s.segments[1:]
	}
	remaining, extra := limit-minimum, s.count-minimum
	counts := make([]int, len(s.segments))
	used := 0
	for i, segment := range s.segments {
		base := min(2, len(segment))
		n := base
		if extra > 0 {
			n += (len(segment) - base) * remaining / extra
		}
		counts[i] = n
		used += n
	}
	for i := len(counts) - 1; used < limit && i >= 0; i-- {
		if counts[i] < len(s.segments[i]) {
			counts[i]++
			used++
		}
	}
	s.count = 0
	for i, segment := range s.segments {
		s.segments[i] = reduceTrendSegment(segment, counts[i])
		s.count += len(s.segments[i])
	}
}

// Largest-triangle selection retains real observations and both endpoints.
// It changes presentation only, never computes sums/averages of cumulative data.
func reduceTrendSegment(points []AccountTrendPoint, limit int) []AccountTrendPoint {
	if len(points) <= limit {
		return points
	}
	if limit <= 2 {
		return []AccountTrendPoint{points[0], points[len(points)-1]}
	}
	out := make([]AccountTrendPoint, 0, limit)
	out = append(out, points[0])
	width := float64(len(points)-2) / float64(limit-2)
	a := 0
	x := func(i int) float64 {
		return float64(points[i].CollectedAt.Sub(points[0].CollectedAt)) / float64(time.Second)
	}
	for i := 0; i < limit-2; i++ {
		avgStart := int(math.Floor(float64(i+1)*width)) + 1
		avgEnd := min(int(math.Floor(float64(i+2)*width))+1, len(points))
		avgX, avgY := 0.0, 0.0
		for j := avgStart; j < avgEnd; j++ {
			avgX += x(j)
			avgY += points[j].Value
		}
		avgX /= float64(avgEnd - avgStart)
		avgY /= float64(avgEnd - avgStart)
		start := int(math.Floor(float64(i)*width)) + 1
		end := min(int(math.Floor(float64(i+1)*width))+1, len(points)-1)
		best, area := start, -1.0
		for j := start; j < end; j++ {
			candidate := math.Abs((x(a)-avgX)*(points[j].Value-points[a].Value) - (x(a)-x(j))*(avgY-points[a].Value))
			if candidate > area {
				best, area = j, candidate
			}
		}
		out = append(out, points[best])
		a = best
	}
	return append(out, points[len(points)-1])
}
