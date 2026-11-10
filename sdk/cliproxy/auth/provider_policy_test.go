package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const providerPolicyTestModel = "policy-model-5"

type providerPolicyTestAuth struct {
	id, provider, kind, index string
}

func providerPolicyAuth(spec providerPolicyTestAuth) *Auth {
	auth := &Auth{ID: spec.id, Index: spec.index, Provider: spec.provider, Status: StatusActive, Attributes: map[string]string{}}
	switch spec.kind {
	case AuthKindOAuth:
		auth.Attributes[AttributeAuthKind] = AuthKindOAuth
	case AuthKindAPIKey:
		auth.Attributes[AttributeAuthKind] = AuthKindAPIKey
		auth.Attributes[AttributeAPIKey] = "secret-" + spec.id
	}
	return auth
}

// providerPolicyTestManager registers the credentials under the production
// pipeline selector. Every provider serves providerPolicyTestModel; the
// returned slice records the ID of every credential an executor ran on.
func providerPolicyTestManager(t *testing.T, cfg *internalconfig.Config, specs ...providerPolicyTestAuth) (*Manager, *[]string) {
	t.Helper()
	m := NewManager(nil, nil, nil)
	m.SetSelector(NewPipelineSelector(m.SharedSelection))
	m.SetRetryConfig(0, 0, 0)
	if cfg == nil {
		cfg = &internalconfig.Config{}
	}
	m.SetConfig(cfg)
	var calls []string
	for _, provider := range []string{"claude", "codex", "gemini"} {
		m.RegisterExecutor(&claudeFallbackExecutor{provider: provider, run: func(a *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			calls = append(calls, a.ID)
			return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
		}})
	}
	for _, spec := range specs {
		registry.GetGlobalRegistry().RegisterClient(spec.id, spec.provider, []*registry.ModelInfo{{ID: providerPolicyTestModel}})
		id := spec.id
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		if _, errRegister := m.Register(WithSkipPersist(context.Background()), providerPolicyAuth(spec)); errRegister != nil {
			t.Fatal(errRegister)
		}
	}
	return m, &calls
}

// cooldownRetryAfterHeader returns the Retry-After header a model cooldown error
// sends to the client, or "" when err is not one.
func cooldownRetryAfterHeader(err error) string {
	var cooldownErr *modelCooldownError
	if !errors.As(err, &cooldownErr) || cooldownErr == nil {
		return ""
	}
	return cooldownErr.Headers().Get("Retry-After")
}

func providerPolicyConfig(claude, codex internalconfig.ProviderRoutingConfig) *internalconfig.Config {
	return &internalconfig.Config{Routing: internalconfig.RoutingConfig{Providers: internalconfig.ProvidersRoutingConfig{Claude: claude, Codex: codex}}}
}

func apiMode(index string) internalconfig.ProviderRoutingConfig {
	return internalconfig.ProviderRoutingConfig{Mode: internalconfig.ProviderModeAPI, APICredential: index}
}

func TestProviderPolicyAllows(t *testing.T) {
	subscription := &routingProfile{providerPolicy: true, providers: internalconfig.RoutingProfile{}.EffectiveProviders()}
	api := &routingProfile{providerPolicy: true, providers: internalconfig.ProvidersRoutingConfig{
		Claude: apiMode("claude-api"), Codex: apiMode("codex-api"),
	}}
	unsupported := &routingProfile{providerPolicy: false}
	for _, tc := range []struct {
		name    string
		profile *routingProfile
		auth    providerPolicyTestAuth
		want    bool
	}{
		{"claude subscription oauth", subscription, providerPolicyTestAuth{"a", "claude", AuthKindOAuth, "i1"}, true},
		{"claude subscription api key", subscription, providerPolicyTestAuth{"a", "claude", AuthKindAPIKey, "claude-api"}, false},
		{"codex subscription oauth", subscription, providerPolicyTestAuth{"a", "codex", AuthKindOAuth, "i1"}, true},
		{"codex subscription api key", subscription, providerPolicyTestAuth{"a", "codex", AuthKindAPIKey, "codex-api"}, false},
		{"subscription unknown kind", subscription, providerPolicyTestAuth{"a", "claude", "", "i1"}, true},
		{"claude api named key", api, providerPolicyTestAuth{"a", "Claude", AuthKindAPIKey, "claude-api"}, true},
		{"claude api other key", api, providerPolicyTestAuth{"a", "claude", AuthKindAPIKey, "other"}, false},
		{"claude api oauth", api, providerPolicyTestAuth{"a", "claude", AuthKindOAuth, "claude-api"}, false},
		{"codex api named key", api, providerPolicyTestAuth{"a", "codex", AuthKindAPIKey, "codex-api"}, true},
		{"codex api claude index", api, providerPolicyTestAuth{"a", "codex", AuthKindAPIKey, "claude-api"}, false},
		{"codex api oauth", api, providerPolicyTestAuth{"a", "codex", AuthKindOAuth, "i1"}, false},
		{"other provider api key", subscription, providerPolicyTestAuth{"a", "gemini", AuthKindAPIKey, "g"}, true},
		{"other provider under api", api, providerPolicyTestAuth{"a", "gemini", AuthKindOAuth, "g"}, true},
		{"unsupported policy", unsupported, providerPolicyTestAuth{"a", "claude", AuthKindAPIKey, "x"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := providerPolicyAllows(tc.profile, providerPolicyAuth(tc.auth)); got != tc.want {
				t.Fatalf("providerPolicyAllows = %t, want %t", got, tc.want)
			}
			eligibility := authSelectionEligibilityForRequest(withRoutingProfile(context.Background(), tc.profile), cliproxyexecutor.Options{})
			if got := eligibility.allows(providerPolicyAuth(tc.auth)); got != tc.want {
				t.Fatalf("eligibility.allows = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestProviderPolicyEligibilityExemptions(t *testing.T) {
	profile := &routingProfile{providerPolicy: true, providers: internalconfig.RoutingProfile{}.EffectiveProviders()}
	ctx := withRoutingProfile(context.Background(), profile)
	apiKey := providerPolicyAuth(providerPolicyTestAuth{"key", "claude", AuthKindAPIKey, "k"})
	pinned := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: "key"}}
	for _, tc := range []struct {
		name       string
		ctx        context.Context
		opts       cliproxyexecutor.Options
		wantPolicy bool
	}{
		{"profile", ctx, cliproxyexecutor.Options{}, true},
		{"no profile", context.Background(), cliproxyexecutor.Options{}, false},
		{"nil context", nil, cliproxyexecutor.Options{}, false},
		{"pinned", ctx, pinned, false},
		{"required kind", withRequiredAuthKind(ctx, AuthKindAPIKey), cliproxyexecutor.Options{}, false},
		{"credential policy", withCredentialPolicy(ctx, CredentialPolicyCodexAlphaSearchV1), cliproxyexecutor.Options{}, false},
		{"unsupported", withRoutingProfile(context.Background(), &routingProfile{providers: profile.providers}), cliproxyexecutor.Options{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eligibility := authSelectionEligibilityForRequest(tc.ctx, tc.opts)
			if (eligibility.providerPolicy != nil) != tc.wantPolicy {
				t.Fatalf("providerPolicy = %+v, want set=%t", eligibility.providerPolicy, tc.wantPolicy)
			}
			if tc.name == "pinned" || tc.name == "no profile" {
				if !eligibility.allows(apiKey) {
					t.Fatal("exempt request filtered the API key")
				}
			}
		})
	}
}

func TestResolveRoutingProfileProviderPolicy(t *testing.T) {
	cfg := routingProfileTestConfig()
	cfg.Routing.KeyProfiles[routingTestKeyA].Providers.Codex = apiMode("codex-api")
	m := NewManager(nil, &RoundRobinSelector{}, nil)
	m.SetConfig(cfg)

	shared := m.resolveRoutingProfile(context.Background())
	if shared == nil || !shared.providerPolicy || shared.providers.Claude.Mode != internalconfig.ProviderModeSubscription || shared.providers.Claude.Fallback.Target != internalconfig.ProviderFallbackCodex {
		t.Fatalf("shared profile = %+v", shared)
	}
	keyA := m.resolveRoutingProfile(ginContextWithKey(routingTestKeyA))
	if keyA == nil || !keyA.providerPolicy || keyA.providers.Codex.Mode != internalconfig.ProviderModeAPI || keyA.providers.Codex.APICredential != "codex-api" || keyA.providers.Claude.Mode != internalconfig.ProviderModeSubscription {
		t.Fatalf("key profile = %+v", keyA)
	}

	m.SetPluginScheduler(&fakePluginScheduler{})
	if plugin := m.resolveRoutingProfile(ginContextWithKey(routingTestKeyA)); plugin == nil || plugin.providerPolicy {
		t.Fatalf("plugin scheduler profile enforces the provider policy: %+v", plugin)
	}

	// Home owns credential choice: no profile, so eligibility carries no policy.
	home := NewManager(nil, &RoundRobinSelector{}, nil)
	homeCfg := routingProfileTestConfig()
	homeCfg.Home.Enabled = true
	home.SetConfig(homeCfg)
	homeCtx := home.ensureRoutingProfile(ginContextWithKey(routingTestKeyA))
	if policy := authSelectionEligibilityForRequest(homeCtx, cliproxyexecutor.Options{}).providerPolicy; policy != nil {
		t.Fatalf("Home request carries a provider policy: %+v", policy)
	}
}

func TestProviderPolicyExecutionSelection(t *testing.T) {
	oauthClaude := providerPolicyTestAuth{"pp-claude-oauth", "claude", AuthKindOAuth, "claude-oauth"}
	keyClaude := providerPolicyTestAuth{"pp-claude-key", "claude", AuthKindAPIKey, "claude-key"}
	otherKeyClaude := providerPolicyTestAuth{"pp-claude-key-2", "claude", AuthKindAPIKey, "claude-key-2"}
	oauthCodex := providerPolicyTestAuth{"pp-codex-oauth", "codex", AuthKindOAuth, "codex-oauth"}
	keyCodex := providerPolicyTestAuth{"pp-codex-key", "codex", AuthKindAPIKey, "codex-key"}
	keyGemini := providerPolicyTestAuth{"pp-gemini-key", "gemini", AuthKindAPIKey, "gemini-key"}
	pin := func(id string) cliproxyexecutor.Options {
		return cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: id}}
	}
	for _, tc := range []struct {
		name      string
		cfg       *internalconfig.Config
		auths     []providerPolicyTestAuth
		providers []string
		opts      cliproxyexecutor.Options
		plugin    bool
		want      string
	}{
		{"default claude uses subscription", nil, []providerPolicyTestAuth{keyClaude, oauthClaude, otherKeyClaude}, []string{"claude"}, cliproxyexecutor.Options{}, false, oauthClaude.id},
		{"default codex uses subscription", nil, []providerPolicyTestAuth{keyCodex, oauthCodex}, []string{"codex"}, cliproxyexecutor.Options{}, false, oauthCodex.id},
		{"claude api mode uses its key", providerPolicyConfig(apiMode(keyClaude.index), internalconfig.ProviderRoutingConfig{}), []providerPolicyTestAuth{oauthClaude, otherKeyClaude, keyClaude}, []string{"claude"}, cliproxyexecutor.Options{}, false, keyClaude.id},
		{"codex api mode uses its key", providerPolicyConfig(internalconfig.ProviderRoutingConfig{}, apiMode(keyCodex.index)), []providerPolicyTestAuth{oauthCodex, keyCodex}, []string{"codex"}, cliproxyexecutor.Options{}, false, keyCodex.id},
		{"mixed pool skips claude key", nil, []providerPolicyTestAuth{keyClaude, keyGemini}, []string{"claude", "gemini"}, cliproxyexecutor.Options{}, false, keyGemini.id},
		{"other provider key untouched", nil, []providerPolicyTestAuth{keyGemini}, []string{"gemini"}, cliproxyexecutor.Options{}, false, keyGemini.id},
		{"pinned api key is exempt", nil, []providerPolicyTestAuth{oauthClaude, keyClaude}, []string{"claude"}, pin(keyClaude.id), false, keyClaude.id},
		{"plugin scheduler keeps api keys", nil, []providerPolicyTestAuth{keyClaude}, []string{"claude"}, cliproxyexecutor.Options{}, true, keyClaude.id},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, calls := providerPolicyTestManager(t, tc.cfg, tc.auths...)
			if tc.plugin {
				m.SetPluginScheduler(&fakePluginScheduler{})
			}
			for i := 0; i < 4; i++ {
				if _, errExecute := m.Execute(context.Background(), tc.providers, cliproxyexecutor.Request{Model: providerPolicyTestModel}, tc.opts); errExecute != nil {
					t.Fatalf("Execute #%d: %v", i, errExecute)
				}
			}
			for _, id := range *calls {
				if id != tc.want {
					t.Fatalf("calls = %v, want only %s", *calls, tc.want)
				}
			}
		})
	}
}

func TestProviderPolicyExecutionErrors(t *testing.T) {
	withQuotaCooldownEnabled(t)
	oauthClaude := providerPolicyTestAuth{"pe-claude-oauth", "claude", AuthKindOAuth, "claude-oauth"}
	keyClaude := providerPolicyTestAuth{"pe-claude-key", "claude", AuthKindAPIKey, "claude-key"}
	keyCodex := providerPolicyTestAuth{"pe-codex-key", "codex", AuthKindAPIKey, "codex-key"}
	retry := time.Hour

	t.Run("subscription with only api keys", func(t *testing.T) {
		for _, provider := range []string{"claude", "codex"} {
			spec := keyClaude
			if provider == "codex" {
				spec = keyCodex
			}
			m, calls := providerPolicyTestManager(t, nil, spec)
			_, errExecute := m.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: providerPolicyTestModel}, cliproxyexecutor.Options{})
			var authErr *Error
			if !errors.As(errExecute, &authErr) || authErr.Code != "auth_not_found" || !strings.Contains(authErr.Message, "no subscription credentials for "+provider) || !strings.Contains(authErr.Message, "providers."+provider+".mode: api") {
				t.Fatalf("%s: err = %v, want the subscription policy error", provider, errExecute)
			}
			if len(*calls) != 0 {
				t.Fatalf("%s: executor ran on %v", provider, *calls)
			}
		}
	})

	t.Run("api mode with unknown credential", func(t *testing.T) {
		m, calls := providerPolicyTestManager(t, providerPolicyConfig(apiMode("missing-index"), internalconfig.ProviderRoutingConfig{}), oauthClaude, keyClaude)
		_, errExecute := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: providerPolicyTestModel}, cliproxyexecutor.Options{})
		var authErr *Error
		if !errors.As(errExecute, &authErr) || authErr.Code != "auth_not_found" || !strings.Contains(authErr.Message, `providers.claude.api-credential "missing-index"`) {
			t.Fatalf("err = %v, want the api-credential error", errExecute)
		}
		if strings.Contains(authErr.Message, "secret-") {
			t.Fatalf("error leaks a credential secret: %q", authErr.Message)
		}
		if len(*calls) != 0 {
			t.Fatalf("executor ran on %v", *calls)
		}
	})

	t.Run("api mode with cooling key", func(t *testing.T) {
		m, calls := providerPolicyTestManager(t, providerPolicyConfig(apiMode(keyClaude.index), internalconfig.ProviderRoutingConfig{}), oauthClaude, keyClaude)
		m.MarkResult(context.Background(), Result{AuthID: keyClaude.id, Provider: "claude", Model: providerPolicyTestModel, Error: &Error{HTTPStatus: http.StatusTooManyRequests}, RetryAfter: &retry})
		_, errExecute := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: providerPolicyTestModel}, cliproxyexecutor.Options{})
		if statusCodeFromError(errExecute) != http.StatusTooManyRequests || cooldownRetryAfterHeader(errExecute) == "" {
			t.Fatalf("err = %v (status %d), want a cooldown error with Retry-After", errExecute, statusCodeFromError(errExecute))
		}
		if len(*calls) != 0 {
			t.Fatalf("a subscription or another key was substituted: %v", *calls)
		}
	})

	t.Run("subscription with cooling oauth keeps cooldown error", func(t *testing.T) {
		m, calls := providerPolicyTestManager(t, nil, oauthClaude, keyClaude)
		m.MarkResult(context.Background(), Result{AuthID: oauthClaude.id, Provider: "claude", Model: providerPolicyTestModel, Error: &Error{HTTPStatus: http.StatusTooManyRequests}, RetryAfter: &retry})
		_, errExecute := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: providerPolicyTestModel}, cliproxyexecutor.Options{})
		if statusCodeFromError(errExecute) != http.StatusTooManyRequests || cooldownRetryAfterHeader(errExecute) == "" {
			t.Fatalf("err = %v, want the OAuth cooldown error", errExecute)
		}
		if len(*calls) != 0 {
			t.Fatalf("the API key was selected: %v", *calls)
		}
	})
}

// The retry and cooldown-wait paths count only credentials the provider policy
// allows, so a cooling API key never schedules a retry round for a
// subscription-mode request.
func TestProviderPolicyRetryPathsCountEligibleCredentials(t *testing.T) {
	withQuotaCooldownEnabled(t)
	keyClaude := providerPolicyTestAuth{"pr-claude-key", "claude", AuthKindAPIKey, "claude-key"}
	m, _ := providerPolicyTestManager(t, nil, keyClaude)
	retry := time.Minute
	m.MarkResult(context.Background(), Result{AuthID: keyClaude.id, Provider: "claude", Model: providerPolicyTestModel, Error: &Error{HTTPStatus: http.StatusTooManyRequests}, RetryAfter: &retry})

	profiled := m.ensureRoutingProfile(context.Background())
	withPolicy := authSelectionEligibilityForRequest(profiled, cliproxyexecutor.Options{})
	if withPolicy.providerPolicy == nil {
		t.Fatal("default profile carries no provider policy")
	}
	if _, found := m.closestCooldownWaitWithAttemptedForRequest(profiled, []string{"claude"}, providerPolicyTestModel, 0, withPolicy, "", 1, http.StatusTooManyRequests, nil); found {
		t.Fatal("cooldown wait counted a policy-excluded API key")
	}
	if m.retryAllowedForRequest(profiled, 0, []string{"claude"}, providerPolicyTestModel, withPolicy, "", 1) {
		t.Fatal("retry allowed for a policy-excluded API key")
	}

	withoutPolicy := authSelectionEligibilityForRequest(context.Background(), cliproxyexecutor.Options{})
	if _, found := m.closestCooldownWaitWithAttemptedForRequest(context.Background(), []string{"claude"}, providerPolicyTestModel, 0, withoutPolicy, "", 1, http.StatusTooManyRequests, nil); !found {
		t.Fatal("control: cooldown wait ignored the cooling API key without a policy")
	}
}

func TestLogRoutingProfileSelectionAPIStage(t *testing.T) {
	hook := newStandardLogHook(t)
	profile := &routingProfile{keyHash: "3f2a9c1b7d4e", fromKey: true, providerPolicy: true, providers: internalconfig.ProvidersRoutingConfig{
		Claude: apiMode("claude-key"), Codex: internalconfig.ProviderRoutingConfig{Mode: internalconfig.ProviderModeSubscription},
	}}
	ctx := withRoutingProfile(context.Background(), profile)
	logRoutingProfileSelection(ctx, routingStagePipeline, providerPolicyAuth(providerPolicyTestAuth{"k", "claude", AuthKindAPIKey, "claude-key"}))
	logRoutingProfileSelection(ctx, routingStageSticky, providerPolicyAuth(providerPolicyTestAuth{"k", "claude", AuthKindAPIKey, "claude-key"}))
	logRoutingProfileSelection(ctx, routingStagePipeline, providerPolicyAuth(providerPolicyTestAuth{"o", "codex", AuthKindOAuth, "codex-oauth"}))
	entries := hook.AllEntries()
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(entries))
	}
	for i, want := range []string{routingStageAPI, routingStageAPI, routingStagePipeline} {
		if got := entries[i].Data["routing_stage"]; got != want {
			t.Fatalf("entry %d routing_stage = %v, want %s", i, got, want)
		}
		for _, value := range entries[i].Data {
			if s, ok := value.(string); ok && strings.Contains(s, "secret-") {
				t.Fatalf("entry %d leaks a credential secret: %v", i, entries[i].Data)
			}
		}
	}
}
