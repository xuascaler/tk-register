package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"goapi-tk-register/server/internal/core"
)

const claudeAccountLookupAPIPath = claudeAccountsAPIPath + "/lookup"

// Keep the public contract independent of internal ownership and submission
// models. Neither submitted secrets nor supplier credentials belong here.
type accountLookupResult struct {
	ID                string                   `json:"id"`
	Username          string                   `json:"username"`
	Account           string                   `json:"account"`
	Vendor            string                   `json:"vendor"`
	Plan              string                   `json:"plan"`
	RemoteID          *string                  `json:"remote_id"`
	PlatformAccountID *string                  `json:"platform_account_id"`
	SubmissionStatus  string                   `json:"submission_status"`
	Snapshot          *core.AccountSnapshotDTO `json:"snapshot"`
	LastSuccessAt     *time.Time               `json:"last_success_at"`
	LastAttemptAt     *time.Time               `json:"last_attempt_at"`
	LastError         *string                  `json:"last_error"`
	IntervalSeconds   int                      `json:"interval_seconds"`
}

func lookupParameters(r *http.Request) (username, account string, valid bool) {
	if len(r.URL.RawQuery) > 4096 || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		return "", "", false
	}
	params, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(params) != 2 || len(params["username"]) != 1 || len(params["account"]) != 1 {
		return "", "", false
	}
	username = core.NormalizeEmail(params.Get("username"))
	account = strings.TrimSpace(params.Get("account"))
	return username, account, validAccountLookupIdentity(username, account)
}

func validAccountLookupIdentity(username, account string) bool {
	if !core.ValidEmail(username) || !utf8.ValidString(account) || len(account) == 0 || len(account) > 254 {
		return false
	}
	for _, c := range account {
		if unicode.IsSpace(c) || unicode.IsControl(c) {
			return false
		}
	}
	return true
}

func publicAccountLookup(row core.AccountDTO, username string, intervalSeconds int) accountLookupResult {
	var lastError *string
	if row.LastError != "" {
		lastError = &row.LastError
	}
	return accountLookupResult{
		ID: row.ID, Username: username, Account: row.Account, Vendor: "tk-register-0", Plan: row.Plan,
		RemoteID: row.RemoteID, PlatformAccountID: row.PlatformAccountID, SubmissionStatus: row.SubmissionStatus,
		Snapshot: row.Snapshot, LastSuccessAt: row.LastSuccessAt, LastAttemptAt: row.LastAttemptAt,
		LastError: lastError, IntervalSeconds: intervalSeconds,
	}
}

func (a *app) claudeAccountLookupAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		fail(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Use GET to look up an account.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	user, _, valid := a.authenticateBearer(w, r, core.AccountsReadScope)
	if !valid {
		return
	}
	// Independent from the submission limit; rotating a token does not reset it.
	key := redisPrefix + "account-lookup:" + strconv.FormatUint(user.ID, 10)
	retry, err := loginAttempt.Run(ctx, a.redis, []string{key}, 600, 60).Int()
	if err != nil {
		internal(w)
		return
	}
	if retry > 0 {
		rateFailure(w, retry)
		return
	}
	username, account, valid := lookupParameters(r)
	if !valid {
		fail(w, 400, "INVALID_REQUEST", "Provide exactly one username and account query parameter. Do not send a request body.")
		return
	}
	// Caller-supplied usernames never select another user's identity, including
	// when the token belongs to an administrator. Do not disclose their existence.
	if username != core.NormalizeEmail(user.Email) {
		fail(w, 404, "ACCOUNT_NOT_FOUND", "The requested account was not found.")
		return
	}
	row, err := a.store.LookupSyncedAccountForUser(ctx, user.ID, "tk-register-0", account)
	if errors.Is(err, core.ErrNotFound) {
		fail(w, 404, "ACCOUNT_NOT_FOUND", "The requested account was not found.")
		return
	}
	if err != nil {
		internal(w)
		return
	}
	settings, err := a.store.Settings(ctx)
	if err != nil {
		internal(w)
		return
	}
	ok(w, publicAccountLookup(row, user.Email, settings.Int("account_sync_interval_seconds")))
}
