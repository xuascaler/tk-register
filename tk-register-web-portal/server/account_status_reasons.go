package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/go-redis/redis/v8"
	"goapi-tk-register/server/internal/core"
	"goapi-tk-register/server/internal/supplier"
)

const accountReasonLeaseKey = redisPrefix + "account-reasons:lease"
const accountReasonCursorKey = redisPrefix + "account-reasons:cursor"
const accountReasonPoll = 10 * time.Second
const accountReasonBatchSize = 20

type accountReasonReader interface {
	ReadAccountReason(context.Context, string, string, string, string) (*string, error)
}

func (a *app) reasonReader() accountReasonReader {
	if a.accountReasonReader != nil {
		return a.accountReasonReader
	}
	if a.supplierClient != nil {
		return a.supplierClient
	}
	return nil
}
func reasonRetryKey(t core.AccountReasonTarget) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d/%s/%s/%s", t.AccountID, t.IdentityKey, t.SupplierRecordID, t.UpstreamStatusRaw)))
	return fmt.Sprintf("%saccount-reasons:retry:%x", redisPrefix, digest)
}

// The normal sync never takes the reason worker's lease or waits for it. Check
// its actual lease and durable due time, not a hard-coded 300-second interval.
func (a *app) accountReasonsPaused(ctx context.Context) (bool, error) {
	running, err := a.redis.Exists(ctx, accountSyncLeaseKey).Result()
	if err != nil {
		return true, err
	}
	if running > 0 {
		return true, nil
	}
	var state core.AccountSyncState
	if err = a.store.DB.WithContext(ctx).First(&state, 1).Error; err != nil {
		return true, err
	}
	return state.NextRunAt != nil && !state.NextRunAt.After(time.Now().Add(10*time.Second)), nil
}
func (a *app) backfillAccountReasons(parent context.Context) (resultErr error) {
	ctx, cancel := context.WithTimeout(parent, 25*time.Second)
	defer cancel()
	values, err := a.store.Settings(ctx)
	if err != nil {
		return err
	}
	if !core.AccountSyncConfigured(values) || a.reasonReader() == nil {
		return nil
	}
	paused, err := a.accountReasonsPaused(ctx)
	if err != nil || paused {
		return err
	}
	token, err := randomToken()
	if err != nil {
		return err
	}
	locked, err := a.redis.SetNX(ctx, accountReasonLeaseKey, token, 45*time.Second).Result()
	if err != nil || !locked {
		return err
	}
	defer func() {
		release, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_ = releaseSend.Run(release, a.redis, []string{accountReasonLeaseKey}, token).Err()
	}()
	cursor, err := a.redis.Get(ctx, accountReasonCursorKey).Uint64()
	if err != nil && !errors.Is(err, redis.Nil) {
		return err
	}
	targets, err := a.store.MissingAccountReasons(ctx, cursor, accountReasonBatchSize)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return a.redis.Set(ctx, accountReasonCursorKey, 0, 24*time.Hour).Err()
	}
	ctx = accountSyncTrace(ctx, "status_reason_backfill", 0, 0)
	summary, finish := beginSyncDiagnostic(ctx)
	defer func() {
		if resultErr != nil {
			summary.Outcome = "failed"
			summary.ErrorKind = syncDiagnosticError(resultErr)
		}
		finish()
	}()
	summary.Outcome = "succeeded"
	summary.ErrorKind = ""
	for _, target := range targets {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		paused, err = a.accountReasonsPaused(ctx)
		if err != nil || paused {
			return err
		}
		if err = a.redis.Set(ctx, accountReasonCursorKey, target.AccountID, 24*time.Hour).Err(); err != nil {
			return err
		}
		retryKey := reasonRetryKey(target)
		allowed, e := a.redis.SetNX(ctx, retryKey, "waiting", time.Minute).Result()
		if e != nil {
			return e
		}
		if !allowed {
			summary.SkippedCount++
			continue
		}
		readCtx, stop := context.WithTimeout(accountSyncTrace(ctx, "status_reason_backfill", 0, target.AccountID), 8*time.Second)
		reason, readErr := a.reasonReader().ReadAccountReason(readCtx, values.String("account_sync_website_username"), values.String("account_sync_website_password"), target.SupplierRecordID, target.Status)
		stop()
		if readErr == nil && reason == nil {
			readErr = errors.New("missing reason result")
		}
		if readErr != nil {
			summary.FailedCount++
			summary.Outcome = "failed"
			summary.ErrorKind = syncDiagnosticError(readErr)
			seconds, _, _, _ := supplier.RetryDetails(readErr)
			if seconds > 0 || errors.Is(readErr, supplier.ErrReasonSessionUnavailable) {
				return nil
			}
		} else {
			saved, e := a.store.SaveAccountReason(ctx, target, *reason)
			if e != nil {
				return e
			}
			if saved {
				summary.UpdatedCount++
			} else {
				summary.SkippedCount++
			}
			if err = a.redis.Del(ctx, retryKey).Err(); err != nil {
				return err
			}
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	if len(targets) < accountReasonBatchSize {
		return a.redis.Set(ctx, accountReasonCursorKey, 0, 24*time.Hour).Err()
	}
	return nil
}
func (a *app) runAccountReasons(ctx context.Context) {
	for ctx.Err() == nil {
		if err := a.backfillAccountReasons(ctx); err != nil && ctx.Err() == nil && !errors.Is(err, context.DeadlineExceeded) {
			log.Print("Account reasons: background lookup could not complete; will retry later")
		}
		timer := time.NewTimer(accountReasonPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
