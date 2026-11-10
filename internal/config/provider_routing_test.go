package config

import (
	"reflect"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
)

const providersTestKey = "client-secret-key"

// sharedProvidersConfig builds a v8 config with body under routing.providers.
func sharedProvidersConfig(body string) string {
	return "config-version: 8\naccess:\n  api-keys: [\"" + providersTestKey + "\"]\nrouting:\n  providers: " + body + "\n"
}

// keyProvidersConfig builds a v8 config with body under the key profile's providers.
func keyProvidersConfig(body string) string {
	return "config-version: 8\naccess:\n  api-keys: [\"" + providersTestKey + "\"]\nrouting:\n  key-profiles:\n    \"" + providersTestKey + "\":\n      providers: " + body + "\n"
}

func TestProvidersRoutingValidation(t *testing.T) {
	captureStandardLogs(t)
	hash := KeyHashPrefix(providersTestKey)
	cases := []struct {
		name    string
		body    string
		wantErr string // path suffix after the providers block; empty means valid
	}{
		{name: "empty block", body: `{}`},
		{name: "defaults spelled out", body: `{claude: {mode: subscription, fallback: {target: off}}, codex: {mode: subscription, fallback: {target: off}}}`},
		{name: "claude api mode", body: `{claude: {mode: api, api-credential: idx-1}}`},
		{name: "codex api mode", body: `{codex: {mode: ' API ', api-credential: ' idx-2 '}}`},
		{name: "claude target api", body: `{claude: {api-credential: idx-1, fallback: {target: api}}}`},
		{name: "codex target api", body: `{codex: {api-credential: idx-2, fallback: {target: API}}}`},
		{name: "claude target codex", body: `{claude: {fallback: {target: codex, mode: always, mappings: [{from: 'claude-*', to: gpt-x}]}}}`},
		{name: "api credential kept with subscription", body: `{claude: {api-credential: idx-1}}`},
		{name: "unknown mode", body: `{claude: {mode: hybrid}}`, wantErr: ".claude.mode has unsupported value"},
		{name: "unknown target", body: `{codex: {fallback: {target: gemini}}}`, wantErr: ".codex.fallback.target has unsupported value"},
		{name: "codex target codex", body: `{codex: {fallback: {target: codex, mappings: [{from: a, to: b}]}}}`, wantErr: ".codex.fallback.target codex is only supported for claude"},
		{name: "api mode without credential", body: `{claude: {mode: api}}`, wantErr: ".claude.api-credential is required"},
		{name: "api mode with blank credential", body: `{codex: {mode: api, api-credential: '  '}}`, wantErr: ".codex.api-credential is required"},
		{name: "target api without credential", body: `{codex: {fallback: {target: api}}}`, wantErr: ".codex.api-credential is required"},
		{name: "api mode with target api", body: `{claude: {mode: api, api-credential: x, fallback: {target: api}}}`, wantErr: ".claude.fallback.target must be off when mode is api"},
		{name: "api mode with target codex", body: `{claude: {mode: api, api-credential: x, fallback: {target: codex, mappings: [{from: a, to: b}]}}}`, wantErr: ".claude.fallback.target must be off when mode is api"},
		{name: "target codex without mappings", body: `{claude: {fallback: {target: codex}}}`, wantErr: ".claude.fallback requires at least one mapping"},
		{name: "target codex bad mode", body: `{claude: {fallback: {target: codex, mode: sometimes, mappings: [{from: a, to: b}]}}}`, wantErr: ".claude.fallback.mode must be auto or always"},
		{name: "target codex inner wildcard", body: `{claude: {fallback: {target: codex, mappings: [{from: 'claude-*-5', to: b}]}}}`, wantErr: ".claude.fallback.mappings[0] requires model IDs"},
		{name: "target codex duplicate", body: `{claude: {fallback: {target: codex, mappings: [{from: a, to: b}, {from: a, to: c}]}}}`, wantErr: ".claude.fallback has duplicate source"},
	}
	for _, tc := range cases {
		for scope, build := range map[string]func(string) string{"shared": sharedProvidersConfig, "key": keyProvidersConfig} {
			t.Run(scope+"/"+tc.name, func(t *testing.T) {
				_, errParse := ParseConfigBytes([]byte(build(tc.body)))
				if tc.wantErr == "" {
					if errParse != nil {
						t.Fatalf("unexpected error: %v", errParse)
					}
					return
				}
				prefix := "routing.providers"
				if scope == "key" {
					prefix = "routing.key-profiles." + hash + ".providers"
				}
				if errParse == nil || !strings.Contains(errParse.Error(), prefix+tc.wantErr) {
					t.Fatalf("error = %v, want it to contain %q", errParse, prefix+tc.wantErr)
				}
				if strings.Contains(errParse.Error(), providersTestKey) {
					t.Fatalf("error leaks the raw key: %v", errParse)
				}
			})
		}
	}
}

func TestProvidersRoutingNormalizeCanonicalizes(t *testing.T) {
	captureStandardLogs(t)
	cfg, errParse := ParseConfigBytes([]byte(keyProvidersConfig(`{claude: {mode: ' API ', api-credential: ' idx-1 '}, codex: {fallback: {target: ' Api '}, api-credential: idx-2}}`)))
	if errParse != nil {
		t.Fatal(errParse)
	}
	got := cfg.Routing.KeyProfiles[providersTestKey].Providers
	want := ProvidersRoutingConfig{
		Claude: ProviderRoutingConfig{Mode: ProviderModeAPI, APICredential: "idx-1"},
		Codex:  ProviderRoutingConfig{APICredential: "idx-2", Fallback: ProviderFallbackConfig{Target: ProviderFallbackAPI}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stored providers = %+v, want %+v", got, want)
	}
	effective := cfg.Routing.KeyProfiles[providersTestKey].EffectiveProviders()
	if effective.Claude.Fallback.Target != ProviderFallbackOff || effective.Codex.Mode != ProviderModeSubscription {
		t.Fatalf("effective providers = %+v", effective)
	}
}

func TestProvidersRoutingWarnsOnIgnoredAPIFallbackFields(t *testing.T) {
	hook := captureStandardLogs(t)
	cfg, errParse := ParseConfigBytes([]byte(keyProvidersConfig(`{claude: {api-credential: idx-1, fallback: {target: api, mode: always, mappings: [{from: 'claude-*', to: gpt-x}]}}}`)))
	if errParse != nil {
		t.Fatal(errParse)
	}
	fallback := cfg.Routing.KeyProfiles[providersTestKey].Providers.Claude.Fallback
	if fallback.Mode != "always" || len(fallback.Mappings) != 1 {
		t.Fatalf("ignored fields were not kept: %+v", fallback)
	}
	want := "routing.key-profiles." + KeyHashPrefix(providersTestKey) + ".providers.claude.fallback: mode and mappings are ignored"
	var warned bool
	for _, entry := range hook.AllEntries() {
		if line, _ := entry.String(); strings.Contains(line, providersTestKey) {
			t.Fatalf("log line leaks the raw key: %s", line)
		}
		if entry.Level == log.WarnLevel && strings.Contains(entry.Message, want) {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("missing warning %q", want)
	}

	// The Normalize method returns the warning instead of logging it.
	providers := ProvidersRoutingConfig{Codex: ProviderRoutingConfig{APICredential: "x", Fallback: ProviderFallbackConfig{Target: "api", Mode: "auto"}}}
	warnings, errNormalize := providers.Normalize("routing.providers")
	if errNormalize != nil || len(warnings) != 1 || !strings.HasPrefix(warnings[0], "routing.providers.codex.fallback") {
		t.Fatalf("warnings = %v, err = %v", warnings, errNormalize)
	}
}

func TestProviderFallbackLegacyConversion(t *testing.T) {
	mappings := []ClaudeFallbackMapping{{From: "claude-*", To: "gpt-x"}}
	for _, tc := range []struct {
		legacy ClaudeFallbackConfig
		want   ProviderFallbackConfig
	}{
		{legacy: ClaudeFallbackConfig{Enabled: true, Mode: "always", Mappings: mappings}, want: ProviderFallbackConfig{Target: ProviderFallbackCodex, Mode: "always", Mappings: mappings}},
		{legacy: ClaudeFallbackConfig{Mode: "auto", Mappings: mappings}, want: ProviderFallbackConfig{Target: ProviderFallbackOff, Mode: "auto", Mappings: mappings}},
		{legacy: ClaudeFallbackConfig{}, want: ProviderFallbackConfig{Target: ProviderFallbackOff}},
	} {
		got := ProviderFallbackFromLegacy(tc.legacy)
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("from legacy %+v = %+v, want %+v", tc.legacy, got, tc.want)
		}
		if back := got.LegacyClaudeFallback(); !reflect.DeepEqual(back, tc.legacy) {
			t.Fatalf("round trip of %+v = %+v", tc.legacy, back)
		}
	}

	// Only target codex is enabled in the legacy view; api keeps mode and mappings.
	api := ProviderFallbackConfig{Target: ProviderFallbackAPI, Mode: "auto", Mappings: mappings}
	if view := api.LegacyClaudeFallback(); view.Enabled || view.Mode != "auto" || len(view.Mappings) != 1 {
		t.Fatalf("api legacy view = %+v", view)
	}

	// Conversions copy mappings.
	legacy := ClaudeFallbackConfig{Enabled: true, Mappings: []ClaudeFallbackMapping{{From: "a", To: "b"}}}
	converted := ProviderFallbackFromLegacy(legacy)
	converted.Mappings[0].To = "changed"
	if legacy.Mappings[0].To != "b" {
		t.Fatal("ProviderFallbackFromLegacy aliases mappings")
	}
	view := converted.LegacyClaudeFallback()
	view.Mappings[0].To = "again"
	if converted.Mappings[0].To != "changed" {
		t.Fatal("LegacyClaudeFallback aliases mappings")
	}
}

func TestProviderFallbackMappingTarget(t *testing.T) {
	fallback := ProviderFallbackConfig{Target: ProviderFallbackCodex, Mappings: []ClaudeFallbackMapping{
		{From: "claude-*", To: "gpt-generic"},
		{From: "claude-opus-*", To: "gpt-astra"},
		{From: "claude-opus-special", To: "gpt-exact"},
	}}
	for model, want := range map[string]string{"claude-sonnet-5": "gpt-generic", "claude-opus-5": "gpt-astra", "claude-opus-special": "gpt-exact", "other": ""} {
		if got := fallback.MappingTarget(model); got != want {
			t.Fatalf("MappingTarget(%s) = %q, want %q", model, got, want)
		}
	}
}

func TestEffectiveProvidersConvertsLegacy(t *testing.T) {
	captureStandardLogs(t)
	raw := "config-version: 8\naccess:\n  api-keys: [\"" + providersTestKey + "\"]\nrouting:\n" +
		"  claude-fallback: {enabled: true, mode: always, mappings: [{from: 'claude-*', to: gpt-x}]}\n" +
		"  key-profiles:\n    \"" + providersTestKey + "\":\n      claude-fallback: {enabled: false, mode: auto, mappings: [{from: 'claude-*', to: gpt-y}]}\n"
	cfg, errParse := ParseConfigBytes([]byte(raw))
	if errParse != nil {
		t.Fatal(errParse)
	}
	if !reflect.DeepEqual(cfg.Routing.Providers, ProvidersRoutingConfig{}) {
		t.Fatalf("legacy-only config stored providers %+v", cfg.Routing.Providers)
	}
	shared := cfg.Routing.SharedProfile().EffectiveProviders()
	if shared.Claude.Mode != ProviderModeSubscription || shared.Claude.Fallback.Target != ProviderFallbackCodex || shared.Claude.Fallback.Mode != "always" || shared.Claude.Fallback.MappingTarget("claude-opus") != "gpt-x" {
		t.Fatalf("shared effective = %+v", shared)
	}
	if shared.Codex.Mode != ProviderModeSubscription || shared.Codex.Fallback.Target != ProviderFallbackOff {
		t.Fatalf("shared codex effective = %+v", shared.Codex)
	}
	key := cfg.Routing.KeyProfiles[providersTestKey].EffectiveProviders()
	if key.Claude.Fallback.Target != ProviderFallbackOff || key.Claude.Fallback.Mode != "auto" || key.Claude.Fallback.MappingTarget("claude-x") != "gpt-y" {
		t.Fatalf("key effective = %+v", key)
	}

	// An omitted providers block means subscription and fallback off for both providers.
	empty := RoutingProfile{}.EffectiveProviders()
	want := ProvidersRoutingConfig{
		Claude: ProviderRoutingConfig{Mode: ProviderModeSubscription, Fallback: ProviderFallbackConfig{Target: ProviderFallbackOff}},
		Codex:  ProviderRoutingConfig{Mode: ProviderModeSubscription, Fallback: ProviderFallbackConfig{Target: ProviderFallbackOff}},
	}
	if !reflect.DeepEqual(empty, want) {
		t.Fatalf("empty effective = %+v, want %+v", empty, want)
	}

	// EffectiveProviders does not alias the stored mappings.
	profile := RoutingProfile{ClaudeFallback: ClaudeFallbackConfig{Enabled: true, Mappings: []ClaudeFallbackMapping{{From: "a", To: "b"}}}}
	profile.EffectiveProviders().Claude.Fallback.Mappings[0].To = "changed"
	if profile.ClaudeFallback.Mappings[0].To != "b" {
		t.Fatal("EffectiveProviders aliases legacy mappings")
	}
}

func TestProvidersWinOverLegacyClaudeFallback(t *testing.T) {
	hash := KeyHashPrefix(providersTestKey)
	for scope, raw := range map[string]string{
		"shared": "config-version: 8\naccess:\n  api-keys: [\"" + providersTestKey + "\"]\nrouting:\n" +
			"  claude-fallback: {enabled: true, mappings: [{from: 'claude-*-bad', to: gpt-x}]}\n" +
			"  providers: {claude: {api-credential: idx-1, fallback: {target: api}}}\n",
		"key": "config-version: 8\naccess:\n  api-keys: [\"" + providersTestKey + "\"]\nrouting:\n  key-profiles:\n    \"" + providersTestKey + "\":\n" +
			"      claude-fallback: {enabled: true, mappings: [{from: 'claude-*-bad', to: gpt-x}]}\n" +
			"      providers: {claude: {api-credential: idx-1, fallback: {target: api}}}\n",
	} {
		t.Run(scope, func(t *testing.T) {
			hook := captureStandardLogs(t)
			cfg, errParse := ParseConfigBytes([]byte(raw))
			if errParse != nil {
				t.Fatalf("invalid ignored legacy block failed the load: %v", errParse)
			}
			profile := cfg.Routing.SharedProfile()
			wantPath := "routing.providers.claude.fallback is set"
			if scope == "key" {
				profile = *cfg.Routing.KeyProfiles[providersTestKey]
				wantPath = "routing.key-profiles." + hash + ".providers.claude.fallback is set"
			}
			if !profile.ClaudeFallback.IsZero() {
				t.Fatalf("legacy block kept: %+v", profile.ClaudeFallback)
			}
			if got := profile.EffectiveProviders().Claude.Fallback.Target; got != ProviderFallbackAPI {
				t.Fatalf("effective target = %q, want api", got)
			}
			var warned bool
			for _, entry := range hook.AllEntries() {
				if line, _ := entry.String(); strings.Contains(line, providersTestKey) {
					t.Fatalf("log line leaks the raw key: %s", line)
				}
				if entry.Level == log.WarnLevel && strings.Contains(entry.Message, wantPath) {
					warned = true
				}
			}
			if !warned {
				t.Fatal("missing precedence warning")
			}
		})
	}

	// A providers block without claude.fallback still takes the legacy block,
	// and the combination is validated: mode api with legacy enabled is rejected.
	captureStandardLogs(t)
	_, errParse := ParseConfigBytes([]byte(keyProvidersConfig(`{claude: {mode: api, api-credential: idx-1}}`) +
		"      claude-fallback: {enabled: true, mappings: [{from: 'claude-*', to: gpt-x}]}\n"))
	want := "routing.key-profiles." + hash + ".providers.claude.fallback.target must be off when mode is api"
	if errParse == nil || !strings.Contains(errParse.Error(), want) {
		t.Fatalf("error = %v, want %q", errParse, want)
	}
	cfg, errParse := ParseConfigBytes([]byte(keyProvidersConfig(`{codex: {mode: api, api-credential: idx-2}}`) +
		"      claude-fallback: {enabled: true, mappings: [{from: 'claude-*', to: gpt-x}]}\n"))
	if errParse != nil {
		t.Fatal(errParse)
	}
	effective := cfg.Routing.KeyProfiles[providersTestKey].EffectiveProviders()
	if effective.Claude.Fallback.Target != ProviderFallbackCodex || effective.Codex.Mode != ProviderModeAPI {
		t.Fatalf("effective = %+v", effective)
	}
}

func TestProvidersNormalizeIsIdempotent(t *testing.T) {
	captureStandardLogs(t)
	cfg, errParse := ParseConfigBytes([]byte(sharedProvidersConfig(`{claude: {mode: Subscription, api-credential: ' a ', fallback: {target: CODEX, mode: ' Auto ', mappings: [{from: ' claude-* ', to: gpt-x}]}}, codex: {api-credential: b, fallback: {target: api}}}`)))
	if errParse != nil {
		t.Fatal(errParse)
	}
	first := cfg.Routing.Providers.Clone()
	if errNormalize := cfg.Routing.Normalize(cfg.APIKeys); errNormalize != nil {
		t.Fatal(errNormalize)
	}
	if !reflect.DeepEqual(first, cfg.Routing.Providers) {
		t.Fatalf("second normalize changed providers: %+v -> %+v", first, cfg.Routing.Providers)
	}
	if first.Claude.Fallback.Mode != "auto" || first.Claude.Fallback.Mappings[0].From != "claude-*" {
		t.Fatalf("fallback not canonicalized: %+v", first.Claude.Fallback)
	}
}
