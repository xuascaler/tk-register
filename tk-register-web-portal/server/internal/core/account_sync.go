package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SyncedAccount is a stable, owner-scoped identity independent of retries in
// submission history. IdentityKey marks the currently accepted remote lifetime.
type SyncedAccount struct {
	ID                  uint64      `gorm:"primaryKey"`
	AccountKey          string      `gorm:"size:64;not null;uniqueIndex"`
	UserID              uint64      `gorm:"not null;index"`
	User                User        `gorm:"foreignKey:UserID;constraint:OnDelete:RESTRICT"`
	VendorID            uint64      `gorm:"not null;index"`
	Vendor              Vendor      `gorm:"foreignKey:VendorID;constraint:OnDelete:RESTRICT"`
	Account             string      `gorm:"size:254;not null"`
	Plan                string      `gorm:"size:64;not null"`
	CurrentSubmissionID *uint64     `gorm:"index"`
	CurrentSubmission   *Submission `gorm:"foreignKey:CurrentSubmissionID;constraint:OnDelete:RESTRICT"`
	LatestSubmissionID  uint64      `gorm:"not null;index"`
	LatestSubmission    Submission  `gorm:"foreignKey:LatestSubmissionID;constraint:OnDelete:RESTRICT"`
	LatestSubmittedAt   time.Time   `gorm:"not null;index"`
	SubmissionStatus    string      `gorm:"size:24;not null"`
	RemoteID            *string     `gorm:"size:32"`
	PlatformAccountID   *string     `gorm:"size:32"`
	IdentityKey         string      `gorm:"size:64;not null"`
	SupplierRecordID    *string     `gorm:"size:32"`
	AuthJSONUpdatedAt   *time.Time
	SnapshotStatus      *string `gorm:"size:80;index"`
	LastSuccessAt       *time.Time
	LastAttemptAt       *time.Time
	LastError           string `gorm:"size:255;not null;default:''"`
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// AccountSnapshotValues preserves absent values as SQL NULL and JSON null.
// Cumulative counters are observations; resets must not become negative usage.
type AccountSnapshotValues struct {
	PlatformAccountID *string    `gorm:"size:32" json:"platform_account_id"`
	SourceUpdatedAt   *time.Time `json:"source_updated_at"`
	AccountStatus     *string    `gorm:"size:80" json:"account_status"`
	InputTokensTotal  *int64     `json:"input_tokens_total"`
	OutputTokensTotal *int64     `json:"output_tokens_total"`
	CostTotal         *float64   `json:"cost_total"`
	Currency          *string    `gorm:"size:8" json:"currency"`
	Usage5hPercent    *float64   `json:"usage_5h_percent"`
	Reset5hAt         *time.Time `json:"reset_5h_at"`
	Usage7dPercent    *float64   `json:"usage_7d_percent"`
	Reset7dAt         *time.Time `json:"reset_7d_at"`
}

type AccountSnapshot struct {
	ID                    uint64        `gorm:"primaryKey"`
	AccountID             uint64        `gorm:"not null;uniqueIndex:idx_account_snapshot_observation;index"`
	Account               SyncedAccount `gorm:"foreignKey:AccountID;constraint:OnDelete:RESTRICT"`
	IdentityKey           string        `gorm:"size:64;not null;uniqueIndex:idx_account_snapshot_observation"`
	SnapshotDate          string        `gorm:"size:10;not null"`
	CollectedAt           time.Time     `gorm:"not null;uniqueIndex:idx_account_snapshot_observation;index"`
	DataSource            string        `gorm:"size:24;not null"`
	AccountSnapshotValues `gorm:"embedded"`
}

// AccountSyncState stores scheduling timestamps across server restarts. A
// distributed scheduler lease is separate from this durable status record.
type AccountSyncState struct {
	ID             uint64     `gorm:"primaryKey" json:"id"`
	LastStartedAt  *time.Time `json:"last_started_at"`
	LastFinishedAt *time.Time `json:"last_finished_at"`
	LastSuccessAt  *time.Time `json:"last_success_at"`
	LastError      string     `gorm:"size:255;not null;default:''" json:"last_error"`
	Status         string     `gorm:"size:24;not null;default:idle" json:"status"`
}

type AccountSnapshotDTO struct {
	ID           string    `json:"id"`
	AccountID    string    `json:"account_id"`
	SnapshotDate string    `json:"snapshot_date"`
	CollectedAt  time.Time `json:"collected_at"`
	DataSource   string    `json:"data_source"`
	AccountSnapshotValues
}
type AccountDTO struct {
	ID                  string              `json:"id"`
	UserID              string              `json:"user_id,omitempty"`
	UserEmail           string              `json:"user_email,omitempty"`
	Account             string              `json:"account"`
	Plan                string              `json:"plan"`
	VendorID            string              `json:"vendor_id"`
	VendorName          string              `json:"vendor_name"`
	CurrentSubmissionID *string             `json:"current_submission_id"`
	LatestSubmissionID  string              `json:"latest_submission_id"`
	SubmissionStatus    string              `json:"submission_status"`
	RemoteID            *string             `json:"remote_id"`
	PlatformAccountID   *string             `json:"platform_account_id"`
	LastSuccessAt       *time.Time          `json:"last_success_at"`
	LastAttemptAt       *time.Time          `json:"last_attempt_at"`
	LastError           string              `json:"last_error"`
	Snapshot            *AccountSnapshotDTO `json:"snapshot"`
	SupplierRecordID    *string             `json:"supplier_record_id"`
	AuthJSONUpdatedAt   *time.Time          `json:"auth_json_updated_at"`
}
type AccountPage struct {
	Items    []AccountDTO `json:"items"`
	Total    int64        `json:"total"`
	Page     int          `json:"page"`
	PageSize int          `json:"page_size"`
}

var ErrAccountIdentityChanged = errors.New("account identity changed during collection")

func (s *Store) migrateAccountSync() error {
	if err := s.DB.AutoMigrate(&SyncedAccount{}, &AccountSnapshot{}, &AccountSyncState{}, &AccountAuthJSONAudit{}); err != nil {
		return err
	}
	if err := s.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&AccountSyncState{ID: 1, Status: "idle"}).Error; err != nil {
		return err
	}
	return s.EnsureSyncedAccounts(context.Background())
}

func accountDigest(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = fmt.Fprintf(h, "%d:%s", len(part), part)
	}
	return hex.EncodeToString(h.Sum(nil))
}
func remoteIdentity(record Submission) string {
	remote := ""
	if record.RemoteID != nil {
		remote = strings.TrimSpace(*record.RemoteID)
	}
	return accountDigest(idString(record.VendorID), remote, record.Plan, idString(record.ID))
}

func (s *Store) EnsureSyncedAccounts(ctx context.Context) error {
	return s.ensureSyncedAccounts(ctx, 0)
}

func (s *Store) ensureSyncedAccounts(ctx context.Context, userID uint64) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		q := tx.Model(&Submission{}).Table("submissions AS s").Where(`NOT EXISTS (SELECT 1 FROM submissions AS newer WHERE newer.user_id = s.user_id AND newer.vendor_id = s.vendor_id AND LOWER(TRIM(newer.account)) = LOWER(TRIM(s.account)) AND (newer.created_at > s.created_at OR (newer.created_at = s.created_at AND newer.id > s.id)))`)
		if userID != 0 {
			q = q.Where("s.user_id = ?", userID)
		}
		var latestRows []Submission
		if err := q.Select("s.id, s.user_id, s.vendor_id, s.account, s.plan, s.status, s.remote_id, s.platform_account_id, s.created_at").Find(&latestRows).Error; err != nil {
			return err
		}
		for _, latest := range latestRows {
			var current Submission
			err := accountGroup(tx.Model(&Submission{}).Select("id, user_id, vendor_id, account, plan, status, remote_id, platform_account_id, created_at"), latest).Where("status = ? AND remote_id IS NOT NULL AND remote_id <> ?", "accepted", "").Order("created_at DESC, id DESC").First(&current).Error
			if err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
			row := SyncedAccount{AccountKey: accountDigest(idString(latest.UserID), idString(latest.VendorID), strings.ToLower(strings.TrimSpace(latest.Account))), UserID: latest.UserID, VendorID: latest.VendorID, Account: latest.Account, Plan: latest.Plan, LatestSubmissionID: latest.ID, LatestSubmittedAt: latest.CreatedAt, SubmissionStatus: latest.Status, IdentityKey: accountDigest("unconfirmed")}
			if err == nil {
				row.CurrentSubmissionID = &current.ID
				row.Plan, row.RemoteID, row.PlatformAccountID = current.Plan, current.RemoteID, current.PlatformAccountID
				row.IdentityKey = remoteIdentity(current)
			}
			if err = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
				return err
			}
			var existing SyncedAccount
			if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("account_key = ?", row.AccountKey).First(&existing).Error; err != nil {
				return err
			}
			// A slower concurrent refresh must not restore an older submission.
			if existing.LatestSubmittedAt.After(row.LatestSubmittedAt) || (existing.LatestSubmittedAt.Equal(row.LatestSubmittedAt) && existing.LatestSubmissionID > row.LatestSubmissionID) {
				continue
			}
			// A newly accepted submission with the same remote ID and plan is
			// still the same lifetime. A later return to an old ID or plan starts
			// a fresh lifetime and must not revive its historical observations.
			if existing.CurrentSubmissionID != nil && row.CurrentSubmissionID != nil && existing.RemoteID != nil && row.RemoteID != nil && *existing.RemoteID == *row.RemoteID && existing.Plan == row.Plan {
				row.IdentityKey = existing.IdentityKey
			}
			updates := map[string]any{"account": row.Account, "plan": row.Plan, "latest_submission_id": row.LatestSubmissionID, "latest_submitted_at": row.LatestSubmittedAt, "submission_status": row.SubmissionStatus, "current_submission_id": row.CurrentSubmissionID, "remote_id": row.RemoteID, "platform_account_id": row.PlatformAccountID, "identity_key": row.IdentityKey}
			if existing.IdentityKey != row.IdentityKey {
				updates["snapshot_status"], updates["last_success_at"], updates["last_attempt_at"], updates["last_error"] = nil, nil, nil, ""
				updates["supplier_record_id"], updates["auth_json_updated_at"] = nil, nil
			}
			if err = tx.Model(&existing).Updates(updates).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func snapshotDTO(row AccountSnapshot) AccountSnapshotDTO {
	return AccountSnapshotDTO{ID: idString(row.ID), AccountID: idString(row.AccountID), SnapshotDate: row.SnapshotDate, CollectedAt: row.CollectedAt.UTC(), DataSource: row.DataSource, AccountSnapshotValues: row.AccountSnapshotValues}
}
func (s *Store) accountDTO(ctx context.Context, row SyncedAccount) (AccountDTO, error) {
	out := AccountDTO{ID: idString(row.ID), Account: row.Account, Plan: row.Plan, VendorID: idString(row.VendorID), LatestSubmissionID: idString(row.LatestSubmissionID), SubmissionStatus: row.SubmissionStatus, RemoteID: row.RemoteID, PlatformAccountID: row.PlatformAccountID, LastSuccessAt: row.LastSuccessAt, LastAttemptAt: row.LastAttemptAt, LastError: row.LastError, SupplierRecordID: row.SupplierRecordID, AuthJSONUpdatedAt: row.AuthJSONUpdatedAt}
	if row.CurrentSubmissionID != nil {
		v := idString(*row.CurrentSubmissionID)
		out.CurrentSubmissionID = &v
	}
	var vendor Vendor
	if err := s.DB.WithContext(ctx).First(&vendor, row.VendorID).Error; err != nil {
		return out, err
	}
	out.VendorName = vendor.Name
	var snap AccountSnapshot
	err := s.DB.WithContext(ctx).Where("account_id = ? AND identity_key = ?", row.ID, row.IdentityKey).Order("collected_at DESC, id DESC").First(&snap).Error
	if err == nil {
		dto := snapshotDTO(snap)
		out.Snapshot = &dto
		if out.PlatformAccountID == nil || *out.PlatformAccountID == "" {
			out.PlatformAccountID = snap.PlatformAccountID
		}
	} else if !errors.Is(err, ErrNotFound) {
		return out, err
	}
	return out, nil
}

func validAccountFilter(page int, search, status string) error {
	if page < 1 || page > 100000 || len(search) > 254 {
		return validationErrorf("invalid account list filter")
	}
	switch status {
	case "", "all", "active", "offline", "banned", "unknown", "pending", "limited":
	default:
		return validationErrorf("invalid account status")
	}
	return nil
}

func (s *Store) ListSyncedAccounts(ctx context.Context, userID uint64, page int, search, status string) (AccountPage, error) {
	if userID == 0 {
		return AccountPage{}, ErrNotFound
	}
	return s.listSyncedAccounts(ctx, userID, page, search, status, false)
}

// ListAdminSyncedAccounts is only exposed by administrator-authorized handlers.
// It shares observations, filters, pagination and ordering with the owner view.
func (s *Store) ListAdminSyncedAccounts(ctx context.Context, page int, search, status string) (AccountPage, error) {
	return s.listSyncedAccounts(ctx, 0, page, search, status, true)
}

func (s *Store) listSyncedAccounts(ctx context.Context, userID uint64, page int, search, status string, includeOwner bool) (AccountPage, error) {
	out := AccountPage{Items: []AccountDTO{}, Page: page, PageSize: 20}
	if err := validAccountFilter(page, search, status); err != nil {
		return out, err
	}
	if err := s.ensureSyncedAccounts(ctx, userID); err != nil {
		return out, err
	}
	q := s.DB.WithContext(ctx).Model(&SyncedAccount{})
	if !includeOwner {
		q = q.Where("user_id = ?", userID)
	}
	if strings.TrimSpace(search) != "" {
		if includeOwner {
			owners := s.DB.Model(&User{}).Select("id").Where("LOWER(email) LIKE ? ESCAPE '!'", escapedSearch(search))
			q = q.Where("LOWER(account) LIKE ? ESCAPE '!' OR user_id IN (?)", escapedSearch(search), owners)
		} else {
			q = q.Where("LOWER(account) LIKE ? ESCAPE '!'", escapedSearch(search))
		}
	}
	if status == "unknown" {
		q = q.Where("snapshot_status IS NULL OR snapshot_status = ? OR snapshot_status = ?", "", "unknown")
	} else if status != "" && status != "all" {
		q = q.Where("snapshot_status = ?", status)
	}
	if err := q.Count(&out.Total).Error; err != nil {
		return out, err
	}
	var rows []SyncedAccount
	if err := q.Order("latest_submitted_at DESC, id DESC").Limit(20).Offset((page - 1) * 20).Find(&rows).Error; err != nil {
		return out, err
	}
	for _, row := range rows {
		dto, err := s.accountDTO(ctx, row)
		if err == nil && includeOwner {
			err = s.accountOwner(ctx, row.UserID, &dto)
		}
		if err != nil {
			return out, err
		}
		out.Items = append(out.Items, dto)
	}
	return out, nil
}
func (s *Store) SyncedAccountForUser(ctx context.Context, userID, id uint64) (AccountDTO, error) {
	if userID == 0 || id == 0 {
		return AccountDTO{}, ErrNotFound
	}
	if err := s.ensureSyncedAccounts(ctx, userID); err != nil {
		return AccountDTO{}, err
	}
	var row SyncedAccount
	if err := s.DB.WithContext(ctx).Where("id = ? AND user_id = ?", id, userID).First(&row).Error; err != nil {
		return AccountDTO{}, err
	}
	return s.accountDTO(ctx, row)
}

// LookupSyncedAccountForUser resolves one Claude account by its exact stable
// owner/vendor/account identity. An account from another owner, vendor or model
// family is not a fallback match. The result reads the current lifetime's saved
// observation; lookup never contacts the supplier or creates a submission.
func (s *Store) LookupSyncedAccountForUser(ctx context.Context, userID uint64, vendorCode, account string) (AccountDTO, error) {
	if userID == 0 {
		return AccountDTO{}, ErrNotFound
	}
	account = strings.ToLower(strings.TrimSpace(account))
	if account == "" || len(account) > 254 || strings.ContainsAny(account, " \t\r\n\x00") {
		return AccountDTO{}, validationErrorf("enter a valid account name")
	}
	var vendor Vendor
	if err := s.DB.WithContext(ctx).Where("code = ?", vendorCode).First(&vendor).Error; err != nil {
		return AccountDTO{}, err
	}
	if err := s.ensureSyncedAccounts(ctx, userID); err != nil {
		return AccountDTO{}, err
	}
	key := accountDigest(idString(userID), idString(vendor.ID), account)
	var row SyncedAccount
	if err := s.DB.WithContext(ctx).Where("account_key = ? AND user_id = ? AND vendor_id = ?", key, userID, vendor.ID).First(&row).Error; err != nil {
		return AccountDTO{}, err
	}
	if !strings.HasPrefix(row.Plan, "claude/") {
		return AccountDTO{}, ErrNotFound
	}
	return s.accountDTO(ctx, row)
}

// SyncedAccountForAdmin must only be called after the HTTP administrator check.
func (s *Store) SyncedAccountForAdmin(ctx context.Context, id uint64) (AccountDTO, error) {
	if id == 0 {
		return AccountDTO{}, ErrNotFound
	}
	if err := s.ensureSyncedAccounts(ctx, 0); err != nil {
		return AccountDTO{}, err
	}
	var row SyncedAccount
	if err := s.DB.WithContext(ctx).First(&row, id).Error; err != nil {
		return AccountDTO{}, err
	}
	dto, err := s.accountDTO(ctx, row)
	if err == nil {
		err = s.accountOwner(ctx, row.UserID, &dto)
	}
	return dto, err
}

func (s *Store) accountOwner(ctx context.Context, userID uint64, dto *AccountDTO) error {
	var user User
	if err := s.DB.WithContext(ctx).Select("id", "email").First(&user, userID).Error; err != nil {
		return err
	}
	dto.UserID, dto.UserEmail = idString(user.ID), user.Email
	return nil
}

// AccountSnapshots is internal: HTTP handlers must first enforce ownership with
// SyncedAccountForUser or administrator access with SyncedAccountForAdmin.
// Prior remote lifetimes are retained but never joined into
// the currently displayed trend.
func (s *Store) AccountSnapshots(ctx context.Context, accountID uint64, days int) ([]AccountSnapshotDTO, error) {
	out := []AccountSnapshotDTO{}
	if days != 7 && days != 30 && days != 90 {
		return out, validationErrorf("select 7, 30, or 90 days")
	}
	var account SyncedAccount
	if err := s.DB.WithContext(ctx).First(&account, accountID).Error; err != nil {
		return out, err
	}
	var rows []AccountSnapshot
	if err := s.DB.WithContext(ctx).Where("account_id = ? AND identity_key = ? AND collected_at >= ?", accountID, account.IdentityKey, time.Now().UTC().AddDate(0, 0, -days)).Order("collected_at ASC, id ASC").Find(&rows).Error; err != nil {
		return out, err
	}
	for _, row := range rows {
		out = append(out, snapshotDTO(row))
	}
	return out, nil
}

func (s *Store) CollectionTargets(ctx context.Context, userID, accountID uint64) ([]SyncedAccount, error) {
	if err := s.ensureSyncedAccounts(ctx, userID); err != nil {
		return nil, err
	}
	q := s.DB.WithContext(ctx).Model(&SyncedAccount{}).Where("current_submission_id IS NOT NULL")
	q = q.Where("vendor_id IN (?)", s.DB.Model(&Vendor{}).Select("id").Where("enabled = ? AND code = ?", true, "tk-register-0"))
	if userID != 0 {
		q = q.Where("user_id = ?", userID)
	}
	if accountID != 0 {
		q = q.Where("id = ?", accountID)
	}
	var rows []SyncedAccount
	err := q.Order("id ASC").Find(&rows).Error
	return rows, err
}

func validateSnapshot(v AccountSnapshotValues) error {
	if v.PlatformAccountID != nil && (len(*v.PlatformAccountID) > 32 || strings.ContainsAny(*v.PlatformAccountID, "\r\n\x00")) {
		return validationErrorf("invalid platform account ID")
	}
	for _, n := range []*int64{v.InputTokensTotal, v.OutputTokensTotal} {
		if n != nil && *n < 0 {
			return validationErrorf("cumulative tokens cannot be negative")
		}
	}
	for _, n := range []*float64{v.CostTotal, v.Usage5hPercent, v.Usage7dPercent} {
		if n != nil && (math.IsNaN(*n) || math.IsInf(*n, 0) || *n < 0) {
			return validationErrorf("snapshot numbers must be finite and nonnegative")
		}
	}
	for _, n := range []*float64{v.Usage5hPercent, v.Usage7dPercent} {
		if n != nil && *n > 100 {
			return validationErrorf("window usage must be between 0 and 100")
		}
	}
	if v.AccountStatus != nil && (len(*v.AccountStatus) > 80 || strings.ContainsAny(*v.AccountStatus, "\r\n\x00")) {
		return validationErrorf("invalid snapshot status")
	}
	if v.Currency != nil {
		if len(*v.Currency) != 3 {
			return validationErrorf("currency must be an ISO currency code")
		}
		for _, c := range *v.Currency {
			if c < 'A' || c > 'Z' {
				return validationErrorf("currency must be an ISO currency code")
			}
		}
	}
	return nil
}

func (s *Store) RecordAccountSnapshot(ctx context.Context, accountID uint64, expectedIdentity string, values AccountSnapshotValues, at time.Time, timezone string) error {
	if err := validateSnapshot(values); err != nil {
		return err
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil || at.IsZero() {
		return validationErrorf("invalid snapshot time or timezone")
	}
	at = at.UTC()
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var account SyncedAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&account, accountID).Error; err != nil {
			return err
		}
		if account.IdentityKey != expectedIdentity || account.CurrentSubmissionID == nil {
			return ErrAccountIdentityChanged
		}
		snap := AccountSnapshot{AccountID: account.ID, IdentityKey: expectedIdentity, SnapshotDate: at.In(loc).Format("2006-01-02"), CollectedAt: at, DataSource: "website", AccountSnapshotValues: values}
		insert := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&snap)
		if insert.Error != nil {
			return insert.Error
		}
		if insert.RowsAffected == 0 {
			return nil
		}
		// An older request completing late must not clear a newer failure or move
		// the displayed observation timestamp backwards.
		if account.LastAttemptAt != nil && account.LastAttemptAt.After(at) {
			if account.LastSuccessAt == nil || account.LastSuccessAt.Before(at) {
				return tx.Model(&account).Updates(map[string]any{"last_success_at": at, "snapshot_status": values.AccountStatus}).Error
			}
			return nil
		}
		return tx.Model(&account).Updates(map[string]any{"last_success_at": at, "last_attempt_at": at, "last_error": "", "snapshot_status": values.AccountStatus}).Error
	})
}

// Callers supply a safe, user-facing message, not a vendor response or exception
// containing credentials. The database limits this to one compact status line.
func (s *Store) RecordAccountFailure(ctx context.Context, accountID uint64, expectedIdentity, message string, at time.Time) error {
	if at.IsZero() {
		return validationErrorf("invalid collection time")
	}
	message = strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ", "\x00", "").Replace(message))
	if len(message) > 255 {
		message = "Account collection failed. Please check the administrator sync settings."
	}
	if message == "" {
		message = "Account collection failed. Please try again."
	}
	at = at.UTC()
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var account SyncedAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&account, accountID).Error; err != nil {
			return err
		}
		if account.IdentityKey != expectedIdentity {
			return ErrAccountIdentityChanged
		}
		if account.LastAttemptAt != nil && account.LastAttemptAt.After(at) {
			return nil
		}
		return tx.Model(&account).Updates(map[string]any{"last_attempt_at": at, "last_error": message}).Error
	})
}
