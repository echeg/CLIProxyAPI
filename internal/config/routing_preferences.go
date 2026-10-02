package config

import (
	"fmt"
	"strings"
)

// NormalizePreferredAccounts validates and copies manual subscription preferences.
// Empty values restore automatic selection for the corresponding provider.
func (r *RoutingConfig) NormalizePreferredAccounts() error {
	if len(r.PreferredAccounts) == 0 {
		r.PreferredAccounts = nil
		return nil
	}
	normalized := make(map[string]string, len(r.PreferredAccounts))
	for provider, index := range r.PreferredAccounts {
		if provider != "codex" && provider != "claude" {
			return fmt.Errorf("routing.preferred-accounts only supports codex and claude")
		}
		if index = strings.TrimSpace(index); index != "" {
			normalized[provider] = index
		}
	}
	r.PreferredAccounts = normalized
	return nil
}
