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
