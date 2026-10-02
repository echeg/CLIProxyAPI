package auth

import (
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

type preferredAccountsContextKey struct{}

func supportsPreferredAccounts(selector Selector) bool {
	switch selector.(type) {
	case *RoundRobinSelector, *WeightedRoundRobinSelector, *FillFirstSelector, *EarliestResetSelector, *SessionAffinitySelector:
		return true
	default:
		return false
	}
}

func (m *Manager) preferredAccounts() map[string]string {
	if m == nil {
		return nil
	}
	cfg, _ := m.runtimeConfig.Load().(*config.Config)
	if cfg == nil || cfg.Home.Enabled {
		return nil
	}
	return cfg.Routing.PreferredAccounts
}

func (m *Manager) withPreferredAccounts(ctx context.Context) context.Context {
	preferences := m.preferredAccounts()
	if len(preferences) == 0 {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, preferredAccountsContextKey{}, preferences)
}

// preferredOrHighestPriorityAuths applies manual preferences only to an already
// eligible set. Session selectors call this after checking existing bindings.
func preferredOrHighestPriorityAuths(ctx context.Context, auths []*Auth) []*Auth {
	var preferences map[string]string
	if ctx != nil {
		preferences, _ = ctx.Value(preferredAccountsContextKey{}).(map[string]string)
	}
	if len(preferences) != 0 {
		var preferred []*Auth
		for _, candidate := range auths {
			if candidate == nil {
				continue
			}
			index := preferences[strings.ToLower(strings.TrimSpace(candidate.Provider))]
			if index == "" {
				continue
			}
			candidateIndex := candidate.Index
			if candidateIndex == "" {
				candidateIndex = candidate.Clone().EnsureIndex()
			}
			if index == candidateIndex {
				preferred = append(preferred, candidate)
			}
		}
		if len(preferred) > 0 {
			return highestPriorityAuths(preferred)
		}
	}
	return highestPriorityAuths(auths)
}
