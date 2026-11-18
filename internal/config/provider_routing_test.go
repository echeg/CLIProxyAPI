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
		{name: "codex speed and flag", body: `{codex: {speed: ultrafast, ignore-client-tier: true}}`},
		{name: "claude empty speed and false flag", body: `{claude: {speed: '', ignore-client-tier: false}}`},
		{name: "claude disabled", body: `{claude: {disabled: true}}`},
		{name: "codex disabled keeps settings", body: `{codex: {disabled: true, mode: api, api-credential: idx-2, speed: fast}}`},
		{name: "both disabled", body: `{claude: {disabled: true}, codex: {disabled: true}}`},
		{name: "disabled false", body: `{claude: {disabled: false}, codex: {disabled: false}}`},
		{name: "disabled claude with unknown mode", body: `{claude: {disabled: true, mode: hybrid}}`, wantErr: ".claude.mode has unsupported value"},
		{name: "disabled codex with target codex", body: `{codex: {disabled: true, fallback: {target: codex, mappings: [{from: a, to: b}]}}}`, wantErr: ".codex.fallback.target codex is only supported for claude"},
		{name: "disabled claude with api mode without credential", body: `{claude: {disabled: true, mode: api}}`, wantErr: ".claude.api-credential is required"},
		{name: "unknown codex speed", body: `{codex: {speed: turbo}}`, wantErr: ".codex.speed has unsupported value"},
		{name: "claude speed", body: `{claude: {speed: fast}}`, wantErr: ".claude.speed is only supported for codex"},
		{name: "claude ignore-client-tier", body: `{claude: {ignore-client-tier: true}}`, wantErr: ".claude.ignore-client-tier is only supported for codex"},
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

	// The target is read like Normalize reads it, so a stored policy that was not
	// normalized yet (for example decoded from the file) keeps its legacy view.
	if view := (ProviderFallbackConfig{Target: " CODEX ", Mappings: mappings}).LegacyClaudeFallback(); !view.Enabled {
		t.Fatalf("upper-case codex legacy view = %+v", view)
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

	// An omitted providers block means subscription and fallback off for both
	// providers, and speed standard for Codex only.
	empty := RoutingProfile{}.EffectiveProviders()
	want := ProvidersRoutingConfig{
		Claude: ProviderRoutingConfig{Mode: ProviderModeSubscription, Fallback: ProviderFallbackConfig{Target: ProviderFallbackOff}},
		Codex:  ProviderRoutingConfig{Mode: ProviderModeSubscription, Fallback: ProviderFallbackConfig{Target: ProviderFallbackOff}, Speed: CodexSpeedStandard},
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

func TestProvidersRoutingCodexSpeedNormalize(t *testing.T) {
	cases := []struct {
		name       string
		providers  ProvidersRoutingConfig
		wantErr    string
		wantSpeed  string
		wantIgnore bool
	}{
		{name: "unset", providers: ProvidersRoutingConfig{}},
		{name: "standard", providers: ProvidersRoutingConfig{Codex: ProviderRoutingConfig{Speed: "standard"}}, wantSpeed: CodexSpeedStandard},
		{name: "fast", providers: ProvidersRoutingConfig{Codex: ProviderRoutingConfig{Speed: "fast"}}, wantSpeed: CodexSpeedFast},
		{name: "ultrafast with flag", providers: ProvidersRoutingConfig{Codex: ProviderRoutingConfig{Speed: "ultrafast", IgnoreClientTier: true}}, wantSpeed: CodexSpeedUltrafast, wantIgnore: true},
		{name: "case and whitespace", providers: ProvidersRoutingConfig{Codex: ProviderRoutingConfig{Speed: "  UltraFast "}}, wantSpeed: CodexSpeedUltrafast},
		{name: "blank speed stays empty", providers: ProvidersRoutingConfig{Codex: ProviderRoutingConfig{Speed: "   "}}},
		{name: "flag only", providers: ProvidersRoutingConfig{Codex: ProviderRoutingConfig{IgnoreClientTier: true}}, wantIgnore: true},
		{name: "unknown value", providers: ProvidersRoutingConfig{Codex: ProviderRoutingConfig{Speed: "priority"}}, wantErr: `routing.providers.codex.speed has unsupported value "priority"`},
		{name: "unknown value under claude", providers: ProvidersRoutingConfig{Claude: ProviderRoutingConfig{Speed: "turbo"}}, wantErr: `routing.providers.claude.speed has unsupported value "turbo"`},
		{name: "claude speed", providers: ProvidersRoutingConfig{Claude: ProviderRoutingConfig{Speed: "fast"}}, wantErr: "routing.providers.claude.speed is only supported for codex"},
		{name: "claude standard speed", providers: ProvidersRoutingConfig{Claude: ProviderRoutingConfig{Speed: "standard"}}, wantErr: "routing.providers.claude.speed is only supported for codex"},
		{name: "claude flag", providers: ProvidersRoutingConfig{Claude: ProviderRoutingConfig{IgnoreClientTier: true}}, wantErr: "routing.providers.claude.ignore-client-tier is only supported for codex"},
		{name: "claude empty speed and false flag", providers: ProvidersRoutingConfig{Claude: ProviderRoutingConfig{Speed: " ", IgnoreClientTier: false}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			providers := tc.providers
			_, errNormalize := providers.Normalize("routing.providers")
			if tc.wantErr != "" {
				if errNormalize == nil || errNormalize.Error() != tc.wantErr {
					t.Fatalf("error = %v, want %q", errNormalize, tc.wantErr)
				}
				return
			}
			if errNormalize != nil {
				t.Fatalf("unexpected error: %v", errNormalize)
			}
			if providers.Codex.Speed != tc.wantSpeed || providers.Codex.IgnoreClientTier != tc.wantIgnore {
				t.Fatalf("stored codex = %+v, want speed %q ignore %v", providers.Codex, tc.wantSpeed, tc.wantIgnore)
			}
			if providers.Claude.Speed != "" || providers.Claude.IgnoreClientTier {
				t.Fatalf("stored claude = %+v", providers.Claude)
			}
		})
	}
}

func TestProvidersRoutingCodexSpeedDefaults(t *testing.T) {
	captureStandardLogs(t)
	raw := "config-version: 8\naccess:\n  api-keys: [\"" + providersTestKey + "\", \"other-key\"]\nrouting:\n" +
		"  providers: {codex: {mode: subscription}}\n" +
		"  key-profiles:\n    \"" + providersTestKey + "\":\n      session-affinity: true\n" +
		"    other-key:\n      providers: {codex: {speed: Fast, ignore-client-tier: true}}\n"
	cfg, errParse := ParseConfigBytes([]byte(raw))
	if errParse != nil {
		t.Fatal(errParse)
	}
	shared := cfg.Routing.SharedProfile()
	key := *cfg.Routing.KeyProfiles[providersTestKey]
	for name, profile := range map[string]RoutingProfile{"shared": shared, "key": key} {
		if profile.Providers.Codex.Speed != "" || profile.Providers.Codex.IgnoreClientTier {
			t.Fatalf("%s stored codex = %+v, want empty", name, profile.Providers.Codex)
		}
		effective := profile.EffectiveProviders()
		if effective.Codex.Speed != CodexSpeedStandard || effective.Codex.IgnoreClientTier {
			t.Fatalf("%s effective codex = %+v, want standard", name, effective.Codex)
		}
		if effective.Claude.Speed != "" || effective.Claude.IgnoreClientTier {
			t.Fatalf("%s effective claude = %+v, want empty speed", name, effective.Claude)
		}
	}
	other := cfg.Routing.KeyProfiles["other-key"]
	if other.Providers.Codex.Speed != CodexSpeedFast || !other.Providers.Codex.IgnoreClientTier {
		t.Fatalf("other key stored codex = %+v", other.Providers.Codex)
	}
	if effective := other.EffectiveProviders().Codex; effective.Speed != CodexSpeedFast || !effective.IgnoreClientTier {
		t.Fatalf("other key effective codex = %+v", effective)
	}

	// The effective view passes validation again for both scopes.
	for name, profile := range map[string]RoutingProfile{"shared": shared, "key": key} {
		providers := profile.Providers.Clone()
		legacy := profile.ClaudeFallback
		if errNormalize := normalizeProfileProviders(&providers, &legacy, "routing.providers", ""); errNormalize != nil {
			t.Fatalf("%s: %v", name, errNormalize)
		}
	}
}

func TestProvidersRoutingDisabledFields(t *testing.T) {
	captureStandardLogs(t)
	cfg, errParse := ParseConfigBytes([]byte(keyProvidersConfig(`{claude: {disabled: true, mode: api, api-credential: idx-1}, codex: {disabled: true, speed: fast}}`)))
	if errParse != nil {
		t.Fatal(errParse)
	}
	got := cfg.Routing.KeyProfiles[providersTestKey].EffectiveProviders()
	if !got.Claude.Disabled || got.Claude.Mode != ProviderModeAPI || got.Claude.APICredential != "idx-1" {
		t.Fatalf("effective claude = %+v", got.Claude)
	}
	if !got.Codex.Disabled || got.Codex.Speed != CodexSpeedFast {
		t.Fatalf("effective codex = %+v", got.Codex)
	}
	shared := cfg.Routing.SharedProfile().EffectiveProviders()
	if shared.Claude.Disabled || shared.Codex.Disabled {
		t.Fatalf("shared profile disabled by default: %+v", shared)
	}
}

func TestProvidersRoutingWarnsOnFallbackIntoDisabledCodex(t *testing.T) {
	hash := KeyHashPrefix(providersTestKey)
	const fallback = `fallback: {target: codex, mappings: [{from: 'claude-*', to: gpt-x}]}`
	const legacy = `claude-fallback: {enabled: true, mappings: [{from: 'claude-*', to: gpt-x}]}`
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "shared",
			raw:  sharedProvidersConfig(`{claude: {` + fallback + `}, codex: {disabled: true}}`),
			want: "routing.providers.claude.fallback: target codex is inactive while routing.providers.codex.disabled is true",
		},
		{
			name: "key profile",
			raw:  keyProvidersConfig(`{claude: {` + fallback + `}, codex: {disabled: true}}`),
			want: "routing.key-profiles." + hash + ".providers.claude.fallback: target codex is inactive while routing.key-profiles." + hash + ".providers.codex.disabled is true",
		},
		{
			name: "shared legacy",
			raw:  "config-version: 8\naccess:\n  api-keys: [\"" + providersTestKey + "\"]\nrouting:\n  " + legacy + "\n  providers: {codex: {disabled: true}}\n",
			want: "routing.claude-fallback: target codex is inactive while routing.providers.codex.disabled is true",
		},
		{
			name: "key profile legacy",
			raw:  "config-version: 8\naccess:\n  api-keys: [\"" + providersTestKey + "\"]\nrouting:\n  key-profiles:\n    \"" + providersTestKey + "\":\n      " + legacy + "\n      providers: {codex: {disabled: true}}\n",
			want: "routing.key-profiles." + hash + ".claude-fallback: target codex is inactive while routing.key-profiles." + hash + ".providers.codex.disabled is true",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hook := captureStandardLogs(t)
			if _, errParse := ParseConfigBytes([]byte(tc.raw)); errParse != nil {
				t.Fatalf("unexpected error: %v", errParse)
			}
			var warned bool
			for _, entry := range hook.AllEntries() {
				if line, _ := entry.String(); strings.Contains(line, providersTestKey) {
					t.Fatalf("log line leaks the raw key: %s", line)
				}
				if entry.Level == log.WarnLevel && entry.Message == tc.want {
					warned = true
				}
			}
			if !warned {
				var got []string
				for _, entry := range hook.AllEntries() {
					got = append(got, entry.Message)
				}
				t.Fatalf("missing warning %q; got %q", tc.want, got)
			}
		})
	}

	t.Run("no warning when codex is enabled or the fallback is off", func(t *testing.T) {
		for _, raw := range []string{
			sharedProvidersConfig(`{claude: {` + fallback + `}}`),
			sharedProvidersConfig(`{claude: {disabled: true}, codex: {disabled: true}}`),
		} {
			hook := captureStandardLogs(t)
			if _, errParse := ParseConfigBytes([]byte(raw)); errParse != nil {
				t.Fatalf("unexpected error: %v", errParse)
			}
			for _, entry := range hook.AllEntries() {
				if strings.Contains(entry.Message, "is inactive") {
					t.Fatalf("unexpected warning %q for %s", entry.Message, raw)
				}
			}
		}
	})
}
