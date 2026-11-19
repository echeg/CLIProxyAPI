package auth

import (
	"context"
	"errors"
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
	message string
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

// IsProviderDisabledError reports whether err is the refusal of a provider the
// request's routing profile disables. The refusal is final for the client key,
// so transports that hide transient failures still surface it.
func IsProviderDisabledError(err error) bool {
	var disabledErr *providerDisabledError
	return errors.As(err, &disabledErr) && disabledErr != nil
}

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
	return &providerDisabledError{message: message}
}

type openSessionPinContextKey struct{}

// WithOpenSessionPin marks a direct auth selection that ends a session opened
// before its provider was disabled, such as the hangup of an open realtime call.
// When the selection options pin an auth, the routing profile's
// providers.<p>.disabled switch is skipped; every other selection check (the
// pinned auth's provider, the required kind, its disabled state and
// availability) still applies.
func WithOpenSessionPin(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, openSessionPinContextKey{}, true)
}

func openSessionPinFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	pinned, _ := ctx.Value(openSessionPinContextKey{}).(bool)
	return pinned
}

// refuseDisabledSelection resolves the routing profile of a direct auth selection
// (Codex search, live and realtime pick an auth without executeWithProviderFallback)
// and refuses it when that profile disables provider, unless the selection is an
// open-session pin (WithOpenSessionPin plus a pinned auth in opts). The returned
// context carries the resolved profile. Home and plugin schedulers never refuse
// because providerMode reports no policy there.
func (m *Manager) refuseDisabledSelection(ctx context.Context, provider string, opts cliproxyexecutor.Options) (context.Context, error) {
	ctx = m.ensureRoutingProfile(ctx)
	profile := routingProfileFromContext(ctx)
	if policy, ok := profile.providerMode(provider); ok && policy.Disabled {
		if openSessionPinFromContext(ctx) && pinnedAuthIDFromMetadata(opts.Metadata) != "" {
			return ctx, nil
		}
		return ctx, refuseDisabledProvider(ctx, profile, provider)
	}
	return ctx, nil
}

// ProviderEnabledForRequest returns a predicate that reports whether the routing
// profile of the request in ctx leaves a provider enabled (providers.<p>.disabled
// is not set). The profile is resolved once and nothing is logged, so a caller can
// steer a choice made before execution, such as resolving the model "auto", while
// the execute gate still enforces the switch. Under Home and a plugin scheduler
// every provider is enabled, as at the gate.
func (m *Manager) ProviderEnabledForRequest(ctx context.Context) func(provider string) bool {
	profile := routingProfileFromContext(m.ensureRoutingProfile(ctx))
	return func(provider string) bool {
		policy, ok := profile.providerMode(provider)
		return !ok || !policy.Disabled
	}
}

// providerPolicyAllows applies the per-provider policy of the routing profile:
// a disabled provider keeps every credential out of the pool, subscription mode
// keeps API-key credentials out, api mode allows only the API-key credential
// named by api-credential. Providers without a policy are unaffected.
func providerPolicyAllows(profile *routingProfile, auth *Auth) bool {
	policy, ok := profile.providerMode(auth.Provider)
	if !ok {
		return true
	}
	if policy.Disabled {
		return false
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
