package core

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The new key itself is the migration marker. Its insertion and legacy-value
// conversion are atomic, so a restart never restores an administrator's old value.
func migrateSyncIntervalSeconds(tx *gorm.DB) error {
	var current Setting
	err := tx.Where(clause.Eq{Column: "key", Value: "account_sync_interval_seconds"}).First(&current).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	seconds := 300
	var legacy Setting
	err = tx.Where(clause.Eq{Column: "key", Value: "account_sync_interval_hours"}).First(&legacy).Error
	if err == nil {
		hours, parseErr := strconv.Atoi(legacy.Value)
		if parseErr != nil || hours < 1 || hours > 168 {
			return errors.New("invalid legacy account sync interval")
		}
		seconds = hours * 3600
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	row := Setting{Key: "account_sync_interval_seconds", Value: strconv.Itoa(seconds), Category: "account_sync", Type: "integer", UpdatedBy: legacy.UpdatedBy}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
}

func AccountSyncConfigured(values Values) bool {
	return values.Bool("account_sync_enabled") && strings.TrimSpace(values.String("account_sync_website_username")) != "" && values.String("account_sync_website_password") != ""
}

func AccountSyncInterval(values Values) time.Duration {
	seconds := values.Int("account_sync_interval_seconds")
	if seconds < 60 || seconds > 604800 {
		seconds = 300
	}
	return time.Duration(seconds) * time.Second
}

// nextCollectionSlot preserves the original grid while skipping every elapsed
// slot. It always returns a future time, preventing catch-up bursts.
func nextCollectionSlot(slot, after time.Time, interval time.Duration) time.Time {
	if slot.After(after) {
		return slot
	}
	return slot.Add((after.Sub(slot)/interval + 1) * interval)
}

func collectionSlotAtOrAfter(slot, limit time.Time, interval time.Duration) time.Time {
	if !slot.Before(limit) {
		return slot
	}
	steps := (limit.Sub(slot) + interval - 1) / interval
	return slot.Add(steps * interval)
}

func resetAccountSchedule(tx *gorm.DB, values Values, now time.Time) error {
	var next *time.Time
	if AccountSyncConfigured(values) {
		at := now.UTC().Truncate(time.Millisecond).Add(AccountSyncInterval(values))
		next = &at
	}
	return tx.Model(&AccountSyncState{}).Where("id = ?", 1).Updates(map[string]any{
		"next_run_at": next, "schedule_revision": gorm.Expr("schedule_revision + 1"),
	}).Error
}

func (s *Store) initializeAccountSchedule(ctx context.Context) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		values, err := s.values(tx.Clauses(clause.Locking{Strength: "UPDATE"}))
		if err != nil {
			return err
		}
		var state AccountSyncState
		if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&state, 1).Error; err != nil {
			return err
		}
		if state.ScheduleRevision != 0 {
			return nil
		}
		var next *time.Time
		if AccountSyncConfigured(values) {
			at := time.Now().UTC().Truncate(time.Millisecond)
			if state.LastStartedAt != nil {
				at = state.LastStartedAt.Add(AccountSyncInterval(values))
			}
			next = &at
		}
		return tx.Model(&state).Updates(map[string]any{"next_run_at": next, "schedule_revision": 1}).Error
	})
}

type AccountSyncClaim struct {
	StartedAt        time.Time
	ScheduleRevision uint64
}

// ClaimScheduledAccountSync consumes at most one due slot in a transaction.
// This happens before the shared Redis work lease: a competing scheduler cannot
// skip the slot that another scheduler is still trying to acquire.
func (s *Store) ClaimScheduledAccountSync(ctx context.Context, now time.Time) (*AccountSyncClaim, error) {
	var claim *AccountSyncClaim
	now = now.UTC().Truncate(time.Millisecond)
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		values, err := s.values(tx.Clauses(clause.Locking{Strength: "UPDATE"}))
		if err != nil {
			return err
		}
		var state AccountSyncState
		if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&state, 1).Error; err != nil {
			return err
		}
		if !AccountSyncConfigured(values) || state.NextRunAt == nil || now.Before(*state.NextRunAt) {
			return nil
		}
		next := nextCollectionSlot(*state.NextRunAt, now, AccountSyncInterval(values))
		// Another full collection may still own the shared lease. Skip this slot
		// without replacing its completion guard. A crashed claim is recoverable
		// after the maximum shared lease lifetime.
		if state.Status == "running" && state.LastStartedAt != nil && now.Before(state.LastStartedAt.Add(7*time.Minute)) {
			return tx.Model(&state).Update("next_run_at", next).Error
		}
		if err = tx.Model(&state).Updates(map[string]any{
			"last_started_at": now, "last_finished_at": nil, "last_error": "", "status": "running", "next_run_at": next,
		}).Error; err != nil {
			return err
		}
		claim = &AccountSyncClaim{StartedAt: now, ScheduleRevision: state.ScheduleRevision}
		return nil
	})
	return claim, err
}

// Finalization reads the current schedule, rather than writing the claimed
// schedule back. An interval edited during a run therefore retains its new grid.
// The started_at guard also prevents a stale worker overwriting a later claim.
func (s *Store) FinishScheduledAccountSync(ctx context.Context, claim AccountSyncClaim, at time.Time, message string, retryAt time.Time, skipped bool) error {
	at = at.UTC().Truncate(time.Millisecond)
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		values, err := s.values(tx.Clauses(clause.Locking{Strength: "UPDATE"}))
		if err != nil {
			return err
		}
		var state AccountSyncState
		if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&state, 1).Error; err != nil {
			return err
		}
		if state.Status != "running" || state.LastStartedAt == nil || !state.LastStartedAt.Equal(claim.StartedAt.UTC().Truncate(time.Millisecond)) {
			return nil
		}
		updates := map[string]any{"last_finished_at": at, "last_error": message, "status": "failed"}
		if skipped {
			updates["status"] = "skipped"
		} else if message == "" {
			updates["status"], updates["last_success_at"] = "succeeded", at
		}
		if AccountSyncConfigured(values) && state.NextRunAt != nil {
			next := nextCollectionSlot(*state.NextRunAt, at, AccountSyncInterval(values))
			if !retryAt.IsZero() {
				next = collectionSlotAtOrAfter(next, retryAt, AccountSyncInterval(values))
			}
			updates["next_run_at"] = next
		}
		return tx.Model(&state).Updates(updates).Error
	})
}
