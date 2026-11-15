package core

import (
	"context"

	"goapi-tk-register/server/internal/safemessage"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// A reason belongs to one observed state of one accepted account lifetime.
// It must never be applied to a replacement account or a newer observation.
type AccountReasonTarget struct {
	AccountID         uint64
	SnapshotID        uint64
	IdentityKey       string
	SupplierRecordID  string
	Status            string
	UpstreamStatusRaw string
}

func (s *Store) MissingAccountReasons(ctx context.Context, after uint64, limit int) ([]AccountReasonTarget, error) {
	if limit < 1 || limit > 100 {
		return nil, validationErrorf("invalid reason batch size")
	}
	rows := []AccountReasonTarget{}
	err := s.DB.WithContext(ctx).Table("synced_accounts AS a").
		Select("a.id AS account_id, s.id AS snapshot_id, a.identity_key, a.supplier_record_id, s.account_status AS status, s.upstream_status_raw").
		Joins("JOIN vendors AS v ON v.id = a.vendor_id").
		Joins("JOIN account_snapshots AS s ON s.account_id = a.id AND s.identity_key = a.identity_key AND s.collected_at = a.last_success_at").
		Where("a.id > ? AND a.deleted_at IS NULL AND a.deletion_status = '' AND a.current_submission_id IS NOT NULL", after).
		Where("v.enabled = ? AND v.code = ? AND v.base_url = ? AND a.plan LIKE ?", true, "tk-register-0", "https://120.233.254.152:8000", "claude/%").
		Where("a.supplier_record_id IS NOT NULL AND a.supplier_record_id <> '' AND s.upstream_error_message IS NULL AND s.upstream_status_raw IS NOT NULL").
		Where("s.account_status IN ?", []string{"banned", "offline", "sync_failed", "login_failed", "re_login_failed", "bound_failed", "cooking_failed"}).
		Order("a.id ASC").Limit(limit).Scan(&rows).Error
	return rows, err
}

// SaveAccountReason changes only the latest observation's reason, never metrics,
// timestamps or collection errors. All network work finishes before this short
// transaction. The account row lock follows RecordAccountSnapshot's lock order.
func (s *Store) SaveAccountReason(ctx context.Context, target AccountReasonTarget, reason string) (bool, error) {
	saved := false
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var account SyncedAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&account, target.AccountID).Error; err != nil {
			return err
		}
		if !account.AvailableForMutation() || account.CurrentSubmissionID == nil || account.IdentityKey != target.IdentityKey || account.SupplierRecordID == nil || *account.SupplierRecordID != target.SupplierRecordID {
			return nil
		}
		var latest AccountSnapshot
		if err := tx.Where("account_id = ? AND identity_key = ?", account.ID, account.IdentityKey).Order("collected_at DESC, id DESC").First(&latest).Error; err != nil {
			return err
		}
		if latest.ID != target.SnapshotID || latest.UpstreamStatusRaw == nil || *latest.UpstreamStatusRaw != target.UpstreamStatusRaw || latest.AccountStatus == nil || *latest.AccountStatus != target.Status || latest.UpstreamErrorMessage != nil {
			return nil
		}
		result := tx.Model(&AccountSnapshot{}).Where("id = ? AND upstream_error_message IS NULL", latest.ID).UpdateColumn("upstream_error_message", safemessage.Text(reason))
		saved = result.RowsAffected == 1
		return result.Error
	})
	return saved, err
}
