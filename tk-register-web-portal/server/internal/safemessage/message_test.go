package safemessage

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRedactionAndBoundedUTF8(t *testing.T) {
	text := Text(`温州润晨 login failed; access_token="unknown-secret"; password: pass123; Authorization: Bearer abc.def; https://user:password@example.com/x?secret=abc known-secret`, "known-secret")
	for _, secret := range []string{"温州润晨", "unknown-secret", "pass123", "abc.def", "user:password", "secret=abc", "known-secret"} {
		if strings.Contains(text, secret) {
			t.Fatalf("secret remained: %s", secret)
		}
	}
	long := Text(strings.Repeat("错", 3000))
	if !utf8.ValidString(long) || len(long) > 4100 || !strings.HasSuffix(long, "…") {
		t.Fatal("bad bound")
	}
}
