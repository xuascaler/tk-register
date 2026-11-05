package supplier

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"goapi-tk-register/server/common"
)

const (
	CodeAccountNotFound   = "supplier_account_not_found"
	CodeAccountAmbiguous  = "supplier_account_ambiguous"
	CodeNotEditable       = "supplier_account_not_editable"
	CodeUpdateRejected    = "supplier_update_rejected"
	CodeUpdateUnconfirmed = "supplier_update_unconfirmed"
	CodeInvalidAuthJSON   = "supplier_invalid_auth_json"
	maxTargetSearchPages  = 10
	maxAuthJSONBytes      = 2 << 20
)

// Target refers to an account whose ownership has already been checked locally.
// RecordID, if present, MUST be a confirmed supplier website row ID, never the
// public platform account ID (a1_id) or an unverified historical remote_id.
type Target struct {
	Account  string
	RecordID string
	// BeforeUpdate checks local ownership against the freshly resolved website
	// row, while the caller still holds its mutation lease. It is never invoked
	// by read-only preflight or collection and receives no supplier credentials.
	BeforeUpdate func(context.Context, EditInfo) error
}

// EditInfo contains no existing credentials or other private supplier fields.
type EditInfo struct {
	RecordID string `json:"supplier_record_id"`
	Account  string `json:"account"`
	Plan     string `json:"plan"`
	Editable bool   `json:"editable"`
	Reason   string `json:"reason"`
}

func authError(code string) *Error {
	messages := map[string]string{
		CodeAccountNotFound:   "The account was not found on the supplier website.",
		CodeAccountAmbiguous:  "More than one supplier record matches this account. An administrator must confirm the account mapping.",
		CodeNotEditable:       "The supplier does not allow Auth JSON updates for this account in its current state.",
		CodeUpdateRejected:    "The supplier rejected the Auth JSON update. Check the account and try again.",
		CodeUpdateUnconfirmed: "The supplier did not confirm whether the Auth JSON update completed. Check the account before trying again.",
		CodeInvalidAuthJSON:   "Auth JSON must be a non-empty JSON object no larger than 2 MB.",
	}
	return &Error{Code: code, Message: messages[code]}
}

type editRecord struct {
	Alias string `json:"alias"`
	rawAccount
	Provider    string          `json:"provider"`
	Model       string          `json:"model"`
	AccountType json.RawMessage `json:"account_type"`
	IsLocked    json.RawMessage `json:"is_locked"`
	Remark      string          `json:"remark"`
	Region      string          `json:"region"`
	ProxyType   string          `json:"proxy_type"`
	ProxyRegion string          `json:"proxy_region"`
	config      map[string]json.RawMessage
	info        EditInfo
}

func normalizeAccount(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

// findAccount uses the website's search endpoint, then performs an exact local
// email comparison. Pagination is restricted to the filtered result set. A
// missing/ignored filter never causes a fallback to the full account list.
func (s *session) findAccount(ctx context.Context, username, password string, target Target) (editRecord, error) {
	return s.findTargetAccount(ctx, username, password, target, true)
}

// Read-only collection needs identity and usage, not editable credentials or
// form metadata. Both paths share the same exact-match search and ambiguity rules.
func (s *session) findTargetAccount(ctx context.Context, username, password string, target Target, editing bool) (editRecord, error) {
	var empty editRecord
	email := strings.TrimSpace(target.Account)
	if email == "" || len(email) > 512 || strings.ContainsAny(email, "\r\n\x00") {
		return empty, safeError(CodeContractMismatch)
	}
	var matches []editRecord
	seen := make(map[string]bool)
	readCount := int64(0)
	for page := 1; page <= maxTargetSearchPages; page++ {
		params := url.Values{
			"status": {"all"}, "provider": {"claude"}, "account_type": {"2"},
			"keyword": {email}, "page": {strconv.Itoa(page)}, "page_size": {strconv.Itoa(pageSize)},
		}
		path := apiPrefix + "/accounts?" + params.Encode()
		status, body, err := s.targetRead(ctx, username, password, path)
		if err != nil {
			return empty, err
		}
		if err = readStatus(status); err != nil {
			return empty, err
		}
		var result struct {
			List  json.RawMessage `json:"list"`
			Total json.RawMessage `json:"total"`
		}
		if common.Unmarshal(body, &result) != nil {
			return empty, safeError(CodeContractMismatch)
		}
		total, err := optionalInt(result.Total)
		if err != nil || total == nil || *total > maxTargetSearchPages*pageSize || len(result.List) == 0 || strings.TrimSpace(string(result.List)) == "null" {
			return empty, safeError(CodeContractMismatch)
		}
		var rows []json.RawMessage
		if common.Unmarshal(result.List, &rows) != nil || len(rows) > pageSize {
			return empty, safeError(CodeContractMismatch)
		}
		for _, raw := range rows {
			// Ignore editable-only fields during reads. Their type or contents
			// must not prevent collecting status/usage for an active account.
			var base struct {
				rawAccount
				Provider    string          `json:"provider"`
				AccountType json.RawMessage `json:"account_type"`
			}
			if common.Unmarshal(raw, &base) != nil {
				return empty, safeError(CodeContractMismatch)
			}
			row := editRecord{rawAccount: base.rawAccount, Provider: base.Provider, AccountType: base.AccountType}
			if editing && common.Unmarshal(raw, &row) != nil {
				return empty, safeError(CodeContractMismatch)
			}
			id := positiveID(row.ID)
			if id == "" || seen[id] {
				return empty, safeError(CodeContractMismatch)
			}
			seen[id] = true
			row.config = make(map[string]json.RawMessage)
			if nestedObject(row.AuthConfig, &row.config) != nil {
				return empty, safeError(CodeContractMismatch)
			}
			var account string
			if common.Unmarshal(row.config["account"], &account) != nil {
				return empty, safeError(CodeContractMismatch)
			}
			if normalizeAccount(account) != normalizeAccount(email) {
				continue
			}
			// Do not trust a server that ignores provider/type filters for writes.
			accountType, e := optionalInt(row.AccountType)
			if row.Provider != "claude" || e != nil || accountType == nil || *accountType != 2 {
				return empty, safeError(CodeContractMismatch)
			}
			var plan string
			if common.Unmarshal(row.config["plan"], &plan) != nil || !strings.HasPrefix(plan, "claude/") || len(plan) > 256 {
				return empty, safeError(CodeContractMismatch)
			}
			row.info = EditInfo{RecordID: id, Account: strings.TrimSpace(account), Plan: plan}
			if editing {
				if err := row.checkEligibility(); err != nil {
					return empty, err
				}
			}
			matches = append(matches, row)
		}
		readCount += int64(len(rows))
		if readCount > *total {
			return empty, safeError(CodeContractMismatch)
		}
		if readCount == *total {
			if len(matches) == 0 {
				return empty, authError(CodeAccountNotFound)
			}
			if len(matches) == 1 {
				return matches[0], nil
			}
			if target.RecordID != "" {
				for _, row := range matches {
					if row.info.RecordID == target.RecordID {
						return row, nil
					}
				}
			}
			return empty, authError(CodeAccountAmbiguous)
		}
		if len(rows) == 0 {
			return empty, safeError(CodeContractMismatch)
		}
	}
	return empty, safeError(CodeContractMismatch)
}

func (r *editRecord) checkEligibility() error {
	locked, err := optionalInt(r.IsLocked)
	if err != nil {
		return err
	}
	if locked != nil && *locked != 0 {
		r.info.Reason = "The supplier has locked this account. Auth JSON cannot be updated."
		return nil
	}
	platformID, err := optionalInt(r.PlatformID)
	if err != nil {
		return err
	}
	status := strings.ToLower(strings.TrimSpace(r.Status))
	if platformID != nil && *platformID > 0 && status != "ban" && status != "offline" && status != "login_failed" {
		r.info.Reason = "Auth JSON cannot be updated while this account is active or in its current supplier state."
		return nil
	}
	r.info.Editable = true
	return nil
}

func (c *Client) CheckAuthJSON(ctx context.Context, username, password string, target Target) (EditInfo, error) {
	s := c.newSession()
	defer s.http.CloseIdleConnections()
	if err := s.authenticate(ctx, username, password, false); err != nil {
		return EditInfo{}, err
	}
	row, err := s.findAccount(ctx, username, password, target)
	return row.info, err
}

// CollectAccount refreshes one searched account, never the full inventory.
func (c *Client) CollectAccount(ctx context.Context, username, password string, target Target) (Account, error) {
	s := c.newSession()
	defer s.http.CloseIdleConnections()
	if err := s.authenticate(ctx, username, password, false); err != nil {
		return Account{}, err
	}
	row, err := s.findTargetAccount(ctx, username, password, target, false)
	if err != nil {
		return Account{}, err
	}
	return parseAccount(row.rawAccount)
}

func (c *Client) UpdateAuthJSON(ctx context.Context, username, password string, target Target, authJSON json.RawMessage) (EditInfo, error) {
	var object map[string]json.RawMessage
	if len(authJSON) > maxAuthJSONBytes || common.Unmarshal(authJSON, &object) != nil || len(object) == 0 {
		return EditInfo{}, authError(CodeInvalidAuthJSON)
	}
	s := c.newSession()
	defer s.http.CloseIdleConnections()
	if err := s.authenticate(ctx, username, password, false); err != nil {
		return EditInfo{}, err
	}
	// Read again when saving: never trust preflight state or initial submission metadata.
	row, err := s.findAccount(ctx, username, password, target)
	if err != nil {
		return EditInfo{}, err
	}
	if !row.info.Editable {
		return row.info, authError(CodeNotEditable)
	}
	if target.BeforeUpdate != nil {
		if err := target.BeforeUpdate(ctx, row.info); err != nil {
			return row.info, err
		}
	}
	body, err := row.updateBody(authJSON)
	if err != nil {
		return row.info, err
	}
	encrypted, err := encryptPassword(s.publicKey, "")
	if err != nil {
		return row.info, safeError(CodeContractMismatch)
	}
	body["enc_adc_config"] = encrypted
	return row.info, s.updateRequest(ctx, row.info.RecordID, body)
}

func (r editRecord) updateBody(authJSON json.RawMessage) (map[string]any, error) {
	body, err := r.metadataUpdateBody()
	if err != nil {
		return nil, err
	}
	body["cpa_auth_json"] = strings.TrimSpace(string(authJSON))
	return body, nil
}

// metadataUpdateBody carries only the supplier's CPA edit-form fields. Existing
// credentials and tags are not reflected into a metadata edit request.
func (r editRecord) metadataUpdateBody() (map[string]any, error) {
	body := map[string]any{
		"provider": "claude", "model": r.Model, "account_type": 2,
		"account_id": r.info.Account, "remark": r.Remark,
		"adc_config": "", "api_key": "", "region": "", "aws_region": "",
		"aws_project_id": "", "aws_auth_type": "arn", "proxy_type": "",
	}
	fields := []string{"plan", "register_time", "plan_start_time", "plan_end_time", "register_location", "balance", "payment_method", "register_device", "recharge_time", "warranty_period"}
	for i, key := range fields {
		value := ""
		raw := r.config[key]
		if len(raw) != 0 && string(raw) != "null" {
			if key == "balance" || key == "warranty_period" {
				var err error
				value, err = numericText(raw)
				if err != nil {
					return nil, err
				}
				if value != "" {
					if _, err = optionalNumber(raw); err != nil {
						return nil, safeError(CodeContractMismatch)
					}
				}
			} else if common.Unmarshal(raw, &value) != nil {
				return nil, safeError(CodeContractMismatch)
			}
		}
		// Required form fields cannot silently be cleared by an incomplete upstream response.
		if i < 5 && strings.TrimSpace(value) == "" {
			return nil, safeError(CodeContractMismatch)
		}
		body["cpa_"+key] = value
	}
	// The supplier edit form omits tags and does not send a password envelope
	// when unchanged. Do not forward existing secrets or unrelated row fields.
	return body, nil
}

func requestNotSent(err *Error) *Error {
	err.RequestNotSent = true
	return err
}

// updateRequest deliberately has no retry, even for expired authentication.
// Failures that could occur after receipt are reported as unconfirmed.
func (s *session) updateRequest(ctx context.Context, id string, payload map[string]any) error {
	if ctx.Err() != nil {
		return requestNotSent(safeError(CodeUnavailable))
	}
	if s.remaining <= 0 || positiveID(json.RawMessage(id)) != id {
		return requestNotSent(safeError(CodeContractMismatch))
	}
	s.remaining--
	encoded, err := common.Marshal(payload)
	if err != nil {
		return requestNotSent(safeError(CodeContractMismatch))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, Origin+apiPrefix+"/accounts/"+id, strings.NewReader(string(encoded)))
	if err != nil {
		return requestNotSent(safeError(CodeContractMismatch))
	}
	// Removing GetBody also prevents HTTP transports from replaying the body.
	req.GetBody = nil
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	response, err := s.http.Do(req)
	if err != nil {
		if response != nil {
			if response.Body != nil {
				response.Body.Close()
			}
			if response.StatusCode >= 300 && response.StatusCode < 400 {
				return authError(CodeUpdateRejected)
			}
		}
		var dial *net.OpError
		var dns *net.DNSError
		if response == nil && (errors.As(err, &dial) && dial.Op == "dial" || errors.As(err, &dns)) {
			return requestNotSent(safeError(CodeUnavailable))
		}
		return authError(CodeUpdateUnconfirmed)
	}
	defer response.Body.Close()
	if s.cache != nil && (response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden) {
		_ = s.invalidateCurrent(ctx) // Never replay a write after invalidating its expired session.
	}
	if response.StatusCode >= 400 && response.StatusCode < 500 {
		return authError(CodeUpdateRejected)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return authError(CodeUpdateUnconfirmed)
	}
	if response.StatusCode == http.StatusNoContent {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return authError(CodeUpdateUnconfirmed)
	}
	var result map[string]json.RawMessage
	if common.Unmarshal(body, &result) != nil || result == nil {
		return authError(CodeUpdateUnconfirmed)
	}
	for _, key := range []string{"success", "ok"} {
		if raw, exists := result[key]; exists {
			var success bool
			if common.Unmarshal(raw, &success) != nil {
				return authError(CodeUpdateUnconfirmed)
			}
			if !success {
				return authError(CodeUpdateRejected)
			}
		}
	}
	if raw, exists := result["error"]; exists && string(raw) != "null" && string(raw) != `""` {
		return authError(CodeUpdateRejected)
	}
	if raw, exists := result["code"]; exists {
		code, e := numericText(raw)
		if e != nil || (code != "0" && code != "200") {
			return authError(CodeUpdateRejected)
		}
	}
	// The supplier website itself treats a successful HTTP JSON response as the
	// saved acknowledgement. This does not assert that the account can log in.
	return nil
}
