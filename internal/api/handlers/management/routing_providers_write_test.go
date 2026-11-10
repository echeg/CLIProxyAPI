package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

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
