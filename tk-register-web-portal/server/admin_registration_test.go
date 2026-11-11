package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"goapi-tk-register/server/internal/core"
	"golang.org/x/crypto/bcrypt"
)

func TestAdminRegistrationPreservesSessionAndCreatesMember(t *testing.T) {
	a, _, mail := fixture(t)
	actor, cookie, csrf := vendorSession(t, a, "operator@example.com", "admin")
	// Even the reserved bootstrap address cannot grant an admin role through
	// this member-creation workflow. Public bootstrap registration is unchanged.
	a.adminEmail = "new-member@example.com"
	email := a.adminEmail
	w := request(t, a, "POST", "admin/users/registration-code", map[string]string{"email": " NEW-MEMBER@EXAMPLE.COM "}, cookie, csrf)
	requireStatus(t, w, 200)
	if data(t, w)["expires_in"].(float64) != 600 || data(t, w)["retry_after"].(float64) != 60 {
		t.Fatal("registration delivery did not use configured TTL/cooldown")
	}
	body := map[string]string{"email": " NEW-MEMBER@EXAMPLE.COM ", "username": "other@example.com", "password": "new-correct-password", "code": mail.code(email, "register"), "role": "admin"}
	requireStatus(t, request(t, a, "POST", "admin/users", body, cookie, csrf), 400)
	delete(body, "role")
	requireStatus(t, request(t, a, "POST", "admin/users", body, cookie, csrf), 400)
	body["username"] = email
	w = request(t, a, "POST", "admin/users", body, cookie, csrf)
	requireStatus(t, w, 200)
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("admin creation must not replace the administrator login cookie")
	}
	result := data(t, w)
	created := result["user"].(map[string]any)
	if len(result) != 1 || len(created) != 5 || created["email"] != email || created["role"] != "member" || created["status"] != "active" {
		t.Fatalf("unexpected safe user response: %+v", result)
	}
	for _, secret := range []string{"password_hash", "csrf_token", "auth_version", body["password"], body["code"]} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("registration response exposed authentication data")
		}
	}
	u, err := a.store.GetUserByEmail(context.Background(), email)
	if err != nil || u.Role != "member" || u.EmailVerifiedAt.IsZero() || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(body["password"])) != nil {
		t.Fatalf("new member was not persisted safely: %v", err)
	}
	if a.redis.Exists(context.Background(), indexKey(u.ID)).Val() != 0 {
		t.Fatal("new member unexpectedly received a login session")
	}
	me := request(t, a, "GET", "auth/me", nil, cookie, "")
	requireStatus(t, me, 200)
	identity := data(t, me)
	if identity["csrf_token"] != csrf || identity["user"].(map[string]any)["email"] != actor.Email {
		t.Fatal("administrator identity or CSRF token changed")
	}
	requireStatus(t, request(t, a, "GET", "admin/users", nil, cookie, ""), 200)
	requireStatus(t, request(t, a, "POST", "admin/users/registration-code", map[string]string{"email": email}, cookie, csrf), 409)
	requireStatus(t, request(t, a, "POST", "admin/users", body, cookie, csrf), 409)
	valid, err := a.checkCode(context.Background(), email, "register", body["code"], 5, "")
	if err != nil || valid {
		t.Fatal("successful registration left a reusable verification code")
	}
}

func TestAdminRegistrationAuthorizationBeforeDelivery(t *testing.T) {
	a, _, mail := fixture(t)
	_, adminCookie, adminCSRF := vendorSession(t, a, "admin@example.com", "admin")
	_, memberCookie, memberCSRF := vendorSession(t, a, "member@example.com", "member")
	body := map[string]string{"email": "target@example.com", "username": "target@example.com", "password": "correct-password", "code": "ABC123"}
	for _, route := range []string{"admin/users/registration-code", "admin/users"} {
		requireStatus(t, request(t, a, "POST", route, body, nil, ""), 401)
		requireStatus(t, request(t, a, "POST", route, body, memberCookie, memberCSRF), 403)
		requireStatus(t, request(t, a, "POST", route, body, adminCookie, ""), 403)
		r := httptest.NewRequest("POST", basePath+"/api/"+route, strings.NewReader(`{"email":"target@example.com"}`))
		r.Header.Set("Origin", "https://other.example")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", adminCSRF)
		r.AddCookie(adminCookie)
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		requireStatus(t, w, 403)
	}
	if mail.count != 0 {
		t.Fatal("unauthorized request caused an email delivery")
	}
	var count int64
	a.store.DB.Model(&core.User{}).Where("email = ?", body["email"]).Count(&count)
	if count != 0 {
		t.Fatal("unauthorized request created a user")
	}
}

func TestAdminRegistrationSharesConfigurationAndSendLimits(t *testing.T) {
	a, m, mail := fixture(t)
	_, cookie, csrf := vendorSession(t, a, "admin@example.com", "admin")
	requireStatus(t, request(t, a, "PUT", "admin/settings", map[string]any{"settings": map[string]string{"registration_code_ttl_seconds": "120", "email_send_cooldown_seconds": "90"}}, cookie, csrf), 200)
	email := "configured@example.com"
	w := request(t, a, "POST", "admin/users/registration-code", map[string]string{"email": email}, cookie, csrf)
	requireStatus(t, w, 200)
	if data(t, w)["expires_in"].(float64) != 120 || data(t, w)["retry_after"].(float64) != 90 {
		t.Fatal("admin delivery ignored configuration")
	}
	requireStatus(t, send(t, a, email, "register"), 429)
	requireStatus(t, request(t, a, "POST", "admin/users/registration-code", map[string]string{"email": email}, cookie, csrf), 429)
	m.FastForward(91 * time.Second)
	requireStatus(t, send(t, a, email, "register"), 200)
	requireStatus(t, request(t, a, "POST", "admin/users/registration-code", map[string]string{"email": email}, cookie, csrf), 429)
	body := map[string]string{"email": email, "username": email, "password": "correct-password", "code": mail.code(email, "register")}
	requireStatus(t, request(t, a, "PUT", "admin/settings", map[string]any{"settings": map[string]string{"registration_enabled": "false"}}, cookie, csrf), 200)
	for _, route := range []string{"admin/users/registration-code", "admin/users"} {
		input := body
		if route == "admin/users/registration-code" {
			input = map[string]string{"email": email}
		}
		w = request(t, a, "POST", route, input, cookie, csrf)
		requireStatus(t, w, 403)
		if !strings.Contains(w.Body.String(), "REGISTRATION_DISABLED") {
			t.Fatal("registration-disabled response was not explicit")
		}
	}
	if mail.count != 2 {
		t.Fatal("disabled registration sent an email")
	}
}

func TestAdminRegistrationRequiresValidDeliveredCodeAndPassword(t *testing.T) {
	for _, scenario := range []string{"missing", "wrong", "expired", "delivery-failed", "short-password", "long-password"} {
		t.Run(scenario, func(t *testing.T) {
			a, m, mail := fixture(t)
			_, cookie, csrf := vendorSession(t, a, "admin@example.com", "admin")
			email := scenario + "@example.com"
			body := map[string]string{"email": email, "username": email, "password": "correct-password", "code": "ABC123"}
			if scenario != "missing" {
				mail.fail = scenario == "delivery-failed"
				w := request(t, a, "POST", "admin/users/registration-code", map[string]string{"email": email}, cookie, csrf)
				if mail.fail {
					requireStatus(t, w, 503)
					if a.redis.Exists(context.Background(), codeKey(email, "register")).Val() != 0 {
						t.Fatal("failed email delivery left a valid code")
					}
				} else {
					requireStatus(t, w, 200)
					body["code"] = mail.code(email, "register")
				}
			}
			switch scenario {
			case "wrong":
				body["code"] = "wrong"
			case "expired":
				m.FastForward(601 * time.Second)
			case "short-password":
				body["password"] = "short"
			case "long-password":
				body["password"] = strings.Repeat("密", 25)
			}
			w := request(t, a, "POST", "admin/users", body, cookie, csrf)
			requireStatus(t, w, 400)
			var count int64
			a.store.DB.Model(&core.User{}).Where("email = ?", email).Count(&count)
			if count != 0 || len(w.Result().Cookies()) != 0 {
				t.Fatal("invalid registration created a user or replaced a cookie")
			}
		})
	}
}

func TestAdminRegistrationRechecksActorBeforePersistence(t *testing.T) {
	a, _, _ := fixture(t)
	actor, _, _ := vendorSession(t, a, "operator@example.com", "admin")
	if err := a.store.DB.Model(&core.User{}).Where("id = ?", actor.ID).Updates(map[string]any{"role": "member", "auth_version": actor.AuthVersion + 1}).Error; err != nil {
		t.Fatal(err)
	}
	u := &core.User{Email: "stale@example.com", PasswordHash: "unused-hash", Role: "member", Status: "active", EmailVerifiedAt: time.Now().UTC()}
	err := a.store.CreateMemberByAdmin(context.Background(), actor.ID, actor.AuthVersion, u)
	if err == nil {
		t.Fatal("stale administrator created a user")
	}
	var count int64
	a.store.DB.Model(&core.User{}).Where("email = ?", u.Email).Count(&count)
	if count != 0 {
		t.Fatalf("rejected create persisted %d users", count)
	}
}
