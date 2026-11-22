package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"goapi-tk-register/server/internal/core"
)

const claudeAccountDailyUsageAPIPath = claudeAccountsAPIPath + "/daily-usage"
const dailyUsageAPIAccountLimit = core.MaxDailyUsageBatchAccounts
const dailyUsageAPIBodyLimit = 64 << 10

type dailyUsageAPIInput struct {
	Username string
	Accounts []string
	Date     string
}

// Parse exactly one object. Duplicate keys and unsupported options must not
// silently change the owner, account selection or billing source.
func decodeDailyUsageAPIInput(body []byte) (dailyUsageAPIInput, error) {
	var input dailyUsageAPIInput
	invalid := errors.New("provide username, accounts and date in one JSON object")
	if !utf8.Valid(body) {
		return input, invalid
	}
	d := json.NewDecoder(bytes.NewReader(body))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return input, invalid
	}
	seen := make(map[string]bool, 3)
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
		case "accounts":
			err = d.Decode(&input.Accounts)
		case "date":
			err = d.Decode(&input.Date)
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
	if d.Decode(new(any)) != io.EOF || len(seen) != 3 || len(input.Accounts) == 0 || len(input.Accounts) > dailyUsageAPIAccountLimit {
		return input, invalid
	}
	input.Username = core.NormalizeEmail(input.Username)
	accounts := make([]string, 0, len(input.Accounts))
	unique := make(map[string]bool, len(input.Accounts))
	for _, account := range input.Accounts {
		account = strings.ToLower(strings.TrimSpace(account))
		if !validAccountLookupIdentity(input.Username, account) {
			return input, invalid
		}
		if !unique[account] {
			unique[account] = true
			accounts = append(accounts, account)
		}
	}
	input.Accounts = accounts
	return input, nil
}

func (a *app) claudeAccountDailyUsageAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		fail(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Use POST to query daily account usage.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	user, _, valid := a.authenticateBearer(w, r, core.AccountsReadScope)
	if !valid {
		return
	}
	// Count by user, not token, so token rotation cannot reset the batch limit.
	key := redisPrefix + "account-daily-usage:" + strconv.FormatUint(user.ID, 10)
	retry, err := loginAttempt.Run(ctx, a.redis, []string{key}, 60, 60).Int()
	if err != nil {
		dailyUsageAPIUnavailable(w, ctx, err)
		return
	}
	if retry > 0 {
		rateFailure(w, retry)
		return
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		fail(w, 400, "INVALID_REQUEST", "Send username, accounts and date in the JSON request body.")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		fail(w, 415, "INVALID_CONTENT_TYPE", "Use application/json.")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, dailyUsageAPIBodyLimit))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			fail(w, 413, "REQUEST_TOO_LARGE", "The JSON request body must not exceed 64 KiB.")
		} else {
			fail(w, 400, "INVALID_REQUEST", "Could not read the JSON request body.")
		}
		return
	}
	input, err := decodeDailyUsageAPIInput(body)
	if err != nil {
		fail(w, 400, "INVALID_REQUEST", "Provide username, 1 to 100 accounts, and date in one JSON object. Do not include extra or duplicate fields.")
		return
	}
	if input.Username != core.NormalizeEmail(user.Email) {
		fail(w, 404, "ACCOUNT_NOT_FOUND", "The requested account was not found.")
		return
	}
	if err := core.ValidateDailyUsageRange(input.Date, input.Date, time.Now()); err != nil {
		fail(w, 400, "INVALID_DATE", "Use YYYY-MM-DD for a complete day, no later than yesterday in Beijing time.")
		return
	}
	modelDetails, err := a.store.APIUserModelCostsEnabled(ctx)
	if err != nil {
		dailyUsageAPIUnavailable(w, ctx, err)
		return
	}
	out, err := a.store.AccountDailyUsageBatch(ctx, user.ID, "tk-register-0", input.Accounts, input.Date, modelDetails)
	if err != nil {
		dailyUsageAPIUnavailable(w, ctx, err)
		return
	}
	ok(w, struct {
		Username string `json:"username"`
		core.DailyUsageBatchDTO
	}{Username: user.Email, DailyUsageBatchDTO: out})
}

func dailyUsageAPIUnavailable(w http.ResponseWriter, ctx context.Context, err error) {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		fail(w, 504, "REQUEST_TIMEOUT", "The daily usage query timed out. Try again later.")
		return
	}
	internal(w)
}
