package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"goapi-tk-register/server/internal/supplier"
	"gorm.io/gorm"
)

// AccountRestorer is shared by browser/API submission. A missing account is a
// successful, complete exact-match search, never an HTTP error or timeout.
type AccountRestorer interface {
	RestoreAccount(context.Context, string, string, supplier.Target, map[string]any) (supplier.RestoreResult, error)
}

// The caller holds the vendor row lock used by deletion. Old owners' tombstones
// remain intact: the new accepted submission belongs solely to the new submitter.
func selectSubmissionRoute(tx *gorm.DB, record *Submission) error {
	var archive DeletedAccount
	err := tx.Where("vendor_id = ? AND LOWER(TRIM(account_email)) = ?", record.VendorID, NormalizeEmail(record.Account)).Order("deleted_at DESC,id DESC").First(&archive).Error
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := rejectLiveRestoreConflict(tx, record.VendorID, record.Account, 0); err != nil {
		return err
	}
	record.RestoredFromDeletedID = &archive.ID
	record.SubmitMode = "restore_update"
	record.TargetSupplierRecordID = archive.SupplierRecordID
	return nil
}

func rejectLiveRestoreConflict(tx *gorm.DB, vendorID uint64, account string, excluding uint64) error {
	var count int64
	err := tx.Model(&Submission{}).Where("vendor_id = ? AND LOWER(TRIM(account)) = ? AND id <> ?", vendorID, NormalizeEmail(account), excluding).
		Where("NOT EXISTS (SELECT 1 FROM synced_accounts AS archived WHERE archived.user_id = submissions.user_id AND archived.vendor_id = submissions.vendor_id AND LOWER(TRIM(archived.account)) = LOWER(TRIM(submissions.account)) AND submissions.id <= archived.deleted_through_submission_id)").
		Where("status = ? OR (status = ? AND restored_from_deleted_id IS NOT NULL) OR (status = ? AND remote_id IS NOT NULL AND remote_id <> '')", "running", "unknown", "accepted").Count(&count).Error
	if err != nil {
		return err
	}
	if count != 0 {
		return vendorError(409, "ACCOUNT_ALREADY_SUBMITTED", "This account is already accepted, running, or awaiting confirmation. Check the existing submission before trying again.")
	}
	return nil
}

func (v *VendorService) executeRestoredSubmission(vendor Vendor, version ProtocolVersion, record *Submission, execution ProtocolExecution, input map[string]any, token string) executionResult {
	fail := func(code, message string, status int) executionResult {
		return executionResult{status: "failed", code: code, message: message, gatewayStatus: status}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if _, err := ValidateClaudeAccountAuthJSON(input["auth_json"], record.Account); err != nil {
		return fail("AUTH_JSON_EMAIL_MISMATCH", "The Auth JSON email must match the submitted account.", 400)
	}
	settings, err := v.Store.Settings(ctx)
	if err != nil {
		return fail("RESTORE_UNAVAILABLE", "Could not read the supplier configuration.", 503)
	}
	username, password := settings.String("account_sync_website_username"), settings.String("account_sync_website_password")
	if username == "" || password == "" {
		return fail("RESTORE_NOT_CONFIGURED", "Configure supplier website login before resubmitting a deleted account.", 409)
	}
	restorer := v.Restorer
	if restorer == nil {
		restorer = supplier.NewClient()
	}
	target := supplier.Target{Account: record.Account, RecordID: record.TargetSupplierRecordID}
	target.BeforeUpdate = func(check context.Context, info supplier.EditInfo) error {
		if NormalizeEmail(info.Account) != NormalizeEmail(record.Account) || !validSupplierRecordID(info.RecordID) {
			return ErrAccountIdentityChanged
		}
		err := v.Store.DB.WithContext(check).Transaction(func(tx *gorm.DB) error {
			// The durable submission blocks subsequent submissions even if the lease
			// expires or the process dies after sending the PUT.
			if err := rejectLiveRestoreConflict(tx, record.VendorID, record.Account, record.ID); err != nil {
				return err
			}
			var count int64
			if err := tx.Model(&SyncedAccount{}).Where("vendor_id = ? AND deleted_at IS NULL AND current_submission_id IS NOT NULL AND supplier_record_id = ?", record.VendorID, info.RecordID).Count(&count).Error; err != nil {
				return err
			}
			if count != 0 {
				return ErrAccountOwnershipConflict
			}
			result := tx.Model(&Submission{}).Where("id = ? AND status = ?", record.ID, "running").Update("target_supplier_record_id", info.RecordID)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 { // MySQL may report unchanged mappings as zero.
				var saved Submission
				if err := tx.First(&saved, record.ID).Error; err != nil {
					return err
				}
				if saved.Status != "running" || saved.TargetSupplierRecordID != info.RecordID {
					return ErrAccountIdentityChanged
				}
			}
			return nil
		})
		if err == nil {
			record.TargetSupplierRecordID = info.RecordID
		}
		return err
	}
	restored, err := restorer.RestoreAccount(ctx, username, password, target, input)
	if err != nil {
		var problem *supplier.Error
		code, message := "RESTORE_FAILED", "The account could not be restored. No new account was created."
		if errors.As(err, &problem) {
			code, message = problem.Code, problem.Message
		}
		if restored.WriteAttempted && !(problem != nil && (problem.RequestNotSent || problem.Code == supplier.CodeUpdateRejected)) {
			return executionResult{status: "unknown", code: code, message: "The supplier update result is unconfirmed. Do not resubmit; an administrator must verify the update before another attempt.", gatewayStatus: http.StatusBadGateway}
		}
		status := http.StatusBadGateway
		if problem != nil && (problem.Code == supplier.CodeNotEditable || problem.Code == supplier.CodeRestoreTagsUnsupported || problem.Code == supplier.CodeAccountIdentityChanged || problem.Code == supplier.CodeUpdateRejected || problem.Code == supplier.CodeAccountAmbiguous) {
			status = http.StatusConflict
		}
		return fail(code, message, status)
	}
	if !restored.Found {
		// Only a fully validated no-match search reaches this fallback. Persist the
		// decision before POST; a replay never reruns routing or sends another write.
		r := v.Store.DB.WithContext(ctx).Model(&Submission{}).Where("id = ? AND status = ?", record.ID, "running").Updates(map[string]any{"submit_mode": "create_fallback", "target_supplier_record_id": ""})
		if r.Error != nil || r.RowsAffected != 1 {
			return fail("RESTORE_UNAVAILABLE", "Could not save the submission route.", 503)
		}
		record.SubmitMode, record.TargetSupplierRecordID = "create_fallback", ""
		return v.execute(vendor, version, execution.RequestID, input, nil, token)
	}
	if !restored.Confirmed || !validSupplierRecordID(restored.RecordID) {
		return executionResult{status: "unknown", code: "RESTORE_UNCONFIRMED", message: "The supplier update could not be confirmed. Do not resubmit automatically.", gatewayStatus: 502}
	}
	record.TargetSupplierRecordID = restored.RecordID
	id, alias := restored.RecordID, restored.Alias
	if alias == "" {
		var archived DeletedAccount
		if v.Store.DB.WithContext(ctx).First(&archived, *record.RestoredFromDeletedID).Error == nil && archived.SupplierRecordID == id && archived.SubmissionID != nil {
			var old Submission
			if v.Store.DB.WithContext(ctx).Select("remote_alias").First(&old, *archived.SubmissionID).Error == nil && old.RemoteAlias != nil {
				alias = *old.RemoteAlias
			}
		}
	}
	status := 200
	// The website PUT returns an acknowledgement, not the OpenAPI creation body.
	// Return compatible IDs and explicitly identify this as a restore operation.
	data := map[string]any{"id": json.Number(id), "alias": alias, "submission_mode": "restore_update"}
	raw, _ := json.Marshal(data)
	return executionResult{status: "succeeded", httpStatus: &status, remoteID: &id, alias: cleanText(&alias, 160), data: data, message: "The vendor updated the existing account. The account has been restored; status collection is queued.", completeResponseBody: raw, contentType: "application/json"}
}

// Archived submissions remain readable as history, but cannot query fresh
// supplier data after the same remote account has been assigned to a new owner.
func (s *Store) checkSubmissionNotArchived(ctx context.Context, record Submission) error {
	var count int64
	if err := s.DB.WithContext(ctx).Model(&SyncedAccount{}).Where("user_id = ? AND vendor_id = ? AND LOWER(TRIM(account)) = ? AND deleted_through_submission_id >= ?", record.UserID, record.VendorID, NormalizeEmail(record.Account), record.ID).Count(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return vendorError(409, "ACCOUNT_ARCHIVED", "This submission has been archived. Its saved history is available, but fresh supplier queries are disabled.")
	}
	return nil
}
