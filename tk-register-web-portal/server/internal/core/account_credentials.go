package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AccountCredential is the last confirmed payload, never a projection of an
// upstream response. Pending payloads are encrypted separately before dispatch.
// Neither this model nor decrypted credentials may be returned by an API.
type AccountCredential struct {
	AccountID          uint64     `gorm:"primaryKey;autoIncrement:false" json:"-"`
	OwnerID            uint64     `gorm:"not null" json:"-"`
	VendorID           uint64     `gorm:"not null" json:"-"`
	IdentityKey        string     `gorm:"size:64;not null" json:"-"`
	SubmissionID       uint64     `gorm:"not null" json:"-"`
	Revision           uint64     `gorm:"not null" json:"-"`
	AuthJSONCiphertext string     `gorm:"size:4194304;not null" json:"-"`
	ConfirmedAt        *time.Time `json:"-"`
	State              string     `gorm:"size:16;not null" json:"-"`
	PendingAuditID     *uint64    `json:"-"`
	PendingCiphertext  string     `gorm:"size:4194304;not null" json:"-"`
	PreviousState      string     `gorm:"size:16;not null" json:"-"`
	CreatedAt          time.Time  `json:"-"`
	UpdatedAt          time.Time  `json:"-"`
}

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

func credentialMatches(row AccountCredential, account SyncedAccount) bool {
	return account.AvailableForMutation() && account.CurrentSubmissionID != nil &&
		row.AccountID == account.ID && row.OwnerID == account.UserID && row.VendorID == account.VendorID &&
		row.IdentityKey == account.IdentityKey && row.SubmissionID == *account.CurrentSubmissionID
}

func credentialEncryptionKey(row AccountCredential) string {
	return "account-credential:" + accountDigest(idString(row.AccountID), idString(row.OwnerID), idString(row.VendorID), row.IdentityKey, idString(row.SubmissionID))
}

func newCredential(account SyncedAccount, revision uint64) AccountCredential {
	return AccountCredential{AccountID: account.ID, OwnerID: account.UserID, VendorID: account.VendorID, IdentityKey: account.IdentityKey, SubmissionID: *account.CurrentSubmissionID, Revision: revision, State: "missing"}
}

func (s *Store) currentCredential(ctx context.Context, target SyncedAccount) (AccountCredential, error) {
	var row AccountCredential
	var current SyncedAccount
	if err := s.DB.WithContext(ctx).First(&current, target.ID).Error; err != nil {
		return row, err
	}
	if !current.AvailableForMutation() || current.IdentityKey != target.IdentityKey || current.UserID != target.UserID || current.CurrentSubmissionID == nil || target.CurrentSubmissionID == nil || *current.CurrentSubmissionID != *target.CurrentSubmissionID {
		return row, ErrAccountIdentityChanged
	}
	err := s.DB.WithContext(ctx).First(&row, "account_id = ?", current.ID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return row, ErrCurrentCredentialMissing
	}
	if err != nil {
		return row, err
	}
	if !credentialMatches(row, current) {
		return row, ErrCurrentCredentialMissing
	}
	if row.State == "pending" || row.State == "unknown" {
		return row, ErrCurrentCredentialUnknown
	}
	if row.State != "ready" || row.AuthJSONCiphertext == "" {
		return row, ErrCurrentCredentialMissing
	}
	return row, nil
}

func (s *Store) credentialDocument(row AccountCredential, email string) (map[string]json.RawMessage, error) {
	plain, err := s.decrypt(credentialEncryptionKey(row), row.AuthJSONCiphertext)
	if err != nil {
		return nil, ErrCurrentCredentialMissing
	}
	// Validate identity/required fields but retain the raw values, including
	// unknown extension fields and large JSON numbers, when replacing proxy_url.
	if _, err = ValidateClaudeAccountAuthJSON(plain, email); err != nil {
		return nil, ErrCurrentCredentialMissing
	}
	var document map[string]json.RawMessage
	if json.Unmarshal([]byte(plain), &document) != nil || document == nil {
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
	row, err := s.currentCredential(ctx, target)
	if err != nil {
		return AccountProxyInfo{}, err
	}
	document, err := s.credentialDocument(row, target.Account)
	if err != nil {
		return AccountProxyInfo{}, err
	}
	var proxy string
	if json.Unmarshal(document["proxy_url"], &proxy) != nil {
		return AccountProxyInfo{}, ErrCurrentCredentialMissing
	}
	return AccountProxyInfo{Revision: idString(row.Revision), ProxyURLMasked: MaskedProxyURL(proxy), UpdatedAt: row.ConfirmedAt}, nil
}

// ReplaceAccountProxy is backend-only. The revision is checked again under the
// account row lock when the mutation audit and encrypted candidate are created.
func (s *Store) ReplaceAccountProxy(ctx context.Context, target SyncedAccount, revision uint64, proxy string) (json.RawMessage, error) {
	row, err := s.currentCredential(ctx, target)
	if err != nil {
		return nil, err
	}
	if revision == 0 || row.Revision != revision {
		return nil, ErrCurrentCredentialChanged
	}
	document, err := s.credentialDocument(row, target.Account)
	if err != nil {
		return nil, err
	}
	document["proxy_url"], err = json.Marshal(proxy)
	if err != nil {
		return nil, err
	}
	return json.Marshal(document)
}

// Recheck just before dispatch, including the accepted submission even when
// the supplier ID (and therefore the existing account identity) did not change.
func (s *Store) CheckPendingCredentialUpdate(ctx context.Context, target SyncedAccount, auditID uint64) error {
	var current SyncedAccount
	if err := s.DB.WithContext(ctx).First(&current, target.ID).Error; err != nil {
		return err
	}
	var row AccountCredential
	if err := s.DB.WithContext(ctx).First(&row, "account_id = ?", target.ID).Error; err != nil {
		return err
	}
	if current.IdentityKey != target.IdentityKey || current.UserID != target.UserID || target.CurrentSubmissionID == nil || row.SubmissionID != *target.CurrentSubmissionID || !credentialMatches(row, current) || row.State != "pending" || row.PendingAuditID == nil || *row.PendingAuditID != auditID {
		return ErrAccountIdentityChanged
	}
	return nil
}

// Called inside the successful submission transaction, after materializing the
// accepted account. A delayed completion must not replace a newer submission.
func (s *Store) saveSubmittedCredentialTx(tx *gorm.DB, submission Submission, input map[string]any, at time.Time) error {
	if !strings.HasPrefix(submission.Plan, "claude/") {
		return nil
	}
	account, err := ensureSyncedAccountForSubmissionTx(tx, submission)
	if err != nil {
		return err
	}
	if !account.AvailableForMutation() || account.CurrentSubmissionID == nil || *account.CurrentSubmissionID != submission.ID {
		return nil
	}
	auth, err := ValidateClaudeAccountAuthJSON(input["auth_json"], account.Account)
	if err != nil {
		return nil
	} // Legacy/non-Claude contracts do not gain a guessed credential.
	raw, err := json.Marshal(auth)
	if err != nil {
		return err
	}
	var previous AccountCredential
	err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&previous, "account_id = ?", account.ID).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err == nil && credentialMatches(previous, account) {
		return nil
	}
	row := newCredential(account, previous.Revision+1)
	row.State, row.ConfirmedAt = "ready", &at
	row.AuthJSONCiphertext, err = s.encrypt(credentialEncryptionKey(row), string(raw))
	if err != nil {
		return err
	}
	return tx.Save(&row).Error
}

// Backfill only accepted current submissions with no successful/uncertain
// credential replacement after them. Reruns never overwrite a tracked version.
func (s *Store) InitializeAccountCredentials(ctx context.Context) error {
	var accounts []SyncedAccount
	return s.DB.WithContext(ctx).
		Where("deleted_at IS NULL AND deletion_status = ? AND current_submission_id IS NOT NULL AND plan LIKE ?", "", "claude/%").
		Where(`NOT EXISTS (SELECT 1 FROM account_credentials c WHERE c.account_id = synced_accounts.id
			AND c.owner_id = synced_accounts.user_id AND c.vendor_id = synced_accounts.vendor_id
			AND c.identity_key = synced_accounts.identity_key AND c.submission_id = synced_accounts.current_submission_id)`).
		FindInBatches(&accounts, 100, func(_ *gorm.DB, _ int) error {
			for _, candidate := range accounts {
				if err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
					var account SyncedAccount
					if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&account, candidate.ID).Error; err != nil {
						return err
					}
					if !account.AvailableForMutation() || account.CurrentSubmissionID == nil {
						return nil
					}
					var previous AccountCredential
					err := tx.First(&previous, "account_id = ?", account.ID).Error
					if err == nil && credentialMatches(previous, account) {
						return nil
					}
					if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
						return err
					}
					var submission Submission
					if err = tx.First(&submission, *account.CurrentSubmissionID).Error; err != nil {
						return err
					}
					if submission.Status != "accepted" || submission.UserID != account.UserID || submission.VendorID != account.VendorID || NormalizeEmail(submission.Account) != NormalizeEmail(account.Account) {
						return nil
					}
					if account.AuthJSONUpdatedAt != nil && !account.AuthJSONUpdatedAt.Before(submission.CreatedAt) {
						return nil
					}
					var count int64
					if err = tx.Model(&AccountAuthJSONAudit{}).Where("account_id = ? AND identity_key = ? AND status IN ? AND (created_at >= ? OR finished_at >= ?)", account.ID, account.IdentityKey, []string{"updated", "unknown", "pending"}, submission.CreatedAt, submission.CreatedAt).Count(&count).Error; err != nil {
						return err
					}
					if count > 0 || submission.SecretParametersCiphertext == "" {
						return nil
					}
					plain, err := s.decrypt("submission:"+idString(submission.UserID)+":"+submission.ClientRequestKey, submission.SecretParametersCiphertext)
					if err != nil {
						// An unreadable historical submission must not prevent startup
						// or become a guessed current credential. The shortcut will
						// require a complete Auth JSON update for this account.
						return nil
					}
					var input map[string]any
					decoder := json.NewDecoder(strings.NewReader(plain))
					decoder.UseNumber()
					if decoder.Decode(&input) != nil {
						return nil
					}
					return s.saveSubmittedCredentialTx(tx, submission, input, submission.CreatedAt)
				}); err != nil {
					return err
				}
			}
			return nil
		}).Error
}

// prepareCredentialUpdateTx shares the account lock acquired by BeginAuthJSON.
func (s *Store) prepareCredentialUpdateTx(tx *gorm.DB, account SyncedAccount, audit AccountAuthJSONAudit, raw json.RawMessage, expected *uint64) error {
	var row AccountCredential
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, "account_id = ?", account.ID).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if !credentialMatches(row, account) {
		row = newCredential(account, row.Revision)
	}
	if row.State == "pending" && time.Since(row.UpdatedAt) < 90*time.Second {
		return ErrCurrentCredentialRunning
	}
	if expected != nil {
		if row.Revision != *expected {
			return ErrCurrentCredentialChanged
		}
		if row.State == "pending" || row.State == "unknown" {
			return ErrCurrentCredentialUnknown
		}
		if row.State != "ready" || row.AuthJSONCiphertext == "" {
			return ErrCurrentCredentialMissing
		}
	}
	if row.State == "pending" {
		row.State = "unknown"
	}
	row.PreviousState = row.State
	row.Revision++
	row.State, row.PendingAuditID = "pending", &audit.ID
	row.PendingCiphertext, err = s.encrypt(credentialEncryptionKey(row), string(raw))
	if err != nil {
		return err
	}
	return tx.Save(&row).Error
}

func finishCredentialUpdateTx(tx *gorm.DB, account SyncedAccount, audit AccountAuthJSONAudit, status string, current bool, at time.Time) error {
	var row AccountCredential
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, "account_id = ?", account.ID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !current || !credentialMatches(row, account) || row.PendingAuditID == nil || *row.PendingAuditID != audit.ID {
		return nil
	}
	switch status {
	case "updated":
		row.AuthJSONCiphertext, row.ConfirmedAt, row.State = row.PendingCiphertext, &at, "ready"
	case "unknown":
		row.State = "unknown"
		return tx.Save(&row).Error // Retain the encrypted candidate for diagnostics; never reuse it as confirmed.
	default:
		row.State = row.PreviousState
	}
	row.PendingCiphertext, row.PreviousState, row.PendingAuditID = "", "", nil
	return tx.Save(&row).Error
}
