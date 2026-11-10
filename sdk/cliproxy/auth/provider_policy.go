package auth

import (
	"context"
	"fmt"
	"strings"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// providerPolicyAllows applies the per-provider mode of the routing profile:
// subscription mode keeps API-key credentials out of the pool, api mode allows
// only the API-key credential named by api-credential. Providers without a
// policy are unaffected.
func providerPolicyAllows(profile *routingProfile, auth *Auth) bool {
	policy, ok := profile.providerMode(auth.Provider)
	if !ok {
		return true
	}
	kind := auth.AuthKind()
	if policy.Mode == internalconfig.ProviderModeAPI {
		return kind == AuthKindAPIKey && candidatePreferenceIndex(auth) == policy.APICredential
	}
	return kind != AuthKindAPIKey
}

// providerPolicySelectionError replaces a generic auth_not_found with a message
// that names the provider policy when the policy, not the pool, left the request
// without credentials: a subscription-mode provider that has only API keys, or
// an api-mode provider whose api-credential matches no enabled API key.
func (m *Manager) providerPolicySelectionError(ctx context.Context, providers []string, opts cliproxyexecutor.Options, err error) error {
	if m == nil || !isAuthNotFoundError(err) {
		return err
	}
	profile := authSelectionEligibilityForRequest(ctx, opts).providerPolicy
	if profile == nil {
		return err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, provider := range providers {
		provider = strings.ToLower(strings.TrimSpace(provider))
		policy, ok := profile.providerMode(provider)
		if !ok {
			continue
		}
		hasAPIKey, hasSubscription, hasCredential := false, false, false
		for _, auth := range m.auths {
			if auth == nil || auth.Disabled || auth.Status == StatusDisabled || strings.ToLower(strings.TrimSpace(auth.Provider)) != provider {
				continue
			}
			if auth.AuthKind() != AuthKindAPIKey {
				hasSubscription = true
				continue
			}
			hasAPIKey = true
			if candidatePreferenceIndex(auth) == policy.APICredential {
				hasCredential = true
			}
		}
		if policy.Mode == internalconfig.ProviderModeAPI {
			if !hasCredential {
				return &Error{Code: "auth_not_found", Message: fmt.Sprintf("no enabled %s API key matches routing providers.%s.api-credential %q", provider, provider, policy.APICredential)}
			}
			continue
		}
		if hasAPIKey && !hasSubscription {
			return &Error{Code: "auth_not_found", Message: fmt.Sprintf("no subscription credentials for %s; set routing providers.%s.mode: api", provider, provider)}
		}
	}
	return err
}
