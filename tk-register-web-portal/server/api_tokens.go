package main

import (
	"errors"
	"log"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"goapi-tk-register/server/common"
	"goapi-tk-register/server/internal/core"
)

const claudeAccountsAPIPath = basePath + "/api/v1/vendors/tk-register-0/claude/accounts"

// Token management is deliberately cookie-only. A submission token cannot
// mint another token, access account settings, or exercise administrator rights.
func (a *app) apiTokenRoutes(w http.ResponseWriter, r *http.Request, route string) bool {
	if route != "account/api-token" && route != "account/api-token/rotate" {
		return false
	}
	user, _, valid := a.authenticated(w, r, r.Method != http.MethodGet)
	if !valid {
		return true
	}
	var result core.APITokenResult
	var err error
	switch {
	case route == "account/api-token" && r.Method == http.MethodGet:
		result, err = a.store.APIToken(r.Context(), user.ID)
	case route == "account/api-token" && r.Method == http.MethodPost:
		var input struct{}
		if !decode(w, r, &input) {
			return true
		}
		result, err = a.store.GenerateAPIToken(r.Context(), user.ID)
	case route == "account/api-token" && r.Method == http.MethodDelete,
		route == "account/api-token/rotate" && r.Method == http.MethodPost:
		var input struct {
			TokenID  string `json:"token_id"`
			Revision uint64 `json:"revision"`
		}
		if !decode(w, r, &input) {
			return true
		}
		id, parseErr := core.ParseBusinessID(input.TokenID)
		if parseErr != nil || input.Revision == 0 {
			fail(w, 400, "INVALID_REQUEST", "Refresh your API token details before changing it.")
			return true
		}
		if r.Method == http.MethodDelete {
			result, err = a.store.RevokeAPIToken(r.Context(), user.ID, id, input.Revision)
		} else {
			result, err = a.store.RotateAPIToken(r.Context(), user.ID, id, input.Revision)
		}
	default:
		fail(w, 405, "METHOD_NOT_ALLOWED", "This method is not supported.")
		return true
	}
	if err != nil {
		vendorFailure(w, err)
	} else {
		ok(w, result)
	}
	return true
}

func bearerFailure(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="tk-register", error="invalid_token"`)
	fail(w, http.StatusUnauthorized, "INVALID_API_TOKEN", "A valid TK Register API token is required.")
}

func (a *app) claudeAccountsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		fail(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Use POST to submit an account.")
		return
	}
	authorization := r.Header.Values("Authorization")
	if len(authorization) != 1 {
		bearerFailure(w)
		return
	}
	parts := strings.Fields(authorization[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		bearerFailure(w)
		return
	}
	user, token, err := a.store.AuthenticateAPIToken(r.Context(), parts[1])
	if errors.Is(err, core.ErrInvalidAPIToken) {
		bearerFailure(w)
		return
	}
	if err != nil {
		internal(w)
		return
	}
	if r.URL.RawQuery != "" {
		fail(w, 400, "INVALID_REQUEST", "Send account parameters in the JSON request body.")
		return
	}
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		fail(w, 415, "INVALID_CONTENT_TYPE", "Use application/json.")
		return
	}
	// Share the browser submission limit rather than granting a separate quota.
	retry, err := loginAttempt.Run(r.Context(), a.redis, []string{redisPrefix + "vendor-action:push:" + strconv.FormatUint(user.ID, 10)}, 10, 60).Int()
	if err != nil {
		internal(w)
		return
	}
	if retry > 0 {
		rateFailure(w, retry)
		return
	}
	var parameters map[string]any
	err = common.DecodeJson(http.MaxBytesReader(w, r.Body, 3<<20), &parameters)
	if err != nil || parameters == nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			fail(w, 413, "REQUEST_TOO_LARGE", "The JSON request body must not exceed 3 MiB.")
		} else {
			fail(w, 400, "INVALID_REQUEST", "Send one JSON object containing the account parameters.")
		}
		return
	}
	service := core.VendorService{Store: a.store, Transport: a.vendorTransport}
	if err := a.store.RecoverVendorExecutions(r.Context()); err != nil {
		internal(w)
		return
	}
	result, err := service.CreateAPI(r.Context(), user.ID, token.ID, parameters)
	if result.SubmissionID != "" {
		w.Header().Set("X-Submission-ID", result.SubmissionID)
	}
	if result.RequestID != "" {
		w.Header().Set("X-Request-ID", result.RequestID)
	}
	if result.HTTPStatus != 0 {
		if err != nil {
			// Never log request parameters, credentials, or unredacted response data.
			log.Printf("TK API submission persistence incomplete: submission=%s request=%s", result.SubmissionID, w.Header().Get("X-Request-ID"))
		}
		if result.ErrorCode != "" {
			fail(w, result.HTTPStatus, result.ErrorCode, result.Message)
			return
		}
		if result.ContentType != "" {
			w.Header().Set("Content-Type", result.ContentType)
		} else {
			w.Header().Set("Content-Type", "application/octet-stream")
		}
		w.WriteHeader(result.HTTPStatus)
		_, _ = w.Write(result.Body)
		return
	}
	if err != nil {
		vendorFailure(w, err)
		return
	}
	internal(w)
}
