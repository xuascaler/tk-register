package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	MaxBillExportReportBytes   = 4 << 20
	MaxBillExportArtifactBytes = 8 << 20
	BillExportRetention        = 7 * 24 * time.Hour
)

var ErrBillExportClaimLost = errors.New("bill export worker claim is no longer valid")

// BillExportJob keeps only the selected user's filtered results. Supplier-wide
// source files and website credentials must never be persisted in this table.
type BillExportJob struct {
	ID                  uint64     `gorm:"primaryKey"`
	RequestedBy         uint64     `gorm:"not null;index"`
	Requester           User       `gorm:"foreignKey:RequestedBy;constraint:OnDelete:RESTRICT" json:"-"`
	TargetUserID        uint64     `gorm:"not null;index"`
	TargetUser          User       `gorm:"foreignKey:TargetUserID;constraint:OnDelete:RESTRICT" json:"-"`
	UserEmail           string     `gorm:"size:50;not null"`
	VendorID            uint64     `gorm:"not null;index"`
	Vendor              Vendor     `gorm:"foreignKey:VendorID;constraint:OnDelete:RESTRICT" json:"-"`
	StartDate           string     `gorm:"size:10;not null"`
	EndDate             string     `gorm:"size:10;not null"`
	Timezone            string     `gorm:"size:32;not null"`
	Status              string     `gorm:"size:24;not null;index:idx_bill_export_due,priority:1"`
	AccountCount        int        `gorm:"not null"`
	MatchedAccountCount int        `gorm:"not null;default:0"`
	ReviewAccountCount  int        `gorm:"not null;default:0"`
	RowCount            int        `gorm:"not null;default:0"`
	TotalCNY            string     `gorm:"size:80;not null;default:''"`
	Currency            string     `gorm:"size:8;not null"`
	ErrorCode           string     `gorm:"size:64;not null;default:''"`
	ErrorMessage        string     `gorm:"size:255;not null;default:''"`
	AttemptCount        int        `gorm:"not null;default:0"`
	NextRunAt           time.Time  `gorm:"not null;index:idx_bill_export_due,priority:2"`
	LeaseToken          string     `gorm:"size:64;not null;default:''" json:"-"`
	LeaseUntil          *time.Time `gorm:"index" json:"-"`
	// Native unbounded string/bytes map to MySQL longtext/longblob,
	// PostgreSQL text/bytea, and SQLite text/blob. Bounds are enforced on save.
	ReportJSON string `json:"-"`
	Artifact   []byte `json:"-"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
	FinishedAt *time.Time
	ExpiresAt  time.Time `gorm:"not null;index"`
}

// BillExportAccount freezes current ownership and the precise ID namespaces at
// job creation. Repeated submissions are represented by one stable account row.
type BillExportAccount struct {
	ID                uint64        `gorm:"primaryKey"`
	JobID             uint64        `gorm:"not null;uniqueIndex:idx_bill_export_account"`
	Job               BillExportJob `gorm:"foreignKey:JobID;constraint:OnDelete:RESTRICT" json:"-"`
	LocalAccountID    uint64        `gorm:"not null;uniqueIndex:idx_bill_export_account"`
	LocalAccount      SyncedAccount `gorm:"foreignKey:LocalAccountID;constraint:OnDelete:RESTRICT" json:"-"`
	IdentityKey       string        `gorm:"size:64;not null"`
	AccountEmail      string        `gorm:"size:254;not null"`
	Plan              string        `gorm:"size:64;not null"`
	PlatformAccountID *string       `gorm:"size:32"`
	SupplierRecordID  *string       `gorm:"size:32"`
	RemoteID          *string       `gorm:"size:32"`
}

type BillExportResult struct {
	MatchedAccountCount int
	ReviewAccountCount  int
	RowCount            int
	TotalCNY            string
	ReportJSON          string
	Artifact            []byte
}

func (s *Store) migrateBillExports() error {
	return s.DB.AutoMigrate(&BillExportJob{}, &BillExportAccount{})
}

func ValidateBillExportDates(start, end string, now time.Time) error {
	zone := time.FixedZone("Asia/Shanghai", 8*60*60)
	from, err := time.ParseInLocation("2006-01-02", start, zone)
	if err != nil || from.Format("2006-01-02") != start {
		return validationErrorf("Choose a valid start date.")
	}
	to, err := time.ParseInLocation("2006-01-02", end, zone)
	if err != nil || to.Format("2006-01-02") != end {
		return validationErrorf("Choose a valid end date.")
	}
	today := now.In(zone).Format("2006-01-02")
	if now.IsZero() || end > today {
		return validationErrorf("The end date cannot be in the future (Beijing time).")
	}
	if to.Before(from) {
		return validationErrorf("The end date must be on or after the start date.")
	}
	if to.Sub(from) > 59*24*time.Hour {
		return validationErrorf("Select no more than 60 days, including both dates.")
	}
	return nil
}

func (s *Store) CreateBillExport(ctx context.Context, actorID, userID uint64, start, end string, now time.Time) (BillExportJob, error) {
	var job BillExportJob
	if err := ValidateBillExportDates(start, end, now); err != nil {
		return job, err
	}
	if actorID == 0 || userID == 0 {
		return job, validationErrorf("Select a user.")
	}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// All exports currently use this one vendor. The short row lock serializes
		// queue admission and prevents simultaneous requests exceeding queue limits.
		var vendor Vendor
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("code = ?", "tk-register-0").First(&vendor).Error; err != nil {
			return err
		}
		var actor, user User
		if err := tx.Where("id = ? AND role = ? AND status = ?", actorID, "admin", "active").First(&actor).Error; err != nil {
			return err
		}
		if err := tx.First(&user, userID).Error; err != nil {
			return err
		}
		active := []string{"queued", "fetching", "filtering"}
		var global, own int64
		if err := tx.Model(&BillExportJob{}).Where("status IN ?", active).Count(&global).Error; err != nil {
			return err
		}
		if err := tx.Model(&BillExportJob{}).Where("status IN ? AND requested_by = ?", active, actorID).Count(&own).Error; err != nil {
			return err
		}
		if global >= 20 || own >= 5 {
			return validationErrorf("Too many exports are waiting. Wait for an existing export to finish.")
		}
		// One materialized query, not an open row cursor followed by nested queries.
		// Use the newest snapshot only for the current accepted account lifetime.
		// This is the same fallback as the Accounts screen's displayed platform ID.
		var accounts []BillExportAccount
		err := tx.Table("synced_accounts AS a").Select(`a.id AS local_account_id, a.identity_key, a.account AS account_email, a.plan, a.remote_id, a.supplier_record_id,
   COALESCE(NULLIF(a.platform_account_id, ''), (SELECT snap.platform_account_id FROM account_snapshots snap WHERE snap.account_id = a.id AND snap.identity_key = a.identity_key ORDER BY snap.collected_at DESC, snap.id DESC LIMIT 1)) AS platform_account_id`).
			Where("a.user_id = ? AND a.vendor_id = ? AND a.deleted_at IS NULL AND a.plan LIKE ?", userID, vendor.ID, "claude/%").Order("a.id ASC").Limit(20001).Scan(&accounts).Error
		if err != nil {
			return err
		}
		if len(accounts) == 0 {
			return validationErrorf("This user has no current Claude accounts to export.")
		}
		if len(accounts) > 20000 {
			return validationErrorf("This user has too many accounts for one export.")
		}
		job = BillExportJob{RequestedBy: actor.ID, TargetUserID: user.ID, UserEmail: user.Email, VendorID: vendor.ID, StartDate: start, EndDate: end, Timezone: "Asia/Shanghai", Status: "queued", AccountCount: len(accounts), Currency: "CNY", NextRunAt: now.UTC(), CreatedAt: now.UTC(), ExpiresAt: now.UTC().Add(BillExportRetention)}
		if err := tx.Create(&job).Error; err != nil {
			return err
		}
		for i := range accounts {
			accounts[i].JobID = job.ID
		}
		return tx.CreateInBatches(accounts, 200).Error
	})
	return job, err
}

func billExportMetadata(db *gorm.DB) *gorm.DB {
	return db.Omit("report_json", "artifact", "lease_token")
}

func (s *Store) BillExport(ctx context.Context, id uint64) (BillExportJob, error) {
	var job BillExportJob
	if id == 0 {
		return job, ErrNotFound
	}
	err := billExportMetadata(s.DB.WithContext(ctx)).First(&job, id).Error
	return job, err
}
func (s *Store) BillExportData(ctx context.Context, id uint64) (BillExportJob, error) {
	var job BillExportJob
	if id == 0 {
		return job, ErrNotFound
	}
	err := s.DB.WithContext(ctx).First(&job, id).Error
	return job, err
}
func (s *Store) RecentBillExports(ctx context.Context) ([]BillExportJob, error) {
	jobs := []BillExportJob{}
	err := billExportMetadata(s.DB.WithContext(ctx)).Order("id DESC").Limit(20).Find(&jobs).Error
	return jobs, err
}
func (s *Store) BillExportAccounts(ctx context.Context, id uint64) ([]BillExportAccount, error) {
	rows := []BillExportAccount{}
	if id == 0 {
		return rows, ErrNotFound
	}
	err := s.DB.WithContext(ctx).Where("job_id = ?", id).Order("id ASC").Find(&rows).Error
	return rows, err
}

// Claim uses a compare-and-swap even when a distributed worker mutex is held.
// A crashed worker's lease becomes reclaimable; its stale token cannot publish.
func (s *Store) ClaimBillExport(ctx context.Context, now, leaseUntil time.Time, token string) (*BillExportJob, error) {
	if len(token) < 16 || len(token) > 64 || !leaseUntil.After(now) {
		return nil, validationErrorf("Invalid export worker claim.")
	}
	eligible := "expires_at > ? AND ((status = ? AND next_run_at <= ?) OR (status IN ? AND lease_until <= ?))"
	args := []any{now.UTC(), "queued", now.UTC(), []string{"fetching", "filtering"}, now.UTC()}
	for i := 0; i < 3; i++ {
		var job BillExportJob
		err := billExportMetadata(s.DB.WithContext(ctx)).Where(eligible, args...).Order("next_run_at ASC, id ASC").First(&job).Error
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		result := s.DB.WithContext(ctx).Model(&BillExportJob{}).Where("id = ?", job.ID).Where(eligible, args...).Updates(map[string]any{"status": "fetching", "lease_token": token, "lease_until": leaseUntil.UTC(), "attempt_count": gorm.Expr("attempt_count + 1"), "error_code": "", "error_message": ""})
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected == 1 {
			job.Status = "fetching"
			job.LeaseToken = token
			job.LeaseUntil = &leaseUntil
			job.AttemptCount++
			job.ErrorCode = ""
			job.ErrorMessage = ""
			return &job, nil
		}
	}
	return nil, nil
}
func (s *Store) billExportClaim(ctx context.Context, id uint64, token string) *gorm.DB {
	return s.DB.WithContext(ctx).Model(&BillExportJob{}).Where("id = ? AND lease_token = ? AND status IN ?", id, token, []string{"fetching", "filtering"})
}
func billExportClaimResult(result *gorm.DB) error {
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrBillExportClaimLost
	}
	return nil
}
func (s *Store) SetBillExportStage(ctx context.Context, id uint64, token, status string) error {
	if status != "filtering" {
		return validationErrorf("Invalid export stage.")
	}
	return billExportClaimResult(s.billExportClaim(ctx, id, token).Update("status", status))
}
func (s *Store) DeferBillExport(ctx context.Context, id uint64, token string, next time.Time, reason string) error {
	return billExportClaimResult(s.billExportClaim(ctx, id, token).Updates(map[string]any{"status": "queued", "next_run_at": next.UTC(), "lease_token": "", "lease_until": nil, "error_code": "WAITING", "error_message": safeBillExportError(reason)}))
}
func (s *Store) FailBillExport(ctx context.Context, id uint64, token, code, message string, now time.Time) error {
	if len(code) > 64 {
		code = "EXPORT_FAILED"
	}
	return billExportClaimResult(s.billExportClaim(ctx, id, token).Updates(map[string]any{"status": "failed", "error_code": code, "error_message": safeBillExportError(message), "finished_at": now.UTC(), "lease_token": "", "lease_until": nil, "report_json": "", "artifact": nil}))
}
func (s *Store) CompleteBillExport(ctx context.Context, id uint64, token string, result BillExportResult, now time.Time) error {
	if len(result.ReportJSON) > MaxBillExportReportBytes || len(result.Artifact) > MaxBillExportArtifactBytes || !json.Valid([]byte(result.ReportJSON)) || len(result.Artifact) < 4 || string(result.Artifact[:2]) != "PK" || result.MatchedAccountCount < 0 || result.ReviewAccountCount < 0 || result.RowCount < 0 || len(result.TotalCNY) == 0 || len(result.TotalCNY) > 80 {
		return validationErrorf("Export result is invalid or too large.")
	}
	return billExportClaimResult(s.billExportClaim(ctx, id, token).Updates(map[string]any{"status": "completed", "matched_account_count": result.MatchedAccountCount, "review_account_count": result.ReviewAccountCount, "row_count": result.RowCount, "total_cny": result.TotalCNY, "report_json": result.ReportJSON, "artifact": result.Artifact, "finished_at": now.UTC(), "expires_at": now.UTC().Add(BillExportRetention), "error_code": "", "error_message": "", "lease_token": "", "lease_until": nil}))
}
func safeBillExportError(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 255 {
		value = value[:255]
	}
	return value
}

// Metadata and the immutable account selection are retained for audit. Expired
// files/results are removed in a bounded UPDATE; active expired jobs terminate.
func (s *Store) ExpireBillExports(ctx context.Context, now time.Time) error {
	if err := s.DB.WithContext(ctx).Model(&BillExportJob{}).Where("expires_at <= ? AND status IN ?", now.UTC(), []string{"queued", "fetching", "filtering"}).Updates(map[string]any{"status": "failed", "error_code": "EXPIRED", "error_message": "This export expired before it finished.", "finished_at": now.UTC(), "lease_token": "", "lease_until": nil}).Error; err != nil {
		return err
	}
	return s.DB.WithContext(ctx).Model(&BillExportJob{}).Where("expires_at <= ? AND (artifact IS NOT NULL OR report_json <> '')", now.UTC()).Updates(map[string]any{"artifact": nil, "report_json": ""}).Error
}
