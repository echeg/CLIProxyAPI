package auth

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

const (
	routingTestKeyA = "sk-client-key-a"
	routingTestKeyB = "sk-client-key-b"
)

// ginContextWithKey mirrors the request context built by the API handlers: the
// gin context is stored under "gin" and carries the authenticated client key.
func ginContextWithKey(key string) context.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	if key != "" {
		c.Set("userApiKey", key)
	}
	return context.WithValue(context.Background(), "gin", c)
}

func routingProfileTestConfig() *internalconfig.Config {
	return &internalconfig.Config{
		SDKConfig: internalconfig.SDKConfig{APIKeys: []string{routingTestKeyA, routingTestKeyB}},
		Routing: internalconfig.RoutingConfig{
			SessionAffinity:   true,
			PreferredAccounts: map[string]string{"claude": "shared-claude"},
			Selection:         internalconfig.SelectionConfig{Primary: "priority", Tiebreak: "fill-first"},
			ClaudeFallback:    internalconfig.ClaudeFallbackConfig{Enabled: true, Mode: "auto", Mappings: []internalconfig.ClaudeFallbackMapping{{From: "claude-opus-*", To: "gpt-6-astra"}}},
			KeyProfiles: map[string]*internalconfig.RoutingProfile{
				routingTestKeyA: {
					PreferredAccounts: map[string]string{"claude": "key-a-claude", "codex": "key-a-codex"},
					Selection:         internalconfig.SelectionConfig{Primary: "earliest-reset", Tiebreak: "round-robin"},
					ClaudeFallback:    internalconfig.ClaudeFallbackConfig{Enabled: true, Mode: "always", Mappings: []internalconfig.ClaudeFallbackMapping{{From: "claude-opus-*", To: "gpt-6-sol"}}},
				},
			},
		},
	}
}

func TestResolveRoutingProfile(t *testing.T) {
	m := NewManager(nil, &RoundRobinSelector{}, nil)
	m.SetConfig(routingProfileTestConfig())

	keyA := m.resolveRoutingProfile(ginContextWithKey(routingTestKeyA))
	if keyA == nil || !keyA.fromKey || keyA.keyHash != internalconfig.KeyHashPrefix(routingTestKeyA) {
		t.Fatalf("key A profile = %+v", keyA)
	}
	if keyA.sticky || keyA.preferred["claude"] != "key-a-claude" || keyA.selection.Primary != "earliest-reset" || keyA.fallback.Mode != "always" {
		t.Fatalf("key A profile inherited shared fields: %+v", keyA)
	}

	keyB := m.resolveRoutingProfile(ginContextWithKey(routingTestKeyB))
	if keyB == nil || keyB.fromKey || keyB.keyHash != internalconfig.KeyHashPrefix(routingTestKeyB) {
		t.Fatalf("key B profile = %+v", keyB)
	}
	if !keyB.sticky || keyB.preferred["claude"] != "shared-claude" || keyB.selection.Primary != "priority" || keyB.fallback.Mode != "auto" {
		t.Fatalf("key B did not resolve the shared profile: %+v", keyB)
	}

	for name, ctx := range map[string]context.Context{"no gin": context.Background(), "no key": ginContextWithKey(""), "nil": nil} {
		shared := m.resolveRoutingProfile(ctx)
		if shared == nil || shared.fromKey || shared.keyHash != "" || shared.preferred["claude"] != "shared-claude" {
			t.Fatalf("%s: profile = %+v", name, shared)
		}
	}

	cfg := routingProfileTestConfig()
	cfg.Home.Enabled = true
	m.SetConfig(cfg)
	if profile := m.resolveRoutingProfile(ginContextWithKey(routingTestKeyA)); profile != nil {
		t.Fatalf("Home resolved %+v", profile)
	}
	if ctx := m.ensureRoutingProfile(context.Background()); routingProfileFromContext(ctx) != nil {
		t.Fatal("Home injected a profile")
	}
}

func TestEnsureRoutingProfileKeepsExistingProfile(t *testing.T) {
	m := NewManager(nil, &RoundRobinSelector{}, nil)
	m.SetConfig(routingProfileTestConfig())
	outer := m.ensureRoutingProfile(ginContextWithKey(routingTestKeyA))
	profile := routingProfileFromContext(outer)
	if profile == nil || !profile.fromKey {
		t.Fatalf("outer profile = %+v", profile)
	}
	if again := routingProfileFromContext(m.ensureRoutingProfile(outer)); again != profile {
		t.Fatal("ensureRoutingProfile replaced the existing profile")
	}
}

func TestResolveRoutingProfilePluginSchedulerUsesSharedProfile(t *testing.T) {
	selector := &RoundRobinSelector{}
	m, accounts, model := preferredAccountTestManager(t, "codex", selector)
	cfg := routingProfileTestConfig()
	cfg.Routing.PreferredAccounts = map[string]string{"codex": accounts[2].Index}
	m.SetConfig(cfg)
	m.SetPluginScheduler(&fakePluginScheduler{})

	profile := m.resolveRoutingProfile(ginContextWithKey(routingTestKeyA))
	if profile == nil || profile.fromKey || profile.preferred["codex"] != accounts[2].Index {
		t.Fatalf("plugin scheduler profile = %+v", profile)
	}

	selectorAuths := func(ctx context.Context) []*Auth {
		m.mu.RLock()
		defer m.mu.RUnlock()
		candidates := make([]*Auth, 0, len(accounts))
		for _, account := range accounts {
			candidates = append(candidates, m.auths[account.ID])
		}
		_, selected, _, errAvailable := m.availableAuthsForSelector(ctx, selector, candidates, "codex", model, cliproxyexecutor.Options{}, time.Now(), pluginSchedulerWantsAcrossPriorities(m.pluginScheduler), true)
		if errAvailable != nil {
			t.Fatal(errAvailable)
		}
		return selected
	}
	// The shared preference keeps every tier in play so the preferred low-priority
	// credential stays reachable; without it only the top tier is offered.
	if got := selectorAuths(withRoutingProfile(context.Background(), profile)); len(got) != len(accounts) {
		t.Fatalf("preference narrowed to %d candidates, want %d", len(got), len(accounts))
	}
	if got := selectorAuths(withRoutingProfile(context.Background(), &routingProfile{})); len(got) != 1 {
		t.Fatalf("no preference offered %d candidates, want 1", len(got))
	}
}

// profileCapturingExecutor records the routing profile seen by each call.
type profileCapturingExecutor struct {
	ProviderExecutor
	provider string
	err      error
	seen     *[]*routingProfile
	models   *[]string
}

func (e *profileCapturingExecutor) Identifier() string { return e.provider }
func (e *profileCapturingExecutor) Execute(ctx context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	*e.seen = append(*e.seen, routingProfileFromContext(ctx))
	*e.models = append(*e.models, e.provider+":"+req.Model)
	return cliproxyexecutor.Response{}, e.err
}

func TestExecuteUsesKeyProfileForFallback(t *testing.T) {
	withQuotaCooldownEnabled(t)
	for _, mode := range []string{"auto", "always"} {
		t.Run(mode, func(t *testing.T) {
			m := NewManager(nil, &FillFirstSelector{}, nil)
			m.SetRetryConfig(0, 0, 0)
			cfg := routingProfileTestConfig()
			cfg.Routing.PreferredAccounts = nil
			cfg.Routing.ClaudeFallback = internalconfig.ClaudeFallbackConfig{}
			keyProfile := *cfg.Routing.KeyProfiles[routingTestKeyA]
			keyProfile.PreferredAccounts = nil
			keyProfile.ClaudeFallback.Mode = mode
			cfg.Routing.KeyProfiles[routingTestKeyA] = &keyProfile
			m.SetConfig(cfg)

			var seen []*routingProfile
			var models []string
			m.RegisterExecutor(&profileCapturingExecutor{provider: "claude", err: &Error{HTTPStatus: 429}, seen: &seen, models: &models})
			m.RegisterExecutor(&profileCapturingExecutor{provider: "codex", seen: &seen, models: &models})
			for i, provider := range []string{"claude", "codex"} {
				id := t.Name() + string(rune('a'+i))
				model := map[string]string{"claude": "claude-opus-5", "codex": "gpt-6-sol"}[provider]
				registry.GetGlobalRegistry().RegisterClient(id, provider, []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
				if _, errRegister := m.Register(context.Background(), &Auth{ID: id, Provider: provider, Status: StatusActive, Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth}}); errRegister != nil {
					t.Fatal(errRegister)
				}
			}

			if _, err := m.Execute(ginContextWithKey(routingTestKeyA), []string{"claude"}, cliproxyexecutor.Request{Model: "claude-opus-5"}, cliproxyexecutor.Options{}); err != nil {
				t.Fatal(err)
			}
			if len(models) == 0 || models[len(models)-1] != "codex:gpt-6-sol" {
				t.Fatalf("calls = %v, want the key A target gpt-6-sol last", models)
			}
			if mode == "always" && len(models) != 1 {
				t.Fatalf("always mode called the primary: %v", models)
			}
			for i, profile := range seen {
				if profile == nil || profile != seen[0] || !profile.fromKey {
					t.Fatalf("call %d (%s) saw profile %p, want the key A profile %p", i, models[i], profile, seen[0])
				}
			}

			// Key B has no profile and the shared fallback is disabled.
			seen, models = nil, nil
			if _, err := m.Execute(ginContextWithKey(routingTestKeyB), []string{"claude"}, cliproxyexecutor.Request{Model: "claude-opus-5"}, cliproxyexecutor.Options{}); err == nil {
				t.Fatal("key B fell back without a shared fallback")
			}
			for _, call := range models {
				if strings.HasPrefix(call, "codex:") {
					t.Fatalf("key B reached Codex: %v", models)
				}
			}
		})
	}
}

// newStandardLogHook attaches a capture hook to the standard logger and restores
// the logger's previous hooks when the test ends.
func newStandardLogHook(t *testing.T) *logtest.Hook {
	t.Helper()
	logger := log.StandardLogger()
	saved := make(log.LevelHooks)
	for level, hooks := range logger.Hooks {
		saved[level] = append([]log.Hook(nil), hooks...)
	}
	hook := logtest.NewLocal(logger)
	t.Cleanup(func() { logger.ReplaceHooks(saved) })
	return hook
}

func TestLogRoutingProfileSelection(t *testing.T) {
	hook := newStandardLogHook(t)
	auth := &Auth{ID: "claude-1", Provider: "Claude", Index: "idx-1"}

	logRoutingProfileSelection(context.Background(), "pipeline", auth)
	logRoutingProfileSelection(withRoutingProfile(context.Background(), &routingProfile{keyHash: "shared"}), "pipeline", auth)
	if entries := routingSelectionEntries(t, hook); len(entries) != 0 {
		t.Fatalf("logged without a key profile: %d entries", len(entries))
	}

	m := NewManager(nil, &RoundRobinSelector{}, nil)
	m.SetConfig(routingProfileTestConfig())
	ctx := m.ensureRoutingProfile(ginContextWithKey(routingTestKeyA))
	logRoutingProfileSelection(ctx, "preferred", auth)
	entries := routingSelectionEntries(t, hook)
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	entry := entries[0]
	want := map[string]any{"routing_key": internalconfig.KeyHashPrefix(routingTestKeyA), "routing_stage": "preferred", "provider": "claude", "auth_index": "idx-1"}
	for field, value := range want {
		if entry.Data[field] != value {
			t.Fatalf("field %s = %v, want %v", field, entry.Data[field], value)
		}
	}
	line, errLine := entry.String()
	if errLine != nil {
		t.Fatal(errLine)
	}
	if strings.Contains(line, routingTestKeyA) {
		t.Fatalf("log line leaked the raw key: %s", line)
	}
}
