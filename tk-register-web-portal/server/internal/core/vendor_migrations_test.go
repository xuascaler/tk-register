package core

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"goapi-tk-register/server/common"
)

func TestRegistrationLocationMigrationPreservesPublishedHistoryAndCredentials(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "locations.sqlite")
	key := strings.Repeat("l", 32)
	store, err := Open("sqlite", dsn, key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	user := User{Email: "migration@example.com", PasswordHash: "test-password-hash", Role: "admin", Status: "active", EmailVerifiedAt: time.Now().UTC()}
	if err = store.CreateUser(context.Background(), &user); err != nil {
		t.Fatal(err)
	}
	var protocol VendorProtocol
	if err = store.DB.Where("code = ?", "submit_account").First(&protocol).Error; err != nil {
		t.Fatal(err)
	}
	var initial ProtocolVersion
	if err = store.DB.Where("protocol_id = ?", protocol.ID).First(&initial).Error; err != nil {
		t.Fatal(err)
	}
	// Build the pre-upgrade database fixture, including non-enum metadata.
	legacyFormat := `{"enum":["US"],"label":"Registration location","opaque":9007199254740993}`
	if err = store.DB.Model(&ProtocolParameter{}).Where("version_id = ? AND name = ?", initial.ID, "register_location").Update("format", legacyFormat).Error; err != nil {
		t.Fatal(err)
	}
	custom := parameter("fixture_parameter", "string", false, true, map[string]any{"description": "preserve existing parameter"})
	custom.VersionID, custom.Position = initial.ID, 99
	if err = store.DB.Create(&custom).Error; err != nil {
		t.Fatal(err)
	}
	token := "migration-test-credential"
	if _, err = store.PatchVendor(context.Background(), protocol.VendorID, user.ID, VendorPatch{AccessToken: &token}); err != nil {
		t.Fatal(err)
	}
	var previous ProtocolVersion
	if err = store.DB.Where("protocol_id = ?", protocol.ID).Order("version DESC").First(&previous).Error; err != nil {
		t.Fatal(err)
	}
	secret, err := store.encrypt("submission-fixture", `{"password":"fixture-secret"}`)
	if err != nil {
		t.Fatal(err)
	}
	remote := "10001"
	submission := Submission{UserID: user.ID, VendorID: protocol.VendorID, ProtocolVersionID: previous.ID, ClientRequestKey: "migration-history-request", RequestHash: "fixture-hash", Account: "fixture@example.com", Plan: "claude/cc-pro", ParametersJSON: `{"register_location":"US"}`, SecretParametersCiphertext: secret, Status: "accepted", RemoteID: &remote}
	if err = store.DB.Create(&submission).Error; err != nil {
		t.Fatal(err)
	}
	execution := ProtocolExecution{SubmissionID: submission.ID, ProtocolVersionID: previous.ID, RequestID: "migration-fixture-execution", Operation: "push", Status: "succeeded", RequestJSON: submission.ParametersJSON}
	if err = store.DB.Create(&execution).Error; err != nil {
		t.Fatal(err)
	}
	var versionsBefore []ProtocolVersion
	var paramsBefore []ProtocolParameter
	var credentialsBefore []VendorCredential
	if err = store.DB.Order("id").Find(&versionsBefore).Error; err != nil {
		t.Fatal(err)
	}
	if err = store.DB.Order("id").Find(&paramsBefore).Error; err != nil {
		t.Fatal(err)
	}
	if err = store.DB.Order("id").Find(&credentialsBefore).Error; err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open("sqlite", dsn, key)
	if err != nil {
		t.Fatal(err)
	}
	var versionsAfter []ProtocolVersion
	var credentialsAfter []VendorCredential
	if err = store.DB.Order("id").Find(&versionsAfter).Error; err != nil {
		t.Fatal(err)
	}
	if len(versionsAfter) != len(versionsBefore)+1 || !reflect.DeepEqual(versionsAfter[:len(versionsBefore)], versionsBefore) {
		t.Fatal("migration changed published versions or non-submission protocols")
	}
	next := versionsAfter[len(versionsAfter)-1]
	if next.ProtocolID != previous.ProtocolID || next.Version != previous.Version+1 || !reflect.DeepEqual(next.CredentialID, previous.CredentialID) || next.RequestDefinition != previous.RequestDefinition || next.ResponseDefinition != previous.ResponseDefinition {
		t.Fatal("migration did not inherit the current protocol/credential definitions")
	}
	if err = store.DB.Order("id").Find(&credentialsAfter).Error; err != nil || !reflect.DeepEqual(credentialsAfter, credentialsBefore) {
		t.Fatal("migration changed encrypted vendor credentials", err)
	}
	var historical []ProtocolParameter
	if err = store.DB.Where("version_id <> ?", next.ID).Order("id").Find(&historical).Error; err != nil || !reflect.DeepEqual(historical, paramsBefore) {
		t.Fatal("migration modified published parameter rows", err)
	}
	var previousParams, nextParams []ProtocolParameter
	if err = store.DB.Where("version_id = ?", previous.ID).Order("position").Find(&previousParams).Error; err != nil {
		t.Fatal(err)
	}
	if err = store.DB.Where("version_id = ?", next.ID).Order("position").Find(&nextParams).Error; err != nil || len(nextParams) != len(previousParams) {
		t.Fatal("migration lost protocol parameters", err)
	}
	for index, param := range nextParams {
		old := previousParams[index]
		if param.Name == "register_location" {
			var format map[string]json.RawMessage
			if err = common.Unmarshal([]byte(param.Format), &format); err != nil {
				t.Fatal(err)
			}
			var values []string
			if err = common.Unmarshal(format["enum"], &values); err != nil || !reflect.DeepEqual(values, registrationLocations()) || string(format["opaque"]) != "9007199254740993" || string(format["label"]) != `"Registration location"` {
				t.Fatal("location enum or additional metadata changed incorrectly", err)
			}
			param.Format = old.Format
		}
		param.ID, param.VersionID = old.ID, old.VersionID
		if !reflect.DeepEqual(param, old) {
			t.Fatal("migration changed parameter flags, position or format", param.Name)
		}
	}
	var savedSubmission Submission
	var savedExecution ProtocolExecution
	if err = store.DB.First(&savedSubmission, submission.ID).Error; err != nil || savedSubmission.ProtocolVersionID != previous.ID || savedSubmission.ParametersJSON != submission.ParametersJSON || savedSubmission.SecretParametersCiphertext != secret || savedSubmission.Status != "accepted" {
		t.Fatal("historical submission was changed", err)
	}
	if err = store.DB.First(&savedExecution, execution.ID).Error; err != nil || savedExecution.ProtocolVersionID != previous.ID || savedExecution.RequestJSON != execution.RequestJSON {
		t.Fatal("historical execution was changed", err)
	}
	if err = store.migrateVendors(); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err = store.DB.Model(&ProtocolVersion{}).Count(&count).Error; err != nil || count != int64(len(versionsAfter)) {
		t.Fatal("migration is not idempotent", err)
	}
	var violations []map[string]any
	if err = store.DB.Raw("PRAGMA foreign_key_check").Scan(&violations).Error; err != nil || len(violations) != 0 {
		t.Fatal("migration violated foreign-key relationships", err)
	}
}
