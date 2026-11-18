package config

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// writeLoad writes raw to a temp config file and loads it.
func writeLoad(t *testing.T, raw string) (string, *Config) {
	t.Helper()
	path := t.TempDir() + "/config.yaml"
	if errWrite := os.WriteFile(path, []byte(raw), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg, errLoad := LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	return path, cfg
}

// saveRead saves cfg to path and returns the bytes on disk.
func saveRead(t *testing.T, path string, cfg *Config) string {
	t.Helper()
	if errSave := SaveConfigPreserveComments(path, cfg, true); errSave != nil {
		t.Fatal(errSave)
	}
	saved, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	return string(saved)
}

// routingNode returns the node at a dotted path of a YAML document, or nil.
func routingNode(t *testing.T, raw, path string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if errUnmarshal := yaml.Unmarshal([]byte(raw), &doc); errUnmarshal != nil || len(doc.Content) == 0 {
		t.Fatalf("parse saved config: %v", errUnmarshal)
	}
	return yamlPath(doc.Content[0], path)
}

func TestSaveConfigProvidersRoundTrip(t *testing.T) {
	const legacyOnly = `config-version: 8
access:
  api-keys: ["key-a"]
routing:
  # shared fallback
  claude-fallback:
    enabled: true
    mode: auto
    mappings:
      - from: claude-opus-*
        to: gpt-6-astra
  key-profiles:
    key-a:
      claude-fallback:
        enabled: true
        mappings:
          - from: claude-*
            to: gpt-6-sol
`
	const withProviders = `config-version: 8
access:
  api-keys: ["key-a"]
routing:
  providers:
    claude:
      api-credential: claude-idx
      fallback:
        target: api
    codex:
      mode: api
      api-credential: codex-idx
  key-profiles:
    key-a:
      providers:
        claude:
          fallback:
            target: codex
            mappings:
              - from: claude-*
                to: gpt-6-sol
`

	for name, raw := range map[string]string{"legacy only": legacyOnly, "with providers": withProviders} {
		t.Run(name+" stays byte-stable", func(t *testing.T) {
			path, cfg := writeLoad(t, raw)
			first := saveRead(t, path, cfg)
			_, reloaded := writeLoad(t, first)
			if second := saveRead(t, path, reloaded); second != first {
				t.Fatalf("second save changed the file:\n%s\n---\n%s", first, second)
			}
			for _, node := range []string{"routing.providers", "routing.claude-fallback", "routing.key-profiles.key-a.providers", "routing.key-profiles.key-a.claude-fallback"} {
				if (routingNode(t, raw, node) == nil) != (routingNode(t, first, node) == nil) {
					t.Fatalf("save changed the presence of %s:\n%s", node, first)
				}
			}
			if !reflect.DeepEqual(reloaded.Routing, cfg.Routing) {
				t.Fatalf("routing changed on reload:\n%+v\n%+v", reloaded.Routing, cfg.Routing)
			}
		})
	}

	t.Run("legacy file written with providers drops the legacy nodes", func(t *testing.T) {
		path, cfg := writeLoad(t, legacyOnly)
		codex := ProviderFallbackConfig{Target: ProviderFallbackCodex, Mode: "always", Mappings: []ClaudeFallbackMapping{{From: "claude-*", To: "gpt-6-luna"}}}
		cfg.Routing.Providers.Claude.Fallback = codex
		cfg.Routing.KeyProfiles["key-a"].Providers.Claude.Fallback = codex
		// Normalize applies "providers wins" and clears the legacy blocks in memory.
		if errNormalize := cfg.Routing.Normalize(cfg.APIKeys); errNormalize != nil {
			t.Fatal(errNormalize)
		}
		saved := saveRead(t, path, cfg)
		if strings.Contains(saved, "claude-fallback") || routingNode(t, saved, "routing.providers.claude.fallback") == nil || routingNode(t, saved, "routing.key-profiles.key-a.providers.claude.fallback") == nil {
			t.Fatalf("legacy nodes kept or providers missing:\n%s", saved)
		}
		_, loaded := writeLoad(t, saved)
		if got := loaded.Routing.SharedProfile().EffectiveProviders().Claude.Fallback; !reflect.DeepEqual(got, codex) {
			t.Fatalf("shared fallback = %+v, want %+v", got, codex)
		}
		if got := loaded.Routing.KeyProfiles["key-a"].EffectiveProviders().Claude.Fallback; !reflect.DeepEqual(got, codex) {
			t.Fatalf("key fallback = %+v, want %+v", got, codex)
		}
	})

	t.Run("providers without a claude fallback keep the legacy node", func(t *testing.T) {
		path, cfg := writeLoad(t, legacyOnly)
		cfg.Routing.Providers.Codex = ProviderRoutingConfig{Mode: ProviderModeAPI, APICredential: "codex-idx"}
		saved := saveRead(t, path, cfg)
		_, loaded := writeLoad(t, saved)
		if !strings.Contains(saved, "# shared fallback") || !loaded.Routing.SharedProfile().EffectiveProviders().Claude.Fallback.LegacyClaudeFallback().Enabled {
			t.Fatalf("legacy shared fallback lost:\n%s", saved)
		}
		if loaded.Routing.Providers.Codex.APICredential != "codex-idx" {
			t.Fatalf("codex providers not written:\n%s", saved)
		}
	})
}

// Hot reload parses the file through the same loader, so canonicalization and
// legacy conversion must hold for a reloaded document as well.
func TestParseConfigProvidersNormalized(t *testing.T) {
	raw := `config-version: 8
access:
  api-keys: ["key-a"]
routing:
  providers:
    codex: {mode: " API ", api-credential: " codex-idx "}
  key-profiles:
    key-a:
      claude-fallback: {enabled: true, mappings: [{from: "claude-*", to: "gpt-6-sol"}]}
`
	cfg, errParse := ParseConfigBytes([]byte(raw))
	if errParse != nil {
		t.Fatal(errParse)
	}
	if got := cfg.Routing.Providers.Codex; got.Mode != ProviderModeAPI || got.APICredential != "codex-idx" {
		t.Fatalf("shared codex = %+v", got)
	}
	if got := cfg.Routing.KeyProfiles["key-a"].EffectiveProviders().Claude.Fallback.Target; got != ProviderFallbackCodex {
		t.Fatalf("key claude target = %q, want codex", got)
	}
}

func TestSaveConfigCodexSpeedFields(t *testing.T) {
	const withSpeed = `config-version: 8
access:
  api-keys: ["key-a"]
routing:
  providers:
    codex:
      mode: subscription
      # shared speed
      speed: fast
      ignore-client-tier: true
  key-profiles:
    key-a:
      session-affinity: true
      providers:
        codex:
          speed: ultrafast
          ignore-client-tier: true
`
	const withoutSpeed = `config-version: 8
access:
  api-keys: ["key-a"]
routing:
  providers:
    codex:
      mode: subscription
  key-profiles:
    key-a:
      session-affinity: true
      providers:
        codex:
          mode: subscription
`

	t.Run("key profile keeps both fields", func(t *testing.T) {
		path, cfg := writeLoad(t, withSpeed)
		saved := saveRead(t, path, cfg)
		for _, node := range []string{"routing.key-profiles.key-a.providers.codex.speed", "routing.key-profiles.key-a.providers.codex.ignore-client-tier", "routing.providers.codex.speed", "routing.providers.codex.ignore-client-tier"} {
			if routingNode(t, saved, node) == nil {
				t.Fatalf("save dropped %s:\n%s", node, saved)
			}
		}
		_, loaded := writeLoad(t, saved)
		if got := loaded.Routing.KeyProfiles["key-a"].Providers.Codex; got.Speed != CodexSpeedUltrafast || !got.IgnoreClientTier {
			t.Fatalf("key codex = %+v", got)
		}
		if got := loaded.Routing.Providers.Codex; got.Speed != CodexSpeedFast || !got.IgnoreClientTier {
			t.Fatalf("shared codex = %+v", got)
		}
	})

	t.Run("config without the fields saves byte-identical", func(t *testing.T) {
		// The first save renders the file in the canonical layout; the fields
		// must not appear in it and a second save must not change a byte.
		path, cfg := writeLoad(t, withoutSpeed)
		first := saveRead(t, path, cfg)
		if strings.Contains(first, "speed") || strings.Contains(first, "ignore-client-tier") {
			t.Fatalf("save added the new fields:\n%s", first)
		}
		_, reloaded := writeLoad(t, first)
		if second := saveRead(t, path, reloaded); second != first {
			t.Fatalf("second save changed the file:\n%s\n---\n%s", first, second)
		}
	})

	t.Run("clearing the fields of a key profile removes them", func(t *testing.T) {
		path, cfg := writeLoad(t, withSpeed)
		cfg.Routing.KeyProfiles["key-a"].Providers.Codex.Speed = ""
		cfg.Routing.KeyProfiles["key-a"].Providers.Codex.IgnoreClientTier = false
		saved := saveRead(t, path, cfg)
		for _, node := range []string{"routing.key-profiles.key-a.providers.codex.speed", "routing.key-profiles.key-a.providers.codex.ignore-client-tier"} {
			if routingNode(t, saved, node) != nil {
				t.Fatalf("cleared %s survived the save:\n%s", node, saved)
			}
		}
		_, loaded := writeLoad(t, saved)
		key := loaded.Routing.KeyProfiles["key-a"]
		if got := key.EffectiveProviders().Codex; got.Speed != CodexSpeedStandard || got.IgnoreClientTier {
			t.Fatalf("key effective codex = %+v", got)
		}
		if !key.SessionAffinity {
			t.Fatalf("key profile lost session-affinity:\n%s", saved)
		}
	})

	t.Run("clearing the fields of the shared profile removes them", func(t *testing.T) {
		path, cfg := writeLoad(t, withSpeed)
		cfg.Routing.Providers.Codex.Speed = ""
		cfg.Routing.Providers.Codex.IgnoreClientTier = false
		saved := saveRead(t, path, cfg)
		for _, node := range []string{"routing.providers.codex.speed", "routing.providers.codex.ignore-client-tier"} {
			if routingNode(t, saved, node) != nil {
				t.Fatalf("cleared %s survived the save:\n%s", node, saved)
			}
		}
		if routingNode(t, saved, "routing.providers.codex.mode") == nil {
			t.Fatalf("save dropped routing.providers.codex.mode:\n%s", saved)
		}
		_, loaded := writeLoad(t, saved)
		if got := loaded.Routing.SharedProfile().EffectiveProviders().Codex; got.Speed != CodexSpeedStandard || got.IgnoreClientTier {
			t.Fatalf("shared effective codex = %+v", got)
		}
		if got := loaded.Routing.KeyProfiles["key-a"].Providers.Codex; got.Speed != CodexSpeedUltrafast || !got.IgnoreClientTier {
			t.Fatalf("key codex changed: %+v", got)
		}
	})

	t.Run("hand-written defaults survive an unrelated save", func(t *testing.T) {
		const withDefaults = `config-version: 8
access:
  api-keys: ["key-a"]
routing:
  providers:
    codex:
      speed: ""
      # kept on purpose
      ignore-client-tier: false
`
		path, cfg := writeLoad(t, withDefaults)
		saved := saveRead(t, path, cfg)
		for _, node := range []string{"routing.providers.codex.speed", "routing.providers.codex.ignore-client-tier"} {
			if routingNode(t, saved, node) == nil {
				t.Fatalf("save dropped the default %s:\n%s", node, saved)
			}
		}
		if !strings.Contains(saved, "# kept on purpose") {
			t.Fatalf("save dropped the comment:\n%s", saved)
		}
	})

	t.Run("clearing a block that holds only the fields removes the emptied mappings", func(t *testing.T) {
		const onlySpeed = `config-version: 8
access:
  api-keys: ["key-a"]
routing:
  providers:
    codex:
      speed: fast
`
		path, cfg := writeLoad(t, onlySpeed)
		cfg.Routing.Providers.Codex.Speed = ""
		saved := saveRead(t, path, cfg)
		if node := routingNode(t, saved, "routing.providers"); node != nil {
			t.Fatalf("cleared block left routing.providers behind:\n%s", saved)
		}
		_, loaded := writeLoad(t, saved)
		if got := loaded.Routing.SharedProfile().EffectiveProviders().Codex; got.Speed != CodexSpeedStandard || got.IgnoreClientTier {
			t.Fatalf("shared effective codex = %+v", got)
		}
	})

	t.Run("an emptied codex block leaves the claude block in place", func(t *testing.T) {
		const withClaude = `config-version: 8
access:
  api-keys: ["key-a"]
routing:
  providers:
    # claude stays
    claude:
      mode: subscription
    codex:
      speed: fast
`
		path, cfg := writeLoad(t, withClaude)
		cfg.Routing.Providers.Codex.Speed = ""
		saved := saveRead(t, path, cfg)
		if node := routingNode(t, saved, "routing.providers.codex"); node != nil {
			t.Fatalf("cleared codex block survived the save:\n%s", saved)
		}
		// The claude block keeps its value and its comment.
		if node := routingNode(t, saved, "routing.providers.claude.mode"); node == nil || node.Value != "subscription" || !strings.Contains(saved, "# claude stays") {
			t.Fatalf("save dropped routing.providers.claude:\n%s", saved)
		}
	})
}

func TestSaveConfigProviderDisabled(t *testing.T) {
	const withoutDisabled = `config-version: 8
access:
  api-keys: ["key-a"]
routing:
  providers:
    claude:
      mode: subscription
    codex:
      mode: subscription
  key-profiles:
    key-a:
      session-affinity: true
      providers:
        codex:
          speed: fast
`
	const withDisabled = `config-version: 8
access:
  api-keys: ["key-a"]
routing:
  providers:
    claude:
      mode: subscription
      # shared claude switch
      disabled: true
    codex:
      speed: fast
      disabled: true
  key-profiles:
    key-a:
      session-affinity: true
      providers:
        claude:
          disabled: true
        codex:
          speed: fast
`

	t.Run("config without disabled saves byte-identical", func(t *testing.T) {
		path, cfg := writeLoad(t, withoutDisabled)
		first := saveRead(t, path, cfg)
		if strings.Contains(first, "disabled") {
			t.Fatalf("save added disabled:\n%s", first)
		}
		_, reloaded := writeLoad(t, first)
		if second := saveRead(t, path, reloaded); second != first {
			t.Fatalf("second save changed the file:\n%s\n---\n%s", first, second)
		}
	})

	t.Run("setting disabled writes only that key", func(t *testing.T) {
		path, cfg := writeLoad(t, withoutDisabled)
		before := saveRead(t, path, cfg)
		_, cfg = writeLoad(t, before)
		cfg.Routing.Providers.Claude.Disabled = true
		key := cfg.Routing.KeyProfiles["key-a"]
		key.Providers.Codex.Disabled = true
		cfg.Routing.KeyProfiles["key-a"] = key
		saved := saveRead(t, path, cfg)
		for _, node := range []string{"routing.providers.claude.disabled", "routing.key-profiles.key-a.providers.codex.disabled"} {
			if got := routingNode(t, saved, node); got == nil || got.Value != "true" {
				t.Fatalf("save did not write %s:\n%s", node, saved)
			}
		}
		// Apart from the two new lines the file is unchanged.
		var added []string
		beforeLines := strings.Split(before, "\n")
		savedLines := strings.Split(saved, "\n")
		j := 0
		for _, line := range savedLines {
			if j < len(beforeLines) && line == beforeLines[j] {
				j++
				continue
			}
			added = append(added, strings.TrimSpace(line))
		}
		if j != len(beforeLines) || !reflect.DeepEqual(added, []string{"disabled: true", "disabled: true"}) {
			t.Fatalf("save changed more than the disabled keys (added %q):\n%s\n---\n%s", added, before, saved)
		}
		_, loaded := writeLoad(t, saved)
		if !loaded.Routing.Providers.Claude.Disabled || loaded.Routing.Providers.Codex.Disabled {
			t.Fatalf("shared providers = %+v", loaded.Routing.Providers)
		}
		if got := loaded.Routing.KeyProfiles["key-a"].Providers.Codex; !got.Disabled || got.Speed != CodexSpeedFast {
			t.Fatalf("key codex = %+v", got)
		}
	})

	t.Run("clearing disabled in the shared profile removes it", func(t *testing.T) {
		path, cfg := writeLoad(t, withDisabled)
		cfg.Routing.Providers.Claude.Disabled = false
		cfg.Routing.Providers.Codex.Disabled = false
		saved := saveRead(t, path, cfg)
		for _, node := range []string{"routing.providers.claude.disabled", "routing.providers.codex.disabled"} {
			if routingNode(t, saved, node) != nil {
				t.Fatalf("cleared %s survived the save:\n%s", node, saved)
			}
		}
		if routingNode(t, saved, "routing.providers.claude.mode") == nil || routingNode(t, saved, "routing.providers.codex.speed") == nil {
			t.Fatalf("save dropped sibling settings:\n%s", saved)
		}
		_, loaded := writeLoad(t, saved)
		if loaded.Routing.Providers.Claude.Disabled || loaded.Routing.Providers.Codex.Disabled {
			t.Fatalf("shared providers = %+v", loaded.Routing.Providers)
		}
		if !loaded.Routing.KeyProfiles["key-a"].Providers.Claude.Disabled {
			t.Fatalf("key profile lost claude.disabled:\n%s", saved)
		}
	})

	t.Run("clearing disabled in a key profile removes it", func(t *testing.T) {
		path, cfg := writeLoad(t, withDisabled)
		key := cfg.Routing.KeyProfiles["key-a"]
		key.Providers.Claude.Disabled = false
		cfg.Routing.KeyProfiles["key-a"] = key
		saved := saveRead(t, path, cfg)
		if routingNode(t, saved, "routing.key-profiles.key-a.providers.claude") != nil {
			t.Fatalf("cleared key claude block survived the save:\n%s", saved)
		}
		_, loaded := writeLoad(t, saved)
		got := loaded.Routing.KeyProfiles["key-a"]
		if got.Providers.Claude.Disabled || got.Providers.Codex.Speed != CodexSpeedFast || !got.SessionAffinity {
			t.Fatalf("key profile = %+v", got)
		}
		if !loaded.Routing.Providers.Claude.Disabled || !loaded.Routing.Providers.Codex.Disabled {
			t.Fatalf("shared providers changed: %+v", loaded.Routing.Providers)
		}
	})

	t.Run("clearing blocks that hold only disabled removes the emptied mappings", func(t *testing.T) {
		const onlyDisabled = `config-version: 8
access:
  api-keys: ["key-a"]
routing:
  providers:
    claude:
      disabled: true
    codex:
      disabled: true
`
		for _, tc := range []struct {
			name  string
			clear func(*Config)
			gone  string
			kept  string
		}{
			{name: "claude", clear: func(c *Config) { c.Routing.Providers.Claude.Disabled = false }, gone: "routing.providers.claude", kept: "routing.providers.codex.disabled"},
			{name: "codex", clear: func(c *Config) { c.Routing.Providers.Codex.Disabled = false }, gone: "routing.providers.codex", kept: "routing.providers.claude.disabled"},
			{name: "both", clear: func(c *Config) {
				c.Routing.Providers.Claude.Disabled = false
				c.Routing.Providers.Codex.Disabled = false
			}, gone: "routing.providers"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				path, cfg := writeLoad(t, onlyDisabled)
				tc.clear(cfg)
				saved := saveRead(t, path, cfg)
				if routingNode(t, saved, tc.gone) != nil {
					t.Fatalf("%s survived the save:\n%s", tc.gone, saved)
				}
				if tc.kept != "" && routingNode(t, saved, tc.kept) == nil {
					t.Fatalf("save dropped %s:\n%s", tc.kept, saved)
				}
			})
		}
	})

	t.Run("a file holding disabled false keeps it", func(t *testing.T) {
		const withFalse = `config-version: 8
access:
  api-keys: ["key-a"]
routing:
  providers:
    claude:
      disabled: false
    codex:
      # kept on purpose
      disabled: false
`
		path, cfg := writeLoad(t, withFalse)
		saved := saveRead(t, path, cfg)
		for _, node := range []string{"routing.providers.claude.disabled", "routing.providers.codex.disabled"} {
			if got := routingNode(t, saved, node); got == nil || got.Value != "false" {
				t.Fatalf("save dropped %s:\n%s", node, saved)
			}
		}
		if !strings.Contains(saved, "# kept on purpose") {
			t.Fatalf("save dropped the comment:\n%s", saved)
		}
	})
}
