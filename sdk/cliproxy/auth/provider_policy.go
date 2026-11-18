package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

// providerDisabledError refuses a request whose provider the routing profile
// disables. It is request-scoped, so no credential cools down, and its Error()
// is the client-facing message without an error-code prefix.
type providerDisabledError struct {
	provider string
	message  string
}

func (e *providerDisabledError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

// StatusCode reports 403 to the handlers.
func (e *providerDisabledError) StatusCode() int { return http.StatusForbidden }

// IsRequestScoped keeps the refusal away from credential cooldown.
func (e *providerDisabledError) IsRequestScoped() bool { return true }

// refuseDisabledProvider logs the refusal of provider and returns the error the
// client receives. The message names the profile kind, never the key or its hash.
func refuseDisabledProvider(ctx context.Context, profile *routingProfile, provider string) error {
	provider = strings.ToLower(strings.TrimSpace(provider))
	message := provider + " is disabled by the routing profile"
	profileName := "shared"
	if profile != nil && profile.fromKey {
		message += " of this API key"
		profileName = profile.keyHash
	}
	logEntryWithRequestID(ctx).WithFields(log.Fields{"provider": provider, "profile": profileName}).Info("Refusing request: provider disabled by routing profile")
	return &providerDisabledError{provider: provider, message: message}
}

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

// RoutingPolicyAllowsAuth reports whether the routing profile of the request in
// ctx would select authID in the primary leg under its per-provider policy. It
// returns true when no policy applies (Home, a scheduler plugin, no profile, or
// a provider without a policy) and for an empty or unknown ID, so callers never
// release a pin they cannot judge. Request-level restrictions (required kind,
// credential policy, free-auth exclusion) are not applied.
func (m *Manager) RoutingPolicyAllowsAuth(ctx context.Context, authID string) bool {
	if m == nil {
		return true
	}
	profile := routingProfileFromContext(m.ensureRoutingProfile(ctx))
	if profile == nil || !profile.providerPolicy {
		return true
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return true
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	auth := m.auths[authID]
	if auth == nil {
		return true
	}
	return providerPolicyAllows(profile, auth)
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
				// The message reaches the client; the auth_index stays in the config.
				return &Error{Code: "auth_not_found", Message: fmt.Sprintf("no enabled %s API key matches routing providers.%s.api-credential", provider, provider)}
			}
			continue
		}
		if hasAPIKey && !hasSubscription {
			return &Error{Code: "auth_not_found", Message: fmt.Sprintf("no subscription credentials for %s; set routing providers.%s.mode: api", provider, provider)}
		}
	}
	return err
}
