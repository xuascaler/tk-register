package core

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// This singleton serializes role changes across processes, including concurrent
// demotions. Updating it obtains a transaction lock on all supported databases.
type AdminRoleGuard struct {
	ID       uint64 `gorm:"primaryKey"`
	Revision uint64 `gorm:"not null"`
}
type AdminRoleAudit struct {
	ID        uint64 `gorm:"primaryKey"`
	ActorID   uint64 `gorm:"not null;index"`
	Actor     User   `gorm:"foreignKey:ActorID;constraint:OnDelete:RESTRICT"`
	TargetID  uint64 `gorm:"not null;index"`
	Target    User   `gorm:"foreignKey:TargetID;constraint:OnDelete:RESTRICT"`
	FromRole  string `gorm:"size:16;not null"`
	ToRole    string `gorm:"size:16;not null"`
	CreatedAt time.Time
}
type AdminUser struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}
type AdminUserPage struct {
	Items    []AdminUser `json:"items"`
	Total    int64       `json:"total"`
	Page     int         `json:"page"`
	PageSize int         `json:"page_size"`
}

func adminUser(u User) AdminUser {
	return AdminUser{idString(u.ID), u.Email, u.Role, u.Status, u.CreatedAt.UTC()}
}
func escapedSearch(v string) string {
	return "%" + strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(strings.ToLower(strings.TrimSpace(v))) + "%"
}
func validAdminFilter(page int, search, status string) error {
	if page < 1 || page > 100000 || len(search) > 254 {
		return vendorError(400, "INVALID_FILTER", "Invalid list filter.")
	}
	if status != "" && status != "running" && status != "accepted" && status != "failed" && status != "unknown" {
		return vendorError(400, "INVALID_FILTER", "Invalid submission status.")
	}
	return nil
}
func (s *Store) AdminUsers(ctx context.Context, page int, search string) (AdminUserPage, error) {
	out := AdminUserPage{Items: []AdminUser{}, Page: page, PageSize: 20}
	if e := validAdminFilter(page, search, ""); e != nil {
		return out, e
	}
	q := s.DB.WithContext(ctx).Model(&User{})
	if search != "" {
		q = q.Where("LOWER(email) LIKE ? ESCAPE '!'", escapedSearch(search))
	}
	if e := q.Count(&out.Total).Error; e != nil {
		return out, e
	}
	var users []User
	if e := q.Order("created_at DESC, id DESC").Limit(20).Offset((page - 1) * 20).Find(&users).Error; e != nil {
		return out, e
	}
	for _, u := range users {
		out.Items = append(out.Items, adminUser(u))
	}
	return out, nil
}

// CreateMemberByAdmin rechecks the actor while holding its row lock, so a
// concurrent demotion cannot create a member using stale admin authorization.
// It intentionally does not create a session or grant administrator privileges.
func (s *Store) CreateMemberByAdmin(ctx context.Context, actorID, actorVersion uint64, user *User) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var actor User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&actor, actorID).Error; err != nil {
			return err
		}
		if actor.Role != "admin" || actor.Status != "active" || actor.AuthVersion != actorVersion {
			return vendorError(403, "FORBIDDEN", "Administrator access is required.")
		}
		user.Role = "member"
		user.Status = "active"
		transactionStore := *s
		transactionStore.DB = tx
		return transactionStore.CreateUser(ctx, user)
	})
}

func (s *Store) ChangeUserRole(ctx context.Context, actorID, actorVersion uint64, email, role, protectedEmail string) (AdminUser, error) {
	var out AdminUser
	email = NormalizeEmail(email)
	if !ValidEmail(email) || (role != "admin" && role != "member") {
		return out, vendorError(400, "INVALID_ROLE_REQUEST", "Enter a valid email address and role.")
	}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		lock := tx.Model(&AdminRoleGuard{}).Where("id = ?", 1).UpdateColumn("revision", gorm.Expr("revision + ?", 1))
		if lock.Error != nil {
			return lock.Error
		}
		if lock.RowsAffected != 1 {
			return errors.New("administrator role lock is unavailable")
		}
		var actor, target User
		if e := tx.First(&actor, actorID).Error; e != nil {
			return e
		}
		if actor.Role != "admin" || actor.Status != "active" || actor.AuthVersion != actorVersion {
			return vendorError(403, "FORBIDDEN", "Administrator access is required.")
		}
		if e := tx.Where("email = ?", email).First(&target).Error; e != nil {
			return e
		}
		if role == "member" && target.ID == actorID {
			return vendorError(409, "SELF_DEMOTION", "You cannot remove your own administrator role.")
		}
		if role == "member" && target.Email == NormalizeEmail(protectedEmail) {
			return vendorError(409, "PROTECTED_ADMIN", "The bootstrap administrator cannot be removed.")
		}
		if role == "admin" && target.Status != "active" {
			return vendorError(409, "USER_INACTIVE", "Only an active user can become an administrator.")
		}
		if target.Role == role {
			return vendorError(409, "ROLE_UNCHANGED", "This user already has the requested role.")
		}
		if role == "member" {
			var n int64
			if e := tx.Model(&User{}).Where("role = ? AND status = ?", "admin", "active").Count(&n).Error; e != nil {
				return e
			}
			if n <= 1 {
				return vendorError(409, "LAST_ADMIN", "The last active administrator cannot be removed.")
			}
		}
		previous := target.Role
		if e := tx.Model(&target).Updates(map[string]any{"role": role, "auth_version": gorm.Expr("auth_version + ?", 1)}).Error; e != nil {
			return e
		}
		if e := tx.Create(&AdminRoleAudit{ActorID: actorID, TargetID: target.ID, FromRole: previous, ToRole: role}).Error; e != nil {
			return e
		}
		target.Role = role
		out = adminUser(target)
		return nil
	})
	return out, err
}
func (s *Store) migrateAdmin() error {
	if e := s.DB.AutoMigrate(&AdminRoleGuard{}, &AdminRoleAudit{}); e != nil {
		return e
	}
	return s.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&AdminRoleGuard{ID: 1}).Error
}

type AdminOwner struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}
type AdminAccount struct {
	ID                string         `json:"id"`
	Account           string         `json:"account"`
	User              AdminOwner     `json:"user"`
	VendorID          string         `json:"vendor_id"`
	VendorName        string         `json:"vendor_name"`
	Plan              string         `json:"plan"`
	SubmissionCount   int64          `json:"submission_count"`
	LatestSubmission  SubmissionDTO  `json:"latest_submission"`
	CurrentSubmission *SubmissionDTO `json:"current_submission"`
}
type AdminAccountPage struct {
	Items    []AdminAccount `json:"items"`
	Total    int64          `json:"total"`
	Page     int            `json:"page"`
	PageSize int            `json:"page_size"`
}
type AdminSubmission struct {
	SubmissionDTO
	User              AdminOwner     `json:"user"`
	AccountID         string         `json:"account_id"`
	CurrentSubmission *SubmissionDTO `json:"current_submission"`
}
type AdminSubmissionPage struct {
	Items    []AdminSubmission `json:"items"`
	Total    int64             `json:"total"`
	Page     int               `json:"page"`
	PageSize int               `json:"page_size"`
}
type AdminSubmissionDetail struct {
	SubmissionDetail
	User              AdminOwner     `json:"user"`
	AccountID         string         `json:"account_id"`
	CurrentSubmission *SubmissionDTO `json:"current_submission"`
}
type AdminAccountDetail struct {
	Account AdminAccount        `json:"account"`
	History AdminSubmissionPage `json:"history"`
}

func accountGroup(q *gorm.DB, record Submission) *gorm.DB {
	return q.Where("user_id = ? AND vendor_id = ? AND LOWER(TRIM(account)) = ?", record.UserID, record.VendorID, strings.ToLower(strings.TrimSpace(record.Account)))
}
func (s *Store) AdminSubmissionRecord(ctx context.Context, id uint64) (Submission, error) {
	var r Submission
	e := s.DB.WithContext(ctx).First(&r, id).Error
	return r, e
}
func (s *Store) currentAccountSubmission(ctx context.Context, record Submission) (*SubmissionDTO, error) {
	// Status belongs to the newest accepted registration. A later failed submit
	// keeps this record; a newly accepted remote ID must be checked for itself
	// rather than inheriting the old account's status snapshot forever.
	var current Submission
	e := accountGroup(s.DB.WithContext(ctx), record).
		Where("status = ? AND remote_id IS NOT NULL", "accepted").
		Order("created_at DESC, id DESC").First(&current).Error
	if errors.Is(e, ErrNotFound) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	dto, e := s.submissionDTO(ctx, current)
	return &dto, e
}

func (s *Store) adminAccount(ctx context.Context, record Submission) (AdminAccount, error) {
	var out AdminAccount
	var latest Submission
	q := accountGroup(s.DB.WithContext(ctx).Model(&Submission{}), record)
	if e := q.Count(&out.SubmissionCount).Error; e != nil {
		return out, e
	}
	if e := q.Order("created_at DESC, id DESC").First(&latest).Error; e != nil {
		return out, e
	}
	dto, e := s.submissionDTO(ctx, latest)
	if e != nil {
		return out, e
	}
	var owner User
	if e = s.DB.WithContext(ctx).First(&owner, record.UserID).Error; e != nil {
		return out, e
	}
	current, e := s.currentAccountSubmission(ctx, record)
	if e != nil {
		return out, e
	}
	out.ID = dto.ID
	out.Account = latest.Account
	out.User = AdminOwner{idString(owner.ID), owner.Email}
	out.VendorID = dto.VendorID
	out.VendorName = dto.VendorName
	out.Plan = dto.Plan
	out.LatestSubmission = dto
	out.CurrentSubmission = current
	return out, nil
}
func adminSubmissionQuery(s *Store, ctx context.Context, search, status string) *gorm.DB {
	q := s.DB.WithContext(ctx).Model(&Submission{}).Table("submissions AS s").Joins("JOIN users AS u ON u.id = s.user_id")
	if status != "" {
		q = q.Where("s.status = ?", status)
	}
	if search != "" {
		pattern := escapedSearch(search)
		q = q.Where("(LOWER(s.account) LIKE ? ESCAPE '!' OR LOWER(u.email) LIKE ? ESCAPE '!' OR LOWER(s.remote_alias) LIKE ? ESCAPE '!' OR s.remote_id = ?)", pattern, pattern, pattern, strings.TrimSpace(search))
	}
	return q
}
func (s *Store) AdminAccounts(ctx context.Context, page int, search, status string) (AdminAccountPage, error) {
	out := AdminAccountPage{Items: []AdminAccount{}, Page: page, PageSize: 20}
	if e := validAdminFilter(page, search, status); e != nil {
		return out, e
	}
	// NOT EXISTS is supported by all three databases, including MySQL 5.7.
	q := adminSubmissionQuery(s, ctx, search, status).Where(`NOT EXISTS (SELECT 1 FROM submissions AS newer WHERE newer.user_id = s.user_id AND newer.vendor_id = s.vendor_id AND LOWER(TRIM(newer.account)) = LOWER(TRIM(s.account)) AND (newer.created_at > s.created_at OR (newer.created_at = s.created_at AND newer.id > s.id)))`)
	if e := q.Count(&out.Total).Error; e != nil {
		return out, e
	}
	var records []Submission
	if e := q.Select("s.*").Order("s.created_at DESC, s.id DESC").Limit(20).Offset((page - 1) * 20).Find(&records).Error; e != nil {
		return out, e
	}
	for _, r := range records {
		dto, e := s.adminAccount(ctx, r)
		if e != nil {
			return out, e
		}
		out.Items = append(out.Items, dto)
	}
	return out, nil
}
func (s *Store) AdminSubmissions(ctx context.Context, page int, search, status string, accountID uint64) (AdminSubmissionPage, error) {
	out := AdminSubmissionPage{Items: []AdminSubmission{}, Page: page, PageSize: 20}
	if e := validAdminFilter(page, search, status); e != nil {
		return out, e
	}
	q := adminSubmissionQuery(s, ctx, search, status)
	if accountID != 0 {
		r, e := s.AdminSubmissionRecord(ctx, accountID)
		if e != nil {
			return out, e
		}
		q = q.Where("s.user_id = ? AND s.vendor_id = ? AND LOWER(TRIM(s.account)) = ?", r.UserID, r.VendorID, strings.ToLower(strings.TrimSpace(r.Account)))
	}
	if e := q.Count(&out.Total).Error; e != nil {
		return out, e
	}
	var records []Submission
	if e := q.Select("s.*").Order("s.created_at DESC, s.id DESC").Limit(20).Offset((page - 1) * 20).Find(&records).Error; e != nil {
		return out, e
	}
	groups := map[string]AdminAccount{}
	for _, r := range records {
		key := idString(r.UserID) + ":" + idString(r.VendorID) + ":" + strings.ToLower(strings.TrimSpace(r.Account))
		group, found := groups[key]
		if !found {
			var e error
			group, e = s.adminAccount(ctx, r)
			if e != nil {
				return out, e
			}
			groups[key] = group
		}
		dto, e := s.submissionDTO(ctx, r)
		if e != nil {
			return out, e
		}
		out.Items = append(out.Items, AdminSubmission{dto, group.User, group.ID, group.CurrentSubmission})
	}
	return out, nil
}
func (s *Store) AdminAccountDetail(ctx context.Context, id uint64, page int) (AdminAccountDetail, error) {
	var out AdminAccountDetail
	r, e := s.AdminSubmissionRecord(ctx, id)
	if e != nil {
		return out, e
	}
	out.Account, e = s.adminAccount(ctx, r)
	if e != nil {
		return out, e
	}
	out.History, e = s.AdminSubmissions(ctx, page, "", "", id)
	return out, e
}
func (s *Store) AdminDetail(ctx context.Context, id uint64) (AdminSubmissionDetail, error) {
	var out AdminSubmissionDetail
	r, e := s.AdminSubmissionRecord(ctx, id)
	if e != nil {
		return out, e
	}
	out.SubmissionDetail, e = s.SubmissionDetail(ctx, r.UserID, id)
	if e != nil {
		return out, e
	}
	group, e := s.adminAccount(ctx, r)
	if e != nil {
		return out, e
	}
	out.User = group.User
	out.AccountID = group.ID
	out.CurrentSubmission = group.CurrentSubmission
	return out, nil
}
