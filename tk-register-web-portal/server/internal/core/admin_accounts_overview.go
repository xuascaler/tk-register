package core

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// AdminOverviewSources lists direct account owners, including other admins and
// users without accounts. Membership does not change this administrative view.
// Administrator authorization is enforced by the HTTP route before calling it.
func (s *Store) AdminOverviewSources(ctx context.Context, viewerID uint64) ([]AccountSource, error) {
	var users []User
	if err := s.DB.WithContext(ctx).Select("id", "email").Order("email ASC, id ASC").Find(&users).Error; err != nil {
		return nil, err
	}
	found := false
	out := []AccountSource{{ID: "myself", Label: "myself"}}
	for _, user := range users {
		if user.ID == viewerID {
			found = true
			continue
		}
		out = append(out, AccountSource{ID: idString(user.ID), Label: user.Email})
	}
	if !found {
		return nil, ErrNotFound
	}
	return out, nil
}

func (s *Store) AdminAccountsOverview(ctx context.Context, viewerID uint64, rawSources, start, end string, allTime bool, now time.Time) (AccountsOverview, error) {
	return s.readLiveAccountsOverview(ctx, viewerID, rawSources, start, end, allTime, now, true)
}

func (s *Store) adminOverviewScope(ctx context.Context, viewerID uint64, rawSources string) (overviewScope, error) {
	sources, err := s.AdminOverviewSources(ctx, viewerID)
	if err != nil {
		return overviewScope{}, err
	}
	if len(rawSources) > 8192 {
		return overviewScope{}, vendorError(400, "INVALID_SOURCE", "Invalid source selection.")
	}
	all := rawSources == "" || rawSources == "all"
	selected := map[string]bool{}
	if !all && rawSources != "none" {
		valid := make(map[string]bool, len(sources))
		for _, source := range sources {
			valid[source.ID] = true
		}
		for _, value := range strings.Split(rawSources, ",") {
			if value != "myself" {
				id, parseErr := ParseBusinessID(value)
				if parseErr != nil {
					return overviewScope{}, vendorError(400, "INVALID_SOURCE", "Choose existing users from the source list.")
				}
				if id == viewerID {
					value = "myself"
				}
			}
			if !valid[value] {
				return overviewScope{}, vendorError(400, "INVALID_SOURCE", "Choose existing users from the source list.")
			}
			selected[value] = true
		}
	}
	// viewerID is a trusted integer, not raw SQL input. Mapping it to zero keeps
	// the shared projection's "myself" source convention without a hierarchy join.
	scope := overviewScope{sourceExpr: fmt.Sprintf("CASE WHEN a.user_id = %d THEN 0 ELSE a.user_id END", viewerID)}
	ownerIDs := make([]uint64, 0, len(sources))
	for _, source := range sources {
		if !all && !selected[source.ID] {
			continue
		}
		scope.sources = append(scope.sources, source)
		id := viewerID
		if source.ID != "myself" {
			id, _ = ParseBusinessID(source.ID)
		}
		ownerIDs = append(ownerIDs, id)
	}
	scope.base = func() *gorm.DB {
		q := s.DB.WithContext(ctx).Table("synced_accounts a").Where("a.deleted_at IS NULL")
		if len(ownerIDs) == 0 {
			return q.Where("1 = 0")
		}
		return q.Where("a.user_id IN ?", ownerIDs)
	}
	return scope, nil
}
