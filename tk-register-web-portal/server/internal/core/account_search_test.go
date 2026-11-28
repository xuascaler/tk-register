package core

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAdminAccountSearchIncludesSubtreesAndOrdersOwnersBeforePagination(t *testing.T) {
	s, root, vendor, protocol := syncFixture(t)
	ctx := context.Background()
	child := hierarchyChild(t, s, root, "matched-child@example.test")
	grandchild := hierarchyChild(t, s, child, "matched-grandchild@example.test")
	leaf := hierarchyChild(t, s, grandchild, "leaf@example.test")
	sibling := hierarchyChild(t, s, root, "sibling@example.test")
	outsider := linkRoot(t, s, "outsider@example.test")
	base := time.Now().UTC().Add(-time.Hour)
	sequence := 0
	add := func(owner User, account string, at time.Time, status string) AccountDTO {
		t.Helper()
		sequence++
		syncSubmission(t, s, owner.ID, vendor.ID, protocol.ID, account, "claude/cc-pro", "accepted", fmt.Sprint(1000+sequence), at.Add(-time.Duration(sequence)*time.Nanosecond))
		if err := s.DB.Model(&SyncedAccount{}).Where("user_id = ? AND account = ?", owner.ID, account).Updates(map[string]any{"latest_submitted_at": at, "snapshot_status": status}).Error; err != nil {
			t.Fatal(err)
		}
		page, err := s.ListSyncedAccounts(ctx, owner.ID, 1, account, "")
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("fixture account: %+v %v", page, err)
		}
		return page.Items[0]
	}
	// More than one full page of older root-owned accounts must precede all
	// newer descendants. The last two share a timestamp to check the ID tie-break.
	var own []AccountDTO
	for i := 0; i < 21; i++ {
		own = append(own, add(root, fmt.Sprintf("root-%02d@accounts.test", i), base.Add(time.Duration(min(i, 19))*time.Second), "active"))
	}
	b := add(child, "child@accounts.test", base.Add(10*time.Minute), "active")
	c := add(grandchild, "grandchild@accounts.test", base.Add(11*time.Minute), "banned")
	d := add(leaf, "leaf@accounts.test", base.Add(12*time.Minute), "")
	e := add(sibling, "sibling@accounts.test", base.Add(13*time.Minute), "active")
	// Existing account-email matches remain included even without a matching owner.
	extra := add(outsider, root.Email, base.Add(14*time.Minute), "active")
	unrelated := add(outsider, "unrelated@accounts.test", base.Add(15*time.Minute), "active")
	deleted := add(root, "deleted@accounts.test", base.Add(16*time.Minute), "active")
	if err := s.DB.Model(&SyncedAccount{}).Where("id = ?", deleted.ID).Update("deleted_at", time.Now().UTC()).Error; err != nil {
		t.Fatal(err)
	}
	want := make([]string, 0, 26)
	for i := len(own) - 1; i >= 0; i-- {
		want = append(want, own[i].ID)
	}
	want = append(want, extra.ID, e.ID, d.ID, c.ID, b.ID)
	var got []string
	for pageNumber := 1; pageNumber <= 2; pageNumber++ {
		page, err := s.ListAdminSyncedAccounts(ctx, pageNumber, "  "+strings.ToUpper(root.Email)+"  ", "")
		if err != nil || page.Total != int64(len(want)) {
			t.Fatalf("page %d: total=%d err=%v", pageNumber, page.Total, err)
		}
		for _, account := range page.Items {
			got = append(got, account.ID)
			if account.ID == d.ID && account.UserEmail != leaf.Email {
				t.Fatal("descendant attributed to the searched ancestor", account)
			}
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("owner priority, date order, tie-break or pagination incorrect: got %v want %v", got, want)
	}
	check := func(search, status string, ids ...string) {
		t.Helper()
		page, err := s.ListAdminSyncedAccounts(ctx, 1, search, status)
		actual := []string{}
		for _, item := range page.Items {
			actual = append(actual, item.ID)
		}
		if err != nil || page.Total != int64(len(ids)) || len(actual) != len(ids) || (len(ids) > 0 && !reflect.DeepEqual(actual, ids)) {
			t.Fatalf("search %q status %q: got %v total=%d want %v err=%v", search, status, actual, page.Total, ids, err)
		}
	}
	check(child.Email, "", b.ID, d.ID, c.ID)
	// Parent and grandchild both match: their own accounts take priority, and
	// overlapping closure paths must not duplicate the grandchild or leaf.
	check("MATCHED-", "", c.ID, b.ID, d.ID)
	check(root.Email, "banned", c.ID)
	check(root.Email, "unknown", d.ID)
	check("unrelated@accounts.test", "active", unrelated.ID)
	check("%", "")
	check("_", "")
	check("' OR 1=1 --", "")
	check("no-such-user", "")
	// Removing a subtree affects the very next query without cache rebuilding.
	if _, err := s.UnlinkMember(ctx, child.ID, child.AuthVersion, grandchild.ID, grandchild.MembershipVersion); err != nil {
		t.Fatal(err)
	}
	check(child.Email, "", b.ID)
	check(root.Email, "banned")
	check(grandchild.Email, "", c.ID, d.ID)
	// Personal search continues to match account emails within visible scope;
	// matching a user's email must not expand it or expose other users' accounts.
	personal, err := s.ListVisibleAccounts(ctx, root.ID, 1, root.Email, "", "all")
	if err != nil || personal.Total != 0 {
		t.Fatal("personal search semantics changed", personal, err)
	}
	// Empty searches retain the ordinary global chronology, without owner priority.
	page, err := s.ListAdminSyncedAccounts(ctx, 1, "", "")
	if err != nil || page.Items[0].Account != "unrelated@accounts.test" {
		t.Fatal("empty search changed chronological order", page, err)
	}
}

func TestAdminAccountSearchFindsChildrenWhenMatchingUserHasNoAccounts(t *testing.T) {
	s, root, vendor, protocol := syncFixture(t)
	ctx := context.Background()
	child := hierarchyChild(t, s, root, "child-only@example.test")
	syncSubmission(t, s, child.ID, vendor.ID, protocol.ID, "only-account@accounts.test", "claude/cc-pro", "accepted", "3001", time.Now())
	page, err := s.ListAdminSyncedAccounts(ctx, 1, root.Email, "")
	if err != nil || page.Total != 1 || page.Items[0].UserEmail != child.Email {
		t.Fatal("accountless matching parent lost descendants", page, err)
	}
}
