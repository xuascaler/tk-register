package core

import (
	"context"
	"math/big"
	"time"

	"gorm.io/gorm"
)

// ModifiedSubmissionBills projects an already authorized submission onto its
// account's saved model lines. Only the last query range and time are saved. An
// unqueried submission has no saved bill, including submissions with legacy data.
func (s *Store) ModifiedSubmissionBills(ctx context.Context, detail SubmissionDetail, requested *BillsInput) (*BillResult, error) {
	dates := detail.Submission.modifiedBillsRange
	queriedAt := detail.Submission.modifiedBillsQueriedAt
	if requested != nil {
		dates = *requested
		now := time.Now().UTC()
		queriedAt = &now
	} else if queriedAt == nil || dates.StartDS == "" || detail.Submission.Status != "accepted" {
		return nil, nil
	}
	id, err := ParseBusinessID(detail.Submission.ID)
	if err != nil {
		return nil, err
	}
	bill := &BillResult{StartDS: dates.StartDS, EndDS: dates.EndDS, Currency: "CNY", QueriedAt: *queriedAt}
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var record Submission
		if err := tx.Select("id", "user_id", "vendor_id", "account", "plan", "status", "remote_id").First(&record, id).Error; err != nil {
			return err
		}
		if requested != nil {
			scoped := *s
			scoped.DB = tx
			if err := scoped.checkSubmissionNotArchived(ctx, record); err != nil {
				return err
			}
			if record.Status != "accepted" || record.RemoteID == nil {
				return vendorError(409, "ACCOUNT_NOT_ACCEPTED", "Billing queries are available only after the vendor accepts this account.")
			}
			var vendor Vendor
			if err := tx.Select("timezone").First(&vendor, record.VendorID).Error; err != nil {
				return err
			}
			if err := ValidateBills(dates, vendor.Timezone, *queriedAt); err != nil {
				return err
			}
		}
		if record.Status != "accepted" || record.RemoteID == nil {
			return nil
		}
		// After a restore even the same upstream record ID can belong to a new
		// lifetime. Accept the current submission or its proven identity key.
		q := tx.Table("synced_accounts a").
			Joins("JOIN account_usage_days d ON d.account_id = a.id AND d.identity_key = a.identity_key").
			Joins("JOIN account_daily_usages_modifiled m ON m.day_id = d.id").
			Where("a.user_id = ? AND a.vendor_id = ? AND LOWER(TRIM(a.account)) = ? AND a.plan = ? AND a.remote_id = ?", record.UserID, record.VendorID, NormalizeEmail(record.Account), record.Plan, *record.RemoteID).
			Where("a.deleted_at IS NULL AND a.deleted_through_submission_id < ? AND (a.current_submission_id = ? OR a.identity_key = ?)", record.ID, record.ID, remoteIdentity(record)).
			Where("d.last_success_at IS NOT NULL AND d.usage_date BETWEEN ? AND ?", dates.StartDS, dates.EndDS)
		var err error
		bill.Account, err = sumModifiedBill(q, *record.RemoteID)
		if err != nil {
			return err
		}
		if requested == nil {
			return nil
		}
		return tx.Model(&Submission{}).Where("id = ?", id).UpdateColumns(map[string]any{"modified_bills_start_ds": dates.StartDS, "modified_bills_end_ds": dates.EndDS, "modified_bills_queried_at": *queriedAt}).Error
	})
	return bill, err
}

func sumModifiedBill(q *gorm.DB, remoteID string) (*BillAccount, error) {
	rows, err := q.Select("m.cost_cny, m.input_tokens, m.output_tokens, m.cache_read_tokens, m.cache_write_tokens").Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var usage overviewExactUsage
	found := false
	for rows.Next() {
		var row overviewSummaryUsage
		if err := rows.Scan(&row.CostCNY, &row.InputTokens, &row.OutputTokens, &row.CacheReadTokens, &row.CacheWriteTokens); err != nil {
			return nil, err
		}
		if err := usage.add(row); err != nil {
			return nil, err
		}
		found = true
	}
	if err := rows.Err(); err != nil || !found {
		return nil, err
	}
	cost, _ := usage.cost.Float64()
	return &BillAccount{
		ID: remoteID, Cost: &cost,
		PromptTokens: billTokenNumber(&usage.input), CompletionTokens: billTokenNumber(&usage.output),
		CacheReadInputTokens: billTokenNumber(&usage.cacheRead), CacheWriteInputTokens: billTokenNumber(&usage.cacheWrite),
	}, nil
}

func billTokenNumber(count *big.Int) *float64 {
	value, _ := count.Float64()
	return &value
}
