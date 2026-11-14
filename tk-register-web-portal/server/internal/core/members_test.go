package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func hierarchyChild(t *testing.T, s *Store, parent User, email string) User {
	t.Helper()
	u := User{Email: email, Role: "admin", PasswordHash: "test-hash", EmailVerifiedAt: time.Now().UTC()}
	if err := s.CreateMemberByUser(context.Background(), parent.ID, parent.AuthVersion, &u); err != nil {
		t.Fatal(err)
	}
	if u.Role != "member" || u.ParentUserID == nil || *u.ParentUserID != parent.ID {
		t.Fatal("incorrect child role/parent")
	}
	return u
}
func TestMemberHierarchyVisibilityAndSourcePrivacy(t *testing.T) {
	s, a, v, p := syncFixture(t)
	ctx := context.Background()
	b := hierarchyChild(t, s, a, "b@example.com")
	c := hierarchyChild(t, s, a, "c@example.com")
	d := hierarchyChild(t, s, b, "hidden-d@example.com")
	e := hierarchyChild(t, s, d, "hidden-e@example.com")
	stranger := User{Email: "stranger@example.com", PasswordHash: "hash", Role: "member", EmailVerifiedAt: time.Now().UTC()}
	if err := s.CreateUser(ctx, &stranger); err != nil {
		t.Fatal(err)
	}
	users := []User{a, b, c, d, e, stranger}
	ids := map[uint64]uint64{}
	subs := map[uint64]uint64{}
	for i, u := range users {
		sub := syncSubmission(t, s, u.ID, v.ID, p.ID, "account-"+idString(u.ID)+"@example.com", "claude/cc-pro", "accepted", idString(u.ID+100), time.Now().Add(time.Duration(i)*time.Second))
		page, err := s.ListSyncedAccounts(ctx, u.ID, 1, "", "")
		if err != nil || page.Total != 1 {
			t.Fatal(page, err)
		}
		ids[u.ID], _ = ParseBusinessID(page.Items[0].ID)
		subs[u.ID] = sub.ID
	}
	for _, tc := range []struct {
		viewer    User
		selection string
		count     int64
	}{{a, "all", 5}, {a, "myself", 1}, {a, idString(b.ID), 3}, {a, idString(c.ID), 1}, {a, "myself," + idString(b.ID), 4}, {a, "none", 0}, {b, "all", 3}, {d, "all", 2}, {stranger, "all", 1}} {
		page, err := s.ListVisibleAccounts(ctx, tc.viewer.ID, 1, "", "", tc.selection)
		if err != nil || page.Total != tc.count {
			t.Fatalf("scope %s %s: count %d want %d err %v", tc.viewer.Email, tc.selection, page.Total, tc.count, err)
		}
	}
	for _, bad := range []string{idString(d.ID), idString(stranger.ID), idString(a.ID), "0", "junk", "myself,"} {
		if _, err := s.ResolveVisibility(ctx, a.ID, bad); err == nil {
			t.Fatalf("accepted invalid source %s", bad)
		}
	}
	for _, u := range []User{d, e} {
		dto, err := s.VisibleAccount(ctx, a.ID, ids[u.ID])
		if err != nil {
			t.Fatal(err)
		}
		if dto.AccountSource == nil || dto.AccountSource.Label != b.Email || dto.CanManage == nil || *dto.CanManage {
			t.Fatalf("bad source: %+v", dto)
		}
		raw, _ := json.Marshal(dto)
		if strings.Contains(string(raw), u.Email) || dto.UserID != "" || dto.UserEmail != "" {
			t.Fatal("hidden owner exposed")
		}
		detail, err := s.VisibleSubmissionDetail(ctx, a.ID, subs[u.ID])
		if err != nil || detail.Submission.AccountSource.Label != b.Email {
			t.Fatal(detail, err)
		}
		if _, err = s.SyncedAccountForUser(ctx, a.ID, ids[u.ID]); !errors.Is(err, ErrNotFound) {
			t.Fatal("owner-only API expanded", err)
		}
	}
	for _, pair := range [][2]uint64{{b.ID, ids[c.ID]}, {c.ID, ids[b.ID]}, {d.ID, ids[a.ID]}, {a.ID, ids[stranger.ID]}} {
		if _, err := s.VisibleAccount(ctx, pair[0], pair[1]); !errors.Is(err, ErrNotFound) {
			t.Fatal("visibility crossed a branch", err)
		}
	}
	sources, err := s.MemberSources(ctx, a.ID)
	if err != nil || len(sources) != 3 || sources[0].Label != "myself" {
		t.Fatal(sources, err)
	}
	direct, err := s.DirectMembers(ctx, a.ID, 1, "")
	if err != nil || direct.Total != 2 {
		t.Fatal(direct, err)
	}
	for _, member := range direct.Items {
		if member.ID == idString(b.ID) && member.VisibleAccounts != 3 {
			t.Fatal("recursive count wrong")
		}
	}
	scope, _ := s.ResolveVisibility(ctx, a.ID, idString(b.ID))
	history, err := s.Submissions(ctx, a.ID, 1, "", "", scope)
	if err != nil || history.Total != 3 {
		t.Fatal(history, err)
	}
	for _, item := range history.Items {
		if item.AccountSource.Label != b.Email {
			t.Fatal("history exposes descendant")
		}
	}
	if _, err = s.PageSyncTargets(ctx, a.ID, []uint64{ids[d.ID]}, false); err != nil {
		t.Fatal("ancestor sync denied", err)
	}
	if _, err = s.PageSyncTargets(ctx, b.ID, []uint64{ids[d.ID], ids[c.ID]}, false); !errors.Is(err, ErrNotFound) {
		t.Fatal("mixed batch authorization failed", err)
	}
	if err = s.DB.Model(&SyncedAccount{}).Where("id = ?", ids[d.ID]).Update("deleted_at", time.Now()).Error; err != nil {
		t.Fatal(err)
	}
	page, err := s.ListVisibleAccounts(ctx, a.ID, 1, "", "", "all")
	if err != nil || page.Total != 4 {
		t.Fatal("deleted account visible", page, err)
	}
	if _, err = s.VisibleSubmissionDetail(ctx, a.ID, subs[d.ID]); err != nil {
		t.Fatal("deleted account history lost", err)
	}
}
func TestMemberMigrationAndCreationAtomicity(t *testing.T) {
	s, a, _, _ := syncFixture(t)
	ctx := context.Background()
	b := hierarchyChild(t, s, a, "child@example.com")
	if err := s.migrateMembers(); err != nil {
		t.Fatal(err)
	}
	if err := s.migrateMembers(); err != nil {
		t.Fatal(err)
	}
	var links int64
	s.DB.Model(&UserHierarchy{}).Count(&links)
	if links != 3 {
		t.Fatalf("non-idempotent closure %d", links)
	}
	duplicate := User{Email: b.Email, PasswordHash: "hash", EmailVerifiedAt: time.Now()}
	if err := s.CreateMemberByUser(ctx, a.ID, a.AuthVersion, &duplicate); err == nil {
		t.Fatal("duplicate accepted")
	}
	var after int64
	s.DB.Model(&UserHierarchy{}).Count(&after)
	if after != links {
		t.Fatal("failed create left closure")
	}
	if err := s.DB.Model(&User{}).Where("id = ?", a.ID).Update("auth_version", a.AuthVersion+1).Error; err != nil {
		t.Fatal(err)
	}
	stale := User{Email: "stale@example.com", PasswordHash: "hash", EmailVerifiedAt: time.Now()}
	if err := s.CreateMemberByUser(ctx, a.ID, a.AuthVersion, &stale); err == nil {
		t.Fatal("stale actor accepted")
	}
	var n int64
	s.DB.Model(&User{}).Where("email = ?", stale.Email).Count(&n)
	if n != 0 {
		t.Fatal("stale actor wrote user")
	}
	// Simulate a pre-upgrade root, without inferring any parent relationship.
	old := User{Email: "legacy@example.com", PasswordHash: "hash", Role: "member", Status: "active", EmailVerifiedAt: time.Now()}
	if err := s.DB.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.migrateMembers(); err != nil {
		t.Fatal(err)
	}
	var row UserHierarchy
	if err := s.DB.Where("ancestor_user_id = ? AND descendant_user_id = ?", old.ID, old.ID).First(&row).Error; err != nil || row.Depth != 0 || row.SourceUserID != nil {
		t.Fatal(row, err)
	}
}
