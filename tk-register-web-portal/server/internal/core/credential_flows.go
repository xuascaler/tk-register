package core

// Credential-flow drafts reuse the settings/submission encryption envelope,
// with their own purpose and owner-bound authenticated context. They live only
// in expiring Redis entries, never in settings or a second submission table.
func (s *Store) SealCredentialFlow(owner uint64, flowID, value string) (string, error) {
	return s.encrypt("credential-flow:"+idString(owner)+":"+flowID, value)
}

func (s *Store) OpenCredentialFlow(owner uint64, flowID, value string) (string, error) {
	return s.decrypt("credential-flow:"+idString(owner)+":"+flowID, value)
}

// Account-scoped generation has a separate authenticated purpose so a
// downgraded server cannot interpret its drafts as legacy submission drafts.
func (s *Store) SealAccountDeactivateFlow(owner uint64, flowID, value string) (string, error) {
	return s.encrypt("account-deactivate-flow:"+idString(owner)+":"+flowID, value)
}

func (s *Store) OpenAccountDeactivateFlow(owner uint64, flowID, value string) (string, error) {
	return s.decrypt("account-deactivate-flow:"+idString(owner)+":"+flowID, value)
}
