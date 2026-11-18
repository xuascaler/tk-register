package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"goapi-tk-register/server/internal/core"
	"golang.org/x/crypto/bcrypt"
)

func TestDecodeAdminRestrictedInput(t *testing.T) {
	configured := "admin@example.invalid"
	valid := `{"email":"ADMIN@example.invalid","password":"fixture-password"}`
	out, err := decodeAdmin(strings.NewReader(valid), configured)
	if err != nil || out.Email != configured || out.Password != "fixture-password" {
		t.Fatal("valid bootstrap input rejected")
	}
	for name, raw := range map[string]string{
		"other-email": `{"email":"other@example.invalid","password":"fixture-password"}`,
		"duplicate":   `{"email":"admin@example.invalid","email":"other@example.invalid","password":"fixture-password"}`,
		"role":        `{"email":"admin@example.invalid","password":"fixture-password","role":"admin"}`,
		"null":        `{"email":"admin@example.invalid","password":null}`,
		"short":       `{"email":"admin@example.invalid","password":"short"}`,
		"numeric":     `{"email":"admin@example.invalid","password":12345678}`,
		"trailing":    valid + `{}`,
		"oversized":   strings.Repeat(" ", 4097),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeAdmin(strings.NewReader(raw), configured); err == nil {
				t.Fatal("unsafe bootstrap input accepted")
			}
		})
	}
	long, _ := json.Marshal(map[string]string{"email": configured, "password": strings.Repeat("界", 25)})
	if _, err := decodeAdmin(strings.NewReader(string(long)), configured); err == nil {
		t.Fatal("bcrypt byte limit not enforced")
	}
	if _, err := decodeAdmin(strings.NewReader(valid), ""); err == nil {
		t.Fatal("missing configured administrator allowed")
	}
}

func TestCreateFirstAdministratorPreservesHierarchyAndRejectsExisting(t *testing.T) {
	store, err := core.Open("sqlite", filepath.Join(t.TempDir(), "bootstrap.db"), strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	input := adminInput{Email: "admin@example.invalid", Password: "fixture-password"}
	if err = createFirstAdministrator(context.Background(), store, input); err != nil {
		t.Fatal(err)
	}
	user, err := store.GetUserByEmail(context.Background(), input.Email)
	if err != nil || user.Role != "admin" || user.Status != "active" || user.EmailVerifiedAt.IsZero() || user.ParentUserID != nil {
		t.Fatal("administrator not correctly initialized")
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(input.Password)) != nil {
		t.Fatal("password hash invalid")
	}
	savedHash := user.PasswordHash
	if err = createFirstAdministrator(context.Background(), store, adminInput{Email: input.Email, Password: "replacement-password"}); err == nil {
		t.Fatal("existing account was reset")
	}
	user, _ = store.GetUserByEmail(context.Background(), input.Email)
	if user.PasswordHash != savedHash {
		t.Fatal("existing password changed")
	}
	var count int64
	store.DB.Model(&core.User{}).Count(&count)
	if count != 1 {
		t.Fatal("unexpected additional user")
	}
	store.DB.Model(&core.UserHierarchy{}).Where("ancestor_user_id = ? AND descendant_user_id = ? AND depth = 0", user.ID, user.ID).Count(&count)
	if count != 1 {
		t.Fatal("hierarchy self-link missing")
	}
}
