package core

import (
	"context"
	"encoding/json"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Store) migrateOverviewSummaries() error {
	if err := s.DB.AutoMigrate(&OverviewSummary{}, &OverviewSummaryModified{}); err != nil {
		return err
	}
	// Old releases embedded Modified inside Raw, and earlier ones had only
	// Raw. Detect both on open, including after a rollback to an old binary.
	var views []struct {
		UserID   uint64
		ViewType string
	}
	err := s.DB.Table("overview_summaries o").Distinct("o.user_id", "o.view_type").
		Joins("JOIN users u ON u.id=o.user_id").
		Joins("LEFT JOIN overview_summaries_modified m ON m.user_id=o.user_id AND m.view_type=o.view_type AND m.period=o.period").
		Where("m.id IS NULL OR o.payload_json LIKE ? OR u.role <> ? OR u.is_super_admin = ?", `%"modified"%`, "admin", false).
		Order("o.user_id, o.view_type").Scan(&views).Error
	if err != nil {
		return err
	}
	for _, view := range views {
		if err := s.migrateOverviewView(view.UserID, view.ViewType); err != nil {
			return err
		}
	}
	return nil
}

// Keep memory and locks bounded to one viewer and view. Only overview cache
// records are changed. Successful or missing historical bills stay untouched.
func (s *Store) migrateOverviewView(viewerID uint64, view string) error {
	return s.DB.Transaction(func(tx *gorm.DB) error {
		var user User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "role", "is_super_admin").First(&user, viewerID).Error; err != nil {
			return err
		}
		selector := "user_id = ? AND view_type = ?"
		var records []OverviewSummary
		if err := tx.Where(selector, viewerID, view).Order("end_date DESC, generated_at DESC").Find(&records).Error; err != nil || len(records) == 0 {
			return err
		}
		// A demoted user is no longer eligible for an administrative view.
		if view == "admin" && user.Role != "admin" {
			if err := tx.Where(selector, viewerID, view).Delete(&OverviewSummaryModified{}).Error; err != nil {
				return err
			}
			return tx.Where(selector, viewerID, view).Delete(&OverviewSummary{}).Error
		}
		super := user.Role == "admin" && user.IsSuperAdmin
		from := records[0]
		var nested bool
		for _, row := range records {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(row.PayloadJSON), &fields); err != nil {
				return err
			}
			if _, found := fields["modified"]; !found {
				continue
			}
			nested = true
			if super {
				delete(fields, "modified")
				raw, err := json.Marshal(fields)
				if err != nil {
					return err
				}
				if err = tx.Model(&OverviewSummary{}).Where("id = ?", row.ID).UpdateColumn("payload_json", string(raw)).Error; err != nil {
					return err
				}
			}
		}
		// Do not overwrite a newer batch if a parallel build already published it.
		var existing []OverviewSummaryModified
		if err := tx.Select("period", "end_date", "generated_at").Where(selector, viewerID, view).Order("end_date DESC, generated_at DESC").Find(&existing).Error; err != nil {
			return err
		}
		if len(existing) < len(overviewSummaryPeriods) || existing[0].EndDate < from.EndDate ||
			(nested && existing[0].EndDate == from.EndDate && existing[0].GeneratedAt.Before(from.GeneratedAt)) {
			// Legacy counts included empty account-days. Recompute only the cache
			// against stored Modified lines and today's ownership, with a fresh timestamp.
			ctx := context.Background()
			scoped := *s
			scoped.DB = tx
			scope, err := scoped.summaryScope(ctx, viewerID, view == "admin")
			if err != nil {
				return err
			}
			now := time.Now().UTC()
			modified, err := scoped.prepareSummaryRecords(ctx, scope, viewerID, view, "modified", from.EndDate, "modified-split-"+idString(from.ID), now)
			if err != nil {
				return err
			}
			if err = publishOverviewRecords(tx, modifiedOverviewTable, modified, time.Now().UTC()); err != nil {
				return err
			}
		}
		if !super {
			return tx.Where(selector, viewerID, view).Delete(&OverviewSummary{}).Error
		}
		return nil
	})
}
