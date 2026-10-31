package main

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"goapi-tk-register/server/common"
	"goapi-tk-register/server/internal/core"
)

func vendorFailure(w http.ResponseWriter, err error) {
	var problem *core.VendorError
	if errors.As(err, &problem) {
		fail(w, problem.Status, problem.Code, problem.Message)
		return
	}
	if errors.Is(err, core.ErrNotFound) {
		fail(w, 404, "NOT_FOUND", "This record does not exist.")
		return
	}
	internal(w)
}
func (a *app) vendorRoutes(w http.ResponseWriter, r *http.Request, route string) bool {
	isVendor := route == "vendors" || route == "submissions" || strings.HasPrefix(route, "submissions/") || strings.HasPrefix(route, "admin/vendors/")
	if !isVendor {
		return false
	}
	user, _, valid := a.authenticated(w, r, r.Method != "GET")
	if !valid {
		return true
	}
	if r.Method == "POST" && (route == "submissions" || strings.HasPrefix(route, "submissions/")) {
		limit := 30
		kind := "query"
		if route == "submissions" {
			limit = 10
			kind = "push"
		}
		retry, err := loginAttempt.Run(r.Context(), a.redis, []string{redisPrefix + "vendor-action:" + kind + ":" + strconv.FormatUint(user.ID, 10)}, limit, 60).Int()
		if err != nil {
			internal(w)
			return true
		}
		if retry > 0 {
			rateFailure(w, retry)
			return true
		}
	}
	service := core.VendorService{Store: a.store, Transport: a.vendorTransport}
	if err := a.store.RecoverVendorExecutions(r.Context()); err != nil {
		internal(w)
		return true
	}
	if route == "vendors" && r.Method == "GET" {
		items, e := a.store.Vendors(r.Context())
		if e != nil {
			vendorFailure(w, e)
		} else {
			ok(w, map[string]any{"vendors": items})
		}
		return true
	}
	if route == "submissions" {
		switch r.Method {
		case "GET":
			page := 1
			if raw := r.URL.Query().Get("page"); raw != "" {
				var e error
				page, e = strconv.Atoi(raw)
				if e != nil {
					fail(w, 400, "INVALID_FILTER", "Invalid page.")
					return true
				}
			}
			items, e := a.store.Submissions(r.Context(), user.ID, page, r.URL.Query().Get("search"), r.URL.Query().Get("status"))
			if e != nil {
				vendorFailure(w, e)
			} else {
				ok(w, items)
			}
		case "POST":
			var input core.CreateSubmissionInput
			if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
				fail(w, 415, "INVALID_CONTENT_TYPE", "Use application/json.")
				return true
			}
			if e := common.DecodeJson(http.MaxBytesReader(w, r.Body, 3<<20), &input); e != nil {
				fail(w, 400, "INVALID_REQUEST", "Please check the account request fields.")
				return true
			}
			result, e := service.Create(r.Context(), user.ID, input)
			if e != nil {
				vendorFailure(w, e)
			} else {
				ok(w, result)
			}
		default:
			fail(w, 405, "METHOD_NOT_ALLOWED", "This method is not supported.")
		}
		return true
	}
	if strings.HasPrefix(route, "admin/vendors/") {
		if user.Role != "admin" {
			fail(w, 403, "FORBIDDEN", "Administrator access is required.")
			return true
		}
		id, e := core.ParseBusinessID(strings.TrimPrefix(route, "admin/vendors/"))
		if e != nil {
			vendorFailure(w, e)
			return true
		}
		var result core.VendorDTO
		if r.Method == "GET" {
			result, e = a.store.VendorDetails(r.Context(), id)
		} else if r.Method == "PUT" {
			var input core.VendorPatch
			if !decode(w, r, &input) {
				return true
			}
			result, e = a.store.PatchVendor(r.Context(), id, user.ID, input)
		} else {
			fail(w, 405, "METHOD_NOT_ALLOWED", "This method is not supported.")
			return true
		}
		if e != nil {
			vendorFailure(w, e)
		} else {
			ok(w, result)
		}
		return true
	}
	if strings.HasPrefix(route, "submissions/") {
		parts := strings.Split(strings.TrimPrefix(route, "submissions/"), "/")
		id, e := core.ParseBusinessID(parts[0])
		if e != nil {
			vendorFailure(w, e)
			return true
		}
		if len(parts) == 1 && r.Method == "GET" {
			result, e := a.store.SubmissionDetail(r.Context(), user.ID, id)
			if e != nil {
				vendorFailure(w, e)
			} else {
				ok(w, result)
			}
			return true
		}
		if len(parts) == 2 && r.Method == "POST" {
			if parts[1] == "status" {
				var input struct {
					Force bool `json:"force"`
				}
				if !decode(w, r, &input) {
					return true
				}
				result, e := service.RefreshStatus(r.Context(), user.ID, id, input.Force)
				if e != nil {
					vendorFailure(w, e)
				} else {
					ok(w, result)
				}
				return true
			}
			input := core.QueryInput{}
			switch parts[1] {
			case "metrics":
				if !decode(w, r, &input.Metrics) {
					return true
				}
			case "bills":
				if !decode(w, r, &input.Bills) {
					return true
				}
			default:
				fail(w, 404, "NOT_FOUND", "This endpoint does not exist.")
				return true
			}
			result, e := service.Query(r.Context(), user.ID, id, parts[1], input)
			if e != nil {
				vendorFailure(w, e)
			} else {
				ok(w, result)
			}
			return true
		}
	}
	fail(w, 404, "NOT_FOUND", "This endpoint does not exist.")
	return true
}
