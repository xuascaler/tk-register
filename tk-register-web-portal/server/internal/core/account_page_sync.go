package core

import (
	"context"
)

// PageSyncTargets resolves only the selected local IDs after the HTTP session
// and administrator checks. No caller-supplied supplier identity is accepted.
func (s *Store) PageSyncTargets(ctx context.Context, userID uint64, ids []uint64, admin bool) ([]SyncedAccount, error) {
	if len(ids) == 0 || len(ids) > 20 || (!admin && userID == 0) {
		return nil, ErrNotFound
	}
	return s.pageAccountRows(ctx, userID, ids, admin)
}
func (s *Store) pageAccountRows(ctx context.Context, userID uint64, ids []uint64, admin bool) ([]SyncedAccount, error) {
	q := s.DB.WithContext(ctx).Where("id IN ?", ids)
	if !admin {
		q = q.Where("user_id = ?", userID)
	}
	var rows []SyncedAccount
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	// Reject the complete batch if even one ID is absent or belongs to someone
	// else. Do not disclose which ID failed or collect the authorized subset.
	if len(rows) != len(ids) {
		return nil, ErrNotFound
	}
	byID := make(map[uint64]SyncedAccount, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	ordered := make([]SyncedAccount, 0, len(ids))
	for _, id := range ids {
		ordered = append(ordered, byID[id])
	}
	return ordered, nil
}
func (s *Store) PageSyncedAccountDTOs(ctx context.Context, userID uint64, ids []uint64, admin bool) ([]AccountDTO, map[uint64]string, error) {
	rows, err := s.pageAccountRows(ctx, userID, ids, admin)
	if err != nil {
		return nil, nil, err
	}
	identities := make(map[uint64]string, len(rows))
	out := make([]AccountDTO, 0, len(rows))
	for _, row := range rows {
		dto, err := s.accountDTO(ctx, row)
		if err == nil && admin {
			err = s.accountOwner(ctx, row.UserID, &dto)
		}
		if err != nil {
			return nil, nil, err
		}
		out = append(out, dto)
		identities[row.ID] = row.IdentityKey
	}
	return out, identities, nil
}
