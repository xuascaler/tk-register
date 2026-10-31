package core

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"goapi-tk-register/server/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const VendorOrigin = "https://120.233.254.152:8000"

// Every relationship uses an explicit foreign key. Published versions are append-only.
type Vendor struct {
	ID        uint64 `gorm:"primaryKey"`
	Code      string `gorm:"size:80;not null;uniqueIndex"`
	Name      string `gorm:"size:120;not null"`
	BaseURL   string `gorm:"size:255;not null"`
	Enabled   bool   `gorm:"not null"`
	Timezone  string `gorm:"size:64;not null"`
	CreatedAt time.Time
	UpdatedAt time.Time
}
type VendorCredential struct {
	ID               uint64 `gorm:"primaryKey"`
	VendorID         uint64 `gorm:"not null;index"`
	Vendor           Vendor `gorm:"foreignKey:VendorID;constraint:OnDelete:RESTRICT"`
	SecretCiphertext string `gorm:"type:text;not null"`
	CreatedBy        uint64 `gorm:"not null"`
	Creator          User   `gorm:"foreignKey:CreatedBy;constraint:OnDelete:RESTRICT"`
	CreatedAt        time.Time
}
type VendorProtocol struct {
	ID        uint64 `gorm:"primaryKey"`
	VendorID  uint64 `gorm:"not null;uniqueIndex:idx_vendor_protocol_code"`
	Vendor    Vendor `gorm:"foreignKey:VendorID;constraint:OnDelete:RESTRICT"`
	Code      string `gorm:"size:64;not null;uniqueIndex:idx_vendor_protocol_code"`
	Name      string `gorm:"size:120;not null"`
	Operation string `gorm:"size:16;not null"`
	CreatedAt time.Time
}
type ProtocolVersion struct {
	ID                 uint64            `gorm:"primaryKey"`
	ProtocolID         uint64            `gorm:"not null;uniqueIndex:idx_protocol_version"`
	Protocol           VendorProtocol    `gorm:"foreignKey:ProtocolID;constraint:OnDelete:RESTRICT"`
	Version            int               `gorm:"not null;uniqueIndex:idx_protocol_version"`
	CredentialID       *uint64           `gorm:"index"`
	Credential         *VendorCredential `gorm:"foreignKey:CredentialID;constraint:OnDelete:RESTRICT"`
	RequestDefinition  string            `gorm:"type:text;not null"`
	ResponseDefinition string            `gorm:"type:text;not null"`
	CreatedAt          time.Time
}
type ProtocolParameter struct {
	ID        uint64          `gorm:"primaryKey"`
	VersionID uint64          `gorm:"not null;uniqueIndex:idx_version_parameter"`
	Version   ProtocolVersion `gorm:"foreignKey:VersionID;constraint:OnDelete:RESTRICT"`
	Name      string          `gorm:"size:80;not null;uniqueIndex:idx_version_parameter"`
	Type      string          `gorm:"size:16;not null"`
	Required  bool            `gorm:"not null"`
	Secret    bool            `gorm:"not null"`
	Format    string          `gorm:"type:text;not null"`
	Position  int             `gorm:"not null"`
}
type Submission struct {
	ID                uint64          `gorm:"primaryKey"`
	UserID            uint64          `gorm:"not null;uniqueIndex:idx_user_request;index"`
	User              User            `gorm:"foreignKey:UserID;constraint:OnDelete:RESTRICT"`
	VendorID          uint64          `gorm:"not null;index"`
	Vendor            Vendor          `gorm:"foreignKey:VendorID;constraint:OnDelete:RESTRICT"`
	ProtocolVersionID uint64          `gorm:"not null;index"`
	ProtocolVersion   ProtocolVersion `gorm:"foreignKey:ProtocolVersionID;constraint:OnDelete:RESTRICT"`
	ClientRequestKey  string          `gorm:"size:80;not null;uniqueIndex:idx_user_request"`
	RequestHash       string          `gorm:"size:64;not null"`
	Source            string          `gorm:"size:16;not null;default:web"`
	APITokenID        *uint64         `gorm:"index"`
	APIToken          *UserAPIToken   `gorm:"foreignKey:APITokenID;constraint:OnDelete:RESTRICT"`
	// A non-null key protects accepted accounts and submissions currently in flight.
	ActiveAccountKey           *string `gorm:"size:64;uniqueIndex"`
	Account                    string  `gorm:"size:254;not null;index"`
	Plan                       string  `gorm:"size:64;not null"`
	ParametersJSON             string  `gorm:"type:text;not null"`
	SecretParametersCiphertext string  `gorm:"size:4194304;not null"`
	Status                     string  `gorm:"size:24;not null;index"`
	RemoteID                   *string `gorm:"size:32"`
	RemoteAlias                *string `gorm:"size:160"`
	RemoteStatus               *string `gorm:"size:80"`
	PlatformAccountID          *string `gorm:"size:32"`
	MetricsJSON                string  `gorm:"type:text"`
	BillsJSON                  string  `gorm:"type:text"`
	LastCheckedAt              *time.Time
	CreatedAt                  time.Time `gorm:"index"`
	UpdatedAt                  time.Time
}
type ProtocolExecution struct {
	ID                uint64          `gorm:"primaryKey"`
	SubmissionID      uint64          `gorm:"not null;index"`
	Submission        Submission      `gorm:"foreignKey:SubmissionID;constraint:OnDelete:RESTRICT"`
	ProtocolVersionID uint64          `gorm:"not null;index"`
	ProtocolVersion   ProtocolVersion `gorm:"foreignKey:ProtocolVersionID;constraint:OnDelete:RESTRICT"`
	RequestID         string          `gorm:"size:64;not null;uniqueIndex"`
	Operation         string          `gorm:"size:24;not null"`
	Status            string          `gorm:"size:24;not null;index"`
	RemoteIDSnapshot  *string         `gorm:"size:32"`
	RequestJSON       string          `gorm:"type:text;not null"`
	ResultJSON        string          `gorm:"type:text"`
	HTTPStatus        *int
	ErrorCode         *string `gorm:"size:80"`
	Message           *string `gorm:"size:255"`
	CreatedAt         time.Time
	FinishedAt        *time.Time
}

type ParameterDTO struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
	Secret   bool   `json:"secret"`
	Format   any    `json:"format"`
}
type ProtocolDTO struct {
	ID         string         `json:"id"`
	VersionID  string         `json:"version_id"`
	Code       string         `json:"code"`
	Name       string         `json:"name"`
	Operation  string         `json:"operation"`
	Version    int            `json:"version"`
	Parameters []ParameterDTO `json:"parameters"`
}
type VendorDTO struct {
	ID                  string        `json:"id"`
	Code                string        `json:"code"`
	Name                string        `json:"name"`
	Enabled             bool          `json:"enabled"`
	Configured          bool          `json:"configured"`
	Timezone            string        `json:"timezone"`
	Protocols           []ProtocolDTO `json:"protocols"`
	CredentialUpdatedAt *time.Time    `json:"credential_updated_at"`
}
type SubmissionDTO struct {
	ID                string         `json:"id"`
	Source            string         `json:"source"`
	VendorID          string         `json:"vendor_id"`
	VendorCode        string         `json:"vendor_code"`
	VendorName        string         `json:"vendor_name"`
	ProtocolVersionID string         `json:"protocol_version_id"`
	Account           string         `json:"account"`
	Plan              string         `json:"plan"`
	Status            string         `json:"status"`
	RemoteID          *string        `json:"remote_id"`
	RemoteAlias       *string        `json:"remote_alias"`
	RemoteStatus      *string        `json:"remote_status"`
	PlatformAccountID *string        `json:"platform_account_id"`
	Parameters        map[string]any `json:"parameters"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
	LastCheckedAt     *time.Time     `json:"last_checked_at"`
	StatusCheckedAt   *time.Time     `json:"status_checked_at"`
}
type ExecutionDTO struct {
	ID              string     `json:"id"`
	RequestID       string     `json:"request_id"`
	Operation       string     `json:"operation"`
	ProtocolName    string     `json:"protocol_name"`
	ProtocolVersion int        `json:"protocol_version"`
	Status          string     `json:"status"`
	HTTPStatus      *int       `json:"http_status"`
	ErrorCode       *string    `json:"error_code"`
	Message         *string    `json:"message"`
	Result          any        `json:"result"`
	CreatedAt       time.Time  `json:"created_at"`
	FinishedAt      *time.Time `json:"finished_at"`
}
type SubmissionDetail struct {
	Submission SubmissionDTO  `json:"submission"`
	Executions []ExecutionDTO `json:"executions"`
	Metrics    *MetricResult  `json:"metrics"`
	Bills      *BillResult    `json:"bills"`
}
type SubmissionPage struct {
	Items    []SubmissionDTO `json:"items"`
	Total    int64           `json:"total"`
	Page     int             `json:"page"`
	PageSize int             `json:"page_size"`
}
type VendorError struct {
	Status        int
	Code, Message string
}

func (e *VendorError) Error() string                     { return e.Message }
func vendorError(status int, code, message string) error { return &VendorError{status, code, message} }
func idString(id uint64) string                          { return strconv.FormatUint(id, 10) }
func ParseBusinessID(v string) (uint64, error) {
	id, e := strconv.ParseUint(v, 10, 64)
	if e != nil || id == 0 || idString(id) != v {
		return 0, vendorError(400, "INVALID_ID", "Invalid record ID.")
	}
	return id, nil
}
func jsonText(v any) (string, error) { b, e := common.Marshal(v); return string(b), e }
func (s *Store) fingerprint(v string) string {
	h := hmac.New(sha256.New, s.macKey)
	_, _ = h.Write([]byte(v))
	return hex.EncodeToString(h.Sum(nil))
}

func (s *Store) migrateVendors() error {
	if e := s.DB.AutoMigrate(&Vendor{}, &VendorCredential{}, &VendorProtocol{}, &ProtocolVersion{}, &ProtocolParameter{}, &Submission{}, &ProtocolExecution{}); e != nil {
		return e
	}
	return s.DB.Transaction(func(tx *gorm.DB) error {
		// Older releases locked unconfirmed submissions indefinitely. Preserve
		// their history but allow a new explicit attempt; accepted/running locks stay.
		if e := tx.Model(&Submission{}).Where("status = ? AND active_account_key IS NOT NULL", "unknown").Update("active_account_key", nil).Error; e != nil {
			return e
		}
		vendor := Vendor{Code: "tk-register-0", Name: "TK Register 0", BaseURL: VendorOrigin, Enabled: true, Timezone: "Asia/Shanghai"}
		if e := tx.Where("code = ?", vendor.Code).FirstOrCreate(&vendor).Error; e != nil {
			return e
		}
		for _, def := range protocolSeeds() {
			p := VendorProtocol{VendorID: vendor.ID, Code: def.code, Name: def.name, Operation: def.operation}
			if e := tx.Where("vendor_id = ? AND code = ?", vendor.ID, p.Code).FirstOrCreate(&p).Error; e != nil {
				return e
			}
			var count int64
			if e := tx.Model(&ProtocolVersion{}).Where("protocol_id = ?", p.ID).Count(&count).Error; e != nil {
				return e
			}
			if count > 0 {
				continue
			}
			req, e := jsonText(map[string]any{"method": "POST", "path": def.path, "content_type": "application/json", "headers": map[string]string{"Authorization": "Bearer {{credentials.access_token}}"}, "body": def.body, "timeout_seconds": 30})
			if e != nil {
				return e
			}
			res, e := jsonText(def.response)
			if e != nil {
				return e
			}
			version := ProtocolVersion{ProtocolID: p.ID, Version: 1, RequestDefinition: req, ResponseDefinition: res}
			if e = tx.Create(&version).Error; e != nil {
				return e
			}
			for i, param := range def.parameters {
				param.VersionID = version.ID
				param.Position = i
				if e = tx.Create(&param).Error; e != nil {
					return e
				}
			}
		}
		return migrateRegistrationLocations(tx, vendor.ID)
	})
}

func (s *Store) Vendors(ctx context.Context) ([]VendorDTO, error) {
	var vendors []Vendor
	if e := s.DB.WithContext(ctx).Order("id").Find(&vendors).Error; e != nil {
		return nil, e
	}
	out := make([]VendorDTO, 0, len(vendors))
	for _, v := range vendors {
		dto, e := s.vendorDTO(ctx, v)
		if e != nil {
			return nil, e
		}
		out = append(out, dto)
	}
	return out, nil
}
func (s *Store) VendorDetails(ctx context.Context, id uint64) (VendorDTO, error) {
	var v Vendor
	e := s.DB.WithContext(ctx).First(&v, id).Error
	if e != nil {
		return VendorDTO{}, e
	}
	return s.vendorDTO(ctx, v)
}
func (s *Store) vendorDTO(ctx context.Context, v Vendor) (VendorDTO, error) {
	out := VendorDTO{ID: idString(v.ID), Code: v.Code, Name: v.Name, Enabled: v.Enabled, Timezone: v.Timezone, Protocols: []ProtocolDTO{}}
	var ps []VendorProtocol
	if e := s.DB.WithContext(ctx).Where("vendor_id = ?", v.ID).Order("id").Find(&ps).Error; e != nil {
		return out, e
	}
	for _, p := range ps {
		var version ProtocolVersion
		if e := s.DB.WithContext(ctx).Where("protocol_id = ?", p.ID).Order("version DESC").First(&version).Error; e != nil {
			return out, e
		}
		dto := ProtocolDTO{ID: idString(p.ID), VersionID: idString(version.ID), Code: p.Code, Name: p.Name, Operation: p.Operation, Version: version.Version, Parameters: []ParameterDTO{}}
		var parameters []ProtocolParameter
		if e := s.DB.WithContext(ctx).Where("version_id = ?", version.ID).Order("position").Find(&parameters).Error; e != nil {
			return out, e
		}
		for _, param := range parameters {
			var format any
			if e := common.Unmarshal([]byte(param.Format), &format); e != nil {
				return out, e
			}
			dto.Parameters = append(dto.Parameters, ParameterDTO{param.Name, param.Type, param.Required, param.Secret, format})
		}
		if version.CredentialID != nil {
			var c VendorCredential
			if e := s.DB.WithContext(ctx).First(&c, *version.CredentialID).Error; e != nil {
				return out, e
			}
			out.Configured = true
			out.CredentialUpdatedAt = &c.CreatedAt
		}
		out.Protocols = append(out.Protocols, dto)
	}
	return out, nil
}

type VendorPatch struct {
	Enabled     *bool   `json:"enabled"`
	AccessToken *string `json:"access_token"`
	Timezone    *string `json:"timezone"`
}

func (s *Store) PatchVendor(ctx context.Context, id, userID uint64, patch VendorPatch) (VendorDTO, error) {
	if patch.Timezone != nil {
		if _, e := time.LoadLocation(*patch.Timezone); e != nil || len(*patch.Timezone) > 64 {
			return VendorDTO{}, vendorError(400, "INVALID_TIMEZONE", "Choose a valid IANA time zone.")
		}
	}
	var token string
	if patch.AccessToken != nil {
		token = strings.TrimSpace(*patch.AccessToken)
		if len(token) > 8192 || strings.ContainsAny(token, "\r\n\x00 \t") {
			return VendorDTO{}, vendorError(400, "INVALID_CREDENTIAL", "The access token is invalid.")
		}
	}
	e := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var v Vendor
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&v, id).Error; e != nil {
			return e
		}
		updates := map[string]any{}
		if patch.Enabled != nil {
			updates["enabled"] = *patch.Enabled
		}
		if patch.Timezone != nil {
			updates["timezone"] = *patch.Timezone
		}
		if len(updates) > 0 {
			if e := tx.Model(&v).Updates(updates).Error; e != nil {
				return e
			}
		}
		if token == "" {
			return nil
		}
		encrypted, e := s.encrypt("vendor:"+idString(id), token)
		if e != nil {
			return e
		}
		c := VendorCredential{VendorID: id, SecretCiphertext: encrypted, CreatedBy: userID}
		if e = tx.Create(&c).Error; e != nil {
			return e
		}
		var ps []VendorProtocol
		if e = tx.Where("vendor_id = ?", id).Find(&ps).Error; e != nil {
			return e
		}
		for _, p := range ps {
			var old ProtocolVersion
			if e = tx.Where("protocol_id = ?", p.ID).Order("version DESC").First(&old).Error; e != nil {
				return e
			}
			next := ProtocolVersion{ProtocolID: p.ID, Version: old.Version + 1, CredentialID: &c.ID, RequestDefinition: old.RequestDefinition, ResponseDefinition: old.ResponseDefinition}
			if e = tx.Create(&next).Error; e != nil {
				return e
			}
			var params []ProtocolParameter
			if e = tx.Where("version_id = ?", old.ID).Find(&params).Error; e != nil {
				return e
			}
			for _, param := range params {
				param.ID = 0
				param.VersionID = next.ID
				if e = tx.Create(&param).Error; e != nil {
					return e
				}
			}
		}
		return nil
	})
	if e != nil {
		return VendorDTO{}, e
	}
	return s.VendorDetails(ctx, id)
}
func (s *Store) submissionDTO(ctx context.Context, record Submission) (SubmissionDTO, error) {
	var v Vendor
	if e := s.DB.WithContext(ctx).First(&v, record.VendorID).Error; e != nil {
		return SubmissionDTO{}, e
	}
	params := map[string]any{}
	if e := common.Unmarshal([]byte(record.ParametersJSON), &params); e != nil {
		return SubmissionDTO{}, e
	}
	var statusCheckedAt *time.Time
	if record.MetricsJSON != "" {
		var metrics MetricResult
		if e := common.Unmarshal([]byte(record.MetricsJSON), &metrics); e != nil {
			return SubmissionDTO{}, e
		}
		if !metrics.QueriedAt.IsZero() {
			checked := metrics.QueriedAt.UTC()
			statusCheckedAt = &checked
		}
	}
	return SubmissionDTO{ID: idString(record.ID), Source: record.Source, VendorID: idString(v.ID), VendorCode: v.Code, VendorName: v.Name, ProtocolVersionID: idString(record.ProtocolVersionID), Account: record.Account, Plan: record.Plan, Status: record.Status, RemoteID: record.RemoteID, RemoteAlias: record.RemoteAlias, RemoteStatus: record.RemoteStatus, PlatformAccountID: record.PlatformAccountID, Parameters: params, CreatedAt: record.CreatedAt.UTC(), UpdatedAt: record.UpdatedAt.UTC(), LastCheckedAt: record.LastCheckedAt, StatusCheckedAt: statusCheckedAt}, nil
}
func (s *Store) OwnedSubmission(ctx context.Context, userID, id uint64) (Submission, error) {
	var record Submission
	e := s.DB.WithContext(ctx).Where("id = ? AND user_id = ?", id, userID).First(&record).Error
	return record, e
}
func (s *Store) SubmissionDetail(ctx context.Context, userID, id uint64) (SubmissionDetail, error) {
	record, e := s.OwnedSubmission(ctx, userID, id)
	if e != nil {
		return SubmissionDetail{}, e
	}
	dto, e := s.submissionDTO(ctx, record)
	if e != nil {
		return SubmissionDetail{}, e
	}
	out := SubmissionDetail{Submission: dto, Executions: []ExecutionDTO{}}
	var executions []ProtocolExecution
	if e = s.DB.WithContext(ctx).Where("submission_id = ?", id).Order("id DESC").Limit(100).Find(&executions).Error; e != nil {
		return out, e
	}
	for _, x := range executions {
		var version ProtocolVersion
		if e = s.DB.WithContext(ctx).Preload("Protocol").First(&version, x.ProtocolVersionID).Error; e != nil {
			return out, e
		}
		var result any
		if x.ResultJSON != "" {
			if e = common.Unmarshal([]byte(x.ResultJSON), &result); e != nil {
				return out, e
			}
		}
		out.Executions = append(out.Executions, ExecutionDTO{ID: idString(x.ID), RequestID: x.RequestID, Operation: x.Operation, ProtocolName: version.Protocol.Name, ProtocolVersion: version.Version, Status: x.Status, HTTPStatus: x.HTTPStatus, ErrorCode: x.ErrorCode, Message: x.Message, Result: result, CreatedAt: x.CreatedAt.UTC(), FinishedAt: x.FinishedAt})
	}
	if record.MetricsJSON != "" {
		if e = common.Unmarshal([]byte(record.MetricsJSON), &out.Metrics); e != nil {
			return out, e
		}
	}
	if record.BillsJSON != "" {
		if e = common.Unmarshal([]byte(record.BillsJSON), &out.Bills); e != nil {
			return out, e
		}
	}
	return out, nil
}
func (s *Store) Submissions(ctx context.Context, userID uint64, page int, search, status string) (SubmissionPage, error) {
	out := SubmissionPage{Items: []SubmissionDTO{}, Page: page, PageSize: 20}
	if page < 1 || page > 100000 || len(search) > 254 {
		return out, vendorError(400, "INVALID_FILTER", "Invalid history filter.")
	}
	if status != "" && status != "running" && status != "accepted" && status != "failed" && status != "unknown" {
		return out, vendorError(400, "INVALID_FILTER", "Invalid submission status.")
	}
	q := s.DB.WithContext(ctx).Model(&Submission{}).Where("user_id = ?", userID)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	if search != "" {
		search = strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(search, "!", "!!"), "%", "!%"), "_", "!_")
		if numericID, e := strconv.ParseUint(search, 10, 64); e == nil && numericID > 0 {
			q = q.Where("(account LIKE ? ESCAPE '!' OR remote_alias LIKE ? ESCAPE '!' OR id = ? OR remote_id = ?)", "%"+search+"%", "%"+search+"%", numericID, search)
		} else {
			q = q.Where("(account LIKE ? ESCAPE '!' OR remote_alias LIKE ? ESCAPE '!')", "%"+search+"%", "%"+search+"%")
		}
	}
	if e := q.Count(&out.Total).Error; e != nil {
		return out, e
	}
	var records []Submission
	if e := q.Order("id DESC").Limit(20).Offset((page - 1) * 20).Find(&records).Error; e != nil {
		return out, e
	}
	for _, r := range records {
		dto, e := s.submissionDTO(ctx, r)
		if e != nil {
			return out, e
		}
		out.Items = append(out.Items, dto)
	}
	return out, nil
}

// Expired running records are uncertain, never automatically put back in a send queue.
func (s *Store) RecoverVendorExecutions(ctx context.Context) error {
	cutoff := time.Now().UTC().Add(-2 * time.Minute)
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Model(&Submission{}).Where("status = ? AND created_at < ?", "running", cutoff).Updates(map[string]any{"status": "unknown", "active_account_key": nil}).Error; e != nil {
			return e
		}
		return tx.Model(&ProtocolExecution{}).Where("status = ? AND created_at < ?", "running", cutoff).Updates(map[string]any{"status": "unknown", "error_code": "EXECUTION_INTERRUPTED", "message": "The service could not confirm the remote result. Do not resubmit this account automatically.", "finished_at": time.Now().UTC()}).Error
	})
}
func (s *Store) validateVendorSecrets() error {
	var cs []VendorCredential
	if e := s.DB.Find(&cs).Error; e != nil {
		return e
	}
	for _, c := range cs {
		if _, e := s.decrypt("vendor:"+idString(c.VendorID), c.SecretCiphertext); e != nil {
			return errors.New("could not decrypt TK vendor credential")
		}
	}
	return nil
}
func credentialKey(vendorID uint64) string { return fmt.Sprintf("vendor:%d", vendorID) }
