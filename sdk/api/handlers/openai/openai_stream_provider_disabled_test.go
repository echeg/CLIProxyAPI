package openai

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

const streamDisabledMessage = "claude is disabled by the routing profile of this API key"

// streamProviderDisabledRefusal returns the refusal the Manager gives a Claude
// request of a key whose profile disables Claude.
func streamProviderDisabledRefusal(t *testing.T) error {
	t.Helper()
	const clientKey = "stream-disabled-client-key"
	cfg := &config.Config{SDKConfig: config.SDKConfig{APIKeys: []string{clientKey}}}
	cfg.Routing.KeyProfiles = map[string]*config.RoutingProfile{
		clientKey: {Providers: config.ProvidersRoutingConfig{Claude: config.ProviderRoutingConfig{Disabled: true}}},
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("userApiKey", clientKey)
	_, err := manager.ExecuteStream(context.WithValue(context.Background(), "gin", c), []string{"claude"}, coreexecutor.Request{Model: "stream-disabled-model"}, coreexecutor.Options{Stream: true}) //nolint:staticcheck // "gin" is the shared Gin context key contract.
	if !coreauth.IsProviderDisabledError(err) {
		t.Fatalf("ExecuteStream err = %v, want a provider disabled refusal", err)
	}
	return err
}

func pendingStreamRefusal(errMsg *interfaces.ErrorMessage) <-chan *interfaces.ErrorMessage {
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- errMsg
	close(errs)
	return errs
}

// assertStreamRefusalError checks the error object of an in-band stream error.
func assertStreamRefusalError(t *testing.T, errNode ojson.Result, body string) {
	t.Helper()
	if code, errType, message := errNode.Get("code").String(), errNode.Get("type").String(), errNode.Get("message").String(); code != "provider_disabled" || errType != "permission_error" || message != streamDisabledMessage {
		t.Fatalf("error code = %q, type = %q, message = %q; want provider_disabled, permission_error, %q; body=%q", code, errType, message, streamDisabledMessage, body)
	}
}

// streamDataPayload returns the data of the only SSE data line in body.
func streamDataPayload(t *testing.T, body string) string {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			return data
		}
	}
	t.Fatalf("no data line in %q", body)
	return ""
}

// A refusal that arrives after a pre-first-token keep-alive committed the SSE
// stream is written in-band; it must keep its own code instead of the 403
// default (insufficient_quota).
func TestResponsesCommittedStreamRefusalKeepsProviderDisabledCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	refusal := streamProviderDisabledRefusal(t)
	for _, tc := range []struct {
		name      string
		userAgent string
		errorPath string
	}{
		{"error event", "", "error"},
		{"codex response.failed", "Codex Desktop/26.803.41515", "response.error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil))
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			if tc.userAgent != "" {
				c.Request.Header.Set("User-Agent", tc.userAgent)
			}
			// The handler sanitizes the refusal before forwarding a committed stream.
			errMsg := sanitizeResponsesStreamErrorMessage(&interfaces.ErrorMessage{StatusCode: http.StatusForbidden, Error: refusal})
			h.forwardResponsesStream(c, c.Writer.(http.Flusher), func(error) {}, make(chan []byte), pendingStreamRefusal(errMsg), nil)
			body := recorder.Body.String()
			assertStreamRefusalError(t, ojson.Get(streamDataPayload(t, body), tc.errorPath), body)
		})
	}
}

func TestChatCompletionsCommittedStreamRefusalKeepsProviderDisabledCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	refusal := streamProviderDisabledRefusal(t)
	h := NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil))
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	h.handleStreamResult(c, c.Writer.(http.Flusher), func(error) {}, make(chan []byte), pendingStreamRefusal(&interfaces.ErrorMessage{StatusCode: http.StatusForbidden, Error: refusal}), false)
	body := recorder.Body.String()
	assertStreamRefusalError(t, ojson.Get(streamDataPayload(t, body), "error"), body)
}

func TestImagesCommittedStreamRefusalKeepsProviderDisabledCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	refusal := streamProviderDisabledRefusal(t)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	writeImagesStreamErrorEvent(c, &interfaces.ErrorMessage{StatusCode: http.StatusForbidden, Error: refusal})
	body := recorder.Body.String()
	assertStreamRefusalError(t, ojson.Get(streamDataPayload(t, body), "error"), body)
}
