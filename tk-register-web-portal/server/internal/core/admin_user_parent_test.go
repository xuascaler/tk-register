package core

import (
	"context"
	"errors"
	"testing"

	"gorm.io/gorm"
)

func TestAdminUnlinkParentPermissionsHistoryAndRollback(t *testing.T) {
	s, root, _, _ := syncFixture(t)
	ctx := context.Background()
	parent := hierarchyChild(t, s, root, "parent@example.com")
	child := hierarchyChild(t, s, parent, "child@example.com")
	leaf := hierarchyChild(t, s, child, "leaf@example.com")
	admin := linkRoot(t, s, "operator@example.com")
	if err := s.DB.Model(&admin).Update("role", "admin").Error; err != nil {
		t.Fatal(err)
	}
	// Preserve exact uint64 versions across the browser/API boundary.
	child.MembershipVersion = 9007199254740993
	if err := s.DB.Model(&child).Update("membership_version", child.MembershipVersion).Error; err != nil {
		t.Fatal(err)
	}
	listing, err := s.AdminUsers(ctx, 1, child.Email)
	if err != nil || len(listing.Items) != 1 || listing.Items[0].Parent == nil || listing.Items[0].Parent.ID != idString(parent.ID) || listing.Items[0].Parent.Email != parent.Email || listing.Items[0].MembershipVersion != "9007199254740993" {
		t.Fatal(listing, err)
	}
	rejected := func(actor User, auth, target, expectedParent, version uint64, status int) {
		t.Helper()
		_, err := s.AdminUnlinkUserParent(ctx, actor.ID, auth, target, expectedParent, version)
		var ve *VendorError
		if !errors.As(err, &ve) || ve.Status != status {
			t.Fatalf("expected %d, got %v", status, err)
		}
	}
	rejected(parent, parent.AuthVersion, child.ID, parent.ID, child.MembershipVersion, 403)
	rejected(admin, admin.AuthVersion+1, child.ID, parent.ID, child.MembershipVersion, 403)
	rejected(admin, admin.AuthVersion, child.ID, root.ID, child.MembershipVersion, 409)
	rejected(admin, admin.AuthVersion, child.ID, parent.ID, child.MembershipVersion-1, 409)
	rejected(admin, admin.AuthVersion, root.ID, parent.ID, root.MembershipVersion, 409)
	if err := s.DB.Model(&child).Update("role", "admin").Error; err != nil {
		t.Fatal(err)
	}
	rejected(admin, admin.AuthVersion, child.ID, parent.ID, child.MembershipVersion, 403)
	if err := s.DB.Model(&child).Update("is_super_admin", true).Error; err != nil {
		t.Fatal(err)
	}
	rejected(admin, admin.AuthVersion, child.ID, parent.ID, child.MembershipVersion, 403)
	if err := s.DB.Model(&child).Updates(map[string]any{"role": "member", "is_super_admin": false}).Error; err != nil {
		t.Fatal(err)
	}
	before, _ := s.GetUserByID(ctx, parent.ID)
	if err := s.DB.Callback().Create().Before("gorm:create").Register("test:admin_unlink_audit_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "member_link_audits" {
			tx.AddError(errors.New("audit unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	_, err = s.AdminUnlinkUserParent(ctx, admin.ID, admin.AuthVersion, child.ID, parent.ID, child.MembershipVersion)
	s.DB.Callback().Create().Remove("test:admin_unlink_audit_failure")
	if err == nil {
		t.Fatal("audit failure ignored")
	}
	after, _ := s.GetUserByID(ctx, parent.ID)
	saved, _ := s.GetUserByID(ctx, child.ID)
	if after.VisibilityVersion != before.VisibilityVersion || saved.ParentUserID == nil || saved.MembershipVersion != child.MembershipVersion {
		t.Fatal("partial transaction")
	}
	result, err := s.AdminUnlinkUserParent(ctx, admin.ID, admin.AuthVersion, child.ID, parent.ID, child.MembershipVersion)
	if err != nil || result.Parent != nil || result.MembershipVersion != "9007199254740994" {
		t.Fatal(result, err)
	}
	var audit MemberLinkAudit
	if err := s.DB.Where("target_id = ? AND action = ?", child.ID, "unlink").First(&audit).Error; err != nil || audit.ActorID != admin.ID || audit.ParentUserID == nil || *audit.ParentUserID != parent.ID {
		t.Fatal(audit, err)
	}
	after, _ = s.GetUserByID(ctx, parent.ID)
	operator, _ := s.GetUserByID(ctx, admin.ID)
	if after.VisibilityVersion != before.VisibilityVersion+1 || operator.VisibilityVersion != admin.VisibilityVersion {
		t.Fatal("invalidated operator instead of old parent")
	}
	var cross, internal int64
	s.DB.Model(&UserHierarchy{}).Where("ancestor_user_id IN ? AND descendant_user_id IN ?", []uint64{root.ID, parent.ID}, []uint64{child.ID, leaf.ID}).Count(&cross)
	s.DB.Model(&UserHierarchy{}).Where("ancestor_user_id = ? AND descendant_user_id = ?", child.ID, leaf.ID).Count(&internal)
	if cross != 0 || internal != 1 {
		t.Fatal("incorrect subtree", cross, internal)
	}
	rejected(admin, admin.AuthVersion, child.ID, parent.ID, child.MembershipVersion, 409)
	// A remove/re-add cycle to the same parent must still reject the old dialog.
	fresh, _ := s.GetUserByID(ctx, child.ID)
	if _, err := s.LinkExistingMember(ctx, parent.ID, parent.AuthVersion, child.ID, child.AuthVersion, fresh.MembershipVersion); err != nil {
		t.Fatal(err)
	}
	rejected(admin, admin.AuthVersion, child.ID, parent.ID, child.MembershipVersion, 409)
	fresh, _ = s.GetUserByID(ctx, child.ID)
	if err := s.DB.Model(&child).Updates(map[string]any{"role": "admin", "is_super_admin": true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Model(&admin).Update("is_super_admin", true).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdminUnlinkUserParent(ctx, admin.ID, admin.AuthVersion, child.ID, parent.ID, fresh.MembershipVersion); err != nil {
		t.Fatal("super admin cannot unlink admin", err)
	}
}
