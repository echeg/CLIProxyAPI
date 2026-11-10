package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeFallbackConfig(t *testing.T) {
	raw := []byte(`routing:
  claude-fallback:
    enabled: true
    mode: auto
    mappings:
      - {from: 'claude-*', to: 'gpt-generic'}
      - {from: 'claude-opus-*', to: 'gpt-astra'}
      - {from: 'claude-opus-special', to: 'gpt-exact(high)'}
`)
	cfg, errParse := ParseConfigBytes(raw)
	if errParse != nil {
		t.Fatal(errParse)
	}
	for source, target := range map[string]string{"claude-sonnet-5": "gpt-generic", "claude-opus-5": "gpt-astra", "claude-opus-special": "gpt-exact(high)", "other": ""} {
		if got := fallbackMappingTarget(cfg.Routing.ClaudeFallback.Mappings, source); got != target {
			t.Fatalf("target(%s) = %s, want %s", source, got, target)
		}
	}
	clone := cfg.CloneForRuntime()
	cfg.Routing.ClaudeFallback.Mappings[0].To = "changed"
	if clone.Routing.ClaudeFallback.Mappings[0].To != "gpt-generic" {
		t.Fatal("shared mappings")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(path, raw, 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	if errSave := SaveConfigPreserveComments(path, clone, true); errSave != nil {
		t.Fatal(errSave)
	}
	loaded, errLoad := LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	if !loaded.Routing.ClaudeFallback.Enabled || loaded.Routing.ClaudeFallback.Mode != "auto" || fallbackMappingTarget(loaded.Routing.ClaudeFallback.Mappings, "claude-opus-5") != "gpt-astra" {
		t.Fatal("fallback config lost on save")
	}
}

func TestClaudeFallbackConfigRejectsInvalidRules(t *testing.T) {
	for _, body := range []string{
		`{enabled: true, mode: auto}`,
		`{mode: invalid}`,
		`{mappings: [{from: 'claude-*-5', to: gpt}]}`,
		`{mappings: [{from: 'claude-*', to: '*'}]}`,
		`{mappings: [{from: 'claude-*', to: ''}]}`,
		`{mappings: [{from: 'claude-*', to: gpt}, {from: 'claude-*', to: other}]}`,
	} {
		if _, errParse := ParseConfigBytes([]byte("routing:\n  claude-fallback: " + body)); errParse == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}
