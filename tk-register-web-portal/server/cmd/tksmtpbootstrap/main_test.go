package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestSMTPPayloadBoundary(t *testing.T) {
	values := map[string]string{
		"smtp_host": "smtp.example.invalid", "smtp_port": "587", "smtp_tls_mode": "starttls",
		"smtp_username": "fixture-user", "smtp_password": "fixture-secret",
		"smtp_from_name": "TK Register", "smtp_from_email": "noreply@example.invalid",
		"smtp_force_auth_login": "false", "smtp_timeout_seconds": "15",
	}
	valid, _ := json.Marshal(values)
	if _, err := decodeSMTP(strings.NewReader(string(valid))); err != nil {
		t.Fatal("valid SMTP payload rejected")
	}
	for name, payload := range map[string]string{
		"unknown":    strings.TrimSuffix(string(valid), "}") + `,"users":[]}`,
		"duplicate":  strings.TrimSuffix(string(valid), "}") + `,"smtp_host":"other.invalid"}`,
		"non-string": strings.Replace(string(valid), `"587"`, `587`, 1),
		"null":       strings.Replace(string(valid), `"fixture-secret"`, `null`, 1),
		"trailing":   string(valid) + `{}`,
		"missing":    `{}`, "array": `[]`, "oversized": strings.Repeat(" ", 65537),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeSMTP(strings.NewReader(payload)); err == nil {
				t.Fatal("unsafe payload accepted")
			}
		})
	}
}

func TestDecryptMatchesSettingFormatAndAuthenticatesContext(t *testing.T) {
	key := strings.Repeat("k", 32)
	digest := sha256.Sum256([]byte(key))
	block, _ := aes.NewCipher(digest[:])
	aead, _ := cipher.NewGCM(block)
	nonce := make([]byte, aead.NonceSize())
	raw := aead.Seal(nonce, nonce, []byte("fixture-secret"), []byte("tk-register:setting:smtp_password"))
	value := "enc:v1:" + base64.RawStdEncoding.EncodeToString(raw)
	plain, err := decryptSetting(key, "smtp_password", value)
	if err != nil || plain != "fixture-secret" {
		t.Fatal("setting format did not round trip")
	}
	for name, args := range map[string][3]string{
		"wrong-key": {strings.Repeat("x", 32), "smtp_password", value},
		"wrong-aad": {key, "other_setting", value},
		"plaintext": {key, "smtp_password", "fixture-secret"},
		"truncated": {key, "smtp_password", "enc:v1:AA"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decryptSetting(args[0], args[1], args[2]); err == nil {
				t.Fatal("invalid ciphertext accepted")
			}
		})
	}
}
