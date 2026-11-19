package gemini

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

const geminiStreamDisabledMessage = "claude is disabled by the routing profile of this API key"

// geminiStreamProviderDisabledRefusal returns the refusal the Manager gives a
// Claude request of a key whose profile disables Claude.
func geminiStreamProviderDisabledRefusal(t *testing.T) error {
	t.Helper()
	const clientKey = "gemini-stream-disabled-client-key"
	cfg := &config.Config{SDKConfig: config.SDKConfig{APIKeys: []string{clientKey}}}
	cfg.Routing.KeyProfiles = map[string]*config.RoutingProfile{
		clientKey: {Providers: config.ProvidersRoutingConfig{Claude: config.ProviderRoutingConfig{Disabled: true}}},
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("userApiKey", clientKey)
	_, err := manager.ExecuteStream(context.WithValue(context.Background(), "gin", c), []string{"claude"}, coreexecutor.Request{Model: "gemini-stream-disabled-model"}, coreexecutor.Options{Stream: true})
	if !coreauth.IsProviderDisabledError(err) {
		t.Fatalf("ExecuteStream err = %v, want a provider disabled refusal", err)
	}
	return err
}

// A refusal that arrives after a pre-first-token keep-alive committed the Gemini
// or Interactions stream is written in-band; it must keep its own type and code
// instead of the 403 default (insufficient_quota).
func TestGeminiCommittedStreamRefusalKeepsProviderDisabledCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	refusal := geminiStreamProviderDisabledRefusal(t)
	for _, tc := range []struct {
		name    string
		path    string
		forward func(h *GeminiAPIHandler, c *gin.Context, errs <-chan *interfaces.ErrorMessage)
	}{
		{"generate content", "/v1beta/models/gemini-stream-disabled-model:streamGenerateContent", func(h *GeminiAPIHandler, c *gin.Context, errs <-chan *interfaces.ErrorMessage) {
			h.forwardGeminiStream(c, c.Writer.(http.Flusher), "", func(error) {}, make(chan []byte), errs)
		}},
		{"interactions", "/v1beta/interactions", func(h *GeminiAPIHandler, c *gin.Context, errs <-chan *interfaces.ErrorMessage) {
			h.forwardInteractionsStream(c, c.Writer.(http.Flusher), func(error) {}, make(chan []byte), errs)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewGeminiAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil))
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, tc.path, nil)
			errs := make(chan *interfaces.ErrorMessage, 1)
			errs <- &interfaces.ErrorMessage{StatusCode: http.StatusForbidden, Error: refusal}
			close(errs)

			tc.forward(h, c, errs)

			body := recorder.Body.String()
			data := ""
			for _, line := range strings.Split(body, "\n") {
				if payload, ok := strings.CutPrefix(line, "data: "); ok {
					data = payload
				}
			}
			if !strings.HasPrefix(body, "event: error\n") || data == "" {
				t.Fatalf("body = %q, want an SSE error event", body)
			}
			errNode := ojson.Get(data, "error")
			if code, errType, message := errNode.Get("code").String(), errNode.Get("type").String(), errNode.Get("message").String(); code != "provider_disabled" || errType != "permission_error" || message != geminiStreamDisabledMessage {
				t.Fatalf("error code = %q, type = %q, message = %q; want provider_disabled, permission_error, %q; body=%q", code, errType, message, geminiStreamDisabledMessage, body)
			}
		})
	}
}
