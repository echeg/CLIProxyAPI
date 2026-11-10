package auth

import (
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	log "github.com/sirupsen/logrus"
)

// routingProfile is the effective routing configuration of one request: the
// profile of the inbound client API key, or the shared profile built from the
// global routing fields. It is resolved once per execution and carried in the
// context through the fallback wrapper, retries, and the Codex leg.
type routingProfile struct {
	// keyHash identifies the client key in logs; it is never the raw key.
	keyHash string
	// fromKey is true when the profile comes from routing.key-profiles.
	fromKey   bool
	sticky    bool
	preferred map[string]string
	selection config.SelectionConfig
	fallback  config.ClaudeFallbackConfig
	// providers is the effective per-provider mode and fallback policy.
	providers config.ProvidersRoutingConfig
	// providerPolicy is false when the provider policy is unsupported (a scheduler
	// plugin owns routing), so eligibility ignores providers.
	providerPolicy bool
}

type routingProfileContextKey struct{}

func withRoutingProfile(ctx context.Context, profile *routingProfile) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if profile == nil {
		return ctx
	}
	return context.WithValue(ctx, routingProfileContextKey{}, profile)
}

func routingProfileFromContext(ctx context.Context) *routingProfile {
	if ctx == nil {
		return nil
	}
	profile, _ := ctx.Value(routingProfileContextKey{}).(*routingProfile)
	return profile
}

func (p *routingProfile) preferredAccounts() map[string]string {
	if p == nil {
		return nil
	}
	return p.preferred
}

func newRoutingProfile(profile config.RoutingProfile, keyHash string, fromKey bool) *routingProfile {
	providers := profile.EffectiveProviders()
	return &routingProfile{
		keyHash:   keyHash,
		fromKey:   fromKey,
		sticky:    profile.SessionAffinity,
		preferred: profile.PreferredAccounts,
		selection: profile.EffectiveSelection(),
		// The Codex leg runs only for target codex; the legacy view carries it.
		fallback:       providers.Claude.Fallback.LegacyClaudeFallback(),
		providers:      providers,
		providerPolicy: true,
	}
}

// providerMode returns the effective mode of provider and whether the profile
// enforces a provider policy for it. Only claude and codex carry a policy.
func (p *routingProfile) providerMode(provider string) (config.ProviderRoutingConfig, bool) {
	if p == nil || !p.providerPolicy {
		return config.ProviderRoutingConfig{}, false
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude":
		return p.providers.Claude, true
	case "codex":
		return p.providers.Codex, true
	}
	return config.ProviderRoutingConfig{}, false
}

// clientAPIKeyFromContext returns the authenticated inbound client key stored by
// the access middleware on the gin context, or "" when there is none.
func clientAPIKeyFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	ginCtx, ok := ctx.Value("gin").(interface{ Get(string) (any, bool) })
	if !ok || ginCtx == nil {
		return ""
	}
	raw, ok := ginCtx.Get("userApiKey")
	if !ok {
		return ""
	}
	return contextStringValue(raw)
}

// resolveRoutingProfile returns the effective profile for the request: nil under
// Home, the shared profile under a plugin scheduler, otherwise the key profile of
// the inbound client key when one exists and the shared profile when not.
func (m *Manager) resolveRoutingProfile(ctx context.Context) *routingProfile {
	if m == nil || m.HomeEnabled() {
		return nil
	}
	cfg := m.runtimeConfigSnapshot()
	if cfg == nil {
		cfg = &config.Config{}
	}
	if m.hasPluginScheduler() {
		// The plugin owns credential choice, so the provider policy does not apply.
		profile := newSharedRoutingProfile(cfg.Routing, "")
		profile.providerPolicy = false
		return profile
	}
	key := clientAPIKeyFromContext(ctx)
	keyHash := ""
	if key != "" {
		keyHash = config.KeyHashPrefix(key)
		if profile := cfg.Routing.KeyProfiles[key]; profile != nil {
			return newRoutingProfile(*profile, keyHash, true)
		}
	}
	return newSharedRoutingProfile(cfg.Routing, keyHash)
}

// newSharedRoutingProfile builds the shared profile. It turns session affinity off
// only when key profiles alone enable it, which is when the configured affinity
// wrapper exists for them; a wrapper installed directly by an SDK embedder keeps
// serving requests without a key profile as before.
func newSharedRoutingProfile(routing config.RoutingConfig, keyHash string) *routingProfile {
	profile := newRoutingProfile(routing.SharedProfile(), keyHash, false)
	profile.sticky = routing.SessionAffinity || !routing.StickyAnywhere()
	return profile
}

// SharedSelection returns the effective shared selection of the manager's current
// runtime config. The pipeline selector reads it at Pick time, so a selection change
// applies without a new selector.
func (m *Manager) SharedSelection() config.SelectionConfig {
	cfg := m.runtimeConfigSnapshot()
	if cfg == nil {
		return config.DefaultSelection()
	}
	return cfg.Routing.EffectiveSelection()
}

// ensureRoutingProfile injects the resolved profile unless the context already
// carries one, so nested and internal calls keep the profile of the outer request.
func (m *Manager) ensureRoutingProfile(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if routingProfileFromContext(ctx) != nil {
		return ctx
	}
	return withRoutingProfile(ctx, m.resolveRoutingProfile(ctx))
}

// Routing profile stages reported by logRoutingProfileSelection.
const (
	routingStageSticky    = "sticky"
	routingStagePreferred = "preferred"
	routingStagePipeline  = "pipeline"
	routingStageFallback  = "fallback"
	// routingStageAPI marks a selection made under providers.<provider>.mode: api.
	routingStageAPI = "api"
)

// routingFallbackLegKey marks the context of the Codex leg of a Claude -> Codex
// fallback, so selections made there report the fallback stage.
type routingFallbackLegKey struct{}

func withRoutingFallbackLeg(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, routingFallbackLegKey{}, true)
}

func routingFallbackLeg(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	leg, _ := ctx.Value(routingFallbackLegKey{}).(bool)
	return leg
}

// logRoutingProfileSelection reports a selection driven by a key profile. It logs
// only the key hash prefix and never the raw client key. A selection inside the
// Codex fallback leg reports the fallback stage and keeps the stage that chose the
// credential in fallback_stage.
func logRoutingProfileSelection(ctx context.Context, stage string, auth *Auth) {
	profile := routingProfileFromContext(ctx)
	if profile == nil || !profile.fromKey || auth == nil {
		return
	}
	fields := log.Fields{
		"routing_key":   profile.keyHash,
		"routing_stage": stage,
		"provider":      strings.ToLower(strings.TrimSpace(auth.Provider)),
		"auth_index":    candidatePreferenceIndex(auth),
	}
	if policy, ok := profile.providerMode(auth.Provider); ok && policy.Mode == config.ProviderModeAPI && auth.AuthKind() == AuthKindAPIKey {
		stage = routingStageAPI
		fields["routing_stage"] = stage
	}
	if routingFallbackLeg(ctx) && stage != routingStageFallback {
		fields["routing_stage"] = routingStageFallback
		fields["fallback_stage"] = stage
	}
	logEntryWithRequestID(ctx).WithFields(fields).Info("Routing profile selection")
}
