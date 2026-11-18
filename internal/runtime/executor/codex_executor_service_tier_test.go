package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// Fixture catalog models: the tests must not depend on which tiers the embedded
// Codex catalog gives to a real model.
const (
	tierFixtureUltrafastModel = "gpt-fixture-ultrafast" // advertises ultrafast and priority
	tierFixturePriorityModel  = "gpt-fixture-priority"  // advertises priority only
	tierFixtureStandardModel  = "gpt-fixture-standard"  // advertises no tier
)

// useCodexServiceTierFixture installs the fixture catalog lookup for one test.
// Tests that call it must not run in parallel.
func useCodexServiceTierFixture(t *testing.T) {
	t.Helper()
	previous := codexModelSupportsServiceTier
	codexModelSupportsServiceTier = func(slug, tier string) bool {
		switch slug {
		case tierFixtureUltrafastModel:
			return tier == "ultrafast" || tier == "priority"
		case tierFixturePriorityModel:
			return tier == "priority"
		}
		return false
	}
	t.Cleanup(func() { codexModelSupportsServiceTier = previous })
}

func codexSpeedTestConfig(speed string, ignoreClientTier bool) *config.Config {
	cfg := &config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}}
	cfg.Routing.Providers.Codex.Speed = speed
	cfg.Routing.Providers.Codex.IgnoreClientTier = ignoreClientTier
	return cfg
}

func TestApplyCodexServiceTierDoc(t *testing.T) {
	useCodexServiceTierFixture(t)

	// Tier chosen from the profile speed for each fixture model.
	speedTier := map[string]map[string]string{
		"":                         {},
		config.CodexSpeedStandard:  {},
		config.CodexSpeedFast:      {tierFixtureUltrafastModel: "priority", tierFixturePriorityModel: "priority"},
		config.CodexSpeedUltrafast: {tierFixtureUltrafastModel: "ultrafast", tierFixturePriorityModel: "priority"},
	}
	models := []string{tierFixtureUltrafastModel, tierFixturePriorityModel, tierFixtureStandardModel}
	clientTiers := []string{"", "priority", "ultrafast"}

	for speed, tiers := range speedTier {
		for _, model := range models {
			for _, clientTier := range clientTiers {
				for _, ignoreClientTier := range []bool{false, true} {
					for _, apiKey := range []bool{false, true} {
						name := "speed=" + speed + "/model=" + model + "/client=" + clientTier
						if ignoreClientTier {
							name += "/ignore"
						}
						if apiKey {
							name += "/api-key"
						}
						t.Run(name, func(t *testing.T) {
							auth := codexOAuthTestAuth("http://example.invalid")
							if apiKey {
								auth = codexAPIKeyTestAuth("http://example.invalid")
							}
							body := `{"model":"` + model + `"}`
							if clientTier != "" {
								body = `{"model":"` + model + `","service_tier":"` + clientTier + `"}`
							}
							doc := ojson.NewDocString(body)
							applyCodexServiceTierDoc(context.Background(), codexSpeedTestConfig(speed, ignoreClientTier), auth, model, doc)

							want := clientTier
							if !apiKey && (clientTier == "" || ignoreClientTier) {
								want = tiers[model]
							}
							got := doc.Get("service_tier")
							if want == "" {
								if got.Exists() {
									t.Fatalf("service_tier = %s, want absent", got.Raw)
								}
								return
							}
							if got.String() != want {
								t.Fatalf("service_tier = %s, want %q", got.Raw, want)
							}
						})
					}
				}
			}
		}
	}
}

func TestApplyCodexServiceTierDocNilConfigKeepsBody(t *testing.T) {
	useCodexServiceTierFixture(t)
	body := `{"model":"gpt-fixture-ultrafast","service_tier":"priority"}`
	doc := ojson.NewDocString(body)
	applyCodexServiceTierDoc(context.Background(), nil, codexOAuthTestAuth("http://example.invalid"), tierFixtureUltrafastModel, doc)
	if got := doc.String(); got != body {
		t.Fatalf("body = %s, want unchanged", got)
	}
}

// One end-to-end case per HTTP transport: the profile tier reaches the upstream
// body and the routing hint, and a client tier is removed under the flag.
func TestCodexExecutorProfileSpeedHTTP(t *testing.T) {
	useCodexServiceTierFixture(t)
	for _, stream := range []bool{false, true} {
		name := "non-stream"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			var captured capturedCodexRequest
			server := newCodexRoutingHintServer(t, &captured)
			defer server.Close()

			executor := NewCodexExecutor(codexSpeedTestConfig(config.CodexSpeedUltrafast, true))
			payload := []byte(`{"model":"gpt-fixture-ultrafast","service_tier":"priority","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
			req := cliproxyexecutor.Request{Model: tierFixtureUltrafastModel, Payload: payload}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Stream: stream}
			runCodexTestRequest(t, executor, codexOAuthTestAuth(server.URL), req, opts)

			if captured.serviceTier.String() != "ultrafast" {
				t.Fatalf("body service_tier = %s, want ultrafast", captured.serviceTier.Raw)
			}
			if want := "model=gpt-fixture-ultrafast;tier=ultrafast"; captured.routingHint != want {
				t.Fatalf("routing hint = %q, want %q", captured.routingHint, want)
			}
		})
	}
}

// One end-to-end case per websocket transport, each on a fresh connection, so
// the handshake hint describes the request.
func TestCodexWebsocketsProfileSpeed(t *testing.T) {
	useCodexServiceTierFixture(t)
	for _, stream := range []bool{false, true} {
		name := "execute"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			var rec codexWebsocketHintRecorder
			server := newCodexWebsocketHintServer(t, &rec)
			defer server.Close()

			exec := NewCodexWebsocketsExecutor(codexSpeedTestConfig(config.CodexSpeedFast, true))
			auth := codexOAuthTestAuth(server.URL)
			auth.ID = "codex-oauth-speed-" + name
			req := cliproxyexecutor.Request{
				Model:   tierFixtureUltrafastModel,
				Payload: []byte(`{"model":"gpt-fixture-ultrafast","service_tier":"ultrafast","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`),
			}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Stream: stream}
			runCodexTestRequest(t, exec, auth, req, opts)

			rec.mu.Lock()
			defer rec.mu.Unlock()
			if len(rec.handshakes) != 1 || len(rec.frames) != 1 {
				t.Fatalf("handshakes = %q, frames = %+v, want one each", rec.handshakes, rec.frames)
			}
			if rec.frames[0].tier != "priority" {
				t.Fatalf("frame service_tier = %q, want priority", rec.frames[0].tier)
			}
			if want := "model=gpt-fixture-ultrafast;tier=priority"; rec.handshakes[0] != want {
				t.Fatalf("handshake hint = %q, want %q", rec.handshakes[0], want)
			}
		})
	}
}

type codexTestExecutor interface {
	Execute(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	ExecuteStream(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
}

func runCodexTestRequest(t *testing.T, exec codexTestExecutor, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) {
	t.Helper()
	if !opts.Stream {
		if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
			t.Fatalf("Execute error: %v", err)
		}
		return
	}
	result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
	}
}

// /responses/compact follows the same rule as response.create.
func TestCodexExecutorCompactProfileSpeed(t *testing.T) {
	useCodexServiceTierFixture(t)
	cases := []struct {
		name             string
		speed            string
		ignoreClientTier bool
		apiKey           bool
		clientTier       string
		wantTier         string
	}{
		{name: "flag strips client tier", speed: config.CodexSpeedStandard, ignoreClientTier: true, clientTier: "priority"},
		{name: "fast injects priority", speed: config.CodexSpeedFast, wantTier: "priority"},
		{name: "client tier kept without flag", speed: config.CodexSpeedStandard, clientTier: "priority", wantTier: "priority"},
		{name: "api key untouched", speed: config.CodexSpeedFast, ignoreClientTier: true, apiKey: true, clientTier: "ultrafast", wantTier: "ultrafast"},
		{name: "api key without tier stays without tier", speed: config.CodexSpeedFast, apiKey: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var captured capturedCodexRequest
			server := newCodexRoutingHintServer(t, &captured)
			defer server.Close()

			auth := codexOAuthTestAuth(server.URL)
			if tc.apiKey {
				auth = codexAPIKeyTestAuth(server.URL)
			}
			payload := `{"model":"gpt-fixture-ultrafast","input":[{"type":"message","role":"user","content":"history"},{"type":"compaction_trigger"}]}`
			if tc.clientTier != "" {
				payload = `{"model":"gpt-fixture-ultrafast","service_tier":"` + tc.clientTier + `","input":[{"type":"message","role":"user","content":"history"},{"type":"compaction_trigger"}]}`
			}
			executor := NewCodexExecutor(codexSpeedTestConfig(tc.speed, tc.ignoreClientTier))
			if _, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: tierFixtureUltrafastModel, Payload: []byte(payload)}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString("openai-response"),
				Alt:          "responses/compact",
			}); err != nil {
				t.Fatalf("compact Execute error: %v", err)
			}
			if captured.path != "/responses/compact" {
				t.Fatalf("upstream path = %q, want /responses/compact", captured.path)
			}
			assertCapturedServiceTier(t, captured.serviceTier, tc.wantTier)
		})
	}
}

// Payload rules run after the profile tier, on create paths and on compact.
func TestCodexProfileSpeedPayloadRulesWin(t *testing.T) {
	useCodexServiceTierFixture(t)
	// No protocol: create paths report "codex" and compact reports "openai-response".
	models := []config.PayloadModelRule{{Name: "gpt-fixture-*"}}
	cases := []struct {
		name     string
		payload  config.PayloadConfig
		wantTier string
	}{
		{name: "filter removes injected tier", payload: config.PayloadConfig{Filter: []config.PayloadFilterRule{{Models: models, Params: []string{"service_tier"}}}}},
		{name: "override replaces injected tier", payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: models, Params: map[string]any{"service_tier": "flex"}}}}, wantTier: "flex"},
	}
	for _, tc := range cases {
		for _, alt := range []string{"", "responses/compact"} {
			t.Run(tc.name+"/alt="+alt, func(t *testing.T) {
				var captured capturedCodexRequest
				server := newCodexRoutingHintServer(t, &captured)
				defer server.Close()

				cfg := codexSpeedTestConfig(config.CodexSpeedUltrafast, false)
				cfg.Payload = tc.payload
				executor := NewCodexExecutor(cfg)
				payload := []byte(`{"model":"gpt-fixture-ultrafast","input":[{"type":"message","role":"user","content":"history"}]}`)
				if _, err := executor.Execute(context.Background(), codexOAuthTestAuth(server.URL), cliproxyexecutor.Request{Model: tierFixtureUltrafastModel, Payload: payload}, cliproxyexecutor.Options{
					SourceFormat: sdktranslator.FromString("openai-response"),
					Alt:          alt,
				}); err != nil {
					t.Fatalf("Execute error: %v", err)
				}
				assertCapturedServiceTier(t, captured.serviceTier, tc.wantTier)
			})
		}
	}
}

// The OpenAI Responses translator drops a client tier it does not know (auto),
// so the request counts as one without a tier and gets the profile tier.
func TestCodexProfileSpeedAfterTranslatorDropsClientTier(t *testing.T) {
	useCodexServiceTierFixture(t)
	var captured capturedCodexRequest
	server := newCodexRoutingHintServer(t, &captured)
	defer server.Close()

	executor := NewCodexExecutor(codexSpeedTestConfig(config.CodexSpeedFast, false))
	payload := []byte(`{"model":"gpt-fixture-priority","service_tier":"auto","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	if _, err := executor.Execute(context.Background(), codexOAuthTestAuth(server.URL), cliproxyexecutor.Request{Model: tierFixturePriorityModel, Payload: payload}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
	}); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	assertCapturedServiceTier(t, captured.serviceTier, "priority")
}

func assertCapturedServiceTier(t *testing.T, got ojson.Result, want string) {
	t.Helper()
	if want == "" {
		if got.Exists() {
			t.Fatalf("upstream service_tier = %s, want absent", got.Raw)
		}
		return
	}
	if got.String() != want {
		t.Fatalf("upstream service_tier = %s, want %q", got.Raw, want)
	}
}

func codexSpeedGinContext(key string) context.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	if key != "" {
		c.Set("userApiKey", key)
	}
	return context.WithValue(context.Background(), "gin", c)
}

// The manager resolves the profile of the inbound client key and the executor
// reads the speed from it: two keys with their own profiles and a key without
// one get different tiers in one process, the context profile wins over the
// executor config, and a changed speed applies to the next request.
func TestCodexProfileSpeedThroughManager(t *testing.T) {
	useCodexServiceTierFixture(t)
	var mu sync.Mutex
	var tiers []ojson.Result
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		tiers = append(tiers, ojson.GetBytes(body, "service_tier"))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-fixture-ultrafast","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n"))
	}))
	defer server.Close()

	const (
		keyUltrafast = "sk-speed-ultrafast"
		keyStandard  = "sk-speed-standard"
		keyShared    = "sk-speed-shared"
		accountID    = "codex-speed-manager-account"
	)
	buildConfig := func(sharedSpeed string) *config.Config {
		cfg := &config.Config{SDKConfig: config.SDKConfig{
			APIKeys:                []string{keyUltrafast, keyStandard, keyShared},
			DisableImageGeneration: config.DisableImageGenerationAll,
		}}
		cfg.Routing.Providers.Codex.Speed = sharedSpeed
		ultrafast := &config.RoutingProfile{}
		ultrafast.Providers.Codex.Speed = config.CodexSpeedUltrafast
		standard := &config.RoutingProfile{}
		standard.Providers.Codex.IgnoreClientTier = true
		cfg.Routing.KeyProfiles = map[string]*config.RoutingProfile{keyUltrafast: ultrafast, keyStandard: standard}
		return cfg
	}

	// The executor config differs from the manager config: without the context
	// profile every request would get priority.
	exec := NewCodexExecutor(codexSpeedTestConfig(config.CodexSpeedFast, false))
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(exec)
	manager.SetConfig(buildConfig(config.CodexSpeedStandard))
	account := codexOAuthTestAuth(server.URL)
	account.ID = accountID
	account.Status = cliproxyauth.StatusActive
	if _, err := manager.Register(context.Background(), account); err != nil {
		t.Fatalf("Register error: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(accountID, "codex", []*registry.ModelInfo{{ID: tierFixtureUltrafastModel}})
	defer registry.GetGlobalRegistry().UnregisterClient(accountID)

	execute := func(key, clientTier string) ojson.Result {
		t.Helper()
		payload := `{"model":"gpt-fixture-ultrafast","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`
		if clientTier != "" {
			payload = `{"model":"gpt-fixture-ultrafast","service_tier":"` + clientTier + `","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`
		}
		mu.Lock()
		before := len(tiers)
		mu.Unlock()
		if _, err := manager.Execute(codexSpeedGinContext(key), []string{"codex"}, cliproxyexecutor.Request{Model: tierFixtureUltrafastModel, Payload: []byte(payload)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")}); err != nil {
			t.Fatalf("Execute(%s) error: %v", key, err)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(tiers) != before+1 {
			t.Fatalf("upstream requests = %d, want %d", len(tiers), before+1)
		}
		return tiers[len(tiers)-1]
	}

	assertCapturedServiceTier(t, execute(keyUltrafast, ""), "ultrafast")
	assertCapturedServiceTier(t, execute(keyStandard, "priority"), "")
	assertCapturedServiceTier(t, execute(keyShared, ""), "")
	assertCapturedServiceTier(t, execute(keyShared, "priority"), "priority")

	manager.SetConfig(buildConfig(config.CodexSpeedUltrafast))
	assertCapturedServiceTier(t, execute(keyShared, ""), "ultrafast")
	assertCapturedServiceTier(t, execute(keyUltrafast, ""), "ultrafast")
	assertCapturedServiceTier(t, execute(keyStandard, ""), "")
}

// Control frames only lose the client tier; they never get the profile speed,
// and API-key auths keep the tier.
func TestRemoveCodexClientServiceTierDoc(t *testing.T) {
	useCodexServiceTierFixture(t)
	for _, tc := range []struct {
		name             string
		ignoreClientTier bool
		apiKey           bool
		body             string
		wantTier         string
	}{
		{name: "ignored", ignoreClientTier: true, body: `{"service_tier":"priority"}`},
		{name: "respected", body: `{"service_tier":"priority"}`, wantTier: "priority"},
		{name: "api key untouched", ignoreClientTier: true, apiKey: true, body: `{"service_tier":"priority"}`, wantTier: "priority"},
		{name: "no speed injection", ignoreClientTier: true, body: `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := codexOAuthTestAuth("http://unused")
			if tc.apiKey {
				auth = codexAPIKeyTestAuth("http://unused")
			}
			doc := ojson.NewDoc([]byte(tc.body))
			removeCodexClientServiceTierDoc(context.Background(), codexSpeedTestConfig(config.CodexSpeedUltrafast, tc.ignoreClientTier), auth, doc)
			if got := doc.Get("service_tier").String(); got != tc.wantTier {
				t.Fatalf("service_tier = %q, want %q: %s", got, tc.wantTier, doc.Bytes())
			}
		})
	}
}
