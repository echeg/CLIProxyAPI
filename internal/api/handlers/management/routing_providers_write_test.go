package management

import (
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"gopkg.in/yaml.v3"
)

const providersWriteKey = "client-key-a-secret"

// serveConfigV8 sends one config write and returns the recorder.
func serveConfigV8(t *testing.T, h *Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	router := gin.New()
	router.Handle(method, "/v8/management/config", h.ConfigV8)
	router.Handle(method, "/v8/management/config/*path", h.ConfigV8)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(method, target, strings.NewReader(body)))
	return rec
}

// savedRoutingNode returns the value at a routing path of the saved file, or nil.
func savedRoutingNode(t *testing.T, path string, parts ...string) *yaml.Node {
	t.Helper()
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	var doc yaml.Node
	if errUnmarshal := yaml.Unmarshal(raw, &doc); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	return configV8Node(doc.Content[0], append([]string{"routing"}, parts...))
}

// storedClaudeBlock renders a providers.claude block with the given stored target.
func storedClaudeBlock(target string) string {
	block := `{api-credential: claude-idx, fallback: {target: ` + target
	if target == config.ProviderFallbackCodex {
		block += `, mode: always, mappings: [{from: "claude-*", to: "gpt-6-sol"}]`
	}
	return block + `}}`
}

func TestConfigV8LegacyClaudeFallbackWriteMergesIntoProviders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	legacyBody := func(enabled bool) string {
		return `{"enabled":` + map[bool]string{true: "true", false: "false"}[enabled] + `,"mode":"auto","mappings":[{"from":"claude-opus-*","to":"gpt-6-astra"}]}`
	}
	wantMappings := []config.ClaudeFallbackMapping{{From: "claude-opus-*", To: "gpt-6-astra"}}

	for _, scope := range []string{"shared", "key"} {
		for _, stored := range []string{config.ProviderFallbackOff, config.ProviderFallbackCodex, config.ProviderFallbackAPI} {
			for _, enabled := range []bool{true, false} {
				name := scope + "/stored-" + stored + "/enabled-" + map[bool]string{true: "true", false: "false"}[enabled]
				t.Run(name, func(t *testing.T) {
					providers := `{claude: ` + storedClaudeBlock(stored) + `, codex: {mode: api, api-credential: codex-idx}}`
					raw := "config-version: 8\naccess:\n  api-keys: [\"" + providersWriteKey + "\"]\nrouting:\n"
					var patch string
					var parts []string
					if scope == "shared" {
						raw += "  providers: " + providers + "\n"
						patch = `{"routing":{"claude-fallback":` + legacyBody(enabled) + `}}`
					} else {
						raw += "  key-profiles:\n    \"" + providersWriteKey + "\":\n      session-affinity: true\n      providers: " + providers + "\n"
						patch = `{"routing":{"key-profiles":{"` + providersWriteKey + `":{"session-affinity":true,"claude-fallback":` + legacyBody(enabled) + `}}}}`
						parts = []string{"key-profiles", providersWriteKey}
					}
					h, path := newRoutingProfilesTestHandler(t, raw)
					if rec := serveConfigV8(t, h, http.MethodPatch, "/v8/management/config", patch); rec.Code != http.StatusOK {
						t.Fatalf("patch status %d: %s", rec.Code, rec.Body.String())
					}

					wantTarget := config.ProviderFallbackOff
					switch {
					case enabled:
						wantTarget = config.ProviderFallbackCodex
					case stored == config.ProviderFallbackAPI:
						wantTarget = config.ProviderFallbackAPI
					}
					loaded, errLoad := config.LoadConfig(path)
					if errLoad != nil {
						t.Fatal(errLoad)
					}
					profile := loaded.Routing.SharedProfile()
					if scope == "key" {
						profile = *loaded.Routing.KeyProfiles[providersWriteKey]
					}
					want := config.ProvidersRoutingConfig{
						Claude: config.ProviderRoutingConfig{APICredential: "claude-idx", Fallback: config.ProviderFallbackConfig{Target: wantTarget, Mode: "auto", Mappings: wantMappings}},
						Codex:  config.ProviderRoutingConfig{Mode: config.ProviderModeAPI, APICredential: "codex-idx"},
					}
					if !reflect.DeepEqual(profile.Providers, want) {
						t.Fatalf("providers = %+v, want %+v", profile.Providers, want)
					}
					if node := savedRoutingNode(t, path, append(parts, "claude-fallback")...); node != nil {
						t.Fatal("legacy claude-fallback node kept after the merge")
					}

					// The legacy read view mirrors the write, so a panel verifying it sees its own value.
					out, _ := getRoutingProfiles(t, h)
					view := out.Shared.ClaudeFallback
					if scope == "key" {
						view = out.Keys[0].Profile.ClaudeFallback
					}
					if view.Enabled != enabled || view.Mode != "auto" || !reflect.DeepEqual(view.Mappings, wantMappings) {
						t.Fatalf("legacy view = %+v", view)
					}
				})
			}
		}
	}
}

func TestConfigV8LegacyClaudeFallbackWriteEdgeCases(t *testing.T) {
	gin.SetMode(gin.TestMode)
	keyPrefix := "config-version: 8\naccess:\n  api-keys: [\"" + providersWriteKey + "\"]\nrouting:\n  key-profiles:\n    \"" + providersWriteKey + "\":\n"

	t.Run("legacy-only file converts on a legacy write", func(t *testing.T) {
		h, path := newRoutingProfilesTestHandler(t, keyPrefix+"      claude-fallback: {enabled: true, mappings: [{from: \"claude-*\", to: \"gpt-6-sol\"}]}\n")
		patch := `{"routing":{"key-profiles":{"` + providersWriteKey + `":{"claude-fallback":{"enabled":false}}}}}`
		if rec := serveConfigV8(t, h, http.MethodPatch, "/v8/management/config", patch); rec.Code != http.StatusOK {
			t.Fatalf("patch status %d: %s", rec.Code, rec.Body.String())
		}
		if savedRoutingNode(t, path, "key-profiles", providersWriteKey, "claude-fallback") != nil {
			t.Fatal("legacy node kept")
		}
		got := h.cfg.Routing.KeyProfiles[providersWriteKey].Providers.Claude.Fallback
		want := config.ProviderFallbackConfig{Target: config.ProviderFallbackOff, Mappings: []config.ClaudeFallbackMapping{{From: "claude-*", To: "gpt-6-sol"}}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("fallback = %+v, want %+v", got, want)
		}
	})

	t.Run("PUT of a legacy profile keeps the stored providers", func(t *testing.T) {
		h, _ := newRoutingProfilesTestHandler(t, keyPrefix+"      providers: {codex: {mode: api, api-credential: codex-idx}, claude: "+storedClaudeBlock(config.ProviderFallbackAPI)+"}\n")
		body := `{"session-affinity":true,"claude-fallback":{"enabled":false}}`
		if rec := serveConfigV8(t, h, http.MethodPut, "/v8/management/config/routing/key-profiles/"+providersWriteKey, body); rec.Code != http.StatusOK {
			t.Fatalf("put status %d: %s", rec.Code, rec.Body.String())
		}
		profile := h.cfg.Routing.KeyProfiles[providersWriteKey]
		if !profile.SessionAffinity || profile.Providers.Codex.APICredential != "codex-idx" || profile.Providers.Claude.APICredential != "claude-idx" || profile.Providers.Claude.Fallback.Target != config.ProviderFallbackAPI {
			t.Fatalf("profile = %+v", profile)
		}
	})

	t.Run("providers write drops the stored legacy node", func(t *testing.T) {
		h, path := newRoutingProfilesTestHandler(t, "config-version: 8\nrouting:\n  claude-fallback: {enabled: true, mappings: [{from: \"claude-*\", to: \"gpt-6-sol\"}]}\n")
		patch := `{"routing":{"providers":{"claude":{"api-credential":"claude-idx","fallback":{"target":"api"}}}}}`
		if rec := serveConfigV8(t, h, http.MethodPatch, "/v8/management/config", patch); rec.Code != http.StatusOK {
			t.Fatalf("patch status %d: %s", rec.Code, rec.Body.String())
		}
		if savedRoutingNode(t, path, "claude-fallback") != nil {
			t.Fatal("legacy node kept")
		}
		if got := h.cfg.Routing.SharedProfile().EffectiveProviders().Claude.Fallback.Target; got != config.ProviderFallbackAPI {
			t.Fatalf("target = %q, want api", got)
		}
	})

	t.Run("partial legacy PATCH keeps the stored mode and mappings", func(t *testing.T) {
		h, _ := newRoutingProfilesTestHandler(t, "config-version: 8\nrouting:\n  providers: {claude: "+storedClaudeBlock(config.ProviderFallbackCodex)+"}\n")
		wantMappings := []config.ClaudeFallbackMapping{{From: "claude-*", To: "gpt-6-sol"}}
		for _, step := range []struct {
			body, target string
		}{
			{`{"enabled":false}`, config.ProviderFallbackOff},
			{`{"enabled":true}`, config.ProviderFallbackCodex},
		} {
			if rec := serveConfigV8(t, h, http.MethodPatch, "/v8/management/config/routing/claude-fallback", step.body); rec.Code != http.StatusOK {
				t.Fatalf("patch %s status %d: %s", step.body, rec.Code, rec.Body.String())
			}
			got := h.cfg.Routing.SharedProfile().EffectiveProviders().Claude
			want := config.ProviderFallbackConfig{Target: step.target, Mode: "always", Mappings: wantMappings}
			if got.APICredential != "claude-idx" || !reflect.DeepEqual(got.Fallback, want) {
				t.Fatalf("after %s: claude = %+v, want fallback %+v", step.body, got, want)
			}
		}
	})

	t.Run("legacy PATCH of a null field resets that field", func(t *testing.T) {
		wantMappings := []config.ClaudeFallbackMapping{{From: "claude-*", To: "gpt-6-sol"}}
		for _, tc := range []struct {
			body string
			want config.ProviderFallbackConfig
		}{
			{`{"enabled":null}`, config.ProviderFallbackConfig{Target: config.ProviderFallbackOff, Mode: "always", Mappings: wantMappings}},
			{`{"mode":null}`, config.ProviderFallbackConfig{Target: config.ProviderFallbackCodex, Mappings: wantMappings}},
		} {
			h, _ := newRoutingProfilesTestHandler(t, "config-version: 8\nrouting:\n  providers: {claude: "+storedClaudeBlock(config.ProviderFallbackCodex)+"}\n")
			if rec := serveConfigV8(t, h, http.MethodPatch, "/v8/management/config/routing/claude-fallback", tc.body); rec.Code != http.StatusOK {
				t.Fatalf("patch %s status %d: %s", tc.body, rec.Code, rec.Body.String())
			}
			if got := h.cfg.Routing.SharedProfile().EffectiveProviders().Claude.Fallback; !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("after %s: fallback = %+v, want %+v", tc.body, got, tc.want)
			}
		}
	})

	t.Run("legacy write ignores a superseded legacy block", func(t *testing.T) {
		raw := "config-version: 8\nrouting:\n  providers: {claude: {fallback: {target: off}}}\n  claude-fallback: {enabled: true, mappings: [{from: \"claude-*\", to: \"gpt-6-sol\"}]}\n"
		for _, write := range []struct{ method, target, body string }{
			{http.MethodPatch, "/v8/management/config/routing/claude-fallback", `{"mode":"auto"}`},
			{http.MethodPut, "/v8/management/config/routing/claude-fallback/mode", `"auto"`},
		} {
			h, path := newRoutingProfilesTestHandler(t, raw)
			if rec := serveConfigV8(t, h, write.method, write.target, write.body); rec.Code != http.StatusOK {
				t.Fatalf("%s %s status %d: %s", write.method, write.target, rec.Code, rec.Body.String())
			}
			want := config.ProviderFallbackConfig{Target: config.ProviderFallbackOff, Mode: "auto"}
			if got := h.cfg.Routing.SharedProfile().EffectiveProviders().Claude.Fallback; !reflect.DeepEqual(got, want) {
				t.Fatalf("%s: fallback = %+v, want %+v", write.method, got, want)
			}
			if savedRoutingNode(t, path, "claude-fallback") != nil {
				t.Fatalf("%s: legacy node kept", write.method)
			}
		}
	})

	t.Run("PUT of a legacy field keeps the other stored fields", func(t *testing.T) {
		h, _ := newRoutingProfilesTestHandler(t, "config-version: 8\nrouting:\n  providers: {claude: "+storedClaudeBlock(config.ProviderFallbackCodex)+"}\n")
		if rec := serveConfigV8(t, h, http.MethodPut, "/v8/management/config/routing/claude-fallback/mode", `"auto"`); rec.Code != http.StatusOK {
			t.Fatalf("put status %d: %s", rec.Code, rec.Body.String())
		}
		want := config.ProviderFallbackConfig{Target: config.ProviderFallbackCodex, Mode: "auto", Mappings: []config.ClaudeFallbackMapping{{From: "claude-*", To: "gpt-6-sol"}}}
		if got := h.cfg.Routing.SharedProfile().EffectiveProviders().Claude.Fallback; !reflect.DeepEqual(got, want) {
			t.Fatalf("fallback = %+v, want %+v", got, want)
		}
	})

	t.Run("legacy PATCH reads a stored target in any case", func(t *testing.T) {
		h, _ := newRoutingProfilesTestHandler(t, "config-version: 8\nrouting:\n  providers: {claude: {fallback: {target: CODEX, mode: always, mappings: [{from: \"claude-*\", to: \"gpt-6-sol\"}]}}}\n")
		if rec := serveConfigV8(t, h, http.MethodPatch, "/v8/management/config/routing/claude-fallback", `{"mode":"auto"}`); rec.Code != http.StatusOK {
			t.Fatalf("patch status %d: %s", rec.Code, rec.Body.String())
		}
		want := config.ProviderFallbackConfig{Target: config.ProviderFallbackCodex, Mode: "auto", Mappings: []config.ClaudeFallbackMapping{{From: "claude-*", To: "gpt-6-sol"}}}
		if got := h.cfg.Routing.SharedProfile().EffectiveProviders().Claude.Fallback; !reflect.DeepEqual(got, want) {
			t.Fatalf("fallback = %+v, want %+v", got, want)
		}
	})

	t.Run("legacy PATCH of null turns the fallback off", func(t *testing.T) {
		for _, stored := range []string{config.ProviderFallbackCodex, config.ProviderFallbackAPI} {
			for _, write := range []struct{ name, target, body string }{
				{"shared", "/v8/management/config", `{"routing":{"claude-fallback":null}}`},
				{"shared-path", "/v8/management/config/routing/claude-fallback", `null`},
				{"key", "/v8/management/config", `{"routing":{"key-profiles":{"` + providersWriteKey + `":{"claude-fallback":null}}}}`},
			} {
				t.Run(stored+"/"+write.name, func(t *testing.T) {
					block := "providers: {claude: " + storedClaudeBlock(stored) + "}\n"
					h, path := newRoutingProfilesTestHandler(t, keyPrefix+"      "+block+"  "+block)
					if rec := serveConfigV8(t, h, http.MethodPatch, write.target, write.body); rec.Code != http.StatusOK {
						t.Fatalf("patch status %d: %s", rec.Code, rec.Body.String())
					}
					profile, parts := h.cfg.Routing.SharedProfile(), []string{}
					if write.name == "key" {
						profile, parts = *h.cfg.Routing.KeyProfiles[providersWriteKey], []string{"key-profiles", providersWriteKey}
					}
					want := config.ProviderFallbackConfig{Target: config.ProviderFallbackOff}
					if stored == config.ProviderFallbackAPI {
						want.Target = config.ProviderFallbackAPI
					}
					if got := profile.EffectiveProviders().Claude; got.APICredential != "claude-idx" || !reflect.DeepEqual(got.Fallback, want) {
						t.Fatalf("claude = %+v, want fallback %+v", got, want)
					}
					if savedRoutingNode(t, path, append(parts, "claude-fallback")...) != nil {
						t.Fatal("legacy node kept")
					}
				})
			}
		}
	})

	t.Run("DELETE of a converted legacy path turns the fallback off", func(t *testing.T) {
		raw := keyPrefix + "      providers: {claude: " + storedClaudeBlock(config.ProviderFallbackCodex) + "}\n  providers: {claude: " + storedClaudeBlock(config.ProviderFallbackCodex) + "}\n"
		h, path := newRoutingProfilesTestHandler(t, raw)
		for _, target := range []string{"/v8/management/config/routing/claude-fallback", "/v8/management/config/routing/key-profiles/" + providersWriteKey + "/claude-fallback"} {
			if rec := serveConfigV8(t, h, http.MethodDelete, target, ""); rec.Code != http.StatusOK {
				t.Fatalf("delete %s status %d: %s", target, rec.Code, rec.Body.String())
			}
		}
		for _, profile := range []config.RoutingProfile{h.cfg.Routing.SharedProfile(), *h.cfg.Routing.KeyProfiles[providersWriteKey]} {
			if got := profile.EffectiveProviders().Claude; got.Fallback.Target != config.ProviderFallbackOff || got.APICredential != "claude-idx" {
				t.Fatalf("claude = %+v, want fallback off with the credential kept", got)
			}
		}
		if savedRoutingNode(t, path, "providers", "claude", "fallback") != nil {
			t.Fatal("converted fallback kept on disk")
		}
	})

	t.Run("DELETE of a legacy path keeps an api target", func(t *testing.T) {
		h, _ := newRoutingProfilesTestHandler(t, "config-version: 8\nrouting:\n  providers: {claude: "+storedClaudeBlock(config.ProviderFallbackAPI)+"}\n")
		if rec := serveConfigV8(t, h, http.MethodDelete, "/v8/management/config/routing/claude-fallback", ""); rec.Code != http.StatusNotFound {
			t.Fatalf("delete status %d: %s", rec.Code, rec.Body.String())
		}
		if got := h.cfg.Routing.SharedProfile().EffectiveProviders().Claude.Fallback.Target; got != config.ProviderFallbackAPI {
			t.Fatalf("target = %q, want api", got)
		}
	})

	t.Run("unrelated write leaves routing untouched", func(t *testing.T) {
		raw := "config-version: 8\nserver:\n  port: 8317\nrouting:\n  claude-fallback: {enabled: true, mappings: [{from: \"claude-*\", to: \"gpt-6-sol\"}]}\n  providers: {codex: {mode: api, api-credential: codex-idx}}\n"
		h, path := newRoutingProfilesTestHandler(t, raw)
		if rec := serveConfigV8(t, h, http.MethodPatch, "/v8/management/config", `{"server":{"port":8318}}`); rec.Code != http.StatusOK {
			t.Fatalf("patch status %d: %s", rec.Code, rec.Body.String())
		}
		if savedRoutingNode(t, path, "claude-fallback") == nil || savedRoutingNode(t, path, "providers", "claude") != nil {
			t.Fatal("unrelated write rewrote the routing profile")
		}
	})
}

func TestConfigV8InvalidProvidersWriteRejected(t *testing.T) {
	gin.SetMode(gin.TestMode)
	raw := "config-version: 8\naccess:\n  api-keys: [\"" + providersWriteKey + "\"]\nrouting:\n  providers: {codex: {mode: api, api-credential: codex-idx}}\n  key-profiles:\n    \"" + providersWriteKey + "\":\n      providers: " + "{claude: " + storedClaudeBlock(config.ProviderFallbackAPI) + "}\n"
	hash := config.KeyHashPrefix(providersWriteKey)
	cases := []struct {
		name, patch, wantPath string
	}{
		{"unknown mode", `{"routing":{"providers":{"claude":{"mode":"hybrid"}}}}`, "routing.providers.claude.mode"},
		{"unknown target", `{"routing":{"key-profiles":{"` + providersWriteKey + `":{"providers":{"codex":{"fallback":{"target":"claude"}}}}}}}`, "routing.key-profiles." + hash + ".providers.codex.fallback.target"},
		{"api mode without credential", `{"routing":{"key-profiles":{"` + providersWriteKey + `":{"providers":{"codex":{"mode":"api"}}}}}}`, "routing.key-profiles." + hash + ".providers.codex.api-credential"},
		{"codex target without mappings", `{"routing":{"key-profiles":{"` + providersWriteKey + `":{"providers":{"claude":{"fallback":{"target":"codex","mappings":null}}}}}}}`, "routing.key-profiles." + hash + ".providers.claude.fallback"},
		{"codex target on codex", `{"routing":{"providers":{"codex":{"mode":"subscription","fallback":{"target":"codex"}}}}}`, "routing.providers.codex.fallback.target"},
		{"api fallback without credential", `{"routing":{"key-profiles":{"` + providersWriteKey + `":{"providers":{"codex":{"fallback":{"target":"api"}}}}}}}`, "routing.key-profiles." + hash + ".providers.codex.api-credential"},
		{"api mode with a fallback", `{"routing":{"key-profiles":{"` + providersWriteKey + `":{"providers":{"claude":{"mode":"api"}}}}}}`, "routing.key-profiles." + hash + ".providers.claude.fallback.target"},
		{"unknown codex speed", `{"routing":{"key-profiles":{"` + providersWriteKey + `":{"providers":{"codex":{"speed":"turbo"}}}}}}`, "routing.key-profiles." + hash + ".providers.codex.speed"},
		{"speed under claude", `{"routing":{"providers":{"claude":{"speed":"fast"}}}}`, "routing.providers.claude.speed"},
		{"ignore-client-tier under claude", `{"routing":{"key-profiles":{"` + providersWriteKey + `":{"providers":{"claude":{"ignore-client-tier":true}}}}}}`, "routing.key-profiles." + hash + ".providers.claude.ignore-client-tier"},
		{"legacy enable under api mode", `{"routing":{"providers":{"claude":{"mode":"api","api-credential":"claude-idx"}},"claude-fallback":{"enabled":true,"mappings":[{"from":"claude-*","to":"gpt-6-sol"}]}}}`, "routing.providers.claude.fallback.target"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, path := newRoutingProfilesTestHandler(t, raw)
			before, errBefore := os.ReadFile(path)
			if errBefore != nil {
				t.Fatal(errBefore)
			}
			rec := serveConfigV8(t, h, http.MethodPatch, "/v8/management/config", tc.patch)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			var body struct {
				Error   string `json:"error"`
				Message string `json:"message"`
			}
			if errDecode := json.Unmarshal(rec.Body.Bytes(), &body); errDecode != nil {
				t.Fatal(errDecode)
			}
			if body.Error != "invalid_config" || !strings.Contains(body.Message, tc.wantPath) || strings.Contains(rec.Body.String(), providersWriteKey) {
				t.Fatalf("body = %s, want invalid_config at %s without the key", rec.Body.String(), tc.wantPath)
			}
			after, errAfter := os.ReadFile(path)
			if errAfter != nil {
				t.Fatal(errAfter)
			}
			if string(after) != string(before) {
				t.Fatal("rejected write changed the file")
			}
		})
	}
}

func TestConfigV8LegacyClaudeFallbackNextToZeroNode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	legacy := "claude-fallback: {enabled: true, mappings: [{from: \"claude-*\", to: \"gpt-6-sol\"}]}"
	wantMappings := []config.ClaudeFallbackMapping{{From: "claude-*", To: "gpt-6-sol"}}
	keyParts := []string{"key-profiles", providersWriteKey}
	// profileRaw renders a file whose shared profile and key profile both hold
	// the given claude.fallback node next to an enabled legacy block.
	profileRaw := func(fallback string) string {
		providers := "providers: {claude: {fallback: " + fallback + "}}"
		return "config-version: 8\naccess:\n  api-keys: [\"" + providersWriteKey + "\"]\nrouting:\n" +
			"  " + providers + "\n  " + legacy + "\n" +
			"  key-profiles:\n    \"" + providersWriteKey + "\":\n      " + providers + "\n      " + legacy + "\n"
	}
	// assertLegacyKept checks that the legacy node is still on disk for a
	// profile and that the loaded config still routes Claude to Codex.
	assertLegacyKept := func(t *testing.T, path string, parts []string) {
		t.Helper()
		if savedRoutingNode(t, path, append(append([]string{}, parts...), "claude-fallback")...) == nil {
			t.Fatalf("%v: legacy claude-fallback node dropped", parts)
		}
		loaded, errLoad := config.LoadConfig(path)
		if errLoad != nil {
			t.Fatal(errLoad)
		}
		profile := loaded.Routing.SharedProfile()
		if len(parts) > 0 {
			profile = *loaded.Routing.KeyProfiles[providersWriteKey]
		}
		got := profile.EffectiveProviders().Claude.Fallback
		if got.Target != config.ProviderFallbackCodex || !reflect.DeepEqual(got.Mappings, wantMappings) {
			t.Fatalf("%v: fallback = %+v, want target codex with the legacy mappings", parts, got)
		}
	}

	for _, zero := range []struct{ name, node string }{
		{"empty", "{}"},
		{"null", "null"},
		{"empty-fields", "{target: \"\", mode: \"\", mappings: []}"},
	} {
		for _, write := range []struct{ name, method, target, body string }{
			{"shared-speed", http.MethodPatch, "/v8/management/config", `{"routing":{"providers":{"codex":{"speed":"fast"}}}}`},
			{"shared-disabled", http.MethodPatch, "/v8/management/config", `{"routing":{"providers":{"codex":{"disabled":true}}}}`},
			{"key-speed", http.MethodPatch, "/v8/management/config", `{"routing":{"key-profiles":{"` + providersWriteKey + `":{"providers":{"codex":{"speed":"fast"}}}}}}`},
			{"shared-speed-path", http.MethodPut, "/v8/management/config/routing/providers/codex/speed", `"fast"`},
		} {
			t.Run(zero.name+"/"+write.name, func(t *testing.T) {
				h, path := newRoutingProfilesTestHandler(t, profileRaw(zero.node))
				if rec := serveConfigV8(t, h, write.method, write.target, write.body); rec.Code != http.StatusOK {
					t.Fatalf("%s %s status %d: %s", write.method, write.target, rec.Code, rec.Body.String())
				}
				assertLegacyKept(t, path, nil)
				assertLegacyKept(t, path, keyParts)
				if savedRoutingNode(t, path, "providers", "claude", "fallback") == nil {
					t.Fatal("stored zero fallback node dropped")
				}
			})
		}
	}

	// A write naming providers.claude.fallback, a stored non-zero node and a
	// legacy-only write still drop the legacy block, as before.
	for _, tc := range []struct{ name, stored, body, wantTarget string }{
		{"write names an empty node", "{}", `{"routing":{"providers":{"claude":{"fallback":{}}}}}`, config.ProviderFallbackOff},
		{"write names a null node", "{}", `{"routing":{"providers":{"claude":{"fallback":null}}}}`, config.ProviderFallbackOff},
		{"write names target off", "{}", `{"routing":{"providers":{"claude":{"fallback":{"target":"off"}}}}}`, config.ProviderFallbackOff},
		{"stored non-zero node", "{target: codex, mappings: [{from: \"claude-*\", to: \"gpt-6-sol\"}]}", `{"routing":{"providers":{"codex":{"speed":"fast"}}}}`, config.ProviderFallbackCodex},
		{"legacy-only write", "{}", `{"routing":{"claude-fallback":{"enabled":true,"mappings":[{"from":"claude-*","to":"gpt-6-sol"}]}}}`, config.ProviderFallbackCodex},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, path := newRoutingProfilesTestHandler(t, profileRaw(tc.stored))
			if rec := serveConfigV8(t, h, http.MethodPatch, "/v8/management/config", tc.body); rec.Code != http.StatusOK {
				t.Fatalf("patch status %d: %s", rec.Code, rec.Body.String())
			}
			if savedRoutingNode(t, path, "claude-fallback") != nil {
				t.Fatal("shared legacy node kept")
			}
			if got := h.cfg.Routing.SharedProfile().EffectiveProviders().Claude.Fallback.Target; got != tc.wantTarget {
				t.Fatalf("target = %q, want %q", got, tc.wantTarget)
			}
			// The key profile is not named by the write and keeps its shape.
			assertLegacyKept(t, path, keyParts)
		})
	}

	// A write that sends the legacy block next to a zero node it names, such as
	// a routing node read back whole with only the Codex speed changed, means
	// target codex on load, so the legacy block it sends is converted rather
	// than dropped with the fallback turned off.
	assertConverted := func(t *testing.T, h *Handler, path string, parts []string) {
		t.Helper()
		if savedRoutingNode(t, path, append(append([]string{}, parts...), "claude-fallback")...) != nil {
			t.Fatalf("%v: legacy node kept", parts)
		}
		if node := savedRoutingNode(t, path, append(append([]string{}, parts...), "providers", "claude", "fallback", "target")...); node == nil || node.Value != config.ProviderFallbackCodex {
			t.Fatalf("%v: stored fallback target = %v, want codex", parts, node)
		}
		profile := h.cfg.Routing.SharedProfile()
		if len(parts) > 0 {
			profile = *h.cfg.Routing.KeyProfiles[providersWriteKey]
		}
		if got := profile.EffectiveProviders().Claude.Fallback; got.Target != config.ProviderFallbackCodex || !reflect.DeepEqual(got.Mappings, wantMappings) {
			t.Fatalf("%v: fallback = %+v, want target codex with the legacy mappings", parts, got)
		}
	}
	t.Run("read-modify-write of both blocks", func(t *testing.T) {
		h, path := newRoutingProfilesTestHandler(t, profileRaw("{}"))
		rec := serveConfigV8(t, h, http.MethodGet, "/v8/management/config/routing", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("get status %d: %s", rec.Code, rec.Body.String())
		}
		var routing map[string]any
		if errDecode := json.Unmarshal(rec.Body.Bytes(), &routing); errDecode != nil {
			t.Fatal(errDecode)
		}
		providers, ok := routing["providers"].(map[string]any)
		if !ok || routing["claude-fallback"] == nil {
			t.Fatalf("GET did not return both blocks: %s", rec.Body.String())
		}
		providers["codex"] = map[string]any{"speed": "fast"}
		body, errEncode := json.Marshal(routing)
		if errEncode != nil {
			t.Fatal(errEncode)
		}
		if rec = serveConfigV8(t, h, http.MethodPut, "/v8/management/config/routing", string(body)); rec.Code != http.StatusOK {
			t.Fatalf("put status %d: %s", rec.Code, rec.Body.String())
		}
		assertConverted(t, h, path, nil)
		assertConverted(t, h, path, keyParts)
		if got := h.cfg.Routing.SharedProfile().EffectiveProviders().Codex.Speed; got != config.CodexSpeedFast {
			t.Fatalf("codex speed = %q, want fast", got)
		}
	})
	t.Run("PATCH of both blocks", func(t *testing.T) {
		h, path := newRoutingProfilesTestHandler(t, profileRaw("{}"))
		body := `{"routing":{"providers":{"claude":{"fallback":{}}},"claude-fallback":{"enabled":true,"mappings":[{"from":"claude-*","to":"gpt-6-sol"}]}}}`
		if rec := serveConfigV8(t, h, http.MethodPatch, "/v8/management/config", body); rec.Code != http.StatusOK {
			t.Fatalf("patch status %d: %s", rec.Code, rec.Body.String())
		}
		assertConverted(t, h, path, nil)
		assertLegacyKept(t, path, keyParts)
	})

	// A PATCH that names the node is judged by the merged node: clearing the
	// target of a stored non-zero node next to an empty legacy patch keeps the
	// mappings and turns the fallback off instead of restoring target codex
	// from the stored legacy view.
	t.Run("PATCH clearing the target of a stored node", func(t *testing.T) {
		h, path := newRoutingProfilesTestHandler(t, profileRaw("{target: codex, mappings: [{from: \"claude-*\", to: \"gpt-6-sol\"}]}"))
		body := `{"routing":{"providers":{"claude":{"fallback":{"target":null}}},"claude-fallback":{}}}`
		if rec := serveConfigV8(t, h, http.MethodPatch, "/v8/management/config", body); rec.Code != http.StatusOK {
			t.Fatalf("patch status %d: %s", rec.Code, rec.Body.String())
		}
		if savedRoutingNode(t, path, "claude-fallback") != nil {
			t.Fatal("shared legacy node kept")
		}
		if node := savedRoutingNode(t, path, "providers", "claude", "fallback", "target"); node != nil && node.Value == config.ProviderFallbackCodex {
			t.Fatal("stored target codex restored from the legacy view")
		}
		loaded, errLoad := config.LoadConfig(path)
		if errLoad != nil {
			t.Fatal(errLoad)
		}
		got := loaded.Routing.SharedProfile().EffectiveProviders().Claude.Fallback
		if got.Target != config.ProviderFallbackOff || !reflect.DeepEqual(got.Mappings, wantMappings) {
			t.Fatalf("fallback = %+v, want target off with the stored mappings", got)
		}
	})

	// Clearing the target of a stored node that has no other field leaves the
	// merged node empty, so load would read the stored enabled legacy block,
	// which the empty legacy patch keeps: it is converted to target codex with
	// the legacy mappings.
	t.Run("PATCH clearing the target of a stored target-only node", func(t *testing.T) {
		h, path := newRoutingProfilesTestHandler(t, profileRaw("{target: \"off\"}"))
		body := `{"routing":{"providers":{"claude":{"fallback":{"target":null}}},"claude-fallback":{}}}`
		if rec := serveConfigV8(t, h, http.MethodPatch, "/v8/management/config", body); rec.Code != http.StatusOK {
			t.Fatalf("patch status %d: %s", rec.Code, rec.Body.String())
		}
		assertConverted(t, h, path, nil)
		// The key profile is not named by the write and keeps its shape.
		if savedRoutingNode(t, path, append(append([]string{}, keyParts...), "claude-fallback")...) == nil {
			t.Fatal("key profile legacy node dropped")
		}
		if got := h.cfg.Routing.KeyProfiles[providersWriteKey].EffectiveProviders().Claude.Fallback.Target; got != config.ProviderFallbackOff {
			t.Fatalf("key profile target = %q, want off", got)
		}
	})

	// A PATCH that clears the stored node and sends the legacy block converts
	// the merged legacy block, which load would read, rather than a legacy view
	// of the node it just cleared: an empty legacy block turns the fallback
	// off, and a stored api target is not kept.
	for _, stored := range []string{config.ProviderFallbackCodex, config.ProviderFallbackAPI} {
		for _, clear := range []struct{ name, node string }{
			{"null node", `null`},
			{"null fields", `{"target":null,"mode":null,"mappings":null}`},
		} {
			t.Run("PATCH clearing a stored "+stored+" node with "+clear.name+" next to an empty legacy block", func(t *testing.T) {
				h, path := newRoutingProfilesTestHandler(t, "config-version: 8\nrouting:\n  providers: {claude: "+storedClaudeBlock(stored)+"}\n")
				body := `{"routing":{"providers":{"claude":{"fallback":` + clear.node + `}},"claude-fallback":{}}}`
				if rec := serveConfigV8(t, h, http.MethodPatch, "/v8/management/config", body); rec.Code != http.StatusOK {
					t.Fatalf("patch status %d: %s", rec.Code, rec.Body.String())
				}
				if savedRoutingNode(t, path, "claude-fallback") != nil {
					t.Fatal("shared legacy node kept")
				}
				loaded, errLoad := config.LoadConfig(path)
				if errLoad != nil {
					t.Fatal(errLoad)
				}
				if got := loaded.Routing.SharedProfile().EffectiveProviders().Claude.Fallback; got.Target != config.ProviderFallbackOff || len(got.Mappings) != 0 {
					t.Fatalf("fallback = %+v, want target off without mappings", got)
				}
			})
		}
	}

	// A PATCH of an empty legacy block keeps a stored enabled legacy block, so
	// clearing the node converts that block, which load would read, and the
	// fallback goes to Codex with the legacy mappings rather than off.
	for _, stored := range []string{config.ProviderFallbackCodex, config.ProviderFallbackAPI} {
		t.Run("PATCH clearing a stored "+stored+" node next to a stored enabled legacy block", func(t *testing.T) {
			storedLegacy := `claude-fallback: {enabled: true, mode: auto, mappings: [{from: "claude-opus-*", to: "gpt-6-astra"}]}`
			h, path := newRoutingProfilesTestHandler(t, "config-version: 8\nrouting:\n  providers: {claude: "+storedClaudeBlock(stored)+"}\n  "+storedLegacy+"\n")
			body := `{"routing":{"providers":{"claude":{"fallback":null}},"claude-fallback":{}}}`
			if rec := serveConfigV8(t, h, http.MethodPatch, "/v8/management/config", body); rec.Code != http.StatusOK {
				t.Fatalf("patch status %d: %s", rec.Code, rec.Body.String())
			}
			if savedRoutingNode(t, path, "claude-fallback") != nil {
				t.Fatal("shared legacy node kept")
			}
			loaded, errLoad := config.LoadConfig(path)
			if errLoad != nil {
				t.Fatal(errLoad)
			}
			got := loaded.Routing.SharedProfile().EffectiveProviders().Claude.Fallback
			if got.Target != config.ProviderFallbackCodex || got.Mode != "auto" || len(got.Mappings) != 1 || got.Mappings[0].To != "gpt-6-astra" {
				t.Fatalf("fallback = %+v, want target codex with the legacy mode and mapping", got)
			}
		})
	}

	// A misspelled field leaves the node non-zero for strict validation, so the
	// write is rejected rather than the node being replaced by the legacy block.
	// A misspelled legacy field is rejected too rather than being ignored by
	// the conversion, or dropped with the legacy block when a stored non-zero
	// node wins over it.
	keyPrefix := `{"routing":{"key-profiles":{"` + providersWriteKey + `":`
	for _, tc := range []struct{ name, method, target, body, field string }{
		{"PATCH", http.MethodPatch, "/v8/management/config", `{"routing":{"providers":{"claude":{"fallback":{"targte":"off"}}},"claude-fallback":{"enabled":true}}}`, "targte"},
		{"PUT", http.MethodPut, "/v8/management/config/routing", `{"providers":{"claude":{"fallback":{"targte":"off"}}},"claude-fallback":{"enabled":true}}`, "targte"},
		{"PATCH of both blocks", http.MethodPatch, "/v8/management/config", `{"routing":{"providers":{"claude":{"fallback":{}}},"claude-fallback":{"enabeld":false}}}`, "enabeld"},
		{"legacy-only PATCH", http.MethodPatch, "/v8/management/config", `{"routing":{"claude-fallback":{"enabeld":false}}}`, "enabeld"},
		{"legacy-only key PATCH", http.MethodPatch, "/v8/management/config", keyPrefix + `{"claude-fallback":{"enabeld":false}}}}}`, "enabeld"},
		{"legacy mapping PATCH", http.MethodPatch, "/v8/management/config", `{"routing":{"claude-fallback":{"enabled":false,"mappings":[{"from":"claude-*","to":"gpt-6-sol","tto":"gpt-6-astra"}]}}}`, "tto"},
		{"legacy block PUT", http.MethodPut, "/v8/management/config/routing/claude-fallback", `{"enabeld":false}`, "enabeld"},
		{"legacy field PUT", http.MethodPut, "/v8/management/config/routing/claude-fallback/enabeld", `false`, "enabeld"},
		{"PUT of a set node and a legacy block", http.MethodPut, "/v8/management/config/routing", `{"providers":{"claude":{"fallback":{"target":"codex","mappings":[{"from":"claude-*","to":"gpt-6-sol"}]}}},"claude-fallback":{"enabeld":false}}`, "enabeld"},
	} {
		for _, stored := range []struct{ name, node string }{
			{"zero", `{}`},
			{"non-zero", `{target: codex, mappings: [{from: "claude-*", to: "gpt-6-sol"}]}`},
		} {
			t.Run(tc.name+" with a misspelled "+tc.field+" field over a "+stored.name+" node", func(t *testing.T) {
				h, path := newRoutingProfilesTestHandler(t, profileRaw(stored.node))
				before, errBefore := os.ReadFile(path)
				if errBefore != nil {
					t.Fatal(errBefore)
				}
				rec := serveConfigV8(t, h, tc.method, tc.target, tc.body)
				if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.field) {
					t.Fatalf("status %d: %s, want 400 naming the misspelled field", rec.Code, rec.Body.String())
				}
				after, errAfter := os.ReadFile(path)
				if errAfter != nil {
					t.Fatal(errAfter)
				}
				if string(after) != string(before) {
					t.Fatal("rejected write changed the file")
				}
			})
		}
	}
}
