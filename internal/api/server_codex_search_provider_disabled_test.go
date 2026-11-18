package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// TestCodexAlphaSearchRefusesDisabledCodex checks that /v1/alpha/search, which
// selects its auth directly instead of through Manager.Execute, answers 403 for a
// key whose routing profile disables Codex and never reaches upstream, while a key
// without that profile is still served.
func TestCodexAlphaSearchRefusesDisabledCodex(t *testing.T) {
	const disabledKey, enabledKey = "codex-off-key", "codex-on-key"
	cfg := &proxyconfig.Config{SDKConfig: sdkconfig.SDKConfig{APIKeys: []string{disabledKey, enabledKey}}}
	cfg.Routing.KeyProfiles = map[string]*proxyconfig.RoutingProfile{
		disabledKey: {Providers: proxyconfig.ProvidersRoutingConfig{Codex: proxyconfig.ProviderRoutingConfig{Disabled: true}}},
	}
	server := newTestServerWithConfig(t, cfg)
	server.handlers.AuthManager.SetConfig(cfg)
	server.handlers.AuthManager.RegisterExecutor(executor.NewCodexAutoExecutor(server.getConfig()))
	credential := &auth.Auth{
		ID:       "codex-search-provider-disabled",
		Provider: "codex",
		Status:   auth.StatusActive,
		Metadata: map[string]any{"type": "codex", "access_token": "oauth-token", "account_id": "account"},
	}
	registry.GetGlobalRegistry().RegisterClient(credential.ID, "codex", []*registry.ModelInfo{{ID: "gpt-6.1-sol"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(credential.ID) })
	if _, errRegister := server.handlers.AuthManager.Register(t.Context(), credential); errRegister != nil {
		t.Fatalf("register Codex credential: %v", errRegister)
	}

	upstreamCalls := 0
	ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", codexSearchSIWCTransport(func(req *http.Request) (*http.Response, error) {
		upstreamCalls++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"results":[]}`)),
			Request:    req,
		}, nil
	}))
	search := func(key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/alpha/search", strings.NewReader(`{"id":"disabled-search","model":"gpt-6.1-sol","commands":{"search_query":[{"q":"OpenAI"}]}}`)).WithContext(ctx)
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		server.engine.ServeHTTP(recorder, req)
		return recorder
	}

	refused := search(disabledKey)
	if refused.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", refused.Code, refused.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	if errUnmarshal := json.Unmarshal(refused.Body.Bytes(), &body); errUnmarshal != nil {
		t.Fatalf("decode body %s: %v", refused.Body.String(), errUnmarshal)
	}
	if body.Error != "codex is disabled by the routing profile of this API key" {
		t.Fatalf("error = %q", body.Error)
	}
	if upstreamCalls != 0 {
		t.Fatalf("upstream calls = %d after a refusal, want 0", upstreamCalls)
	}

	if served := search(enabledKey); served.Code != http.StatusOK {
		t.Fatalf("enabled key status = %d, want 200; body=%s", served.Code, served.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("upstream calls = %d, want 1", upstreamCalls)
	}
}
