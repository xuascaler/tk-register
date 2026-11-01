package core

import (
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"goapi-tk-register/server/common"
)

type protocolSeed struct {
	code, name, operation, path string
	body, response              any
	parameters                  []ProtocolParameter
}

func parameter(name, kind string, required, secret bool, format any) ProtocolParameter {
	raw, _ := jsonText(format)
	return ProtocolParameter{Name: name, Type: kind, Required: required, Secret: secret, Format: raw}
}

func registrationLocations() []string { return []string{"US", "MY", "ID", "SG", "PH"} }

func protocolSeeds() []protocolSeed {
	params := []ProtocolParameter{
		parameter("plan", "string", true, false, map[string]any{"enum": []string{"claude/cc-max-20x", "claude/cc-max-5x", "claude/cc-pro"}}),
		parameter("account", "string", true, false, map[string]any{"max_length": 254, "no_whitespace": true}),
		parameter("password", "string", true, true, map[string]any{"max_length": 1024}),
		parameter("auth_json", "object", true, true, map[string]any{"accept_json_string": true, "required": []string{"id_token", "access_token", "refresh_token", "last_refresh", "email", "account_uuid", "organization_uuid", "organization_name", "claude_device_ids", "type", "expired", "proxy_url", "verification_status"}}),
		parameter("register_location", "string", true, false, map[string]any{"enum": registrationLocations()}),
		parameter("register_time", "date", true, false, map[string]string{"format": "YYYY-MM-DD"}),
		parameter("plan_start_time", "date", true, false, map[string]string{"format": "YYYY-MM-DD"}),
		parameter("plan_end_time", "date", true, false, map[string]string{"format": "YYYY-MM-DD"}),
		parameter("payment_method", "string", false, false, map[string]any{"enum": []string{"physical_credit_card", "virtual_credit_card", "paypal", "apple_pay", "google_pay", "third_party_payment"}}),
		parameter("register_device", "string", false, false, map[string]any{"enum": []string{"Mac", "Windows", "Linux"}}),
		parameter("balance", "number", false, false, map[string]any{"min": 0, "max": 100}),
		parameter("recharge_time", "date", false, false, map[string]string{"format": "YYYY-MM-DD"}),
		parameter("warranty_period", "number", false, false, map[string]any{"enum": []int{1, 7, 30}}),
		parameter("tags", "array", false, false, map[string]any{"enum": []string{"速刷", "慢蒸"}, "max_items": 1}),
		parameter("remark", "string", false, false, map[string]any{"max_length": 2000}),
	}
	body := map[string]string{}
	for _, p := range params {
		body[p.Name] = "{{input." + p.Name + "}}"
	}
	return []protocolSeed{
		{"submit_account", "Submit account", "push", "/supplier-account/api/openapi/claude/accounts", body, map[string]any{"id": "$.id", "alias": "$.alias", "success": "positive integer id", "state": "accepted", "notes": "Acceptance does not imply that the account is online."}, params},
		{"metrics", "Account metrics", "query", "/supplier-account/api/openapi/metrics/query", map[string]any{"account_ids": []string{"{{submission.remote_id}}"}, "begin_time": "{{input.begin_time}}", "end_time": "{{input.end_time}}", "aggregate": false}, map[string]any{"records": "$.accounts", "match": "id == submission.remote_id", "ignore_fields": []string{"cost"}, "unknown": "null"}, []ProtocolParameter{parameter("begin_time", "datetime", true, false, map[string]any{"format": "YYYY-MM-DD HH:mm:ss", "timezone": "vendor.timezone"}), parameter("end_time", "datetime", true, false, map[string]any{"format": "YYYY-MM-DD HH:mm:ss", "max_span_days": 7})}},
		{"bills", "Account bills", "query", "/supplier-account/api/openapi/bill/query", map[string]any{"account_ids": []string{"{{submission.remote_id}}"}, "start_ds": "{{input.start_ds}}", "end_ds": "{{input.end_ds}}"}, map[string]any{"records": "$.list", "match": "id == submission.remote_id", "currency": "CNY", "range": "$.range", "unknown": "null"}, []ProtocolParameter{parameter("start_ds", "date", true, false, map[string]any{"format": "YYYY-MM-DD", "max_age_days": 90}), parameter("end_ds", "date", true, false, map[string]any{"format": "YYYY-MM-DD"})}},
	}
}
func stringField(input map[string]any, key string, required bool, max int) (string, error) {
	value, present := input[key]
	if !present {
		if required {
			return "", errors.New(key + " is required")
		}
		return "", nil
	}
	s, ok := value.(string)
	if !ok || len(s) > max || strings.ContainsRune(s, 0) || (required && strings.TrimSpace(s) == "") {
		return "", errors.New("invalid " + key)
	}
	return s, nil
}
func enumValue(value string, allowed ...string) bool {
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}
func validDate(value string) bool {
	t, e := time.Parse("2006-01-02", value)
	return e == nil && t.Format("2006-01-02") == value
}
func numberValue(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		if !math.IsNaN(n) && !math.IsInf(n, 0) {
			return n, nil
		}
	case string:
		return strconv.ParseFloat(n, 64)
	}
	return 0, errors.New("invalid number")
}

// ValidateSubmissionInput returns typed, canonical parameters, never inferred defaults.
func ValidateSubmissionInput(input map[string]any) (map[string]any, map[string]any, error) {
	invalid := func(msg string) (map[string]any, map[string]any, error) {
		return nil, nil, vendorError(400, "INVALID_PARAMETERS", msg)
	}
	if input == nil {
		return invalid("Account parameters are required.")
	}
	allowed := map[string]bool{}
	for _, p := range protocolSeeds()[0].parameters {
		allowed[p.Name] = true
	}
	for k := range input {
		if !allowed[k] {
			return invalid("An unsupported account parameter was supplied.")
		}
	}
	out := map[string]any{}
	for _, name := range []string{"plan", "account", "password", "register_location", "register_time", "plan_start_time", "plan_end_time"} {
		max := 254
		if name == "password" {
			max = 1024
		}
		value, e := stringField(input, name, true, max)
		if e != nil {
			return invalid(e.Error())
		}
		out[name] = value
	}
	if !enumValue(out["plan"].(string), "claude/cc-max-20x", "claude/cc-max-5x", "claude/cc-pro") {
		return invalid("Choose a supported Claude plan.")
	}
	if strings.IndexFunc(out["account"].(string), unicode.IsSpace) >= 0 {
		return invalid("The account must not contain whitespace.")
	}
	if !enumValue(out["register_location"].(string), registrationLocations()...) {
		return invalid("Choose a supported registration location: US, MY, ID, SG, or PH.")
	}
	for _, name := range []string{"register_time", "plan_start_time", "plan_end_time"} {
		if !validDate(out[name].(string)) {
			return invalid("Use YYYY-MM-DD for account dates.")
		}
	}
	if out["plan_end_time"].(string) < out["plan_start_time"].(string) {
		return invalid("The plan end date must not be before its start date.")
	}
	authRaw, present := input["auth_json"]
	if !present {
		return invalid("Authentication JSON is required.")
	}
	auth, err := ValidateClaudeAuthJSON(authRaw)
	if err != nil {
		return invalid(err.Error())
	}
	out["auth_json"] = auth
	for _, name := range []string{"payment_method", "register_device", "recharge_time", "remark"} {
		if _, ok := input[name]; !ok {
			continue
		}
		max := 120
		if name == "remark" {
			max = 2000
		}
		value, e := stringField(input, name, false, max)
		if e != nil {
			return invalid(e.Error())
		}
		if value == "" {
			continue
		}
		out[name] = value
	}
	if v, ok := out["payment_method"].(string); ok && !enumValue(v, "physical_credit_card", "virtual_credit_card", "paypal", "apple_pay", "google_pay", "third_party_payment") {
		return invalid("Invalid payment method.")
	}
	if v, ok := out["register_device"].(string); ok && !enumValue(v, "Mac", "Windows", "Linux") {
		return invalid("Invalid registration device.")
	}
	if v, ok := out["recharge_time"].(string); ok && !validDate(v) {
		return invalid("Use YYYY-MM-DD for the recharge date.")
	}
	for _, name := range []string{"balance", "warranty_period"} {
		if value, ok := input[name]; ok {
			n, e := numberValue(value)
			if e != nil || math.IsNaN(n) || math.IsInf(n, 0) {
				return invalid("Invalid " + name + ".")
			}
			if name == "balance" && (n < 0 || n > 100) {
				return invalid("Balance must be between 0 and 100.")
			}
			if name == "warranty_period" && n != 1 && n != 7 && n != 30 {
				return invalid("Warranty period must be 1, 7 or 30 days.")
			}
			out[name] = n
		}
	}
	if value, present := input["tags"]; present {
		tags, ok := value.([]any)
		if !ok || len(tags) > 1 {
			return invalid("Choose at most one account tag.")
		}
		for _, tag := range tags {
			value, ok := tag.(string)
			if !ok || !enumValue(value, "速刷", "慢蒸") {
				return invalid("Invalid account tag.")
			}
		}
		out["tags"] = tags
	}
	safe := map[string]any{}
	for k, v := range out {
		safe[k] = v
	}
	safe["password"] = "[REDACTED]"
	safe["auth_json"] = "[REDACTED]"
	return out, safe, nil
}

type MetricAccount struct {
	ID               string   `json:"id"`
	AID              *string  `json:"aid"`
	AccountAlias     *string  `json:"account_alias"`
	Status           *string  `json:"status"`
	Requests         *float64 `json:"requests"`
	SuccessRate      *float64 `json:"success_rate"`
	PromptTokens     *float64 `json:"prompt_tokens"`
	CompletionTokens *float64 `json:"completion_tokens"`
}
type MetricResult struct {
	BeginTime string         `json:"begin_time"`
	EndTime   string         `json:"end_time"`
	QueriedAt time.Time      `json:"queried_at"`
	Account   *MetricAccount `json:"account"`
}
type BillAccount struct {
	ID                    string   `json:"id"`
	RequestCount          *float64 `json:"request_count"`
	PromptTokens          *float64 `json:"prompt_tokens"`
	CompletionTokens      *float64 `json:"completion_tokens"`
	CacheReadInputTokens  *float64 `json:"cache_read_input_tokens"`
	CacheWriteInputTokens *float64 `json:"cache_write_input_tokens"`
	Cost                  *float64 `json:"cost"`
}
type BillResult struct {
	StartDS   string       `json:"start_ds"`
	EndDS     string       `json:"end_ds"`
	QueriedAt time.Time    `json:"queried_at"`
	Currency  string       `json:"currency"`
	Account   *BillAccount `json:"account"`
}
type MetricsInput struct {
	BeginTime string `json:"begin_time"`
	EndTime   string `json:"end_time"`
}
type BillsInput struct {
	StartDS string `json:"start_ds"`
	EndDS   string `json:"end_ds"`
}

func ValidateMetrics(input MetricsInput, timezone string, now time.Time) error {
	loc, e := time.LoadLocation(timezone)
	if e != nil {
		return e
	}
	begin, e1 := time.ParseInLocation("2006-01-02 15:04:05", input.BeginTime, loc)
	end, e2 := time.ParseInLocation("2006-01-02 15:04:05", input.EndTime, loc)
	if e1 != nil || e2 != nil || !end.After(begin) || end.Sub(begin) > 7*24*time.Hour || end.After(now) {
		return vendorError(400, "INVALID_RANGE", "Choose a past metrics range of up to 7 days in the vendor time zone.")
	}
	return nil
}
func ValidateBills(input BillsInput, timezone string, now time.Time) error {
	loc, e := time.LoadLocation(timezone)
	if e != nil {
		return e
	}
	start, e1 := time.ParseInLocation("2006-01-02", input.StartDS, loc)
	end, e2 := time.ParseInLocation("2006-01-02", input.EndDS, loc)
	today := time.Date(now.In(loc).Year(), now.In(loc).Month(), now.In(loc).Day(), 0, 0, 0, 0, loc)
	if e1 != nil || e2 != nil || end.Before(start) || end.After(today) || start.Before(today.AddDate(0, 0, -90)) {
		return vendorError(400, "INVALID_RANGE", "Choose a billing range within the past 90 days.")
	}
	return nil
}
func remoteID(number json.Number) (string, error) {
	n, e := strconv.ParseUint(string(number), 10, 64)
	if e != nil || n == 0 {
		return "", errors.New("invalid remote ID")
	}
	return idString(n), nil
}
func cleanNumber(n *float64, max float64) *float64 {
	if n == nil || *n < 0 || math.IsInf(*n, 0) || math.IsNaN(*n) || *n > max {
		return nil
	}
	return n
}
func cleanText(s *string, max int) *string {
	if s == nil || len(*s) > max || strings.ContainsAny(*s, "\r\n\x00") {
		return nil
	}
	return s
}
func parseMetrics(raw []byte, remote string, input MetricsInput) (*MetricResult, error) {
	var response struct {
		Accounts []struct {
			ID               json.Number `json:"id"`
			AID              json.Number `json:"aid"`
			Alias            *string     `json:"account_alias"`
			Status           *string     `json:"status"`
			Requests         *float64    `json:"requests"`
			SuccessRate      *float64    `json:"success_rate"`
			PromptTokens     *float64    `json:"prompt_tokens"`
			CompletionTokens *float64    `json:"completion_tokens"`
		} `json:"accounts"`
	}
	if e := common.Unmarshal(raw, &response); e != nil {
		return nil, e
	}
	if response.Accounts == nil {
		return nil, errors.New("missing accounts")
	}
	out := &MetricResult{BeginTime: input.BeginTime, EndTime: input.EndTime, QueriedAt: time.Now().UTC()}
	for _, a := range response.Accounts {
		id, e := remoteID(a.ID)
		if e != nil || id != remote {
			continue
		}
		if out.Account != nil {
			return nil, errors.New("duplicate account result")
		}
		var aid *string
		if value, e := remoteID(a.AID); e == nil {
			aid = &value
		}
		out.Account = &MetricAccount{ID: id, AID: aid, AccountAlias: cleanText(a.Alias, 160), Status: cleanText(a.Status, 80), Requests: cleanNumber(a.Requests, 9e15), SuccessRate: cleanNumber(a.SuccessRate, 100), PromptTokens: cleanNumber(a.PromptTokens, 9e15), CompletionTokens: cleanNumber(a.CompletionTokens, 9e15)}
	}
	return out, nil
}
func parseBills(raw []byte, remote string, input BillsInput) (*BillResult, error) {
	var response struct {
		List []struct {
			ID                    json.Number `json:"id"`
			RequestCount          *float64    `json:"request_count"`
			PromptTokens          *float64    `json:"prompt_tokens"`
			CompletionTokens      *float64    `json:"completion_tokens"`
			CacheReadInputTokens  *float64    `json:"cache_read_input_tokens"`
			CacheWriteInputTokens *float64    `json:"cache_write_input_tokens"`
			Cost                  *float64    `json:"cost"`
		} `json:"list"`
		Range struct {
			StartDS vendorDate `json:"start_ds"`
			EndDS   vendorDate `json:"end_ds"`
		} `json:"range"`
	}
	if e := common.Unmarshal(raw, &response); e != nil {
		return nil, e
	}
	if response.List == nil {
		return nil, errors.New("missing bill list")
	}
	out := &BillResult{StartDS: input.StartDS, EndDS: input.EndDS, QueriedAt: time.Now().UTC(), Currency: "CNY"}
	for _, pair := range []struct {
		raw    vendorDate
		target *string
	}{{response.Range.StartDS, &out.StartDS}, {response.Range.EndDS, &out.EndDS}} {
		if pair.raw != "" {
			d, e := time.Parse("20060102", string(pair.raw))
			if len(pair.raw) == 10 {
				d, e = time.Parse("2006-01-02", string(pair.raw))
			}
			if e != nil {
				return nil, errors.New("invalid billing range")
			}
			*pair.target = d.Format("2006-01-02")
		}
	}
	for _, a := range response.List {
		id, e := remoteID(a.ID)
		if e != nil || id != remote {
			continue
		}
		if out.Account != nil {
			return nil, errors.New("duplicate bill result")
		}
		out.Account = &BillAccount{ID: id, RequestCount: cleanNumber(a.RequestCount, 9e15), PromptTokens: cleanNumber(a.PromptTokens, 9e15), CompletionTokens: cleanNumber(a.CompletionTokens, 9e15), CacheReadInputTokens: cleanNumber(a.CacheReadInputTokens, 9e15), CacheWriteInputTokens: cleanNumber(a.CacheWriteInputTokens, 9e15), Cost: cleanNumber(a.Cost, 1e12)}
	}
	return out, nil
}

// Vendor billing ranges may be integer YYYYMMDD or ISO date strings.
type vendorDate string

func (d *vendorDate) UnmarshalJSON(raw []byte) error {
	var str string
	if len(raw) > 0 && raw[0] == '"' {
		if e := common.Unmarshal(raw, &str); e != nil {
			return e
		}
	} else {
		var number json.Number
		if e := common.Unmarshal(raw, &number); e != nil {
			return e
		}
		str = string(number)
	}
	*d = vendorDate(str)
	return nil
}

// ValidateClaudeAuthJSON is shared by submission and credential replacement.
// Errors name invalid fields only; credential values are never returned.
func ValidateClaudeAuthJSON(authRaw any) (map[string]any, error) {
	invalid := func(message string) (map[string]any, error) {
		return nil, vendorError(400, "INVALID_AUTH_JSON", message)
	}
	var auth map[string]any
	if str, ok := authRaw.(string); ok {
		if len(str) > 2<<20 || common.Unmarshal([]byte(str), &auth) != nil {
			return invalid("Authentication JSON must be a valid object.")
		}
	} else {
		var ok bool
		auth, ok = authRaw.(map[string]any)
		if !ok {
			return invalid("Authentication JSON must be an object.")
		}
	}
	for _, key := range []string{"id_token", "access_token", "refresh_token", "last_refresh", "email", "account_uuid", "organization_uuid", "organization_name", "type", "expired", "proxy_url", "verification_status"} {
		value, ok := auth[key].(string)
		if !ok || len(value) > 16384 || (key != "id_token" && strings.TrimSpace(value) == "") {
			return invalid("Authentication JSON is missing a required field: " + key + ".")
		}
	}
	devices, ok := auth["claude_device_ids"].([]any)
	if !ok || len(devices) == 0 || len(devices) > 32 {
		return invalid("Authentication JSON must contain claude_device_ids.")
	}
	for _, d := range devices {
		if s, ok := d.(string); !ok || strings.TrimSpace(s) == "" || len(s) > 1024 {
			return invalid("Invalid authentication device ID.")
		}
	}
	for _, key := range []string{"last_refresh", "expired"} {
		if _, e := time.Parse(time.RFC3339, auth[key].(string)); e != nil {
			return invalid("Authentication timestamps must use RFC3339.")
		}
	}
	proxy, e := url.Parse(auth["proxy_url"].(string))
	if e != nil || proxy.Host == "" || !enumValue(proxy.Scheme, "http", "https", "socks5", "socks5h") {
		return invalid("Authentication JSON has an invalid proxy URL.")
	}
	raw, e := common.Marshal(auth)
	if e != nil || len(raw) > 2<<20 {
		return invalid("Authentication JSON is too large.")
	}
	return auth, nil
}
