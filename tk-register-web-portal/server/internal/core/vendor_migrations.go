package core

import (
	"encoding/json"

	"goapi-tk-register/server/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Expand only the original US-only contract. Publishing a new version preserves
// credentials, historical request definitions and every existing submission.
func migrateRegistrationLocations(tx *gorm.DB, vendorID uint64) error {
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
	locationIndex := -1
	for index, param := range parameters {
		if param.Name != "register_location" {
			continue
		}
		var format map[string]json.RawMessage
		if err := common.Unmarshal([]byte(param.Format), &format); err != nil {
			return err
		}
		var values []string
		if common.Unmarshal(format["enum"], &values) != nil || len(values) != 1 || values[0] != "US" {
			return nil
		}
		locations, err := common.Marshal(registrationLocations())
		if err != nil {
			return err
		}
		format["enum"] = locations
		updated, err := jsonText(format)
		if err != nil {
			return err
		}
		parameters[index].Format = updated
		locationIndex = index
		break
	}
	if locationIndex < 0 {
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
