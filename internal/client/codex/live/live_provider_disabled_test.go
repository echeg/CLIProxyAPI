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
						Type    string `json:"type"`
						Code    string `json:"code"`
					} `json:"error"`
				}
				if errUnmarshal := json.Unmarshal(refused.Body.Bytes(), &body); errUnmarshal != nil {
					t.Fatalf("decode body %s: %v", refused.Body.String(), errUnmarshal)
				}
				// The OpenAI-format body carries the refusal's own type and code.
				if body.Error.Type != "permission_error" || body.Error.Code != "provider_disabled" {
					t.Fatalf("error type = %q, code = %q; want permission_error, provider_disabled; body=%s", body.Error.Type, body.Error.Code, refused.Body.String())
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

// TestRealtimeEntryPointsRefuseDisabledCodex checks the realtime entry points
// that select before the websocket upgrade: a new direct realtime websocket and
// the sideband of a call opened before Codex was disabled. A disable applies to
// new connections, so both are refused with the OpenAI-format refusal; only the
// hangup of an open call is exempt.
func TestRealtimeEntryPointsRefuseDisabledCodex(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const ownerKey = "realtime-codex-off"
	const message = "codex is disabled by the routing profile of this API key"
	for _, tc := range []struct {
		name  string
		route string
		path  string
		mount func(handler *Handler) gin.HandlerFunc
	}{
		{"direct websocket", "/v1/realtime", "/v1/realtime?model=gpt-realtime", func(handler *Handler) gin.HandlerFunc { return handler.HandleDirectWebsocket }},
		{"sideband of an open call", "/v1/realtime/calls/:call_id", "/v1/realtime/calls/call-open", func(handler *Handler) gin.HandlerFunc {
			handler.sessions.put("call-open", liveSession{
				authID:         "codex-oauth-realtime-disabled",
				model:          defaultLiveModel,
				ownerPrincipal: ownerKey,
				ownerProvider:  "static",
			})
			return handler.HandleSideband
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := auth.NewManager(nil, nil, nil)
			cfg := &config.Config{SDKConfig: config.SDKConfig{APIKeys: []string{ownerKey}}}
			cfg.Routing.KeyProfiles = map[string]*config.RoutingProfile{
				ownerKey: {Providers: config.ProvidersRoutingConfig{Codex: config.ProviderRoutingConfig{Disabled: true}}},
			}
			manager.SetConfig(cfg)
			executor := &captureExecutor{}
			manager.RegisterExecutor(executor)
			registerCredential(t, manager, &auth.Auth{
				ID:       "codex-oauth-realtime-disabled",
				Provider: "codex",
				Status:   auth.StatusActive,
				Metadata: map[string]any{"access_token": "oauth-token", "account_id": "account-123"},
			})

			handler := NewHandler(manager, nil)
			router := gin.New()
			router.GET(tc.route, func(c *gin.Context) {
				// The access middleware stores the authenticated client key here.
				c.Set("userApiKey", ownerKey)
				c.Set("accessProvider", "static")
				c.Next()
			}, tc.mount(handler))
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("Connection", "Upgrade")
			req.Header.Set("Upgrade", "websocket")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)

			if recorder.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body=%s", recorder.Code, recorder.Body.String())
			}
			var body struct {
				Error struct {
					Message string `json:"message"`
					Type    string `json:"type"`
					Code    string `json:"code"`
				} `json:"error"`
			}
			if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &body); errUnmarshal != nil {
				t.Fatalf("decode body %s: %v", recorder.Body.String(), errUnmarshal)
			}
			if body.Error.Message != message || body.Error.Type != "permission_error" || body.Error.Code != "provider_disabled" {
				t.Fatalf("error = %+v; want %q, permission_error, provider_disabled", body.Error, message)
			}
			if calls := executor.httpCalls.Load(); calls != 0 {
				t.Fatalf("upstream calls = %d after a refusal, want 0", calls)
			}
		})
	}
}
