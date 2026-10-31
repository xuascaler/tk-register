// Package supplier reads the supplier website's account list. It deliberately
// does not implement account submission, editing, or any other vendor mutation.
package supplier

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"goapi-tk-register/server/common"
)

const (
	// Origin is fixed: credentials and the TLS exception must never be sent to a
	// user-supplied URL or redirected to a different vendor.
	Origin                 = "https://120.233.254.152:8000"
	apiPrefix              = "/supplier-account/api/supplier"
	pageSize               = 100
	maxPages               = 100
	maxResponseBytes       = 8 << 20
	CodeCredentialsInvalid = "supplier_credentials_invalid"
	CodeSessionExpired     = "supplier_session_expired"
	CodeUnavailable        = "supplier_unavailable"
	CodeContractMismatch   = "supplier_contract_mismatch"
)

// Error contains only fixed, public-safe messages. Upstream response bodies,
// credentials, tokens, and transport errors are never included in its text.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func safeError(code string) *Error {
	messages := map[string]string{
		CodeCredentialsInvalid: "The supplier rejected the username or password.",
		CodeSessionExpired:     "The supplier session expired again after signing in. Please try later.",
		CodeUnavailable:        "The supplier website is unavailable. Please try later.",
		CodeContractMismatch:   "The supplier website returned an unsupported response. Please check the integration.",
	}
	return &Error{Code: code, Message: messages[code]}
}

// Account is an explicit allowlist. In particular, it cannot contain the
// password or authentication JSON included in some supplier account responses.
// All usage metrics are nullable: absent values must not turn into zero usage.
type Account struct {
	ID                string
	PlatformAccountID string
	Account           string
	Plan              string
	Status            *string
	InputTokensTotal  *int64
	OutputTokensTotal *int64
	CostTotal         *float64
	Currency          *string
	Usage5HPercent    *float64
	Usage7DPercent    *float64
	Reset5HAt         *time.Time
	Reset7DAt         *time.Time
	SourceUpdatedAt   *time.Time
}

type Client struct{ transport http.RoundTripper }

func NewClient() *Client {
	return &Client{transport: &http.Transport{
		DialContext: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		// This exception was explicitly authorized for this exact supplier. The
		// only destination is the fixed Origin; redirects are rejected below.
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConns:          2,
		MaxIdleConnsPerHost:   2,
		ForceAttemptHTTP2:     true,
	}}
}

// Each call owns a separate short-lived session. Login tokens are neither saved
// on Client nor reused between periodic runs, including TestLogin calls.
type session struct {
	http      *http.Client
	token     string
	remaining int
}

func (c *Client) newSession() *session {
	transport := c.transport
	if transport == nil {
		transport = NewClient().transport
	}
	return &session{
		http: &http.Client{
			Transport: transport,
			Timeout:   30 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return errors.New("supplier redirects are disabled")
			},
		},
		remaining: maxPages + 5, // two logins, one read retry, bounded pagination
	}
}

func (c *Client) TestLogin(ctx context.Context, username, password string) error {
	s := c.newSession()
	defer s.http.CloseIdleConnections()
	return s.login(ctx, username, password)
}

// Collect logs in afresh, then reads the website account list. A single expired
// session can trigger one fresh login and one retry of the failed read. There
// are no retries of login failures or any mutating vendor endpoints.
func (c *Client) Collect(ctx context.Context, username, password string) ([]Account, error) {
	s := c.newSession()
	defer s.http.CloseIdleConnections()
	if err := s.login(ctx, username, password); err != nil {
		return nil, err
	}
	accounts := make([]Account, 0)
	seen := make(map[string]bool)
	reauthenticated := false
	for page := 1; page <= maxPages; page++ {
		params := url.Values{
			"status": {"all"}, "provider": {"claude"}, "account_type": {"2"},
			"page": {strconv.Itoa(page)}, "page_size": {strconv.Itoa(pageSize)},
		}
		path := apiPrefix + "/accounts?" + params.Encode()
		status, body, err := s.request(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			if reauthenticated {
				return nil, safeError(CodeSessionExpired)
			}
			reauthenticated = true
			if err = s.login(ctx, username, password); err != nil {
				return nil, err
			}
			status, body, err = s.request(ctx, http.MethodGet, path, nil)
			if err != nil {
				return nil, err
			}
			if status == http.StatusUnauthorized || status == http.StatusForbidden {
				return nil, safeError(CodeSessionExpired)
			}
		}
		if err = readStatus(status); err != nil {
			return nil, err
		}
		var result struct {
			List  json.RawMessage `json:"list"`
			Total json.RawMessage `json:"total"`
		}
		if common.Unmarshal(body, &result) != nil {
			return nil, safeError(CodeContractMismatch)
		}
		total, err := optionalInt(result.Total)
		if err != nil || total == nil || *total > maxPages*pageSize || len(result.List) == 0 || strings.TrimSpace(string(result.List)) == "null" {
			return nil, safeError(CodeContractMismatch)
		}
		var rows []rawAccount
		if common.Unmarshal(result.List, &rows) != nil || len(rows) > pageSize {
			return nil, safeError(CodeContractMismatch)
		}
		for _, row := range rows {
			account, err := parseAccount(row)
			if err != nil || seen[account.ID] {
				return nil, safeError(CodeContractMismatch)
			}
			seen[account.ID] = true
			accounts = append(accounts, account)
		}
		if int64(len(accounts)) >= *total {
			return accounts, nil
		}
		if len(rows) == 0 {
			return nil, safeError(CodeContractMismatch)
		}
	}
	return nil, safeError(CodeContractMismatch)
}

func (s *session) request(ctx context.Context, method, path string, payload any) (int, []byte, error) {
	if s.remaining <= 0 || !strings.HasPrefix(path, apiPrefix+"/") {
		return 0, nil, safeError(CodeContractMismatch)
	}
	s.remaining--
	var reader io.Reader
	if payload != nil {
		b, err := common.Marshal(payload)
		if err != nil {
			return 0, nil, safeError(CodeContractMismatch)
		}
		reader = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, Origin+path, reader)
	if err != nil {
		return 0, nil, safeError(CodeContractMismatch)
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	response, err := s.http.Do(req)
	if err != nil {
		return 0, nil, safeError(CodeUnavailable)
	}
	defer response.Body.Close()
	// Error responses are not needed for parsing, and may echo secrets. Do not
	// read, return, or log their bodies at all.
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return 0, nil, safeError(CodeUnavailable)
	}
	if len(body) > maxResponseBytes {
		return 0, nil, safeError(CodeContractMismatch)
	}
	return response.StatusCode, body, nil
}

func readStatus(status int) error {
	if status >= 200 && status < 300 {
		return nil
	}
	if status == 429 || status >= 500 {
		return safeError(CodeUnavailable)
	}
	return safeError(CodeContractMismatch)
}

func loginStatus(status int) error {
	if status == 400 || status == 401 || status == 403 {
		return safeError(CodeCredentialsInvalid)
	}
	return readStatus(status)
}

func (s *session) login(ctx context.Context, username, password string) error {
	username = strings.TrimSpace(username)
	if username == "" || len(username) > 256 || password == "" || len(password) > 4096 {
		return safeError(CodeCredentialsInvalid)
	}
	s.token = ""
	status, body, err := s.request(ctx, http.MethodPost, apiPrefix+"/pubkey", map[string]string{"username": username})
	if err != nil {
		return err
	}
	if err = loginStatus(status); err != nil {
		return err
	}
	var pubkey struct {
		PublicKey string `json:"public_key"`
	}
	if common.Unmarshal(body, &pubkey) != nil {
		return safeError(CodeContractMismatch)
	}
	encrypted, err := encryptPassword(pubkey.PublicKey, password)
	if err != nil {
		return safeError(CodeContractMismatch)
	}
	status, body, err = s.request(ctx, http.MethodPost, apiPrefix+"/login", map[string]string{
		"username": username, "enc_password": encrypted,
	})
	if err != nil {
		return err
	}
	if err = loginStatus(status); err != nil {
		return err
	}
	var result struct {
		Token string `json:"token"`
	}
	if common.Unmarshal(body, &result) != nil || result.Token == "" || len(result.Token) > 16384 || strings.ContainsAny(result.Token, "\r\n \t") {
		return safeError(CodeContractMismatch)
	}
	s.token = result.Token
	return nil
}

// This is the public supplier frontend's hybrid-aesgcm password envelope:
// AES-256-GCM(password), RSA-OAEP-SHA256(AES key), base64(JSON(k,iv,ct)).
func encryptPassword(publicPEM, password string) (string, error) {
	block, _ := pem.Decode([]byte(publicPEM))
	if block == nil {
		return "", safeError(CodeContractMismatch)
	}
	var pub *rsa.PublicKey
	if parsed, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		pub, _ = parsed.(*rsa.PublicKey)
	} else {
		pub, _ = x509.ParsePKCS1PublicKey(block.Bytes)
	}
	if pub == nil || pub.N.BitLen() < 2048 || pub.N.BitLen() > 8192 {
		return "", safeError(CodeContractMismatch)
	}
	key, nonce := make([]byte, 32), make([]byte, 12)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	blockCipher, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(blockCipher)
	if err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nil, nonce, []byte(password), nil)
	encryptedKey, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pub, key, nil)
	if err != nil {
		return "", err
	}
	envelope, err := common.Marshal(map[string]string{
		"__enc__": "hybrid-aesgcm", "k": base64.StdEncoding.EncodeToString(encryptedKey),
		"iv": base64.StdEncoding.EncodeToString(nonce), "ct": base64.StdEncoding.EncodeToString(ciphertext),
	})
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(envelope), nil
}

type rawAccount struct {
	ID         json.RawMessage `json:"id"`
	PlatformID json.RawMessage `json:"a1_id"`
	Status     string          `json:"a1_status"`
	AuthConfig json.RawMessage `json:"auth_config"`
	Extra      json.RawMessage `json:"extra"`
}

// Canonical statuses are the vocabulary used by the account list filter.
// These mappings come from the supplier's public CPA status labels; errors or
// future states without a corresponding canonical meaning remain unknown.
func canonicalStatus(status string) string {
	switch status {
	case "using":
		return "active"
	case "ban":
		return "banned"
	case "offline":
		return "offline"
	case "wait_check", "init", "bound", "cooking", "ready", "re_login", "pending_activation":
		return "pending"
	default:
		return "unknown"
	}
}

func parseAccount(raw rawAccount) (Account, error) {
	var a Account
	a.ID = positiveID(raw.ID)
	a.PlatformAccountID = positiveID(raw.PlatformID)
	var config struct {
		Account string `json:"account"`
		Plan    string `json:"plan"`
	}
	if a.ID == "" || nestedObject(raw.AuthConfig, &config) != nil {
		return a, safeError(CodeContractMismatch)
	}
	a.Account, a.Plan = strings.TrimSpace(config.Account), strings.TrimSpace(config.Plan)
	if a.Account == "" || len(a.Account) > 512 || !strings.HasPrefix(a.Plan, "claude/") || len(a.Plan) > 256 {
		return a, safeError(CodeContractMismatch)
	}
	if status := strings.ToLower(strings.TrimSpace(raw.Status)); status != "" {
		if len(status) > 64 {
			return a, safeError(CodeContractMismatch)
		}
		status = canonicalStatus(status)
		a.Status = &status
	}
	var extra struct {
		Usage *struct {
			Input       json.RawMessage `json:"input_tokens"`
			Output      json.RawMessage `json:"output_tokens"`
			Cost        json.RawMessage `json:"cost"`
			Utilization *struct {
				FiveHour      json.RawMessage `json:"five_hour_used_percent"`
				Week          json.RawMessage `json:"week_used_percent"`
				FiveHourReset json.RawMessage `json:"five_hour_reset_at"`
				WeekReset     json.RawMessage `json:"week_reset_at"`
			} `json:"utilization"`
		} `json:"cpa_usage"`
	}
	if nestedObject(raw.Extra, &extra) != nil {
		return a, safeError(CodeContractMismatch)
	}
	if extra.Usage == nil {
		return a, nil
	}
	u := extra.Usage
	var err error
	if a.InputTokensTotal, err = optionalInt(u.Input); err != nil {
		return a, err
	}
	if a.OutputTokensTotal, err = optionalInt(u.Output); err != nil {
		return a, err
	}
	if a.CostTotal, err = optionalNumber(u.Cost); err != nil {
		return a, err
	}
	if a.CostTotal != nil {
		// The site's ql formatter divides by 1e6 to display CNY. Its USD
		// display is a fixed-rate conversion, which we intentionally omit.
		*a.CostTotal /= 1e6
		currency := "CNY"
		a.Currency = &currency
	}
	if u.Utilization != nil {
		v := u.Utilization
		if a.Usage5HPercent, err = optionalNumber(v.FiveHour); err != nil {
			return a, err
		}
		if a.Usage7DPercent, err = optionalNumber(v.Week); err != nil {
			return a, err
		}
		if a.Reset5HAt, err = optionalUnix(v.FiveHourReset); err != nil {
			return a, err
		}
		if a.Reset7DAt, err = optionalUnix(v.WeekReset); err != nil {
			return a, err
		}
		if a.Usage5HPercent != nil {
			if *a.Usage5HPercent > math.MaxFloat64/100 {
				return a, safeError(CodeContractMismatch)
			}
			*a.Usage5HPercent *= 100
		}
		if a.Usage7DPercent != nil {
			if *a.Usage7DPercent > math.MaxFloat64/100 {
				return a, safeError(CodeContractMismatch)
			}
			*a.Usage7DPercent *= 100
		}
		// The website treats an all-zero utilization block as unavailable.
		// Preserve real 0% when either window has an actual reset or usage.
		if zero(a.Usage5HPercent) && zero(a.Usage7DPercent) && a.Reset5HAt == nil && a.Reset7DAt == nil {
			a.Usage5HPercent, a.Usage7DPercent = nil, nil
		}
	}
	// row.updated_at is account metadata time, not a documented usage sample
	// time. Do not misrepresent it as SourceUpdatedAt.
	return a, nil
}

func nestedObject(raw json.RawMessage, result any) error {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if raw[0] == '"' {
		var encoded string
		if common.Unmarshal(raw, &encoded) != nil {
			return safeError(CodeContractMismatch)
		}
		raw = json.RawMessage(strings.TrimSpace(encoded))
		if len(raw) == 0 || string(raw) == "null" {
			return nil
		}
	}
	if raw[0] != '{' || common.Unmarshal(raw, result) != nil {
		return safeError(CodeContractMismatch)
	}
	return nil
}

func numericText(raw json.RawMessage) (string, error) {
	v := strings.TrimSpace(string(raw))
	if v == "" || v == "null" {
		return "", nil
	}
	if v[0] == '"' {
		var value string
		if common.Unmarshal(raw, &value) != nil {
			return "", safeError(CodeContractMismatch)
		}
		return strings.TrimSpace(value), nil
	}
	return v, nil
}

func optionalNumber(raw json.RawMessage) (*float64, error) {
	v, err := numericText(raw)
	if err != nil || v == "" {
		return nil, err
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
		return nil, safeError(CodeContractMismatch)
	}
	return &n, nil
}

func optionalInt(raw json.RawMessage) (*int64, error) {
	v, err := numericText(raw)
	if err != nil || v == "" {
		return nil, err
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return nil, safeError(CodeContractMismatch)
	}
	return &n, nil
}

func optionalUnix(raw json.RawMessage) (*time.Time, error) {
	n, err := optionalInt(raw)
	if err != nil || n == nil || *n == 0 {
		return nil, err
	}
	if *n > 253402300799 {
		return nil, safeError(CodeContractMismatch)
	}
	t := time.Unix(*n, 0).UTC()
	return &t, nil
}

func positiveID(raw json.RawMessage) string {
	n, err := optionalInt(raw)
	if err != nil || n == nil || *n == 0 {
		return ""
	}
	return strconv.FormatInt(*n, 10)
}

func zero(v *float64) bool { return v == nil || *v == 0 }
