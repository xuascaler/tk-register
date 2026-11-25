package main

import (
	"goapi-tk-register/server/internal/core"
	"net/http"
	"strconv"
	"strings"
)

func (a *app) memberRoutes(w http.ResponseWriter, r *http.Request, route string) bool {
	if route != "members" && !strings.HasPrefix(route, "members/") {
		return false
	}
	user, _, valid := a.authenticated(w, r, r.Method != "GET")
	if !valid {
		return true
	}
	switch r.Method + " " + route {
	case "POST members/unlink":
		a.unlinkMember(w, r, user)
	case "POST members/link-code", "POST members/link":
		a.linkMember(w, r, user, route == "members/link-code")
	case "GET members":
		page, valid := adminPage(w, r)
		if !valid {
			return true
		}
		result, err := a.store.DirectMembers(r.Context(), user.ID, page, r.URL.Query().Get("search"))
		if err != nil {
			vendorFailure(w, err)
		} else {
			ok(w, result)
		}
	case "GET members/sources":
		sources, err := a.store.MemberSources(r.Context(), user.ID)
		if err != nil {
			vendorFailure(w, err)
		} else {
			ok(w, map[string]any{"sources": sources})
		}
	case "POST members/registration-code", "POST members":
		retry, err := loginAttempt.Run(r.Context(), a.redis, []string{redisPrefix + "member-create:" + strconv.FormatUint(user.ID, 10)}, 10, 60).Int()
		if err != nil {
			internal(w)
			return true
		}
		if retry > 0 {
			rateFailure(w, retry)
			return true
		}
		if route == "members/registration-code" {
			var input struct {
				Email string `json:"email"`
			}
			if decode(w, r, &input) {
				a.sendVerificationCode(w, r, input.Email, "register", false)
			}
		} else {
			created, _, good := a.registerVerifiedUser(w, r, user, true)
			if good {
				ok(w, map[string]any{"user": core.AdminUser{ID: strconv.FormatUint(created.ID, 10), Email: created.Email, Role: created.Role, Status: created.Status, CreatedAt: created.CreatedAt.UTC()}})
			}
		}
	default:
		fail(w, 405, "METHOD_NOT_ALLOWED", "This method is not supported.")
	}
	return true
}

func memberLinkPurpose(actor, target *core.User) string {
	return "member_link:" + strconv.FormatUint(actor.ID, 10) + ":" + strconv.FormatUint(actor.AuthVersion, 10) + ":" + strconv.FormatUint(target.ID, 10) + ":" + strconv.FormatUint(target.AuthVersion, 10) + ":" + strconv.FormatUint(target.MembershipVersion, 10)
}

func (a *app) linkMember(w http.ResponseWriter, r *http.Request, actor *core.User, send bool) {
	retry, err := loginAttempt.Run(r.Context(), a.redis, []string{redisPrefix + "member-link:" + strconv.FormatUint(actor.ID, 10)}, 10, 60).Int()
	if err != nil {
		internal(w)
		return
	}
	if retry > 0 {
		rateFailure(w, retry)
		return
	}
	var input struct {
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.Email = core.NormalizeEmail(input.Email)
	if !validEmail(input.Email) {
		fail(w, 400, "INVALID_REQUEST", "Enter a valid email address of up to 50 characters.")
		return
	}
	target, err := a.store.MemberLinkTarget(r.Context(), actor.ID, input.Email)
	if err != nil {
		vendorFailure(w, err)
		return
	}
	v, good := a.config(w, r)
	if !good {
		return
	}
	purpose := memberLinkPurpose(actor, target)
	if send {
		ttl := v.Int("registration_code_ttl_seconds")
		if err := a.deliver(r.Context(), target.Email, purpose, clientIP(r), ttl, v, true, "member_link:"+actor.Email); err != nil {
			deliveryFailure(w, err)
			return
		}
		ok(w, map[string]any{"retry_after": v.Int("email_send_cooldown_seconds"), "expires_in": ttl})
		return
	}
	valid, err := a.checkCode(r.Context(), target.Email, purpose, input.Code, v.Int("verification_max_attempts"), "")
	if err != nil {
		internal(w)
		return
	}
	if !valid {
		fail(w, 400, "INVALID_CODE", "The verification code is invalid or expired. Request a code for this connection.")
		return
	}
	linked, err := a.store.LinkExistingMember(r.Context(), actor.ID, actor.AuthVersion, target.ID, target.AuthVersion, target.MembershipVersion)
	if err != nil {
		vendorFailure(w, err)
		return
	}
	ok(w, map[string]any{"user": linked})
}

func (a *app) unlinkMember(w http.ResponseWriter, r *http.Request, actor *core.User) {
	var input struct {
		MemberID          string `json:"member_id"`
		MembershipVersion string `json:"membership_version"`
	}
	if !decode(w, r, &input) {
		return
	}
	id, idErr := core.ParseBusinessID(input.MemberID)
	version, versionErr := strconv.ParseUint(input.MembershipVersion, 10, 64)
	if idErr != nil || versionErr != nil || strconv.FormatUint(version, 10) != input.MembershipVersion {
		fail(w, 400, "INVALID_REQUEST", "Choose a direct member from the current member list.")
		return
	}
	retry, err := loginAttempt.Run(r.Context(), a.redis, []string{redisPrefix + "member-unlink:" + strconv.FormatUint(actor.ID, 10)}, 10, 60).Int()
	if err != nil {
		internal(w)
		return
	}
	if retry > 0 {
		rateFailure(w, retry)
		return
	}
	removed, err := a.store.UnlinkMember(r.Context(), actor.ID, actor.AuthVersion, id, version)
	if err != nil {
		vendorFailure(w, err)
		return
	}
	ok(w, map[string]any{"user": removed})
}
