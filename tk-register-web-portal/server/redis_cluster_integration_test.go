package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
	"goapi-tk-register/server/common"
)

// TestRedisClusterIntegration is opt-in and can run against a managed Redis
// proxy. It uses no database, accounts, SMTP, or application session keys.
// Every written key has a random test namespace and expires within 60 seconds.
// Set TK_REGISTER_TEST_REDIS_EXPECT_CROSSSLOT=1 for the read-only negative
// control that demonstrates why the previous untagged key layout was invalid.
func TestRedisClusterIntegration(t *testing.T) {
	redisURL := os.Getenv("TK_REGISTER_TEST_REDIS_URL")
	if redisURL == "" {
		t.Skip("set TK_REGISTER_TEST_REDIS_URL to run the Redis integration test")
	}
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		// Parse errors can contain the original URL and credentials.
		t.Fatal("invalid TK_REGISTER_TEST_REDIS_URL")
	}
	options.DialTimeout = 5 * time.Second
	options.ReadTimeout = 5 * time.Second
	options.WriteTimeout = 5 * time.Second
	options.MaxRetries = -1
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })

	nonce := make([]byte, 24)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal("could not create integration test namespace")
	}
	testID := hex.EncodeToString(nonce)
	prefix := "tk-register:{tk-register}:integration:" + testID + ":"
	key := func(suffix string) string { return prefix + suffix }
	keys := []string{
		key("cooldown"), key("send-email"), key("send-ip"), key("code"),
		key("challenge"), key("login-email"), key("login-ip"),
		key("session"), key("user-sessions"),
	}
	// Cleanup is registered before any writes, uses a fresh context, and only
	// deletes explicitly known keys belonging to this test invocation.
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := client.Del(cleanup, keys...).Err(); err != nil {
			t.Errorf("integration key cleanup failed (%s); keys have bounded TTLs", clusterSafeError(err))
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	clusterCheck(t, "Redis connectivity", client.Ping(ctx).Err())

	t.Run("application_key_builders_share_hash_tag", func(t *testing.T) {
		// Inspect only; these production-shaped keys are never read or written.
		for name, value := range map[string]string{
			"code":          codeKey("integration@example.invalid", "registration"),
			"cooldown":      cooldownKey("integration@example.invalid"),
			"challenge":     challengeKey(testID),
			"session":       sessionKey(testID),
			"user-sessions": indexKey(0),
		} {
			if !strings.HasPrefix(value, "tk-register:{tk-register}:") {
				t.Errorf("%s key is missing the shared TK Register hash tag", name)
			}
		}
	})

	t.Run("untagged_keys_are_rejected_control", func(t *testing.T) {
		if os.Getenv("TK_REGISTER_TEST_REDIS_EXPECT_CROSSSLOT") != "1" {
			t.Skip("set TK_REGISTER_TEST_REDIS_EXPECT_CROSSSLOT=1 to require the cluster negative control")
		}
		// These deliberately untagged keys are unique and READ ONLY. They never
		// exist and are not added to the list of written/cleanup keys.
		oldA := "tk-register:integration:" + testID + ":control:a"
		oldB := "tk-register:integration:" + testID + ":control:b"
		for clusterSlot(oldA) == clusterSlot(oldB) {
			oldB += "b"
		}
		readOnly := redis.NewScript(`return {redis.call('EXISTS',KEYS[1]),redis.call('EXISTS',KEYS[2])}`)
		_, err := readOnly.Run(ctx, client, []string{oldA, oldB}).Result()
		if err == nil || !strings.Contains(strings.ToUpper(err.Error()), "CROSSSLOT") {
			t.Fatalf("old untagged keys should fail with CROSSSLOT (%s)", clusterSafeError(err))
		}
	})

	t.Run("delivery_and_code_scripts", func(t *testing.T) {
		request := "integration-request-" + testID
		codeHash := digest("integration-code-" + testID)
		// reserveSend initializes new hourly counters with a 3,600-second TTL.
		// Preseed this test's counters with value 1 and a 60-second TTL so its
		// actual increment path preserves the required short expiration.
		clusterCheck(t, "seed email counter", client.Set(ctx, key("send-email"), 1, 60*time.Second).Err())
		clusterCheck(t, "seed IP counter", client.Set(ctx, key("send-ip"), 1, 60*time.Second).Err())
		sendKeys := []string{key("cooldown"), key("send-email"), key("send-ip")}
		reserved, err := reserveSend.Run(ctx, client, sendKeys, request, 60, 10, 30).Int()
		clusterCheck(t, "reserveSend", err)
		if reserved != 0 {
			t.Fatal("first delivery reservation was unexpectedly limited")
		}
		retry, err := reserveSend.Run(ctx, client, sendKeys, request, 60, 10, 30).Int()
		clusterCheck(t, "reserveSend cooldown", err)
		if retry <= 0 || retry > 60 {
			t.Fatal("delivery reservation did not enforce its cooldown")
		}
		_, err = releaseSend.Run(ctx, client, []string{key("cooldown")}, "wrong-owner").Result()
		clusterCheck(t, "releaseSend other owner", err)
		owner, err := client.Get(ctx, key("cooldown")).Result()
		clusterCheck(t, "read reservation owner", err)
		if owner != request {
			t.Fatal("releaseSend removed another reservation")
		}
		_, err = releaseSend.Run(ctx, client, []string{key("cooldown")}, request).Result()
		clusterCheck(t, "releaseSend", err)
		remaining, err := client.Exists(ctx, key("cooldown")).Result()
		clusterCheck(t, "check released reservation", err)
		if remaining != 0 {
			t.Fatal("releaseSend did not release its reservation")
		}
		_, err = reserveSend.Run(ctx, client, sendKeys, request, 60, 10, 30).Result()
		clusterCheck(t, "reserveSend after release", err)
		saved, err := saveCode.Run(ctx, client, []string{key("cooldown"), key("code")}, request, codeHash, 60, 60).Int()
		clusterCheck(t, "saveCode", err)
		if saved != 1 {
			t.Fatal("saveCode did not store the reserved code")
		}
		clusterCheck(t, "seed administrator challenge", client.Set(ctx, key("challenge"), "integration-only", 60*time.Second).Err())
		codeKeys := []string{key("code"), key("challenge")}
		consumed, err := consumeCode.Run(ctx, client, codeKeys, "wrong-hash", 5).Text()
		clusterCheck(t, "consumeCode wrong code", err)
		if consumed != "" {
			t.Fatal("incorrect code was consumed")
		}
		consumed, err = consumeCode.Run(ctx, client, codeKeys, codeHash, 5).Text()
		clusterCheck(t, "consumeCode with administrator challenge", err)
		if consumed != request {
			t.Fatal("correct code did not return the reservation request ID")
		}
		remaining, err = client.Exists(ctx, codeKeys...).Result()
		clusterCheck(t, "verify code and challenge consumption", err)
		if remaining != 0 {
			t.Fatal("code and administrator challenge were not consumed atomically")
		}
		consumed, err = consumeCode.Run(ctx, client, codeKeys, codeHash, 5).Text()
		clusterCheck(t, "consumeCode replay", err)
		if consumed != "" {
			t.Fatal("code was reusable")
		}
		// Members consume a code without an administrator challenge. Exercise
		// that single-key invocation against the same real script and proxy.
		saved, err = saveCode.Run(ctx, client, []string{key("cooldown"), key("code")}, request, codeHash, 60, 60).Int()
		clusterCheck(t, "saveCode for member", err)
		if saved != 1 {
			t.Fatal("member code was not saved under the existing reservation")
		}
		consumed, err = consumeCode.Run(ctx, client, []string{key("code")}, codeHash, 5).Text()
		clusterCheck(t, "consumeCode for member", err)
		if consumed != request {
			t.Fatal("member code did not return the reservation request ID")
		}
		remaining, err = client.Exists(ctx, key("code")).Result()
		clusterCheck(t, "verify member code consumption", err)
		if remaining != 0 {
			t.Fatal("member code was not consumed")
		}
	})

	t.Run("login_limits_and_multi_key_delete", func(t *testing.T) {
		loginKeys := []string{key("login-email"), key("login-ip")}
		for i := 0; i < 2; i++ {
			retry, err := loginAttempt.Run(ctx, client, loginKeys, 2, 60).Int()
			clusterCheck(t, "loginAttempt", err)
			if retry != 0 {
				t.Fatal("login rate limit activated too early")
			}
		}
		retry, err := loginAttempt.Run(ctx, client, loginKeys, 2, 60).Int()
		clusterCheck(t, "loginAttempt limit", err)
		if retry <= 0 || retry > 60 {
			t.Fatal("login rate limit was not enforced")
		}
		deleted, err := client.Del(ctx, loginKeys...).Result()
		clusterCheck(t, "multi-key login counter DEL", err)
		if deleted != 2 {
			t.Fatal("multi-key DEL did not delete both login counters")
		}
	})

	t.Run("session_scripts_and_transactional_revoke", func(t *testing.T) {
		now := time.Now().Unix()
		s := session{UserID: 0, Role: "admin", CSRF: "integration-only", Expires: now + 60, Idle: 30}
		body, err := common.Marshal(s)
		clusterCheck(t, "encode isolated session", err)
		member := digest(testID)
		sessionKeys := []string{key("session"), key("user-sessions")}
		saved, err := saveSession.Run(ctx, client, sessionKeys, string(body), 30, now, s.Expires, member, 60).Int()
		clusterCheck(t, "saveSession", err)
		if saved != 1 {
			t.Fatal("session was not saved")
		}
		touched, err := touchSession.Run(ctx, client, []string{key("session")}, now).Text()
		clusterCheck(t, "touchSession", err)
		if touched != string(body) {
			t.Fatal("touchSession did not return the saved session")
		}
		pipe := client.TxPipeline()
		pipe.Del(ctx, key("session"))
		pipe.ZRem(ctx, key("user-sessions"), member)
		_, err = pipe.Exec(ctx)
		_ = pipe.Close()
		clusterCheck(t, "transactional session revoke", err)
		remaining, err := client.Exists(ctx, key("session")).Result()
		clusterCheck(t, "check revoked session", err)
		members, err := client.ZCard(ctx, key("user-sessions")).Result()
		clusterCheck(t, "check session index", err)
		if remaining != 0 || members != 0 {
			t.Fatal("session revoke did not update both session and user index")
		}
	})

	t.Run("all_remaining_test_keys_have_bounded_ttls", func(t *testing.T) {
		for _, k := range keys {
			ttl, err := client.PTTL(ctx, k).Result()
			clusterCheck(t, "check test key expiration", err)
			if ttl == -2*time.Nanosecond { // go-redis preserves Redis's missing-key sentinel.
				continue
			}
			if ttl <= 0 || ttl > 60*time.Second {
				t.Fatal("integration test key has a missing or excessive expiration")
			}
		}
	})
}

func clusterCheck(t *testing.T, step string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s failed (%s)", step, clusterSafeError(err))
	}
}

func clusterSafeError(err error) string {
	if err == nil {
		return "no error"
	}
	for _, category := range []string{"CROSSSLOT", "NOSCRIPT", "WRONGPASS", "NOAUTH", "NOPERM", "READONLY", "MOVED", "ASK"} {
		if strings.Contains(strings.ToUpper(err.Error()), category) {
			return category
		}
	}
	message := strings.ToLower(err.Error())
	for _, category := range []struct{ match, label string }{
		{"bad lua script", "MANAGED_PROXY_BAD_LUA_SCRIPT"},
		{"lua scripts", "MANAGED_PROXY_LUA_SCRIPTS_REJECTED"},
		{"not support", "MANAGED_PROXY_UNSUPPORTED_OPERATION"},
	} {
		if strings.Contains(message, category.match) {
			return category.label
		}
	}
	// Never print Redis URLs, credentials, endpoint addresses, or payloads.
	return fmt.Sprintf("%T", err)
}

// Redis Cluster uses CRC16-XMODEM modulo 16,384. The negative control passes
// deliberately untagged keys, so no hash-tag extraction is needed here.
func clusterSlot(key string) uint16 {
	var crc uint16
	for _, b := range []byte(key) {
		crc ^= uint16(b) << 8
		for bit := 0; bit < 8; bit++ {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc % 16384
}
