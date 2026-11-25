package core

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestUnlinkSubtreeRevokesAncestorsAndPreservesData(t *testing.T) {
	s, root, vendor, protocol := syncFixture(t)
	ctx := context.Background()
	parent := hierarchyChild(t, s, root, "parent@example.com")
	child := hierarchyChild(t, s, parent, "child@example.com")
	leaf := hierarchyChild(t, s, child, "leaf@example.com")
	sibling := hierarchyChild(t, s, root, "sibling@example.com")
	sub := syncSubmission(t, s, leaf.ID, vendor.ID, protocol.ID, "leaf-account@example.com", "claude/cc-pro", "accepted", "555", time.Now())
	var account SyncedAccount
	if err := s.DB.Where("user_id = ?", leaf.ID).First(&account).Error; err != nil {
		t.Fatal(err)
	}
	before := map[uint64]User{}
	for _, u := range []User{root, parent, child, leaf, sibling} {
		saved, err := s.GetUserByID(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		before[u.ID] = *saved
	}
	if _, err := s.UnlinkMember(ctx, parent.ID, parent.AuthVersion, child.ID, child.MembershipVersion); err != nil {
		t.Fatal(err)
	}
	for _, viewer := range []User{root, parent} {
		page, err := s.ListVisibleAccounts(ctx, viewer.ID, 1, "", "", "all")
		if err != nil || page.Total != 0 {
			t.Fatal("removed subtree still listed", page, err)
		}
		if _, err := s.VisibleAccount(ctx, viewer.ID, account.ID); !errors.Is(err, ErrNotFound) {
			t.Fatal("detail access retained", err)
		}
		if _, err := s.VisibleSubmissionDetail(ctx, viewer.ID, sub.ID); !errors.Is(err, ErrNotFound) {
			t.Fatal("history access retained", err)
		}
		if _, err := s.PageSyncTargets(ctx, viewer.ID, []uint64{account.ID}, false); !errors.Is(err, ErrNotFound) {
			t.Fatal("sync access retained", err)
		}
		scope, err := s.ResolveVisibility(ctx, viewer.ID, "all")
		if err != nil {
			t.Fatal(err)
		}
		history, err := s.Submissions(ctx, viewer.ID, 1, "", "", scope)
		if err != nil || history.Total != 0 {
			t.Fatal("history list leaked", history, err)
		}
	}
	if _, err := s.ResolveVisibility(ctx, parent.ID, idString(child.ID)); err == nil {
		t.Fatal("removed source accepted")
	}
	for _, viewer := range []User{child, leaf} {
		if _, err := s.VisibleAccount(ctx, viewer.ID, account.ID); err != nil {
			t.Fatal("internal access removed", err)
		}
	}
	for _, u := range []User{root, parent, child, leaf, sibling} {
		after, err := s.GetUserByID(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		old := before[u.ID]
		if after.PasswordHash != old.PasswordHash || after.AuthVersion != old.AuthVersion || after.Role != old.Role || after.Status != old.Status {
			t.Fatal("user identity changed")
		}
		increment := uint64(0)
		if u.ID == root.ID || u.ID == parent.ID {
			increment = 1
		}
		if after.VisibilityVersion != old.VisibilityVersion+increment {
			t.Fatal("unrelated viewer invalidated", u.ID)
		}
		if u.ID == child.ID {
			if after.ParentUserID != nil || after.MembershipVersion != old.MembershipVersion+1 {
				t.Fatal("child not detached")
			}
		} else if !reflect.DeepEqual(after.ParentUserID, old.ParentUserID) {
			t.Fatal("other relationship changed")
		}
	}
	var savedSub Submission
	var savedAccount SyncedAccount
	s.DB.First(&savedSub, sub.ID)
	s.DB.First(&savedAccount, account.ID)
	if savedSub.UserID != leaf.ID || !reflect.DeepEqual(account, savedAccount) {
		t.Fatal("owned data modified")
	}
	// A freshly issued link can reattach the intact subtree, while an old dialog cannot remove it.
	if _, err := s.LinkExistingMember(ctx, parent.ID, parent.AuthVersion, child.ID, child.AuthVersion, child.MembershipVersion); err == nil {
		t.Fatal("old consent version accepted")
	}
	fresh, _ := s.GetUserByID(ctx, child.ID)
	if _, err := s.LinkExistingMember(ctx, parent.ID, parent.AuthVersion, child.ID, child.AuthVersion, fresh.MembershipVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UnlinkMember(ctx, parent.ID, parent.AuthVersion, child.ID, child.MembershipVersion); err == nil {
		t.Fatal("stale removal accepted after relink")
	}
	if _, err := s.VisibleAccount(ctx, root.ID, account.ID); err != nil {
		t.Fatal("reattached subtree missing", err)
	}
	var audits []MemberLinkAudit
	if err := s.DB.Where("target_id = ?", child.ID).Order("id").Find(&audits).Error; err != nil || len(audits) != 2 || audits[0].Action != "unlink" || audits[1].Action != "link" {
		t.Fatal(audits, err)
	}
}

func TestUnlinkAuthorizationAtomicityAndConcurrentRequests(t *testing.T) {
	s, parent, _, _ := syncFixture(t)
	ctx := context.Background()
	child := hierarchyChild(t, s, parent, "child@example.com")
	leaf := hierarchyChild(t, s, child, "leaf@example.com")
	outsider := linkRoot(t, s, "outsider@example.com")
	for _, tc := range []struct {
		actor, target User
		auth, member  uint64
	}{
		{parent, parent, parent.AuthVersion, parent.MembershipVersion},
		{parent, leaf, parent.AuthVersion, leaf.MembershipVersion},
		{outsider, child, outsider.AuthVersion, child.MembershipVersion},
		{parent, child, parent.AuthVersion + 1, child.MembershipVersion},
		{parent, child, parent.AuthVersion, child.MembershipVersion + 1},
	} {
		if _, err := s.UnlinkMember(ctx, tc.actor.ID, tc.auth, tc.target.ID, tc.member); err == nil {
			t.Fatal("invalid removal allowed")
		}
	}
	before, _ := s.GetUserByID(ctx, parent.ID)
	var queueBefore []OverviewRefreshRequest
	s.DB.Order("user_id").Find(&queueBefore)
	if err := s.DB.Callback().Create().Before("gorm:create").Register("test:unlink_audit_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "member_link_audits" {
			tx.AddError(errors.New("audit unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	_, err := s.UnlinkMember(ctx, parent.ID, parent.AuthVersion, child.ID, child.MembershipVersion)
	s.DB.Callback().Create().Remove("test:unlink_audit_failure")
	if err == nil {
		t.Fatal("audit failure ignored")
	}
	after, _ := s.GetUserByID(ctx, parent.ID)
	savedChild, _ := s.GetUserByID(ctx, child.ID)
	var queueAfter []OverviewRefreshRequest
	s.DB.Order("user_id").Find(&queueAfter)
	if before.VisibilityVersion != after.VisibilityVersion || !reflect.DeepEqual(queueBefore, queueAfter) || savedChild.ParentUserID == nil || savedChild.MembershipVersion != child.MembershipVersion {
		t.Fatal("failed transaction partially committed")
	}
	start, results := make(chan struct{}), make(chan error, 3)
	newLeaf := User{Email: "later@example.com", PasswordHash: "hash", EmailVerifiedAt: time.Now()}
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			_, err := s.UnlinkMember(ctx, parent.ID, parent.AuthVersion, child.ID, child.MembershipVersion)
			results <- err
		}()
	}
	go func() { <-start; results <- s.CreateMemberByUser(ctx, child.ID, child.AuthVersion, &newLeaf) }()
	close(start)
	successes := 0
	for i := 0; i < 3; i++ {
		if <-results == nil {
			successes++
		}
	}
	if successes != 2 {
		t.Fatalf("expected one unlink and one create, got %d", successes)
	}
	var cross, internal int64
	s.DB.Model(&UserHierarchy{}).Where("ancestor_user_id = ? AND descendant_user_id IN ?", parent.ID, []uint64{child.ID, leaf.ID, newLeaf.ID}).Count(&cross)
	s.DB.Model(&UserHierarchy{}).Where("ancestor_user_id = ? AND descendant_user_id IN ?", child.ID, []uint64{child.ID, leaf.ID, newLeaf.ID}).Count(&internal)
	if cross != 0 || internal != 3 {
		t.Fatal("concurrent creation broke closure", cross, internal)
	}
}

func TestMemberAuditMigrationPreservesHistoryAndAllowsRelink(t *testing.T) {
	s, parent, _, _ := syncFixture(t)
	ctx := context.Background()
	child := linkRoot(t, s, "legacy-child@example.com")
	if _, err := s.LinkExistingMember(ctx, parent.ID, parent.AuthVersion, child.ID, child.AuthVersion, child.MembershipVersion); err != nil {
		t.Fatal(err)
	}
	// Recreate the previous schema, including a legacy row without an action column.
	if err := s.DB.Migrator().DropColumn(&MemberLinkAudit{}, "action"); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Exec("CREATE UNIQUE INDEX idx_member_link_audits_target_id ON member_link_audits(target_id)").Error; err != nil {
		t.Fatal(err)
	}
	if s.DB.Migrator().HasIndex(&MemberLinkAudit{}, "idx_member_link_target") {
		if err := s.DB.Migrator().DropIndex(&MemberLinkAudit{}, "idx_member_link_target"); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := s.migrateMembers(); err != nil {
			t.Fatal(err)
		}
	}
	var audit MemberLinkAudit
	if err := s.DB.First(&audit).Error; err != nil || audit.Action != "link" {
		t.Fatal("legacy audit lost", audit, err)
	}
	fresh, _ := s.GetUserByID(ctx, child.ID)
	if _, err := s.UnlinkMember(ctx, parent.ID, parent.AuthVersion, child.ID, fresh.MembershipVersion); err != nil {
		t.Fatal(err)
	}
	fresh, _ = s.GetUserByID(ctx, child.ID)
	if _, err := s.LinkExistingMember(ctx, parent.ID, parent.AuthVersion, child.ID, child.AuthVersion, fresh.MembershipVersion); err != nil {
		t.Fatal(err)
	}
	var count int64
	s.DB.Model(&MemberLinkAudit{}).Count(&count)
	if count != 3 {
		t.Fatal("history missing", count)
	}
}

func TestUnlinkOverviewInvalidationAndLatePublication(t *testing.T) {
	s, users, _, now, _ := overviewSummaryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	root, parent, child := users[0], users[1], users[2]
	for _, u := range users {
		if err := s.BuildOverviewSummaries(ctx, u.ID, false, "2026-11-15", "before", now); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.BuildOverviewSummaries(ctx, root.ID, true, "2026-11-15", "admin-before", now); err != nil {
		t.Fatal(err)
	}
	var billingBefore []AccountDailyUsage
	s.DB.Order("id").Find(&billingBefore)
	var triggered bool
	var unlinkErr error
	// The read snapshot has finished when publication locks its viewer. Commit an
	// actual unlink in this gap, reproducing a build started before revocation.
	db, _ := s.DB.DB()
	db.SetMaxOpenConns(2)
	if err := s.DB.Callback().Query().Before("gorm:query").Register("test:unlink_before_publish", func(tx *gorm.DB) {
		if _, locking := tx.Statement.Clauses["FOR"]; !locking || tx.Statement.Table != "users" || triggered {
			return
		}
		triggered = true
		_, unlinkErr = s.UnlinkMember(ctx, parent.ID, parent.AuthVersion, child.ID, child.MembershipVersion)
	}); err != nil {
		t.Fatal(err)
	}
	err := s.BuildOverviewSummaries(ctx, root.ID, false, "2026-11-15", "late", now)
	s.DB.Callback().Query().Remove("test:unlink_before_publish")
	if !triggered || unlinkErr != nil || !errors.Is(err, ErrOverviewVisibilityChanged) {
		t.Fatal("late publication not fenced", triggered, unlinkErr, err)
	}
	for _, source := range []string{"raw", "modified"} {
		view := WithModelUsageSource(ctx, source)
		out, err := s.ReadOverviewSummary(view, root.ID, false, "all_time", now)
		if err != nil || out.SummaryAvailable == nil || *out.SummaryAvailable || !out.SummaryRefreshing || len(out.Sources) != 0 || len(out.Rows) != 0 {
			t.Fatal("stale personal data returned", source, out, err)
		}
		out, err = s.ReadOverviewSummary(view, root.ID, true, "all_time", now)
		if err != nil || out.SummaryAvailable == nil || !*out.SummaryAvailable {
			t.Fatal("admin pool incorrectly invalidated", err)
		}
	}
	var queue []OverviewRefreshRequest
	s.DB.Order("user_id").Find(&queue)
	if len(queue) != 2 || queue[0].UserID != root.ID || queue[1].UserID != parent.ID {
		t.Fatal("wrong affected users", queue)
	}
	for _, u := range []User{root, parent} {
		if err := s.BuildOverviewSummaries(ctx, u.ID, false, "2026-11-15", "after", now); err != nil {
			t.Fatal(err)
		}
	}
	out := readSummary(t, s, root.ID, false, "all_time", now)
	if summarySource(t, out, idString(parent.ID)).Total != 1 {
		t.Fatal("rebuilt summary retained detached child")
	}
	var pending int64
	s.DB.Model(&OverviewRefreshRequest{}).Count(&pending)
	if pending != 0 {
		t.Fatal("successful refresh not acknowledged")
	}
	var billingAfter []AccountDailyUsage
	s.DB.Order("id").Find(&billingAfter)
	if !reflect.DeepEqual(billingBefore, billingAfter) {
		t.Fatal("billing rewritten")
	}
}
