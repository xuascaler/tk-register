package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/go-redis/redis/v8"
)

// miniredis accepts cross-slot operations that the production Redis proxy
// rejects. Observe the commands issued by real authentication handlers so a
// newly added key or transaction cannot silently reintroduce that mismatch.
type hashSlotGuard struct {
	mu           sync.Mutex
	problems     []string
	commands     map[string]int
	scriptWidths map[string]map[int]int
}

func newHashSlotGuard() *hashSlotGuard {
	return &hashSlotGuard{commands: map[string]int{}, scriptWidths: map[string]map[int]int{}}
}
func authCommandHashTag(key string) string {
	start := strings.IndexByte(key, '{')
	if start < 0 {
		return ""
	}
	rest := key[start+1:]
	end := strings.IndexByte(rest, '}')
	if end <= 0 {
		return ""
	}
	return rest[:end]
}
func (g *hashSlotGuard) check(operation string, keys []string) {
	if len(keys) < 2 {
		return
	}
	g.commands[operation]++
	tag := authCommandHashTag(keys[0])
	for _, key := range keys {
		if tag == "" || authCommandHashTag(key) != tag {
			g.problems = append(g.problems, fmt.Sprintf("%s: %d keys do not share one nonempty hash tag", operation, len(keys)))
			return
		}
	}
}
func (g *hashSlotGuard) inspect(cmd redis.Cmder) []string {
	args := cmd.Args()
	switch cmd.Name() {
	case "eval", "evalsha":
		if len(args) < 3 {
			g.problems = append(g.problems, "invalid script command")
			return nil
		}
		count, err := strconv.Atoi(fmt.Sprint(args[2]))
		if err != nil || count < 0 || len(args) < 3+count {
			g.problems = append(g.problems, "invalid script key count")
			return nil
		}
		keys := make([]string, count)
		for i := range keys {
			keys[i] = fmt.Sprint(args[i+3])
		}
		hash := fmt.Sprint(args[1])
		if cmd.Name() == "eval" {
			sum := sha1.Sum([]byte(hash))
			hash = hex.EncodeToString(sum[:])
		}
		if g.scriptWidths[hash] == nil {
			g.scriptWidths[hash] = map[int]int{}
		}
		g.scriptWidths[hash][len(keys)]++
		g.check(cmd.Name(), keys)
		return keys
	case "del":
		keys := make([]string, len(args)-1)
		for i := range keys {
			keys[i] = fmt.Sprint(args[i+1])
		}
		g.check("del", keys)
		return keys
	case "zrem":
		return []string{fmt.Sprint(args[1])}
	case "multi", "exec":
		return nil
	default:
		return nil
	}
}
func (g *hashSlotGuard) BeforeProcess(ctx context.Context, cmd redis.Cmder) (context.Context, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.inspect(cmd)
	return ctx, nil
}
func (g *hashSlotGuard) AfterProcess(context.Context, redis.Cmder) error { return nil }
func (g *hashSlotGuard) BeforeProcessPipeline(ctx context.Context, cmds []redis.Cmder) (context.Context, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var keys []string
	transaction := false
	for _, cmd := range cmds {
		if cmd.Name() == "multi" {
			transaction = true
		}
		switch cmd.Name() {
		case "multi", "exec", "del", "zrem", "eval", "evalsha":
		default:
			g.problems = append(g.problems, "uncovered pipeline command: "+cmd.Name())
		}
		keys = append(keys, g.inspect(cmd)...)
	}
	if transaction {
		g.check("transaction", keys)
	}
	return ctx, nil
}
func (g *hashSlotGuard) AfterProcessPipeline(context.Context, []redis.Cmder) error { return nil }

func TestAuthCommandsKeepAtomicRedisKeysInOneHashSlot(t *testing.T) {
	a, _, mail := fixture(t)
	guard := newHashSlotGuard()
	a.redis.AddHook(guard)
	email := "slot-member@example.com"
	requireStatus(t, send(t, a, email, "register"), http.StatusOK)
	signup := request(t, a, "POST", "auth/register", map[string]string{
		"email": email, "username": email, "password": "correct-password", "code": mail.code(email, "register"),
	}, nil, "")
	requireStatus(t, signup, http.StatusOK)
	// Password authentication exercises the email+IP attempt script and its
	// multi-key DEL. Passing an existing cookie also exercises session rotation.
	login := request(t, a, "POST", "auth/login", map[string]string{
		"email": email, "method": "password", "password": "correct-password",
	}, signup.Result().Cookies()[0], "")
	requireStatus(t, login, http.StatusOK)
	requireStatus(t, request(t, a, "POST", "auth/logout", nil, login.Result().Cookies()[0], data(t, login)["csrf_token"].(string)), http.StatusOK)
	// Administrator verification consumes both the code and challenge atomically.
	admin := addUser(t, a, "slot-admin@example.com", "admin")
	firstFactor := password(t, a, admin.Email)
	requireStatus(t, firstFactor, http.StatusOK)
	requireStatus(t, request(t, a, "POST", "auth/admin/verify", map[string]string{
		"challenge_id": data(t, firstFactor)["challenge_id"].(string), "code": mail.code(admin.Email, "admin_login"),
	}, nil, ""), http.StatusOK)
	guard.mu.Lock()
	defer guard.mu.Unlock()
	for _, problem := range guard.problems {
		t.Error(problem)
	}
	for _, operation := range []string{"eval", "evalsha", "del", "transaction"} {
		if guard.commands[operation] == 0 {
			t.Errorf("auth flow did not exercise multi-key %s", operation)
		}
	}
	for _, script := range []struct {
		name, hash string
		width      int
	}{
		{"reserveSend", reserveSend.Hash(), 3},
		{"saveCode", saveCode.Hash(), 2},
		{"consumeCode (administrator)", consumeCode.Hash(), 2},
		{"loginAttempt", loginAttempt.Hash(), 2},
		{"saveSession", saveSession.Hash(), 2},
	} {
		if guard.scriptWidths[script.hash][script.width] == 0 {
			t.Errorf("auth flow did not exercise %s with %d keys", script.name, script.width)
		}
	}
}
