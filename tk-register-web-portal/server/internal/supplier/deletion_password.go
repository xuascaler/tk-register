package supplier

import (
	"context"
	"errors"
)

const (
	CodeDeletionPasswordLocked      = "supplier_deletion_password_locked"
	CodeDeletionPasswordRejected    = "supplier_deletion_password_rejected"
	CodeDeletionPasswordUnconfirmed = "supplier_deletion_password_unconfirmed"
	CodeDeletionPasswordNotSent     = "supplier_deletion_password_not_sent"
	deletionReplacementPassword     = "qazwsxedc****"
)

func deletionPasswordError(code string) *Error {
	message := map[string]string{
		CodeDeletionPasswordLocked:      "The supplier has locked this account. The requested operation cannot be completed.",
		CodeDeletionPasswordRejected:    "The supplier rejected the account change. The account has been retained.",
		CodeDeletionPasswordUnconfirmed: "The supplier has not confirmed the account change. The account has been retained while its result is checked.",
		CodeDeletionPasswordNotSent:     "The account change could not be sent to the supplier. The account has been retained. Please try again later.",
	}[code]
	return &Error{Code: code, Message: message}
}

// ChangePasswordForDeletion changes only the supplier account password. The
// caller holds the account mutation lease and durably records intent in
// beforeWrite before any PUT. An uncertain operation must resume with
// allowWrite=false: the supplier does not echo passwords, so metadata reads
// cannot confirm an uncertain credential change or authorize a replay.
func (c *Client) ChangePasswordForDeletion(ctx context.Context, username, password string, target Target, allowWrite bool, beforeWrite func(context.Context, ExpireInfo) error) (ExpireResult, error) {
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
	if !allowWrite {
		return result, nil
	}
	locked, err := optionalInt(row.IsLocked)
	if err != nil || locked == nil {
		return result, safeError(CodeContractMismatch)
	}
	if *locked != 0 {
		return result, deletionPasswordError(CodeDeletionPasswordLocked)
	}
	// Auth JSON eligibility is separate from password editing. Preserve the
	// current dates and other CPA metadata, even for active or expired accounts.
	body, err := row.metadataUpdateBody()
	if err != nil {
		return result, err
	}
	body["region"], body["proxy_type"], body["proxy_region"] = row.Region, row.ProxyType, row.ProxyRegion
	body["cpa_auth_json"] = "" // The supplier's unchanged-Auth-JSON marker.
	encrypted, err := encryptPassword(s.publicKey, deletionReplacementPassword)
	if err != nil {
		return result, safeError(CodeContractMismatch)
	}
	body["enc_api_key"] = encrypted
	encrypted, err = encryptPassword(s.publicKey, "")
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
	if err = s.updateRequestWithAcknowledgement(ctx, row.info.RecordID, body, true); err != nil {
		var problem *Error
		if errors.As(err, &problem) && problem.RequestNotSent {
			result.WriteAttempted = false
			return result, requestNotSent(deletionPasswordError(CodeDeletionPasswordNotSent))
		}
		switch {
		case isSupplierCode(err, CodeUpdateRejected):
			return result, deletionPasswordError(CodeDeletionPasswordRejected)
		case isSupplierCode(err, CodeUpdateUnconfirmed):
			return result, deletionPasswordError(CodeDeletionPasswordUnconfirmed)
		default:
			return result, err
		}
	}
	// The supplier's successful PUT acknowledgement confirms the saved change.
	// Neither dates, status, nor a masked password can provide this evidence.
	result.Confirmed = true
	return result, nil
}
