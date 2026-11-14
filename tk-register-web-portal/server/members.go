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
