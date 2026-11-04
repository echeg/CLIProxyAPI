package config

import (
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
	if profile.ClaudeFallback.Mode != "always" || profile.ClaudeFallback.Target("claude-opus") != "gpt-x" {
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
	}
	shared := routing.SharedProfile()
	want := RoutingProfile{
		SessionAffinity:   true,
		PreferredAccounts: map[string]string{"codex": "idx-codex"},
		Selection:         SelectionConfig{Primary: SelectionReserves, Tiebreak: SelectionEarliestReset},
		ClaudeFallback:    routing.ClaudeFallback,
	}
	if !reflect.DeepEqual(shared, want) {
		t.Fatalf("shared = %+v, want %+v", shared, want)
	}
	shared.PreferredAccounts["codex"] = "changed"
	shared.ClaudeFallback.Mappings[0].To = "changed"
	if routing.PreferredAccounts["codex"] != "idx-codex" || routing.ClaudeFallback.Mappings[0].To != "gpt-x" {
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
