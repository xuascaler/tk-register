package supplier

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
	"goapi-tk-register/server/internal/diagnostics"
)

const sessionPrefix = "tk-register:{tk-register}:supplier-auth:tk-register-0:"
const loginLeaseDuration = 75 * time.Second
const loginWaitDuration = 5 * time.Second
const fallbackSessionTTL = 30 * time.Minute
const fallbackCooldown = 60 * time.Second

type sessionCache struct {
	redis          *redis.Client
	aead           cipher.AEAD
	fingerprintKey []byte
}
type cachedSession struct {
	Token     string    `json:"token"`
	PublicKey string    `json:"public_key"`
	ExpiresAt time.Time `json:"expires_at"`
}

// NewCachedClient shares encrypted supplier login state between application
// instances, while each operation retains its own HTTP and read-retry budgets.
func NewCachedClient(r *redis.Client, encryptionKey string) (*Client, error) {
	if r == nil || len(encryptionKey) < 32 {
		return nil, errors.New("supplier session cache configuration is invalid")
	}
	key := sha256.Sum256([]byte("tk-register:supplier-session:encryption:v1\x00" + encryptionKey))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	fingerprint := sha256.Sum256([]byte("tk-register:supplier-session:fingerprint:v1\x00" + encryptionKey))
	c := NewClient()
	c.cache = &sessionCache{redis: r, aead: aead, fingerprintKey: fingerprint[:]}
	return c, nil
}

// InvalidateSessions changes the cache namespace atomically. An in-flight old
// login can only populate its old namespace, never revive a changed configuration.
func (c *Client) InvalidateSessions(ctx context.Context) error {
	if c.cache == nil {
		return nil
	}
	if err := c.cache.redis.Incr(ctx, sessionPrefix+"revision").Err(); err != nil {
		return safeError(CodeUnavailable)
	}
	diagnostics.LogSession(ctx, "credentials_changed", "login", 0)
	return nil
}

// RetryDetails exposes only safe scheduling metadata. Deferred marks a local
// cooldown or login-lock gate; an actual upstream 429 is never deferred.
func RetryDetails(err error) (int, time.Time, string, bool) {
	var e *Error
	if !errors.As(err, &e) {
		return 0, time.Time{}, "", false
	}
	seconds := e.RetryAfterSeconds
	if !e.RetryAt.IsZero() {
		seconds = max(1, int(math.Ceil(time.Until(e.RetryAt).Seconds())))
	}
	return seconds, e.RetryAt, e.Stage, e.Deferred
}

func retryError(stage string, at time.Time, deferred bool) *Error {
	e := safeError(CodeRateLimited)
	e.Stage = stage
	e.RetryAt = at
	e.RetryAfterSeconds = max(1, int(math.Ceil(time.Until(at).Seconds())))
	e.Deferred = deferred
	if stage == "login_lock" {
		e.Code = CodeSessionBusy
		e.Message = "A supplier login is in progress. Please try again shortly."
	} else if stage == "login" {
		e.Message = "The supplier login is rate limited. Please wait before trying again."
	} else {
		e.Message = "The supplier account query is rate limited. Please wait before trying again."
	}
	return e
}

func (c *sessionCache) credentialKey(ctx context.Context, username, password string) (string, error) {
	revision, err := c.redis.Get(ctx, sessionPrefix+"revision").Result()
	if err == redis.Nil {
		revision = "0"
	} else if err != nil {
		return "", safeError(CodeUnavailable)
	}
	if _, err = strconv.ParseUint(revision, 10, 64); err != nil {
		return "", safeError(CodeUnavailable)
	}
	h := hmac.New(sha256.New, c.fingerprintKey)
	b, _ := json.Marshal([]string{strings.TrimSpace(username), password})
	h.Write(b)
	return sessionPrefix + "session:" + revision + ":" + hex.EncodeToString(h.Sum(nil)), nil
}
func (c *sessionCache) seal(key string, value cachedSession) (string, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(c.aead.Seal(nonce, nonce, b, []byte(key))), nil
}
func (c *sessionCache) open(key, value string) (cachedSession, error) {
	var entry cachedSession
	b, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil || len(b) < c.aead.NonceSize() {
		return entry, errors.New("invalid cached session")
	}
	b, err = c.aead.Open(nil, b[:c.aead.NonceSize()], b[c.aead.NonceSize():], []byte(key))
	if err != nil {
		return entry, err
	}
	if json.Unmarshal(b, &entry) != nil || entry.Token == "" || len(entry.Token) > 16384 || strings.ContainsAny(entry.Token, "\r\n \t") || entry.PublicKey == "" || len(entry.PublicKey) > 16384 || entry.ExpiresAt.IsZero() {
		return cachedSession{}, errors.New("invalid cached session")
	}
	return entry, nil
}

var compareDelete = redis.NewScript(`if redis.call('GET',KEYS[1]) == ARGV[1] then return redis.call('DEL',KEYS[1]) end return 0`)
var setCooldown = redis.NewScript(`local old=tonumber(redis.call('GET',KEYS[1]) or '0'); local new=tonumber(ARGV[1]); if new>old then redis.call('PSETEX',KEYS[1],ARGV[2],ARGV[1]); return new end return old`)

func (c *sessionCache) read(ctx context.Context, key string) (cachedSession, string, error) {
	var entry cachedSession
	raw, err := c.redis.Get(ctx, key).Result()
	if err == redis.Nil {
		return entry, "", nil
	}
	if err != nil {
		return entry, "", safeError(CodeUnavailable)
	}
	entry, err = c.open(key, raw)
	if err != nil || !entry.ExpiresAt.After(time.Now()) {
		if e := compareDelete.Run(ctx, c.redis, []string{key}, raw).Err(); e != nil {
			return cachedSession{}, "", safeError(CodeUnavailable)
		}
		diagnostics.LogSession(ctx, "cache_expired", "login", 0)
		return cachedSession{}, "", nil
	}
	return entry, raw, nil
}
func (c *sessionCache) cooldown(ctx context.Context, stage string) error {
	raw, err := c.redis.Get(ctx, sessionPrefix+"cooldown:"+stage).Result()
	if err == redis.Nil {
		return nil
	}
	if err != nil {
		return safeError(CodeUnavailable)
	}
	millis, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return safeError(CodeUnavailable)
	}
	at := time.UnixMilli(millis)
	if !at.After(time.Now()) {
		return nil
	}
	e := retryError(stage, at, true)
	diagnostics.LogSession(ctx, "cooldown_wait", stage, e.RetryAfterSeconds)
	return e
}
func (c *sessionCache) recordCooldown(ctx context.Context, stage string, retry *int64) error {
	wait := fallbackCooldown
	if retry != nil {
		wait = time.Duration(max(int64(1), *retry)) * time.Second
	}
	at := time.Now().Add(wait)
	persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	value, err := setCooldown.Run(persist, c.redis, []string{sessionPrefix + "cooldown:" + stage}, at.UnixMilli(), wait.Milliseconds()).Int64()
	if err == nil {
		at = time.UnixMilli(value)
	} else {
		diagnostics.LogSession(ctx, "cache_unavailable", stage, 0)
	}
	e := retryError(stage, at, false)
	diagnostics.LogSession(ctx, "cooldown_started", stage, e.RetryAfterSeconds)
	return e
}
func (s *session) invalidateCurrent(ctx context.Context) error {
	if s.cache == nil || s.cacheKey == "" || s.cacheValue == "" {
		return nil
	}
	if err := compareDelete.Run(ctx, s.cache.redis, []string{s.cacheKey}, s.cacheValue).Err(); err != nil {
		return safeError(CodeUnavailable)
	}
	diagnostics.LogSession(ctx, "session_invalidated", "login", 0)
	s.cacheValue = ""
	return nil
}
func (s *session) applySession(entry cachedSession, raw string) {
	s.token = entry.Token
	s.publicKey = entry.PublicKey
	s.expiresAt = entry.ExpiresAt
	s.cacheValue = raw
}
func (s *session) authenticate(ctx context.Context, username, password string, force bool) error {
	if s.cache == nil {
		return s.login(ctx, username, password)
	}
	if strings.TrimSpace(username) == "" || len(strings.TrimSpace(username)) > 256 || password == "" || len(password) > 4096 {
		return safeError(CodeCredentialsInvalid)
	}
	if !force {
		if err := s.cache.cooldown(ctx, "accounts"); err != nil {
			return err
		}
	}
	key, err := s.cache.credentialKey(ctx, username, password)
	if err != nil {
		return err
	}
	s.cacheKey = key
	deadline := time.Now().Add(loginWaitDuration)
	loggedWait := false
	for {
		if ctx.Err() != nil {
			return collectionTransportError(ctx, ctx.Err())
		}
		if !force {
			if err := s.cache.cooldown(ctx, "accounts"); err != nil {
				return err
			}
			entry, raw, err := s.cache.read(ctx, key)
			if err != nil {
				return err
			}
			if raw != "" {
				s.applySession(entry, raw)
				diagnostics.LogSession(ctx, "cache_hit", "login", 0)
				return nil
			}
		}
		if err := s.cache.cooldown(ctx, "login"); err != nil {
			return err
		}
		nonce := make([]byte, 24)
		if _, err = rand.Read(nonce); err != nil {
			return safeError(CodeUnavailable)
		}
		lockID := base64.RawURLEncoding.EncodeToString(nonce)
		locked, err := s.cache.redis.SetNX(ctx, sessionPrefix+"login-lock", lockID, loginLeaseDuration).Result()
		if err != nil {
			return safeError(CodeUnavailable)
		}
		if locked {
			defer func() {
				release, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_ = compareDelete.Run(release, s.cache.redis, []string{sessionPrefix + "login-lock"}, lockID).Err()
			}()
			// A preceding owner could have populated the cache between our read and SETNX.
			if !force {
				if err := s.cache.cooldown(ctx, "accounts"); err != nil {
					return err
				}
				entry, raw, err := s.cache.read(ctx, key)
				if err != nil {
					return err
				}
				if raw != "" {
					s.applySession(entry, raw)
					diagnostics.LogSession(ctx, "cache_hit", "login", 0)
					return nil
				}
			}
			if err := s.cache.cooldown(ctx, "login"); err != nil {
				return err
			}
			reason := "cache_miss"
			if force {
				reason = "test_login"
			}
			diagnostics.LogSession(ctx, reason, "login", 0)
			if err := s.login(ctx, username, password); err != nil {
				return err
			}
			if force {
				return nil
			}
			entry := cachedSession{Token: s.token, PublicKey: s.publicKey, ExpiresAt: s.expiresAt}
			raw, err := s.cache.seal(key, entry)
			if err != nil {
				return safeError(CodeUnavailable)
			}
			ttl := time.Until(entry.ExpiresAt)
			if ttl <= 0 {
				return safeError(CodeSessionExpired)
			}
			// Publish only while still holding our distributed lease. A late login must
			// not overwrite a replacement created after its lease expired.
			stored, err := redis.NewScript(`if redis.call('GET',KEYS[1]) == ARGV[1] then redis.call('PSETEX',KEYS[2],ARGV[2],ARGV[3]); return 1 end return 0`).Run(ctx, s.cache.redis, []string{sessionPrefix + "login-lock", key}, lockID, max(int64(1), ttl.Milliseconds()), raw).Int()
			if err != nil {
				return safeError(CodeUnavailable)
			}
			if stored != 1 {
				return retryError("login_lock", time.Now().Add(time.Second), true)
			}
			s.cacheValue = raw
			diagnostics.LogSession(ctx, "session_saved", "login", 0)
			return nil
		}
		if !loggedWait {
			diagnostics.LogSession(ctx, "login_lock_wait", "login_lock", 1)
			loggedWait = true
		}
		if !time.Now().Before(deadline) {
			return retryError("login_lock", time.Now().Add(time.Second), true)
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return collectionTransportError(ctx, ctx.Err())
		case <-timer.C:
		}
	}
}
func (s *session) reauthenticate(ctx context.Context, username, password string) error {
	if err := s.invalidateCurrent(ctx); err != nil {
		return err
	}
	if s.cache != nil {
		diagnostics.LogSession(ctx, "session_refresh", "login", 0)
	}
	return s.authenticate(ctx, username, password, false)
}
func sessionExpiry(raw json.RawMessage, now time.Time) time.Time {
	ttl := fallbackSessionTTL
	if len(raw) > 0 && string(raw) != "null" {
		var n json.Number
		if json.Unmarshal(raw, &n) == nil {
			if seconds, err := strconv.ParseFloat(n.String(), 64); err == nil && seconds > 0 && seconds <= 7*24*60*60 {
				ttl = time.Duration(seconds * float64(time.Second))
			}
		}
	}
	// Bound local retention and expire before the supplier's stated lifetime.
	ttl = min(ttl, 24*time.Hour)
	margin := min(time.Minute, ttl/10)
	return now.Add(ttl - margin)
}

// Cooldown policy clamps valid large waits, rather than replacing a long server
// deadline with the shorter fallback. Raw headers are never stored or logged.
func cooldownRetryAfter(value string, now time.Time) *int64 {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return nil
	}
	const maximum = int64(7 * 24 * 60 * 60)
	digits := true
	for _, r := range value {
		if r < '0' || r > '9' {
			digits = false
			break
		}
	}
	var seconds int64
	if digits {
		n, err := strconv.ParseUint(value, 10, 64)
		if err != nil || n > uint64(maximum) {
			seconds = maximum
		} else {
			seconds = int64(n)
		}
	} else {
		at, err := http.ParseTime(value)
		if err != nil {
			return nil
		}
		seconds = int64(math.Ceil(at.Sub(now).Seconds()))
		seconds = max(0, min(maximum, seconds))
	}
	return &seconds
}
