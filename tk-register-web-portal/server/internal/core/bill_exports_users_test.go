package core

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func billExportRoot(t *testing.T, s *Store, email string) User {
	t.Helper()
	u := User{Email: email, PasswordHash: "test", Role: "member", Status: "active", EmailVerifiedAt: time.Now().UTC()}
	if err := s.CreateUser(context.Background(), &u); err != nil {
		t.Fatal(err)
	}
	return u
}

func assertBillExportUsers(t *testing.T, job BillExportJob, expected []User) {
	t.Helper()
	if len(job.Users) != len(expected) {
		t.Fatalf("selected users missing: %+v", job.Users)
	}
	for i, user := range expected {
		got := job.Users[i]
		if got.UserID != user.ID || got.Email != user.Email || got.JobID != job.ID || got.ID == 0 || (i > 0 && got.ID <= job.Users[i-1].ID) {
			t.Fatalf("selected user order/snapshot %d: %+v, expected %+v", i, got, user)
		}
	}
}

func TestBillExportMultipleUsersUnionsOverlappingTreesAndFreezesScope(t *testing.T) {
	s, owner, admin, vendor, version, now := billExportFixture(t)
	ctx := context.Background()
	child := hierarchyChild(t, s, owner, "multi-child@example.com")
	grandchild := hierarchyChild(t, s, child, "multi-grandchild@example.com")
	other := billExportRoot(t, s, "multi-other@example.com")
	otherChild := hierarchyChild(t, s, other, "multi-other-child@example.com")
	empty := billExportRoot(t, s, "multi-empty@example.com")
	owners := map[string]string{}
	for i, user := range []User{owner, child, grandchild, other, otherChild, admin} {
		email := fmt.Sprintf("multi-account-%d@example.com", i)
		syncSubmission(t, s, user.ID, vendor.ID, version.ID, email, "claude/cc-pro", "accepted", fmt.Sprint(5000+i), now.Add(time.Duration(i)*time.Second))
		owners[email] = user.Email
	}
	selectedIDs := []uint64{child.ID, owner.ID, other.ID, child.ID, empty.ID, owner.ID}
	wantUsers := []User{child, owner, other, empty}
	all, err := s.CreateBillExportForUsers(ctx, admin.ID, selectedIDs, "2026-11-10", "2026-11-11", now, BillExportOptions{IncludeSubUsers: true, UsageSource: "raw"})
	if err != nil || all.AccountCount != 5 || all.UsageSource != "modified" || all.TargetUserID != child.ID || all.UserEmail != child.Email {
		t.Fatalf("overlapping root union or ordinary-admin source incorrect: %+v %v", all, err)
	}
	assertBillExportUsers(t, all, wantUsers)
	accounts, err := s.BillExportAccounts(ctx, all.ID)
	if err != nil || len(accounts) != 5 {
		t.Fatalf("account snapshot: %+v %v", accounts, err)
	}
	seen := map[uint64]bool{}
	for _, account := range accounts {
		if seen[account.LocalAccountID] || account.Username != owners[account.AccountEmail] || account.Username == admin.Email {
			t.Fatalf("overlapping visibility duplicated an account or changed its owner: %+v", account)
		}
		seen[account.LocalAccountID] = true
	}
	direct, err := s.CreateBillExportForUsers(ctx, admin.ID, selectedIDs, "2026-11-10", "2026-11-11", now)
	if err != nil || direct.AccountCount != 3 || direct.IncludeSubUsers {
		t.Fatalf("default scope included unselected descendants: %+v %v", direct, err)
	}
	assertBillExportUsers(t, direct, wantUsers)

	lateChild := hierarchyChild(t, s, otherChild, "multi-late@example.com")
	syncSubmission(t, s, lateChild.ID, vendor.ID, version.ID, "multi-late-account@example.com", "claude/cc-pro", "accepted", "6000", now.Add(time.Minute))
	if err := s.DB.Model(&User{}).Where("id = ?", child.ID).Update("email", "multi-renamed@example.com").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Model(&SyncedAccount{}).Where("user_id = ?", child.ID).Update("user_id", admin.ID).Error; err != nil {
		t.Fatal(err)
	}
	for _, read := range []func(context.Context, uint64, uint64) (BillExportJob, error){s.BillExport, s.BillExportData} {
		job, err := read(ctx, admin.ID, all.ID)
		if err != nil {
			t.Fatal(err)
		}
		assertBillExportUsers(t, job, wantUsers)
	}
	recent, err := s.RecentBillExports(ctx, admin.ID)
	if err != nil || len(recent) != 2 {
		t.Fatalf("metadata read: %+v %v", recent, err)
	}
	for _, job := range recent {
		assertBillExportUsers(t, job, wantUsers)
	}
	claimed, err := s.ClaimBillExport(ctx, now, now.Add(time.Minute), "multi-user-worker-token")
	if err != nil || claimed == nil || claimed.ID != all.ID {
		t.Fatalf("worker claim: %+v %v", claimed, err)
	}
	assertBillExportUsers(t, *claimed, wantUsers)
	claimed, err = s.ClaimBillExport(ctx, now.Add(2*time.Minute), now.Add(3*time.Minute), "multi-user-worker-retry")
	if err != nil || claimed == nil || claimed.ID != all.ID {
		t.Fatalf("worker retry: %+v %v", claimed, err)
	}
	assertBillExportUsers(t, *claimed, wantUsers)
	frozen, err := s.BillExportAccounts(ctx, all.ID)
	if err != nil || len(frozen) != 5 {
		t.Fatalf("later membership altered the frozen account list: %+v %v", frozen, err)
	}
	for _, account := range frozen {
		if !seen[account.LocalAccountID] || account.Username != owners[account.AccountEmail] {
			t.Fatalf("later transfer/email change altered the frozen owner: %+v", account)
		}
	}
}

func TestBillExportManyUsersDedupAndAtomicValidation(t *testing.T) {
	s, owner, admin, vendor, version, now := billExportFixture(t)
	ctx := context.Background()
	syncSubmission(t, s, owner.ID, vendor.ID, version.ID, "multi-limit-account@example.com", "claude/cc-pro", "accepted", "7000", now)
	users := []User{owner}
	for i := 0; i < 240; i++ {
		users = append(users, billExportRoot(t, s, fmt.Sprintf("multi-limit-%d@example.com", i)))
	}
	ids := []uint64{}
	for _, user := range users {
		ids = append(ids, user.ID, user.ID)
	}
	job, err := s.CreateBillExportForUsers(ctx, admin.ID, ids, "2026-11-10", "2026-11-11", now)
	if err != nil || job.AccountCount != 1 {
		t.Fatalf("selection above twenty must work; empty roots must not discard other accounts: %+v %v", job, err)
	}
	assertBillExportUsers(t, job, users)
	saved, err := s.BillExport(ctx, admin.ID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBillExportUsers(t, saved, users)

	for _, tc := range []struct {
		name     string
		actor    uint64
		ids      []uint64
		notFound bool
	}{
		{"empty selection", admin.ID, nil, false},
		{"zero user", admin.ID, []uint64{owner.ID, 0}, false},
		{"missing selected user", admin.ID, []uint64{owner.ID, 999999999}, true},
		{"all selected users have no accounts", admin.ID, []uint64{users[1].ID, users[2].ID}, false},
		{"non-admin actor", owner.ID, []uint64{owner.ID, users[1].ID}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.CreateBillExportForUsers(ctx, tc.actor, tc.ids, "2026-11-10", "2026-11-11", now)
			if tc.notFound && !errors.Is(err, ErrNotFound) || !tc.notFound && !IsValidationError(err) {
				t.Fatalf("invalid selection accepted: %v", err)
			}
			var jobs, savedUsers, accounts int64
			for _, pair := range []struct {
				model any
				count *int64
			}{{&BillExportJob{}, &jobs}, {&BillExportUser{}, &savedUsers}, {&BillExportAccount{}, &accounts}} {
				if err := s.DB.Model(pair.model).Count(pair.count).Error; err != nil {
					t.Fatal(err)
				}
			}
			if jobs != 1 || savedUsers != int64(len(users)) || accounts != 1 {
				t.Fatalf("invalid request left a partial job/snapshot: jobs=%d users=%d accounts=%d", jobs, savedUsers, accounts)
			}
		})
	}
	partial, err := s.CreateBillExportForUsers(ctx, admin.ID, []uint64{users[1].ID, owner.ID}, "2026-11-10", "2026-11-11", now)
	if err != nil || partial.AccountCount != 1 || partial.TargetUserID != users[1].ID {
		t.Fatalf("first selected user without accounts must not prevent export: %+v %v", partial, err)
	}
	assertBillExportUsers(t, partial, []User{users[1], owner})
}

func TestBillExportSelectedUsersMigrationKeepsLegacyJobIntact(t *testing.T) {
	s, owner, admin, vendor, version, now := billExportFixture(t)
	ctx := context.Background()
	syncSubmission(t, s, owner.ID, vendor.ID, version.ID, "multi-legacy@example.com", "claude/cc-pro", "accepted", "8000", now)
	job, err := s.CreateBillExport(ctx, admin.ID, owner.ID, "2026-11-10", "2026-11-11", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Model(&BillExportJob{}).Where("id = ?", job.ID).Updates(map[string]any{"report_json": `{"legacy":true}`, "artifact": []byte("PK-legacy"), "status": "completed"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Migrator().DropTable(&BillExportUser{}); err != nil {
		t.Fatal(err)
	}
	if err := s.migrateBillExports(); err != nil {
		t.Fatal(err)
	}
	legacy, err := s.BillExportData(ctx, admin.ID, job.ID)
	if err != nil || len(legacy.Users) != 0 || legacy.TargetUserID != owner.ID || legacy.UserEmail != owner.Email || legacy.ReportJSON != `{"legacy":true}` || string(legacy.Artifact) != "PK-legacy" {
		t.Fatalf("migration rewrote legacy job instead of preserving interface fallback: %+v %v", legacy, err)
	}
	fresh, err := s.CreateBillExport(ctx, admin.ID, owner.ID, "2026-11-10", "2026-11-11", now)
	if err != nil {
		t.Fatal(err)
	}
	assertBillExportUsers(t, fresh, []User{owner})
}
