package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// providerDisabledCountingExecutor answers every non-streaming request of one
// provider with a fixed payload and counts the calls.
type providerDisabledCountingExecutor struct {
	provider string
	calls    int
}

func (e *providerDisabledCountingExecutor) Identifier() string { return e.provider }

func (e *providerDisabledCountingExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls++
	return coreexecutor.Response{Payload: []byte(`{"id":"resp_disabled_test","object":"response","status":"completed","output":[]}`)}, nil
}

func (e *providerDisabledCountingExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (e *providerDisabledCountingExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *providerDisabledCountingExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *providerDisabledCountingExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

// A key profile that disables Claude still serves /v1/responses with a Codex
// model and refuses the same endpoint for a Claude model.
func TestResponsesCodexServedWhenProfileDisablesClaude(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const (
		clientKey   = "responses-disabled-client-key"
		codexModel  = "responses-disabled-codex-model"
		claudeModel = "responses-disabled-claude-model"
	)
	cfg := &config.Config{SDKConfig: config.SDKConfig{APIKeys: []string{clientKey}}}
	cfg.Routing.KeyProfiles = map[string]*config.RoutingProfile{
		clientKey: {Providers: config.ProvidersRoutingConfig{Claude: config.ProviderRoutingConfig{Disabled: true}}},
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	codex := &providerDisabledCountingExecutor{provider: "codex"}
	claude := &providerDisabledCountingExecutor{provider: "claude"}
	manager.RegisterExecutor(codex)
	manager.RegisterExecutor(claude)
	for _, auth := range []struct{ id, provider, model string }{
		{"responses-disabled-codex", "codex", codexModel},
		{"responses-disabled-claude", "claude", claudeModel},
	} {
		if _, err := manager.Register(context.Background(), &coreauth.Auth{
			ID:         auth.id,
			Provider:   auth.provider,
			Status:     coreauth.StatusActive,
			Attributes: map[string]string{"auth_kind": "oauth"},
			Metadata:   map[string]any{"access_token": "test-token"},
		}); err != nil {
			t.Fatalf("register %s: %v", auth.id, err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.id, auth.provider, []*registry.ModelInfo{{ID: auth.model}})
		id := auth.id
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}

	h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("userApiKey", clientKey) })
	router.POST("/v1/responses", h.Responses)
	postStream := func(model string, stream bool) *httptest.ResponseRecorder {
		body := `{"model":"` + model + `","input":"hi","stream":false}`
		if stream {
			body = `{"model":"` + model + `","input":"hi","stream":true}`
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	post := func(model string) *httptest.ResponseRecorder { return postStream(model, false) }

	if rec := post(codexModel); rec.Code != http.StatusOK {
		t.Fatalf("codex status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if codex.calls != 1 {
		t.Fatalf("codex calls = %d, want 1", codex.calls)
	}

	rec := post(claudeModel)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("claude status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	if got := ojson.GetBytesNoCopy(rec.Body.Bytes(), "error.message").String(); got != "claude is disabled by the routing profile of this API key" {
		t.Fatalf("claude error.message = %q; body=%s", got, rec.Body.String())
	}
	// A routing-profile refusal must not read as a billing problem.
	if code, errType := ojson.GetBytesNoCopy(rec.Body.Bytes(), "error.code").String(), ojson.GetBytesNoCopy(rec.Body.Bytes(), "error.type").String(); code != "provider_disabled" || errType != "permission_error" {
		t.Fatalf("claude error.code = %q, error.type = %q; want provider_disabled, permission_error; body=%s", code, errType, rec.Body.String())
	}
	// The streaming bootstrap refusal carries the same code.
	if streamRec := postStream(claudeModel, true); streamRec.Code != http.StatusForbidden || ojson.GetBytesNoCopy(streamRec.Body.Bytes(), "error.code").String() != "provider_disabled" {
		t.Fatalf("claude stream status = %d, want 403 with error.code provider_disabled; body=%s", streamRec.Code, streamRec.Body.String())
	}
	if claude.calls != 0 {
		t.Fatalf("claude calls = %d, want 0", claude.calls)
	}
}
