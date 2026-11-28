package main

import (
	"net/http"
	"strconv"

	"goapi-tk-register/server/internal/core"
)

func (a *app) adminUnlinkUserParent(w http.ResponseWriter, r *http.Request, actor *core.User) {
	var input struct {
		UserID            string `json:"user_id"`
		ParentID          string `json:"parent_id"`
		MembershipVersion string `json:"membership_version"`
	}
	if !decode(w, r, &input) {
		return
	}
	id, idErr := core.ParseBusinessID(input.UserID)
	parentID, parentErr := core.ParseBusinessID(input.ParentID)
	version, versionErr := strconv.ParseUint(input.MembershipVersion, 10, 64)
	if idErr != nil || parentErr != nil || versionErr != nil || strconv.FormatUint(version, 10) != input.MembershipVersion {
		fail(w, 400, "INVALID_REQUEST", "Choose a user with a direct parent from the current user list.")
		return
	}
	retry, err := loginAttempt.Run(r.Context(), a.redis, []string{redisPrefix + "admin-unlink-parent:" + strconv.FormatUint(actor.ID, 10)}, 10, 60).Int()
	if err != nil {
		internal(w)
		return
	}
	if retry > 0 {
		rateFailure(w, retry)
		return
	}
	user, err := a.store.AdminUnlinkUserParent(r.Context(), actor.ID, actor.AuthVersion, id, parentID, version)
	if err != nil {
		vendorFailure(w, err)
		return
	}
	ok(w, map[string]any{"user": user})
}
