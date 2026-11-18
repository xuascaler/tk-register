package core

import (
	"context"
	"testing"
)

func TestOverviewModelVisibilitySettingDefaultsAndValidation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	descriptors, err := s.Descriptors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, setting := range descriptors {
		if setting.Key == "user_overview_model_costs_enabled" {
			found = true
			if setting.Category != "overview" || setting.Type != "boolean" || setting.Value != "false" || setting.Secret {
				t.Fatal("incorrect Overview setting descriptor", setting)
			}
		}
	}
	if !found {
		t.Fatal("Overview setting missing")
	}
	if err := s.PatchSettings(ctx, map[string]string{"user_overview_model_costs_enabled": "enabled"}, 0); !IsValidationError(err) {
		t.Fatal("non-boolean setting accepted", err)
	}
	if err := s.PatchSettings(ctx, map[string]string{"user_overview_model_costs_enabled": "true"}, 0); err != nil {
		t.Fatal(err)
	}
	values, err := s.Settings(ctx)
	if err != nil || !values.Bool("user_overview_model_costs_enabled") {
		t.Fatal("setting not saved", values, err)
	}
}
