package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-redis/redis/v8"
	"goapi-tk-register/server/internal/core"
	"goapi-tk-register/server/internal/diagnostics"
	"golang.org/x/crypto/bcrypt"
)

// Successful validation does not remove the code. SQL commits proof consumption
// with the password change; an unavailable database must not burn a valid code.
var validatePasswordChangeCode = redis.NewScript(`
 local hash=redis.call('HGET',KEYS[1],'hash');if not hash then return '' end
 if hash~=ARGV[1] then
  local n=redis.call('HINCRBY',KEYS[1],'attempts',1)
  if n>=tonumber(ARGV[2]) then redis.call('DEL',KEYS[1]) end
  return ''
 end
 return redis.call('HGET',KEYS[1],'request') or ''
`)

func (a *app) passwordRoutes(w http.ResponseWriter, r *http.Request, route string) bool {
	if route != "account/password/code" && route != "account/password" {
		return false
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		fail(w, 405, "METHOD_NOT_ALLOWED", "Use POST for this request.")
		return true
	}
	u, s, good := a.authenticated(w, r, true)
	if !good {
		return true
	}
	if u.EmailVerifiedAt.IsZero() {
		fail(w, 403, "EMAIL_NOT_VERIFIED", "A verified email address is required.")
		return true
	}
	r = r.WithContext(diagnostics.WithTrace(r.Context(), diagnostics.Trace{ActorID: u.ID}))
	if route == "account/password/code" {
		a.sendPasswordChangeCode(w, r, u, s)
	} else {
		a.changePassword(w, r, u)
	}
	return true
}

func passwordChangePurpose(challenge string) string { return "password_change:" + digest(challenge) }

func (a *app) sendPasswordChangeCode(w http.ResponseWriter, r *http.Request, u *core.User, s *session) {
	var in struct{}
	if !decode(w, r, &in) {
		return
	}
	v, good := a.config(w, r)
	if !good {
		return
	}
	token, err := randomToken()
	if err != nil {
		internal(w)
		return
	}
	cookie, _ := r.Cookie(cookieName)
	ttl := v.Int("password_change_code_ttl_seconds")
	if remaining := int(s.Expires - time.Now().Unix()); remaining < ttl {
		ttl = remaining
	}
	if ttl < 1 {
		a.cookie(w, "", -1)
		fail(w, 401, "UNAUTHENTICATED", "Please sign in again.")
		return
	}
	c := &core.PasswordChangeChallenge{ID: digest(token), UserID: u.ID, Email: u.Email, Role: u.Role, AuthVersion: u.AuthVersion, SessionHash: digest(cookie.Value), CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Duration(ttl) * time.Second)}
	purpose := passwordChangePurpose(token)
	if err = a.deliverPrepared(r.Context(), u.Email, purpose, clientIP(r), ttl, v, true, func() error { return a.store.CreatePasswordChange(r.Context(), c) }); err != nil {
		_ = a.store.DB.WithContext(r.Context()).Where("id = ? AND status = ?", c.ID, "pending").Delete(&core.PasswordChangeChallenge{}).Error
		diagnostics.LogPasswordChange(r.Context(), "send", "delivery_failed")
		deliveryFailure(w, err)
		return
	}
	if err = a.store.ActivatePasswordChange(r.Context(), c); err != nil {
		_ = a.redis.Del(r.Context(), codeKey(u.Email, purpose)).Err()
		passwordChangeFailure(w, r, "send", err)
		return
	}
	diagnostics.LogPasswordChange(r.Context(), "send", "sent")
	remaining := int(time.Until(c.ExpiresAt).Seconds())
	if remaining < 0 {
		remaining = 0
	}
	ok(w, map[string]any{"challenge_id": token, "expires_in": remaining, "retry_after": v.Int("email_send_cooldown_seconds"), "code_length": 6})
}

type passwordChangeInput struct {
	ChallengeID  string `json:"challenge_id"`
	Code         string `json:"code"`
	Password     string `json:"password"`
	Confirmation string `json:"confirmation"`
	RequestID    string `json:"request_id"`
}

func validPasswordOperationID(id string) bool {
	if len(id) < 16 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func (a *app) passwordRequestFingerprint(in passwordChangeInput) string {
	h := hmac.New(sha256.New, []byte(a.secret))
	_, _ = h.Write([]byte("tk-register:password-change\x00" + in.ChallengeID + "\x00" + in.Password + "\x00" + strings.ToUpper(strings.TrimSpace(in.Code))))
	return hex.EncodeToString(h.Sum(nil))
}

func passwordChangeFailure(w http.ResponseWriter, r *http.Request, stage string, err error) {
	if errors.Is(err, core.ErrPasswordChangeConflict) || errors.Is(err, core.ErrNotFound) {
		diagnostics.LogPasswordChange(r.Context(), stage, "conflict")
		fail(w, 409, "PASSWORD_CHANGE_CONFLICT", "This password change has expired or your account changed. Request a new code.")
		return
	}
	diagnostics.LogPasswordChange(r.Context(), stage, "unavailable")
	internal(w)
}

func (a *app) changePassword(w http.ResponseWriter, r *http.Request, u *core.User) {
	var in passwordChangeInput
	if !decode(w, r, &in) {
		return
	}
	if !utf8.ValidString(in.Password) || utf8.RuneCountInString(in.Password) < 8 || len(in.Password) > 72 {
		fail(w, 400, "INVALID_PASSWORD", "Use at least 8 characters and no more than 72 UTF-8 bytes.")
		return
	}
	if in.Password != in.Confirmation {
		fail(w, 400, "PASSWORD_MISMATCH", "The passwords do not match.")
		return
	}
	if len(in.ChallengeID) != 43 || !validPasswordOperationID(in.RequestID) {
		fail(w, 400, "INVALID_REQUEST", "Request a code before changing your password.")
		return
	}
	c, err := a.store.PasswordChange(r.Context(), digest(in.ChallengeID))
	if err != nil {
		passwordChangeFailure(w, r, "change", err)
		return
	}
	cookie, _ := r.Cookie(cookieName)
	if c.UserID != u.ID || c.Email != u.Email || c.Role != u.Role || c.AuthVersion != u.AuthVersion || c.SessionHash != digest(cookie.Value) || !c.ExpiresAt.After(time.Now()) || c.Status != "sent" {
		passwordChangeFailure(w, r, "change", core.ErrPasswordChangeConflict)
		return
	}
	v, good := a.config(w, r)
	if !good {
		return
	}
	purpose := passwordChangePurpose(in.ChallengeID)
	verification, err := validatePasswordChangeCode.Run(r.Context(), a.redis, []string{codeKey(u.Email, purpose)}, a.codeHash(u.Email, purpose, in.Code), v.Int("verification_max_attempts")).Text()
	if err != nil {
		passwordChangeFailure(w, r, "change", err)
		return
	}
	if verification == "" {
		diagnostics.LogPasswordChange(r.Context(), "change", "invalid_code")
		fail(w, 400, "INVALID_CODE", "The code is incorrect or expired. Request a new code if needed.")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(in.Password)) == nil {
		fail(w, 400, "PASSWORD_UNCHANGED", "Choose a password different from your current password.")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		passwordChangeFailure(w, r, "change", err)
		return
	}
	requestHash := digest("password-change:" + c.ID + ":" + in.RequestID)
	if err = a.store.CompletePasswordChange(r.Context(), c, requestHash, a.passwordRequestFingerprint(in), string(hash), verification); err != nil {
		passwordChangeFailure(w, r, "change", err)
		return
	}
	// The database version invalidates all old browser sessions even when Redis
	// cleanup fails. Personal API tokens deliberately remain unchanged.
	pipe := a.redis.TxPipeline()
	pipe.Del(r.Context(), codeKey(u.Email, purpose), sessionKey(cookie.Value))
	pipe.ZRem(r.Context(), indexKey(u.ID), digest(cookie.Value))
	_, _ = pipe.Exec(r.Context())
	a.cookie(w, "", -1)
	diagnostics.LogPasswordChange(r.Context(), "change", "changed")
	ok(w, map[string]bool{"password_changed": true, "sign_in_required": true})
}
