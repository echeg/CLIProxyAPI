package handlers

import (
	"context"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// "auto" skips a model whose every provider the key's routing profile disables, so
// a key without Claude resolves to a model it can run instead of a 403.
func TestAutoModelSkipsModelOfDisabledProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const (
		disabledKey = "auto-model-claude-disabled-key"
		plainKey    = "auto-model-plain-key"
		claudeModel = "auto-model-disabled-claude-model"
		codexModel  = "auto-model-enabled-codex-model"
	)
	cfg := &config.Config{SDKConfig: config.SDKConfig{APIKeys: []string{disabledKey, plainKey}}}
	cfg.Routing.KeyProfiles = map[string]*config.RoutingProfile{
		disabledKey: {Providers: config.ProvidersRoutingConfig{Claude: config.ProviderRoutingConfig{Disabled: true}}},
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	h := NewBaseAPIHandlers(&cfg.SDKConfig, manager)

	// The Claude model is the newest, so the unfiltered "auto" picks it first.
	const newest = int64(1) << 62
	for _, client := range []struct {
		id, provider, model string
		created             int64
	}{
		{"auto-model-claude-client", "claude", claudeModel, newest},
		{"auto-model-codex-client", "codex", codexModel, newest - 1},
	} {
		registry.GetGlobalRegistry().RegisterClient(client.id, client.provider, []*registry.ModelInfo{{ID: client.model, Created: client.created}})
		id := client.id
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	ctxWithKey := func(key string) context.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Set("userApiKey", key)
		return context.WithValue(context.Background(), "gin", c)
	}

	disabledFilter := h.AutoModelProviderFilter(ctxWithKey(disabledKey))
	if disabledFilter("claude") || !disabledFilter("codex") {
		t.Fatalf("Claude-disabled filter: claude = %t, codex = %t; want false, true", disabledFilter("claude"), disabledFilter("codex"))
	}
	plainFilter := h.AutoModelProviderFilter(ctxWithKey(plainKey))
	if !plainFilter("claude") || !plainFilter("codex") {
		t.Fatalf("plain filter: claude = %t, codex = %t; want true, true", plainFilter("claude"), plainFilter("codex"))
	}

	providers, model, errMsg := h.getRequestDetailsWithOptions(ctxWithKey(disabledKey), "auto", false, false)
	if errMsg != nil {
		t.Fatalf("auto under a Claude-disabled key: %v", errMsg.Error)
	}
	if model != codexModel || !slices.Equal(providers, []string{"codex"}) {
		t.Fatalf("auto resolved to %q with providers %v; want %q on codex", model, providers, codexModel)
	}
}

// newAutoModelDisabledClaudeHandler returns a handler whose key profile disables
// Claude, and the context of a request with that key.
func newAutoModelDisabledClaudeHandler(t *testing.T, key string) (*BaseAPIHandler, context.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{SDKConfig: config.SDKConfig{APIKeys: []string{key}}}
	cfg.Routing.KeyProfiles = map[string]*config.RoutingProfile{
		key: {Providers: config.ProvidersRoutingConfig{Claude: config.ProviderRoutingConfig{Disabled: true}}},
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("userApiKey", key)
	return NewBaseAPIHandlers(&cfg.SDKConfig, manager), context.WithValue(context.Background(), "gin", c)
}

// When the key's only registered provider is disabled and none of its clients is
// available (cooling down, or suspended for a reason that hides the model from
// the listing), "auto" still resolves to that provider's model, so the request
// reaches the execute gate and gets the 403 instead of a 400 for model "auto".
func TestAutoModelResolvesDisabledProviderModelWhileItIsUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name        string
		unavailable func(reg *registry.ModelRegistry, clientID, model string)
	}{
		{"cooling down", func(reg *registry.ModelRegistry, clientID, model string) { reg.SetModelQuotaExceeded(clientID, model) }},
		{"suspended", func(reg *registry.ModelRegistry, clientID, model string) {
			reg.SuspendClientModel(clientID, model, "unauthorized")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const (
				claudeClientID = "auto-model-unavailable-claude-client"
				claudeModel    = "auto-model-unavailable-claude-model"
			)
			h, ctx := newAutoModelDisabledClaudeHandler(t, "auto-model-unavailable-claude-disabled-key")
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(claudeClientID, "claude", []*registry.ModelInfo{{ID: claudeModel, Created: int64(1) << 62}})
			t.Cleanup(func() { reg.UnregisterClient(claudeClientID) })
			tc.unavailable(reg, claudeClientID, claudeModel)
			if got, err := reg.GetFirstRegisteredModelMatching(func(provider string) bool { return provider != "claude" }); err == nil {
				t.Fatalf("GetFirstRegisteredModelMatching(not claude) = %q; the test needs a registry where only Claude registers a model", got)
			}

			providers, model, errMsg := h.getRequestDetailsWithOptions(ctx, "auto", false, false)
			if errMsg != nil {
				t.Fatalf("auto with an unavailable disabled Claude: %d %v, want the Claude model for the 403 gate", errMsg.StatusCode, errMsg.Error)
			}
			if model != claudeModel || !slices.Equal(providers, []string{"claude"}) {
				t.Fatalf("auto resolved to %q with providers %v; want %q on claude", model, providers, claudeModel)
			}
		})
	}
}

// A disabled provider is never the target while an enabled one serves a model:
// with a healthy disabled Claude and a cooling-down Codex, "auto" resolves to the
// Codex model, so the request gets Codex's cooldown error instead of a refusal
// for Claude, which the client never asked for.
func TestAutoModelPrefersUnavailableEnabledProviderOverDisabledOne(t *testing.T) {
	const (
		claudeClientID = "auto-model-healthy-claude-client"
		claudeModel    = "auto-model-healthy-claude-model"
		codexClientID  = "auto-model-cooling-codex-client"
		codexModel     = "auto-model-cooling-codex-model"
	)
	h, ctx := newAutoModelDisabledClaudeHandler(t, "auto-model-cooling-codex-claude-disabled-key")
	reg := registry.GetGlobalRegistry()
	const newest = int64(1) << 62
	reg.RegisterClient(claudeClientID, "claude", []*registry.ModelInfo{{ID: claudeModel, Created: newest}})
	t.Cleanup(func() { reg.UnregisterClient(claudeClientID) })
	reg.RegisterClient(codexClientID, "codex", []*registry.ModelInfo{{ID: codexModel, Created: newest - 1}})
	t.Cleanup(func() { reg.UnregisterClient(codexClientID) })
	reg.SetModelQuotaExceeded(codexClientID, codexModel)
	if got, err := reg.GetFirstAvailableModel(""); err != nil || got != claudeModel {
		t.Fatalf("GetFirstAvailableModel = %q, %v; the test needs the healthy Claude model to be the global choice", got, err)
	}

	providers, model, errMsg := h.getRequestDetailsWithOptions(ctx, "auto", false, false)
	if errMsg != nil {
		t.Fatalf("auto with a cooling-down Codex: %d %v", errMsg.StatusCode, errMsg.Error)
	}
	if model != codexModel || !slices.Equal(providers, []string{"codex"}) {
		t.Fatalf("auto resolved to %q with providers %v; want %q on codex", model, providers, codexModel)
	}
}
