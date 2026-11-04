package config

// NormalizePreferredAccounts validates and copies manual subscription preferences.
// Empty values restore automatic selection for the corresponding provider.
func (r *RoutingConfig) NormalizePreferredAccounts() error {
	normalized, err := normalizePreferredAccountsMap(r.PreferredAccounts, "routing.preferred-accounts")
	if err != nil {
		return err
	}
	r.PreferredAccounts = normalized
	return nil
}
