package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
	"goapi-tk-register/server/common"
)

// A shared hash tag keeps email/IP limits and session/index transactions in one
// Redis slot. Per-email tags cannot cover operations that also use an IP key.
const redisPrefix = "tk-register:{tk-register}:"

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func digest(value string) string { v := sha256.Sum256([]byte(value)); return hex.EncodeToString(v[:]) }
func randomCode() (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 6)
	for i := range b {
		n, e := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if e != nil {
			return "", e
		}
		b[i] = alphabet[n.Int64()]
	}
	return string(b), nil
}
func (a *app) codeHash(email, purpose, code string) string {
	h := hmac.New(sha256.New, []byte(a.secret))
	h.Write([]byte(email + "\x00" + purpose + "\x00" + strings.ToUpper(strings.TrimSpace(code))))
	return hex.EncodeToString(h.Sum(nil))
}
func codeKey(email, purpose string) string {
	return redisPrefix + "code:" + purpose + ":" + digest(email)
}
func cooldownKey(email string) string { return redisPrefix + "cooldown:" + digest(email) }
func challengeKey(id string) string   { return redisPrefix + "admin-challenge:" + digest(id) }
func sessionKey(token string) string  { return redisPrefix + "session:" + digest(token) }
func indexKey(id uint64) string       { return redisPrefix + "user-sessions:" + strconv.FormatUint(id, 10) }

// Reserve all delivery limits atomically before contacting SMTP.
var reserveSend = redis.NewScript(`
 local ttl=redis.call('TTL',KEYS[1]);if ttl>0 then return ttl end
 if tonumber(redis.call('GET',KEYS[2]) or '0')>=tonumber(ARGV[3]) then return math.max(1,redis.call('TTL',KEYS[2])) end
 if tonumber(redis.call('GET',KEYS[3]) or '0')>=tonumber(ARGV[4]) then return math.max(1,redis.call('TTL',KEYS[3])) end
 redis.call('SET',KEYS[1],ARGV[1],'EX',ARGV[2])
 for i=2,3 do local n=redis.call('INCR',KEYS[i]);if n==1 then redis.call('EXPIRE',KEYS[i],3600) end end
 return 0
`)
var releaseSend = redis.NewScript(`if redis.call('GET',KEYS[1])==ARGV[1] then redis.call('DEL',KEYS[1]) end return 1`)
var saveCode = redis.NewScript(`
 if redis.call('GET',KEYS[1])~=ARGV[1] then return 0 end
 redis.call('DEL',KEYS[2]);redis.call('HSET',KEYS[2],'hash',ARGV[2],'request',ARGV[1],'attempts',0)
 redis.call('EXPIRE',KEYS[2],ARGV[3]);redis.call('EXPIRE',KEYS[1],ARGV[4]);return 1
`)

// Code consumption and optional administrator challenge consumption are one operation.
var consumeCode = redis.NewScript(`
 if #KEYS>1 and redis.call('EXISTS',KEYS[2])==0 then return '' end
 local hash=redis.call('HGET',KEYS[1],'hash');if not hash then return '' end
 if hash~=ARGV[1] then
  local n=redis.call('HINCRBY',KEYS[1],'attempts',1)
  if n>=tonumber(ARGV[2]) then redis.call('DEL',KEYS[1]);if #KEYS>1 then redis.call('DEL',KEYS[2]) end end
  return ''
 end
 local request=redis.call('HGET',KEYS[1],'request');redis.call('DEL',KEYS[1]);if #KEYS>1 then redis.call('DEL',KEYS[2]) end
 return request
`)
var loginAttempt = redis.NewScript(`
 for _,key in ipairs(KEYS) do if tonumber(redis.call('GET',key) or '0')>=tonumber(ARGV[1]) then return math.max(1,redis.call('TTL',key)) end end
 for _,key in ipairs(KEYS) do local n=redis.call('INCR',key);if n==1 then redis.call('EXPIRE',key,ARGV[2]) end end
 return 0
`)

type session struct {
	AuthVersion uint64 `json:"auth_version"`
	UserID      uint64 `json:"user_id"`
	Role        string `json:"role"`
	CSRF        string `json:"csrf"`
	Expires     int64  `json:"expires"`
	Idle        int    `json:"idle"`
}

var saveSession = redis.NewScript(`
 redis.call('SET',KEYS[1],ARGV[1],'EX',ARGV[2])
 redis.call('ZREMRANGEBYSCORE',KEYS[2],'-inf',ARGV[3]);redis.call('ZADD',KEYS[2],ARGV[4],ARGV[5])
 local ttl=redis.call('TTL',KEYS[2]);if ttl<tonumber(ARGV[6]) then redis.call('EXPIRE',KEYS[2],ARGV[6]) end
 return 1
`)
var touchSession = redis.NewScript(`
 local value=redis.call('GET',KEYS[1]);if not value then return '' end
 local s=cjson.decode(value);local remaining=tonumber(s.expires)-tonumber(ARGV[1])
 if remaining<=0 then redis.call('DEL',KEYS[1]);return '' end
 if tonumber(s.idle)>0 then redis.call('EXPIRE',KEYS[1],math.min(remaining,tonumber(s.idle))) end
 return value
`)

func (a *app) newSession(ctx context.Context, uid uint64, role string, absolute, idle int, authenticatedVersions ...uint64) (string, *session, error) {
	user, err := a.store.GetUserByID(ctx, uid)
	if err != nil {
		return "", nil, err
	}
	version := user.AuthVersion
	if len(authenticatedVersions) > 0 {
		version = authenticatedVersions[0]
	}
	if user.Status != "active" || user.Role != role || user.AuthVersion != version {
		return "", nil, errSessionExpired
	}
	token, err := randomToken()
	if err != nil {
		return "", nil, err
	}
	csrf, err := randomToken()
	if err != nil {
		return "", nil, err
	}
	now := time.Now().Unix()
	s := &session{AuthVersion: version, UserID: uid, Role: role, CSRF: csrf, Expires: now + int64(absolute), Idle: idle}
	b, err := common.Marshal(s)
	if err != nil {
		return "", nil, err
	}
	ttl := absolute
	if idle > 0 && idle < ttl {
		ttl = idle
	}
	_, err = saveSession.Run(ctx, a.redis, []string{sessionKey(token), indexKey(uid)}, string(b), ttl, now, s.Expires, digest(token), absolute).Result()
	return token, s, err
}

var errSessionExpired = errors.New("expired session")

func (a *app) getSession(ctx context.Context, token string) (*session, error) {
	if len(token) != 43 {
		return nil, errSessionExpired
	}
	raw, err := touchSession.Run(ctx, a.redis, []string{sessionKey(token)}, time.Now().Unix()).Text()
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return nil, errSessionExpired
	}
	var s session
	if err = common.Unmarshal([]byte(raw), &s); err != nil {
		return nil, err
	}
	return &s, nil
}
