package executor

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// codex.fast-mode must make subscription requests look like native Codex Fast:
// service_tier=priority in the body and the matching routing hint.
func TestCodexExecutorFastModeRequestsPriorityTier(t *testing.T) {
	filterTier := config.PayloadConfig{Filter: []config.PayloadFilterRule{{
		Models: []config.PayloadModelRule{{Name: "gpt-*", Protocol: "codex"}},
		Params: []string{"service_tier"},
	}}}

	cases := []struct {
		name     string
		fastMode bool
		apiKey   bool
		stream   bool
		model    string
		payload  config.PayloadConfig
		wantHint string
		wantTier string
	}{
		{name: "oauth stream", fastMode: true, stream: true, model: "gpt-5.5", wantHint: "model=gpt-5.5;tier=priority", wantTier: "priority"},
		{name: "oauth non-stream", fastMode: true, model: "gpt-5.5", wantHint: "model=gpt-5.5;tier=priority", wantTier: "priority"},
		{name: "disabled", stream: true, model: "gpt-5.5", wantHint: "model=gpt-5.5"},
		{name: "api key untouched", fastMode: true, apiKey: true, stream: true, model: "gpt-5.5"},
		{name: "model without priority tier", fastMode: true, stream: true, model: "gpt-unlisted", wantHint: "model=gpt-unlisted"},
		{name: "payload filter wins", fastMode: true, stream: true, model: "gpt-5.5", payload: filterTier, wantHint: "model=gpt-5.5"},
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
			cfg := &config.Config{Payload: tc.payload}
			cfg.Codex.FastMode = tc.fastMode
			executor := NewCodexExecutor(cfg)
			payload := []byte(`{"model":"` + tc.model + `","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
			req := cliproxyexecutor.Request{Model: tc.model, Payload: payload}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude"), Stream: tc.stream}

			if tc.stream {
				result, err := executor.ExecuteStream(context.Background(), auth, req, opts)
				if err != nil {
					t.Fatalf("ExecuteStream error: %v", err)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("stream chunk error: %v", chunk.Err)
					}
				}
			} else if _, err := executor.Execute(context.Background(), auth, req, opts); err != nil {
				t.Fatalf("Execute error: %v", err)
			}

			if tc.wantHint == "" {
				if captured.hasHint {
					t.Fatalf("routing hint = %q, want header absent", captured.routingHint)
				}
			} else if captured.routingHint != tc.wantHint {
				t.Fatalf("routing hint = %q, want %q", captured.routingHint, tc.wantHint)
			}
			if tc.wantTier == "" {
				if captured.serviceTier.Exists() {
					t.Fatalf("body service_tier = %s, want absent", captured.serviceTier.Raw)
				}
			} else if captured.serviceTier.String() != tc.wantTier {
				t.Fatalf("body service_tier = %s, want %q", captured.serviceTier.Raw, tc.wantTier)
			}
		})
	}
}

func TestApplyCodexFastModeKeepsRequestedTier(t *testing.T) {
	cfg := &config.Config{}
	cfg.Codex.FastMode = true
	body := []byte(`{"model":"gpt-5.5","service_tier":"ultrafast"}`)
	if got := string(applyCodexFastMode(cfg, codexOAuthTestAuth("http://example.invalid"), "gpt-5.5", body)); got != string(body) {
		t.Fatalf("body = %s, want unchanged", got)
	}
}
