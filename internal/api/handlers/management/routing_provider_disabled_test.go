package management

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// providerDisabledBlock returns the claude or codex block of a profile.
func providerDisabledBlock(providers *config.ProvidersRoutingConfig, provider string) *config.ProviderRoutingConfig {
	if provider == "claude" {
		return &providers.Claude
	}
	return &providers.Codex
}

// otherScope returns the profile scope a write to scope must not touch.
func otherScope(scope string) string {
	if scope == "shared" {
		return "key"
	}
	return "shared"
}

func TestGetRoutingProfilesProviderDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("defaults", func(t *testing.T) {
		h, _ := newRoutingProfilesTestHandler(t, "config-version: 8\naccess:\n  api-keys: [\""+providersWriteKey+"\"]\nrouting:\n  key-profiles:\n    \""+providersWriteKey+"\":\n      session-affinity: true\n")
		out, rec := getRoutingProfiles(t, h)
		for _, scope := range []string{"shared", "key"} {
			providers := codexSpeedProfile(t, out, scope).Providers
			if providers.Claude.Disabled || providers.Codex.Disabled {
				t.Fatalf("%s providers = %+v, want both enabled", scope, providers)
			}
		}

		// The field is always present, so a panel can detect support by it.
		var raw struct {
			Shared struct {
				Providers map[string]map[string]any `json:"providers"`
			} `json:"shared"`
			Keys []struct {
				Profile struct {
					Providers map[string]map[string]any `json:"providers"`
				} `json:"profile"`
			} `json:"keys"`
		}
		if errDecode := json.Unmarshal(rec.Body.Bytes(), &raw); errDecode != nil {
			t.Fatal(errDecode)
		}
		if len(raw.Keys) != 1 {
			t.Fatalf("keys = %+v", raw.Keys)
		}
		blocks := map[string]map[string]map[string]any{"shared": raw.Shared.Providers, "key": raw.Keys[0].Profile.Providers}
		for scope, providers := range blocks {
			for _, provider := range []string{"claude", "codex"} {
				if flag, ok := providers[provider]["disabled"]; !ok || flag != false {
					t.Fatalf("%s %s disabled = %v (present %v), want false", scope, provider, flag, ok)
				}
			}
		}
	})

	t.Run("explicit values", func(t *testing.T) {
		raw := "config-version: 8\naccess:\n  api-keys: [\"" + providersWriteKey + "\"]\nrouting:\n" +
			"  providers: {claude: {disabled: true}}\n" +
			"  key-profiles:\n    \"" + providersWriteKey + "\":\n      providers: {codex: {disabled: true, speed: fast}}\n"
		h, _ := newRoutingProfilesTestHandler(t, raw)
		out, _ := getRoutingProfiles(t, h)
		if got := codexSpeedProfile(t, out, "shared").Providers; !got.Claude.Disabled || got.Codex.Disabled {
			t.Fatalf("shared providers = %+v, want only claude disabled", got)
		}
		// A key profile does not inherit the shared disable.
		if got := codexSpeedProfile(t, out, "key").Providers; got.Claude.Disabled || !got.Codex.Disabled || got.Codex.Speed != config.CodexSpeedFast {
			t.Fatalf("key providers = %+v, want only codex disabled with speed fast", got)
		}
	})
}

func TestConfigV8ProviderDisabledPatchKeepsProfile(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, scope := range []string{"shared", "key"} {
		for _, provider := range []string{"claude", "codex"} {
			t.Run(scope+"/"+provider, func(t *testing.T) {
				h, path := newRoutingProfilesTestHandler(t, codexSpeedTestConfig)
				patchFor := func(value string) string {
					block := `{"providers":{"` + provider + `":{"disabled":` + value + `}}}`
					if scope == "shared" {
						return `{"routing":` + block + `}`
					}
					return `{"routing":{"key-profiles":{"` + providersWriteKey + `":` + block + `}}}`
				}
				parts := []string{"providers", provider, "disabled"}
				if scope == "key" {
					parts = append([]string{"key-profiles", providersWriteKey}, parts...)
				}

				before, _ := getRoutingProfiles(t, h)
				original := codexSpeedProfile(t, before, scope)
				untouched := codexSpeedProfile(t, before, otherScope(scope))
				check := func(stage string, want config.RoutingProfile) {
					t.Helper()
					out, _ := getRoutingProfiles(t, h)
					if got := codexSpeedProfile(t, out, scope); !reflect.DeepEqual(got, want) {
						t.Fatalf("profile after %s = %+v, want %+v", stage, got, want)
					}
					if got := codexSpeedProfile(t, out, otherScope(scope)); !reflect.DeepEqual(got, untouched) {
						t.Fatalf("%s profile after %s = %+v, want %+v", otherScope(scope), stage, got, untouched)
					}
				}

				if rec := serveConfigV8(t, h, http.MethodPatch, "/v8/management/config", patchFor("true")); rec.Code != http.StatusOK {
					t.Fatalf("patch status %d: %s", rec.Code, rec.Body.String())
				}
				disabled := codexSpeedProfile(t, before, scope)
				providerDisabledBlock(&disabled.Providers, provider).Disabled = true
				check("disable", disabled)
				if node := savedRoutingNode(t, path, parts...); node == nil || node.Value != "true" {
					t.Fatalf("saved %v = %+v, want true", parts, node)
				}

				// PATCH false re-enables and keeps an explicit false in the file.
				if rec := serveConfigV8(t, h, http.MethodPatch, "/v8/management/config", patchFor("false")); rec.Code != http.StatusOK {
					t.Fatalf("false patch status %d: %s", rec.Code, rec.Body.String())
				}
				check("re-enable", original)
				if node := savedRoutingNode(t, path, parts...); node == nil || node.Value != "false" {
					t.Fatalf("saved %v = %+v, want false", parts, node)
				}

				// DELETE is the explicit removal and drops the node from the file.
				if rec := serveConfigV8(t, h, http.MethodPatch, "/v8/management/config", patchFor("true")); rec.Code != http.StatusOK {
					t.Fatalf("second patch status %d: %s", rec.Code, rec.Body.String())
				}
				target := "/v8/management/config/routing/" + strings.Join(parts, "/")
				if rec := serveConfigV8(t, h, http.MethodDelete, target, ""); rec.Code != http.StatusOK {
					t.Fatalf("delete %s status %d: %s", target, rec.Code, rec.Body.String())
				}
				check("delete", original)
				if node := savedRoutingNode(t, path, parts...); node != nil {
					t.Fatalf("deleted %v kept in the file: %+v", parts, node)
				}
			})
		}
	}
}

func TestConfigV8DisableCodexKeepsClaudeFallbackToCodex(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, scope := range []string{"shared", "key"} {
		t.Run(scope, func(t *testing.T) {
			fallback := `{claude: {fallback: {target: codex, mode: always, mappings: [{from: "claude-*", to: "gpt-6-sol"}]}}}`
			raw := "config-version: 8\naccess:\n  api-keys: [\"" + providersWriteKey + "\"]\nrouting:\n"
			patch := `{"routing":{"providers":{"codex":{"disabled":true}}}}`
			if scope == "shared" {
				raw += "  providers: " + fallback + "\n" +
					"  key-profiles:\n    \"" + providersWriteKey + "\":\n      session-affinity: true\n"
			} else {
				raw += "  key-profiles:\n    \"" + providersWriteKey + "\":\n      providers: " + fallback + "\n"
				patch = `{"routing":{"key-profiles":{"` + providersWriteKey + `":{"providers":{"codex":{"disabled":true}}}}}}`
			}
			h, _ := newRoutingProfilesTestHandler(t, raw)
			before, _ := getRoutingProfiles(t, h)

			// The combination is a warning, not a validation error.
			if rec := serveConfigV8(t, h, http.MethodPatch, "/v8/management/config", patch); rec.Code != http.StatusOK {
				t.Fatalf("patch status %d: %s", rec.Code, rec.Body.String())
			}
			after, _ := getRoutingProfiles(t, h)
			want := codexSpeedProfile(t, before, scope)
			want.Providers.Codex.Disabled = true
			if got := codexSpeedProfile(t, after, scope); !reflect.DeepEqual(got, want) {
				t.Fatalf("profile after patch = %+v, want %+v", got, want)
			}
			if got := codexSpeedProfile(t, after, scope).Providers.Claude.Fallback.Target; got != config.ProviderFallbackCodex {
				t.Fatalf("claude fallback target = %q, want codex kept", got)
			}
		})
	}
}
