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
	if model == claudeModel || slices.Equal(providers, []string{"claude"}) {
		t.Fatalf("auto resolved to %q with providers %v; want a model of an enabled provider", model, providers)
	}
}

// When the key's only registered provider is disabled and its clients are all
// cooling down, "auto" still resolves to that provider's model, so the request
// reaches the execute gate and gets the 403 instead of a 400 for model "auto".
func TestAutoModelResolvesDisabledProviderModelWhileItCoolsDown(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const (
		disabledKey    = "auto-model-cooling-claude-disabled-key"
		claudeClientID = "auto-model-cooling-claude-client"
		claudeModel    = "auto-model-cooling-claude-model"
	)
	cfg := &config.Config{SDKConfig: config.SDKConfig{APIKeys: []string{disabledKey}}}
	cfg.Routing.KeyProfiles = map[string]*config.RoutingProfile{
		disabledKey: {Providers: config.ProvidersRoutingConfig{Claude: config.ProviderRoutingConfig{Disabled: true}}},
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	h := NewBaseAPIHandlers(&cfg.SDKConfig, manager)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(claudeClientID, "claude", []*registry.ModelInfo{{ID: claudeModel, Created: int64(1) << 62}})
	t.Cleanup(func() { reg.UnregisterClient(claudeClientID) })
	reg.SetModelQuotaExceeded(claudeClientID, claudeModel)
	if got, err := reg.GetFirstAvailableModel(""); err == nil {
		t.Fatalf("GetFirstAvailableModel = %q; the test needs a registry without an available model", got)
	}

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("userApiKey", disabledKey)
	ctx := context.WithValue(context.Background(), "gin", c)
	providers, model, errMsg := h.getRequestDetailsWithOptions(ctx, "auto", false, false)
	if errMsg != nil {
		t.Fatalf("auto with a cooling-down disabled Claude: %d %v, want the Claude model for the 403 gate", errMsg.StatusCode, errMsg.Error)
	}
	if model != claudeModel || !slices.Equal(providers, []string{"claude"}) {
		t.Fatalf("auto resolved to %q with providers %v; want %q on claude", model, providers, claudeModel)
	}
}
