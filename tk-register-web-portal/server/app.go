package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-redis/redis/v8"
	"goapi-tk-register/server/common"
	"goapi-tk-register/server/internal/core"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const basePath = "/tk-register"
const cookieName = "tk_register_session"

type mailer interface {
	SendCode(context.Context, string, string, string, int) error
}
type app struct {
	store                              *core.Store
	redis                              *redis.Client
	mail                               mailer
	secret, adminEmail, origin, assets string
	secure                             bool
	vendorTransport                    http.RoundTripper
	accountCollector                   accountCollector
}
type envelope struct {
	Success    bool   `json:"success"`
	Data       any    `json:"data,omitempty"`
	Message    string `json:"message,omitempty"`
	Code       string `json:"code,omitempty"`
	RetryAfter int    `json:"retry_after,omitempty"`
}

func respond(w http.ResponseWriter, status int, v envelope) {
	b, err := common.Marshal(v)
	if err != nil {
		http.Error(w, "Response unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}
func ok(w http.ResponseWriter, data any) { respond(w, 200, envelope{Success: true, Data: data}) }
func fail(w http.ResponseWriter, status int, code, message string) {
	respond(w, status, envelope{Code: code, Message: message})
}
func rateFailure(w http.ResponseWriter, seconds int) {
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	respond(w, 429, envelope{Code: "RATE_LIMITED", Message: "Please wait before trying again.", RetryAfter: seconds})
}
func internal(w http.ResponseWriter) {
	fail(w, 503, "UNAVAILABLE", "This service is temporarily unavailable. Please try again.")
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		fail(w, 415, "INVALID_CONTENT_TYPE", "Use application/json.")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	if err := common.DecodeJson(r.Body, v); err != nil {
		fail(w, 400, "INVALID_REQUEST", "Please check the request fields.")
		return false
	}
	return true
}
func clientIP(r *http.Request) string {
	if ip := net.ParseIP(r.Header.Get("X-TK-Client-IP")); ip != nil {
		return ip.String()
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}
func (a *app) Handler() http.Handler { return http.HandlerFunc(a.serve) }
func (a *app) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; font-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	requestID, err := randomToken()
	if err != nil {
		internal(w)
		return
	}
	w.Header().Set("X-Request-ID", requestID)
	if r.URL.Path == basePath+"/healthz" {
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if a.store.Ping(ctx) != nil || a.redis.Ping(ctx).Err() != nil {
			internal(w)
			return
		}
		ok(w, map[string]bool{"ready": true})
		return
	}
	if !strings.HasPrefix(r.URL.Path, basePath+"/api/") {
		a.static(w, r)
		return
	}
	if r.URL.Path == claudeAccountsAPIPath {
		a.claudeAccountsAPI(w, r)
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		if r.Header.Get("Origin") != a.origin || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			fail(w, 403, "ORIGIN_REJECTED", "This request must come from TK Register.")
			return
		}
	}
	route := strings.TrimPrefix(r.URL.Path, basePath+"/api/")
	if a.accountSyncRoutes(w, r, route) {
		return
	}
	if a.apiTokenRoutes(w, r, route) {
		return
	}
	if a.adminRoutes(w, r, route) {
		return
	}
	if a.vendorRoutes(w, r, route) {
		return
	}
	switch r.Method + " " + route {
	case "GET auth/status":
		a.status(w, r)
	case "GET auth/me":
		a.me(w, r)
	case "POST auth/code":
		a.sendCode(w, r)
	case "POST auth/register":
		a.register(w, r)
	case "POST auth/login":
		a.login(w, r)
	case "POST auth/admin/verify":
		a.adminVerify(w, r)
	case "POST auth/admin/resend":
		a.adminResend(w, r)
	case "POST auth/logout":
		a.logout(w, r)
	case "GET admin/settings", "PUT admin/settings":
		a.settings(w, r)
	default:
		fail(w, 404, "NOT_FOUND", "This endpoint does not exist.")
	}
}
func (a *app) static(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != basePath && !strings.HasPrefix(r.URL.Path, basePath+"/") {
		http.NotFound(w, r)
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		w.WriteHeader(405)
		return
	}
	suffix := strings.TrimPrefix(r.URL.Path, basePath)
	clean := path.Clean("/" + suffix)
	for _, part := range strings.Split(suffix, "/") {
		if strings.HasPrefix(part, ".") {
			http.NotFound(w, r)
			return
		}
	}
	filename := filepath.Join(a.assets, filepath.FromSlash(strings.TrimPrefix(clean, "/")))
	if info, err := os.Stat(filename); err == nil && !info.IsDir() {
		if strings.HasPrefix(clean, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		http.ServeFile(w, r, filename)
		return
	}
	if strings.HasPrefix(clean, "/assets/") || path.Ext(clean) != "" {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(a.assets, "index.html"))
}
func (a *app) config(w http.ResponseWriter, r *http.Request) (core.Values, bool) {
	v, e := a.store.Settings(r.Context())
	if e != nil {
		internal(w)
		return nil, false
	}
	return v, true
}
func (a *app) status(w http.ResponseWriter, r *http.Request) {
	v, good := a.config(w, r)
	if !good {
		return
	}
	ok(w, map[string]any{"registration_enabled": v.Bool("registration_enabled"), "password_login_enabled": v.Bool("password_login_enabled"), "email_code_login_enabled": v.Bool("email_code_login_enabled"), "code_length": 6, "email_max_length": 50, "password_min_length": 8, "password_max_length": 72, "email_send_cooldown_seconds": v.Int("email_send_cooldown_seconds"), "registration_code_ttl_seconds": v.Int("registration_code_ttl_seconds"), "login_code_ttl_seconds": v.Int("login_code_ttl_seconds")})
}
func (a *app) cookie(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: basePath, Secure: a.secure, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
}
func (a *app) authenticated(w http.ResponseWriter, r *http.Request, csrf bool) (*core.User, *session, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		fail(w, 401, "UNAUTHENTICATED", "Please sign in.")
		return nil, nil, false
	}
	s, err := a.getSession(r.Context(), c.Value)
	if err != nil {
		if !errors.Is(err, errSessionExpired) {
			internal(w)
			return nil, nil, false
		}
		a.cookie(w, "", -1)
		fail(w, 401, "UNAUTHENTICATED", "Your session has expired. Please sign in again.")
		return nil, nil, false
	}
	user, err := a.store.GetUserByID(r.Context(), s.UserID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		internal(w)
		return nil, nil, false
	}
	if err != nil || user.Status != "active" || user.Role != s.Role || user.AuthVersion != s.AuthVersion {
		a.redis.Del(r.Context(), sessionKey(c.Value))
		a.cookie(w, "", -1)
		fail(w, 401, "UNAUTHENTICATED", "Please sign in again.")
		return nil, nil, false
	}
	if csrf && subtle.ConstantTimeCompare([]byte(s.CSRF), []byte(r.Header.Get("X-CSRF-Token"))) != 1 {
		fail(w, 403, "CSRF_REJECTED", "Refresh the page before trying again.")
		return nil, nil, false
	}
	return user, s, true
}
func authData(user *core.User, s *session) map[string]any {
	return map[string]any{"user": user, "csrf_token": s.CSRF}
}
func (a *app) me(w http.ResponseWriter, r *http.Request) {
	user, s, good := a.authenticated(w, r, false)
	if good {
		ok(w, authData(user, s))
	}
}
func (a *app) establish(w http.ResponseWriter, r *http.Request, user *core.User, v core.Values) {
	absolute, idle := v.Int("user_session_absolute_ttl_seconds"), 0
	if user.Role == "admin" {
		absolute = v.Int("admin_session_absolute_ttl_seconds")
		idle = v.Int("admin_session_idle_ttl_seconds")
	}
	token, s, err := a.newSession(r.Context(), user.ID, user.Role, absolute, idle, user.AuthVersion)
	if errors.Is(err, errSessionExpired) {
		fail(w, 401, "INVALID_CREDENTIALS", "Your account permissions changed. Please sign in again.")
		return
	}
	if err != nil {
		internal(w)
		return
	}
	// Rotate any old token at every successful authentication.
	if old, e := r.Cookie(cookieName); e == nil {
		if previous, e := a.getSession(r.Context(), old.Value); e == nil {
			pipe := a.redis.TxPipeline()
			pipe.Del(r.Context(), sessionKey(old.Value))
			pipe.ZRem(r.Context(), indexKey(previous.UserID), digest(old.Value))
			_, _ = pipe.Exec(r.Context())
		}
	}
	a.cookie(w, token, absolute)
	ok(w, authData(user, s))
}
func (a *app) logout(w http.ResponseWriter, r *http.Request) {
	_, s, good := a.authenticated(w, r, true)
	if !good {
		return
	}
	c, _ := r.Cookie(cookieName)
	pipe := a.redis.TxPipeline()
	pipe.Del(r.Context(), sessionKey(c.Value))
	pipe.ZRem(r.Context(), indexKey(s.UserID), digest(c.Value))
	if _, err := pipe.Exec(r.Context()); err != nil {
		internal(w)
		return
	}
	a.cookie(w, "", -1)
	ok(w, map[string]bool{"signed_out": true})
}
func (a *app) settings(w http.ResponseWriter, r *http.Request) {
	u, _, good := a.authenticated(w, r, r.Method == "PUT")
	if !good {
		return
	}
	if u.Role != "admin" {
		fail(w, 403, "FORBIDDEN", "Administrator access is required.")
		return
	}
	if r.Method == "PUT" {
		var in struct {
			Settings map[string]string `json:"settings"`
		}
		if !decode(w, r, &in) {
			return
		}
		if e := a.store.PatchSettings(r.Context(), in.Settings, u.ID); e != nil {
			if core.IsValidationError(e) {
				fail(w, 400, "INVALID_SETTINGS", e.Error())
			} else {
				internal(w)
			}
			return
		}
	}
	rows, e := a.store.Descriptors(r.Context())
	if e != nil {
		internal(w)
		return
	}
	ok(w, map[string]any{"settings": rows})
}
func validEmail(email string) bool {
	if !core.ValidEmail(email) {
		return false
	}
	for _, c := range email {
		if c > 127 {
			return false
		}
	}
	return true
}

func (a *app) checkCode(ctx context.Context, email, purpose, code string, maxAttempts int, challenge string) (bool, error) {
	// Invalid-length values count as attempts too.
	keys := []string{codeKey(email, purpose)}
	if challenge != "" {
		keys = append(keys, challengeKey(challenge))
	}
	request, err := consumeCode.Run(ctx, a.redis, keys, a.codeHash(email, purpose, code), maxAttempts).Text()
	if err != nil {
		return false, err
	}
	if request == "" {
		return false, nil
	}
	_ = a.store.UpdateVerification(ctx, request, "consumed")
	return true, nil
}

type deliveryError struct {
	retry int
	err   error
}

func (e *deliveryError) Error() string { return "delivery unavailable" }
func (a *app) deliver(ctx context.Context, email, purpose, ip string, ttl int, v core.Values, send bool) error {
	request, err := randomToken()
	if err != nil {
		return err
	}
	cooldown := v.Int("email_send_cooldown_seconds")
	lease := cooldown
	if lease < v.Int("smtp_timeout_seconds")+10 {
		lease = v.Int("smtp_timeout_seconds") + 10
	}
	emailCooldown := cooldownKey(email)
	emailRate := redisPrefix + "send-email:" + digest(email)
	if !send {
		// Apply the same generic response and throttling without allowing an
		// unauthenticated, ineligible request to block a real code delivery.
		emailCooldown = redisPrefix + "ineligible-cooldown:" + digest(email)
		emailRate = redisPrefix + "ineligible-send-email:" + digest(email)
	}
	retry, err := reserveSend.Run(ctx, a.redis, []string{emailCooldown, emailRate, redisPrefix + "send-ip:" + digest(ip)}, request, lease, v.Int("email_send_limit_per_hour"), v.Int("ip_send_limit_per_hour")).Int()
	if err != nil {
		return err
	}
	if retry > 0 {
		return &deliveryError{retry: retry}
	}
	if !send {
		a.redis.Expire(ctx, emailCooldown, time.Duration(cooldown)*time.Second)
		return nil
	}
	released := false
	defer func() {
		if !released {
			cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, _ = releaseSend.Run(cleanup, a.redis, []string{cooldownKey(email)}, request).Result()
		}
	}()
	code, err := randomCode()
	if err != nil {
		return err
	}
	record := &core.EmailVerification{RequestID: request, Email: email, Purpose: strings.Split(purpose, ":")[0], Status: "sending", CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Duration(ttl) * time.Second)}
	if err = a.store.RecordVerification(ctx, record); err != nil {
		return err
	}
	if err = a.mail.SendCode(ctx, email, code, strings.Split(purpose, ":")[0], ttl); err != nil {
		_ = a.store.UpdateVerification(ctx, request, "failed")
		return err
	}
	saved, err := saveCode.Run(ctx, a.redis, []string{cooldownKey(email), codeKey(email, purpose)}, request, a.codeHash(email, purpose, code), ttl, cooldown).Int()
	if err != nil || saved != 1 {
		_ = a.store.UpdateVerification(ctx, request, "failed")
		return errors.New("code storage unavailable")
	}
	released = true
	_ = a.store.UpdateVerification(ctx, request, "sent")
	return nil
}
func deliveryFailure(w http.ResponseWriter, err error) {
	var d *deliveryError
	if errors.As(err, &d) && d.retry > 0 {
		rateFailure(w, d.retry)
		return
	}
	fail(w, 503, "EMAIL_UNAVAILABLE", "We could not send the code. Please try again shortly.")
}
func (a *app) sendCode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email   string `json:"email"`
		Purpose string `json:"purpose"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Email = core.NormalizeEmail(in.Email)
	if !validEmail(in.Email) || (in.Purpose != "register" && in.Purpose != "login") {
		fail(w, 400, "INVALID_REQUEST", "Enter a valid email address of up to 50 characters.")
		return
	}
	v, good := a.config(w, r)
	if !good {
		return
	}
	if (in.Purpose == "register" && !v.Bool("registration_enabled")) || (in.Purpose == "login" && !v.Bool("email_code_login_enabled")) {
		fail(w, 403, "METHOD_DISABLED", "This sign-in method is currently unavailable.")
		return
	}
	u, err := a.store.GetUserByEmail(r.Context(), in.Email)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		internal(w)
		return
	}
	send := in.Purpose == "register" && errors.Is(err, gorm.ErrRecordNotFound) || in.Purpose == "login" && err == nil && u.Status == "active" && u.Role == "member"
	ttl := v.Int("registration_code_ttl_seconds")
	if in.Purpose == "login" {
		ttl = v.Int("login_code_ttl_seconds")
	}
	if err = a.deliver(r.Context(), in.Email, in.Purpose, clientIP(r), ttl, v, send); err != nil {
		deliveryFailure(w, err)
		return
	}
	ok(w, map[string]any{"retry_after": v.Int("email_send_cooldown_seconds"), "expires_in": ttl, "message": "If this email is eligible, a verification code has been sent."})
}
func (a *app) register(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Username string `json:"username"`
		Code     string `json:"code"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Email = core.NormalizeEmail(in.Email)
	if !validEmail(in.Email) || (in.Username != "" && core.NormalizeEmail(in.Username) != in.Email) || utf8.RuneCountInString(in.Password) < 8 || len(in.Password) > 72 {
		fail(w, 400, "INVALID_REQUEST", "Use a valid email (up to 50 characters) and a password of at least 8 characters, up to 72 UTF-8 bytes. The username must match the email.")
		return
	}
	v, good := a.config(w, r)
	if !good {
		return
	}
	if !v.Bool("registration_enabled") {
		fail(w, 403, "REGISTRATION_DISABLED", "Registration is currently unavailable.")
		return
	}
	existing, err := a.store.GetUserByEmail(r.Context(), in.Email)
	if err == nil && existing != nil {
		fail(w, 400, "REGISTRATION_FAILED", "This registration could not be completed. Check the email and code or sign in.")
		return
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		internal(w)
		return
	}
	valid, err := a.checkCode(r.Context(), in.Email, "register", in.Code, v.Int("verification_max_attempts"), "")
	if err != nil {
		internal(w)
		return
	}
	if !valid {
		fail(w, 400, "INVALID_CODE", "The verification code is invalid or expired.")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		internal(w)
		return
	}
	role := "member"
	if in.Email == a.adminEmail {
		role = "admin"
	}
	user := &core.User{Email: in.Email, PasswordHash: string(hash), Role: role, Status: "active", EmailVerifiedAt: time.Now().UTC()}
	if err = a.store.CreateUser(r.Context(), user); err != nil {
		fail(w, 409, "REGISTRATION_FAILED", "This registration could not be completed. Try signing in or request a new code.")
		return
	}
	a.establish(w, r, user, v)
}
func (a *app) passwordRate(w http.ResponseWriter, r *http.Request, email string, v core.Values) ([]string, bool) {
	keys := []string{redisPrefix + "login-email:" + digest(email), redisPrefix + "login-ip:" + digest(clientIP(r))}
	n, e := loginAttempt.Run(r.Context(), a.redis, keys, v.Int("login_failure_max_attempts"), v.Int("login_failure_window_seconds")).Int()
	if e != nil {
		internal(w)
		return nil, false
	}
	if n > 0 {
		rateFailure(w, n)
		return nil, false
	}
	return keys, true
}

// A fixed-cost hash makes unknown-user password checks follow the same work path.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("unused-timing-comparison-password"), bcrypt.DefaultCost)

func (a *app) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Method   string `json:"method"`
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Email = core.NormalizeEmail(in.Email)
	if !validEmail(in.Email) || (in.Method != "password" && in.Method != "code") || len(in.Password) > 72 {
		fail(w, 400, "INVALID_REQUEST", "Check your email and sign-in details.")
		return
	}
	v, good := a.config(w, r)
	if !good {
		return
	}
	keys, good := a.passwordRate(w, r, in.Email, v)
	if !good {
		return
	}
	user, err := a.store.GetUserByEmail(r.Context(), in.Email)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		internal(w)
		return
	}
	valid := false
	if in.Method == "password" {
		hash := dummyHash
		if err == nil {
			hash = []byte(user.PasswordHash)
		}
		passwordOK := bcrypt.CompareHashAndPassword(hash, []byte(in.Password)) == nil
		valid = err == nil && passwordOK && (user.Role == "admin" || v.Bool("password_login_enabled"))
	} else if v.Bool("email_code_login_enabled") && err == nil && user.Role == "member" {
		valid, err = a.checkCode(r.Context(), in.Email, "login", in.Code, v.Int("verification_max_attempts"), "")
		if err != nil {
			internal(w)
			return
		}
	}
	if !valid || user == nil || user.Status != "active" {
		fail(w, 401, "INVALID_CREDENTIALS", "Your sign-in details are invalid, or this method is unavailable. Administrators must use a password.")
		return
	}
	// Successful first factor must not clear an administrator's failure limit yet.
	if user.Role == "admin" {
		a.beginAdmin(w, r, user, v)
		return
	}
	a.redis.Del(r.Context(), keys...)
	a.establish(w, r, user, v)
}

type challenge struct {
	AuthVersion uint64 `json:"auth_version"`
	UserID      uint64 `json:"user_id"`
	Email       string `json:"email"`
	Expires     int64  `json:"expires"`
}

func (a *app) beginAdmin(w http.ResponseWriter, r *http.Request, user *core.User, v core.Values) {
	id, err := randomToken()
	if err != nil {
		internal(w)
		return
	}
	ttl := v.Int("admin_login_challenge_ttl_seconds")
	c := challenge{UserID: user.ID, Email: user.Email, Expires: time.Now().Unix() + int64(ttl), AuthVersion: user.AuthVersion}
	b, err := common.Marshal(c)
	if err != nil {
		internal(w)
		return
	}
	if err = a.redis.Set(r.Context(), challengeKey(id), b, time.Duration(ttl)*time.Second).Err(); err != nil {
		internal(w)
		return
	}
	codeTTL := v.Int("admin_login_code_ttl_seconds")
	if codeTTL > ttl {
		codeTTL = ttl
	}
	if err = a.deliver(r.Context(), user.Email, "admin_login:"+digest(id), clientIP(r), codeTTL, v, true); err != nil {
		a.redis.Del(r.Context(), challengeKey(id))
		deliveryFailure(w, err)
		return
	}
	ok(w, map[string]any{"challenge_required": true, "challenge_id": id, "email": user.Email, "expires_in": ttl, "code_expires_in": codeTTL, "retry_after": v.Int("email_send_cooldown_seconds")})
}
func (a *app) readChallenge(ctx context.Context, id string) (*challenge, error) {
	if len(id) != 43 {
		return nil, errors.New("invalid challenge")
	}
	raw, e := a.redis.Get(ctx, challengeKey(id)).Bytes()
	if e != nil {
		return nil, e
	}
	var c challenge
	if e = common.Unmarshal(raw, &c); e != nil {
		return nil, e
	}
	if c.Expires <= time.Now().Unix() {
		return nil, errors.New("expired challenge")
	}
	user, e := a.store.GetUserByID(ctx, c.UserID)
	if e != nil || user.Role != "admin" || user.Status != "active" || user.Email != c.Email || user.AuthVersion != c.AuthVersion {
		return nil, errors.New("invalidated challenge")
	}
	return &c, nil
}
func (a *app) adminVerify(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ChallengeID string `json:"challenge_id"`
		Code        string `json:"code"`
	}
	if !decode(w, r, &in) {
		return
	}
	c, err := a.readChallenge(r.Context(), in.ChallengeID)
	if err != nil {
		fail(w, 401, "CHALLENGE_EXPIRED", "Your sign-in attempt has expired. Enter your password again.")
		return
	}
	v, good := a.config(w, r)
	if !good {
		return
	}
	valid, err := a.checkCode(r.Context(), c.Email, "admin_login:"+digest(in.ChallengeID), in.Code, v.Int("verification_max_attempts"), in.ChallengeID)
	if err != nil {
		internal(w)
		return
	}
	if !valid {
		fail(w, 401, "INVALID_CODE", "The verification code is invalid or expired.")
		return
	}
	user, err := a.store.GetUserByID(r.Context(), c.UserID)
	if err != nil || user.Role != "admin" || user.Status != "active" || user.Email != c.Email || user.AuthVersion != c.AuthVersion {
		fail(w, 401, "INVALID_CREDENTIALS", "Please sign in again.")
		return
	}
	a.redis.Del(r.Context(), redisPrefix+"login-email:"+digest(c.Email), redisPrefix+"login-ip:"+digest(clientIP(r)))
	a.establish(w, r, user, v)
}
func (a *app) adminResend(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ChallengeID string `json:"challenge_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	c, e := a.readChallenge(r.Context(), in.ChallengeID)
	if e != nil {
		fail(w, 401, "CHALLENGE_EXPIRED", "Your sign-in attempt has expired. Enter your password again.")
		return
	}
	v, good := a.config(w, r)
	if !good {
		return
	}
	ttl := int(c.Expires - time.Now().Unix())
	if ttl > v.Int("admin_login_code_ttl_seconds") {
		ttl = v.Int("admin_login_code_ttl_seconds")
	}
	if ttl < 1 {
		fail(w, 401, "CHALLENGE_EXPIRED", "Please sign in again.")
		return
	}
	if e = a.deliver(r.Context(), c.Email, "admin_login:"+digest(in.ChallengeID), clientIP(r), ttl, v, true); e != nil {
		deliveryFailure(w, e)
		return
	}
	ok(w, map[string]int{"retry_after": v.Int("email_send_cooldown_seconds"), "expires_in": ttl})
}
