package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
	"goapi-tk-register/server/internal/core"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

func passwordChangeSession(t *testing.T, a *app, u *core.User) (*http.Cookie, string) {
	t.Helper()
	token, s, err := a.newSession(context.Background(), u.ID, u.Role, 3600, 0)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: cookieName, Value: token}, s.CSRF
}

func passwordChangeCode(t *testing.T, a *app, mail *fakeMail, u *core.User, cookie *http.Cookie, csrf string) (string, string) {
	t.Helper()
	w := request(t, a, "POST", "account/password/code", map[string]any{}, cookie, csrf)
	requireStatus(t, w, 200)
	d := data(t, w)
	if d["retry_after"] != float64(60) || d["code_length"] != float64(6) || d["expires_in"].(float64) < 590 {
		t.Fatal("unexpected challenge metadata", d)
	}
	return d["challenge_id"].(string), mail.code(u.Email, "password_change")
}

func passwordChangeBody(challenge, code string) map[string]string {
	return map[string]string{"challenge_id": challenge, "code": code, "password": "replacement-password", "confirmation": "replacement-password", "request_id": "password-change-request-0001"}
}

func TestPasswordChangeRequiresOwnSessionCSRFAndStrictPayload(t *testing.T) {
	a, _, mail := fixture(t)
	u := addUser(t, a, "password-own@example.com", "member")
	cookie, csrf := passwordChangeSession(t, a, u)
	for _, route := range []string{"account/password/code", "account/password"} {
		requireStatus(t, request(t, a, "POST", route, map[string]any{}, nil, ""), 401)
		requireStatus(t, request(t, a, "POST", route, map[string]any{}, cookie, "wrong"), 403)
		requireStatus(t, request(t, a, "GET", route, nil, cookie, csrf), 405)
		requireStatus(t, request(t, a, "POST", route, map[string]string{"email": "other@example.com"}, cookie, csrf), 400)
	}
	if mail.count != 0 {
		t.Fatal("rejected request delivered a code")
	}
	for _, name := range []string{"registration_enabled", "email_code_login_enabled", "password_login_enabled"} {
		if err := a.store.DB.Model(&core.Setting{}).Where("key = ?", name).Update("value", "false").Error; err != nil {
			t.Fatal(err)
		}
	}
	challenge, code := passwordChangeCode(t, a, mail, u, cookie, csrf)
	otherCookie, otherCSRF := passwordChangeSession(t, a, u)
	body := passwordChangeBody(challenge, code)
	requireStatus(t, request(t, a, "POST", "account/password", body, otherCookie, otherCSRF), 409)
	otherUser := addUser(t, a, "password-other@example.com", "member")
	otherCookie, otherCSRF = passwordChangeSession(t, a, otherUser)
	requireStatus(t, request(t, a, "POST", "account/password", body, otherCookie, otherCSRF), 409)
	requireStatus(t, request(t, a, "POST", "account/password", body, cookie, csrf), 200)
}

func TestPasswordChangeInvalidPasswordsDoNotConsumeCode(t *testing.T) {
	a, _, mail := fixture(t)
	u := addUser(t, a, "password-validation@example.com", "member")
	cookie, csrf := passwordChangeSession(t, a, u)
	challenge, code := passwordChangeCode(t, a, mail, u, cookie, csrf)
	body := passwordChangeBody(challenge, code)
	for _, password := range []string{"short", strings.Repeat("界", 25), "correct-password"} {
		body["password"], body["confirmation"] = password, password
		requireStatus(t, request(t, a, "POST", "account/password", body, cookie, csrf), 400)
	}
	body["password"], body["confirmation"] = "replacement-password", "different-password"
	requireStatus(t, request(t, a, "POST", "account/password", body, cookie, csrf), 400)
	body["password"], body["confirmation"] = strings.Repeat("界", 8), strings.Repeat("界", 8)
	requireStatus(t, request(t, a, "POST", "account/password", body, cookie, csrf), 200)
	got, _ := a.store.GetUserByID(context.Background(), u.ID)
	if bcrypt.CompareHashAndPassword([]byte(got.PasswordHash), []byte(body["password"])) != nil {
		t.Fatal("Unicode password not persisted")
	}
}

func TestPasswordChangeDatabaseFailureRetainsProofAndRollsBackVersion(t *testing.T) {
	a, _, mail := fixture(t)
	u := addUser(t, a, "password-retry@example.com", "member")
	cookie, csrf := passwordChangeSession(t, a, u)
	challenge, code := passwordChangeCode(t, a, mail, u, cookie, csrf)
	body := passwordChangeBody(challenge, code)
	// Fail the receipt write, after the user update, to exercise a real rollback.
	var fail atomic.Bool
	fail.Store(true)
	if err := a.store.DB.Callback().Update().Before("gorm:update").Register("password-change-injected-failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "password_change_challenges" && fail.Load() {
			tx.AddError(errors.New("synthetic database failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	w := request(t, a, "POST", "account/password", body, cookie, csrf)
	requireStatus(t, w, 503)
	if strings.Contains(w.Body.String(), "synthetic") || strings.Contains(w.Body.String(), code) || strings.Contains(w.Body.String(), body["password"]) {
		t.Fatal("error leaked input or database detail")
	}
	got, _ := a.store.GetUserByID(context.Background(), u.ID)
	if got.AuthVersion != u.AuthVersion || got.PasswordHash != u.PasswordHash {
		t.Fatal("failed transaction changed password or version")
	}
	if a.redis.Exists(context.Background(), codeKey(u.Email, passwordChangePurpose(challenge))).Val() != 1 {
		t.Fatal("database failure consumed Redis proof")
	}
	fail.Store(false)
	requireStatus(t, request(t, a, "POST", "account/password", body, cookie, csrf), 200)
	got, _ = a.store.GetUserByID(context.Background(), u.ID)
	if got.AuthVersion != u.AuthVersion+1 {
		t.Fatal("retry did not change exactly once")
	}
	requireStatus(t, request(t, a, "POST", "account/password", body, cookie, csrf), 401)
}

func TestPasswordChangeResendExpiryAttemptsAndPurposeBinding(t *testing.T) {
	a, clock, mail := fixture(t)
	u := addUser(t, a, "password-codes@example.com", "member")
	cookie, csrf := passwordChangeSession(t, a, u)
	// A login code must not satisfy a password-change challenge.
	requireStatus(t, send(t, a, u.Email, "login"), 200)
	loginCode := mail.code(u.Email, "login")
	clock.FastForward(time.Minute)
	challenge, code := passwordChangeCode(t, a, mail, u, cookie, csrf)
	body := passwordChangeBody(challenge, loginCode)
	requireStatus(t, request(t, a, "POST", "account/password", body, cookie, csrf), 400)
	var before int64
	a.store.DB.Model(&core.PasswordChangeChallenge{}).Count(&before)
	requireStatus(t, request(t, a, "POST", "account/password/code", map[string]any{}, cookie, csrf), 429)
	var after int64
	a.store.DB.Model(&core.PasswordChangeChallenge{}).Count(&after)
	if before != after {
		t.Fatal("rate rejection created a challenge row")
	}
	clock.FastForward(time.Minute)
	mail.mu.Lock()
	mail.fail = true
	mail.mu.Unlock()
	requireStatus(t, request(t, a, "POST", "account/password/code", map[string]any{}, cookie, csrf), 503)
	current, _ := a.store.PasswordChange(context.Background(), digest(challenge))
	if current.Status != "sent" {
		t.Fatal("SMTP failure revoked old challenge")
	}
	mail.mu.Lock()
	mail.fail = false
	mail.mu.Unlock()
	newChallenge, newCode := passwordChangeCode(t, a, mail, u, cookie, csrf)
	body["code"] = code
	requireStatus(t, request(t, a, "POST", "account/password", body, cookie, csrf), 409)
	body = passwordChangeBody(newChallenge, "WRONG1")
	for i := 0; i < 5; i++ {
		requireStatus(t, request(t, a, "POST", "account/password", body, cookie, csrf), 400)
	}
	body["code"] = newCode
	requireStatus(t, request(t, a, "POST", "account/password", body, cookie, csrf), 400)
	clock.FastForward(time.Minute)
	expiring, expiringCode := passwordChangeCode(t, a, mail, u, cookie, csrf)
	if err := a.store.DB.Model(&core.PasswordChangeChallenge{}).Where("id = ?", digest(expiring)).Update("expires_at", time.Now().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	requireStatus(t, request(t, a, "POST", "account/password", passwordChangeBody(expiring, expiringCode), cookie, csrf), 409)
}

func TestPasswordChangeConcurrentProofOnlyChangesOnce(t *testing.T) {
	a, _, mail := fixture(t)
	u := addUser(t, a, "password-race@example.com", "member")
	cookie, csrf := passwordChangeSession(t, a, u)
	challenge, code := passwordChangeCode(t, a, mail, u, cookie, csrf)
	var successes atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			body := passwordChangeBody(challenge, code)
			body["request_id"] += strings.Repeat("x", i)
			w := request(t, a, "POST", "account/password", body, cookie, csrf)
			if w.Code == 200 {
				successes.Add(1)
			} else if w.Code != 401 && w.Code != 409 && w.Code != 503 {
				t.Errorf("unexpected race status: %d", w.Code)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("password changed %d times", successes.Load())
	}
	got, _ := a.store.GetUserByID(context.Background(), u.ID)
	if got.AuthVersion != u.AuthVersion+1 {
		t.Fatal("concurrent request incremented version twice")
	}
}

func TestPasswordChangeRevokesAllBrowserSessionsPreservesAPIToken(t *testing.T) {
	a, _, mail := fixture(t)
	u := addUser(t, a, "password-sessions@example.com", "admin")
	cookie, csrf := passwordChangeSession(t, a, u)
	other, _ := passwordChangeSession(t, a, u)
	token, err := a.store.GenerateAPIToken(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Pending administrator second-factor challenges are version-bound too.
	w := password(t, a, u.Email)
	requireStatus(t, w, 200)
	adminChallenge := data(t, w)["challenge_id"].(string)
	adminCode := mail.code(u.Email, "admin_login")
	a.redis.Del(context.Background(), cooldownKey(u.Email))
	challenge, code := passwordChangeCode(t, a, mail, u, cookie, csrf)
	a.redis.AddHook(passwordChangeCleanupFailure{})
	w = request(t, a, "POST", "account/password", passwordChangeBody(challenge, code), cookie, csrf)
	requireStatus(t, w, 200)
	if data(t, w)["sign_in_required"] != true || len(w.Result().Cookies()) != 1 || w.Result().Cookies()[0].MaxAge >= 0 {
		t.Fatal("successful change did not clear browser cookie")
	}
	for _, c := range []*http.Cookie{cookie, other} {
		requireStatus(t, request(t, a, "GET", "auth/me", nil, c, ""), 401)
	}
	requireStatus(t, request(t, a, "POST", "auth/admin/verify", map[string]string{"challenge_id": adminChallenge, "code": adminCode}, nil, ""), 401)
	if _, _, err := a.store.AuthenticateAPIToken(context.Background(), token.PlaintextToken); err != nil {
		t.Fatal("password change revoked API token", err)
	}
}

type passwordChangeCleanupFailure struct{}

func (passwordChangeCleanupFailure) BeforeProcess(ctx context.Context, _ redis.Cmder) (context.Context, error) {
	return ctx, nil
}
func (passwordChangeCleanupFailure) AfterProcess(context.Context, redis.Cmder) error { return nil }
func (passwordChangeCleanupFailure) BeforeProcessPipeline(ctx context.Context, commands []redis.Cmder) (context.Context, error) {
	for _, command := range commands {
		if command.Name() == "del" {
			return ctx, errors.New("synthetic Redis cleanup failure")
		}
	}
	return ctx, nil
}
func (passwordChangeCleanupFailure) AfterProcessPipeline(context.Context, []redis.Cmder) error {
	return nil
}

func TestPasswordChangeTransactionReceiptIsIdempotentAndUserChangesFailClosed(t *testing.T) {
	a, _, mail := fixture(t)
	u := addUser(t, a, "password-receipt@example.com", "member")
	cookie, csrf := passwordChangeSession(t, a, u)
	challenge, code := passwordChangeCode(t, a, mail, u, cookie, csrf)
	c, err := a.store.PasswordChange(context.Background(), digest(challenge))
	if err != nil {
		t.Fatal(err)
	}
	input := passwordChangeInput{ChallengeID: challenge, Code: code, Password: "replacement-password", Confirmation: "replacement-password", RequestID: "password-change-receipt-0001"}
	requestHash, fingerprint := digest("receipt-test"), a.passwordRequestFingerprint(input)
	// A change between HTTP authentication and commit must fail closed.
	for _, update := range []map[string]any{{"role": "admin"}, {"status": "blocked"}, {"email": "changed-email@example.com"}, {"auth_version": u.AuthVersion + 1}} {
		if err = a.store.DB.Model(&core.User{}).Where("id = ?", u.ID).Updates(update).Error; err != nil {
			t.Fatal(err)
		}
		if err = a.store.CompletePasswordChange(context.Background(), c, requestHash, fingerprint, "unused-hash", "unused-verification"); !errors.Is(err, core.ErrPasswordChangeConflict) {
			t.Fatal("changed user accepted", err)
		}
		if err = a.store.DB.Model(&core.User{}).Where("id = ?", u.ID).Updates(map[string]any{"role": u.Role, "status": u.Status, "email": u.Email, "auth_version": u.AuthVersion}).Error; err != nil {
			t.Fatal(err)
		}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.store.CompletePasswordChange(context.Background(), c, requestHash, fingerprint, string(hash), "unused-verification"); err != nil {
		t.Fatal(err)
	}
	if err = a.store.CompletePasswordChange(context.Background(), c, requestHash, fingerprint, "different-hash", "unused-verification"); err != nil {
		t.Fatal("exact receipt retry failed", err)
	}
	if err = a.store.CompletePasswordChange(context.Background(), c, requestHash, digest("different-operation"), "unused-hash", "unused-verification"); !errors.Is(err, core.ErrPasswordChangeConflict) {
		t.Fatal("receipt accepted changed operation", err)
	}
	got, _ := a.store.GetUserByID(context.Background(), u.ID)
	if got.AuthVersion != u.AuthVersion+1 || got.PasswordHash != string(hash) {
		t.Fatal("receipt replay performed a second update")
	}
}

func TestPasswordChangeUsesSharedEmailAndIPSendingLimits(t *testing.T) {
	a, _, mail := fixture(t)
	u := addUser(t, a, "password-limits@example.com", "member")
	cookie, csrf := passwordChangeSession(t, a, u)
	if err := a.store.DB.Model(&core.Setting{}).Where("key IN ?", []string{"email_send_limit_per_hour", "ip_send_limit_per_hour"}).Update("value", "1").Error; err != nil {
		t.Fatal(err)
	}
	passwordChangeCode(t, a, mail, u, cookie, csrf)
	a.redis.Del(context.Background(), cooldownKey(u.Email))
	requireStatus(t, request(t, a, "POST", "account/password/code", map[string]any{}, cookie, csrf), 429)
	other := addUser(t, a, "password-ip-limit@example.com", "member")
	otherCookie, otherCSRF := passwordChangeSession(t, a, other)
	requireStatus(t, request(t, a, "POST", "account/password/code", map[string]any{}, otherCookie, otherCSRF), 429)
	var count int64
	a.store.DB.Model(&core.PasswordChangeChallenge{}).Count(&count)
	if count != 1 || mail.count != 1 {
		t.Fatal("sending limits allowed extra challenge or message")
	}
}
