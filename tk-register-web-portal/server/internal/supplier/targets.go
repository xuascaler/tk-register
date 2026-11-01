package supplier

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maxBatchTargets        = 20
	maxBatchSearchRequests = 40
	maxBatchDuration       = 45 * time.Second
	maxTargetDuration      = 8 * time.Second
)

// AccountResult corresponds to the target at the same position in CollectTargets'
// input. Account and Err are mutually exclusive. No supplier secrets are exposed.
type AccountResult struct {
	Target  Target
	Account *Account
	Err     error
}

// CollectTargets collects only the requested current-page accounts. It shares
// one short-lived website session, with at most one re-login across the batch.
// Searches are sequential to avoid session races; each result is independent.
// A failed initial login is returned at batch level without making account reads.
func (c *Client) CollectTargets(ctx context.Context, username, password string, targets []Target) ([]AccountResult, error) {
	if len(targets) > maxBatchTargets {
		return nil, safeError(CodeContractMismatch)
	}
	results := make([]AccountResult, 0, len(targets))
	if len(targets) == 0 {
		return results, nil
	}
	ctx, cancel := context.WithTimeout(ctx, maxBatchDuration)
	defer cancel()
	s := c.newSession()
	defer s.http.CloseIdleConnections()
	// Reserve four requests for the initial login and one re-login, plus one
	// retry of a read rejected with 401/403. All requests share this hard cap.
	s.remaining = maxBatchSearchRequests + 5
	s.targetReadLimit = maxBatchSearchRequests
	if err := s.login(ctx, username, password); err != nil {
		return nil, err
	}
	type key struct{ account, recordID string }
	cached := make(map[key]AccountResult)
	for _, target := range targets {
		result := AccountResult{Target: target}
		cacheKey := key{normalizeAccount(target.Account), target.RecordID}
		if previous, ok := cached[cacheKey]; ok {
			result.Account, result.Err = previous.Account, previous.Err
			results = append(results, result)
			continue
		}
		if ctx.Err() != nil || s.remaining <= 0 {
			result.Err = safeError(CodeUnavailable)
		} else {
			// A slow account cannot consume the entire page's collection window.
			targetCtx, targetCancel := context.WithTimeout(ctx, maxTargetDuration)
			row, err := s.findTargetAccount(targetCtx, username, password, target, false)
			targetExpired := targetCtx.Err() != nil
			targetCancel()
			if err == nil {
				var account Account
				account, err = parseAccount(row.rawAccount)
				if err == nil {
					result.Account = &account
				}
			}
			if err != nil && (targetExpired || ctx.Err() != nil || s.remaining <= 0) {
				err = safeError(CodeUnavailable)
			}
			result.Err = err
		}
		cached[cacheKey] = result
		results = append(results, result)
	}
	return results, nil
}

// targetRead retries only an expired read once. Authentication failure is sticky
// within this session, so a page containing many accounts cannot trigger repeated
// logins or unauthenticated reads. Mutations never use this helper.
func (s *session) targetRead(ctx context.Context, username, password, path string) (int, []byte, error) {
	if s.readAuthError != nil {
		return 0, nil, s.readAuthError
	}
	if ctx.Err() != nil {
		return 0, nil, safeError(CodeUnavailable)
	}
	// The caller always provides a filtered account search. Guard against future
	// accidental reuse of this helper for an unfiltered inventory or mutation.
	query, err := url.ParseQuery(strings.TrimPrefix(path, apiPrefix+"/accounts?"))
	if !strings.HasPrefix(path, apiPrefix+"/accounts?") || err != nil || strings.TrimSpace(query.Get("keyword")) == "" || query.Get("provider") != "claude" || query.Get("account_type") != "2" {
		return 0, nil, safeError(CodeContractMismatch)
	}
	if s.targetReadLimit > 0 && s.targetReads >= s.targetReadLimit {
		return 0, nil, safeError(CodeUnavailable)
	}
	s.targetReads++
	status, body, err := s.request(ctx, http.MethodGet, path, nil)
	if err != nil || (status != http.StatusUnauthorized && status != http.StatusForbidden) {
		return status, body, err
	}
	if s.readReauthenticated {
		s.readAuthError = safeError(CodeSessionExpired)
		return 0, nil, s.readAuthError
	}
	s.readReauthenticated = true
	if err = s.login(ctx, username, password); err != nil {
		s.readAuthError = err
		return 0, nil, err
	}
	status, body, err = s.request(ctx, http.MethodGet, path, nil)
	if err == nil && (status == http.StatusUnauthorized || status == http.StatusForbidden) {
		s.readAuthError = safeError(CodeSessionExpired)
		return 0, nil, s.readAuthError
	}
	return status, body, err
}
