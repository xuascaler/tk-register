// Package safemessage bounds and redacts text before persistence or display.
package safemessage

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var assignment = regexp.MustCompile(`(?i)((?:["']?(?:access_token|refresh_token|id_token|api_key|authorization|password|passwd|secret|cookie|proxy_url|auth_json)["']?)\s*[:=]\s*)("(?:\\.|[^"\\])*"|'[^']*'|[^\s,;}]+)`)
var bearer = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)
var jwt = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)
var address = regexp.MustCompile(`(?i)(?:https?|socks5h?)://[^\s<>"']+`)

// Known secret leaves are supplied by the caller without exposing the source
// object. Never render this text as HTML; it remains untrusted supplier text.
func Text(value string, secrets ...string) string {
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		for _, variant := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret)} {
			value = strings.ReplaceAll(value, variant, "[redacted]")
		}
	}
	value = bearer.ReplaceAllString(value, "Bearer [redacted]")
	value = assignment.ReplaceAllString(value, "${1}[redacted]")
	value = jwt.ReplaceAllString(value, "[redacted]")
	value = address.ReplaceAllStringFunc(value, func(v string) string {
		u, err := url.Parse(v)
		if err != nil {
			return "[redacted URL]"
		}
		if u.User != nil {
			u.User = url.User("redacted")
		}
		// Query strings may carry credentials under arbitrary names.
		if u.RawQuery != "" {
			u.RawQuery = "redacted"
		}
		u.Fragment = ""
		return u.String()
	})
	value = strings.ReplaceAll(value, "温州润晨", "xxxxx")
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, value)
	value = strings.TrimSpace(value)
	if len(value) > 4096 {
		value = value[:4096]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
		value += "…"
	}
	return value
}
