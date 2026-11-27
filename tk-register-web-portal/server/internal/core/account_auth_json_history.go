package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"time"

	"gorm.io/gorm"
)

var (
	ErrCurrentCredentialMissing = errors.New("current account credentials are not saved")
	ErrCurrentCredentialUnknown = errors.New("current account credentials are unconfirmed")
	ErrCurrentCredentialChanged = errors.New("current account credentials changed")
	ErrCurrentCredentialRunning = errors.New("account credentials are being updated")
)

type AccountProxyInfo struct {
	Revision       string     `json:"revision"`
	ProxyURLMasked string     `json:"proxy_url_masked"`
	UpdatedAt      *time.Time `json:"updated_at"`
}

// credentialSnapshot is transient, never a database model or an API response.
// Payloads remain in their original submission/update history records.
type credentialSnapshot struct {
	raw         json.RawMessage
	revision    string
	confirmedAt *time.Time
}

func sameCredentialLifetime(current, target SyncedAccount) bool {
	return current.AvailableForMutation() && current.ID == target.ID &&
		current.UserID == target.UserID && current.VendorID == target.VendorID &&
		current.IdentityKey == target.IdentityKey && current.CurrentSubmissionID != nil &&
		target.CurrentSubmissionID != nil && *current.CurrentSubmissionID == *target.CurrentSubmissionID
}

func historyCredentialRevision(account SyncedAccount, source string, id uint64) string {
	return accountDigest(idString(account.ID), idString(account.UserID), idString(account.VendorID),
		account.IdentityKey, idString(*account.CurrentSubmissionID), source, idString(id))
}

// AAD binds encrypted update payloads to their immutable operation and lifetime.
func authJSONHistoryEncryptionKey(audit AccountAuthJSONAudit) string {
	submissionID := uint64(0)
	if audit.SubmissionID != nil {
		submissionID = *audit.SubmissionID
	}
	return "auth-json-history:" + accountDigest(idString(audit.AccountID), idString(audit.OwnerID),
		idString(audit.VendorID), audit.IdentityKey, idString(submissionID), audit.RequestID)
}

func credentialSubmission(db *gorm.DB, account SyncedAccount) (Submission, error) {
	var sub Submission
	if account.CurrentSubmissionID == nil {
		return sub, ErrCurrentCredentialMissing
	}
	err := db.First(&sub, *account.CurrentSubmissionID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return sub, ErrCurrentCredentialMissing
	}
	if err != nil {
		return sub, err
	}
	if sub.Status != "accepted" || sub.UserID != account.UserID || sub.VendorID != account.VendorID ||
		NormalizeEmail(sub.Account) != NormalizeEmail(account.Account) {
		return sub, ErrCurrentCredentialMissing
	}
	return sub, nil
}

func credentialHistory(db *gorm.DB, account SyncedAccount, sub Submission) *gorm.DB {
	// Old metadata-only audits may not have a submission_id. They must still
	// block fallback if they could have changed this accepted submission.
	return db.Model(&AccountAuthJSONAudit{}).
		Where("account_id = ? AND owner_id = ? AND vendor_id = ? AND identity_key = ?", account.ID, account.UserID, account.VendorID, account.IdentityKey).
		Where("submission_id = ? OR (submission_id IS NULL AND (created_at >= ? OR finished_at >= ?))", sub.ID, sub.CreatedAt, sub.CreatedAt)
}

func (s *Store) historyCredential(db *gorm.DB, account SyncedAccount) (credentialSnapshot, error) {
	var snapshot credentialSnapshot
	sub, err := credentialSubmission(db, account)
	if err != nil {
		return snapshot, err
	}
	var audit AccountAuthJSONAudit
	// A failed attempt must not hide an earlier unknown operation. Pick the
	// latest operation that either succeeded or may have changed the supplier.
	err = credentialHistory(db, account, sub).Where("status NOT IN ?", []string{"failed", "rejected"}).Order("id DESC").Take(&audit).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return snapshot, err
	}
	if err == nil {
		switch audit.Status {
		case "pending", "unknown":
			return snapshot, ErrCurrentCredentialUnknown
		case "updated":
			// Never skip a successful legacy update without a saved payload.
			if audit.AuthJSONCiphertext == nil || *audit.AuthJSONCiphertext == "" || audit.FinishedAt == nil {
				return snapshot, ErrCurrentCredentialMissing
			}
			if account.AuthJSONUpdatedAt != nil && account.AuthJSONUpdatedAt.After(*audit.FinishedAt) {
				return snapshot, ErrCurrentCredentialMissing
			}
			plain, decryptErr := s.decrypt(authJSONHistoryEncryptionKey(audit), *audit.AuthJSONCiphertext)
			if decryptErr != nil {
				return snapshot, ErrCurrentCredentialMissing
			}
			return credentialSnapshot{json.RawMessage(plain), historyCredentialRevision(account, "update", audit.ID), audit.FinishedAt}, nil
		default:
			return snapshot, ErrCurrentCredentialMissing
		}
	}
	confirmedAt := sub.CreatedAt
	if account.AuthJSONUpdatedAt != nil && !account.AuthJSONUpdatedAt.Before(sub.CreatedAt) {
		// Restore submissions perform an Auth JSON update themselves. Only the
		// matching confirmed push can explain this timestamp without an audit.
		if sub.SubmitMode != "restore_update" {
			return snapshot, ErrCurrentCredentialMissing
		}
		var execution ProtocolExecution
		if err := db.Where("submission_id = ? AND operation = ? AND status = ?", sub.ID, "push", "succeeded").Order("id DESC").Take(&execution).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return snapshot, ErrCurrentCredentialMissing
			}
			return snapshot, err
		}
		if execution.FinishedAt == nil || account.AuthJSONUpdatedAt.After(*execution.FinishedAt) {
			return snapshot, ErrCurrentCredentialMissing
		}
		confirmedAt = *execution.FinishedAt
	}
	plain, err := s.decrypt("submission:"+idString(sub.UserID)+":"+sub.ClientRequestKey, sub.SecretParametersCiphertext)
	if err != nil {
		return snapshot, ErrCurrentCredentialMissing
	}
	var params map[string]json.RawMessage
	if json.Unmarshal([]byte(plain), &params) != nil || len(params["auth_json"]) == 0 {
		return snapshot, ErrCurrentCredentialMissing
	}
	raw := params["auth_json"]
	// Older encrypted submissions can contain a JSON string rather than an object.
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		raw = json.RawMessage(encoded)
	}
	return credentialSnapshot{raw, historyCredentialRevision(account, "submission", sub.ID), &confirmedAt}, nil
}

func (s *Store) currentCredential(ctx context.Context, target SyncedAccount) (credentialSnapshot, error) {
	var current SyncedAccount
	if err := s.DB.WithContext(ctx).First(&current, target.ID).Error; err != nil {
		return credentialSnapshot{}, err
	}
	if !sameCredentialLifetime(current, target) {
		return credentialSnapshot{}, ErrAccountIdentityChanged
	}
	return s.historyCredential(s.DB.WithContext(ctx), current)
}

func credentialDocument(snapshot credentialSnapshot, email string) (map[string]json.RawMessage, error) {
	if _, err := ValidateClaudeAccountAuthJSON(string(snapshot.raw), email); err != nil {
		return nil, ErrCurrentCredentialMissing
	}
	// Preserve unknown extension fields and large numbers when replacing proxy_url.
	var document map[string]json.RawMessage
	if json.Unmarshal(snapshot.raw, &document) != nil || document == nil {
		return nil, ErrCurrentCredentialMissing
	}
	return document, nil
}

// MaskedProxyURL never returns userinfo, paths, query strings or fragments.
func MaskedProxyURL(value string) string {
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5" && u.Scheme != "socks5h" {
		return ""
	}
	userinfo := ""
	if u.User != nil {
		userinfo = "••••:••••@"
	}
	return u.Scheme + "://" + userinfo + u.Host
}

func (s *Store) AccountProxyInfo(ctx context.Context, target SyncedAccount) (AccountProxyInfo, error) {
	snapshot, err := s.currentCredential(ctx, target)
	if err != nil {
		return AccountProxyInfo{}, err
	}
	document, err := credentialDocument(snapshot, target.Account)
	if err != nil {
		return AccountProxyInfo{}, err
	}
	var proxy string
	if json.Unmarshal(document["proxy_url"], &proxy) != nil {
		return AccountProxyInfo{}, ErrCurrentCredentialMissing
	}
	return AccountProxyInfo{Revision: snapshot.revision, ProxyURLMasked: MaskedProxyURL(proxy), UpdatedAt: snapshot.confirmedAt}, nil
}

// Revision is an opaque history identity, rechecked under the account lock in BeginAuthJSONUpdateWithMethod.
func (s *Store) ReplaceAccountProxy(ctx context.Context, target SyncedAccount, revision string, proxy string) (json.RawMessage, error) {
	snapshot, err := s.currentCredential(ctx, target)
	if err != nil {
		return nil, err
	}
	if revision == "" || snapshot.revision != revision {
		return nil, ErrCurrentCredentialChanged
	}
	document, err := credentialDocument(snapshot, target.Account)
	if err != nil {
		return nil, err
	}
	document["proxy_url"], err = json.Marshal(proxy)
	if err != nil {
		return nil, err
	}
	return json.Marshal(document)
}

// Called with the existing account row locked, before inserting a new audit.
func (s *Store) prepareAuthJSONHistoryTx(tx *gorm.DB, account SyncedAccount, audit *AccountAuthJSONAudit, credential AuthJSONCredentialWrite) error {
	sub, err := credentialSubmission(tx, account)
	if err != nil {
		return err
	}
	var running int64
	if err := credentialHistory(tx, account, sub).Where("status = ? AND created_at > ?", "pending", time.Now().UTC().Add(-90*time.Second)).Count(&running).Error; err != nil {
		return err
	}
	if running != 0 {
		return ErrCurrentCredentialRunning
	}
	if credential.ExpectedRevision != nil {
		snapshot, err := s.historyCredential(tx, account)
		if err != nil {
			return err
		}
		if snapshot.revision != *credential.ExpectedRevision {
			return ErrCurrentCredentialChanged
		}
	}
	encrypted, err := s.encrypt(authJSONHistoryEncryptionKey(*audit), string(credential.Raw))
	if err != nil {
		return err
	}
	audit.AuthJSONCiphertext = &encrypted
	return nil
}

// Recheck the accepted lifetime and pending operation immediately before dispatch.
func (s *Store) CheckPendingCredentialUpdate(ctx context.Context, target SyncedAccount, auditID uint64) error {
	db := s.DB.WithContext(ctx)
	var current SyncedAccount
	if err := db.First(&current, target.ID).Error; err != nil {
		return err
	}
	if !sameCredentialLifetime(current, target) {
		return ErrAccountIdentityChanged
	}
	sub, err := credentialSubmission(db, current)
	if err != nil {
		return err
	}
	var latest AccountAuthJSONAudit
	if err := credentialHistory(db, current, sub).Order("id DESC").Take(&latest).Error; err != nil {
		return err
	}
	if latest.ID != auditID || latest.Status != "pending" || latest.AuthJSONCiphertext == nil {
		return ErrAccountIdentityChanged
	}
	return nil
}
