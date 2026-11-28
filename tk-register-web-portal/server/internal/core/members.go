package core

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// A closure row records one ancestor's visibility of one user. SourceUserID is
// the immediate child on that path, not the descendant who owns the account.
// Self rows have depth zero and a NULL source. Mutations share the user access guard.
type UserHierarchy struct {
	AncestorUserID   uint64  `gorm:"primaryKey;autoIncrement:false;index:idx_hierarchy_source,priority:1"`
	AncestorUser     User    `gorm:"foreignKey:AncestorUserID;constraint:OnDelete:RESTRICT"`
	DescendantUserID uint64  `gorm:"primaryKey;autoIncrement:false;index"`
	DescendantUser   User    `gorm:"foreignKey:DescendantUserID;constraint:OnDelete:RESTRICT"`
	SourceUserID     *uint64 `gorm:"index:idx_hierarchy_source,priority:2"`
	SourceUser       *User   `gorm:"foreignKey:SourceUserID;constraint:OnDelete:RESTRICT"`
	Depth            int     `gorm:"not null"`
}

func (UserHierarchy) TableName() string { return "user_hierarchy" }

func (s *Store) migrateMembers() error {
	if err := s.DB.AutoMigrate(&UserHierarchy{}, &MemberLinkAudit{}, &OverviewRefreshRequest{}); err != nil {
		return err
	}
	// Keep the new non-unique index (including for the foreign key) before
	// dropping the old once-per-target audit constraint. History survives relinks.
	if s.DB.Migrator().HasIndex(&MemberLinkAudit{}, "idx_member_link_audits_target_id") {
		if err := s.DB.Migrator().DropIndex(&MemberLinkAudit{}, "idx_member_link_audits_target_id"); err != nil {
			return err
		}
	}
	// Existing users become independent roots. Do not infer a parent from an
	// administrator, registration IP, shared vendor, or previous account transfer.
	return s.DB.Exec(`INSERT INTO user_hierarchy (ancestor_user_id, descendant_user_id, depth)
        SELECT id, id, 0 FROM users WHERE NOT EXISTS
        (SELECT 1 FROM user_hierarchy h WHERE h.ancestor_user_id = users.id AND h.descendant_user_id = users.id)`).Error
}

func (s *Store) CreateMemberByUser(ctx context.Context, actorID, actorVersion uint64, user *User) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockUserAccess(tx); err != nil {
			return err
		}
		var actor User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&actor, actorID).Error; err != nil {
			return err
		}
		if actor.Status != "active" || actor.AuthVersion != actorVersion {
			return vendorError(403, "FORBIDDEN", "An active signed-in account is required.")
		}
		user.Role, user.Status = "member", "active"
		parent := actor.ID
		user.ParentUserID = &parent
		user.MembershipVersion = 1
		scoped := *s
		scoped.DB = tx
		if err := scoped.createHierarchyUser(ctx, user); err != nil {
			return err
		}
		return invalidateMemberVisibility(tx, actor.ID)
	})
}

// Serialize hierarchy mutations with role changes. All callers acquire this
// guard before user rows, and never hold it while sending mail or calling Redis.
// Child creation must share the guard so a concurrent subtree attachment cannot
// omit the new descendant. The existing singleton also works across replicas.
func lockUserAccess(tx *gorm.DB) error {
	result := tx.Model(&AdminRoleGuard{}).Where("id = ?", 1).UpdateColumn("revision", gorm.Expr("revision + ?", 1))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("user access guard is unavailable")
	}
	return nil
}

// No passwords or verification codes are stored in this audit.
type MemberLinkAudit struct {
	// Nullable for legacy history. The parent can differ from the administrator acting.
	ParentUserID *uint64 `gorm:"index"`
	ID           uint64  `gorm:"primaryKey"`
	ActorID      uint64  `gorm:"not null;index"`
	Actor        User    `gorm:"foreignKey:ActorID;constraint:OnDelete:RESTRICT"`
	TargetID     uint64  `gorm:"not null;index:idx_member_link_target"`
	Target       User    `gorm:"foreignKey:TargetID;constraint:OnDelete:RESTRICT"`
	Action       string  `gorm:"size:16;not null;default:link"`
	CreatedAt    time.Time
}

func linkTarget(db *gorm.DB, actorID uint64, email string) (*User, error) {
	var target User
	if err := db.Where("email = ?", NormalizeEmail(email)).First(&target).Error; err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, vendorError(400, "MEMBER_NOT_ELIGIBLE", "Use an existing active member's email address.")
		}
		return nil, err
	}
	if target.ID == actorID {
		return nil, vendorError(409, "MEMBER_SELF", "You cannot add yourself as a sub account.")
	}
	if target.Status != "active" || target.Role != "member" || target.EmailVerifiedAt.IsZero() {
		return nil, vendorError(409, "MEMBER_NOT_ELIGIBLE", "Only an active, verified member can be added. Administrator accounts cannot be added.")
	}
	if target.ParentUserID != nil {
		if *target.ParentUserID == actorID {
			return nil, vendorError(409, "MEMBER_ALREADY_LINKED", "This user is already your direct member.")
		}
		return nil, vendorError(409, "MEMBER_HAS_PARENT", "This user already has a parent and cannot be added here.")
	}
	var count int64
	if err := db.Model(&UserHierarchy{}).Where("ancestor_user_id = ? AND descendant_user_id = ?", target.ID, actorID).Count(&count).Error; err != nil {
		return nil, err
	}
	if count != 0 {
		return nil, vendorError(409, "MEMBER_CYCLE", "An ancestor cannot be added as your sub account.")
	}
	return &target, nil
}

func (s *Store) MemberLinkTarget(ctx context.Context, actorID uint64, email string) (*User, error) {
	return linkTarget(s.DB.WithContext(ctx), actorID, email)
}

// The caller must first consume a dedicated code bound to both users and their
// auth versions and the target membership version. Revalidate inside the link transaction.
func (s *Store) LinkExistingMember(ctx context.Context, actorID, actorVersion, targetID, targetVersion, membershipVersion uint64) (AdminUser, error) {
	var out AdminUser
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockUserAccess(tx); err != nil {
			return err
		}
		var actor, candidate User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&actor, actorID).Error; err != nil {
			return err
		}
		if actor.Status != "active" || actor.AuthVersion != actorVersion {
			return vendorError(403, "FORBIDDEN", "An active signed-in account is required.")
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&candidate, targetID).Error; err != nil {
			return err
		}
		if candidate.AuthVersion != targetVersion || candidate.MembershipVersion != membershipVersion {
			return vendorError(409, "MEMBER_CHANGED", "This user's access changed. Request a new verification code.")
		}
		target, err := linkTarget(tx, actorID, candidate.Email)
		if err != nil {
			return err
		}
		var ancestors, descendants []UserHierarchy
		if err := tx.Where("descendant_user_id = ?", actorID).Find(&ancestors).Error; err != nil {
			return err
		}
		if err := tx.Where("ancestor_user_id = ?", target.ID).Find(&descendants).Error; err != nil {
			return err
		}
		if len(ancestors) == 0 || len(descendants) == 0 {
			return errors.New("user hierarchy is unavailable")
		}
		result := tx.Model(&User{}).Where("id = ? AND parent_user_id IS NULL AND membership_version = ?", target.ID, membershipVersion).
			Updates(map[string]any{"parent_user_id": actorID, "membership_version": gorm.Expr("membership_version + 1")})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return vendorError(409, "MEMBER_CHANGED", "This user's membership changed. Refresh the member list.")
		}
		// Batch the cross-product to keep memory and SQL parameter usage bounded.
		links := make([]UserHierarchy, 0, 200)
		flush := func() error {
			if len(links) == 0 {
				return nil
			}
			if err := tx.Omit("AncestorUser", "DescendantUser", "SourceUser").Create(&links).Error; err != nil {
				return err
			}
			links = links[:0]
			return nil
		}
		for _, ancestor := range ancestors {
			source := ancestor.SourceUserID
			if ancestor.Depth == 0 {
				id := target.ID
				source = &id
			}
			for _, descendant := range descendants {
				links = append(links, UserHierarchy{AncestorUserID: ancestor.AncestorUserID, DescendantUserID: descendant.DescendantUserID, SourceUserID: source, Depth: ancestor.Depth + 1 + descendant.Depth})
				if len(links) == 200 {
					if err := flush(); err != nil {
						return err
					}
				}
			}
		}
		if err := flush(); err != nil {
			return err
		}
		if err := tx.Omit("Actor", "Target").Create(&MemberLinkAudit{ActorID: actorID, ParentUserID: &actorID, TargetID: target.ID, Action: "link"}).Error; err != nil {
			return err
		}
		if err := invalidateMemberVisibility(tx, actor.ID); err != nil {
			return err
		}
		out = adminUser(*target)
		return nil
	})
	return out, err
}

type AccountSource struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}
type DirectMember struct {
	ID                string    `json:"id"`
	Email             string    `json:"email"`
	Status            string    `json:"status"`
	CreatedAt         time.Time `json:"created_at"`
	VisibleAccounts   int64     `json:"visible_accounts"`
	MembershipVersion string    `json:"membership_version"`
}
type DirectMemberPage struct {
	Items    []DirectMember `json:"items"`
	Total    int64          `json:"total"`
	Page     int            `json:"page"`
	PageSize int            `json:"page_size"`
}

func (s *Store) DirectMembers(ctx context.Context, viewerID uint64, page int, search string) (DirectMemberPage, error) {
	out := DirectMemberPage{Items: []DirectMember{}, Page: page, PageSize: 20}
	if err := validAdminFilter(page, search, ""); err != nil {
		return out, err
	}
	q := s.DB.WithContext(ctx).Model(&User{}).Where("parent_user_id = ?", viewerID)
	if search != "" {
		q = q.Where("LOWER(email) LIKE ? ESCAPE '!'", escapedSearch(search))
	}
	if err := q.Count(&out.Total).Error; err != nil {
		return out, err
	}
	var members []User
	if err := q.Order("created_at DESC, id DESC").Limit(20).Offset((page - 1) * 20).Find(&members).Error; err != nil {
		return out, err
	}
	for _, u := range members {
		var count int64
		owners := s.DB.Model(&UserHierarchy{}).Select("descendant_user_id").Where("ancestor_user_id = ?", u.ID)
		if err := s.DB.WithContext(ctx).Model(&SyncedAccount{}).Where("deleted_at IS NULL AND user_id IN (?)", owners).Count(&count).Error; err != nil {
			return out, err
		}
		out.Items = append(out.Items, DirectMember{ID: idString(u.ID), Email: u.Email, Status: u.Status, CreatedAt: u.CreatedAt.UTC(), VisibleAccounts: count, MembershipVersion: idString(u.MembershipVersion)})
	}
	return out, nil
}
func (s *Store) MemberSources(ctx context.Context, viewerID uint64) ([]AccountSource, error) {
	out := []AccountSource{{ID: "myself", Label: "myself"}}
	var children []User
	if err := s.DB.WithContext(ctx).Select("id", "email").Where("parent_user_id = ?", viewerID).Order("email ASC, id ASC").Find(&children).Error; err != nil {
		return nil, err
	}
	for _, u := range children {
		out = append(out, AccountSource{ID: idString(u.ID), Label: u.Email})
	}
	return out, nil
}

type UserVisibility struct {
	ViewerID uint64
	All      bool
	Self     bool
	Sources  []uint64
}

func (v *UserVisibility) Owners(db *gorm.DB) *gorm.DB {
	q := db.Model(&UserHierarchy{}).Select("descendant_user_id").Where("ancestor_user_id = ?", v.ViewerID)
	if v.All {
		return q
	}
	if v.Self {
		return q.Where("depth = 0 OR source_user_id IN ?", v.Sources)
	}
	if len(v.Sources) == 0 {
		return q.Where("1 = 0")
	}
	return q.Where("source_user_id IN ?", v.Sources)
}

// Only a viewer and their direct children are accepted as filter values.
// An explicit none differs from an omitted/default all selection.
func (s *Store) ResolveVisibility(ctx context.Context, viewerID uint64, raw string) (*UserVisibility, error) {
	v := &UserVisibility{ViewerID: viewerID}
	if viewerID == 0 {
		return nil, ErrNotFound
	}
	if raw == "" || raw == "all" {
		v.All = true
		return v, nil
	}
	if raw == "none" {
		return v, nil
	}
	if len(raw) > 8192 {
		return nil, vendorError(400, "INVALID_SOURCE", "Invalid source selection.")
	}
	seen := map[string]bool{}
	for _, value := range strings.Split(raw, ",") {
		if seen[value] {
			continue
		}
		seen[value] = true
		if value == "myself" {
			v.Self = true
			continue
		}
		id, err := ParseBusinessID(value)
		if err != nil {
			return nil, vendorError(400, "INVALID_SOURCE", "Choose yourself or a direct member.")
		}
		v.Sources = append(v.Sources, id)
	}
	if len(v.Sources) > 0 {
		var count int64
		if err := s.DB.WithContext(ctx).Model(&User{}).Where("parent_user_id = ? AND id IN ?", viewerID, v.Sources).Count(&count).Error; err != nil {
			return nil, err
		}
		if count != int64(len(v.Sources)) {
			return nil, vendorError(400, "INVALID_SOURCE", "Choose yourself or a direct member.")
		}
	}
	return v, nil
}
func (s *Store) visibilitySource(ctx context.Context, viewerID, ownerID uint64) (*AccountSource, error) {
	var link UserHierarchy
	if err := s.DB.WithContext(ctx).Where("ancestor_user_id = ? AND descendant_user_id = ?", viewerID, ownerID).First(&link).Error; err != nil {
		return nil, err
	}
	if link.Depth == 0 {
		return &AccountSource{ID: "myself", Label: "myself"}, nil
	}
	if link.SourceUserID == nil {
		return nil, ErrNotFound
	}
	var source User
	if err := s.DB.WithContext(ctx).Select("id", "email").Where("id = ? AND parent_user_id = ?", *link.SourceUserID, viewerID).First(&source).Error; err != nil {
		return nil, err
	}
	return &AccountSource{ID: idString(source.ID), Label: source.Email}, nil
}
func (s *Store) decorateVisibleAccount(ctx context.Context, viewerID, ownerID uint64, dto *AccountDTO) error {
	source, err := s.visibilitySource(ctx, viewerID, ownerID)
	if err != nil {
		return err
	}
	own := viewerID == ownerID
	dto.AccountSource, dto.CanManage = source, &own
	dto.UserID, dto.UserEmail = "", ""
	return nil
}
func (s *Store) decorateVisibleSubmission(ctx context.Context, viewerID, ownerID uint64, dto *SubmissionDTO) error {
	source, err := s.visibilitySource(ctx, viewerID, ownerID)
	if err != nil {
		return err
	}
	own := viewerID == ownerID
	dto.AccountSource, dto.CanManage = source, &own
	return nil
}
func (s *Store) ListVisibleAccounts(ctx context.Context, viewerID uint64, page int, search, status, rawSources string) (AccountPage, error) {
	scope, err := s.ResolveVisibility(ctx, viewerID, rawSources)
	if err != nil {
		return AccountPage{}, err
	}
	return s.listSyncedAccounts(ctx, viewerID, page, search, status, false, scope)
}
func (s *Store) VisibleAccount(ctx context.Context, viewerID, id uint64) (AccountDTO, error) {
	var row SyncedAccount
	scope := &UserVisibility{ViewerID: viewerID, All: true}
	if err := s.DB.WithContext(ctx).Where("id = ? AND deleted_at IS NULL AND user_id IN (?)", id, scope.Owners(s.DB.WithContext(ctx))).First(&row).Error; err != nil {
		return AccountDTO{}, err
	}
	dto, err := s.accountDTO(ctx, row)
	if err == nil {
		err = s.decorateVisibleAccount(ctx, viewerID, row.UserID, &dto)
	}
	return dto, err
}
func (s *Store) VisibleSubmissionDetail(ctx context.Context, viewerID, id uint64) (SubmissionDetail, error) {
	var row Submission
	scope := &UserVisibility{ViewerID: viewerID, All: true}
	if err := s.DB.WithContext(ctx).Where("id = ? AND user_id IN (?)", id, scope.Owners(s.DB.WithContext(ctx))).First(&row).Error; err != nil {
		return SubmissionDetail{}, err
	}
	out, err := s.SubmissionDetail(ctx, row.UserID, id)
	if err == nil {
		err = s.decorateVisibleSubmission(ctx, viewerID, row.UserID, &out.Submission)
	}
	return out, err
}
