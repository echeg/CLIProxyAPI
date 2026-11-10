package auth

import (
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// withAPIModeRouting puts provider (claude or codex) into routing mode api with
// authIndex as its API credential, so tests about API-key behavior can select an
// API key. Other providers have no policy and are left unchanged.
func withAPIModeRouting(cfg *internalconfig.Config, provider, authIndex string) {
	if cfg == nil {
		return
	}
	policy := internalconfig.ProviderRoutingConfig{Mode: internalconfig.ProviderModeAPI, APICredential: authIndex}
	switch provider {
	case "claude":
		cfg.Routing.Providers.Claude = policy
	case "codex":
		cfg.Routing.Providers.Codex = policy
	}
}
