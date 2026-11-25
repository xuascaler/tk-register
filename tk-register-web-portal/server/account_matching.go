package main

import (
	"errors"
	"strings"
	"unicode"

	"goapi-tk-register/server/internal/core"
	"goapi-tk-register/server/internal/supplier"
)

type supplierAccountKey struct {
	account string
	plan    string
}

// supplierAccountIndex belongs to one collected inventory. Keep every candidate:
// duplicate emails or IDs must remain ambiguous instead of overwriting a row.
type supplierAccountIndex struct {
	records    []supplier.Account
	candidates map[supplierAccountKey][]int
}

func supplierMatchKey(account, plan string) supplierAccountKey {
	// EqualFold uses Unicode simple folding, which ToLower alone does not match
	// (for example, Greek final sigma and the long s). The smallest rune in each
	// fold cycle gives all and only equivalent names the same lookup key.
	account = strings.Map(func(r rune) rune {
		first, smallest := r, r
		for folded := unicode.SimpleFold(r); folded != first; folded = unicode.SimpleFold(folded) {
			if folded < smallest {
				smallest = folded
			}
		}
		return smallest
	}, strings.TrimSpace(account))
	return supplierAccountKey{account: account, plan: plan}
}

func newSupplierAccountIndex(records []supplier.Account) *supplierAccountIndex {
	index := &supplierAccountIndex{records: records, candidates: make(map[supplierAccountKey][]int, len(records))}
	for i := range records {
		key := supplierMatchKey(records[i].Account, records[i].Plan)
		index.candidates[key] = append(index.candidates[key], i)
	}
	return index
}

func (index *supplierAccountIndex) match(target core.SyncedAccount) (*supplier.Account, error) {
	matches := index.candidates[supplierMatchKey(target.Account, target.Plan)]
	if len(matches) == 0 {
		return nil, errors.New("This account was not found in the supplier website. Previously collected data is retained.")
	}
	if target.PlatformAccountID != nil && *target.PlatformAccountID != "" {
		match, count := 0, 0
		for _, i := range matches {
			if index.records[i].PlatformAccountID == *target.PlatformAccountID {
				match, count = i, count+1
			}
		}
		if count == 1 {
			return &index.records[match], nil
		}
		return nil, errors.New("The platform account ID no longer matches the supplier record. Previously collected data is retained.")
	}
	if target.RemoteID != nil {
		// Historical imports sometimes used the visible platform ID as RemoteID.
		match, count := 0, 0
		for _, i := range matches {
			if index.records[i].ID == *target.RemoteID || index.records[i].PlatformAccountID == *target.RemoteID {
				match, count = i, count+1
			}
		}
		if count == 1 {
			return &index.records[match], nil
		}
		return nil, errors.New("The account ID no longer matches the supplier record. Previously collected data is retained.")
	}
	if len(matches) == 1 {
		return &index.records[matches[0]], nil
	}
	return nil, errors.New("More than one supplier record matches this account. An administrator must check the account identity.")
}

func matchSupplierAccount(target core.SyncedAccount, records []supplier.Account) (*supplier.Account, error) {
	return newSupplierAccountIndex(records).match(target)
}
