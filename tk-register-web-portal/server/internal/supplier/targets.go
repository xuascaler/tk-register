package supplier

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxBatchTargets        = 20
	maxBatchSearchRequests = 40
	maxBatchWorkers        = 3
	maxBatchDuration       = 45 * time.Second
	maxTargetDuration      = 8 * time.Second
	minBatchReadInterval   = 250 * time.Millisecond
)

// AccountResult is ordered like the input; Account and Err are mutually exclusive.
type AccountResult struct {
	Target  Target
	Account *Account
	Err     error
}

// CollectTargets reads only the supplied current-page accounts, using one login,
// at most three concurrent filtered searches, and at most one shared re-login.
// Each target has an independent outcome; initial login failure is batch-level.
func (c *Client) CollectTargets(ctx context.Context, username, password string, targets []Target) ([]AccountResult, error) {
	if len(targets) > maxBatchTargets {
		return nil, safeError(CodeContractMismatch)
	}
	results := make([]AccountResult, len(targets))
	if len(targets) == 0 {
		return results, nil
	}
	ctx, cancel := context.WithTimeout(ctx, maxBatchDuration)
	defer cancel()
	s := c.newSession()
	defer s.http.CloseIdleConnections()
	s.classifyReadErrors = true
	if err := s.login(ctx, username, password); err != nil {
		return nil, err
	}
	batch := &targetBatch{ctx: ctx, http: s.http, username: username, password: password, token: s.token}
	// Initial login already used two HTTP requests. The remaining budget permits
	// forty searches, one two-request re-login, and up to three old-token retries.
	batch.remaining.Store(maxBatchSearchRequests + maxBatchWorkers + 2)
	batch.searches.Store(maxBatchSearchRequests)
	s.targetReadDelegate = batch.read
	type key struct{ account, recordID string }
	unique := []Target{}
	indices := make([]int, len(targets))
	byKey := map[key]int{}
	for i, target := range targets {
		k := key{normalizeAccount(target.Account), target.RecordID}
		index, found := byKey[k]
		if !found {
			index = len(unique)
			byKey[k] = index
			unique = append(unique, target)
		}
		indices[i] = index
	}
	collected := make([]AccountResult, len(unique))
	jobs := make(chan int, len(unique))
	for i := range unique {
		jobs <- i
	}
	close(jobs)
	var workers sync.WaitGroup
	for n := 0; n < min(maxBatchWorkers, len(unique)); n++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range jobs {
				target := unique[i]
				result := AccountResult{Target: target}
				if ctx.Err() != nil {
					result.Err = collectionTransportError(ctx, ctx.Err())
				} else {
					targetCtx, targetCancel := context.WithTimeout(ctx, maxTargetDuration)
					row, err := s.findTargetAccount(targetCtx, username, password, target, false)
					if err == nil {
						var account Account
						account, err = parseAccount(row.rawAccount)
						if err == nil {
							result.Account = &account
						}
					}
					result.Err = err
					targetCancel()
				}
				collected[i] = result
			}
		}()
	}
	workers.Wait()
	batch.reauthWork.Wait()
	for i, target := range targets {
		results[i] = collected[indices[i]]
		results[i].Target = target
	}
	return results, nil
}

// The coordinator alone owns authentication and budgets. Each actual HTTP call
// has a private session wrapper, so legacy request()/login() fields are never
// shared across workers. Locks are never held during network I/O or pacing.
type targetBatch struct {
	ctx                 context.Context
	http                *http.Client
	username, password  string
	remaining, searches atomic.Int32
	auth                sync.Mutex
	token               string
	generation          uint64
	reauthenticated     bool
	refreshing          chan struct{}
	authError           error
	reauthWork          sync.WaitGroup
	paceInit            sync.Once
	pace                chan struct{}
	nextRead            time.Time
}

func takeBudget(b *atomic.Int32, n int32) bool {
	for {
		left := b.Load()
		if left < n {
			return false
		}
		if b.CompareAndSwap(left, left-n) {
			return true
		}
	}
}
func collectionTransportError(ctx context.Context, err error) error {
	var network net.Error
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &network) && network.Timeout()) {
		return safeError(CodeTimeout)
	}
	return safeError(CodeUnavailable)
}
func validTargetPath(path string) bool {
	query, err := url.ParseQuery(strings.TrimPrefix(path, apiPrefix+"/accounts?"))
	return strings.HasPrefix(path, apiPrefix+"/accounts?") && err == nil && strings.TrimSpace(query.Get("keyword")) != "" && query.Get("provider") == "claude" && query.Get("account_type") == "2"
}
func (b *targetBatch) waitRead(ctx context.Context) error {
	b.paceInit.Do(func() { b.pace = make(chan struct{}, 1); b.pace <- struct{}{} })
	select {
	case <-ctx.Done():
		return collectionTransportError(ctx, ctx.Err())
	case <-b.pace:
	}
	defer func() { b.pace <- struct{}{} }()
	if wait := time.Until(b.nextRead); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return collectionTransportError(ctx, ctx.Err())
		case <-timer.C:
		}
	}
	if ctx.Err() != nil {
		return collectionTransportError(ctx, ctx.Err())
	}
	b.nextRead = time.Now().Add(minBatchReadInterval)
	return nil
}
func (b *targetBatch) get(ctx context.Context, path, token string) (int, []byte, error) {
	if ctx.Err() != nil {
		return 0, nil, collectionTransportError(ctx, ctx.Err())
	}
	if !takeBudget(&b.remaining, 1) {
		return 0, nil, safeError(CodeUnavailable)
	}
	if err := b.waitRead(ctx); err != nil {
		return 0, nil, err
	}
	local := &session{http: b.http, token: token, remaining: 1, classifyReadErrors: true}
	return local.request(ctx, http.MethodGet, path, nil)
}
func (b *targetBatch) read(ctx context.Context, _, _, path string) (int, []byte, error) {
	if !validTargetPath(path) {
		return 0, nil, safeError(CodeContractMismatch)
	}
	if ctx.Err() != nil {
		return 0, nil, collectionTransportError(ctx, ctx.Err())
	}
	b.auth.Lock()
	token, generation, authErr := b.token, b.generation, b.authError
	b.auth.Unlock()
	if authErr != nil {
		return 0, nil, authErr
	}
	if !takeBudget(&b.searches, 1) {
		return 0, nil, safeError(CodeUnavailable)
	}
	status, body, err := b.get(ctx, path, token)
	if err != nil || (status != http.StatusUnauthorized && status != http.StatusForbidden) {
		return status, body, err
	}
	token, err = b.refresh(ctx, generation)
	if err != nil {
		return 0, nil, err
	}
	status, body, err = b.get(ctx, path, token)
	if err == nil && (status == http.StatusUnauthorized || status == http.StatusForbidden) {
		b.auth.Lock()
		b.authError = safeError(CodeSessionExpired)
		err = b.authError
		b.auth.Unlock()
	}
	return status, body, err
}
func (b *targetBatch) refresh(ctx context.Context, oldGeneration uint64) (string, error) {
	for {
		if ctx.Err() != nil {
			return "", collectionTransportError(ctx, ctx.Err())
		}
		b.auth.Lock()
		if b.authError != nil {
			err := b.authError
			b.auth.Unlock()
			return "", err
		}
		if b.generation != oldGeneration {
			token := b.token
			b.auth.Unlock()
			return token, nil
		}
		waiting := b.refreshing
		if waiting == nil {
			if b.reauthenticated {
				b.authError = safeError(CodeSessionExpired)
				err := b.authError
				b.auth.Unlock()
				return "", err
			}
			b.reauthenticated = true
			waiting = make(chan struct{})
			b.refreshing = waiting
			b.reauthWork.Add(1)
			go b.relogin(waiting)
		}
		b.auth.Unlock()
		select {
		case <-ctx.Done():
			return "", collectionTransportError(ctx, ctx.Err())
		case <-waiting:
		}
	}
}
func (b *targetBatch) relogin(done chan struct{}) {
	defer b.reauthWork.Done()
	ctx, cancel := context.WithTimeout(b.ctx, maxTargetDuration)
	defer cancel()
	local := &session{http: b.http, remaining: 2, classifyReadErrors: true}
	var err error
	if !takeBudget(&b.remaining, 2) {
		err = safeError(CodeUnavailable)
	} else {
		err = local.login(ctx, b.username, b.password)
	}
	b.auth.Lock()
	defer b.auth.Unlock()
	if err != nil {
		b.authError = err
	} else {
		b.token = local.token
		b.generation++
	}
	b.refreshing = nil
	close(done)
}

// targetRead retries only an expired read once. Authentication failure is sticky
// within this session, so a page containing many accounts cannot trigger repeated
// logins or unauthenticated reads. Mutations never use this helper.
func (s *session) targetRead(ctx context.Context, username, password, path string) (int, []byte, error) {
	if s.targetReadDelegate != nil {
		return s.targetReadDelegate(ctx, username, password, path)
	}
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
