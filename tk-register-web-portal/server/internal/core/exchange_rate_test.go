package core

import (
	"context"
	"gorm.io/gorm/clause"
	"testing"
)

func TestExchangeRateDefaultValidationPersistenceAndResponseConsistency(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	rate, err := s.ExchangeRate(ctx)
	if err != nil || rate != 7.2 {
		t.Fatal(rate, err)
	}
	descriptors, err := s.Descriptors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range descriptors {
		if d.Key == exchangeRateSettingKey {
			found = true
			if d.Category != "exchange_rate" || d.Type != "decimal" || d.Value != "7.2" || d.Secret {
				t.Fatal(d)
			}
		}
	}
	if !found {
		t.Fatal("missing editable exchange rate")
	}
	for _, invalid := range []string{"", " ", "0", "-1", "NaN", "Inf", "1e2", "10001", "7.1234567", "7.2 ", ".5"} {
		err := s.PatchSettings(ctx, map[string]string{exchangeRateSettingKey: invalid, "smtp_host": "must-not-be-saved.example.com"}, 1)
		if !IsValidationError(err) {
			t.Fatalf("accepted %q: %v", invalid, err)
		}
	}
	values, err := s.Settings(ctx)
	if err != nil || values.String("smtp_host") != "" {
		t.Fatal("invalid configuration partially persisted", err)
	}
	for _, valid := range []string{"0.000001", "7.123456", "10000", "8"} {
		if err := s.PatchSettings(ctx, map[string]string{exchangeRateSettingKey: valid}, 1); err != nil {
			t.Fatal(err)
		}
	}
	pinned, err := s.WithExchangeRate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PatchSettings(ctx, map[string]string{exchangeRateSettingKey: "7.5"}, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.seedDefaults(); err != nil {
		t.Fatal(err)
	}
	rate, err = s.ExchangeRate(pinned)
	if err != nil || rate != 8 {
		t.Fatal("response changed rate midway", rate, err)
	}
	rate, err = s.ExchangeRate(ctx)
	if err != nil || rate != 7.5 {
		t.Fatal("saved rate overwritten or not applied", rate, err)
	}
	if err := s.DB.Model(&Setting{}).Where(clause.Eq{Column: "key", Value: exchangeRateSettingKey}).Update("value", "invalid").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExchangeRate(ctx); err == nil {
		t.Fatal("invalid configuration silently used a default")
	}
}
