package supplier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"goapi-tk-register/server/common"
	"goapi-tk-register/server/internal/diagnostics"
)

const (
	collectionPageSize     = 500
	maxInventory           = 10000
	collectionMaxPages     = maxInventory / collectionPageSize
	collectionPageInterval = 10 * time.Millisecond
	collectionPageRetries  = 2
)

// CollectionError describes an incomplete inventory read. PartialSafe explicitly
// authorizes using its returned complete pages; arbitrary rows plus an error do not.
type CollectionError struct {
	Cause                                                          error
	CompletedPages, FailedPage, TotalRecords, PageSize, RetryCount int
	LastCollectedAt                                                time.Time
	PartialSafe                                                    bool
}

func (e *CollectionError) Error() string { return e.Cause.Error() }
func (e *CollectionError) Unwrap() error { return e.Cause }
func CollectionDetails(err error) (*CollectionError, bool) {
	var detail *CollectionError
	ok := errors.As(err, &detail)
	return detail, ok
}

// Collect validates entire pages before retaining them. Operational interruptions
// preserve those pages; inconsistent or malformed inventories are rejected in full.
func (c *Client) Collect(ctx context.Context, username, password string) (result []Account, resultErr error) {
	s := c.newSession()
	s.remaining = collectionMaxPages*(collectionPageRetries+1) + 5
	s.classifyReadErrors = true
	s.retryAccountQueries = true
	defer s.http.CloseIdleConnections()
	progress := &CollectionError{FailedPage: 1, PageSize: collectionPageSize}
	defer func() {
		diagnostics.LogCollection(ctx, diagnostics.CollectionEvent{
			Complete: resultErr == nil, TotalRecords: progress.TotalRecords,
			CollectedRecords: len(result), CompletedPages: progress.CompletedPages,
			FailedPage: progress.FailedPage, PageSize: progress.PageSize, RetryCount: progress.RetryCount,
		})
	}()
	accounts := make([]Account, 0)
	fail := func(err error) ([]Account, error) {
		progress.Cause = err
		var upstream *Error
		if errors.As(err, &upstream) && upstream.Code == CodeContractMismatch {
			return nil, progress
		}
		progress.PartialSafe = progress.CompletedPages > 0
		return accounts, progress
	}
	if err := s.authenticate(ctx, username, password, false); err != nil {
		return fail(err)
	}
	seen := make(map[string]bool)
	reauthenticated := false
	for page := 1; page <= collectionMaxPages; page++ {
		progress.FailedPage = page
		if page > 1 {
			if err := waitCollection(ctx, collectionPageInterval); err != nil {
				return fail(collectionTransportError(ctx, err))
			}
		}
		params := url.Values{
			"status": {"all"}, "provider": {"claude"}, "account_type": {"2"},
			"page": {strconv.Itoa(page)}, "page_size": {strconv.Itoa(collectionPageSize)},
		}
		path := apiPrefix + "/accounts?" + params.Encode()
		var status int
		var body []byte
		var err error
		for retry := 0; ; {
			status, body, err = s.request(ctx, http.MethodGet, path, nil)
			if err == nil && (status == http.StatusUnauthorized || status == http.StatusForbidden) {
				if reauthenticated {
					return fail(safeError(CodeSessionExpired))
				}
				reauthenticated = true
				if err = s.reauthenticate(ctx, username, password); err != nil {
					return fail(err)
				}
				status, body, err = s.request(ctx, http.MethodGet, path, nil)
				if err == nil && (status == http.StatusUnauthorized || status == http.StatusForbidden) {
					return fail(safeError(CodeSessionExpired))
				}
			}
			if err == nil {
				break
			}
			seconds, retryAt, stage, _ := RetryDetails(err)
			if seconds <= 0 || stage != "accounts" || retry >= collectionPageRetries {
				return fail(err)
			}
			wait := time.Duration(seconds) * time.Second
			if !retryAt.IsZero() {
				wait = max(time.Duration(0), time.Until(retryAt))
			}
			// Preserve the rate-limit cause if its advertised wait cannot fit in
			// the caller's budget. No request is sent before Retry-After elapses.
			if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= wait {
				return fail(err)
			}
			if waitErr := waitCollection(ctx, wait); waitErr != nil {
				return fail(err)
			}
			retry++
			progress.RetryCount++
		}
		if err = readStatus(status); err != nil {
			return fail(err)
		}
		var envelope struct {
			List  json.RawMessage `json:"list"`
			Total json.RawMessage `json:"total"`
		}
		if common.Unmarshal(body, &envelope) != nil {
			return fail(contractError(page, "invalid response envelope"))
		}
		total, err := optionalInt(envelope.Total)
		if err != nil || total == nil || *total > maxInventory || len(envelope.List) == 0 || strings.TrimSpace(string(envelope.List)) == "null" {
			return fail(contractError(page, "missing or invalid total/list, or inventory exceeds the supported limit"))
		}
		if page > 1 && int(*total) != progress.TotalRecords {
			return fail(contractError(page, "the supplier inventory total changed during pagination"))
		}
		progress.TotalRecords = int(*total)
		var rows []rawAccount
		if common.Unmarshal(envelope.List, &rows) != nil || len(rows) > collectionPageSize || int64(len(accounts)+len(rows)) > *total {
			return fail(contractError(page, "invalid list or too many rows"))
		}
		if len(rows) == 0 && int64(len(accounts)) < *total {
			return fail(contractError(page, "an empty page was returned before the reported total"))
		}
		pageAccounts := make([]Account, 0, len(rows))
		for index, row := range rows {
			account, err := parseAccount(row)
			if err != nil {
				return fail(contractError(page, fmt.Sprintf("row %d contains invalid account or usage fields", index+1)))
			}
			if seen[account.ID] {
				return fail(contractError(page, "an account record was repeated across pages; the supplier inventory may have changed during pagination"))
			}
			seen[account.ID] = true
			pageAccounts = append(pageAccounts, account)
		}
		progress.LastCollectedAt = time.Now().UTC()
		for i := range pageAccounts {
			pageAccounts[i].CollectedAt = progress.LastCollectedAt
		}
		accounts = append(accounts, pageAccounts...)
		progress.CompletedPages++
		if len(accounts) == progress.TotalRecords {
			progress.FailedPage = 0
			return accounts, nil
		}
	}
	return fail(contractError(collectionMaxPages, "pagination limit reached before the reported total"))
}

func waitCollection(ctx context.Context, wait time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= wait {
		return context.DeadlineExceeded
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
