package executor

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// withAPIModeRouting puts the provider of a single Claude or Codex API-key auth
// in routing mode api with that auth as api-credential, because the Manager
// selects Claude and Codex API keys only under mode api. It gives the auth an
// explicit Index (its ID) when it has none. Other auths leave cfg unchanged.
// A nil cfg starts from an empty config.
func withAPIModeRouting(cfg *config.Config, auth *cliproxyauth.Auth) *config.Config {
	if cfg == nil {
		cfg = &config.Config{}
	}
	if auth == nil || auth.AuthKind() != cliproxyauth.AuthKindAPIKey || (auth.Provider != "claude" && auth.Provider != "codex") {
		return cfg
	}
	if auth.Index == "" {
		auth.Index = auth.ID
	}
	policy := config.ProviderRoutingConfig{Mode: config.ProviderModeAPI, APICredential: auth.Index}
	if auth.Provider == "claude" {
		cfg.Routing.Providers.Claude = policy
	} else {
		cfg.Routing.Providers.Codex = policy
	}
	return cfg
}
