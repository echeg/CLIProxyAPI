package test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// perAuthExecutor answers 200 or 429 per credential and records every call.
type perAuthExecutor struct {
	coreauth.ProviderExecutor
	provider string

	mu      sync.Mutex
	failing map[string]bool
	calls   []string
	models  []string
}

func (e *perAuthExecutor) Identifier() string { return e.provider }

func (e *perAuthExecutor) Execute(_ context.Context, auth *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, auth.ID)
	e.models = append(e.models, req.Model)
	if e.failing[auth.ID] {
		return coreexecutor.Response{}, &coreauth.Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate limited"}
	}
	return coreexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *perAuthExecutor) setFailing(ids ...string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.failing = map[string]bool{}
	for _, id := range ids {
		e.failing[id] = true
	}
}

// takeCalls returns and clears the recorded credential IDs and models.
func (e *perAuthExecutor) takeCalls() ([]string, []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	calls, models := e.calls, e.models
	e.calls, e.models = nil, nil
	return calls, models
}

// keyRequestContext mirrors the request context built by the HTTP layer: the access
// middleware stores the client key as userApiKey on the gin context, and the handler
// stores the gin context under "gin".
func keyRequestContext(key string) context.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	if key != "" {
		c.Set("userApiKey", key)
	}
	return context.WithValue(context.Background(), "gin", c)
}

func TestKeyRoutingProfilesShareOnePool(t *testing.T) {
	const (
		keyA      = "sk-integration-key-a"
		keyB      = "sk-integration-key-b"
		keyShared = "sk-integration-key-shared"
		model     = "claude-opus-5"
		mapped    = "gpt-6-sol"
		session   = "integration-session"
	)
	hook := logtest.NewLocal(log.StandardLogger())
	t.Cleanup(hook.Reset)

	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	affinity := coreauth.NewSessionAffinitySelectorWithConfig(coreauth.SessionAffinityConfig{
		Fallback: coreauth.NewPipelineSelector(manager.SharedSelection),
	})
	t.Cleanup(affinity.Stop)
	manager.SetSelector(affinity)

	claude := &perAuthExecutor{provider: "claude"}
	codex := &perAuthExecutor{provider: "codex"}
	manager.RegisterExecutor(claude)
	manager.RegisterExecutor(codex)

	id := func(suffix string) string { return t.Name() + "-" + suffix }
	indexes := map[string]string{}
	// claude-2 and codex-1 sit in the higher priority tier, so a preference that lands
	// on claude-1 or codex-2 is visible against the priority narrowing.
	for _, spec := range []struct{ name, provider, priority, model string }{
		{"claude-1", "claude", "0", model},
		{"claude-2", "claude", "10", model},
		{"codex-1", "codex", "10", mapped},
		{"codex-2", "codex", "0", mapped},
	} {
		authID := id(spec.name)
		registry.GetGlobalRegistry().RegisterClient(authID, spec.provider, []*registry.ModelInfo{{ID: spec.model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
		registered, errRegister := manager.Register(coreauth.WithSkipPersist(context.Background()), &coreauth.Auth{
			ID: authID, Provider: spec.provider, Status: coreauth.StatusActive,
			Attributes: map[string]string{coreauth.AttributeAuthKind: coreauth.AuthKindOAuth, "priority": spec.priority},
			Metadata:   map[string]any{"access_token": "test-token"},
		})
		if errRegister != nil {
			t.Fatal(errRegister)
		}
		indexes[authID] = registered.Index
	}

	fallback := config.ClaudeFallbackConfig{Enabled: true, Mode: "auto", Mappings: []config.ClaudeFallbackMapping{{From: "claude-opus-*", To: mapped}}}
	manager.SetConfig(&config.Config{
		SDKConfig: config.SDKConfig{APIKeys: []string{keyA, keyB, keyShared}},
		Routing: config.RoutingConfig{
			// The shared profile: no preference, priority tier first, no sticky.
			Selection:      config.SelectionConfig{Primary: config.SelectionPriority, Tiebreak: config.SelectionFillFirst},
			ClaudeFallback: fallback,
			KeyProfiles: map[string]*config.RoutingProfile{
				keyA: {
					PreferredAccounts: map[string]string{"claude": indexes[id("claude-1")], "codex": indexes[id("codex-2")]},
					Selection:         config.SelectionConfig{Primary: config.SelectionPriority, Tiebreak: config.SelectionFillFirst},
					ClaudeFallback:    fallback,
				},
				keyB: {
					SessionAffinity:   true,
					PreferredAccounts: map[string]string{"claude": indexes[id("claude-2")]},
					Selection:         config.SelectionConfig{Primary: config.SelectionPriority, Tiebreak: config.SelectionRoundRobin},
					ClaudeFallback:    fallback,
				},
			},
		},
	})

	execute := func(t *testing.T, key string) {
		t.Helper()
		opts := coreexecutor.Options{Headers: http.Header{"X-Session-Id": []string{session + "-" + key}}}
		if _, errExecute := manager.Execute(keyRequestContext(key), []string{"claude"}, coreexecutor.Request{Model: model}, opts); errExecute != nil {
			t.Fatalf("Execute(%s) error = %v", key, errExecute)
		}
	}
	stages := func(provider string) []any {
		var out []any
		for _, entry := range hook.AllEntries() {
			if entry.Message == "Routing profile selection" && entry.Data["provider"] == provider {
				out = append(out, entry.Data["routing_stage"])
			}
		}
		return out
	}

	t.Run("key A stays on its preferred credential without sticky", func(t *testing.T) {
		hook.Reset()
		for range 3 {
			execute(t, keyA)
		}
		calls, _ := claude.takeCalls()
		want := []string{id("claude-1"), id("claude-1"), id("claude-1")}
		if !reflect.DeepEqual(calls, want) {
			t.Fatalf("key A claude calls = %v, want %v", calls, want)
		}
		if got := stages("claude"); !reflect.DeepEqual(got, []any{"preferred", "preferred", "preferred"}) {
			t.Fatalf("key A stages = %v, want preferred every time", got)
		}
		if authID, status := affinity.LookupAffinity("claude", model, session+"-"+keyA); status == "bound" {
			t.Fatalf("sticky-off key A left a binding to %s", authID)
		}
	})

	t.Run("key B binds and keeps its preferred credential", func(t *testing.T) {
		hook.Reset()
		for range 3 {
			execute(t, keyB)
		}
		calls, _ := claude.takeCalls()
		want := []string{id("claude-2"), id("claude-2"), id("claude-2")}
		if !reflect.DeepEqual(calls, want) {
			t.Fatalf("key B claude calls = %v, want %v", calls, want)
		}
		if got := stages("claude"); !reflect.DeepEqual(got, []any{"preferred", "sticky", "sticky"}) {
			t.Fatalf("key B stages = %v, want preferred then sticky", got)
		}
		if authID, status := affinity.LookupAffinity("claude", model, session+"-"+keyB); status != "bound" || authID != id("claude-2") {
			t.Fatalf("key B binding = %s/%s, want bound to claude-2", authID, status)
		}
	})

	t.Run("a key without a profile uses the shared profile", func(t *testing.T) {
		hook.Reset()
		for _, key := range []string{keyShared, ""} {
			execute(t, key)
		}
		calls, _ := claude.takeCalls()
		want := []string{id("claude-2"), id("claude-2")}
		if !reflect.DeepEqual(calls, want) {
			t.Fatalf("shared claude calls = %v, want the priority tier %v", calls, want)
		}
		if got := stages("claude"); len(got) != 0 {
			t.Fatalf("shared profile logged profile selections: %v", got)
		}
		if authID, status := affinity.LookupAffinity("claude", model, session+"-"+keyShared); status == "bound" {
			t.Fatalf("shared profile without sticky left a binding to %s", authID)
		}
	})

	claude.setFailing(id("claude-1"), id("claude-2"))

	t.Run("key A falls back to its preferred Codex credential", func(t *testing.T) {
		hook.Reset()
		execute(t, keyA)
		claudeCalls, _ := claude.takeCalls()
		if len(claudeCalls) == 0 {
			t.Fatal("key A did not try Claude before the fallback")
		}
		calls, models := codex.takeCalls()
		if !reflect.DeepEqual(calls, []string{id("codex-2")}) || !reflect.DeepEqual(models, []string{mapped}) {
			t.Fatalf("key A codex calls = %v models = %v, want [codex-2] with %s", calls, models, mapped)
		}
		if got := stages("codex"); !reflect.DeepEqual(got, []any{"fallback"}) {
			t.Fatalf("key A codex stages = %v, want fallback", got)
		}
	})

	t.Run("the shared profile falls back with the shared selection", func(t *testing.T) {
		execute(t, keyShared)
		claude.takeCalls()
		calls, models := codex.takeCalls()
		if !reflect.DeepEqual(calls, []string{id("codex-1")}) || !reflect.DeepEqual(models, []string{mapped}) {
			t.Fatalf("shared codex calls = %v models = %v, want [codex-1] with %s", calls, models, mapped)
		}
	})
}
