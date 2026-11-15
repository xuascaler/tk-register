package main

import (
	"context"
	"errors"
	"fmt"
	"goapi-tk-register/server/internal/core"
	"goapi-tk-register/server/internal/supplier"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDailyUsageEndpointHonorsOwnerHierarchyAdminAndBounds(t *testing.T) {
	a, owner, cookie, _, rows, _ := setupPageSync(t)
	_, other, _ := vendorSession(t, a, "other-daily@example.com", "member")
	parent, parentCookie, _ := vendorSession(t, a, "parent-daily@example.com", "member")
	if _, err := a.store.LinkExistingMember(context.Background(), parent.ID, parent.AuthVersion, owner.ID, owner.AuthVersion); err != nil {
		t.Fatal(err)
	}
	_, adminCookie, _ := vendorSession(t, a, "daily-admin@example.com", "admin")
	path := fmt.Sprintf("accounts/%d/daily-usage", rows[0].ID)
	requireStatus(t, request(t, a, "GET", path, nil, nil, ""), 401)
	requireStatus(t, request(t, a, "GET", path, nil, other, ""), 404)
	for _, session := range []struct {
		name  string
		admin bool
	}{{"owner", false}, {"ancestor", false}, {"admin", true}} {
		c := cookie
		if session.name == "ancestor" {
			c = parentCookie
		}
		p := path
		if session.admin {
			c = adminCookie
			p = "admin/synced-" + path
		}
		w := request(t, a, "GET", p, nil, c, "")
		requireStatus(t, w, 200)
		for _, secret := range []string{owner.Email, "password", "identity_key", "lease_token"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("private metadata leaked", secret)
			}
		}
	}
	requireStatus(t, request(t, a, "GET", path+"?start_date=2026-01-01&end_date=2026-06-01", nil, cookie, ""), 400)
	requireStatus(t, request(t, a, "POST", path, map[string]any{}, cookie, ""), 403)
}
func TestDailyWorkerRetainsDurableCooldownAndStoresOneDay(t *testing.T) {
	for _, limited := range []bool{false, true} {
		t.Run(fmt.Sprint(limited), func(t *testing.T) {
			a, owner, _, _, rows, _ := setupPageSync(t)
			ctx := context.Background()
			configureSync(t, a, owner.ID, true)
			if err := a.store.DB.Model(&core.SyncedAccount{}).Where("id = ?", rows[0].ID).Update("platform_account_id", "15001").Error; err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile("internal/billing/testdata/claude-bills.xlsx")
			if err != nil {
				t.Fatal(err)
			}
			fake := &fakeBillExporter{raw: raw}
			if limited {
				fake.err = &supplier.Error{Code: supplier.CodeRateLimited, RetryAfterSeconds: 300, Stage: "bill_export", Deferred: true}
			}
			a.billExporter = fake
			if err = a.processDailyUsage(ctx); err != nil {
				t.Fatal(err)
			}
			date := core.DailyUsageDueDate(time.Now()).Format("2006-01-02")
			var job core.DailyUsageJob
			if err = a.store.DB.Where("usage_date = ?", date).First(&job).Error; err != nil {
				t.Fatal(err)
			}
			out, err := a.store.AccountDailyUsage(ctx, rows[0].ID, date, date)
			if err != nil {
				t.Fatal(err)
			}
			if limited {
				if job.Status != "queued" || job.Attempts != 0 || job.NextRunAt.Before(time.Now().Add(250*time.Second)) || len(out.Rows) != 0 {
					t.Fatal("cooldown was not deferred", job)
				}
			} else {
				if job.Status != "succeeded" || len(out.Rows) != 1 || out.Rows[0].CostCNY != "12.34" {
					t.Fatal("daily data not stored", job, out)
				}
			}
			if fake.calls != 1 {
				t.Fatal("unexpected export calls", fake.calls)
			}
		})
	}
}

func TestDailyWorkerBusyExportGateRetriesOffIdleExportCadence(t *testing.T) {
	a, owner, _, _, _, _ := setupPageSync(t)
	configureSync(t, a, owner.ID, true)
	ctx := context.Background()
	if err := a.redis.Set(ctx, redisPrefix+"bill-export:lease", "another-export", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if err := a.processDailyUsage(ctx); !errors.Is(err, errDailyExportBusy) {
		t.Fatalf("want busy lease, got %v", err)
	}
	if dailyUsageDelay(errDailyExportBusy) != time.Second || dailyUsageDelay(nil) != 30*time.Second {
		t.Fatal("worker must break fixed ticker contention")
	}
	var count int64
	a.store.DB.Model(&core.DailyUsageJob{}).Count(&count)
	if count != 0 {
		t.Fatal("waiting for a lease consumed a job attempt")
	}
}
