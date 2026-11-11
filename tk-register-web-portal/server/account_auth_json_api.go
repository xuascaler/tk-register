package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"goapi-tk-register/server/internal/core"
)

const claudeAccountAuthJSONAPIPath = claudeAccountsAPIPath + "/auth-json"

type authJSONAPIInput struct {
	Username string
	Account  string
	AuthJSON any
}

// Reject duplicate and unknown envelope keys rather than allowing the last
// username/account to silently replace an earlier identity in the same body.
func decodeAuthJSONAPIInput(reader io.Reader) (authJSONAPIInput, error) {
	var input authJSONAPIInput
	invalid := errors.New("provide username, account and auth_json in one JSON object")
	d := json.NewDecoder(reader)
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return input, invalid
	}
	seen := make(map[string]bool)
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return input, invalid
		}
		name, valid := key.(string)
		if !valid || seen[name] {
			return input, invalid
		}
		seen[name] = true
		switch name {
		case "username":
			err = d.Decode(&input.Username)
		case "account":
			err = d.Decode(&input.Account)
		case "auth_json":
			err = d.Decode(&input.AuthJSON)
		default:
			return input, invalid
		}
		if err != nil {
			return input, invalid
		}
	}
	if end, err := d.Token(); err != nil || end != json.Delim('}') {
		return input, invalid
	}
	if d.Decode(new(any)) != io.EOF || len(seen) != 3 {
		return input, invalid
	}
	input.Username = core.NormalizeEmail(input.Username)
	input.Account = strings.TrimSpace(input.Account)
	if !validAccountLookupIdentity(input.Username, input.Account) {
		return input, invalid
	}
	return input, nil
}

func (a *app) claudeAccountAuthJSONAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		fail(w, 405, "METHOD_NOT_ALLOWED", "Use POST to update account Auth JSON.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 70*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	user, token, valid := a.authenticateBearer(w, r, core.ClaudeAuthJSONUpdateScope)
	if !valid {
		return
	}
	if r.URL.RawQuery != "" {
		fail(w, 400, "INVALID_REQUEST", "Send username, account and auth_json in the JSON request body.")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		fail(w, 415, "INVALID_CONTENT_TYPE", "Use application/json.")
		return
	}
	input, err := decodeAuthJSONAPIInput(http.MaxBytesReader(w, r.Body, 3<<20))
	if err != nil {
		fail(w, 400, "INVALID_REQUEST", "Provide username, account and auth_json in one JSON object. Do not include extra or duplicate fields.")
		return
	}
	// An administrator's personal token is still owner-scoped. Caller-supplied
	// usernames never select another user's credentials or disclose their data.
	if input.Username != core.NormalizeEmail(user.Email) {
		fail(w, 404, "ACCOUNT_NOT_FOUND", "The requested account was not found.")
		return
	}
	row, err := a.store.LookupSyncedAccountForUser(ctx, user.ID, "tk-register-0", input.Account)
	if errors.Is(err, core.ErrNotFound) {
		fail(w, 404, "ACCOUNT_NOT_FOUND", "The requested account was not found.")
		return
	}
	if err != nil {
		logUnexpectedVendorFailure(w, err)
		internal(w)
		return
	}
	id, err := core.ParseBusinessID(row.ID)
	if err != nil {
		internal(w)
		return
	}
	settings, err := a.store.Settings(ctx)
	if err != nil {
		internal(w)
		return
	}
	lookup := func(id uint64) (core.AccountDTO, error) {
		// The write can finish after the caller disconnects; use a separate,
		// bounded read for its response just as the mutation uses a bounded ctx.
		readCtx, readCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer readCancel()
		return a.store.SyncedAccountForUser(readCtx, user.ID, id)
	}
	a.runAccountAuthJSON(w, r, &user, id, false, lookup, authJSONOperation{
		Token: &token, Input: input.AuthJSON,
		FormatAccount: func(account core.AccountDTO) any {
			return publicAccountLookup(account, user.Email, settings.Int("account_sync_interval_seconds"))
		},
	})
}
