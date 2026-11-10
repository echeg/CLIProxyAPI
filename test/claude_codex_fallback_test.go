package test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type unavailableClaudeExecutor struct{ coreauth.ProviderExecutor }

func (*unavailableClaudeExecutor) Identifier() string { return "claude" }
func (*unavailableClaudeExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, &coreauth.Error{HTTPStatus: 503}
}
func (*unavailableClaudeExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, &coreauth.Error{HTTPStatus: 503}
}

func TestClaudeFallbackRealCodexTranslation(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			requests := make(chan []byte, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					t.Error(errRead)
					return
				}
				requests <- body
				w.Header().Set("Content-Type", "text/event-stream")
				for _, event := range []string{
					`{"type":"response.created","response":{"id":"resp_fallback","model":"gpt-6-astra"}}`,
					`{"type":"response.output_item.added","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_next","name":"Read","arguments":""}}`,
					`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"path\":\"next.txt\"}"}`,
					`{"type":"response.function_call_arguments.done","output_index":0,"arguments":"{\"path\":\"next.txt\"}"}`,
					`{"type":"response.output_item.done","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_next","name":"Read","arguments":"{\"path\":\"next.txt\"}"}}`,
					`{"type":"response.completed","response":{"id":"resp_fallback","model":"gpt-6-astra","status":"completed","output":[{"id":"fc_1","type":"function_call","call_id":"call_next","name":"Read","arguments":"{\"path\":\"next.txt\"}"}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`,
				} {
					if _, errWrite := fmt.Fprintf(w, "data: %s\n\n", event); errWrite != nil {
						t.Error(errWrite)
					}
				}
			}))
			defer server.Close()
			cfg := &config.Config{Routing: config.RoutingConfig{ClaudeFallback: config.ClaudeFallbackConfig{Enabled: true, Mode: "auto", Mappings: []config.ClaudeFallbackMapping{{From: "claude-opus-*", To: "gpt-6-astra"}}}}}
			manager := coreauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			manager.SetRetryConfig(0, 0, 0)
			manager.RegisterExecutor(&unavailableClaudeExecutor{})
			manager.RegisterExecutor(runtimeexecutor.NewCodexExecutor(cfg))
			for _, provider := range []string{"claude", "codex"} {
				id := t.Name() + provider
				model := "claude-opus-5"
				if provider == "codex" {
					model = "gpt-6-astra"
				}
				registry.GetGlobalRegistry().RegisterClient(id, provider, []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
				_, errRegister := manager.Register(context.Background(), &coreauth.Auth{ID: id, Provider: provider, Attributes: map[string]string{"base_url": server.URL, coreauth.AttributeAuthKind: coreauth.AuthKindOAuth}, Metadata: map[string]any{"access_token": "test-token"}})
				if errRegister != nil {
					t.Fatal(errRegister)
				}
			}
			payload := []byte(`{"model":"claude-opus-5","max_tokens":1000,"system":"Keep the conversation","tools":[{"name":"Read","description":"Read a file","input_schema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}],"messages":[{"role":"user","content":"read files"},{"role":"assistant","content":[{"type":"tool_use","id":"call_previous","name":"Read","input":{"path":"previous.txt"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_previous","content":"previous content"}]}]}`)
			req := coreexecutor.Request{Model: "claude-opus-5", Payload: payload}
			opts := coreexecutor.Options{Stream: stream, OriginalRequest: payload, SourceFormat: sdktranslator.FormatClaude}
			var output string
			if stream {
				res, errStream := manager.ExecuteStream(context.Background(), []string{"claude"}, req, opts)
				if errStream != nil {
					t.Fatal(errStream)
				}
				for chunk := range res.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
					output += string(chunk.Payload)
				}
				if !strings.Contains(output, "message_stop") {
					t.Fatalf("invalid Claude stream: %s", output)
				}
			} else {
				res, errExecute := manager.Execute(context.Background(), []string{"claude"}, req, opts)
				if errExecute != nil {
					t.Fatal(errExecute)
				}
				output = string(res.Payload)
			}
			if !strings.Contains(output, "tool_use") || !strings.Contains(output, "call_next") || !strings.Contains(output, "Read") {
				t.Fatalf("lost downstream tool call: %s", output)
			}
			select {
			case body := <-requests:
				if ojson.GetBytes(body, "model").String() != "gpt-6-astra" || !strings.Contains(string(body), "function_call_output") || !strings.Contains(string(body), "previous content") || !strings.Contains(string(body), "Keep the conversation") {
					t.Fatalf("lost model/history in translation: %s", body)
				}
			default:
				t.Fatal("no Codex request")
			}
		})
	}
}
