package live

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestHandleHangupForwardsPinnedOAuthCall(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := auth.NewManager(nil, nil, nil)
	executor := &captureExecutor{
		statusCode:   http.StatusOK,
		responseBody: io.NopCloser(strings.NewReader(`{"status":"ok"}`)),
	}
	manager.RegisterExecutor(executor)
	registerCredential(t, manager, &auth.Auth{
		ID:       "codex-oauth",
		Provider: "codex",
		Status:   auth.StatusActive,
		Metadata: map[string]any{"access_token": "oauth-token"},
	})
	handler := NewHandler(manager, nil)
	handler.sessions.put("call-123", liveSession{
		authID:         "codex-oauth",
		model:          defaultLiveModel,
		ownerPrincipal: "owner-key",
		ownerProvider:  "static",
	})

	router := gin.New()
	router.POST("/v1/realtime/calls/:call_id/hangup", func(c *gin.Context) {
		c.Set("userApiKey", "owner-key")
		c.Set("accessProvider", "static")
		c.Next()
	}, handler.HandleHangup)
	request := httptest.NewRequest(http.MethodPost, "/v1/realtime/calls/call-123/hangup", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if executor.request == nil || executor.request.URL.String() != "https://api.openai.com/v1/realtime/calls/call-123/hangup" {
		t.Fatalf("upstream request = %#v", executor.request)
	}
	if _, ok := handler.sessions.peek("call-123"); ok {
		t.Fatal("successful hangup retained session")
	}
}

// A disable applies to new calls only, so the hangup of a call opened before
// Codex was disabled still reaches upstream and ends the session without being
// logged as a refusal. The exception skips only the disabled switch: a pinned ID
// that now names another provider's credential or a Codex API key is never sent.
func TestHandleHangupForwardsOpenCallAfterCodexDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const ownerKey = "owner-key"
	for _, tc := range []struct {
		name    string
		pinned  *auth.Auth
		forward bool
	}{
		{"open codex oauth call", &auth.Auth{ID: "pinned-auth", Provider: "codex", Status: auth.StatusActive, Metadata: map[string]any{"access_token": "oauth-token"}}, true},
		{"id now names a claude credential", &auth.Auth{ID: "pinned-auth", Provider: "claude", Status: auth.StatusActive, Metadata: map[string]any{"access_token": "claude-token"}}, false},
		{"id now names a codex api key", &auth.Auth{ID: "pinned-auth", Provider: "codex", Status: auth.StatusActive, Attributes: map[string]string{"api_key": "sk-codex"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := auth.NewManager(nil, nil, nil)
			cfg := &config.Config{SDKConfig: config.SDKConfig{APIKeys: []string{ownerKey}}}
			cfg.Routing.KeyProfiles = map[string]*config.RoutingProfile{
				ownerKey: {Providers: config.ProvidersRoutingConfig{Codex: config.ProviderRoutingConfig{Disabled: true}}},
			}
			manager.SetConfig(cfg)
			executor := &captureExecutor{
				statusCode:   http.StatusOK,
				responseBody: io.NopCloser(strings.NewReader(`{"status":"ok"}`)),
			}
			manager.RegisterExecutor(executor)
			claudeExecutor := &claudeCaptureExecutor{captureExecutor: &captureExecutor{
				statusCode:   http.StatusOK,
				responseBody: io.NopCloser(strings.NewReader(`{"status":"ok"}`)),
			}}
			manager.RegisterExecutor(claudeExecutor)
			registerCredential(t, manager, tc.pinned)
			handler := NewHandler(manager, nil)
			handler.sessions.put("call-123", liveSession{
				authID:         tc.pinned.ID,
				model:          defaultLiveModel,
				ownerPrincipal: ownerKey,
				ownerProvider:  "static",
			})
			logger := log.StandardLogger()
			savedHooks := make(log.LevelHooks)
			for level, hooks := range logger.Hooks {
				savedHooks[level] = append([]log.Hook(nil), hooks...)
			}
			hook := logtest.NewLocal(logger)
			t.Cleanup(func() { logger.ReplaceHooks(savedHooks) })

			router := gin.New()
			router.POST("/v1/realtime/calls/:call_id/hangup", func(c *gin.Context) {
				c.Set("userApiKey", ownerKey)
				c.Set("accessProvider", "static")
				c.Next()
			}, handler.HandleHangup)
			request := httptest.NewRequest(http.MethodPost, "/v1/realtime/calls/call-123/hangup", nil)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			for _, entry := range hook.AllEntries() {
				if strings.Contains(entry.Message, "provider disabled") {
					t.Fatalf("hangup logged a refusal: %q", entry.Message)
				}
			}
			_, retained := handler.sessions.peek("call-123")
			if !tc.forward {
				if recorder.Code == http.StatusOK || executor.request != nil || claudeExecutor.request != nil || !retained {
					t.Fatalf("status = %d, codex request = %v, claude request = %v, session retained = %t; want no upstream hangup", recorder.Code, executor.request, claudeExecutor.request, retained)
				}
				return
			}
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
			}
			if executor.request == nil || executor.request.URL.String() != "https://api.openai.com/v1/realtime/calls/call-123/hangup" {
				t.Fatalf("upstream request = %#v", executor.request)
			}
			if retained {
				t.Fatal("successful hangup retained session")
			}
		})
	}
}

// claudeCaptureExecutor records requests sent with a Claude credential.
type claudeCaptureExecutor struct{ *captureExecutor }

func (*claudeCaptureExecutor) Identifier() string { return "claude" }

func TestHandleHangupForwardsUnauthorizedHomeResponseWithoutRefresh(t *testing.T) {
	const upstreamError = `{"error":{"message":"access token expired"}}`
	gin.SetMode(gin.TestMode)
	manager := auth.NewManager(nil, nil, nil)
	manager.SetConfig(&config.Config{Home: config.HomeConfig{Enabled: true}})
	manager.PublishHomeDispatch(&homeDispatcher{}, executionregistry.New(), 1)
	executor := &captureExecutor{
		statusCode:   http.StatusUnauthorized,
		responseBody: io.NopCloser(strings.NewReader(upstreamError)),
	}
	manager.RegisterExecutor(executor)
	handler := NewHandler(manager, nil)
	handler.sessions.put("call-123", liveSession{authID: "home-codex-live", model: defaultLiveModel})

	router := gin.New()
	router.POST("/v1/realtime/calls/:call_id/hangup", handler.HandleHangup)
	request := httptest.NewRequest(http.MethodPost, "/v1/realtime/calls/call-123/hangup", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized || recorder.Body.String() != upstreamError {
		t.Fatalf("response = status %d body %q, want original upstream 401", recorder.Code, recorder.Body.String())
	}
	if executor.refreshCalls.Load() != 0 || executor.httpCalls.Load() != 1 {
		t.Fatalf("refresh/http calls = %d/%d, want 0/1", executor.refreshCalls.Load(), executor.httpCalls.Load())
	}
	if got := executor.request.Header.Get("Authorization"); got != "Bearer home-live-token" {
		t.Fatalf("Authorization = %q, want original Home token", got)
	}
}

func TestHandleHangupReportsUnauthorizedWhenResponseReadFails(t *testing.T) {
	const upstreamError = `{"error":{"message":"access token expired"}}`
	gin.SetMode(gin.TestMode)
	runtimeConfig := &config.Config{
		Home:      config.HomeConfig{Enabled: true},
		SDKConfig: config.SDKConfig{RequestLog: true},
	}
	manager := auth.NewManager(nil, nil, nil)
	manager.SetConfig(runtimeConfig)
	manager.PublishHomeDispatch(&homeDispatcher{}, executionregistry.New(), 1)
	executor := &captureExecutor{
		statusCode:   http.StatusUnauthorized,
		responseBody: &errorResponseBody{payload: []byte(upstreamError)},
	}
	manager.RegisterExecutor(executor)
	usageCapture := registerHomeUnauthorizedUsageCapture(t, t.Name(), "home-codex-live")
	handler := NewHandler(manager, runtimeConfig)
	handler.sessions.put("call-123", liveSession{authID: "home-codex-live", model: defaultLiveModel})

	router := gin.New()
	var apiResponse []byte
	router.Use(func(c *gin.Context) {
		c.Next()
		if raw, exists := c.Get("API_RESPONSE"); exists {
			apiResponse, _ = raw.([]byte)
		}
	})
	router.POST("/v1/realtime/calls/:call_id/hangup", handler.HandleHangup)
	request := httptest.NewRequest(http.MethodPost, "/v1/realtime/calls/call-123/hangup", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadGateway, recorder.Body.String())
	}
	record := usageCapture.wait(t)
	if record.Fail.StatusCode != http.StatusUnauthorized || record.Fail.Body != upstreamError {
		t.Fatalf("Home unauthorized failure = status %d body %q, want status 401 body %q", record.Fail.StatusCode, record.Fail.Body, upstreamError)
	}
	if !strings.Contains(string(apiResponse), upstreamError) || strings.Contains(string(apiResponse), io.ErrUnexpectedEOF.Error()) {
		t.Fatalf("API_RESPONSE = %q, want upstream body without local read error", apiResponse)
	}
	if executor.refreshCalls.Load() != 0 || executor.httpCalls.Load() != 1 {
		t.Fatalf("refresh/http calls = %d/%d, want 0/1", executor.refreshCalls.Load(), executor.httpCalls.Load())
	}
}

func TestHandleHangupRejectsDifferentAPIPrincipal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewHandler(auth.NewManager(nil, nil, nil), nil)
	handler.sessions.put("call-123", liveSession{
		authID:         "codex-oauth",
		model:          defaultLiveModel,
		ownerPrincipal: "owner-key",
		ownerProvider:  "static",
	})
	router := gin.New()
	router.POST("/v1/realtime/calls/:call_id/hangup", func(c *gin.Context) {
		c.Set("userApiKey", "other-key")
		c.Set("accessProvider", "static")
		c.Next()
	}, handler.HandleHangup)
	request := httptest.NewRequest(http.MethodPost, "/v1/realtime/calls/call-123/hangup", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusForbidden, recorder.Body.String())
	}
}

func TestUnsupportedRealtimeCapabilitiesUseStandardError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewHandler(nil, nil)
	router := gin.New()
	router.POST("/v1/realtime/transcription_sessions", handler.HandleTranscriptionSession)
	router.POST("/v1/realtime/calls/:call_id/accept", handler.HandleSIPControl)

	for _, path := range []string{"/v1/realtime/transcription_sessions", "/v1/realtime/calls/call-123/accept"} {
		request := httptest.NewRequest(http.MethodPost, path, nil)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNotImplemented {
			t.Errorf("%s status = %d, want %d", path, recorder.Code, http.StatusNotImplemented)
		}
		if !strings.Contains(recorder.Body.String(), `"type":"not_supported_error"`) || !strings.Contains(recorder.Body.String(), `"code":"realtime_capability_not_supported"`) {
			t.Errorf("%s body = %s", path, recorder.Body.String())
		}
	}
}
