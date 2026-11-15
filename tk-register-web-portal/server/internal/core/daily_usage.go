package core

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"goapi-tk-register/server/internal/billing"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var dailyZone = time.FixedZone("Asia/Shanghai", 8*60*60)

// Successful data and failed attempts are separate: retries never replace a
// successful day with zero, and missing days are never manufactured as usage.
type AccountUsageDay struct {
	ID            uint64        `gorm:"primaryKey"`
	AccountID     uint64        `gorm:"not null;uniqueIndex:idx_daily_account_date,priority:1"`
	Account       SyncedAccount `gorm:"foreignKey:AccountID;constraint:OnDelete:RESTRICT"`
	IdentityKey   string        `gorm:"size:64;not null;uniqueIndex:idx_daily_account_date,priority:2"`
	UsageDate     string        `gorm:"size:10;not null;uniqueIndex:idx_daily_account_date,priority:3"`
	LastSuccessAt *time.Time
	LastAttemptAt time.Time `gorm:"not null"`
	LastError     string    `gorm:"size:255;not null"`
}
type AccountDailyUsage struct {
	ID               uint64          `gorm:"primaryKey" json:"-"`
	DayID            uint64          `gorm:"not null;uniqueIndex:idx_daily_model,priority:1" json:"-"`
	Day              AccountUsageDay `gorm:"foreignKey:DayID;constraint:OnDelete:RESTRICT" json:"-"`
	Model            string          `gorm:"size:254;not null;uniqueIndex:idx_daily_model,priority:2" json:"model"`
	InputTokens      string          `gorm:"size:30;not null" json:"input_tokens"`
	OutputTokens     string          `gorm:"size:30;not null" json:"output_tokens"`
	CacheReadTokens  string          `gorm:"size:30;not null" json:"cache_read_tokens"`
	CacheWriteTokens string          `gorm:"size:30;not null" json:"cache_write_tokens"`
	CostCNY          string          `gorm:"type:decimal(30,8);not null" json:"cost_cny"`
}
type DailyUsageJob struct {
	ID           uint64    `gorm:"primaryKey"`
	VendorID     uint64    `gorm:"not null;uniqueIndex:idx_daily_vendor_date"`
	Vendor       Vendor    `gorm:"foreignKey:VendorID;constraint:OnDelete:RESTRICT"`
	UsageDate    string    `gorm:"size:10;not null;uniqueIndex:idx_daily_vendor_date"`
	Status       string    `gorm:"size:24;not null;index:idx_daily_due,priority:1"`
	NextRunAt    time.Time `gorm:"not null;index:idx_daily_due,priority:2"`
	Attempts     int       `gorm:"not null"`
	LeaseToken   string    `gorm:"size:64;not null"`
	LeaseUntil   *time.Time
	LastError    string `gorm:"size:255;not null"`
	AccountCount int    `gorm:"not null"`
	ReviewCount  int    `gorm:"not null"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
	FinishedAt   *time.Time
}

func (s *Store) migrateDailyUsage() error {
	return s.DB.AutoMigrate(&AccountUsageDay{}, &AccountDailyUsage{}, &DailyUsageJob{})
}

// At 03:00 Beijing, yesterday becomes due. Before then, the previous day's
// schedule remains due. A bounded seven-day recovery also fills the initial UI.
func DailyUsageDueDate(now time.Time) time.Time {
	local := now.In(dailyZone)
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, dailyZone)
	if local.Hour() < 3 {
		return midnight.AddDate(0, 0, -2)
	}
	return midnight.AddDate(0, 0, -1)
}
func (s *Store) EnsureDailyUsageJobs(ctx context.Context, now time.Time) error {
	var vendor Vendor
	err := s.DB.WithContext(ctx).Where("code = ? AND enabled = ?", "tk-register-0", true).First(&vendor).Error
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	due := DailyUsageDueDate(now)
	var existing []string
	if err = s.DB.WithContext(ctx).Model(&DailyUsageJob{}).Where("vendor_id = ? AND usage_date BETWEEN ? AND ?", vendor.ID, due.AddDate(0, 0, -6).Format("2006-01-02"), due.Format("2006-01-02")).Pluck("usage_date", &existing).Error; err != nil {
		return err
	}
	present := map[string]bool{}
	for _, date := range existing {
		present[date] = true
	}
	// Most recent day first. Unique vendor/day keys make this safe on restart and
	// across replicas; no network operation or user request holds a DB transaction.
	for i := 0; i < 7; i++ {
		date := due.AddDate(0, 0, -i).Format("2006-01-02")
		if present[date] {
			continue
		}
		row := DailyUsageJob{VendorID: vendor.ID, UsageDate: date, Status: "queued", NextRunAt: now, CreatedAt: now, UpdatedAt: now}
		if err = s.DB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) ClaimDailyUsageJob(ctx context.Context, now time.Time, token string) (*DailyUsageJob, error) {
	if len(token) < 16 || len(token) > 64 {
		return nil, errors.New("invalid worker lease")
	}
	q := s.DB.WithContext(ctx).Model(&DailyUsageJob{}).Where("(status = ? AND next_run_at <= ?) OR (status = ? AND lease_until < ?)", "queued", now, "running", now)
	var row DailyUsageJob
	if err := q.Order("usage_date DESC").First(&row).Error; errors.Is(err, ErrNotFound) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	until := now.Add(4 * time.Minute)
	result := q.Where("id = ?", row.ID).Updates(map[string]any{"status": "running", "lease_token": token, "lease_until": until, "attempts": gorm.Expr("attempts + 1")})
	if result.Error != nil || result.RowsAffected != 1 {
		return nil, result.Error
	}
	row.Status = "running"
	row.LeaseToken = token
	row.LeaseUntil = &until
	row.Attempts++
	return &row, nil
}
func dailyClaim(tx *gorm.DB, id uint64, token string) *gorm.DB {
	return tx.Model(&DailyUsageJob{}).Where("id = ? AND status = ? AND lease_token = ?", id, "running", token)
}
func (s *Store) DeferDailyUsage(ctx context.Context, job DailyUsageJob, next time.Time, message string, cooldown bool) error {
	status := "queued"
	if !cooldown && job.Attempts >= 6 {
		status = "failed"
	}
	updates := map[string]any{"status": status, "next_run_at": next, "last_error": message, "lease_token": "", "lease_until": nil}
	if cooldown {
		updates["attempts"] = gorm.Expr("CASE WHEN attempts > 0 THEN attempts - 1 ELSE 0 END")
	}
	return dailyClaim(s.DB.WithContext(ctx), job.ID, job.LeaseToken).Updates(updates).Error
}
func (s *Store) DailyUsageTargets(ctx context.Context, vendorID uint64) ([]billing.Account, error) {
	var rows []struct {
		ID                         uint64
		Account, Plan, IdentityKey string
		PlatformAccountID          *string
	}
	err := s.DB.WithContext(ctx).Table("synced_accounts AS a").Select(`a.id,a.account,a.plan,a.identity_key,COALESCE(NULLIF(a.platform_account_id,''),(SELECT NULLIF(s.platform_account_id,'') FROM account_snapshots s WHERE s.account_id=a.id AND s.identity_key=a.identity_key AND s.platform_account_id IS NOT NULL AND s.platform_account_id<>'' ORDER BY s.collected_at DESC,s.id DESC LIMIT 1)) AS platform_account_id`).Where("a.vendor_id = ? AND a.deleted_at IS NULL AND a.current_submission_id IS NOT NULL AND a.plan LIKE ?", vendorID, "claude/%").Order("a.id").Limit(20001).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) > 20000 {
		return nil, errors.New("daily account selection exceeds supported size")
	}
	out := make([]billing.Account, 0, len(rows))
	for _, r := range rows {
		id := ""
		if r.PlatformAccountID != nil {
			id = *r.PlatformAccountID
		}
		out = append(out, billing.Account{LocalAccountID: idString(r.ID), Account: r.Account, Plan: r.Plan, IdentityKey: r.IdentityKey, PlatformAccountID: id})
	}
	return out, nil
}
func (s *Store) SaveDailyUsageAccount(ctx context.Context, job DailyUsageJob, result billing.DailyAccount, now time.Time) error {
	accountID, err := ParseBusinessID(result.LocalAccountID)
	if err != nil {
		return err
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Fenced per-account commits avoid a vendor-sized transaction and nested
		// reads with an open cursor. Reprocessing replaces, rather than adds, usage.
		var currentJob DailyUsageJob
		if err := dailyClaim(tx, job.ID, job.LeaseToken).Clauses(clause.Locking{Strength: "UPDATE"}).First(&currentJob).Error; err != nil {
			return err
		}
		var account SyncedAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND vendor_id = ? AND identity_key = ? AND deleted_at IS NULL", accountID, job.VendorID, result.IdentityKey).First(&account).Error; err != nil {
			return err
		}
		day := AccountUsageDay{AccountID: accountID, IdentityKey: result.IdentityKey, UsageDate: job.UsageDate, LastAttemptAt: now}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&day).Error; err != nil {
			return err
		}
		if err := tx.Where("account_id = ? AND identity_key = ? AND usage_date = ?", accountID, result.IdentityKey, job.UsageDate).First(&day).Error; err != nil {
			return err
		}
		updates := map[string]any{"last_attempt_at": now, "last_error": result.Error}
		if result.Error == "" {
			if err := tx.Where("day_id = ?", day.ID).Delete(&AccountDailyUsage{}).Error; err != nil {
				return err
			}
			rows := make([]AccountDailyUsage, 0, len(result.Rows))
			for _, r := range result.Rows {
				rows = append(rows, AccountDailyUsage{DayID: day.ID, Model: r.Model, InputTokens: r.InputTokens, OutputTokens: r.OutputTokens, CacheReadTokens: r.CacheReadTokens, CacheWriteTokens: r.CacheWriteTokens, CostCNY: r.CostCNY})
			}
			if len(rows) > 0 {
				if err := tx.CreateInBatches(&rows, 100).Error; err != nil {
					return err
				}
			}
			updates["last_success_at"] = now
		}
		return tx.Model(&day).Updates(updates).Error
	})
}
func (s *Store) CompleteDailyUsage(ctx context.Context, job DailyUsageJob, count, review int, now time.Time) error {
	return dailyClaim(s.DB.WithContext(ctx), job.ID, job.LeaseToken).Updates(map[string]any{"status": "succeeded", "lease_token": "", "lease_until": nil, "last_error": "", "account_count": count, "review_count": review, "finished_at": now}).Error
}

type DailyUsageRowDTO struct {
	UsageDate string `json:"date"`
	AccountDailyUsage
	CostUSD float64 `json:"cost_usd"`
}
type DailyUsageDayDTO struct {
	Date        string     `json:"date"`
	Status      string     `json:"status"`
	CollectedAt *time.Time `json:"collected_at"`
	Error       string     `json:"error,omitempty"`
}
type DailyUsageDTO struct {
	StartDate      string             `json:"start_date"`
	EndDate        string             `json:"end_date"`
	Timezone       string             `json:"timezone"`
	CollectionTime string             `json:"collection_time"`
	Currency       string             `json:"currency"`
	ExchangeRate   float64            `json:"exchange_rate_cny_per_usd"`
	Rows           []DailyUsageRowDTO `json:"rows"`
	Days           []DailyUsageDayDTO `json:"days"`
}

func ValidateDailyUsageRange(start, end string, now time.Time) error {
	a, e1 := time.ParseInLocation("2006-01-02", start, dailyZone)
	b, e2 := time.ParseInLocation("2006-01-02", end, dailyZone)
	local := now.In(dailyZone)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, dailyZone)
	if e1 != nil || e2 != nil || a.Format("2006-01-02") != start || b.Format("2006-01-02") != end || b.Before(a) || b.Sub(a) > 89*24*time.Hour || !b.Before(today) {
		return &ValidationError{Message: "Choose up to 90 complete days, ending no later than yesterday."}
	}
	return nil
}

// Caller must authorize the stable account ID before calling this method.
func (s *Store) AccountDailyUsage(ctx context.Context, id uint64, start, end string) (DailyUsageDTO, error) {
	out := DailyUsageDTO{StartDate: start, EndDate: end, Timezone: "Asia/Shanghai", CollectionTime: "03:00", Currency: "CNY", Rows: []DailyUsageRowDTO{}, Days: []DailyUsageDayDTO{}}
	var err error
	out.ExchangeRate, err = s.ExchangeRate(ctx)
	if err != nil {
		return out, err
	}
	var account SyncedAccount
	if err = s.DB.WithContext(ctx).First(&account, id).Error; err != nil {
		return out, err
	}
	var days []AccountUsageDay
	if err = s.DB.WithContext(ctx).Where("account_id = ? AND identity_key = ? AND usage_date BETWEEN ? AND ?", id, account.IdentityKey, start, end).Find(&days).Error; err != nil {
		return out, err
	}
	var jobs []DailyUsageJob
	if err = s.DB.WithContext(ctx).Select("usage_date,status,last_error").Where("vendor_id = ? AND usage_date BETWEEN ? AND ?", account.VendorID, start, end).Find(&jobs).Error; err != nil {
		return out, err
	}
	byDay := map[string]AccountUsageDay{}
	byJob := map[string]DailyUsageJob{}
	for _, d := range days {
		byDay[d.UsageDate] = d
	}
	for _, j := range jobs {
		byJob[j.UsageDate] = j
	}
	first, _ := time.ParseInLocation("2006-01-02", start, dailyZone)
	last, _ := time.ParseInLocation("2006-01-02", end, dailyZone)
	for date := first; !date.After(last); date = date.AddDate(0, 0, 1) {
		key := date.Format("2006-01-02")
		d := byDay[key]
		j := byJob[key]
		item := DailyUsageDayDTO{Date: key, Status: "missing"}
		if d.LastSuccessAt != nil {
			item.Status = "complete"
			item.CollectedAt = d.LastSuccessAt
		}
		if d.LastError != "" {
			item.Status = "failed"
			item.Error = d.LastError
			if d.LastSuccessAt != nil {
				item.Status = "stale"
			}
		} else if item.Status == "missing" && j.LastError != "" {
			item.Error = j.LastError
			item.Status = "failed"
		} else if item.Status == "missing" && (j.Status == "running" || j.Status == "queued") {
			item.Status = "pending"
		}
		out.Days = append(out.Days, item)
	}
	err = s.DB.WithContext(ctx).Table("account_daily_usages AS u").Select("u.*,d.usage_date").Joins("JOIN account_usage_days d ON d.id=u.day_id").Where("d.account_id = ? AND d.identity_key = ? AND d.usage_date BETWEEN ? AND ? AND d.last_success_at IS NOT NULL", id, account.IdentityKey, start, end).Order("d.usage_date DESC,u.model ASC").Limit(10001).Scan(&out.Rows).Error
	if err != nil {
		return out, err
	}
	if len(out.Rows) > 10000 {
		return out, fmt.Errorf("daily usage selection exceeds supported size")
	}
	for i := range out.Rows {
		cost, err := strconv.ParseFloat(out.Rows[i].CostCNY, 64)
		if err != nil {
			return out, err
		}
		out.Rows[i].CostUSD = cost / out.ExchangeRate
	}
	return out, nil
}
