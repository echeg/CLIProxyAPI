package config

import (
	"os"
	"reflect"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func captureStandardLogs(t *testing.T) *logtest.Hook {
	t.Helper()
	logger := log.StandardLogger()
	saved := make(log.LevelHooks)
	for level, hooks := range logger.Hooks {
		saved[level] = append(saved[level], hooks...)
	}
	hook := logtest.NewGlobal()
	t.Cleanup(func() { logger.ReplaceHooks(saved) })
	return hook
}

func TestRoutingSelectionParseV8(t *testing.T) {
	withSelection := []byte(`config-version: 8
access:
  api-keys: ["key-a"]
routing:
  strategy: fill-first
  selection:
    primary: ' Priority '
    tiebreak: MOST-REMAINING
`)
	cfg, errParse := ParseConfigBytes(withSelection)
	if errParse != nil {
		t.Fatal(errParse)
	}
	want := SelectionConfig{Primary: SelectionPriority, Tiebreak: SelectionMostRemaining}
	if cfg.Routing.Selection != want || cfg.Routing.EffectiveSelection() != want {
		t.Fatalf("selection = %+v, effective = %+v", cfg.Routing.Selection, cfg.Routing.EffectiveSelection())
	}

	withoutSelection := []byte("config-version: 8\nrouting:\n  strategy: fill-first\n")
	cfg, errParse = ParseConfigBytes(withoutSelection)
	if errParse != nil {
		t.Fatal(errParse)
	}
	if cfg.Routing.Selection != (SelectionConfig{}) {
		t.Fatalf("absent selection parsed as %+v", cfg.Routing.Selection)
	}
	if got := cfg.Routing.EffectiveSelection(); got != (SelectionConfig{Primary: SelectionReserves, Tiebreak: SelectionFillFirst}) {
		t.Fatalf("derived selection = %+v", got)
	}
}

func TestRoutingEffectiveSelectionLegacyDerivation(t *testing.T) {
	for strategy, tiebreak := range map[string]string{
		"":                     SelectionRoundRobin,
		"round-robin":          SelectionRoundRobin,
		"roundrobin":           SelectionRoundRobin,
		"unknown":              SelectionRoundRobin,
		"weighted-round-robin": SelectionWeightedRoundRobin,
		"weightedroundrobin":   SelectionWeightedRoundRobin,
		" WRR ":                SelectionWeightedRoundRobin,
		"fill-first":           SelectionFillFirst,
		"fillfirst":            SelectionFillFirst,
		"ff":                   SelectionFillFirst,
		"earliest-reset":       SelectionEarliestReset,
	} {
		got := RoutingConfig{Strategy: strategy}.EffectiveSelection()
		if got != (SelectionConfig{Primary: SelectionReserves, Tiebreak: tiebreak}) {
			t.Fatalf("strategy %q derived %+v", strategy, got)
		}
	}
	if got := (RoutingProfile{}).EffectiveSelection(); got != DefaultSelection() {
		t.Fatalf("empty profile selection = %+v", got)
	}
}

func TestRoutingSelectionInvalidSlots(t *testing.T) {
	cases := map[string]string{
		"routing:\n  selection: {primary: fastest}\n":                                                   "routing.selection.primary",
		"routing:\n  selection: {primary: priority, tiebreak: lowest-id}\n":                             "routing.selection.tiebreak",
		"routing:\n  selection: {tiebreak: round-robin}\n":                                              "routing.selection.primary is required",
		"api-keys: [key-a]\nrouting:\n  key-profiles:\n    key-a:\n      selection: {primary: bogus}\n": "routing.key-profiles." + KeyHashPrefix("key-a") + ".selection.primary",
	}
	for body, wantPath := range cases {
		_, errParse := ParseConfigBytes([]byte(body))
		if errParse == nil || !strings.Contains(errParse.Error(), wantPath) {
			t.Fatalf("body %q error = %v, want path %q", body, errParse, wantPath)
		}
		if strings.Contains(errParse.Error(), "key-a") {
			t.Fatalf("error leaks the raw key: %v", errParse)
		}
	}
}

func TestIsPickerSlot(t *testing.T) {
	for _, slot := range SelectionPickerSlots {
		if !IsPickerSlot(slot) {
			t.Fatalf("%s is a picker", slot)
		}
	}
	for _, slot := range append(append([]string{}, SelectionNarrowingSlots...), "", "unknown") {
		if IsPickerSlot(slot) {
			t.Fatalf("%q is not a picker", slot)
		}
	}
}

func TestRoutingKeyProfilesNormalize(t *testing.T) {
	hook := captureStandardLogs(t)
	raw := []byte(`config-version: 8
access:
  api-keys: ["key-a", "key-b"]
routing:
  key-profiles:
    key-a:
      session-affinity: true
      preferred-accounts: {claude: ' idx-claude ', codex: ''}
      selection: {primary: Reserves, tiebreak: Fill-First}
      claude-fallback:
        enabled: true
        mode: ' always '
        mappings:
          - {from: 'claude-*', to: 'gpt-x'}
    key-b: null
    removed-key-secret:
      session-affinity: true
`)
	cfg, errParse := ParseConfigBytes(raw)
	if errParse != nil {
		t.Fatal(errParse)
	}
	if len(cfg.Routing.KeyProfiles) != 1 {
		t.Fatalf("profiles = %v", cfg.Routing.KeyProfiles)
	}
	profile := cfg.Routing.KeyProfiles["key-a"]
	if profile == nil || !profile.SessionAffinity {
		t.Fatalf("profile key-a = %+v", profile)
	}
	if !reflect.DeepEqual(profile.PreferredAccounts, map[string]string{"claude": "idx-claude"}) {
		t.Fatalf("preferred = %v", profile.PreferredAccounts)
	}
	if profile.Selection != (SelectionConfig{Primary: SelectionReserves, Tiebreak: SelectionFillFirst}) {
		t.Fatalf("selection = %+v", profile.Selection)
	}
	if profile.ClaudeFallback.Mode != "always" || fallbackMappingTarget(profile.ClaudeFallback.Mappings, "claude-opus") != "gpt-x" {
		t.Fatalf("fallback = %+v", profile.ClaudeFallback)
	}

	var warned bool
	for _, entry := range hook.AllEntries() {
		line, _ := entry.String()
		if strings.Contains(line, "removed-key-secret") || strings.Contains(line, "key-a") {
			t.Fatalf("log line leaks a key: %s", line)
		}
		if entry.Level == log.WarnLevel && strings.Contains(entry.Message, "routing.key-profiles") {
			warned = true
			if entry.Data["routing_key"] != KeyHashPrefix("removed-key-secret") {
				t.Fatalf("warn fields = %v", entry.Data)
			}
		}
	}
	if !warned {
		t.Fatal("unknown-key profile was dropped without a warning")
	}

	clone := cfg.CloneForRuntime()
	cfg.Routing.KeyProfiles["key-a"].PreferredAccounts["claude"] = "changed"
	if clone.Routing.KeyProfiles["key-a"].PreferredAccounts["claude"] != "idx-claude" {
		t.Fatal("runtime clone shares key profile maps")
	}
}

func TestRoutingKeyProfilesEmptyAfterDrop(t *testing.T) {
	captureStandardLogs(t)
	cfg, errParse := ParseConfigBytes([]byte("routing:\n  key-profiles:\n    ghost: {session-affinity: true}\n    other: null\n"))
	if errParse != nil {
		t.Fatal(errParse)
	}
	if cfg.Routing.KeyProfiles != nil {
		t.Fatalf("profiles = %v, want nil", cfg.Routing.KeyProfiles)
	}
}

func TestRoutingKeyProfilesInvalidBlocks(t *testing.T) {
	hash := KeyHashPrefix("key-a")
	cases := map[string]string{
		"api-keys: [key-a]\nrouting:\n  key-profiles:\n    key-a:\n      claude-fallback: {enabled: true}\n":                              "routing.key-profiles." + hash + ": routing.claude-fallback requires at least one mapping",
		"api-keys: [key-a]\nrouting:\n  key-profiles:\n    key-a:\n      claude-fallback: {mode: sometimes}\n":                            "routing.key-profiles." + hash + ": routing.claude-fallback.mode",
		"api-keys: [key-a]\nrouting:\n  key-profiles:\n    key-a:\n      preferred-accounts: {gemini: x}\n":                               "routing.key-profiles." + hash + ".preferred-accounts only supports codex and claude",
		"config-version: 8\naccess:\n  api-keys: [key-a]\nrouting:\n  key-profiles:\n    key-a:\n      preferred-accounts: {gemini: x}\n": "routing.key-profiles." + hash + ".preferred-accounts",
	}
	for body, want := range cases {
		_, errParse := ParseConfigBytes([]byte(body))
		if errParse == nil || !strings.Contains(errParse.Error(), want) {
			t.Fatalf("body %q error = %v, want %q", body, errParse, want)
		}
	}
	// The global field keeps its original message.
	_, errGlobal := ParseConfigBytes([]byte("routing:\n  preferred-accounts: {gemini: x}\n"))
	if errGlobal == nil || errGlobal.Error() != "routing.preferred-accounts only supports codex and claude" {
		t.Fatalf("global preferred error = %v", errGlobal)
	}
}

func TestRoutingSharedProfile(t *testing.T) {
	routing := RoutingConfig{
		Strategy:          "earliest-reset",
		SessionAffinity:   true,
		PreferredAccounts: map[string]string{"codex": "idx-codex"},
		ClaudeFallback: ClaudeFallbackConfig{
			Enabled:  true,
			Mode:     "auto",
			Mappings: []ClaudeFallbackMapping{{From: "claude-*", To: "gpt-x"}},
		},
		Providers: ProvidersRoutingConfig{
			Codex: ProviderRoutingConfig{APICredential: "idx-api", Fallback: ProviderFallbackConfig{Target: ProviderFallbackAPI, Mappings: []ClaudeFallbackMapping{{From: "gpt-*", To: "kept"}}}},
		},
	}
	shared := routing.SharedProfile()
	want := RoutingProfile{
		SessionAffinity:   true,
		PreferredAccounts: map[string]string{"codex": "idx-codex"},
		Selection:         SelectionConfig{Primary: SelectionReserves, Tiebreak: SelectionEarliestReset},
		ClaudeFallback:    routing.ClaudeFallback,
		Providers:         routing.Providers,
	}
	if !reflect.DeepEqual(shared, want) {
		t.Fatalf("shared = %+v, want %+v", shared, want)
	}
	if effective := shared.EffectiveProviders(); effective.Claude.Fallback.Target != ProviderFallbackCodex || effective.Codex.Fallback.Target != ProviderFallbackAPI {
		t.Fatalf("shared effective providers = %+v", effective)
	}
	shared.PreferredAccounts["codex"] = "changed"
	shared.ClaudeFallback.Mappings[0].To = "changed"
	shared.Providers.Codex.Fallback.Mappings[0].To = "changed"
	if routing.PreferredAccounts["codex"] != "idx-codex" || routing.ClaudeFallback.Mappings[0].To != "gpt-x" || routing.Providers.Codex.Fallback.Mappings[0].To != "kept" {
		t.Fatal("shared profile aliases the global routing fields")
	}

	routing.Selection = SelectionConfig{Primary: SelectionPriority, Tiebreak: SelectionRoundRobin}
	if got := routing.SharedProfile().Selection; got != routing.Selection {
		t.Fatalf("explicit shared selection = %+v", got)
	}
	if empty := (RoutingConfig{}).SharedProfile(); empty.PreferredAccounts != nil || empty.Selection != DefaultSelection() {
		t.Fatalf("empty shared profile = %+v", empty)
	}
}

func TestRoutingProfileCloneProviders(t *testing.T) {
	profile := RoutingProfile{Providers: ProvidersRoutingConfig{
		Claude: ProviderRoutingConfig{Fallback: ProviderFallbackConfig{Target: ProviderFallbackCodex, Mappings: []ClaudeFallbackMapping{{From: "claude-*", To: "gpt-x"}}}},
		Codex:  ProviderRoutingConfig{Fallback: ProviderFallbackConfig{Mappings: []ClaudeFallbackMapping{{From: "a", To: "b"}}}},
	}}
	clone := profile.Clone()
	if !reflect.DeepEqual(clone, profile) {
		t.Fatalf("clone = %+v, want %+v", clone, profile)
	}
	clone.Providers.Claude.Fallback.Mappings[0].To = "changed"
	clone.Providers.Codex.Fallback.Mappings[0].To = "changed"
	clone.Providers.Claude.Mode = ProviderModeAPI
	if profile.Providers.Claude.Fallback.Mappings[0].To != "gpt-x" || profile.Providers.Codex.Fallback.Mappings[0].To != "b" || profile.Providers.Claude.Mode != "" {
		t.Fatal("clone shares providers state")
	}
}

func TestKeyHashPrefix(t *testing.T) {
	got := KeyHashPrefix("key-a")
	if len(got) != 12 || got == KeyHashPrefix("key-b") || strings.Contains(got, "key") {
		t.Fatalf("hash prefix = %q", got)
	}
}

func TestRoutingConfigStickyAnywhere(t *testing.T) {
	for _, tt := range []struct {
		name    string
		routing RoutingConfig
		want    bool
	}{
		{name: "empty", routing: RoutingConfig{}, want: false},
		{name: "shared", routing: RoutingConfig{SessionAffinity: true}, want: true},
		{name: "key", routing: RoutingConfig{KeyProfiles: map[string]*RoutingProfile{"k": {SessionAffinity: true}}}, want: true},
		{name: "key off and nil", routing: RoutingConfig{KeyProfiles: map[string]*RoutingProfile{"k": {}, "nil": nil}}, want: false},
	} {
		if got := tt.routing.StickyAnywhere(); got != tt.want {
			t.Fatalf("%s: StickyAnywhere() = %t, want %t", tt.name, got, tt.want)
		}
	}
}

func TestSaveConfigPrunesRemovedKeyProfiles(t *testing.T) {
	path := t.TempDir() + "/config.yaml"
	raw := `config-version: 8
access:
  api-keys: ["key-a", "key-b"]
routing:
  # keep this comment
  strategy: fill-first
  key-profiles:
    key-a:
      selection: {primary: priority}
    key-b:
      session-affinity: true
`
	if errWrite := os.WriteFile(path, []byte(raw), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg, errLoad := LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	if len(cfg.Routing.KeyProfiles) != 2 {
		t.Fatalf("profiles = %v", cfg.Routing.KeyProfiles)
	}

	cfg.APIKeys = []string{"key-b"}
	delete(cfg.Routing.KeyProfiles, "key-a")
	if errSave := SaveConfigPreserveComments(path, cfg, true); errSave != nil {
		t.Fatal(errSave)
	}
	saved, _ := os.ReadFile(path)
	if strings.Contains(string(saved), "key-a") || !strings.Contains(string(saved), "key-b:") || !strings.Contains(string(saved), "# keep this comment") {
		t.Fatalf("pruning failed:\n%s", saved)
	}

	cfg.Routing.KeyProfiles = nil
	if errSave := SaveConfigPreserveComments(path, cfg, true); errSave != nil {
		t.Fatal(errSave)
	}
	saved, _ = os.ReadFile(path)
	if strings.Contains(string(saved), "key-profiles") || !strings.Contains(string(saved), "strategy: fill-first") {
		t.Fatalf("empty key-profiles node kept:\n%s", saved)
	}
	loaded, errReload := LoadConfig(path)
	if errReload != nil || len(loaded.Routing.KeyProfiles) != 0 || loaded.Routing.Strategy != "fill-first" {
		t.Fatalf("reload: %v %+v", errReload, loaded)
	}
}

func TestRoutingProfileEffectiveSelection(t *testing.T) {
	if got := (RoutingProfile{}).EffectiveSelection(); got != DefaultSelection() {
		t.Fatalf("empty profile selection = %+v, want the default", got)
	}
	explicit := SelectionConfig{Primary: SelectionMostRemaining, Tiebreak: SelectionWeightedRoundRobin}
	if got := (RoutingProfile{Selection: explicit}).EffectiveSelection(); got != explicit {
		t.Fatalf("explicit profile selection = %+v, want %+v", got, explicit)
	}
}

// An empty profile is an explicit override of the shared settings, and a profile
// never inherits fields, so saving must keep empty entries and drop cleared fields.
func TestSaveConfigKeepsEmptyKeyProfilesAndDropsClearedFields(t *testing.T) {
	save := func(t *testing.T, raw string, edit func(*Config)) *Config {
		t.Helper()
		path := t.TempDir() + "/config.yaml"
		if errWrite := os.WriteFile(path, []byte(raw), 0o600); errWrite != nil {
			t.Fatal(errWrite)
		}
		cfg, errLoad := LoadConfig(path)
		if errLoad != nil {
			t.Fatal(errLoad)
		}
		edit(cfg)
		if errSave := SaveConfigPreserveComments(path, cfg, true); errSave != nil {
			t.Fatal(errSave)
		}
		loaded, errReload := LoadConfig(path)
		if errReload != nil {
			t.Fatal(errReload)
		}
		return loaded
	}
	const withProfiles = `config-version: 8
access:
  api-keys: ["key-a", "old"]
routing:
  session-affinity: true
  key-profiles:
    key-a:
      session-affinity: true
      selection: {primary: priority, tiebreak: fill-first}
    old: {}
`

	t.Run("rotated empty profile", func(t *testing.T) {
		loaded := save(t, withProfiles, func(cfg *Config) {
			cfg.APIKeys = []string{"key-a", "new"}
			cfg.Routing.KeyProfiles["new"] = cfg.Routing.KeyProfiles["old"]
			delete(cfg.Routing.KeyProfiles, "old")
		})
		if profile := loaded.Routing.KeyProfiles["new"]; profile == nil || !reflect.DeepEqual(*profile, RoutingProfile{}) {
			t.Fatalf("rotated profile = %+v, want an empty override", profile)
		}
		if _, ok := loaded.Routing.KeyProfiles["old"]; ok {
			t.Fatal("old key profile kept")
		}
	})

	t.Run("profile cleared to empty", func(t *testing.T) {
		loaded := save(t, withProfiles, func(cfg *Config) {
			cfg.Routing.KeyProfiles["key-a"] = &RoutingProfile{}
		})
		if profile := loaded.Routing.KeyProfiles["key-a"]; profile == nil || !reflect.DeepEqual(*profile, RoutingProfile{}) {
			t.Fatalf("cleared profile = %+v, want empty", profile)
		}
	})

	t.Run("one field cleared", func(t *testing.T) {
		loaded := save(t, withProfiles, func(cfg *Config) {
			cfg.Routing.KeyProfiles["key-a"].Selection.Tiebreak = ""
		})
		want := RoutingProfile{SessionAffinity: true, Selection: SelectionConfig{Primary: SelectionPriority}}
		if profile := loaded.Routing.KeyProfiles["key-a"]; profile == nil || !reflect.DeepEqual(*profile, want) {
			t.Fatalf("profile = %+v, want %+v", profile, want)
		}
	})

	for name, raw := range map[string]string{
		"no key-profiles": "config-version: 8\naccess:\n  api-keys: [\"key-a\"]\nrouting:\n  strategy: fill-first\n",
		"no routing":      "config-version: 8\naccess:\n  api-keys: [\"key-a\"]\n",
	} {
		t.Run("empty profile added with "+name, func(t *testing.T) {
			loaded := save(t, raw, func(cfg *Config) {
				cfg.Routing.KeyProfiles = map[string]*RoutingProfile{"key-a": {}}
			})
			if profile := loaded.Routing.KeyProfiles["key-a"]; profile == nil || !reflect.DeepEqual(*profile, RoutingProfile{}) {
				t.Fatalf("added profile = %+v, want an empty override", profile)
			}
		})
	}
}
