package core

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func linkRoot(t *testing.T, s *Store, email string) User {
	t.Helper()
	u := User{Email: email, Role: "member", PasswordHash: "preserved-hash", EmailVerifiedAt: time.Now().UTC()}
	if err := s.CreateUser(context.Background(), &u); err != nil {
		t.Fatal(err)
	}
	return u
}

func TestLinkExistingSubtreePreservesOwnershipAndSources(t *testing.T) {
	s, root, vendor, protocol := syncFixture(t)
	ctx := context.Background()
	actor := hierarchyChild(t, s, root, "parent@example.com")
	target := linkRoot(t, s, "existing@example.com")
	grand := hierarchyChild(t, s, target, "hidden@example.com")
	sub := syncSubmission(t, s, grand.ID, vendor.ID, protocol.ID, "vendor-account@example.com", "claude/cc-pro", "accepted", "451", time.Now())
	list, err := s.ListSyncedAccounts(ctx, grand.ID, 1, "", "")
	if err != nil || list.Total != 1 {
		t.Fatal(list, err)
	}
	accountID, err := ParseBusinessID(list.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.LinkExistingMember(ctx, actor.ID, actor.AuthVersion, target.ID, target.AuthVersion); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		viewer User
		source User
	}{{actor, target}, {root, actor}} {
		dto, err := s.VisibleAccount(ctx, tc.viewer.ID, accountID)
		if err != nil || dto.AccountSource == nil || dto.AccountSource.Label != tc.source.Email || dto.CanManage == nil || *dto.CanManage {
			t.Fatal(dto, err)
		}
		raw, _ := json.Marshal(dto)
		if strings.Contains(string(raw), grand.Email) {
			t.Fatal("descendant identity leaked")
		}
		history, err := s.VisibleSubmissionDetail(ctx, tc.viewer.ID, sub.ID)
		if err != nil || history.Submission.AccountSource.Label != tc.source.Email {
			t.Fatal(history, err)
		}
	}
	saved, _ := s.GetUserByID(ctx, target.ID)
	if saved.ParentUserID == nil || *saved.ParentUserID != actor.ID || saved.PasswordHash != target.PasswordHash || saved.AuthVersion != target.AuthVersion || saved.Role != target.Role {
		t.Fatal("identity modified", saved)
	}
	var original Submission
	s.DB.First(&original, sub.ID)
	if original.UserID != grand.ID {
		t.Fatal("ownership moved")
	}
	if _, err := s.SyncedAccountForUser(ctx, actor.ID, accountID); err == nil {
		t.Fatal("owner API scope expanded")
	}
	// New descendants after attachment inherit the whole chain too.
	leaf := hierarchyChild(t, s, grand, "later@example.com")
	var edge UserHierarchy
	if err := s.DB.Where("ancestor_user_id=? AND descendant_user_id=?", root.ID, leaf.ID).First(&edge).Error; err != nil || edge.Depth != 4 || edge.SourceUserID == nil || *edge.SourceUserID != actor.ID {
		t.Fatal(edge, err)
	}
	var audits int64
	s.DB.Model(&MemberLinkAudit{}).Count(&audits)
	if audits != 1 {
		t.Fatal("missing audit")
	}
}

func TestMemberLinkRejectsInvalidRelationshipsAndRollsBack(t *testing.T) {
	s, actor, _, _ := syncFixture(t)
	ctx := context.Background()
	a := linkRoot(t, s, "a-link@example.com")
	b := linkRoot(t, s, "b-link@example.com")
	child := hierarchyChild(t, s, a, "child-link@example.com")
	for _, tc := range []struct {
		from, to User
		version  uint64
	}{{actor, actor, actor.AuthVersion}, {actor, a, a.AuthVersion + 1}, {actor, child, child.AuthVersion}, {child, a, a.AuthVersion}} {
		if _, err := s.LinkExistingMember(ctx, tc.from.ID, tc.from.AuthVersion, tc.to.ID, tc.version); err == nil {
			t.Fatal("invalid relationship accepted")
		}
	}
	s.DB.Model(&b).Update("role", "admin")
	if _, err := s.LinkExistingMember(ctx, actor.ID, actor.AuthVersion, b.ID, b.AuthVersion); err == nil {
		t.Fatal("admin attached")
	}
	s.DB.Model(&b).Updates(map[string]any{"role": "member", "status": "disabled"})
	if _, err := s.LinkExistingMember(ctx, actor.ID, actor.AuthVersion, b.ID, b.AuthVersion); err == nil {
		t.Fatal("disabled attached")
	}
	// Force audit failure after closure insertion; parent and all new edges roll back.
	s.DB.Create(&MemberLinkAudit{ActorID: actor.ID, TargetID: a.ID})
	if _, err := s.LinkExistingMember(ctx, actor.ID, actor.AuthVersion, a.ID, a.AuthVersion); err == nil {
		t.Fatal("expected audit conflict")
	}
	saved, _ := s.GetUserByID(ctx, a.ID)
	if saved.ParentUserID != nil {
		t.Fatal("partial parent commit")
	}
	var count int64
	s.DB.Model(&UserHierarchy{}).Where("ancestor_user_id=? AND descendant_user_id IN ?", actor.ID, []uint64{a.ID, child.ID}).Count(&count)
	if count != 0 {
		t.Fatal("partial closure commit")
	}
}

func TestMemberLinkConcurrentClaimsAndCycle(t *testing.T) {
	for _, cycle := range []bool{false, true} {
		t.Run(map[bool]string{true: "cycle", false: "same-target"}[cycle], func(t *testing.T) {
			s, _, _, _ := syncFixture(t)
			ctx := context.Background()
			a := linkRoot(t, s, "concurrent-a@example.com")
			b := linkRoot(t, s, "concurrent-b@example.com")
			c := linkRoot(t, s, "concurrent-c@example.com")
			pairs := [][2]User{{a, c}, {b, c}}
			if cycle {
				pairs = [][2]User{{a, b}, {b, a}}
			}
			start := make(chan struct{})
			errs := make(chan error, 2)
			var wg sync.WaitGroup
			for _, pair := range pairs {
				wg.Add(1)
				go func(pair [2]User) {
					defer wg.Done()
					<-start
					_, err := s.LinkExistingMember(ctx, pair[0].ID, pair[0].AuthVersion, pair[1].ID, pair[1].AuthVersion)
					errs <- err
				}(pair)
			}
			close(start)
			wg.Wait()
			close(errs)
			success := 0
			for err := range errs {
				if err == nil {
					success++
				}
			}
			if success != 1 {
				t.Fatalf("want exactly one successful link, got %d", success)
			}
		})
	}
}

func TestMemberLinkConcurrentChildCreation(t *testing.T) {
	s, actor, _, _ := syncFixture(t)
	ctx := context.Background()
	target := linkRoot(t, s, "tree@example.com")
	child := User{Email: "concurrent-child@example.com", PasswordHash: "hash", EmailVerifiedAt: time.Now()}
	start := make(chan struct{})
	errs := make(chan error, 2)
	go func() {
		<-start
		_, err := s.LinkExistingMember(ctx, actor.ID, actor.AuthVersion, target.ID, target.AuthVersion)
		errs <- err
	}()
	go func() { <-start; errs <- s.CreateMemberByUser(ctx, target.ID, target.AuthVersion, &child) }()
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	var edge UserHierarchy
	if err := s.DB.Where("ancestor_user_id=? AND descendant_user_id=?", actor.ID, child.ID).First(&edge).Error; err != nil || edge.Depth != 2 {
		t.Fatal(edge, err)
	}
}
