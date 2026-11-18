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

// codexSpeedTestConfig holds a shared profile and a key profile with every field
// set, so a narrow write can be checked against the rest of the profile.
const codexSpeedTestConfig = "config-version: 8\naccess:\n  api-keys: [\"" + providersWriteKey + "\"]\nrouting:\n" +
	"  session-affinity: true\n" +
	"  preferred-accounts: {claude: claude-1}\n" +
	"  selection: {primary: priority, tiebreak: round-robin}\n" +
	"  providers: {claude: {api-credential: claude-idx, fallback: {target: api}}, codex: {mode: api, api-credential: codex-idx}}\n" +
	"  key-profiles:\n" +
	"    \"" + providersWriteKey + "\":\n" +
	"      session-affinity: true\n" +
	"      preferred-accounts: {codex: codex-9}\n" +
	"      selection: {primary: earliest-reset, tiebreak: fill-first}\n" +
	"      providers: {claude: {fallback: {target: codex, mode: always, mappings: [{from: \"claude-*\", to: \"gpt-6-sol\"}]}}, codex: {api-credential: codex-idx}}\n"

// codexSpeedProfile returns the shared profile or the profile of providersWriteKey
// from a GET response.
func codexSpeedProfile(t *testing.T, out routingProfilesResponse, scope string) config.RoutingProfile {
	t.Helper()
	if scope == "shared" {
		return out.Shared
	}
	for _, key := range out.Keys {
		if key.Key == providersWriteKey {
			return key.Profile
		}
	}
	t.Fatalf("key profile missing: %+v", out.Keys)
	return config.RoutingProfile{}
}

func TestGetRoutingProfilesCodexSpeed(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("defaults", func(t *testing.T) {
		h, _ := newRoutingProfilesTestHandler(t, "config-version: 8\naccess:\n  api-keys: [\""+providersWriteKey+"\"]\nrouting:\n  key-profiles:\n    \""+providersWriteKey+"\":\n      session-affinity: true\n")
		out, rec := getRoutingProfiles(t, h)
		for _, scope := range []string{"shared", "key"} {
			providers := codexSpeedProfile(t, out, scope).Providers
			if providers.Codex.Speed != config.CodexSpeedStandard || providers.Codex.IgnoreClientTier {
				t.Fatalf("%s codex = %+v, want standard without ignore-client-tier", scope, providers.Codex)
			}
			if providers.Claude.Speed != "" || providers.Claude.IgnoreClientTier {
				t.Fatalf("%s claude = %+v, want an empty speed", scope, providers.Claude)
			}
		}

		// Both fields are always present, so a panel can rely on them.
		var raw struct {
			Shared struct {
				Providers map[string]map[string]any `json:"providers"`
			} `json:"shared"`
		}
		if errDecode := json.Unmarshal(rec.Body.Bytes(), &raw); errDecode != nil {
			t.Fatal(errDecode)
		}
		for provider, want := range map[string]string{"codex": config.CodexSpeedStandard, "claude": ""} {
			block := raw.Shared.Providers[provider]
			if speed, ok := block["speed"]; !ok || speed != want {
				t.Fatalf("%s speed = %v (present %v), want %q", provider, speed, ok, want)
			}
			if flag, ok := block["ignore-client-tier"]; !ok || flag != false {
				t.Fatalf("%s ignore-client-tier = %v (present %v), want false", provider, flag, ok)
			}
		}
	})

	t.Run("explicit values", func(t *testing.T) {
		raw := "config-version: 8\naccess:\n  api-keys: [\"" + providersWriteKey + "\"]\nrouting:\n" +
			"  providers: {codex: {speed: ultrafast, ignore-client-tier: true}}\n" +
			"  key-profiles:\n    \"" + providersWriteKey + "\":\n      providers: {codex: {speed: fast}}\n"
		h, _ := newRoutingProfilesTestHandler(t, raw)
		out, _ := getRoutingProfiles(t, h)
		if got := codexSpeedProfile(t, out, "shared").Providers.Codex; got.Speed != config.CodexSpeedUltrafast || !got.IgnoreClientTier {
			t.Fatalf("shared codex = %+v", got)
		}
		// A key profile does not inherit the shared ignore-client-tier.
		if got := codexSpeedProfile(t, out, "key").Providers.Codex; got.Speed != config.CodexSpeedFast || got.IgnoreClientTier {
			t.Fatalf("key codex = %+v", got)
		}
	})
}

func TestConfigV8CodexSpeedPatchKeepsProfile(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fields := []struct {
		name, value string
		apply       func(*config.ProviderRoutingConfig)
		wantSaved   string
	}{
		{"speed", `"ultrafast"`, func(p *config.ProviderRoutingConfig) { p.Speed = config.CodexSpeedUltrafast }, "ultrafast"},
		{"ignore-client-tier", `true`, func(p *config.ProviderRoutingConfig) { p.IgnoreClientTier = true }, "true"},
	}
	for _, scope := range []string{"shared", "key"} {
		for _, field := range fields {
			t.Run(scope+"/"+field.name, func(t *testing.T) {
				h, path := newRoutingProfilesTestHandler(t, codexSpeedTestConfig)
				patchFor := func(value string) string {
					codex := `{"providers":{"codex":{"` + field.name + `":` + value + `}}}`
					if scope == "shared" {
						return `{"routing":` + codex + `}`
					}
					return `{"routing":{"key-profiles":{"` + providersWriteKey + `":` + codex + `}}}`
				}
				parts := []string{"providers", "codex", field.name}
				if scope == "key" {
					parts = append([]string{"key-profiles", providersWriteKey}, parts...)
				}

				before, _ := getRoutingProfiles(t, h)
				original := codexSpeedProfile(t, before, scope)

				if rec := serveConfigV8(t, h, http.MethodPatch, "/v8/management/config", patchFor(field.value)); rec.Code != http.StatusOK {
					t.Fatalf("patch status %d: %s", rec.Code, rec.Body.String())
				}
				after, _ := getRoutingProfiles(t, h)
				want := codexSpeedProfile(t, before, scope)
				field.apply(&want.Providers.Codex)
				if got := codexSpeedProfile(t, after, scope); !reflect.DeepEqual(got, want) {
					t.Fatalf("profile after patch = %+v, want %+v", got, want)
				}
				if node := savedRoutingNode(t, path, parts...); node == nil || node.Value != field.wantSaved {
					t.Fatalf("saved %v = %+v, want %s", parts, node, field.wantSaved)
				}

				if rec := serveConfigV8(t, h, http.MethodPatch, "/v8/management/config", patchFor("null")); rec.Code != http.StatusOK {
					t.Fatalf("null patch status %d: %s", rec.Code, rec.Body.String())
				}
				cleared, _ := getRoutingProfiles(t, h)
				if got := codexSpeedProfile(t, cleared, scope); !reflect.DeepEqual(got, original) {
					t.Fatalf("profile after null = %+v, want %+v", got, original)
				}
				// PATCH keeps an explicit null (config_v8.go mergeConfigV8Patch); it
				// loads as an empty field, so the view above is already the default.
				if node := savedRoutingNode(t, path, parts...); node != nil && node.Tag != "!!null" {
					t.Fatalf("cleared %v saved as %+v, want null or absent", parts, node)
				}

				// DELETE is the explicit removal and drops the node from the file.
				if rec := serveConfigV8(t, h, http.MethodPatch, "/v8/management/config", patchFor(field.value)); rec.Code != http.StatusOK {
					t.Fatalf("second patch status %d: %s", rec.Code, rec.Body.String())
				}
				target := "/v8/management/config/routing/" + strings.Join(parts, "/")
				if rec := serveConfigV8(t, h, http.MethodDelete, target, ""); rec.Code != http.StatusOK {
					t.Fatalf("delete %s status %d: %s", target, rec.Code, rec.Body.String())
				}
				deleted, _ := getRoutingProfiles(t, h)
				if got := codexSpeedProfile(t, deleted, scope); !reflect.DeepEqual(got, original) {
					t.Fatalf("profile after delete = %+v, want %+v", got, original)
				}
				if node := savedRoutingNode(t, path, parts...); node != nil {
					t.Fatalf("deleted %v kept in the file: %+v", parts, node)
				}
			})
		}
	}
}

// routingRoundTripOtherKey has a profile that no providers-block write targets.
const routingRoundTripOtherKey = "client-key-b-secret"

func TestConfigV8RoutingProfileGetBodyPatchesBack(t *testing.T) {
	gin.SetMode(gin.TestMode)
	raw := "config-version: 8\naccess:\n  api-keys: [\"" + providersWriteKey + "\", \"" + routingRoundTripOtherKey + "\"]\nrouting:\n" +
		"  session-affinity: true\n" +
		"  selection: {primary: priority, tiebreak: round-robin}\n" +
		"  providers: {claude: {api-credential: claude-idx, fallback: {target: api}, disabled: true}, codex: {speed: ultrafast, ignore-client-tier: true}}\n" +
		"  key-profiles:\n" +
		"    \"" + providersWriteKey + "\":\n" +
		"      preferred-accounts: {codex: codex-9}\n" +
		"      providers: {claude: {fallback: {target: codex, mode: always, mappings: [{from: \"claude-*\", to: \"gpt-6-sol\"}]}}, codex: {speed: fast, disabled: true}}\n" +
		"    \"" + routingRoundTripOtherKey + "\":\n" +
		"      providers: {claude: {disabled: true}, codex: {mode: api, api-credential: codex-idx}}\n"

	type getBody struct {
		Shared map[string]any `json:"shared"`
		Keys   []struct {
			Key     string         `json:"key"`
			Profile map[string]any `json:"profile"`
		} `json:"keys"`
	}
	// roundTrip GETs the profiles, PATCHes back the routing section built from
	// the raw GET body, and requires the effective profiles to stay unchanged.
	roundTrip := func(t *testing.T, build func(body getBody) map[string]any) {
		t.Helper()
		h, _ := newRoutingProfilesTestHandler(t, raw)
		before, rec := getRoutingProfiles(t, h)
		if !before.Shared.Providers.Claude.Disabled || len(before.Keys) != 2 || !codexSpeedProfile(t, before, "key").Providers.Codex.Disabled {
			t.Fatalf("fixture not loaded as expected: %+v", before)
		}
		var body getBody
		if errDecode := json.Unmarshal(rec.Body.Bytes(), &body); errDecode != nil {
			t.Fatal(errDecode)
		}
		patch, errMarshal := json.Marshal(map[string]any{"routing": build(body)})
		if errMarshal != nil {
			t.Fatal(errMarshal)
		}
		if rec := serveConfigV8(t, h, http.MethodPatch, "/v8/management/config", string(patch)); rec.Code != http.StatusOK {
			t.Fatalf("patch status %d: %s", rec.Code, rec.Body.String())
		}
		after, _ := getRoutingProfiles(t, h)
		if !reflect.DeepEqual(after.Shared, before.Shared) || !reflect.DeepEqual(after.Keys, before.Keys) {
			t.Fatalf("round trip changed the profiles:\nbefore %+v\nafter  %+v", before, after)
		}
	}

	t.Run("whole body", func(t *testing.T) {
		roundTrip(t, func(body getBody) map[string]any {
			routing := body.Shared
			keyProfiles := map[string]any{}
			for _, key := range body.Keys {
				keyProfiles[key.Key] = key.Profile
			}
			routing["key-profiles"] = keyProfiles
			return routing
		})
	})

	t.Run("shared providers block", func(t *testing.T) {
		roundTrip(t, func(body getBody) map[string]any {
			return map[string]any{"providers": body.Shared["providers"]}
		})
	})

	t.Run("key providers block", func(t *testing.T) {
		roundTrip(t, func(body getBody) map[string]any {
			for _, key := range body.Keys {
				if key.Key == providersWriteKey {
					return map[string]any{"key-profiles": map[string]any{
						providersWriteKey: map[string]any{"providers": key.Profile["providers"]},
					}}
				}
			}
			t.Fatalf("key profile missing: %+v", body.Keys)
			return nil
		})
	})
}
