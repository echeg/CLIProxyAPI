package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
)

type claudeFallbackExecutor struct {
	ProviderExecutor
	provider string
	run      func(*Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	stream   func(*Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
}

func (e *claudeFallbackExecutor) Identifier() string { return e.provider }
func (e *claudeFallbackExecutor) Execute(_ context.Context, a *Auth, r cliproxyexecutor.Request, o cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.run(a, r, o)
}
func (e *claudeFallbackExecutor) CountTokens(ctx context.Context, a *Auth, r cliproxyexecutor.Request, o cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.Execute(ctx, a, r, o)
}
func (e *claudeFallbackExecutor) ExecuteStream(_ context.Context, a *Auth, r cliproxyexecutor.Request, o cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e.stream != nil {
		return e.stream(a, r, o)
	}
	res, err := e.run(a, r, o)
	if err != nil {
		return nil, err
	}
	return fallbackTestStream(cliproxyexecutor.StreamChunk{Payload: res.Payload}), nil
}
func fallbackTestStream(chunks ...cliproxyexecutor.StreamChunk) *cliproxyexecutor.StreamResult {
	ch := make(chan cliproxyexecutor.StreamChunk, len(chunks))
	for _, c := range chunks {
		ch <- c
	}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}
}
func fallbackTestManager(t *testing.T, mode string, primaryErr error) (*Manager, *[]string, *claudeFallbackExecutor, *claudeFallbackExecutor) {
	t.Helper()
	m := NewManager(nil, &FillFirstSelector{}, nil)
	m.SetRetryConfig(0, 0, 0)
	m.SetConfig(&internalconfig.Config{Routing: internalconfig.RoutingConfig{ClaudeFallback: internalconfig.ClaudeFallbackConfig{Enabled: mode != "off", Mode: mode, Mappings: []internalconfig.ClaudeFallbackMapping{{From: "claude-opus-*", To: "gpt-6-astra"}}}}})
	var calls []string
	primary := &claudeFallbackExecutor{provider: "claude", run: func(a *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		calls = append(calls, a.Provider)
		return cliproxyexecutor.Response{Payload: []byte("primary")}, primaryErr
	}}
	fallback := &claudeFallbackExecutor{provider: "codex", run: func(a *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		calls = append(calls, a.Provider)
		if req.Model != "gpt-6-astra" || a.AuthKind() != AuthKindOAuth {
			t.Errorf("bad fallback selection: %s/%s", req.Model, a.AuthKind())
		}
		if opts.Metadata[cliproxyexecutor.RequestedModelMetadataKey] != "claude-opus-5" {
			t.Error("lost requested model")
		}
		return cliproxyexecutor.Response{Payload: []byte("fallback")}, nil
	}}
	m.RegisterExecutor(primary)
	m.RegisterExecutor(fallback)
	for i, provider := range []string{"claude", "claude", "codex"} {
		id := t.Name() + string(rune('a'+i))
		model := "claude-opus-5"
		if provider == "codex" {
			model = "gpt-6-astra"
		}
		registry.GetGlobalRegistry().RegisterClient(id, provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		if _, errRegister := m.Register(context.Background(), &Auth{ID: id, Provider: provider, Status: StatusActive, Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth}}); errRegister != nil {
			t.Fatal(errRegister)
		}
	}
	return m, &calls, primary, fallback
}

func TestClaudeFallbackModesAndFailures(t *testing.T) {
	withQuotaCooldownEnabled(t)
	for _, path := range []string{"execute", "count", "stream"} {
		for _, tc := range []struct {
			name, mode string
			err        error
			want       []string
		}{
			{"healthy", "auto", nil, []string{"claude"}},
			{"quota", "auto", &Error{HTTPStatus: 429}, []string{"claude", "claude", "codex"}},
			{"overload", "auto", &Error{HTTPStatus: 529}, []string{"claude", "claude", "codex"}},
			{"network", "auto", io.ErrUnexpectedEOF, []string{"claude", "claude", "codex"}},
			{"bad request", "auto", &Error{HTTPStatus: 400, Message: `{"error":{"type":"invalid_request_error","message":"invalid request"}}`}, []string{"claude"}},
			{"canceled", "auto", context.Canceled, []string{"claude"}},
			{"disabled", "off", &Error{HTTPStatus: 429}, []string{"claude", "claude"}},
			{"forced", "always", nil, []string{"codex"}},
		} {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				m, calls, _, _ := fallbackTestManager(t, tc.mode, tc.err)
				req := cliproxyexecutor.Request{Model: "claude-opus-5"}
				var err error
				switch path {
				case "execute":
					_, err = m.Execute(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{})
				case "count":
					_, err = m.ExecuteCount(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{})
				case "stream":
					var result *cliproxyexecutor.StreamResult
					result, err = m.ExecuteStream(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{Stream: true})
					if err == nil {
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								err = chunk.Err
							}
						}
					}
				}
				if path == "stream" && tc.name == "canceled" {
					tc.want = []string{"claude", "claude"}
				}
				if !reflect.DeepEqual(*calls, tc.want) {
					t.Fatalf("calls=%v, want %v; err=%v", *calls, tc.want, err)
				}
				wantSuccess := tc.err == nil || tc.want[len(tc.want)-1] == "codex"
				if (err == nil) != wantSuccess {
					t.Fatalf("unexpected error: %v", err)
				}
			})
		}
	}
}

func TestClaudeFallbackStreamingBoundary(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(map[bool]string{false: "bootstrap", true: "started"}[started], func(t *testing.T) {
			m, calls, primary, _ := fallbackTestManager(t, "auto", nil)
			primary.stream = func(a *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				*calls = append(*calls, a.Provider)
				chunks := []cliproxyexecutor.StreamChunk{}
				if started {
					chunks = append(chunks, cliproxyexecutor.StreamChunk{Payload: []byte("data: partial\n\n")})
				}
				chunks = append(chunks, cliproxyexecutor.StreamChunk{Err: &Error{HTTPStatus: 503}})
				return fallbackTestStream(chunks...), nil
			}
			result, err := m.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "claude-opus-5"}, cliproxyexecutor.Options{Stream: true})
			if err != nil {
				t.Fatal(err)
			}
			var body string
			var streamErr error
			for chunk := range result.Chunks {
				body += string(chunk.Payload)
				if chunk.Err != nil {
					streamErr = chunk.Err
				}
			}
			if started {
				if body != "data: partial\n\n" || streamErr == nil || len(*calls) != 1 {
					t.Fatalf("replayed started stream: %q %v %v", body, streamErr, *calls)
				}
			} else if body != "fallback" || streamErr != nil {
				t.Fatalf("bootstrap fallback: %q %v", body, streamErr)
			}
		})
	}
}

func TestClaudeFallbackPreservesContextAndReturnsToPrimary(t *testing.T) {
	m, calls, primary, fallback := fallbackTestManager(t, "auto", io.ErrUnexpectedEOF)
	payload := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"continue"}],"tools":[{"name":"Read"}]}`)
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: payload, Headers: http.Header{"X-Session-Id": []string{"existing-session"}}, Metadata: map[string]any{cliproxyexecutor.AuthSelectionModelMetadataKey: "claude-opus-5"}}
	fallback.run = func(_ *Auth, r cliproxyexecutor.Request, o cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		*calls = append(*calls, "codex")
		if r.Model != "gpt-6-astra" || string(r.Payload) != string(payload) || string(o.OriginalRequest) != string(payload) || o.SourceFormat != opts.SourceFormat || o.Headers.Get("X-Session-Id") != "existing-session" {
			t.Fatal("lost conversation/protocol")
		}
		if _, exists := o.Metadata[cliproxyexecutor.AuthSelectionModelMetadataKey]; exists {
			t.Fatal("source selection override survived")
		}
		return cliproxyexecutor.Response{}, nil
	}
	req := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: payload}
	if _, err := m.Execute(context.Background(), []string{"claude"}, req, opts); err != nil {
		t.Fatal(err)
	}
	if opts.Metadata[cliproxyexecutor.AuthSelectionModelMetadataKey] != "claude-opus-5" {
		t.Fatal("mutated original options")
	}
	primary.run = func(_ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		*calls = append(*calls, "claude")
		return cliproxyexecutor.Response{}, nil
	}
	if _, err := m.Execute(context.Background(), []string{"claude"}, req, opts); err != nil {
		t.Fatal(err)
	}
	if (*calls)[len(*calls)-1] != "claude" {
		t.Fatal("did not recover primary")
	}
}

func TestClaudeFallbackCooldownAndProtection(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m, calls, _, _ := fallbackTestManager(t, "auto", nil)
	retry := time.Hour
	for _, a := range m.List() {
		if a.Provider == "claude" {
			m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: a.Provider, Model: "claude-opus-5", Error: &Error{HTTPStatus: 429}, RetryAfter: &retry})
		}
	}
	if _, err := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "claude-opus-5"}, cliproxyexecutor.Options{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*calls, []string{"codex"}) {
		t.Fatal(*calls)
	}
	profileCtx := m.ensureRoutingProfile(context.Background())
	_, target := m.claudeFallbackTarget(profileCtx, []string{"claude"}, cliproxyexecutor.Request{Model: "claude-opus-5(high)"}, cliproxyexecutor.Options{})
	if target != "gpt-6-astra(high)" {
		t.Fatal(target)
	}
	for _, opts := range []cliproxyexecutor.Options{{Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: "probe-auth"}}} {
		if _, target := m.claudeFallbackTarget(profileCtx, []string{"claude"}, cliproxyexecutor.Request{Model: "claude-opus-5"}, opts); target != "" {
			t.Fatal("pinned request rerouted")
		}
	}
	if shouldFallbackClaude(errors.New("unknown internal error")) || shouldFallbackClaude(context.DeadlineExceeded) {
		t.Fatal("unclassified error rerouted")
	}
}

func fallbackProfileContext(mode, to string) context.Context {
	return withRoutingProfile(context.Background(), &routingProfile{fallback: internalconfig.ClaudeFallbackConfig{
		Enabled: mode != "off", Mode: mode, Mappings: []internalconfig.ClaudeFallbackMapping{{From: "claude-opus-*", To: to}},
	}})
}

func TestClaudeFallbackTargetReadsContextProfile(t *testing.T) {
	m, _, _, _ := fallbackTestManager(t, "off", nil)
	req := cliproxyexecutor.Request{Model: "claude-opus-5"}
	if mode, target := m.claudeFallbackTarget(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{}); mode != "" || target != "" {
		t.Fatalf("nil profile produced fallback %q/%q", mode, target)
	}
	for _, mode := range []string{"auto", "always"} {
		gotMode, target := m.claudeFallbackTarget(fallbackProfileContext(mode, "gpt-6-sol"), []string{"claude"}, req, cliproxyexecutor.Options{})
		if gotMode != mode || target != "gpt-6-sol" {
			t.Fatalf("mode %s: got %q/%q", mode, gotMode, target)
		}
	}
	if _, target := m.claudeFallbackTarget(fallbackProfileContext("off", "gpt-6-sol"), []string{"claude"}, req, cliproxyexecutor.Options{}); target != "" {
		t.Fatalf("disabled profile produced target %q", target)
	}
}

func TestClaudeFallbackContextProfileOverridesSharedConfig(t *testing.T) {
	// The shared config disables fallback; the request profile forces it.
	m, calls, _, fallback := fallbackTestManager(t, "off", nil)
	fallback.run = func(a *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		*calls = append(*calls, a.Provider)
		if req.Model != "gpt-6-astra" {
			t.Errorf("fallback model %q", req.Model)
		}
		return cliproxyexecutor.Response{}, nil
	}
	if _, err := m.Execute(fallbackProfileContext("always", "gpt-6-astra"), []string{"claude"}, cliproxyexecutor.Request{Model: "claude-opus-5"}, cliproxyexecutor.Options{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*calls, []string{"codex"}) {
		t.Fatalf("calls=%v, want [codex]", *calls)
	}
}

// TestClaudeFallbackLegUsesKeyProfile runs the Codex leg through the pipeline:
// the key's Codex preference wins, a key without a preference applies its own
// selection, and a key without a profile uses the shared selection and logs nothing.
func TestClaudeFallbackLegUsesKeyProfile(t *testing.T) {
	withQuotaCooldownEnabled(t)
	hook := newStandardLogHook(t)

	const keyPreferred, keySelection, keyShared = "sk-fallback-preferred", "sk-fallback-selection", "sk-fallback-shared"
	m := NewManager(nil, nil, nil)
	m.SetSelector(NewPipelineSelector(m.SharedSelection))
	m.SetRetryConfig(0, 0, 0)

	var codexCalls []string
	m.RegisterExecutor(&claudeFallbackExecutor{provider: "claude", run: func(*Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusTooManyRequests}
	}})
	m.RegisterExecutor(&claudeFallbackExecutor{provider: "codex", run: func(a *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		if req.Model != "gpt-6-sol" {
			t.Errorf("fallback model %q, want gpt-6-sol", req.Model)
		}
		codexCalls = append(codexCalls, a.ID)
		return cliproxyexecutor.Response{}, nil
	}})
	indexes := map[string]string{}
	for _, spec := range []struct{ id, provider, priority, model string }{
		{"claude-1", "claude", "0", "claude-opus-5"},
		{"claude-2", "claude", "0", "claude-opus-5"},
		{"codex-a", "codex", "0", "gpt-6-sol"},
		{"codex-b", "codex", "10", "gpt-6-sol"},
		{"codex-c", "codex", "0", "gpt-6-sol"},
	} {
		id := t.Name() + "-" + spec.id
		registry.GetGlobalRegistry().RegisterClient(id, spec.provider, []*registry.ModelInfo{{ID: spec.model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		registered, errRegister := m.Register(WithSkipPersist(context.Background()), &Auth{ID: id, Provider: spec.provider, Status: StatusActive,
			Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth, "priority": spec.priority}})
		if errRegister != nil {
			t.Fatal(errRegister)
		}
		indexes[id] = registered.Index
	}
	codexID := func(suffix string) string { return t.Name() + "-" + suffix }

	fallback := internalconfig.ClaudeFallbackConfig{Enabled: true, Mode: "auto", Mappings: []internalconfig.ClaudeFallbackMapping{{From: "claude-opus-*", To: "gpt-6-sol"}}}
	keySelectionConfig := internalconfig.SelectionConfig{Primary: "priority", Tiebreak: "fill-first"}
	m.SetConfig(&internalconfig.Config{
		SDKConfig: internalconfig.SDKConfig{APIKeys: []string{keyPreferred, keySelection, keyShared}},
		Routing: internalconfig.RoutingConfig{
			Selection:      internalconfig.SelectionConfig{Primary: "fill-first"},
			ClaudeFallback: fallback,
			KeyProfiles: map[string]*internalconfig.RoutingProfile{
				keyPreferred: {PreferredAccounts: map[string]string{"codex": indexes[codexID("codex-c")]}, Selection: keySelectionConfig, ClaudeFallback: fallback},
				keySelection: {Selection: keySelectionConfig, ClaudeFallback: fallback},
			},
		},
	})

	for _, tc := range []struct {
		key, wantCodex, wantStage string
	}{
		// The preference beats the key's priority narrowing (codex-b has priority 10).
		{keyPreferred, "codex-c", routingStagePreferred},
		// The key's {priority, fill-first} selects the higher tier.
		{keySelection, "codex-b", routingStagePipeline},
		// The shared {fill-first} selects the lowest ID and logs nothing.
		{keyShared, "codex-a", ""},
	} {
		t.Run(tc.key, func(t *testing.T) {
			hook.Reset()
			codexCalls = nil
			if _, err := m.Execute(ginContextWithKey(tc.key), []string{"claude"}, cliproxyexecutor.Request{Model: "claude-opus-5"}, cliproxyexecutor.Options{}); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(codexCalls, []string{codexID(tc.wantCodex)}) {
				t.Fatalf("codex calls = %v, want [%s]", codexCalls, codexID(tc.wantCodex))
			}
			var codexEntries []*log.Entry
			for _, entry := range hook.AllEntries() {
				if entry.Message != "Routing profile selection" {
					continue
				}
				if line, _ := entry.String(); strings.Contains(line, tc.key) {
					t.Fatalf("log line leaked the raw key: %s", line)
				}
				if entry.Data["routing_key"] != internalconfig.KeyHashPrefix(tc.key) {
					t.Fatalf("routing_key = %v, want the hash prefix", entry.Data["routing_key"])
				}
				if entry.Data["provider"] == "codex" {
					codexEntries = append(codexEntries, entry)
				} else if entry.Data["routing_stage"] != routingStagePipeline || entry.Data["fallback_stage"] != nil {
					t.Fatalf("claude leg log fields = %v", entry.Data)
				}
			}
			if tc.wantStage == "" {
				for _, entry := range hook.AllEntries() {
					if entry.Message == "Routing profile selection" {
						t.Fatalf("shared profile logged a selection: %v", entry.Data)
					}
				}
				return
			}
			if len(codexEntries) != 1 {
				t.Fatalf("codex selection log entries = %d, want 1", len(codexEntries))
			}
			got := codexEntries[0].Data
			if got["routing_stage"] != routingStageFallback || got["fallback_stage"] != tc.wantStage || got["auth_index"] != indexes[codexID(tc.wantCodex)] {
				t.Fatalf("codex leg log fields = %v", got)
			}
		})
	}
}

// A request that requires a non-OAuth credential kind, or that lists no Claude
// provider, never falls back.
func TestClaudeFallbackTargetGuards(t *testing.T) {
	m, _, _, _ := fallbackTestManager(t, "off", nil)
	req := cliproxyexecutor.Request{Model: "claude-opus-5"}
	for _, tc := range []struct {
		name      string
		ctx       context.Context
		providers []string
		want      string
	}{
		{name: "oauth required", ctx: withRequiredAuthKind(fallbackProfileContext("auto", "gpt-6-sol"), AuthKindOAuth), providers: []string{"claude"}, want: "gpt-6-sol"},
		{name: "api key required", ctx: withRequiredAuthKind(fallbackProfileContext("auto", "gpt-6-sol"), AuthKindAPIKey), providers: []string{"claude"}},
		{name: "no claude provider", ctx: fallbackProfileContext("always", "gpt-6-sol"), providers: []string{"codex"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, target := m.claudeFallbackTarget(tc.ctx, tc.providers, req, cliproxyexecutor.Options{}); target != tc.want {
				t.Fatalf("target = %q, want %q", target, tc.want)
			}
		})
	}
}

// With retries enabled, a fallback-eligible Claude failure goes to Codex after one
// Claude round instead of retrying Claude first.
func TestClaudeFallbackSkipsClaudeRetries(t *testing.T) {
	withQuotaCooldownEnabled(t)
	for _, path := range []string{"execute", "count", "stream"} {
		t.Run(path, func(t *testing.T) {
			m, calls, _, _ := fallbackTestManager(t, "auto", &Error{HTTPStatus: 503})
			m.SetRetryConfig(3, time.Minute, 0)
			// Without the short-circuit the manager waits out Claude cooldowns of up to
			// a minute; the deadline turns that regression into a fast failure.
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			req := cliproxyexecutor.Request{Model: "claude-opus-5"}
			var err error
			switch path {
			case "execute":
				_, err = m.Execute(ctx, []string{"claude"}, req, cliproxyexecutor.Options{})
			case "count":
				_, err = m.ExecuteCount(ctx, []string{"claude"}, req, cliproxyexecutor.Options{})
			case "stream":
				var result *cliproxyexecutor.StreamResult
				result, err = m.ExecuteStream(ctx, []string{"claude"}, req, cliproxyexecutor.Options{Stream: true})
				if err == nil {
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							err = chunk.Err
						}
					}
				}
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if want := []string{"claude", "claude", "codex"}; !reflect.DeepEqual(*calls, want) {
				t.Fatalf("calls = %v, want %v", *calls, want)
			}
		})
	}
}

// A caller that goes away during the Claude attempt gets its context error and
// triggers no Codex call.
func TestClaudeFallbackStopsWhenCallerCancels(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m, calls, primary, _ := fallbackTestManager(t, "auto", nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	primary.run = func(a *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		*calls = append(*calls, a.Provider)
		cancel()
		return cliproxyexecutor.Response{}, &Error{HTTPStatus: 429}
	}
	_, err := m.Execute(ctx, []string{"claude"}, cliproxyexecutor.Request{Model: "claude-opus-5"}, cliproxyexecutor.Options{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	for _, provider := range *calls {
		if provider == "codex" {
			t.Fatalf("calls = %v, want no Codex call", *calls)
		}
	}
}

func TestClaudeFallbackKeepsPrimaryErrorWhenCodexUnavailable(t *testing.T) {
	withQuotaCooldownEnabled(t)
	for _, path := range []string{"execute", "count", "stream"} {
		t.Run(path, func(t *testing.T) {
			primaryErr := &Error{HTTPStatus: http.StatusTooManyRequests, Message: "claude quota"}
			m, calls, _, _ := fallbackTestManager(t, "auto", primaryErr)
			// The Codex credential no longer serves the mapped model, so the leg cannot select one.
			registry.GetGlobalRegistry().UnregisterClient(t.Name() + "c")
			req := cliproxyexecutor.Request{Model: "claude-opus-5"}
			var err error
			switch path {
			case "execute":
				_, err = m.Execute(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{})
			case "count":
				_, err = m.ExecuteCount(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{})
			case "stream":
				var result *cliproxyexecutor.StreamResult
				result, err = m.ExecuteStream(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{Stream: true})
				if err == nil && result != nil {
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							err = chunk.Err
						}
					}
				}
			}
			if !reflect.DeepEqual(*calls, []string{"claude", "claude"}) {
				t.Fatalf("calls=%v; err=%v", *calls, err)
			}
			if statusCodeFromError(err) != http.StatusTooManyRequests {
				t.Fatalf("err=%v (status %d), want the Claude 429", err, statusCodeFromError(err))
			}
		})
	}
}

// A Claude error that matches a stop rule ends the request; the fallback must not
// turn it into a Codex call, and the client sees the original error.
func TestClaudeFallbackHonorsRequestScopedStop(t *testing.T) {
	withQuotaCooldownEnabled(t)
	for _, action := range []string{RequestScopedActionStop, RequestScopedActionStopAndCooldown} {
		for _, path := range []string{"execute", "count", "stream"} {
			t.Run(action+"/"+path, func(t *testing.T) {
				primaryErr := &Error{HTTPStatus: http.StatusTooManyRequests, Message: "account_suspended"}
				m, calls, _, _ := fallbackTestManager(t, "auto", primaryErr)
				m.SetConfig(&internalconfig.Config{
					Routing: internalconfig.RoutingConfig{ClaudeFallback: internalconfig.ClaudeFallbackConfig{Enabled: true, Mode: "auto", Mappings: []internalconfig.ClaudeFallbackMapping{{From: "claude-opus-*", To: "gpt-6-astra"}}}},
					OAuthRequestScopedErrors: map[string][]internalconfig.RequestScopedErrorRule{
						"claude": {{Status: http.StatusTooManyRequests, Match: []string{"account_suspended"}, Action: action}},
					},
				})
				req := cliproxyexecutor.Request{Model: "claude-opus-5"}
				var err error
				switch path {
				case "execute":
					_, err = m.Execute(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{})
				case "count":
					_, err = m.ExecuteCount(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{})
				case "stream":
					_, err = m.ExecuteStream(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{Stream: true})
				}
				if !reflect.DeepEqual(*calls, []string{"claude"}) {
					t.Fatalf("calls=%v, want only the stopped Claude attempt; err=%v", *calls, err)
				}
				if err != primaryErr {
					t.Fatalf("err = %T/%v, want the original Claude error", err, err)
				}
			})
		}
	}
}
