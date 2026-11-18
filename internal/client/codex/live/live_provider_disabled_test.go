package live

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// TestHandlerRefusesDisabledCodex checks that live and realtime calls, which
// select their auth directly, answer 403 for a key whose routing profile disables
// Codex and never reach upstream, while a key without that profile is served.
func TestHandlerRefusesDisabledCodex(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const disabledKey, enabledKey = "live-codex-off", "live-codex-on"
	const message = "codex is disabled by the routing profile of this API key"

	for _, path := range []string{"/v1/live", "/v1/realtime/calls"} {
		t.Run(path, func(t *testing.T) {
			manager := auth.NewManager(nil, nil, nil)
			cfg := &config.Config{SDKConfig: config.SDKConfig{APIKeys: []string{disabledKey, enabledKey}}}
			cfg.Routing.KeyProfiles = map[string]*config.RoutingProfile{
				disabledKey: {Providers: config.ProvidersRoutingConfig{Codex: config.ProviderRoutingConfig{Disabled: true}}},
			}
			manager.SetConfig(cfg)
			executor := &captureExecutor{responseBody: &trackedResponseBody{Reader: strings.NewReader("v=0\r\n")}}
			manager.RegisterExecutor(executor)
			registerCredential(t, manager, &auth.Auth{
				ID:       "codex-oauth-disabled-profile",
				Provider: "codex",
				Status:   auth.StatusActive,
				Metadata: map[string]any{"access_token": "oauth-token", "account_id": "account-123"},
			})

			handler := NewHandler(manager, nil)
			router := gin.New()
			router.POST(path, func(c *gin.Context) {
				// The access middleware stores the authenticated client key here.
				c.Set("userApiKey", strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
			}, handler.Handle)
			call := func(key string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"gpt-live-1-codex"}`))
				req.Header.Set("Authorization", "Bearer "+key)
				req.Header.Set("Content-Type", "application/json")
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, req)
				return recorder
			}

			refused := call(disabledKey)
			if refused.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body=%s", refused.Code, refused.Body.String())
			}
			var got string
			if strings.HasPrefix(path, "/v1/realtime") {
				var body struct {
					Error struct {
						Message string `json:"message"`
					} `json:"error"`
				}
				if errUnmarshal := json.Unmarshal(refused.Body.Bytes(), &body); errUnmarshal != nil {
					t.Fatalf("decode body %s: %v", refused.Body.String(), errUnmarshal)
				}
				got = body.Error.Message
			} else {
				var body struct {
					Error string `json:"error"`
				}
				if errUnmarshal := json.Unmarshal(refused.Body.Bytes(), &body); errUnmarshal != nil {
					t.Fatalf("decode body %s: %v", refused.Body.String(), errUnmarshal)
				}
				got = body.Error
			}
			if got != message {
				t.Fatalf("error message = %q, want %q", got, message)
			}
			if calls := executor.httpCalls.Load(); calls != 0 {
				t.Fatalf("upstream calls = %d after a refusal, want 0", calls)
			}

			if served := call(enabledKey); served.Code != http.StatusCreated {
				t.Fatalf("enabled key status = %d, want 201; body=%s", served.Code, served.Body.String())
			}
			if calls := executor.httpCalls.Load(); calls != 1 {
				t.Fatalf("upstream calls = %d, want 1", calls)
			}
		})
	}
}
