package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const wsDisabledClientKey = "ws-disabled-client-key"

// newDisabledCodexPinFixture serves one Codex subscription on an upstream
// websocket to a client key that has its own key profile. The router stores the
// key on the gin context as the access middleware does.
func newDisabledCodexPinFixture(t *testing.T) *policyPinFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	name := strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	f := &policyPinFixture{
		t:        t,
		cfg:      &config.Config{SDKConfig: config.SDKConfig{APIKeys: []string{wsDisabledClientKey}}},
		executor: &websocketPolicyPinExecutor{provider: "codex"},
		model:    "ws-disabled-model-" + name,
		subID:    "ws-disabled-sub-" + name,
	}
	f.cfg.Routing.KeyProfiles = map[string]*config.RoutingProfile{wsDisabledClientKey: {}}
	f.manager = coreauth.NewManager(nil, nil, nil)
	f.manager.SetConfig(f.cfg)
	f.manager.RegisterExecutor(f.executor)
	subscription := &coreauth.Auth{
		ID:         f.subID,
		Provider:   "codex",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
		Metadata:   map[string]any{"access_token": "test-token"},
	}
	if _, err := f.manager.Register(context.Background(), subscription); err != nil {
		t.Fatalf("register %s: %v", subscription.ID, err)
	}
	registry.GetGlobalRegistry().RegisterClient(f.subID, "codex", []*registry.ModelInfo{{ID: f.model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(f.subID) })

	h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&f.cfg.SDKConfig, f.manager))
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("userApiKey", wsDisabledClientKey) })
	router.GET("/v1/responses", h.ResponsesWebsocket)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	f.wsURL = "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
	return f
}

// disableCodexForKey reloads the config with Codex disabled in the key profile.
func (f *policyPinFixture) disableCodexForKey() {
	f.t.Helper()
	reloaded := *f.cfg
	reloaded.Routing.KeyProfiles = map[string]*config.RoutingProfile{
		wsDisabledClientKey: {Providers: config.ProvidersRoutingConfig{Codex: config.ProviderRoutingConfig{Disabled: true}}},
	}
	f.manager.SetConfig(&reloaded)
}

// expectDisabledError writes request and expects the 403 refusal event.
func expectDisabledError(t *testing.T, conn *websocket.Conn, request string) {
	t.Helper()
	if err := conn.WriteMessage(websocket.TextMessage, []byte(request)); err != nil {
		t.Fatalf("write request: %v", err)
	}
	_, payload, err := conn.ReadMessage()
	if err != nil || ojson.GetBytesNoCopy(payload, "type").String() != wsEventTypeError {
		t.Fatalf("response = %s, err=%v, want %s", payload, err, wsEventTypeError)
	}
	if got := ojson.GetBytesNoCopy(payload, "status").Int(); got != http.StatusForbidden {
		t.Fatalf("error status = %d, want 403: %s", got, payload)
	}
	if got := ojson.GetBytesNoCopy(payload, "error.message").String(); got != "codex is disabled by the routing profile of this API key" {
		t.Fatalf("error message = %q: %s", got, payload)
	}
	if got := ojson.GetBytesNoCopy(payload, "error.code").String(); got != "provider_disabled" {
		t.Fatalf("error code = %q, want provider_disabled: %s", got, payload)
	}
}

func TestResponsesWebsocketPinnedTurnRefusedWhenCodexDisabled(t *testing.T) {
	for _, incremental := range []bool{false, true} {
		name := "full create"
		if incremental {
			name = "incremental"
		}
		t.Run(name, func(t *testing.T) {
			f := newDisabledCodexPinFixture(t)
			conn := f.dial()
			responseID := f.pinSubscription(conn)

			f.disableCodexForKey()
			request := f.fullCreate("msg-2")
			if incremental {
				request = policyPinIncremental(responseID, "msg-2")
			}
			expectDisabledError(t, conn, request)
			if got := f.executor.turns(f.subID); got != 1 {
				t.Fatalf("subscription turns = %d, want 1 (refused turn reached upstream); auth IDs %v", got, f.executor.AuthIDs())
			}
		})
	}
}

// Execution resolves "auto" with the key's routing profile, so the pin matcher
// must too: when the global "auto" is a Claude model but the key disables Claude,
// turn 1 runs on the Codex websocket and an incremental turn 2 keeps that pin
// instead of dropping it and closing the connection for replay.
func TestResponsesWebsocketAutoModelKeepsCodexPinWhenProfileDisablesClaude(t *testing.T) {
	f := newDisabledCodexPinFixture(t)
	const newest = int64(1) << 62
	claudeClientID := "ws-auto-claude-" + f.subID
	claudeModel := "ws-auto-claude-model-" + f.subID
	registry.GetGlobalRegistry().RegisterClient(claudeClientID, "claude", []*registry.ModelInfo{{ID: claudeModel, Created: newest}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(claudeClientID) })
	registry.GetGlobalRegistry().RegisterClient(f.subID, "codex", []*registry.ModelInfo{{ID: f.model, Created: newest - 1}})

	reloaded := *f.cfg
	reloaded.Routing.KeyProfiles = map[string]*config.RoutingProfile{
		wsDisabledClientKey: {Providers: config.ProvidersRoutingConfig{Claude: config.ProviderRoutingConfig{Disabled: true}}},
	}
	f.manager.SetConfig(&reloaded)
	if got := util.ResolveAutoModel("auto"); got != claudeModel {
		t.Fatalf("global auto = %q, want %q", got, claudeModel)
	}

	f.model = "auto"
	conn := f.dial()
	responseID := f.pinSubscription(conn)
	policyPinSendCompleted(t, conn, policyPinIncremental(responseID, "msg-2"))
	if got := f.executor.turns(f.subID); got != 2 {
		t.Fatalf("subscription turns = %d, want 2; auth IDs %v", got, f.executor.AuthIDs())
	}
}

// A disabled provider never serves the turn, so its auths must not decide the
// replay shape: on a model shared by a disabled Claude and an enabled HTTP Codex,
// compaction replay follows Codex alone, and a key without the profile still sees
// the mixed backends.
func TestResponsesWebsocketCompactionReplayIgnoresDisabledProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const (
		disabledKey = "ws-compaction-claude-disabled-key"
		plainKey    = "ws-compaction-plain-key"
		model       = "ws-compaction-shared-model"
	)
	cfg := &config.Config{SDKConfig: config.SDKConfig{APIKeys: []string{disabledKey, plainKey}}}
	cfg.Routing.KeyProfiles = map[string]*config.RoutingProfile{
		disabledKey: {Providers: config.ProvidersRoutingConfig{Claude: config.ProviderRoutingConfig{Disabled: true}}},
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	for _, auth := range []*coreauth.Auth{
		{ID: "ws-compaction-codex", Provider: "codex", Status: coreauth.StatusActive},
		{ID: "ws-compaction-claude", Provider: "claude", Status: coreauth.StatusActive},
	} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register %s: %v", auth.ID, err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
		id := auth.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
	ctxWithKey := func(key string) context.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Set("userApiKey", key)
		return context.WithValue(context.Background(), "gin", c)
	}

	if !h.websocketUpstreamSupportsCompactionReplayForModel(ctxWithKey(disabledKey), model) {
		t.Fatal("Claude-disabled key: compaction replay = false, want true (only Codex can serve the turn)")
	}
	if h.websocketUpstreamSupportsCompactionReplayForModel(ctxWithKey(plainKey), model) {
		t.Fatal("plain key: compaction replay = true, want false (mixed Claude and Codex backends)")
	}
}
