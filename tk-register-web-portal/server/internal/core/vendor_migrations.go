package core

import (
	"encoding/json"
	"slices"

	"goapi-tk-register/server/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Update known older submission enums by publishing a new version. Existing
// submissions keep their original protocol and credential references.
func migrateSubmissionParameters(tx *gorm.DB, vendorID uint64) error {
	var vendor Vendor
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&vendor, vendorID).Error; err != nil {
		return err
	}
	var protocol VendorProtocol
	if err := tx.Where("vendor_id = ? AND code = ?", vendorID, "submit_account").First(&protocol).Error; err != nil {
		return err
	}
	var current ProtocolVersion
	// Use current reads after taking the same vendor lock as credential rotation,
	// even when earlier seed checks established a MySQL repeatable-read snapshot.
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("protocol_id = ?", protocol.ID).Order("version DESC").First(&current).Error; err != nil {
		return err
	}
	var parameters []ProtocolParameter
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("version_id = ?", current.ID).Order("position").Find(&parameters).Error; err != nil {
		return err
	}
	changed := false
	for index, param := range parameters {
		if param.Name != "register_location" && param.Name != "plan" {
			continue
		}
		var format map[string]json.RawMessage
		if err := common.Unmarshal([]byte(param.Format), &format); err != nil {
			return err
		}
		var values []string
		if common.Unmarshal(format["enum"], &values) != nil {
			continue
		}
		var replacement []string
		switch {
		case param.Name == "register_location" && slices.Equal(values, []string{"US"}):
			replacement = registrationLocations()
		case param.Name == "plan" && slices.Equal(values, []string{"claude/cc-max-20x", "claude/cc-max-5x", "claude/cc-pro"}):
			replacement = submissionPlans()
		default:
			continue
		}
		enum, err := common.Marshal(replacement)
		if err != nil {
			return err
		}
		format["enum"] = enum
		updated, err := jsonText(format)
		if err != nil {
			return err
		}
		parameters[index].Format = updated
		changed = true
	}
	if !changed {
		return nil
	}
	next := ProtocolVersion{ProtocolID: current.ProtocolID, Version: current.Version + 1, CredentialID: current.CredentialID, RequestDefinition: current.RequestDefinition, ResponseDefinition: current.ResponseDefinition}
	if err := tx.Create(&next).Error; err != nil {
		return err
	}
	for _, param := range parameters {
		param.ID = 0
		param.VersionID = next.ID
		if err := tx.Create(&param).Error; err != nil {
			return err
		}
	}
	return nil
}
