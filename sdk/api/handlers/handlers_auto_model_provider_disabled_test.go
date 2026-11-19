package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
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
		return context.WithValue(context.Background(), "gin", c) //nolint:staticcheck // "gin" is the shared Gin context key contract.
	}

	disabledFilter := h.AutoModelProviderFilter(ctxWithKey(disabledKey))
	if disabledFilter("claude") || !disabledFilter("codex") {
		t.Fatalf("Claude-disabled filter: claude = %t, codex = %t; want false, true", disabledFilter("claude"), disabledFilter("codex"))
	}
	// A profile that disables nothing keeps the unfiltered resolution.
	if plainFilter := h.AutoModelProviderFilter(ctxWithKey(plainKey)); plainFilter != nil {
		t.Fatalf("plain filter: claude = %t, codex = %t; want a nil filter", plainFilter("claude"), plainFilter("codex"))
	}

	providers, model, errMsg := h.getRequestDetailsWithOptions(ctxWithKey(disabledKey), "auto", false, modelExecutionOptions{})
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
	return newAutoModelHandler(t, key, &config.RoutingProfile{Providers: config.ProvidersRoutingConfig{Claude: config.ProviderRoutingConfig{Disabled: true}}})
}

// newAutoModelHandler returns a handler that gives key the routing profile
// profile (the shared one when nil), and the context of a request with that key.
func newAutoModelHandler(t *testing.T, key string, profile *config.RoutingProfile) (*BaseAPIHandler, context.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{SDKConfig: config.SDKConfig{APIKeys: []string{key}}}
	if profile != nil {
		cfg.Routing.KeyProfiles = map[string]*config.RoutingProfile{key: profile}
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("userApiKey", key)
	return NewBaseAPIHandlers(&cfg.SDKConfig, manager), context.WithValue(context.Background(), "gin", c) //nolint:staticcheck // "gin" is the shared Gin context key contract.
}

// When the key's only registered provider is disabled and none of its clients is
// available (cooling down, or suspended for a reason that hides the model from
// the listing), "auto" still resolves to that provider's model, so the request
// gets the 403, refused while routing, instead of a 400 for model "auto".
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
			if got, err := reg.GetFirstRegisteredModelMatching(func(provider string) bool { return provider != "claude" }, nil); err == nil {
				t.Fatalf("GetFirstRegisteredModelMatching(not claude) = %q; the test needs a registry where only Claude registers a model", got)
			}

			if model := h.ResolveAutoModelForRequest(ctx, "auto"); model != claudeModel {
				t.Fatalf("auto resolved to %q; want %q", model, claudeModel)
			}
			_, _, errMsg := h.getRequestDetailsWithOptions(ctx, "auto", false, modelExecutionOptions{})
			if errMsg == nil || errMsg.StatusCode != http.StatusForbidden || !coreauth.IsProviderDisabledError(errMsg.Error) {
				t.Fatalf("auto with an unavailable disabled Claude: %+v, want the provider_disabled 403", errMsg)
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

	providers, model, errMsg := h.getRequestDetailsWithOptions(ctx, "auto", false, modelExecutionOptions{})
	if errMsg != nil {
		t.Fatalf("auto with a cooling-down Codex: %d %v", errMsg.StatusCode, errMsg.Error)
	}
	if model != codexModel || !slices.Equal(providers, []string{"codex"}) {
		t.Fatalf("auto resolved to %q with providers %v; want %q on codex", model, providers, codexModel)
	}
}

// A provider may register a model ID that carries a thinking suffix while another
// provider registers its base model. Routing sends that ID to the base model's
// providers, so "auto" must not count it as served by its own provider: with the
// base model's provider disabled, "auto" skips the ID for a model the key can run.
func TestAutoModelSkipsSuffixedModelRoutedToDisabledProvider(t *testing.T) {
	const (
		claudeClientID = "auto-model-suffixed-claude-client"
		codexClientID  = "auto-model-suffixed-codex-client"
		baseModel      = "auto-model-suffixed-shared"
		suffixedModel  = baseModel + "(high)"
		codexModel     = "auto-model-suffixed-codex-model"
	)
	h, ctx := newAutoModelDisabledClaudeHandler(t, "auto-model-suffixed-claude-disabled-key")
	reg := registry.GetGlobalRegistry()
	const newest = int64(1) << 62
	reg.RegisterClient(claudeClientID, "claude", []*registry.ModelInfo{{ID: baseModel, Created: newest}})
	t.Cleanup(func() { reg.UnregisterClient(claudeClientID) })
	reg.RegisterClient(codexClientID, "codex", []*registry.ModelInfo{
		{ID: suffixedModel, Created: newest},
		{ID: codexModel, Created: newest - 1},
	})
	t.Cleanup(func() { reg.UnregisterClient(codexClientID) })

	providers, model, errMsg := h.getRequestDetailsWithOptions(ctx, "auto", false, modelExecutionOptions{})
	if errMsg != nil {
		t.Fatalf("auto with a suffixed Codex model shadowed by a disabled Claude: %d %v", errMsg.StatusCode, errMsg.Error)
	}
	if model != codexModel || !slices.Equal(providers, []string{"codex"}) {
		t.Fatalf("auto resolved to %q with providers %v; want %q on codex", model, providers, codexModel)
	}
}

// Availability is counted where routing sends a model: a healthy Codex client of
// "x(high)" does not make it a candidate when the request routes to the clients
// of "x", whose only enabled provider is cooling down. "auto" picks an older
// model the key can run instead.
func TestAutoModelCountsClientsOfRoutedModel(t *testing.T) {
	const (
		claudeClientID = "auto-model-routed-claude-client"
		geminiClientID = "auto-model-routed-gemini-client"
		codexClientID  = "auto-model-routed-codex-client"
		baseModel      = "auto-model-routed-shared"
		suffixedModel  = baseModel + "(high)"
		codexModel     = "auto-model-routed-codex-model"
	)
	h, ctx := newAutoModelDisabledClaudeHandler(t, "auto-model-routed-claude-disabled-key")
	reg := registry.GetGlobalRegistry()
	const newest = int64(1) << 62
	for _, client := range []struct {
		id, provider string
		models       []*registry.ModelInfo
	}{
		{claudeClientID, "claude", []*registry.ModelInfo{{ID: baseModel, Created: newest}}},
		{geminiClientID, "gemini", []*registry.ModelInfo{{ID: baseModel, Created: newest}}},
		{codexClientID, "codex", []*registry.ModelInfo{{ID: suffixedModel, Created: newest}, {ID: codexModel, Created: newest - 1}}},
	} {
		reg.RegisterClient(client.id, client.provider, client.models)
		id := client.id
		t.Cleanup(func() { reg.UnregisterClient(id) })
	}
	reg.SetModelQuotaExceeded(geminiClientID, baseModel)

	providers, model, errMsg := h.getRequestDetailsWithOptions(ctx, "auto", false, modelExecutionOptions{})
	if errMsg != nil {
		t.Fatalf("auto with the routed model cooling down: %d %v", errMsg.StatusCode, errMsg.Error)
	}
	if model != codexModel || !slices.Equal(providers, []string{"codex"}) {
		t.Fatalf("auto resolved to %q with providers %v; want %q on codex", model, providers, codexModel)
	}
}

// "auto" with a thinking suffix judges each model by the name execution routes,
// suffix included: a Codex "x(v2)" requested as "x(v2)(high)" routes to Codex
// through "x(v2)", even though a disabled Claude registers "x".
func TestAutoModelWithSuffixJudgesSuffixedName(t *testing.T) {
	const (
		claudeClientID = "auto-model-suffix-claude-client"
		codexClientID  = "auto-model-suffix-codex-client"
		claudeModel    = "auto-model-suffix-x"
		codexModel     = claudeModel + "(v2)"
	)
	h, ctx := newAutoModelDisabledClaudeHandler(t, "auto-model-suffix-claude-disabled-key")
	reg := registry.GetGlobalRegistry()
	const newest = int64(1) << 62
	reg.RegisterClient(claudeClientID, "claude", []*registry.ModelInfo{{ID: claudeModel, Created: newest}})
	t.Cleanup(func() { reg.UnregisterClient(claudeClientID) })
	reg.RegisterClient(codexClientID, "codex", []*registry.ModelInfo{{ID: codexModel, Created: newest}})
	t.Cleanup(func() { reg.UnregisterClient(codexClientID) })

	providers, model, errMsg := h.getRequestDetailsWithOptions(ctx, "auto(high)", false, modelExecutionOptions{})
	if errMsg != nil {
		t.Fatalf("auto(high) with a Codex %q: %d %v", codexModel, errMsg.StatusCode, errMsg.Error)
	}
	if want := codexModel + "(high)"; model != want || !slices.Equal(providers, []string{"codex"}) {
		t.Fatalf("auto(high) resolved to %q with providers %v; want %q on codex", model, providers, want)
	}
}

// A profile that disables no provider resolves "auto" as before this switch
// existed: each listed model is counted on its own registration, so a
// cooling-down "x(high)" does not win over a healthy "other" because an older
// "x" of another provider is healthy.
func TestAutoModelUnfilteredWhenProfileDisablesNothing(t *testing.T) {
	const (
		suffixedClientID = "auto-model-plain-suffixed-client"
		otherClientID    = "auto-model-plain-other-client"
		baseClientID     = "auto-model-plain-base-client"
		baseModel        = "auto-model-plain-x"
		suffixedModel    = baseModel + "(high)"
		otherModel       = "auto-model-plain-other"
	)
	h, ctx := newAutoModelHandler(t, "auto-model-plain-key", nil)
	if filter := h.AutoModelProviderFilter(ctx); filter != nil {
		t.Fatal("AutoModelProviderFilter returned a filter for a profile that disables nothing, want nil")
	}
	reg := registry.GetGlobalRegistry()
	const newest = int64(1) << 62
	for _, client := range []struct {
		id, provider, model string
		created             int64
	}{
		{suffixedClientID, "codex", suffixedModel, newest},
		{otherClientID, "gemini", otherModel, newest - 1},
		{baseClientID, "claude", baseModel, newest - 2},
	} {
		reg.RegisterClient(client.id, client.provider, []*registry.ModelInfo{{ID: client.model, Created: client.created}})
		id := client.id
		t.Cleanup(func() { reg.UnregisterClient(id) })
	}
	reg.SetModelQuotaExceeded(suffixedClientID, suffixedModel)

	providers, model, errMsg := h.getRequestDetailsWithOptions(ctx, "auto", false, modelExecutionOptions{})
	if errMsg != nil {
		t.Fatalf("auto under a profile that disables nothing: %d %v", errMsg.StatusCode, errMsg.Error)
	}
	if model != otherModel || !slices.Equal(providers, []string{"gemini"}) {
		t.Fatalf("auto resolved to %q with providers %v; want %q on gemini", model, providers, otherModel)
	}
}

// Routing finds a mixed-case base model through its lowercase registration, so
// "auto" counts the clients of that registration: a Codex "Auto-Model-Case-X(high)"
// routed to a healthy Gemini "auto-model-case-x" is the newest model the key can
// run, not skipped for an older one.
func TestAutoModelCountsLowercaseRoutedModel(t *testing.T) {
	const (
		codexClientID  = "auto-model-case-codex-client"
		geminiClientID = "auto-model-case-gemini-client"
		mixedModel     = "Auto-Model-Case-X(high)"
		lowerModel     = "auto-model-case-x"
		olderModel     = "auto-model-case-older"
	)
	h, ctx := newAutoModelDisabledClaudeHandler(t, "auto-model-case-claude-disabled-key")
	reg := registry.GetGlobalRegistry()
	const newest = int64(1) << 62
	reg.RegisterClient(codexClientID, "codex", []*registry.ModelInfo{
		{ID: mixedModel, Created: newest},
		{ID: olderModel, Created: newest - 1},
	})
	t.Cleanup(func() { reg.UnregisterClient(codexClientID) })
	reg.RegisterClient(geminiClientID, "gemini", []*registry.ModelInfo{{ID: lowerModel, Created: newest - 2}})
	t.Cleanup(func() { reg.UnregisterClient(geminiClientID) })

	providers, model, errMsg := h.getRequestDetailsWithOptions(ctx, "auto", false, modelExecutionOptions{})
	if errMsg != nil {
		t.Fatalf("auto with a mixed-case model routed to a lowercase registration: %d %v", errMsg.StatusCode, errMsg.Error)
	}
	if model != mixedModel || !slices.Equal(providers, []string{"gemini"}) {
		t.Fatalf("auto resolved to %q with providers %v; want %q on gemini", model, providers, mixedModel)
	}
}

// The alternative-target probe resolves "auto" like the request but does not
// log the refusal, so the refusal log counts refused requests, not routing
// checks: only the request itself writes the line.
func TestAutoModelProbeDoesNotLogRefusal(t *testing.T) {
	const (
		claudeClientID = "auto-model-probe-claude-client"
		claudeModel    = "auto-model-probe-claude-model"
		refusalLog     = "Refusing request: provider disabled by routing profile"
	)
	h, ctx := newAutoModelDisabledClaudeHandler(t, "auto-model-probe-claude-disabled-key")
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(claudeClientID, "claude", []*registry.ModelInfo{{ID: claudeModel, Created: int64(1) << 62}})
	t.Cleanup(func() { reg.UnregisterClient(claudeClientID) })
	if got, err := reg.GetFirstRegisteredModelMatching(func(provider string) bool { return provider != "claude" }, nil); err == nil {
		t.Fatalf("GetFirstRegisteredModelMatching(not claude) = %q; the test needs a registry where only Claude registers a model", got)
	}

	logger := log.StandardLogger()
	savedHooks := make(log.LevelHooks)
	for level, hooks := range logger.Hooks {
		savedHooks[level] = append([]log.Hook(nil), hooks...)
	}
	savedLevel := logger.GetLevel()
	logger.SetLevel(log.InfoLevel)
	hook := logtest.NewLocal(logger)
	t.Cleanup(func() {
		logger.ReplaceHooks(savedHooks)
		logger.SetLevel(savedLevel)
	})
	refusals := func() int {
		count := 0
		for _, entry := range hook.AllEntries() {
			if entry.Message == refusalLog {
				count++
			}
		}
		return count
	}

	if h.HasAlternativeExecutionTarget(ctx, "openai", "auto", []byte(`{"model":"auto"}`), "") {
		t.Fatal("HasAlternativeExecutionTarget = true for auto routed only to a disabled Claude, want false")
	}
	if n := refusals(); n != 0 {
		t.Fatalf("probe logged %d refusals, want none", n)
	}
	_, _, errMsg := h.getRequestDetailsWithOptions(ctx, "auto", false, modelExecutionOptions{})
	if errMsg == nil || errMsg.StatusCode != http.StatusForbidden || !coreauth.IsProviderDisabledError(errMsg.Error) {
		t.Fatalf("auto routed only to a disabled Claude: %+v, want the provider_disabled 403", errMsg)
	}
	if n := refusals(); n != 1 {
		t.Fatalf("request logged %d refusals, want 1", n)
	}
}

// A request reads one routing profile from resolving "auto" to execution: when
// a reload turns a Codex-only key into a Claude-only one after "auto" picked the
// Codex model, the request still runs on Codex instead of getting a 403 that
// neither profile gives on its own. Non-streaming, streaming and count requests
// each pin the profile.
func TestAutoModelExecutionKeepsProfileAcrossReload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const (
		key         = "auto-model-reload-key"
		claudeModel = "auto-model-reload-claude-model"
		codexModel  = "auto-model-reload-codex-model"
	)
	profileConfig := func(claudeDisabled, codexDisabled bool) *config.Config {
		cfg := &config.Config{SDKConfig: config.SDKConfig{APIKeys: []string{key}}}
		cfg.Routing.KeyProfiles = map[string]*config.RoutingProfile{key: {Providers: config.ProvidersRoutingConfig{
			Claude: config.ProviderRoutingConfig{Disabled: claudeDisabled},
			Codex:  config.ProviderRoutingConfig{Disabled: codexDisabled},
		}}}
		return cfg
	}
	codexOnly := profileConfig(true, false)
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(codexOnly)
	executors := map[string]*interceptorCaptureExecutor{}
	const newest = int64(1) << 62
	for _, client := range []struct {
		provider, model string
		created         int64
	}{
		{"claude", claudeModel, newest},
		{"codex", codexModel, newest - 1},
	} {
		executor := &interceptorCaptureExecutor{provider: client.provider, stream: func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
			chunks := make(chan coreexecutor.StreamChunk, 1)
			chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"chunk":true}`)}
			close(chunks)
			return &coreexecutor.StreamResult{Chunks: chunks}, nil
		}}
		executors[client.provider] = executor
		manager.RegisterExecutor(executor)
		auth := &coreauth.Auth{ID: "auto-model-reload-" + client.provider, Provider: client.provider, Status: coreauth.StatusActive}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("manager.Register(%s): %v", client.provider, errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, client.provider, []*registry.ModelInfo{{ID: client.model, Created: client.created}})
		id := auth.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	h := NewBaseAPIHandlers(&codexOnly.SDKConfig, manager)
	// The reload lands after routing resolved "auto" and before execution.
	h.SetPluginHost(&handlerInterceptorTestHost{
		interceptRequestBeforeAuth: func(_ context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
			manager.SetConfig(profileConfig(false, true))
			return pluginapi.RequestInterceptResponse{Headers: cloneHeader(req.Headers)}
		},
	})
	body := []byte(`{"model":"auto"}`)
	for _, tc := range []struct {
		name string
		run  func(ctx context.Context) *interfaces.ErrorMessage
	}{
		{"execute", func(ctx context.Context) *interfaces.ErrorMessage {
			_, _, errMsg := h.ExecuteWithAuthManager(ctx, "openai", "auto", body, "")
			return errMsg
		}},
		{"stream", func(ctx context.Context) *interfaces.ErrorMessage {
			dataChan, _, errChan := h.ExecuteStreamWithAuthManager(ctx, "openai", "auto", body, "")
			if dataChan != nil {
				for range dataChan {
				}
			}
			for errMsg := range errChan {
				if errMsg != nil {
					return errMsg
				}
			}
			return nil
		}},
		{"count", func(ctx context.Context) *interfaces.ErrorMessage {
			_, _, errMsg := h.ExecuteCountWithAuthManager(ctx, "openai", "auto", body, "")
			return errMsg
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager.SetConfig(codexOnly)
			for _, executor := range executors {
				executor.capture(coreexecutor.Request{}, coreexecutor.Options{})
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Set("userApiKey", key)
			if errMsg := tc.run(context.WithValue(context.Background(), "gin", c)); errMsg != nil { //nolint:staticcheck // "gin" is the shared Gin context key contract.
				t.Fatalf("auto across a reload: %d %v, want it served by Codex", errMsg.StatusCode, errMsg.Error)
			}
			if req, _ := executors["codex"].captured(); req.Model != codexModel {
				t.Fatalf("codex executor model = %q, want %q", req.Model, codexModel)
			}
			if req, _ := executors["claude"].captured(); req.Model != "" {
				t.Fatalf("claude executor ran %q, want no Claude call", req.Model)
			}
		})
	}
}

// Credential selection matches a request's credentials by its base model, so a
// model registered only under its suffixed name ("x(high)" without "x") has no
// credential that serves it. "auto" skips such a model for an older one the key
// can run instead of resolving to a request that selection rejects.
func TestAutoModelSkipsModelRegisteredOnlyWithSuffix(t *testing.T) {
	const (
		codexClientID = "auto-model-suffix-only-codex-client"
		suffixedModel = "auto-model-suffix-only-x(high)"
		olderModel    = "auto-model-suffix-only-older"
	)
	h, ctx := newAutoModelDisabledClaudeHandler(t, "auto-model-suffix-only-claude-disabled-key")
	reg := registry.GetGlobalRegistry()
	const newest = int64(1) << 62
	reg.RegisterClient(codexClientID, "codex", []*registry.ModelInfo{
		{ID: suffixedModel, Created: newest},
		{ID: olderModel, Created: newest - 1},
	})
	t.Cleanup(func() { reg.UnregisterClient(codexClientID) })

	providers, model, errMsg := h.getRequestDetailsWithOptions(ctx, "auto", false, modelExecutionOptions{})
	if errMsg != nil {
		t.Fatalf("auto with a model registered only with its suffix: %d %v", errMsg.StatusCode, errMsg.Error)
	}
	if model != olderModel || !slices.Equal(providers, []string{"codex"}) {
		t.Fatalf("auto resolved to %q with providers %v; want %q on codex", model, providers, olderModel)
	}
}

// Credential selection matches a client by the request's base model
// case-insensitively, so a client that registers "x(high)" and "X" but not "x"
// serves "x(high)", which routing sends to it by the full name. "auto" counts
// that client and resolves to the newest model, which then runs on it, instead
// of skipping it for an older one.
func TestAutoModelCountsSuffixedModelWhoseClientRegistersBaseInAnotherCase(t *testing.T) {
	const (
		codexAuthID   = "auto-model-mixed-base-codex"
		suffixedModel = "auto-model-mixed-base-x(high)"
		mixedBase     = "Auto-Model-Mixed-Base-X"
		olderModel    = "auto-model-mixed-base-older"
	)
	h, ctx := newAutoModelDisabledClaudeHandler(t, "auto-model-mixed-base-claude-disabled-key")
	codex := &interceptorCaptureExecutor{provider: "codex"}
	h.AuthManager.RegisterExecutor(codex)
	if _, errRegister := h.AuthManager.Register(context.Background(), &coreauth.Auth{ID: codexAuthID, Provider: "codex", Status: coreauth.StatusActive}); errRegister != nil {
		t.Fatalf("manager.Register(codex): %v", errRegister)
	}
	reg := registry.GetGlobalRegistry()
	const newest = int64(1) << 62
	reg.RegisterClient(codexAuthID, "codex", []*registry.ModelInfo{
		{ID: suffixedModel, Created: newest},
		{ID: olderModel, Created: newest - 1},
		{ID: mixedBase, Created: newest - 2},
	})
	t.Cleanup(func() { reg.UnregisterClient(codexAuthID) })

	providers, model, errMsg := h.getRequestDetailsWithOptions(ctx, "auto", false, modelExecutionOptions{})
	if errMsg != nil {
		t.Fatalf("auto with a base model registered in another case: %d %v", errMsg.StatusCode, errMsg.Error)
	}
	if model != suffixedModel || !slices.Equal(providers, []string{"codex"}) {
		t.Fatalf("auto resolved to %q with providers %v; want %q on codex", model, providers, suffixedModel)
	}
	if _, _, errExec := h.ExecuteWithAuthManager(ctx, "openai", "auto", []byte(`{"model":"auto"}`), ""); errExec != nil {
		t.Fatalf("execute auto: %d %v, want it served by Codex", errExec.StatusCode, errExec.Error)
	}
	if req, _ := codex.captured(); req.Model == "" || req.Model == olderModel {
		t.Fatalf("codex executor model = %q, want the newest model", req.Model)
	}
}

// registerAutoModelCodexAuth registers a Codex auth with models in the manager
// and the global registry.
func registerAutoModelCodexAuth(t *testing.T, h *BaseAPIHandler, auth *coreauth.Auth, models []*registry.ModelInfo) {
	t.Helper()
	if _, errRegister := h.AuthManager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("manager.Register(%s): %v", auth.ID, errRegister)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", models)
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
}

// Credential selection runs "x(high)", routed to Codex by its full name, on any
// Codex credential that registers the base model in any case, not only on the
// credentials that register "x(high)": with credential A registering "x(high)"
// and credential B registering "X", "auto" resolves to "x(high)" and the request
// runs on B, as an explicit "x(high)" does.
func TestAutoModelCountsSuffixedModelServedByAnotherCredential(t *testing.T) {
	const (
		suffixedAuthID = "auto-model-split-suffixed-codex"
		baseAuthID     = "auto-model-split-base-codex"
		suffixedModel  = "auto-model-split-x(high)"
		mixedBase      = "Auto-Model-Split-X"
		olderModel     = "auto-model-split-older"
	)
	h, ctx := newAutoModelDisabledClaudeHandler(t, "auto-model-split-claude-disabled-key")
	var executedAuthIDs []string
	codex := &interceptorCaptureExecutor{provider: "codex", execute: func(_ context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
		executedAuthIDs = append(executedAuthIDs, auth.ID)
		return coreexecutor.Response{Payload: []byte("ok")}, nil
	}}
	h.AuthManager.RegisterExecutor(codex)
	const newest = int64(1) << 62
	registerAutoModelCodexAuth(t, h, &coreauth.Auth{ID: suffixedAuthID, Provider: "codex", Status: coreauth.StatusActive}, []*registry.ModelInfo{
		{ID: suffixedModel, Created: newest},
		{ID: olderModel, Created: newest - 1},
	})
	registerAutoModelCodexAuth(t, h, &coreauth.Auth{ID: baseAuthID, Provider: "codex", Status: coreauth.StatusActive}, []*registry.ModelInfo{
		{ID: mixedBase, Created: newest - 2},
	})

	providers, model, errMsg := h.getRequestDetailsWithOptions(ctx, "auto", false, modelExecutionOptions{})
	if errMsg != nil {
		t.Fatalf("auto with the base model on another credential: %d %v", errMsg.StatusCode, errMsg.Error)
	}
	if model != suffixedModel || !slices.Equal(providers, []string{"codex"}) {
		t.Fatalf("auto resolved to %q with providers %v; want %q on codex", model, providers, suffixedModel)
	}
	if _, _, errExec := h.ExecuteWithAuthManager(ctx, "openai", "auto", []byte(`{"model":"auto"}`), ""); errExec != nil {
		t.Fatalf("execute auto: %d %v, want it served by Codex", errExec.StatusCode, errExec.Error)
	}
	if !slices.Equal(executedAuthIDs, []string{baseAuthID}) {
		t.Fatalf("executed auth IDs = %v, want [%s]", executedAuthIDs, baseAuthID)
	}
}

// Credential selection also accepts a credential for the upstream model of its
// OAuth alias, so a forked alias "x(high)" whose credential registers the
// upstream model "y" (and not "x") serves "x(high)". "auto" counts it and
// resolves to "x(high)", which then runs on that credential as "y(high)" (the
// alias keeps the request's suffix).
func TestAutoModelCountsForkedSuffixedOAuthAlias(t *testing.T) {
	const (
		codexAuthID   = "auto-model-forked-alias-codex"
		aliasModel    = "auto-model-forked-alias-x(high)"
		upstreamModel = "auto-model-forked-alias-upstream-y"
		olderModel    = "auto-model-forked-alias-older"
	)
	h, ctx := newAutoModelDisabledClaudeHandler(t, "auto-model-forked-alias-claude-disabled-key")
	codex := &interceptorCaptureExecutor{provider: "codex"}
	h.AuthManager.RegisterExecutor(codex)
	auth := &coreauth.Auth{ID: codexAuthID, Provider: "codex", Status: coreauth.StatusActive}
	coreauth.SetOAuthModelAliasesAttribute(auth, []config.OAuthModelAlias{{Name: upstreamModel, Alias: aliasModel, Fork: true}})
	const newest = int64(1) << 62
	// A forked alias registers the alias next to its upstream model.
	registerAutoModelCodexAuth(t, h, auth, []*registry.ModelInfo{
		{ID: aliasModel, Created: newest},
		{ID: olderModel, Created: newest - 1},
		{ID: upstreamModel, Created: newest - 2},
	})

	providers, model, errMsg := h.getRequestDetailsWithOptions(ctx, "auto", false, modelExecutionOptions{})
	if errMsg != nil {
		t.Fatalf("auto with a forked suffixed alias: %d %v", errMsg.StatusCode, errMsg.Error)
	}
	if model != aliasModel || !slices.Equal(providers, []string{"codex"}) {
		t.Fatalf("auto resolved to %q with providers %v; want %q on codex", model, providers, aliasModel)
	}
	if _, _, errExec := h.ExecuteWithAuthManager(ctx, "openai", "auto", []byte(`{"model":"auto"}`), ""); errExec != nil {
		t.Fatalf("execute auto: %d %v, want it served by Codex", errExec.StatusCode, errExec.Error)
	}
	if req, _ := codex.captured(); req.Model != upstreamModel+"(high)" {
		t.Fatalf("codex executor model = %q, want the alias upstream %q", req.Model, upstreamModel+"(high)")
	}
}

// Credential selection checks a credential for "x(high)", its forked alias of
// "y", by the cooldown of "y", also when the credential registers "x", which
// routes the request. With "y" cooling down and "x" healthy, "auto" skips
// "x(high)" for an older model the credential can run instead of resolving to a
// request that gets a cooldown error.
func TestAutoModelChecksCooldownOfForkedAliasUpstream(t *testing.T) {
	const (
		codexAuthID   = "auto-model-alias-cooldown-codex"
		aliasModel    = "auto-model-alias-cooldown-x(high)"
		baseModel     = "auto-model-alias-cooldown-x"
		upstreamModel = "auto-model-alias-cooldown-upstream-y"
		olderModel    = "auto-model-alias-cooldown-older"
	)
	h, ctx := newAutoModelDisabledClaudeHandler(t, "auto-model-alias-cooldown-claude-disabled-key")
	codex := &interceptorCaptureExecutor{provider: "codex"}
	h.AuthManager.RegisterExecutor(codex)
	auth := &coreauth.Auth{ID: codexAuthID, Provider: "codex", Status: coreauth.StatusActive}
	coreauth.SetOAuthModelAliasesAttribute(auth, []config.OAuthModelAlias{{Name: upstreamModel, Alias: aliasModel, Fork: true}})
	const newest = int64(1) << 62
	registerAutoModelCodexAuth(t, h, auth, []*registry.ModelInfo{
		{ID: aliasModel, Created: newest},
		{ID: olderModel, Created: newest - 1},
		{ID: baseModel, Created: newest - 2},
		{ID: upstreamModel, Created: newest - 3},
	})
	retryAfter := time.Hour
	h.AuthManager.MarkResult(context.Background(), coreauth.Result{
		AuthID: codexAuthID, Provider: "codex", Model: upstreamModel,
		Error: &coreauth.Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate limited"}, RetryAfter: &retryAfter,
	})
	reg := registry.GetGlobalRegistry()
	if reg.GetModelCount(baseModel) == 0 || reg.GetModelCount(upstreamModel) != 0 {
		t.Fatalf("model counts: base %d, upstream %d; the test needs a healthy base and a cooling upstream", reg.GetModelCount(baseModel), reg.GetModelCount(upstreamModel))
	}

	providers, model, errMsg := h.getRequestDetailsWithOptions(ctx, "auto", false, modelExecutionOptions{})
	if errMsg != nil {
		t.Fatalf("auto with the alias upstream cooling down: %d %v", errMsg.StatusCode, errMsg.Error)
	}
	if model != olderModel || !slices.Equal(providers, []string{"codex"}) {
		t.Fatalf("auto resolved to %q with providers %v; want %q on codex", model, providers, olderModel)
	}
	if _, _, errExec := h.ExecuteWithAuthManager(ctx, "openai", "auto", []byte(`{"model":"auto"}`), ""); errExec != nil {
		t.Fatalf("execute auto: %d %v, want it served by Codex", errExec.StatusCode, errExec.Error)
	}
	if req, _ := codex.captured(); req.Model != olderModel {
		t.Fatalf("codex executor model = %q, want %q", req.Model, olderModel)
	}
}

// Credential selection maps a request through one OAuth alias and checks the
// credential's state for the model it maps to. With aliases "b" -> "a" and
// "a" -> "c", "b(high)" is checked by the state of "a", while the auth manager
// projects onto the credential's registration of "a" the state of "c". With "a"
// cooling down and "c" healthy, "auto(high)" skips "b(high)" for an older model
// the credential can run instead of resolving to a request that gets a cooldown
// error.
func TestAutoModelChecksCooldownOfChainedAliasSelectionKey(t *testing.T) {
	const (
		codexAuthID = "auto-model-chained-alias-codex"
		aliasModel  = "auto-model-chained-alias-b"
		middleModel = "auto-model-chained-alias-a"
		lastModel   = "auto-model-chained-alias-c"
		olderModel  = "auto-model-chained-alias-older"
	)
	h, ctx := newAutoModelDisabledClaudeHandler(t, "auto-model-chained-alias-claude-disabled-key")
	codex := &interceptorCaptureExecutor{provider: "codex"}
	h.AuthManager.RegisterExecutor(codex)
	auth := &coreauth.Auth{ID: codexAuthID, Provider: "codex", Status: coreauth.StatusActive}
	coreauth.SetOAuthModelAliasesAttribute(auth, []config.OAuthModelAlias{
		{Name: middleModel, Alias: aliasModel, Fork: true},
		{Name: lastModel, Alias: middleModel, Fork: true},
	})
	const newest = int64(1) << 62
	registerAutoModelCodexAuth(t, h, auth, []*registry.ModelInfo{
		{ID: aliasModel, Created: newest},
		{ID: olderModel, Created: newest - 1},
		{ID: middleModel, Created: newest - 2},
		{ID: lastModel, Created: newest - 3},
	})
	retryAfter := time.Hour
	h.AuthManager.MarkResult(context.Background(), coreauth.Result{
		AuthID: codexAuthID, Provider: "codex", Model: middleModel, RouteModel: aliasModel + "(high)",
		Error: &coreauth.Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate limited"}, RetryAfter: &retryAfter,
	})
	reg := registry.GetGlobalRegistry()
	if reg.GetModelCount(aliasModel) != 0 || reg.GetModelCount(middleModel) == 0 {
		t.Fatalf("model counts: alias %d, middle %d; the test needs the registration of %q to carry the healthy state of %q", reg.GetModelCount(aliasModel), reg.GetModelCount(middleModel), middleModel, lastModel)
	}

	providers, model, errMsg := h.getRequestDetailsWithOptions(ctx, "auto(high)", false, modelExecutionOptions{})
	if errMsg != nil {
		t.Fatalf("auto(high) with the chained alias selection key cooling down: %d %v", errMsg.StatusCode, errMsg.Error)
	}
	if model != olderModel+"(high)" || !slices.Equal(providers, []string{"codex"}) {
		t.Fatalf("auto(high) resolved to %q with providers %v; want %q on codex", model, providers, olderModel+"(high)")
	}
	if _, _, errExec := h.ExecuteWithAuthManager(ctx, "openai", "auto(high)", []byte(`{"model":"auto(high)"}`), ""); errExec != nil {
		t.Fatalf("execute auto(high): %d %v, want it served by Codex", errExec.StatusCode, errExec.Error)
	}
	if req, _ := codex.captured(); req.Model != olderModel+"(high)" {
		t.Fatalf("codex executor model = %q, want %q", req.Model, olderModel+"(high)")
	}
}

// Credential selection checks a use_siwc credential by the token the request
// uses: a Codex client's request uses the outer token, any other the nested SIWC
// token. With a valid SIWC token and an expired outer token, "auto" from a Codex
// client skips "x(high)", served only through that credential's forked alias,
// for an older model a healthy credential runs, while "auto" from another client
// still resolves to "x(high)".
func TestAutoModelChecksAliasSelectionKeyWithRequestToken(t *testing.T) {
	const (
		siwcAuthID    = "auto-model-siwc-alias-codex"
		healthyAuthID = "auto-model-siwc-healthy-codex"
		aliasModel    = "auto-model-siwc-alias-x(high)"
		upstreamModel = "auto-model-siwc-alias-upstream-y"
		olderModel    = "auto-model-siwc-older"
	)
	h, ctx := newAutoModelDisabledClaudeHandler(t, "auto-model-siwc-claude-disabled-key")
	var executedAuthIDs []string
	codex := &interceptorCaptureExecutor{provider: "codex", execute: func(_ context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
		executedAuthIDs = append(executedAuthIDs, auth.ID)
		return coreexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
	}}
	h.AuthManager.RegisterExecutor(codex)
	now := time.Now().UTC()
	siwcAuth := &coreauth.Auth{ID: siwcAuthID, Provider: "codex", Status: coreauth.StatusActive, Metadata: map[string]any{
		"type":         "codex",
		"access_token": "auto-model-siwc-outer-token",
		"expired":      now.Add(-time.Hour).Format(time.RFC3339),
		"use_siwc":     true,
		"siwc": map[string]any{
			"access_token": "auto-model-siwc-nested-token",
			"expired":      now.Add(time.Hour).Format(time.RFC3339),
		},
	}}
	coreauth.SetOAuthModelAliasesAttribute(siwcAuth, []config.OAuthModelAlias{{Name: upstreamModel, Alias: aliasModel, Fork: true}})
	const newest = int64(1) << 62
	registerAutoModelCodexAuth(t, h, siwcAuth, []*registry.ModelInfo{
		{ID: aliasModel, Created: newest},
		{ID: upstreamModel, Created: newest - 2},
	})
	registerAutoModelCodexAuth(t, h, &coreauth.Auth{ID: healthyAuthID, Provider: "codex", Status: coreauth.StatusActive}, []*registry.ModelInfo{
		{ID: olderModel, Created: newest - 1},
	})

	if _, model, errMsg := h.getRequestDetailsWithOptions(ctx, "auto", false, modelExecutionOptions{}); errMsg != nil || model != aliasModel {
		t.Fatalf("auto from another client = %q, %v; want %q on the valid SIWC token", model, errMsg, aliasModel)
	}

	codexCtx := coreauth.WithCodexClientRequest(ctx, true)
	providers, model, errMsg := h.getRequestDetailsWithOptions(codexCtx, "auto", false, modelExecutionOptions{})
	if errMsg != nil {
		t.Fatalf("auto from a Codex client with the outer token expired: %d %v", errMsg.StatusCode, errMsg.Error)
	}
	if model != olderModel || !slices.Equal(providers, []string{"codex"}) {
		t.Fatalf("auto from a Codex client resolved to %q with providers %v; want %q on codex", model, providers, olderModel)
	}
	if _, _, errExec := h.ExecuteWithAuthManager(codexCtx, "openai", "auto", []byte(`{"model":"auto"}`), ""); errExec != nil {
		t.Fatalf("execute auto from a Codex client: %d %v, want it served by the healthy credential", errExec.StatusCode, errExec.Error)
	}
	if !slices.Equal(executedAuthIDs, []string{healthyAuthID}) {
		t.Fatalf("executed auth IDs = %v, want [%s]", executedAuthIDs, healthyAuthID)
	}
}

// Credential selection resolves an OAuth alias of the full requested name
// case-insensitively, so with aliases "x" -> "y" and "x(HIGH)" -> "x" it checks
// "x(high)" by the state of "x", which the auth manager projects onto the
// credential's registration of "x(HIGH)", while its registration of "x" carries
// the state of "y". With "y" cooling down and "x" healthy, "auto(high)" still
// resolves to "x(high)", which then runs on that credential.
func TestAutoModelReadsSuffixedAliasRegistrationInAnotherCase(t *testing.T) {
	const (
		codexAuthID   = "auto-model-case-alias-codex"
		baseModel     = "auto-model-case-alias-x"
		suffixedAlias = "auto-model-case-alias-x(HIGH)"
		upstreamModel = "auto-model-case-alias-upstream-y"
		olderModel    = "auto-model-case-alias-older"
	)
	h, ctx := newAutoModelDisabledClaudeHandler(t, "auto-model-case-alias-claude-disabled-key")
	var executedAuthIDs []string
	codex := &interceptorCaptureExecutor{provider: "codex", execute: func(_ context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
		executedAuthIDs = append(executedAuthIDs, auth.ID)
		return coreexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
	}}
	h.AuthManager.RegisterExecutor(codex)
	auth := &coreauth.Auth{ID: codexAuthID, Provider: "codex", Status: coreauth.StatusActive}
	coreauth.SetOAuthModelAliasesAttribute(auth, []config.OAuthModelAlias{
		{Name: upstreamModel, Alias: baseModel, Fork: true},
		{Name: baseModel, Alias: suffixedAlias, Fork: true},
	})
	const newest = int64(1) << 62
	registerAutoModelCodexAuth(t, h, auth, []*registry.ModelInfo{
		{ID: baseModel, Created: newest},
		{ID: olderModel, Created: newest - 1},
		{ID: upstreamModel, Created: newest - 2},
		{ID: suffixedAlias, Created: newest - 3},
	})
	retryAfter := time.Hour
	h.AuthManager.MarkResult(context.Background(), coreauth.Result{
		AuthID: codexAuthID, Provider: "codex", Model: upstreamModel,
		Error: &coreauth.Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate limited"}, RetryAfter: &retryAfter,
	})
	reg := registry.GetGlobalRegistry()
	if reg.GetModelCount(baseModel) != 0 || reg.GetModelCount(suffixedAlias) == 0 {
		t.Fatalf("model counts: base %d, suffixed alias %d; the test needs the registration of %q to carry the cooling state of %q", reg.GetModelCount(baseModel), reg.GetModelCount(suffixedAlias), baseModel, upstreamModel)
	}

	providers, model, errMsg := h.getRequestDetailsWithOptions(ctx, "auto(high)", false, modelExecutionOptions{})
	if errMsg != nil {
		t.Fatalf("auto(high) with the base alias upstream cooling down: %d %v", errMsg.StatusCode, errMsg.Error)
	}
	if model != baseModel+"(high)" || !slices.Equal(providers, []string{"codex"}) {
		t.Fatalf("auto(high) resolved to %q with providers %v; want %q on codex", model, providers, baseModel+"(high)")
	}
	if _, _, errExec := h.ExecuteWithAuthManager(ctx, "openai", "auto(high)", []byte(`{"model":"auto(high)"}`), ""); errExec != nil {
		t.Fatalf("execute auto(high): %d %v, want it served by Codex", errExec.StatusCode, errExec.Error)
	}
	if !slices.Equal(executedAuthIDs, []string{codexAuthID}) {
		t.Fatalf("executed auth IDs = %v, want [%s]", executedAuthIDs, codexAuthID)
	}
}

// Credential selection checks a use_siwc credential by the token the request
// uses also for a model the credential registers directly, without an alias.
// With a valid SIWC token and an expired outer token, "auto" from a Codex client
// skips the newest model only that credential registers for an older model a
// healthy credential runs, while "auto" from another client still resolves to
// the newest model.
func TestAutoModelChecksDirectModelWithRequestToken(t *testing.T) {
	const (
		siwcAuthID    = "auto-model-siwc-direct-codex"
		healthyAuthID = "auto-model-siwc-direct-healthy-codex"
		directModel   = "auto-model-siwc-direct-x"
		olderModel    = "auto-model-siwc-direct-older"
	)
	h, ctx := newAutoModelDisabledClaudeHandler(t, "auto-model-siwc-direct-claude-disabled-key")
	var executedAuthIDs []string
	codex := &interceptorCaptureExecutor{provider: "codex", execute: func(_ context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
		executedAuthIDs = append(executedAuthIDs, auth.ID)
		return coreexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
	}}
	h.AuthManager.RegisterExecutor(codex)
	now := time.Now().UTC()
	siwcAuth := &coreauth.Auth{ID: siwcAuthID, Provider: "codex", Status: coreauth.StatusActive, Metadata: map[string]any{
		"type":         "codex",
		"access_token": "auto-model-siwc-direct-outer-token",
		"expired":      now.Add(-time.Hour).Format(time.RFC3339),
		"use_siwc":     true,
		"siwc": map[string]any{
			"access_token": "auto-model-siwc-direct-nested-token",
			"expired":      now.Add(time.Hour).Format(time.RFC3339),
		},
	}}
	const newest = int64(1) << 62
	registerAutoModelCodexAuth(t, h, siwcAuth, []*registry.ModelInfo{{ID: directModel, Created: newest}})
	registerAutoModelCodexAuth(t, h, &coreauth.Auth{ID: healthyAuthID, Provider: "codex", Status: coreauth.StatusActive}, []*registry.ModelInfo{
		{ID: olderModel, Created: newest - 1},
	})

	if _, model, errMsg := h.getRequestDetailsWithOptions(ctx, "auto", false, modelExecutionOptions{}); errMsg != nil || model != directModel {
		t.Fatalf("auto from another client = %q, %v; want %q on the valid SIWC token", model, errMsg, directModel)
	}

	codexCtx := coreauth.WithCodexClientRequest(ctx, true)
	providers, model, errMsg := h.getRequestDetailsWithOptions(codexCtx, "auto", false, modelExecutionOptions{})
	if errMsg != nil {
		t.Fatalf("auto from a Codex client with the outer token expired: %d %v", errMsg.StatusCode, errMsg.Error)
	}
	if model != olderModel || !slices.Equal(providers, []string{"codex"}) {
		t.Fatalf("auto from a Codex client resolved to %q with providers %v; want %q on codex", model, providers, olderModel)
	}
	if _, _, errExec := h.ExecuteWithAuthManager(codexCtx, "openai", "auto", []byte(`{"model":"auto"}`), ""); errExec != nil {
		t.Fatalf("execute auto from a Codex client: %d %v, want it served by the healthy credential", errExec.StatusCode, errExec.Error)
	}
	if !slices.Equal(executedAuthIDs, []string{healthyAuthID}) {
		t.Fatalf("executed auth IDs = %v, want [%s]", executedAuthIDs, healthyAuthID)
	}
}

// Filtered "auto" judges a model by the routed request, not by the global
// listing, which hides a model whose every client is suspended for a reason
// other than quota. With aliases "x" -> "y" and "x(HIGH)" -> "x", a failing "y"
// suspends the credential's registration of "x" and hides "x" from the listing,
// while "x(high)" is checked by the healthy state of "x". "auto(high)" still
// resolves to "x(high)", which then runs on that credential.
func TestAutoModelJudgesSuffixedRouteOfModelHiddenFromListing(t *testing.T) {
	const (
		codexAuthID   = "auto-model-hidden-alias-codex"
		baseModel     = "auto-model-hidden-alias-x"
		suffixedAlias = "auto-model-hidden-alias-x(HIGH)"
		upstreamModel = "auto-model-hidden-alias-upstream-y"
		olderModel    = "auto-model-hidden-alias-older"
	)
	h, ctx := newAutoModelDisabledClaudeHandler(t, "auto-model-hidden-alias-claude-disabled-key")
	var executedAuthIDs []string
	codex := &interceptorCaptureExecutor{provider: "codex", execute: func(_ context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
		executedAuthIDs = append(executedAuthIDs, auth.ID)
		return coreexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
	}}
	h.AuthManager.RegisterExecutor(codex)
	auth := &coreauth.Auth{ID: codexAuthID, Provider: "codex", Status: coreauth.StatusActive}
	coreauth.SetOAuthModelAliasesAttribute(auth, []config.OAuthModelAlias{
		{Name: upstreamModel, Alias: baseModel, Fork: true},
		{Name: baseModel, Alias: suffixedAlias, Fork: true},
	})
	const newest = int64(1) << 62
	registerAutoModelCodexAuth(t, h, auth, []*registry.ModelInfo{
		{ID: baseModel, Created: newest},
		{ID: olderModel, Created: newest - 1},
		{ID: upstreamModel, Created: newest - 2},
		{ID: suffixedAlias, Created: newest - 3},
	})
	retryAfter := time.Hour
	h.AuthManager.MarkResult(context.Background(), coreauth.Result{
		AuthID: codexAuthID, Provider: "codex", Model: upstreamModel,
		Error: &coreauth.Error{HTTPStatus: http.StatusServiceUnavailable, Message: "upstream unavailable"}, RetryAfter: &retryAfter,
	})
	reg := registry.GetGlobalRegistry()
	for _, listed := range reg.GetAvailableModels("") {
		if listed["id"] == baseModel {
			t.Fatalf("listing still shows %q; the test needs the failing %q to hide it", baseModel, upstreamModel)
		}
	}
	if reg.GetModelCount(suffixedAlias) == 0 {
		t.Fatalf("model count of %q = 0; the test needs it to carry the healthy state of %q", suffixedAlias, baseModel)
	}

	providers, model, errMsg := h.getRequestDetailsWithOptions(ctx, "auto(high)", false, modelExecutionOptions{})
	if errMsg != nil {
		t.Fatalf("auto(high) with %q hidden from the listing: %d %v", baseModel, errMsg.StatusCode, errMsg.Error)
	}
	if model != baseModel+"(high)" || !slices.Equal(providers, []string{"codex"}) {
		t.Fatalf("auto(high) resolved to %q with providers %v; want %q on codex", model, providers, baseModel+"(high)")
	}
	if _, _, errExec := h.ExecuteWithAuthManager(ctx, "openai", "auto(high)", []byte(`{"model":"auto(high)"}`), ""); errExec != nil {
		t.Fatalf("execute auto(high): %d %v, want it served by Codex", errExec.StatusCode, errExec.Error)
	}
	if !slices.Equal(executedAuthIDs, []string{codexAuthID}) {
		t.Fatalf("executed auth IDs = %v, want [%s]", executedAuthIDs, codexAuthID)
	}
}
