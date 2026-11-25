package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"goapi-tk-register/server/internal/core"
	"goapi-tk-register/server/internal/supplier"
)

func TestSupplierAccountIndexPreservesMatchingRules(t *testing.T) {
	str := func(value string) *string { return &value }
	const plan = "claude/cc-max-20x"
	first := supplier.Account{ID: "101", PlatformAccountID: "201", Account: "account@example.com", Plan: plan}
	second := supplier.Account{ID: "102", PlatformAccountID: "202", Account: " ACCOUNT@EXAMPLE.COM ", Plan: plan}
	otherPlan := first
	otherPlan.Plan = "claude/cc-max-5x"
	sameIDs := first
	sameIDs.PlatformAccountID = sameIDs.ID
	collidingID := second
	collidingID.PlatformAccountID = first.ID
	tests := []struct {
		name    string
		records []supplier.Account
		target  core.SyncedAccount
		want    int
		wantErr string
	}{
		{"trim and case", []supplier.Account{first}, core.SyncedAccount{Account: " \tAccount@Example.com\n", Plan: plan}, 0, ""},
		{"different plan excluded", []supplier.Account{otherPlan, first}, core.SyncedAccount{Account: first.Account, Plan: plan}, 1, ""},
		{"plan remains case sensitive", []supplier.Account{first}, core.SyncedAccount{Account: first.Account, Plan: strings.ToUpper(plan)}, -1, "not found"},
		{"platform ID selects duplicate email", []supplier.Account{first, second}, core.SyncedAccount{Account: first.Account, Plan: plan, PlatformAccountID: str("202")}, 1, ""},
		{"platform ID takes priority", []supplier.Account{first, second}, core.SyncedAccount{Account: first.Account, Plan: plan, PlatformAccountID: str("202"), RemoteID: str("101")}, 1, ""},
		{"platform mismatch cannot fall back", []supplier.Account{first}, core.SyncedAccount{Account: first.Account, Plan: plan, PlatformAccountID: str("missing"), RemoteID: str("101")}, -1, "platform account ID"},
		{"remote record ID", []supplier.Account{first, second}, core.SyncedAccount{Account: first.Account, Plan: plan, RemoteID: str("102")}, 1, ""},
		{"legacy remote platform ID", []supplier.Account{first, second}, core.SyncedAccount{Account: first.Account, Plan: plan, RemoteID: str("201")}, 0, ""},
		{"empty platform permits remote ID", []supplier.Account{first}, core.SyncedAccount{Account: first.Account, Plan: plan, PlatformAccountID: str(""), RemoteID: str("101")}, 0, ""},
		{"empty remote pointer is not absent", []supplier.Account{first}, core.SyncedAccount{Account: first.Account, Plan: plan, RemoteID: str("")}, -1, "account ID no longer matches"},
		{"duplicate email stays ambiguous", []supplier.Account{first, second}, core.SyncedAccount{Account: first.Account, Plan: plan}, -1, "More than one"},
		{"duplicate platform ID stays ambiguous", []supplier.Account{first, first}, core.SyncedAccount{Account: first.Account, Plan: plan, PlatformAccountID: str("201")}, -1, "platform account ID"},
		{"duplicate remote ID stays ambiguous", []supplier.Account{first, first}, core.SyncedAccount{Account: first.Account, Plan: plan, RemoteID: str("101")}, -1, "account ID no longer matches"},
		{"missing email", []supplier.Account{first}, core.SyncedAccount{Account: "other@example.com", Plan: plan, RemoteID: str("101")}, -1, "not found"},
		{"one row matching both ID fields counts once", []supplier.Account{sameIDs}, core.SyncedAccount{Account: first.Account, Plan: plan, RemoteID: str("101")}, 0, ""},
		{"record and platform ID collision is ambiguous", []supplier.Account{first, collidingID}, core.SyncedAccount{Account: first.Account, Plan: plan, RemoteID: str("101")}, -1, "account ID no longer matches"},
		{"empty inventory", nil, core.SyncedAccount{Account: first.Account, Plan: plan}, -1, "not found"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := newSupplierAccountIndex(tc.records).match(tc.target)
			if tc.wantErr != "" {
				if got != nil || err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("got %v, %v; want error containing %q", got, err, tc.wantErr)
				}
				return
			}
			if err != nil || got != &tc.records[tc.want] {
				t.Fatalf("got %p, %v; want original record %p", got, err, &tc.records[tc.want])
			}
		})
	}
}

func TestSupplierAccountIndexKeepsEqualFoldSemantics(t *testing.T) {
	// In addition to ordinary casing, preserve simple-fold cycles that lowercase
	// normalization would split. Sharp s must not expand to two ASCII letters.
	names := []string{" K@EXAMPLE.COM ", "k@example.com", "K@example.com", "S@example.com", "s@example.com", "ſ@example.com", "Σ@example.com", "σ@example.com", "ς@example.com", "ß@example.com", "ss@example.com", "İ@example.com", "i@example.com", "\xff@example.com", "\xfe@example.com", "�@example.com"}
	for _, source := range names {
		records := []supplier.Account{{ID: "101", Account: source, Plan: "plan"}}
		index := newSupplierAccountIndex(records)
		for _, target := range names {
			want := strings.EqualFold(strings.TrimSpace(source), strings.TrimSpace(target))
			got, err := index.match(core.SyncedAccount{Account: target, Plan: "plan"})
			if (err == nil) != want || (want && got != &records[0]) {
				t.Errorf("source %q target %q: got %v, %v; EqualFold=%v", source, target, got, err, want)
			}
		}
	}
}

// This baseline preserves the previous full-inventory scan for comparison only.
func linearSupplierMatch(target core.SyncedAccount, records []supplier.Account) (*supplier.Account, error) {
	var matches []int
	for i := range records {
		if strings.EqualFold(strings.TrimSpace(records[i].Account), strings.TrimSpace(target.Account)) && records[i].Plan == target.Plan {
			matches = append(matches, i)
		}
	}
	var matched *supplier.Account
	for _, i := range matches {
		valid := target.PlatformAccountID == nil || *target.PlatformAccountID == ""
		if !valid {
			valid = records[i].PlatformAccountID == *target.PlatformAccountID
		} else if target.RemoteID != nil {
			valid = records[i].ID == *target.RemoteID || records[i].PlatformAccountID == *target.RemoteID
		}
		if valid {
			if matched != nil {
				return nil, errors.New("ambiguous")
			}
			matched = &records[i]
		}
	}
	if matched == nil {
		return nil, errors.New("not found")
	}
	return matched, nil
}

func BenchmarkSupplierAccountMatching(b *testing.B) {
	records := make([]supplier.Account, 3734)
	for i := range records {
		records[i] = supplier.Account{ID: fmt.Sprint(100000 + i), PlatformAccountID: fmt.Sprint(1000 + i), Account: fmt.Sprintf("account-%04d@example.com", i), Plan: "claude/cc-max-20x"}
	}
	targets := make([]core.SyncedAccount, 628)
	for i := range targets {
		record := &records[i*5]
		targets[i] = core.SyncedAccount{Account: strings.ToUpper(record.Account), Plan: record.Plan, PlatformAccountID: &record.PlatformAccountID}
	}
	b.Run("linear_inventory_scan", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			for _, target := range targets {
				if got, err := linearSupplierMatch(target, records); err != nil || got == nil {
					b.Fatal(got, err)
				}
			}
		}
	})
	b.Run("build_index_and_match", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			index := newSupplierAccountIndex(records)
			for _, target := range targets {
				if got, err := index.match(target); err != nil || got == nil {
					b.Fatal(got, err)
				}
			}
		}
	})
}
