package supplier

import (
	"context"
	"errors"
	"strings"

	"goapi-tk-register/server/common"
)

const (
	ExpiredPlanDate            = "2020-01-01"
	CodeAccountIdentityChanged = "supplier_account_identity_changed"
	CodePlanDatesLocked        = "supplier_plan_dates_locked"
	CodePlanDatesRejected      = "supplier_plan_dates_rejected"
	CodePlanDatesUnconfirmed   = "supplier_plan_dates_unconfirmed"
	CodePlanDatesNotSent       = "supplier_plan_dates_not_sent"
)

// ExpireInfo is safe to persist in an operation journal. It intentionally does
// not include account credentials, the raw supplier row, or authentication JSON.
type ExpireInfo struct {
	RecordID      string `json:"supplier_record_id"`
	Account       string `json:"account"`
	Plan          string `json:"plan"`
	PlanStartTime string `json:"plan_start_time"`
	PlanEndTime   string `json:"plan_end_time"`
}

// Info describes the first observed row, before changing its dates. An absent
// account is confirmed only by a successful, fully paginated exact-match search.
type ExpireResult struct {
	Found          bool
	Confirmed      bool
	WriteAttempted bool
	Info           ExpireInfo
}

func planDatesError(code string) *Error {
	message := map[string]string{
		CodeAccountIdentityChanged: "The supplier account identity changed. An administrator must check the account mapping.",
		CodePlanDatesLocked:        "The supplier has locked this account. The requested operation cannot be completed.",
		CodePlanDatesRejected:      "The supplier rejected the account change. The account has been retained.",
		CodePlanDatesUnconfirmed:   "The supplier has not confirmed the account change. The account has been retained while its result is checked.",
		CodePlanDatesNotSent:       "The account change could not be sent to the supplier. The account has been retained. Please try again later.",
	}[code]
	return &Error{Code: code, Message: message}
}

func expireInfo(row editRecord) (ExpireInfo, error) {
	info := ExpireInfo{RecordID: row.info.RecordID, Account: row.info.Account, Plan: row.info.Plan}
	for key, dest := range map[string]*string{"plan_start_time": &info.PlanStartTime, "plan_end_time": &info.PlanEndTime} {
		if raw, ok := row.config[key]; ok && string(raw) != "null" {
			if common.Unmarshal(raw, dest) != nil {
				return info, safeError(CodeContractMismatch)
			}
			*dest = strings.TrimSpace(*dest)
		}
	}
	return info, nil
}

func expiredPlan(info ExpireInfo) bool {
	return info.PlanStartTime == ExpiredPlanDate && info.PlanEndTime == ExpiredPlanDate
}

func isSupplierCode(err error, code string) bool {
	var problem *Error
	return errors.As(err, &problem) && problem.Code == code
}

func (s *session) findExpirationAccount(ctx context.Context, username, password string, target Target) (editRecord, error) {
	row, err := s.findAccount(ctx, username, password, target)
	if err != nil {
		return row, err
	}
	// Auth JSON edits may learn a newly resolved ID. A deletion must never move
	// to a replacement supplier row merely because it has the same email.
	if target.RecordID != "" && row.info.RecordID != target.RecordID {
		return row, planDatesError(CodeAccountIdentityChanged)
	}
	return row, nil
}

// ExpireAccount reads current configuration once, sets only the two plan dates,
// and verifies the result. The caller must hold the supplier-account mutation
// lease and pass a durable beforeWrite callback. Once that callback completes,
// a crashed/uncertain operation must resume with allowWrite=false. No mutation
// is automatically replayed, including after an expired session or timeout.
func (c *Client) ExpireAccount(ctx context.Context, username, password string, target Target, allowWrite bool, beforeWrite func(context.Context, ExpireInfo) error) (ExpireResult, error) {
	result := ExpireResult{Info: ExpireInfo{RecordID: target.RecordID, Account: target.Account}}
	s := c.newSession()
	defer s.http.CloseIdleConnections()
	if err := s.authenticate(ctx, username, password, false); err != nil {
		return result, err
	}
	row, err := s.findExpirationAccount(ctx, username, password, target)
	if isSupplierCode(err, CodeAccountNotFound) {
		result.Confirmed = true
		return result, nil
	}
	if err != nil {
		return result, err
	}
	result.Found = true
	result.Info, err = expireInfo(row)
	if err != nil {
		return result, err
	}
	if expiredPlan(result.Info) {
		result.Confirmed = true
		return result, nil
	}
	if !allowWrite {
		return result, nil
	}
	locked, err := optionalInt(row.IsLocked)
	if err != nil || locked == nil {
		return result, safeError(CodeContractMismatch)
	}
	if *locked != 0 {
		return result, planDatesError(CodePlanDatesLocked)
	}
	// Credential-edit eligibility is deliberately not used: the supplier's
	// editor leaves plan dates editable when the Auth JSON control is locked.
	body, err := row.expirationBody()
	if err != nil {
		return result, err
	}
	encrypted, err := encryptPassword(s.publicKey, "")
	if err != nil {
		return result, safeError(CodeContractMismatch)
	}
	body["enc_adc_config"] = encrypted
	if target.BeforeUpdate != nil {
		if err = target.BeforeUpdate(ctx, row.info); err != nil {
			return result, err
		}
	}
	if beforeWrite == nil {
		return result, safeError(CodeContractMismatch)
	}
	if err = beforeWrite(ctx, result.Info); err != nil {
		return result, err
	}
	result.WriteAttempted = true
	if err = s.updateRequest(ctx, row.info.RecordID, body); err != nil {
		var problem *Error
		if errors.As(err, &problem) && problem.RequestNotSent {
			result.WriteAttempted = false
			return result, requestNotSent(planDatesError(CodePlanDatesNotSent))
		}
		switch {
		case isSupplierCode(err, CodeUpdateRejected):
			return result, planDatesError(CodePlanDatesRejected)
		case isSupplierCode(err, CodeUpdateUnconfirmed):
			return result, planDatesError(CodePlanDatesUnconfirmed)
		default:
			return result, err
		}
	}
	// Verify using the newly confirmed row ID, even for legacy unmapped targets.
	verifyTarget := Target{Account: row.info.Account, RecordID: row.info.RecordID}
	verified, err := s.findExpirationAccount(ctx, username, password, verifyTarget)
	if isSupplierCode(err, CodeAccountNotFound) {
		result.Found, result.Confirmed = false, true
		return result, nil
	}
	if err != nil {
		return result, err
	}
	info, err := expireInfo(verified)
	if err != nil {
		return result, err
	}
	result.Confirmed = expiredPlan(info)
	return result, nil
}

func (r editRecord) expirationBody() (map[string]any, error) {
	body, err := r.metadataUpdateBody()
	if err != nil {
		return nil, err
	}
	body["cpa_plan_start_time"], body["cpa_plan_end_time"] = ExpiredPlanDate, ExpiredPlanDate
	body["region"], body["proxy_type"], body["proxy_region"] = r.Region, r.ProxyType, r.ProxyRegion
	// Verified against the supplier's live EditAccountModal bundle on 2026-11-04:
	// fillFrom clears cpaAuthJson and the UI says "留空则不修改（原值不回显）";
	// collect(edit=true) still sends cpa_auth_json:"". Passwords are only sent in
	// enc_api_key when a new password was entered. Reproduce that metadata-only
	// edit contract; never fetch/resend a masked password or stored Auth JSON.
	body["cpa_auth_json"] = ""
	return body, nil
}
