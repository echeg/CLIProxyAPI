package diff

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestBuildConfigChangeDetailsRoutingSelectionAndKeyProfiles(t *testing.T) {
	const keyA, keyB = "client-key-alpha-secret", "client-key-beta-secret"
	base := func() *config.Config {
		cfg := &config.Config{}
		cfg.APIKeys = []string{keyA, keyB}
		cfg.Routing.KeyProfiles = map[string]*config.RoutingProfile{keyA: {SessionAffinity: true}}
		return cfg
	}

	cases := []struct {
		name   string
		mutate func(*config.Config)
		want   string
	}{
		{"selection set", func(c *config.Config) {
			c.Routing.Selection = config.SelectionConfig{Primary: "priority", Tiebreak: "fill-first"}
		}, "routing.selection: <derived> -> priority,fill-first"},
		{"selection picker only", func(c *config.Config) {
			c.Routing.Selection = config.SelectionConfig{Primary: "round-robin"}
		}, "routing.selection: <derived> -> round-robin"},
		{"profile added", func(c *config.Config) {
			c.Routing.KeyProfiles[keyB] = &config.RoutingProfile{}
		}, "routing.key-profiles count: 1 -> 2"},
		{"profile removed", func(c *config.Config) {
			c.Routing.KeyProfiles = nil
		}, "routing.key-profiles count: 1 -> 0"},
		{"profile updated", func(c *config.Config) {
			c.Routing.KeyProfiles[keyA] = &config.RoutingProfile{PreferredAccounts: map[string]string{"claude": "idx-1"}}
		}, "routing.key-profiles: updated (redacted)"},
		{"shared fallback updated", func(c *config.Config) {
			c.Routing.ClaudeFallback = config.ClaudeFallbackConfig{Enabled: true, Mode: "auto", Mappings: []config.ClaudeFallbackMapping{{From: "claude-*", To: "gpt-6-sol"}}}
		}, "routing.claude-fallback: updated"},
		{"shared providers updated", func(c *config.Config) {
			c.Routing.Providers.Codex = config.ProviderRoutingConfig{Mode: "api", APICredential: "idx-1"}
		}, "routing.providers: updated"},
		{"profile providers updated", func(c *config.Config) {
			c.Routing.KeyProfiles[keyA] = &config.RoutingProfile{SessionAffinity: true, Providers: config.ProvidersRoutingConfig{
				Claude: config.ProviderRoutingConfig{APICredential: "idx-1", Fallback: config.ProviderFallbackConfig{Target: "api"}},
			}}
		}, "routing.key-profiles: updated (redacted)"},
		{"profile rekeyed", func(c *config.Config) {
			c.Routing.KeyProfiles = map[string]*config.RoutingProfile{keyB: {SessionAffinity: true}}
		}, "routing.key-profiles: updated (redacted)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oldCfg, newCfg := base(), base()
			tc.mutate(newCfg)
			changes := BuildConfigChangeDetails(oldCfg, newCfg)
			if len(changes) != 1 || changes[0] != tc.want {
				t.Fatalf("changes = %v, want [%s]", changes, tc.want)
			}
			for _, line := range changes {
				for _, secret := range []string{keyA, keyB, config.KeyHashPrefix(keyA), config.KeyHashPrefix(keyB), "idx-1"} {
					if strings.Contains(line, secret) {
						t.Fatalf("change line %q leaks %q", line, secret)
					}
				}
			}
		})
	}

	if changes := BuildConfigChangeDetails(base(), base()); len(changes) != 0 {
		t.Fatalf("equal profiles produced changes: %v", changes)
	}
}
