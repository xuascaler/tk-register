package core

import (
	"context"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm/clause"
)

func TestSyncSecondsMigrationPreservesLegacyAndNewConfiguration(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.DB.Where(clause.Eq{Column: "key", Value: "account_sync_interval_seconds"}).Delete(&Setting{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Create(&Setting{Key: "account_sync_interval_hours", Value: "8", Category: "account_sync", Type: "integer", UpdatedBy: 7}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.seedDefaults(); err != nil {
		t.Fatal(err)
	}
	values, err := s.Settings(ctx)
	if err != nil || values.Int("account_sync_interval_seconds") != 28800 {
		t.Fatalf("legacy conversion: %v %v", values.Int("account_sync_interval_seconds"), err)
	}
	if err = s.PatchSettings(ctx, map[string]string{"account_sync_interval_seconds": "301"}, 1); err != nil {
		t.Fatal(err)
	}
	if err = s.seedDefaults(); err != nil {
		t.Fatal(err)
	}
	values, _ = s.Settings(ctx)
	if values.Int("account_sync_interval_seconds") != 301 {
		t.Fatal("restart overwrote seconds configuration")
	}
	for _, bad := range []string{"0", "59", "604801", "300.5", "0300"} {
		if err = s.PatchSettings(ctx, map[string]string{"account_sync_interval_seconds": bad}, 1); !IsValidationError(err) {
			t.Fatalf("accepted %q", bad)
		}
	}
	for _, valid := range []string{"60", "300", "604800"} {
		if err = s.PatchSettings(ctx, map[string]string{"account_sync_interval_seconds": valid}, 1); err != nil {
			t.Fatalf("rejected %q: %v", valid, err)
		}
	}
	if err = s.PatchSettings(ctx, map[string]string{"account_sync_interval_hours": "1"}, 1); !IsValidationError(err) {
		t.Fatal("legacy write accepted")
	}
}

func scheduleFixture(t *testing.T) (*Store, time.Time) {
	t.Helper()
	s := testStore(t)
	if err := s.PatchSettings(context.Background(), map[string]string{"account_sync_enabled": "true", "account_sync_website_username": "test-user", "account_sync_website_password": "test-password", "account_sync_interval_seconds": "300"}, 1); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Truncate(time.Millisecond)
	if err := s.DB.Model(&AccountSyncState{}).Where("id = ?", 1).Update("next_run_at", base).Error; err != nil {
		t.Fatal(err)
	}
	return s, base
}
func scheduleState(t *testing.T, s *Store) AccountSyncState {
	t.Helper()
	var state AccountSyncState
	if err := s.DB.First(&state, 1).Error; err != nil {
		t.Fatal(err)
	}
	return state
}

func TestScheduledSlotsSkipOverlapAndMissedRuns(t *testing.T) {
	s, base := scheduleFixture(t)
	ctx := context.Background()
	claim, err := s.ClaimScheduledAccountSync(ctx, base.Add(123*time.Microsecond))
	if err != nil || claim == nil {
		t.Fatal(claim, err)
	}
	if claim.StartedAt.Nanosecond()%int(time.Millisecond) != 0 {
		t.Fatal("claim is not MySQL millisecond precision")
	}
	duplicate, err := s.ClaimScheduledAccountSync(ctx, base)
	if err != nil || duplicate != nil {
		t.Fatal("duplicate slot was claimed", err)
	}
	// Another scheduler arrives while the five-minute slot is still occupied.
	overlapping, err := s.ClaimScheduledAccountSync(ctx, base.Add(5*time.Minute))
	if err != nil || overlapping != nil {
		t.Fatal("running round was replaced", err)
	}
	if !scheduleState(t, s).LastStartedAt.Equal(claim.StartedAt) {
		t.Fatal("overlap changed completion guard")
	}
	if err = s.FinishScheduledAccountSync(ctx, *claim, base.Add(7*time.Minute), "", time.Time{}, false); err != nil {
		t.Fatal(err)
	}
	state := scheduleState(t, s)
	if state.NextRunAt == nil || !state.NextRunAt.Equal(base.Add(10*time.Minute)) || state.Status != "succeeded" {
		t.Fatalf("long completion: %+v", state)
	}
	// Restart after several missed slots runs once, then returns to the grid.
	recovered, err := s.ClaimScheduledAccountSync(ctx, base.Add(32*time.Minute))
	if err != nil || recovered == nil {
		t.Fatal("recovery did not claim one round", err)
	}
	duplicate, err = s.ClaimScheduledAccountSync(ctx, base.Add(32*time.Minute))
	if err != nil || duplicate != nil {
		t.Fatal("recovery replayed another missed round", err)
	}
	state = scheduleState(t, s)
	if !state.NextRunAt.Equal(base.Add(35 * time.Minute)) {
		t.Fatal("recovery did not skip backlog", state.NextRunAt)
	}
}

func TestScheduleSaveDuringRunSurvivesCompletionAndRestart(t *testing.T) {
	s, base := scheduleFixture(t)
	ctx := context.Background()
	claim, err := s.ClaimScheduledAccountSync(ctx, base)
	if err != nil || claim == nil {
		t.Fatal(claim, err)
	}
	if err = s.PatchSettings(ctx, map[string]string{"account_sync_interval_seconds": "900"}, 1); err != nil {
		t.Fatal(err)
	}
	saved := scheduleState(t, s)
	if saved.NextRunAt == nil || saved.ScheduleRevision <= claim.ScheduleRevision {
		t.Fatal("save did not reset schedule")
	}
	if err = s.FinishScheduledAccountSync(ctx, *claim, base.Add(time.Minute), "", time.Time{}, false); err != nil {
		t.Fatal(err)
	}
	finished := scheduleState(t, s)
	if !finished.NextRunAt.Equal(*saved.NextRunAt) {
		t.Fatal("old run overwrote saved schedule")
	}
	if err = s.initializeAccountSchedule(ctx); err != nil {
		t.Fatal(err)
	}
	if !scheduleState(t, s).NextRunAt.Equal(*saved.NextRunAt) {
		t.Fatal("restart reset timer")
	}
	if err = s.PatchSettings(ctx, map[string]string{"account_sync_enabled": "false"}, 1); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishScheduledAccountSync(ctx, *claim, base.Add(2*time.Minute), "", time.Time{}, false); err != nil {
		t.Fatal(err)
	}
	if scheduleState(t, s).NextRunAt != nil {
		t.Fatal("old completion reenabled schedule")
	}
}

func TestScheduleCooldownAndBusyRemainOnFutureGrid(t *testing.T) {
	s, base := scheduleFixture(t)
	ctx := context.Background()
	claim, err := s.ClaimScheduledAccountSync(ctx, base)
	if err != nil || claim == nil {
		t.Fatal(claim, err)
	}
	if err = s.FinishScheduledAccountSync(ctx, *claim, base.Add(time.Second), "query limited", base.Add(12*time.Minute), false); err != nil {
		t.Fatal(err)
	}
	state := scheduleState(t, s)
	if state.NextRunAt == nil || !state.NextRunAt.Equal(base.Add(15*time.Minute)) {
		t.Fatal("cooldown deadline not respected", state.NextRunAt)
	}
	next, err := s.ClaimScheduledAccountSync(ctx, *state.NextRunAt)
	if err != nil || next == nil {
		t.Fatal(next, err)
	}
	if err = s.FinishScheduledAccountSync(ctx, *next, base.Add(15*time.Minute+time.Second), "busy", time.Time{}, true); err != nil {
		t.Fatal(err)
	}
	state = scheduleState(t, s)
	if state.Status != "skipped" || state.LastSuccessAt != nil || !state.NextRunAt.Equal(base.Add(20*time.Minute)) {
		t.Fatal("busy slot was queued", state)
	}
	// Finishing the earlier request cannot alter the newer claim.
	if err = s.FinishScheduledAccountSync(ctx, *claim, base.Add(16*time.Minute), "stale", time.Time{}, false); err != nil {
		t.Fatal(err)
	}
	if scheduleState(t, s).LastError != "busy" {
		t.Fatal("stale completion overwrote new result")
	}
}

func TestScheduledConcurrentClaimsAreSingleAndTerminalFinishIsIgnored(t *testing.T) {
	s, base := scheduleFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	claims := make(chan *AccountSyncClaim, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claim, err := s.ClaimScheduledAccountSync(ctx, base)
			claims <- claim
			errs <- err
		}()
	}
	wg.Wait()
	close(claims)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var only *AccountSyncClaim
	count := 0
	for claim := range claims {
		if claim != nil {
			only = claim
			count++
		}
	}
	if count != 1 {
		t.Fatalf("%d concurrent claims", count)
	}
	if err := s.FinishScheduledAccountSync(ctx, *only, base.Add(time.Second), "", time.Time{}, false); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishScheduledAccountSync(ctx, *only, base.Add(2*time.Second), "stale failure", time.Time{}, false); err != nil {
		t.Fatal(err)
	}
	if state := scheduleState(t, s); state.Status != "succeeded" || state.LastError != "" {
		t.Fatal("terminal finish was overwritten", state)
	}
}
