package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"goapi-tk-register/server/common"
	"goapi-tk-register/server/internal/core"
	"golang.org/x/crypto/bcrypt"
)

type fakeMail struct {
	mu    sync.Mutex
	codes map[string]string
	count int
	fail  bool
}

func (f *fakeMail) SendCode(_ context.Context, email, code, purpose string, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("mock SMTP failed")
	}
	f.codes[email+":"+purpose] = code
	f.count++
	return nil
}
func (f *fakeMail) code(email, purpose string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.codes[email+":"+purpose]
}
func fixture(t *testing.T) (*app, *miniredis.Miniredis, *fakeMail) {
	t.Helper()
	s, e := core.Open("sqlite", filepath.Join(t.TempDir(), "tk.sqlite"), strings.Repeat("k", 32))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	m := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: m.Addr()})
	t.Cleanup(func() { r.Close() })
	mail := &fakeMail{codes: map[string]string{}}
	return &app{store: s, redis: r, mail: mail, secret: strings.Repeat("k", 32), adminEmail: "admin@example.com", origin: "https://goapi.example", secure: true, assets: t.TempDir()}, m, mail
}
func request(t *testing.T, a *app, method, route string, body any, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		var e error
		raw, e = common.Marshal(body)
		if e != nil {
			t.Fatal(e)
		}
	}
	r := httptest.NewRequest(method, basePath+"/api/"+route, strings.NewReader(string(raw)))
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("Origin", a.origin)
	r.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}
func data(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var e struct {
		Data map[string]any `json:"data"`
	}
	if err := common.Unmarshal(w.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	return e.Data
}
func requireStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status %d want %d: %s", w.Code, want, w.Body.String())
	}
}
func addUser(t *testing.T, a *app, email, role string) *core.User {
	t.Helper()
	hash, e := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.MinCost)
	if e != nil {
		t.Fatal(e)
	}
	u := &core.User{Email: email, Role: role, Status: "active", PasswordHash: string(hash), EmailVerifiedAt: time.Now().UTC()}
	if e = a.store.CreateUser(context.Background(), u); e != nil {
		t.Fatal(e)
	}
	return u
}
func send(t *testing.T, a *app, email, purpose string) *httptest.ResponseRecorder {
	t.Helper()
	return request(t, a, "POST", "auth/code", map[string]string{"email": email, "purpose": purpose}, nil, "")
}
func password(t *testing.T, a *app, email string) *httptest.ResponseRecorder {
	t.Helper()
	return request(t, a, "POST", "auth/login", map[string]string{"email": email, "method": "password", "password": "correct-password"}, nil, "")
}

func TestRegistrationEmailIdentityAndSingleUse(t *testing.T) {
	a, _, mail := fixture(t)
	email := "person@example.com"
	requireStatus(t, send(t, a, email, "register"), 200)
	requireStatus(t, send(t, a, email, "register"), 429)
	body := map[string]string{"email": email, "username": "different@example.com", "password": "correct-password", "code": mail.code(email, "register")}
	requireStatus(t, request(t, a, "POST", "auth/register", body, nil, ""), 400)
	body["username"] = email
	w := request(t, a, "POST", "auth/register", body, nil, "")
	requireStatus(t, w, 200)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].Path != basePath || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatal("insecure session cookie", cookies)
	}
	u, e := a.store.GetUserByEmail(context.Background(), email)
	if e != nil || u.Email != email || u.Role != "member" || u.EmailVerifiedAt.IsZero() || u.PasswordHash == body["password"] {
		t.Fatal("registration not persisted safely", e)
	}
	requireStatus(t, request(t, a, "POST", "auth/register", body, nil, ""), 400)
	v, e := a.store.Settings(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	valid, e := a.checkCode(context.Background(), email, "register", body["code"], v.Int("verification_max_attempts"), "")
	if e != nil || valid {
		t.Fatal("code was reusable")
	}
	for _, key := range a.redis.Keys(context.Background(), "*").Val() {
		if !strings.HasPrefix(key, redisPrefix) {
			t.Fatal("unprefixed redis key", key)
		}
	}
}
func TestAdminRequiresPasswordAndCodeAndChallengeIsSingleUse(t *testing.T) {
	a, _, mail := fixture(t)
	u := addUser(t, a, "admin@example.com", "admin")
	requireStatus(t, send(t, a, u.Email, "login"), 200)
	if mail.count != 0 {
		t.Fatal("administrator email-only login sent mail")
	}
	w := password(t, a, u.Email)
	requireStatus(t, w, 200)
	d := data(t, w)
	if d["challenge_required"] != true || len(w.Result().Cookies()) != 0 {
		t.Fatal("session established before second factor")
	}
	id := d["challenge_id"].(string)
	code := mail.code(u.Email, "admin_login")
	requireStatus(t, request(t, a, "POST", "auth/login", map[string]string{"email": u.Email, "method": "code", "code": code}, nil, ""), 401)
	verify := map[string]string{"challenge_id": id, "code": code}
	w = request(t, a, "POST", "auth/admin/verify", verify, nil, "")
	requireStatus(t, w, 200)
	if len(w.Result().Cookies()) != 1 {
		t.Fatal("verified admin lacks session")
	}
	requireStatus(t, request(t, a, "POST", "auth/admin/verify", verify, nil, ""), 401)
}
func TestBootstrapAdminRequiresVerifiedEmail(t *testing.T) {
	a, _, mail := fixture(t)
	body := map[string]string{"email": a.adminEmail, "username": a.adminEmail, "password": "correct-password", "code": "ABC123"}
	requireStatus(t, request(t, a, "POST", "auth/register", body, nil, ""), 400)
	requireStatus(t, send(t, a, a.adminEmail, "register"), 200)
	body["code"] = mail.code(a.adminEmail, "register")
	w := request(t, a, "POST", "auth/register", body, nil, "")
	requireStatus(t, w, 200)
	u, e := a.store.GetUserByEmail(context.Background(), a.adminEmail)
	if e != nil || u.Role != "admin" {
		t.Fatal("bootstrap admin missing", e)
	}
}
func TestExpiredAndExhaustedCodes(t *testing.T) {
	a, m, mail := fixture(t)
	email := "expiry@example.com"
	requireStatus(t, send(t, a, email, "register"), 200)
	code := mail.code(email, "register")
	m.FastForward(601 * time.Second)
	valid, e := a.checkCode(context.Background(), email, "register", code, 5, "")
	if e != nil || valid {
		t.Fatal("expired code accepted")
	}
	requireStatus(t, send(t, a, email, "register"), 200)
	code = mail.code(email, "register")
	for i := 0; i < 5; i++ {
		valid, e = a.checkCode(context.Background(), email, "register", "wrong", 5, "")
		if e != nil || valid {
			t.Fatal("wrong code accepted")
		}
	}
	valid, e = a.checkCode(context.Background(), email, "register", code, 5, "")
	if e != nil || valid {
		t.Fatal("exhausted code accepted")
	}
}
func TestConcurrentConsumptionOnlyOneWinner(t *testing.T) {
	a, _, mail := fixture(t)
	email := "race@example.com"
	requireStatus(t, send(t, a, email, "register"), 200)
	code := mail.code(email, "register")
	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			valid, e := a.checkCode(context.Background(), email, "register", code, 5, "")
			if e != nil {
				t.Error(e)
			}
			if valid {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("%d code consumptions", winners.Load())
	}
}
func TestSMTPFailureReleasesReservationAndPreservesPreviousCode(t *testing.T) {
	a, m, mail := fixture(t)
	email := "smtp@example.com"
	mail.fail = true
	requireStatus(t, send(t, a, email, "register"), 503)
	if a.redis.Exists(context.Background(), cooldownKey(email)).Val() != 0 {
		t.Fatal("stuck send reservation")
	}
	mail.fail = false
	requireStatus(t, send(t, a, email, "register"), 200)
	old := mail.code(email, "register")
	m.FastForward(61 * time.Second)
	mail.fail = true
	requireStatus(t, send(t, a, email, "register"), 503)
	valid, e := a.checkCode(context.Background(), email, "register", old, 5, "")
	if e != nil || !valid {
		t.Fatal("failed resend invalidated previous code", e)
	}
}
func TestCSRFAndSettingsAuthorizationAndLogout(t *testing.T) {
	a, _, _ := fixture(t)
	member := addUser(t, a, "member@example.com", "member")
	w := password(t, a, member.Email)
	requireStatus(t, w, 200)
	cookie := w.Result().Cookies()[0]
	csrf := data(t, w)["csrf_token"].(string)
	requireStatus(t, request(t, a, "GET", "admin/settings", nil, cookie, ""), 403)
	requireStatus(t, request(t, a, "POST", "auth/logout", map[string]string{}, cookie, ""), 403)
	requireStatus(t, request(t, a, "POST", "auth/logout", map[string]string{}, cookie, csrf), 200)
	requireStatus(t, request(t, a, "GET", "auth/me", nil, cookie, ""), 401)
	r := httptest.NewRequest("POST", basePath+"/api/auth/code", strings.NewReader(`{"email":"x@example.com","purpose":"register"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://evil.example")
	res := httptest.NewRecorder()
	a.Handler().ServeHTTP(res, r)
	requireStatus(t, res, 403)
	admin := addUser(t, a, "settings@example.com", "admin")
	token, s, e := a.newSession(context.Background(), admin.ID, admin.Role, 28800, 1800)
	if e != nil {
		t.Fatal(e)
	}
	adminCookie := &http.Cookie{Name: cookieName, Value: token}
	requireStatus(t, request(t, a, "PUT", "admin/settings", map[string]any{"settings": map[string]string{"smtp_password": "sensitive-secret", "email_send_cooldown_seconds": "120"}}, adminCookie, s.CSRF), 200)
	w = request(t, a, "GET", "admin/settings", nil, adminCookie, "")
	requireStatus(t, w, 200)
	if strings.Contains(w.Body.String(), "sensitive-secret") {
		t.Fatal("SMTP secret returned to browser")
	}
	v, e := a.store.Settings(context.Background())
	if e != nil || v.Int("email_send_cooldown_seconds") != 120 {
		t.Fatal("settings did not persist", e)
	}
	requireStatus(t, request(t, a, "PUT", "admin/settings", map[string]any{"settings": map[string]string{"admin_require_email": "false"}}, adminCookie, s.CSRF), 400)
}
func TestSessionIdleAbsoluteExpiryAndDeletedUser(t *testing.T) {
	a, m, _ := fixture(t)
	u := addUser(t, a, "sessions@example.com", "admin")
	token, _, e := a.newSession(context.Background(), u.ID, u.Role, 100, 20)
	if e != nil {
		t.Fatal(e)
	}
	if a.redis.ZCard(context.Background(), indexKey(u.ID)).Val() != 1 {
		t.Fatal("session index missing")
	}
	m.FastForward(21 * time.Second)
	if _, e = a.getSession(context.Background(), token); e == nil {
		t.Fatal("idle-expired session accepted")
	}
	u.Role = "member"
	if e = a.store.DB.Model(u).Update("role", u.Role).Error; e != nil {
		t.Fatal(e)
	}
	token, _, e = a.newSession(context.Background(), u.ID, u.Role, 30, 0)
	if e != nil {
		t.Fatal(e)
	}
	m.FastForward(31 * time.Second)
	if _, e = a.getSession(context.Background(), token); e == nil {
		t.Fatal("absolute-expired session accepted")
	}
	u2 := addUser(t, a, "deleted@example.com", "member")
	w := password(t, a, u2.Email)
	requireStatus(t, w, 200)
	cookie := w.Result().Cookies()[0]
	if e = a.store.DB.Delete(&core.User{}, u2.ID).Error; e != nil {
		t.Fatal(e)
	}
	requireStatus(t, request(t, a, "GET", "auth/me", nil, cookie, ""), 401)
}
func TestPasswordRateLimitsAndDisabledMethods(t *testing.T) {
	a, _, _ := fixture(t)
	u := addUser(t, a, "limited@example.com", "member")
	bad := map[string]string{"email": u.Email, "method": "password", "password": "wrong-password"}
	for i := 0; i < 5; i++ {
		requireStatus(t, request(t, a, "POST", "auth/login", bad, nil, ""), 401)
	}
	requireStatus(t, password(t, a, u.Email), 429)
	if e := a.store.PatchSettings(context.Background(), map[string]string{"registration_enabled": "false", "email_code_login_enabled": "false"}, 1); e != nil {
		t.Fatal(e)
	}
	requireStatus(t, send(t, a, "new@example.com", "register"), 403)
	requireStatus(t, send(t, a, u.Email, "login"), 403)
}
func TestDatabaseAndOriginGuards(t *testing.T) {
	for _, dsn := range []string{"user:secret@tcp(localhost:3306)/go_api", "user:secret@tcp(localhost:3306)/new_api", ""} {
		if validateDatabase("mysql", dsn) == nil {
			t.Fatal("unsafe TK database allowed")
		}
	}
	if e := validateDatabase("mysql", "user:secret@tcp(localhost:3306)/tk_register"); e != nil {
		t.Fatal(e)
	}
	if _, _, e := validateOrigin("http://goapi.cloud"); e == nil {
		t.Fatal("insecure public origin allowed")
	}
	if _, secure, e := validateOrigin("http://localhost:3002"); e != nil || secure {
		t.Fatal("local development origin rejected")
	}
}

func TestRedisUnavailableFailsClosed(t *testing.T) {
	a, m, mail := fixture(t)
	m.Close()
	requireStatus(t, send(t, a, "outage@example.com", "register"), 503)
	if mail.count != 0 {
		t.Fatal("email sent while rate limits were unavailable")
	}
	body := map[string]string{"email": "outage@example.com", "password": "correct-password", "code": "ABC123"}
	requireStatus(t, request(t, a, "POST", "auth/register", body, nil, ""), 503)
	var count int64
	if e := a.store.DB.Model(&core.User{}).Count(&count).Error; e != nil || count != 0 {
		t.Fatal("registration bypassed Redis", e)
	}
}

func TestIneligibleCodeRequestCannotBlockAdministratorSignIn(t *testing.T) {
	a, mini, mail := fixture(t)
	user := addUser(t, a, "protected-admin@example.com", "admin")
	requireStatus(t, send(t, a, user.Email, "login"), 200)
	if a.redis.Exists(context.Background(), cooldownKey(user.Email)).Val() != 0 {
		t.Fatal("ineligible request reserved actual email delivery")
	}
	w := password(t, a, user.Email)
	requireStatus(t, w, 200)
	challengeID := data(t, w)["challenge_id"].(string)
	code := mail.code(user.Email, "admin_login")
	if mail.count != 1 {
		t.Fatal("expected only administrator verification email")
	}
	mini.FastForward(61 * time.Second)
	requireStatus(t, send(t, a, user.Email, "login"), 200)
	if mail.count != 1 {
		t.Fatal("ineligible request delivered mail")
	}
	requireStatus(t, request(t, a, "POST", "auth/admin/verify", map[string]string{"challenge_id": challengeID, "code": code}, nil, ""), 200)
}
