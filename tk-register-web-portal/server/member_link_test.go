package main

import (
	"context"
	"goapi-tk-register/server/internal/core"
	"testing"
	"time"
)

func TestMemberLinkHTTPVerificationSessionAndReplay(t *testing.T) {
	a, _, mail := fixture(t)
	ctx := context.Background()
	actor, cookie, csrf := vendorSession(t, a, "link-parent@example.com", "member")
	target, targetCookie, _ := vendorSession(t, a, "link-target@example.com", "member")
	_, otherCookie, otherCSRF := vendorSession(t, a, "other-parent@example.com", "member")
	body := map[string]string{"email": target.Email}
	requireStatus(t, request(t, a, "POST", "members/link-code", body, nil, ""), 401)
	requireStatus(t, request(t, a, "POST", "members/link-code", body, cookie, ""), 403)
	if mail.count != 0 {
		t.Fatal("unauthorized mail")
	}
	requireStatus(t, request(t, a, "POST", "members/link-code", body, cookie, csrf), 200)
	body["code"] = mail.code(target.Email, "member_link:"+actor.Email)
	if body["code"] == "" {
		t.Fatal("missing dedicated message")
	}
	requireStatus(t, request(t, a, "POST", "members/link-code", body, cookie, csrf), 429)
	requireStatus(t, request(t, a, "POST", "members/link", body, otherCookie, otherCSRF), 400)
	// A link code cannot sign in to the target, or vice versa.
	requireStatus(t, request(t, a, "POST", "auth/login", map[string]string{"email": target.Email, "method": "code", "code": body["code"]}, nil, ""), 401)
	body["parent_user_id"] = "1"
	requireStatus(t, request(t, a, "POST", "members/link", body, cookie, csrf), 400)
	delete(body, "parent_user_id")
	w := request(t, a, "POST", "members/link", body, cookie, csrf)
	requireStatus(t, w, 200)
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("session replaced")
	}
	for _, c := range []struct {
		email  string
		target bool
	}{{actor.Email, false}, {target.Email, true}} {
		ck := cookie
		if c.target {
			ck = targetCookie
		}
		me := request(t, a, "GET", "auth/me", nil, ck, "")
		requireStatus(t, me, 200)
		if data(t, me)["user"].(map[string]any)["email"] != c.email {
			t.Fatal("session identity changed")
		}
	}
	requireStatus(t, request(t, a, "POST", "members/link", body, cookie, csrf), 409)
	saved, _ := a.store.GetUserByID(ctx, target.ID)
	if saved.ParentUserID == nil || *saved.ParentUserID != actor.ID {
		t.Fatal("missing link")
	}
	var audit core.EmailVerification
	if err := a.store.DB.Where("email=? AND purpose=?", target.Email, "member_link").First(&audit).Error; err != nil || audit.Status != "consumed" {
		t.Fatal(audit, err)
	}
}

func TestMemberLinkCodeFailureExpiryAndVersion(t *testing.T) {
	for _, kind := range []string{"smtp", "expiry", "target-version", "actor-version", "login-code", "attempts"} {
		t.Run(kind, func(t *testing.T) {
			a, redis, mail := fixture(t)
			ctx := context.Background()
			actor, cookie, csrf := vendorSession(t, a, "parent@example.com", "member")
			target := addUser(t, a, "existing@example.com", "member")
			body := map[string]string{"email": target.Email}
			if kind == "smtp" {
				mail.fail = true
			}
			sent := request(t, a, "POST", "members/link-code", body, cookie, csrf)
			if kind == "smtp" {
				requireStatus(t, sent, 503)
				body["code"] = "ABC123"
			} else {
				requireStatus(t, sent, 200)
				body["code"] = mail.code(target.Email, "member_link:"+actor.Email)
			}
			switch kind {
			case "expiry":
				redis.FastForward(11 * time.Minute)
			case "target-version":
				a.store.DB.Model(target).Update("auth_version", target.AuthVersion+1)
			case "actor-version":
				a.store.DB.Model(actor).Update("auth_version", actor.AuthVersion+1)
				actor.AuthVersion++
				raw, newCSRF, err := a.newSession(ctx, actor.ID, actor.Role, 3600, 1800)
				if err != nil {
					t.Fatal(err)
				}
				cookie.Value = raw
				csrf = newCSRF.CSRF
			case "login-code":
				a.redis.Del(ctx, codeKey(target.Email, memberLinkPurpose(actor, target)))
				values, _ := a.store.Settings(ctx)
				redis.FastForward(time.Minute)
				if err := a.deliver(ctx, target.Email, "login", "127.0.0.1", 600, values, true); err != nil {
					t.Fatal(err)
				}
				body["code"] = mail.code(target.Email, "login")
			case "attempts":
				values, _ := a.store.Settings(ctx)
				wrong := map[string]string{"email": target.Email, "code": "wrong"}
				for i := 0; i < values.Int("verification_max_attempts"); i++ {
					requireStatus(t, request(t, a, "POST", "members/link", wrong, cookie, csrf), 400)
				}
			}
			requireStatus(t, request(t, a, "POST", "members/link", body, cookie, csrf), 400)
			saved, _ := a.store.GetUserByID(ctx, target.ID)
			if saved.ParentUserID != nil {
				t.Fatal("unauthorized link")
			}
		})
	}
}

func TestMemberLinkRateLimitAndClosedRegistration(t *testing.T) {
	a, _, _ := fixture(t)
	_, cookie, csrf := vendorSession(t, a, "parent@example.com", "member")
	target := addUser(t, a, "existing@example.com", "member")
	if err := a.store.DB.Model(&core.Setting{}).Where("key = ?", "registration_enabled").Update("value", "false").Error; err != nil {
		t.Fatal(err)
	}
	// Linking an existing identity is independent of new-user registration.
	requireStatus(t, request(t, a, "POST", "members/link-code", map[string]string{"email": target.Email}, cookie, csrf), 200)
	for i := 0; i < 9; i++ {
		requireStatus(t, request(t, a, "POST", "members/link", map[string]string{"email": "missing@example.com"}, cookie, csrf), 400)
	}
	requireStatus(t, request(t, a, "POST", "members/link", map[string]string{"email": target.Email}, cookie, csrf), 429)
}
