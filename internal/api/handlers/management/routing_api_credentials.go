package management

import (
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// routingAPICredentialProviders lists the providers whose API keys a routing
// profile can name in providers.<provider>.api-credential.
var routingAPICredentialProviders = []string{"claude", "codex"}

// routingAPICredentialView describes one API-key credential a routing profile can
// select. It never carries the key itself.
type routingAPICredentialView struct {
	AuthIndex string `json:"auth_index"`
	Label     string `json:"label"`
	BaseURL   string `json:"base_url"`
	Disabled  bool   `json:"disabled"`
}

// routingAPICredentials returns the API-key credentials of claude and codex known
// to manager, sorted by auth_index. Both providers are always present.
func routingAPICredentials(manager *coreauth.Manager) map[string][]routingAPICredentialView {
	out := make(map[string][]routingAPICredentialView, len(routingAPICredentialProviders))
	for _, provider := range routingAPICredentialProviders {
		out[provider] = []routingAPICredentialView{}
	}
	if manager == nil {
		return out
	}
	for _, auth := range manager.List() {
		if auth == nil || auth.AuthKind() != coreauth.AuthKindAPIKey {
			continue
		}
		provider := strings.ToLower(strings.TrimSpace(auth.Provider))
		if _, ok := out[provider]; !ok {
			continue
		}
		// List returns clones, so assigning a missing index does not touch the manager.
		index := strings.TrimSpace(auth.EnsureIndex())
		out[provider] = append(out[provider], routingAPICredentialView{
			AuthIndex: index,
			Label:     auth.Label,
			BaseURL:   authAttribute(auth, "base_url"),
			Disabled:  auth.Disabled || auth.Status == coreauth.StatusDisabled,
		})
	}
	for provider := range out {
		sort.Slice(out[provider], func(i, j int) bool { return out[provider][i].AuthIndex < out[provider][j].AuthIndex })
	}
	return out
}

// effectiveProvidersView returns the effective providers block of a profile for a
// management response, with mappings as [] instead of null.
func effectiveProvidersView(profile config.RoutingProfile) config.ProvidersRoutingConfig {
	providers := profile.EffectiveProviders()
	if providers.Claude.Fallback.Mappings == nil {
		providers.Claude.Fallback.Mappings = []config.ClaudeFallbackMapping{}
	}
	if providers.Codex.Fallback.Mappings == nil {
		providers.Codex.Fallback.Mappings = []config.ClaudeFallbackMapping{}
	}
	return providers
}

// warnUnknownRoutingAPICredentials logs one warning per profile whose
// api-credential does not name an enabled API-key credential of that provider.
// An unknown credential is not a config error: the key may be added later.
// Profiles are identified by "shared" or the key hash prefix, never the raw key.
func warnUnknownRoutingAPICredentials(cfg *config.Config, manager *coreauth.Manager) {
	if cfg == nil || !manager.ClaudeFallbackSupported() {
		return
	}
	enabled := make(map[string]map[string]bool, len(routingAPICredentialProviders))
	for provider, credentials := range routingAPICredentials(manager) {
		enabled[provider] = make(map[string]bool, len(credentials))
		for _, credential := range credentials {
			if !credential.Disabled {
				enabled[provider][credential.AuthIndex] = true
			}
		}
	}
	warn := func(profileName string, profile config.RoutingProfile) {
		providers := profile.EffectiveProviders()
		fields := log.Fields{}
		for provider, block := range map[string]config.ProviderRoutingConfig{"claude": providers.Claude, "codex": providers.Codex} {
			if block.APICredential != "" && !enabled[provider][block.APICredential] {
				fields[provider+"_api_credential"] = block.APICredential
			}
		}
		if len(fields) == 0 {
			return
		}
		fields["profile"] = profileName
		log.WithFields(fields).Warn("routing providers api-credential does not match an enabled API-key credential")
	}
	warn("shared", cfg.Routing.SharedProfile())
	seen := make(map[string]bool, len(cfg.Routing.KeyProfiles))
	for _, key := range cfg.APIKeys {
		profile := cfg.Routing.KeyProfiles[key]
		if profile == nil || seen[key] {
			continue
		}
		seen[key] = true
		warn(config.KeyHashPrefix(key), *profile)
	}
}
