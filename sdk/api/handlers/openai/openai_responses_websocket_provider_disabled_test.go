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
