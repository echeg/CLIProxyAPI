package auth

import (
	"context"
	"strings"
)

func supportsPreferredAccounts(selector Selector) bool {
	switch selector.(type) {
	case *RoundRobinSelector, *WeightedRoundRobinSelector, *FillFirstSelector, *EarliestResetSelector, *SessionAffinitySelector:
		return true
	default:
		return false
	}
}

// preferredOrHighestPriorityAuths applies manual preferences only to an already
// eligible set. Session selectors call this after checking existing bindings.
func preferredOrHighestPriorityAuths(ctx context.Context, auths []*Auth) []*Auth {
	preferences := routingProfileFromContext(ctx).preferredAccounts()
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
