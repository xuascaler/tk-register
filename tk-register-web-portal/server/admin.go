package main

import (
	"net/http"
	"strconv"
	"strings"

	"goapi-tk-register/server/internal/core"
)

func adminPage(w http.ResponseWriter, r *http.Request) (int, bool) {
	page := 1
	if raw := r.URL.Query().Get("page"); raw != "" {
		var e error
		page, e = strconv.Atoi(raw)
		if e != nil || page < 1 || page > 100000 {
			fail(w, 400, "INVALID_FILTER", "Invalid page.")
			return 0, false
		}
	}
	return page, true
}
func (a *app) adminRoutes(w http.ResponseWriter, r *http.Request, route string) bool {
	if route != "admin/users" && route != "admin/users/role" && route != "admin/users/registration-code" && route != "admin/accounts" && !strings.HasPrefix(route, "admin/accounts/") && route != "admin/submissions" && !strings.HasPrefix(route, "admin/submissions/") {
		return false
	}
	actor, _, valid := a.authenticated(w, r, r.Method != "GET")
	if !valid {
		return true
	}
	if actor.Role != "admin" {
		fail(w, 403, "FORBIDDEN", "Administrator access is required.")
		return true
	}
	if strings.HasPrefix(route, "admin/accounts") || strings.HasPrefix(route, "admin/submissions") {
		if err := a.store.RecoverVendorExecutions(r.Context()); err != nil {
			internal(w)
			return true
		}
	}
	if route == "admin/users/registration-code" && r.Method == "POST" {
		var input struct {
			Email string `json:"email"`
		}
		if decode(w, r, &input) {
			a.sendVerificationCode(w, r, input.Email, "register", true)
		}
		return true
	}
	if route == "admin/users" && r.Method == "POST" {
		user, _, created := a.registerVerifiedUser(w, r, actor)
		if created {
			ok(w, map[string]any{"user": core.AdminUser{ID: strconv.FormatUint(user.ID, 10), Email: user.Email, Role: user.Role, Status: user.Status, CreatedAt: user.CreatedAt.UTC()}})
		}
		return true
	}
	if route == "admin/users/role" && r.Method == "POST" {
		var input struct {
			Email string `json:"email"`
			Role  string `json:"role"`
		}
		if !decode(w, r, &input) {
			return true
		}
		user, e := a.store.ChangeUserRole(r.Context(), actor.ID, actor.AuthVersion, input.Email, input.Role, a.adminEmail)
		if e != nil {
			vendorFailure(w, e)
		} else {
			ok(w, map[string]any{"user": user})
		}
		return true
	}
	if route == "admin/users" && r.Method == "GET" {
		page, good := adminPage(w, r)
		if !good {
			return true
		}
		result, e := a.store.AdminUsers(r.Context(), page, r.URL.Query().Get("search"))
		if e != nil {
			vendorFailure(w, e)
		} else {
			ok(w, result)
		}
		return true
	}
	if route == "admin/accounts" && r.Method == "GET" {
		page, good := adminPage(w, r)
		if !good {
			return true
		}
		result, e := a.store.AdminAccounts(r.Context(), page, r.URL.Query().Get("search"), r.URL.Query().Get("status"))
		if e != nil {
			vendorFailure(w, e)
		} else {
			ok(w, result)
		}
		return true
	}
	if strings.HasPrefix(route, "admin/accounts/") && r.Method == "GET" {
		id, e := core.ParseBusinessID(strings.TrimPrefix(route, "admin/accounts/"))
		if e != nil {
			vendorFailure(w, e)
			return true
		}
		page, good := adminPage(w, r)
		if !good {
			return true
		}
		result, e := a.store.AdminAccountDetail(r.Context(), id, page)
		if e != nil {
			vendorFailure(w, e)
		} else {
			ok(w, result)
		}
		return true
	}
	if route == "admin/submissions" && r.Method == "GET" {
		page, good := adminPage(w, r)
		if !good {
			return true
		}
		var accountID uint64
		if raw := r.URL.Query().Get("account_id"); raw != "" {
			var e error
			accountID, e = core.ParseBusinessID(raw)
			if e != nil {
				vendorFailure(w, e)
				return true
			}
		}
		result, e := a.store.AdminSubmissions(r.Context(), page, r.URL.Query().Get("search"), r.URL.Query().Get("status"), accountID)
		if e != nil {
			vendorFailure(w, e)
		} else {
			ok(w, result)
		}
		return true
	}
	if strings.HasPrefix(route, "admin/submissions/") {
		parts := strings.Split(strings.TrimPrefix(route, "admin/submissions/"), "/")
		id, e := core.ParseBusinessID(parts[0])
		if e != nil {
			vendorFailure(w, e)
			return true
		}
		if len(parts) == 1 && r.Method == "GET" {
			result, e := a.store.AdminDetail(r.Context(), id)
			if e != nil {
				vendorFailure(w, e)
			} else {
				ok(w, result)
			}
			return true
		}
		if len(parts) == 2 && r.Method == "POST" && (parts[1] == "status" || parts[1] == "bills") {
			// Rate limits belong to the administrator making the query, not its owner.
			retry, e := loginAttempt.Run(r.Context(), a.redis, []string{redisPrefix + "vendor-action:query:" + strconv.FormatUint(actor.ID, 10)}, 30, 60).Int()
			if e != nil {
				internal(w)
				return true
			}
			if retry > 0 {
				rateFailure(w, retry)
				return true
			}
			record, e := a.store.AdminSubmissionRecord(r.Context(), id)
			if e != nil {
				vendorFailure(w, e)
				return true
			}
			service := a.submissionService()
			if parts[1] == "status" {
				var input struct {
					Force bool `json:"force"`
				}
				if !decode(w, r, &input) {
					return true
				}
				_, e = service.RefreshStatus(r.Context(), record.UserID, id, input.Force)
			} else {
				input := core.QueryInput{}
				if !decode(w, r, &input.Bills) {
					return true
				}
				_, e = service.Query(r.Context(), record.UserID, id, "bills", input)
			}
			if e != nil {
				vendorFailure(w, e)
				return true
			}
			result, e := a.store.AdminDetail(r.Context(), id)
			if e != nil {
				vendorFailure(w, e)
			} else {
				ok(w, result)
			}
			return true
		}
	}
	fail(w, 405, "METHOD_NOT_ALLOWED", "This method is not supported.")
	return true
}
