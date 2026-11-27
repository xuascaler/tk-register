package main

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strings"
	"time"

	"goapi-tk-register/server/common"
	"goapi-tk-register/server/internal/core"
)

const proxyUnconfirmedMessage = "No clear response was received from the supplier. The proxy URL may already have been updated. This does not mean the update failed. Please do not resubmit immediately."

func accountCredentialProblem(err error) (authJSONProblem, bool) {
	switch {
	case errors.Is(err, core.ErrCurrentCredentialMissing):
		return authJSONProblem{409, "ACCOUNT_CREDENTIALS_MISSING", "The latest Auth JSON is not saved. Use Update Auth JSON to submit complete credentials containing the new Proxy URL."}, true
	case errors.Is(err, core.ErrCurrentCredentialUnknown):
		return authJSONProblem{409, "ACCOUNT_CREDENTIALS_UNCONFIRMED", proxyUnconfirmedMessage}, true
	case errors.Is(err, core.ErrCurrentCredentialChanged):
		return authJSONProblem{409, "ACCOUNT_CREDENTIALS_CHANGED", "Account credentials have changed. Reopen the dialog before continuing."}, true
	case errors.Is(err, core.ErrCurrentCredentialRunning):
		return authJSONProblem{409, "AUTH_JSON_UPDATE_RUNNING", "An update for this account is already running. Please wait."}, true
	}
	return authJSONProblem{}, false
}

func proxyFailure(w http.ResponseWriter, err error) {
	if p, known := accountCredentialProblem(err); known {
		authJSONFail(w, p)
		return
	}
	vendorFailure(w, err)
}

type accountProxyInput struct {
	ProxyURL  string `json:"proxy_url"`
	Revision  string `json:"revision"`
	TestToken string `json:"test_token"`
}

// Redis stores only a short-lived proof bound to the actor, accepted lifetime,
// credential revision and proxy digest, never the proxy URL or Auth JSON.
type accountProxyProof struct {
	ActorID      uint64
	AuthVersion  uint64
	AccountID    uint64
	OwnerID      uint64
	VendorID     uint64
	IdentityKey  string
	SubmissionID uint64
	Revision     string
	ProxyDigest  string
}

func proxyProof(user *core.User, target core.SyncedAccount, input accountProxyInput) accountProxyProof {
	return accountProxyProof{user.ID, user.AuthVersion, target.ID, target.UserID, target.VendorID, target.IdentityKey, *target.CurrentSubmissionID, input.Revision, digest(input.ProxyURL)}
}

func readAccountProxyInput(w http.ResponseWriter, r *http.Request) (accountProxyInput, string, error) {
	var input accountProxyInput
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return input, "", credentialProblem(415, "INVALID_CONTENT_TYPE", "Use application/json.")
	}
	if common.DecodeJson(http.MaxBytesReader(w, r.Body, 16<<10), &input) != nil {
		return input, "", credentialInvalid()
	}
	if !validCredentialProxy(input.ProxyURL) {
		return input, "", credentialProxyFailure("invalid_proxy")
	}
	if len(input.Revision) != 64 || strings.Trim(input.Revision, "0123456789abcdef") != "" {
		return input, "", credentialInvalid()
	}
	return input, input.Revision, nil
}

func (a *app) testAccountProxy(w http.ResponseWriter, r *http.Request, user *core.User, target core.SyncedAccount, info core.AccountProxyInfo) {
	input, _, err := readAccountProxyInput(w, r)
	if err != nil {
		proxyFailure(w, err)
		return
	}
	if input.Revision != info.Revision {
		proxyFailure(w, core.ErrCurrentCredentialChanged)
		return
	}
	if !a.credentialRate(w, r, user.ID, "proxy-test", 30) {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithValue(r.Context(), credentialProxyTestContextKey{}, true), 20*time.Second)
	defer cancel()
	result, err := a.callCredentialHelper(ctx, user.ID, "/proxy-test", map[string]any{"proxy_url": input.ProxyURL})
	if err != nil {
		vendorFailure(w, err)
		return
	}
	current, err := a.store.AccountProxyInfo(ctx, target)
	if err != nil {
		proxyFailure(w, err)
		return
	}
	if current.Revision != input.Revision {
		proxyFailure(w, core.ErrCurrentCredentialChanged)
		return
	}
	token, err := randomToken()
	if err != nil {
		internal(w)
		return
	}
	encoded, err := json.Marshal(proxyProof(user, target, input))
	if err != nil {
		internal(w)
		return
	}
	if err = a.redis.Set(ctx, redisPrefix+"account-proxy-test:"+token, encoded, 10*time.Minute).Err(); err != nil {
		internal(w)
		return
	}
	ok(w, map[string]any{"test_token": token, "expires_in": 600, "proxy_check": result.ProxyCheck})
}

func (a *app) resolveAccountProxy(r *http.Request, w http.ResponseWriter, user *core.User, target core.SyncedAccount) (json.RawMessage, *string, error) {
	input, revision, err := readAccountProxyInput(w, r)
	if err != nil {
		return nil, nil, err
	}
	// Report a credential conflict before a test expiry so an obsolete dialog
	// cannot encourage testing/reusing the old payload.
	raw, err := a.store.ReplaceAccountProxy(r.Context(), target, revision, input.ProxyURL)
	if err != nil {
		return nil, nil, err
	}
	testRequired := credentialProblem(409, "PROXY_TEST_REQUIRED", "Test the new proxy connection again before saving.")
	if !credentialFlowID.MatchString(input.TestToken) {
		return nil, nil, testRequired
	}
	encoded, err := a.redis.Get(r.Context(), redisPrefix+"account-proxy-test:"+input.TestToken).Bytes()
	if err != nil {
		return nil, nil, testRequired
	}
	var proof accountProxyProof
	if json.Unmarshal(encoded, &proof) != nil || proof != proxyProof(user, target, input) {
		return nil, nil, testRequired
	}
	return raw, &revision, nil
}
