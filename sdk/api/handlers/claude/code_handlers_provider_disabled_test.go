package claude

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// A key profile that disables Claude refuses /v1/messages (non-streaming and
// streaming) and count-tokens with 403 and the exact client-facing message,
// without reaching the executor.
func TestClaudeMessagesRefusedWhenProfileDisablesClaude(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const (
		model     = "provider-disabled-claude-model"
		clientKey = "sk-provider-disabled-client"
		message   = "claude is disabled by the routing profile of this API key"
	)

	for _, tc := range []struct {
		name      string
		path      string
		streaming bool
	}{
		{name: "messages_non_stream", path: "/v1/messages"},
		{name: "messages_stream", path: "/v1/messages", streaming: true},
		{name: "count_tokens", path: "/v1/messages/count_tokens"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstreamCalls := 0
			manager := coreauth.NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{
				SDKConfig: internalconfig.SDKConfig{APIKeys: []string{clientKey}},
				Routing: internalconfig.RoutingConfig{KeyProfiles: map[string]*internalconfig.RoutingProfile{
					clientKey: {Providers: internalconfig.ProvidersRoutingConfig{Claude: internalconfig.ProviderRoutingConfig{Disabled: true}}},
				}},
			})
			manager.RegisterExecutor(&issue7521ClaudeExecutor{
				executeFn: func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
					upstreamCalls++
					return coreexecutor.Response{Payload: []byte(`{}`)}, nil
				},
				streamFn: func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
					upstreamCalls++
					chunks := make(chan coreexecutor.StreamChunk)
					close(chunks)
					return &coreexecutor.StreamResult{Chunks: chunks}, nil
				},
				countFn: func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
					upstreamCalls++
					return coreexecutor.Response{Payload: []byte(`{"input_tokens":1}`)}, nil
				},
			})
			authID := "provider-disabled-" + tc.name
			if _, errReg := manager.Register(context.Background(), &coreauth.Auth{
				ID:         authID,
				Provider:   "claude",
				Status:     coreauth.StatusActive,
				Attributes: map[string]string{"auth_kind": "oauth"},
			}); errReg != nil {
				t.Fatalf("register auth: %v", errReg)
			}
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { reg.UnregisterClient(authID) })

			handler := NewClaudeCodeAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
			router := gin.New()
			router.Use(func(c *gin.Context) { c.Set("userApiKey", clientKey) })
			router.POST("/v1/messages", handler.ClaudeMessages)
			router.POST("/v1/messages/count_tokens", handler.ClaudeCountTokens)

			body := `{"model":"` + model + `","messages":[{"role":"user","content":"hi"}],"stream":` + strconv.FormatBool(tc.streaming) + `}`
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
			}
			if got := ojson.GetBytesNoCopy(rec.Body.Bytes(), "error.message").String(); got != message {
				t.Fatalf("error.message = %q, want %q; body=%s", got, message, rec.Body.String())
			}
			if upstreamCalls != 0 {
				t.Fatalf("upstream calls = %d, want 0", upstreamCalls)
			}
		})
	}
}
