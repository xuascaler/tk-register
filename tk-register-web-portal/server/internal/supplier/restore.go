package supplier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"goapi-tk-register/server/common"
)

const CodeRestoreTagsUnsupported = "supplier_restore_tags_unsupported"

type RestoreResult struct {
	Found          bool
	Confirmed      bool
	WriteAttempted bool
	RecordID       string
	Alias          string
}

// RestoreAccount follows the supplier edit form, using freshly read metadata.
// It sends one PUT at most. A successful no-match search is the only result
// authorizing the caller to fall back to OpenAPI creation.
func (c *Client) RestoreAccount(ctx context.Context, username, password string, target Target, input map[string]any) (RestoreResult, error) {
	result := RestoreResult{}
	s := c.newSession()
	defer s.http.CloseIdleConnections()
	if err := s.authenticate(ctx, username, password, false); err != nil {
		return result, err
	}
	row, err := s.findAccount(ctx, username, password, target)
	if isSupplierCode(err, CodeAccountNotFound) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	result.Found, result.RecordID, result.Alias = true, row.info.RecordID, row.Alias
	if target.RecordID != "" && target.RecordID != row.info.RecordID {
		return result, planDatesError(CodeAccountIdentityChanged)
	}
	if !row.info.Editable {
		return result, authError(CodeNotEditable)
	}
	body, err := row.restoreBody(input)
	if err != nil {
		return result, err
	}
	passwordValue, ok := input["password"].(string)
	if !ok || passwordValue == "" {
		return result, safeError(CodeContractMismatch)
	}
	encrypted, err := encryptPassword(s.publicKey, passwordValue)
	if err != nil {
		return result, safeError(CodeContractMismatch)
	}
	body["enc_api_key"] = encrypted
	encrypted, err = encryptPassword(s.publicKey, "")
	if err != nil {
		return result, safeError(CodeContractMismatch)
	}
	body["enc_adc_config"] = encrypted
	if target.BeforeUpdate == nil {
		return result, safeError(CodeContractMismatch)
	}
	if err := target.BeforeUpdate(ctx, row.info); err != nil {
		return result, err
	}
	result.WriteAttempted = true
	if err = s.updateRequest(ctx, row.info.RecordID, body); err != nil {
		var problem *Error
		if errors.As(err, &problem) && problem.RequestNotSent {
			result.WriteAttempted = false
		}
		return result, err
	}
	// The supplier deliberately does not echo Auth JSON or passwords. Its PUT
	// acknowledgement confirms saving them; metadata-only reads cannot prove a
	// timed-out credential update succeeded, so uncertain writes stay uncertain.
	result.Confirmed = true
	return result, nil
}

func (r editRecord) restoreBody(input map[string]any) (map[string]any, error) {
	body, err := r.metadataUpdateBody()
	if err != nil {
		return nil, err
	}
	auth, err := common.Marshal(input["auth_json"])
	var object map[string]json.RawMessage
	if err != nil || len(auth) > maxAuthJSONBytes || common.Unmarshal(auth, &object) != nil || len(object) == 0 {
		return nil, authError(CodeInvalidAuthJSON)
	}
	var email string
	if common.Unmarshal(object["email"], &email) != nil || normalizeAccount(email) != normalizeAccount(r.info.Account) {
		return nil, authError(CodeInvalidAuthJSON)
	}
	// The live supplier EditAccountModal (verified 2026-11-05) sends CPA fields
	// and encrypted password, but never cpa_tags on edits. Do not silently ignore
	// requested tag changes or resend masked secrets from the supplier response.
	if tags, present := input["tags"]; present {
		desiredRaw, _ := common.Marshal(tags)
		var desired, current []string
		if common.Unmarshal(desiredRaw, &desired) != nil {
			return nil, safeError(CodeContractMismatch)
		}
		if raw := r.config["tags"]; len(raw) > 0 && string(raw) != "null" {
			if common.Unmarshal(raw, &current) != nil {
				return nil, safeError(CodeContractMismatch)
			}
		}
		if len(desired) != len(current) || len(desired) > 0 && !reflect.DeepEqual(desired, current) {
			return nil, &Error{Code: CodeRestoreTagsUnsupported, Message: "The supplier cannot change tags when restoring an account. Keep its existing tags or omit tags from this submission."}
		}
	}
	for _, key := range []string{"plan", "register_time", "plan_start_time", "plan_end_time", "register_location", "balance", "payment_method", "register_device", "recharge_time", "warranty_period"} {
		if value, present := input[key]; present {
			body["cpa_"+key] = fmt.Sprint(value)
		}
	}
	if remark, present := input["remark"]; present {
		body["remark"] = remark
	}
	body["cpa_auth_json"] = strings.TrimSpace(string(auth))
	body["region"], body["proxy_type"], body["proxy_region"] = r.Region, r.ProxyType, r.ProxyRegion
	return body, nil
}
