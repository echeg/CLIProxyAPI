package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

const (
	routingCredentialClaudeSecret = "sk-ant-routing-secret"
	routingCredentialCodexSecret  = "sk-codex-routing-secret"
)

type routingProfilesPluginScheduler struct{}

func (routingProfilesPluginScheduler) PickAuth(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
	return pluginapi.SchedulerPickResponse{}, false, nil
}

// registerRoutingCredentials adds API-key and OAuth credentials of several
// providers; only the claude and codex API keys are routing API credentials.
func registerRoutingCredentials(t *testing.T, manager *coreauth.Manager) {
	t.Helper()
	auths := []*coreauth.Auth{
		{ID: "claude-key-b", Index: "claude-idx-b", Provider: "claude", Label: "Claude backup", Attributes: map[string]string{"api_key": routingCredentialClaudeSecret + "-b", "base_url": "https://claude-b.example"}},
		{ID: "claude-key-a", Index: "claude-idx", Provider: "claude", Label: "Claude main", Attributes: map[string]string{"api_key": routingCredentialClaudeSecret, "base_url": "https://claude.example"}},
		{ID: "codex-key", Index: "codex-idx", Provider: "codex", Label: "Codex", Disabled: true, Attributes: map[string]string{"api_key": routingCredentialCodexSecret}},
		{ID: "claude-oauth", Index: "claude-oauth-idx", Provider: "claude", Attributes: map[string]string{"auth_kind": "oauth"}},
		{ID: "gemini-key", Index: "gemini-idx", Provider: "gemini", Attributes: map[string]string{"api_key": "gemini-secret"}},
	}
	for _, auth := range auths {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatal(errRegister)
		}
	}
}

func TestGetRoutingProfilesProvidersView(t *testing.T) {
	gin.SetMode(gin.TestMode)
	offView := config.ProviderRoutingConfig{Mode: config.ProviderModeSubscription, Fallback: config.ProviderFallbackConfig{Target: config.ProviderFallbackOff, Mappings: []config.ClaudeFallbackMapping{}}}
	legacyMappings := []config.ClaudeFallbackMapping{{From: "claude-*", To: "gpt-6-sol"}}
	cases := []struct {
		name       string
		routing    string
		wantShared config.ProvidersRoutingConfig
		wantKey    config.ProvidersRoutingConfig
		wantLegacy bool
	}{
		{
			name:       "legacy only",
			routing:    "  claude-fallback: {enabled: true, mode: always, mappings: [{from: \"claude-*\", to: \"gpt-6-sol\"}]}\n  key-profiles:\n    key-a:\n      session-affinity: true\n",
			wantShared: config.ProvidersRoutingConfig{Claude: config.ProviderRoutingConfig{Mode: config.ProviderModeSubscription, Fallback: config.ProviderFallbackConfig{Target: config.ProviderFallbackCodex, Mode: "always", Mappings: legacyMappings}}, Codex: offView},
			wantKey:    config.ProvidersRoutingConfig{Claude: offView, Codex: offView},
			wantLegacy: true,
		},
		{
			name:       "providers only",
			routing:    "  providers: {claude: {api-credential: claude-idx, fallback: {target: api}}, codex: {mode: api, api-credential: codex-idx}}\n  key-profiles:\n    key-a:\n      providers: {codex: {api-credential: codex-idx, fallback: {target: api}}}\n",
			wantShared: config.ProvidersRoutingConfig{Claude: config.ProviderRoutingConfig{Mode: config.ProviderModeSubscription, APICredential: "claude-idx", Fallback: config.ProviderFallbackConfig{Target: config.ProviderFallbackAPI, Mappings: []config.ClaudeFallbackMapping{}}}, Codex: config.ProviderRoutingConfig{Mode: config.ProviderModeAPI, APICredential: "codex-idx", Fallback: config.ProviderFallbackConfig{Target: config.ProviderFallbackOff, Mappings: []config.ClaudeFallbackMapping{}}}},
			wantKey:    config.ProvidersRoutingConfig{Claude: offView, Codex: config.ProviderRoutingConfig{Mode: config.ProviderModeSubscription, APICredential: "codex-idx", Fallback: config.ProviderFallbackConfig{Target: config.ProviderFallbackAPI, Mappings: []config.ClaudeFallbackMapping{}}}},
		},
		{
			name:       "mixed",
			routing:    "  providers: {codex: {api-credential: codex-idx, fallback: {target: api}}}\n  key-profiles:\n    key-a:\n      claude-fallback: {enabled: true, mappings: [{from: \"claude-*\", to: \"gpt-6-sol\"}]}\n      providers: {claude: {api-credential: claude-idx}}\n",
			wantShared: config.ProvidersRoutingConfig{Claude: offView, Codex: config.ProviderRoutingConfig{Mode: config.ProviderModeSubscription, APICredential: "codex-idx", Fallback: config.ProviderFallbackConfig{Target: config.ProviderFallbackAPI, Mappings: []config.ClaudeFallbackMapping{}}}},
			wantKey:    config.ProvidersRoutingConfig{Claude: config.ProviderRoutingConfig{Mode: config.ProviderModeSubscription, APICredential: "claude-idx", Fallback: config.ProviderFallbackConfig{Target: config.ProviderFallbackCodex, Mappings: legacyMappings}}, Codex: offView},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newRoutingProfilesTestHandler(t, "config-version: 8\naccess:\n  api-keys: [\"key-a\"]\nrouting:\n"+tc.routing)
			out, rec := getRoutingProfiles(t, h)
			if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d headers=%v", rec.Code, rec.Header())
			}
			if !reflect.DeepEqual(out.Shared.Providers, tc.wantShared) {
				t.Fatalf("shared providers = %+v, want %+v", out.Shared.Providers, tc.wantShared)
			}
			if out.Shared.ClaudeFallback.Enabled != tc.wantLegacy {
				t.Fatalf("shared legacy view = %+v", out.Shared.ClaudeFallback)
			}
			if len(out.Keys) != 1 || !reflect.DeepEqual(out.Keys[0].Profile.Providers, tc.wantKey) {
				t.Fatalf("key providers = %+v, want %+v", out.Keys, tc.wantKey)
			}
			// The legacy view is derived from the effective providers.claude.fallback.
			if got := out.Keys[0].Profile.ClaudeFallback.Enabled; got != (tc.wantKey.Claude.Fallback.Target == config.ProviderFallbackCodex) {
				t.Fatalf("key legacy view enabled = %v", got)
			}

			// Both sub-blocks are always present and mappings are [] rather than null.
			var raw struct {
				Shared struct {
					Providers map[string]struct {
						Fallback struct {
							Mappings json.RawMessage `json:"mappings"`
						} `json:"fallback"`
					} `json:"providers"`
				} `json:"shared"`
			}
			if errDecode := json.Unmarshal(rec.Body.Bytes(), &raw); errDecode != nil {
				t.Fatal(errDecode)
			}
			for _, provider := range []string{"claude", "codex"} {
				block, ok := raw.Shared.Providers[provider]
				if !ok || string(block.Fallback.Mappings) == "null" {
					t.Fatalf("shared providers.%s = %+v (present %v)", provider, block, ok)
				}
			}
		})
	}
}

func TestGetRoutingProfilesAPICredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, _ := newRoutingProfilesTestHandler(t, "config-version: 8\n")
	registerRoutingCredentials(t, h.authManager)
	_, rec := getRoutingProfiles(t, h)
	var out struct {
		APICredentials map[string][]routingAPICredentialView `json:"api_credentials"`
	}
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &out); errDecode != nil {
		t.Fatal(errDecode)
	}
	want := map[string][]routingAPICredentialView{
		"claude": {
			{AuthIndex: "claude-idx", Label: "Claude main", BaseURL: "https://claude.example"},
			{AuthIndex: "claude-idx-b", Label: "Claude backup", BaseURL: "https://claude-b.example"},
		},
		"codex": {{AuthIndex: "codex-idx", Label: "Codex", Disabled: true}},
	}
	if !reflect.DeepEqual(out.APICredentials, want) {
		t.Fatalf("api_credentials = %+v, want %+v", out.APICredentials, want)
	}
	body := rec.Body.String()
	for _, secret := range []string{routingCredentialClaudeSecret, routingCredentialCodexSecret, "gemini-secret", "api_key"} {
		if strings.Contains(body, secret) {
			t.Fatalf("response leaks %q: %s", secret, body)
		}
	}

	// Without credentials (or without a manager) both providers are listed as [].
	empty := &Handler{}
	rec = httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	empty.GetRoutingProfiles(c)
	if !strings.Contains(rec.Body.String(), `"api_credentials":{"claude":[],"codex":[]}`) {
		t.Fatalf("empty api_credentials: %s", rec.Body.String())
	}
}

func TestGetRoutingProfilesUnsupportedUnderPluginScheduler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, _ := newRoutingProfilesTestHandler(t, routingProfilesTestConfig)
	h.authManager.SetPluginScheduler(routingProfilesPluginScheduler{})
	out, rec := getRoutingProfiles(t, h)
	if out.Supported || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("supported=%v headers=%v", out.Supported, rec.Header())
	}
}

func TestGetClaudeFallbackDerivedFromProviders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name, routing string
		wantEnabled   bool
	}{
		{"api target", "  providers: {claude: {api-credential: claude-idx, fallback: {target: api}}}\n", false},
		{"codex target", "  providers: {claude: {fallback: {target: codex, mappings: [{from: \"claude-*\", to: \"gpt-6-sol\"}]}}}\n", true},
		{"providers wins over legacy", "  claude-fallback: {enabled: true, mappings: [{from: \"claude-*\", to: \"gpt-6-sol\"}]}\n  providers: {claude: {fallback: {target: off}}}\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newRoutingProfilesTestHandler(t, "config-version: 8\nrouting:\n"+tc.routing)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			h.GetClaudeFallback(c)
			var out struct {
				Settings config.ClaudeFallbackConfig `json:"settings"`
			}
			if errDecode := json.Unmarshal(rec.Body.Bytes(), &out); errDecode != nil {
				t.Fatal(errDecode)
			}
			if out.Settings.Enabled != tc.wantEnabled || out.Settings.Mode != "auto" || out.Settings.Mappings == nil {
				t.Fatalf("settings = %+v, want enabled %v", out.Settings, tc.wantEnabled)
			}
		})
	}
}

func TestConfigV8ProvidersWritePersistsAndWarnsOnUnknownCredential(t *testing.T) {
	gin.SetMode(gin.TestMode)
	hook := logtest.NewGlobal()
	h, path := newRoutingProfilesTestHandler(t, "config-version: 8\naccess:\n  api-keys: [\""+providersWriteKey+"\"]\n")
	registerRoutingCredentials(t, h.authManager)
	hash := config.KeyHashPrefix(providersWriteKey)

	warnings := func() []*log.Entry {
		var out []*log.Entry
		for _, entry := range hook.AllEntries() {
			if entry.Level == log.WarnLevel && strings.Contains(entry.Message, "api-credential") {
				out = append(out, entry)
			}
		}
		return out
	}

	// Known enabled credentials: persisted, returned, and no warning.
	patch := `{"routing":{"providers":{"claude":{"api-credential":"claude-idx","fallback":{"target":"api"}}},"key-profiles":{"` + providersWriteKey + `":{"providers":{"claude":{"mode":"api","api-credential":"claude-idx-b"}}}}}}`
	if rec := serveConfigV8(t, h, http.MethodPatch, "/v8/management/config", patch); rec.Code != http.StatusOK {
		t.Fatalf("patch status %d: %s", rec.Code, rec.Body.String())
	}
	if got := warnings(); len(got) != 0 {
		t.Fatalf("unexpected warnings: %v", got)
	}
	loaded, errLoad := config.LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	if got := loaded.Routing.KeyProfiles[providersWriteKey].Providers.Claude; got.Mode != config.ProviderModeAPI || got.APICredential != "claude-idx-b" {
		t.Fatalf("saved key providers = %+v", got)
	}
	out, _ := getRoutingProfiles(t, h)
	if out.Shared.Providers.Claude.Fallback.Target != config.ProviderFallbackAPI || out.Shared.Providers.Claude.APICredential != "claude-idx" {
		t.Fatalf("shared providers = %+v", out.Shared.Providers)
	}
	if len(out.Keys) != 1 || out.Keys[0].Profile.Providers.Claude.Mode != config.ProviderModeAPI {
		t.Fatalf("keys = %+v", out.Keys)
	}

	// An unknown index and a disabled credential are accepted with one warning per profile.
	hook.Reset()
	patch = `{"routing":{"providers":{"codex":{"api-credential":"codex-idx","fallback":{"target":"api"}}},"key-profiles":{"` + providersWriteKey + `":{"providers":{"claude":{"api-credential":"missing-idx"}}}}}}`
	rec := serveConfigV8(t, h, http.MethodPatch, "/v8/management/config", patch)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status %d: %s", rec.Code, rec.Body.String())
	}
	got := warnings()
	if len(got) != 2 {
		t.Fatalf("warnings = %d, want 2: %v", len(got), got)
	}
	byProfile := map[string]log.Fields{}
	for _, entry := range got {
		byProfile[entry.Data["profile"].(string)] = entry.Data
		if line, _ := entry.String(); strings.Contains(line, providersWriteKey) || strings.Contains(line, routingCredentialClaudeSecret) || strings.Contains(line, routingCredentialCodexSecret) {
			t.Fatalf("warning leaks a secret: %s", line)
		}
	}
	if byProfile["shared"]["codex_api_credential"] != "codex-idx" || byProfile["shared"]["claude_api_credential"] != nil {
		t.Fatalf("shared warning = %v", byProfile["shared"])
	}
	if byProfile[hash]["claude_api_credential"] != "missing-idx" {
		t.Fatalf("key warning = %v", byProfile)
	}
	if h.cfg.Routing.KeyProfiles[providersWriteKey].Providers.Claude.APICredential != "missing-idx" {
		t.Fatalf("unknown credential not persisted: %+v", h.cfg.Routing.KeyProfiles[providersWriteKey].Providers)
	}

	// Under a plugin scheduler the policy is not applied, so nothing is logged.
	hook.Reset()
	h.authManager.SetPluginScheduler(routingProfilesPluginScheduler{})
	if rec = serveConfigV8(t, h, http.MethodPatch, "/v8/management/config", `{"server":{"port":8318}}`); rec.Code != http.StatusOK {
		t.Fatalf("patch status %d: %s", rec.Code, rec.Body.String())
	}
	if got = warnings(); len(got) != 0 {
		t.Fatalf("warnings under a plugin scheduler: %v", got)
	}
}
