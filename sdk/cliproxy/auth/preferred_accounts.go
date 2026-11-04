package auth

import (
	"context"
	"strings"
)

func supportsPreferredAccounts(selector Selector) bool {
	switch selector.(type) {
	case *RoundRobinSelector, *WeightedRoundRobinSelector, *FillFirstSelector, *EarliestResetSelector, *SessionAffinitySelector, *PipelineSelector:
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
			if index == candidatePreferenceIndex(candidate) {
				preferred = append(preferred, candidate)
			}
		}
		if len(preferred) > 0 {
			return highestPriorityAuths(preferred)
		}
	}
	return highestPriorityAuths(auths)
}

// preferredAvailableAuth returns the context profile's preferred credential for the
// provider when it is among the available candidates, regardless of its soft reserve
// level. Only the pipeline uses it; availability (disabled, cooldown, model support,
// hard reserve) is decided by the caller. A "mixed" or empty provider matches the
// preference of each candidate's own provider.
func preferredAvailableAuth(ctx context.Context, provider string, available []*Auth) *Auth {
	preferences := routingProfileFromContext(ctx).preferredAccounts()
	if len(preferences) == 0 {
		return nil
	}
	requested := strings.ToLower(strings.TrimSpace(provider))
	if requested == "mixed" {
		requested = ""
	}
	for _, candidate := range available {
		if candidate == nil {
			continue
		}
		candidateProvider := strings.ToLower(strings.TrimSpace(candidate.Provider))
		if requested != "" && candidateProvider != requested {
			continue
		}
		index := preferences[candidateProvider]
		if index == "" {
			continue
		}
		if index == candidatePreferenceIndex(candidate) {
			return candidate
		}
	}
	return nil
}

// candidatePreferenceIndex returns the auth index a preference is matched against,
// deriving it on a clone when the credential has none yet.
func candidatePreferenceIndex(candidate *Auth) string {
	if candidate.Index != "" {
		return candidate.Index
	}
	return candidate.Clone().EnsureIndex()
}
