package core

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// A durable, coalesced request per affected viewer. Only personal summaries
// depend on membership. Collection, model allocation and admin scope are unchanged.
type OverviewRefreshRequest struct {
	UserID            uint64    `gorm:"primaryKey;autoIncrement:false"`
	User              User      `gorm:"foreignKey:UserID;constraint:OnDelete:RESTRICT"`
	VisibilityVersion uint64    `gorm:"not null"`
	RequestedAt       time.Time `gorm:"not null"`
	RetryAfter        time.Time `gorm:"not null;index"`
}

func invalidateMemberVisibility(tx *gorm.DB, parentID uint64) error {
	var ids []uint64
	if err := tx.Model(&UserHierarchy{}).Where("descendant_user_id = ?", parentID).Order("ancestor_user_id").Pluck("ancestor_user_id", &ids).Error; err != nil {
		return err
	}
	if len(ids) == 0 {
		return errors.New("parent hierarchy is unavailable")
	}
	now := time.Now().UTC()
	for start := 0; start < len(ids); start += 200 {
		batch := ids[start:min(start+200, len(ids))]
		if err := tx.Model(&User{}).Where("id IN ?", batch).UpdateColumn("visibility_version", gorm.Expr("visibility_version + 1")).Error; err != nil {
			return err
		}
		var viewers []User
		if err := tx.Select("id", "visibility_version").Where("id IN ?", batch).Find(&viewers).Error; err != nil {
			return err
		}
		requests := make([]OverviewRefreshRequest, 0, len(viewers))
		for _, viewer := range viewers {
			requests = append(requests, OverviewRefreshRequest{UserID: viewer.ID, VisibilityVersion: viewer.VisibilityVersion, RequestedAt: now, RetryAfter: now})
		}
		if err := tx.Omit("User").Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}}, DoUpdates: clause.AssignmentColumns([]string{"visibility_version", "requested_at", "retry_after"})}).Create(&requests).Error; err != nil {
			return err
		}
	}
	return nil
}

func memberRelationshipChanged() error {
	return vendorError(409, "MEMBER_RELATION_CHANGED", "This membership has changed. Refresh the member list and try again.")
}

// UnlinkMember removes the boundary between two subtrees. It never deletes
// users/accounts, changes their credentials or severs relationships inside the child.
func (s *Store) UnlinkMember(ctx context.Context, actorID, actorVersion, targetID, membershipVersion uint64) (AdminUser, error) {
	return s.unlinkUserParent(ctx, actorID, actorVersion, targetID, actorID, membershipVersion, false)
}

func (s *Store) AdminUnlinkUserParent(ctx context.Context, actorID, actorVersion, targetID, parentID, membershipVersion uint64) (AdminUser, error) {
	return s.unlinkUserParent(ctx, actorID, actorVersion, targetID, parentID, membershipVersion, true)
}

func (s *Store) unlinkUserParent(ctx context.Context, actorID, actorVersion, targetID, parentID, membershipVersion uint64, admin bool) (AdminUser, error) {
	var out AdminUser
	changed := memberRelationshipChanged
	if admin {
		changed = func() error {
			return vendorError(409, "USER_PARENT_CHANGED", "This user’s direct parent has changed. Refresh the list and try again.")
		}
	}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockUserAccess(tx); err != nil {
			return err
		}
		var actor, target User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&actor, actorID).Error; err != nil {
			return err
		}
		if actor.Status != "active" || actor.AuthVersion != actorVersion {
			return vendorError(403, "FORBIDDEN", "An active signed-in account is required.")
		}
		if admin && actor.Role != "admin" {
			return vendorError(403, "FORBIDDEN", "Administrator access is required.")
		}
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND parent_user_id = ?", targetID, parentID).First(&target).Error
		if errors.Is(err, ErrNotFound) {
			return changed()
		}
		if err != nil {
			return err
		}
		if (!admin && actorID == targetID) || target.MembershipVersion != membershipVersion {
			return changed()
		}
		if admin && (target.Role == "admin" || target.IsSuperAdmin) && !actor.IsSuperAdmin {
			return vendorError(403, "FORBIDDEN", "Super administrator access is required to unlink an administrator.")
		}
		var ancestors, descendants []uint64
		if err := tx.Model(&UserHierarchy{}).Where("descendant_user_id = ?", parentID).Pluck("ancestor_user_id", &ancestors).Error; err != nil {
			return err
		}
		if err := tx.Model(&UserHierarchy{}).Where("ancestor_user_id = ?", target.ID).Pluck("descendant_user_id", &descendants).Error; err != nil {
			return err
		}
		if len(ancestors) == 0 || len(descendants) == 0 {
			return errors.New("user hierarchy is unavailable")
		}
		result := tx.Model(&User{}).Where("id = ? AND parent_user_id = ? AND membership_version = ?", target.ID, parentID, membershipVersion).
			Updates(map[string]any{"parent_user_id": nil, "membership_version": gorm.Expr("membership_version + 1")})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return changed()
		}
		for _, ancestor := range ancestors {
			for start := 0; start < len(descendants); start += 200 {
				if err := tx.Where("ancestor_user_id = ? AND descendant_user_id IN ?", ancestor, descendants[start:min(start+200, len(descendants))]).Delete(&UserHierarchy{}).Error; err != nil {
					return err
				}
			}
		}
		if err := invalidateMemberVisibility(tx, parentID); err != nil {
			return err
		}
		if err := tx.Omit("Actor", "Target").Create(&MemberLinkAudit{ActorID: actor.ID, ParentUserID: &parentID, TargetID: target.ID, Action: "unlink"}).Error; err != nil {
			return err
		}
		target.ParentUserID = nil
		target.MembershipVersion++
		out = adminUser(target)
		return nil
	})
	return out, err
}
