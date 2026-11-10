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
)

const apiFallbackModel = "api-fallback-model-5"

// apiFallbackFixture runs one provider with two subscriptions and one API key.
// Subscriptions return primaryErr; the API key returns apiErr. calls records the
// credential kind of every executor call.
type apiFallbackFixture struct {
	m          *Manager
	calls      []string
	ids        []string
	primaryErr error
	apiErr     error
	keyIndex   string
	keyID      string
	oauthIDs   []string
	executors  map[string]*claudeFallbackExecutor
}

func apiFallbackPolicy(index string) internalconfig.ProviderRoutingConfig {
	return internalconfig.ProviderRoutingConfig{APICredential: index, Fallback: internalconfig.ProviderFallbackConfig{Target: internalconfig.ProviderFallbackAPI}}
}

func apiFallbackConfig(provider, index string) *internalconfig.Config {
	cfg := &internalconfig.Config{}
	if provider == "claude" {
		cfg.Routing.Providers.Claude = apiFallbackPolicy(index)
	} else {
		cfg.Routing.Providers.Codex = apiFallbackPolicy(index)
	}
	return cfg
}

// newAPIFallbackFixture registers oauthCount subscriptions and one API key of
// provider; cfg nil means target api with the fixture key.
func newAPIFallbackFixture(t *testing.T, provider string, oauthCount int, cfg *internalconfig.Config) *apiFallbackFixture {
	t.Helper()
	f := &apiFallbackFixture{keyIndex: provider + "-key-idx", keyID: t.Name() + "-" + provider + "-key", executors: map[string]*claudeFallbackExecutor{}}
	f.m = NewManager(nil, nil, nil)
	f.m.SetSelector(NewPipelineSelector(f.m.SharedSelection))
	f.m.SetRetryConfig(0, 0, 0)
	if cfg == nil {
		cfg = apiFallbackConfig(provider, f.keyIndex)
	}
	f.m.SetConfig(cfg)
	for _, name := range []string{"claude", "codex"} {
		executor := &claudeFallbackExecutor{provider: name, run: func(a *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			f.calls = append(f.calls, a.AuthKind())
			f.ids = append(f.ids, a.ID)
			if a.AuthKind() == AuthKindAPIKey {
				return cliproxyexecutor.Response{Payload: []byte("api")}, f.apiErr
			}
			return cliproxyexecutor.Response{Payload: []byte("primary")}, f.primaryErr
		}}
		f.executors[name] = executor
		f.m.RegisterExecutor(executor)
	}
	specs := []providerPolicyTestAuth{{f.keyID, provider, AuthKindAPIKey, f.keyIndex}}
	for i := 0; i < oauthCount; i++ {
		id := t.Name() + "-" + provider + "-oauth-" + string(rune('a'+i))
		f.oauthIDs = append(f.oauthIDs, id)
		specs = append(specs, providerPolicyTestAuth{id, provider, AuthKindOAuth, ""})
	}
	for _, spec := range specs {
		registry.GetGlobalRegistry().RegisterClient(spec.id, spec.provider, []*registry.ModelInfo{{ID: apiFallbackModel}})
		id := spec.id
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		if _, errRegister := f.m.Register(WithSkipPersist(context.Background()), providerPolicyAuth(spec)); errRegister != nil {
			t.Fatal(errRegister)
		}
	}
	return f
}

// runFallbackPath executes the request on one public path and drains a stream.
func runFallbackPath(ctx context.Context, m *Manager, path string, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (string, error) {
	switch path {
	case "execute":
		resp, err := m.Execute(ctx, providers, req, opts)
		return string(resp.Payload), err
	case "count":
		resp, err := m.ExecuteCount(ctx, providers, req, opts)
		return string(resp.Payload), err
	}
	opts.Stream = true
	result, err := m.ExecuteStream(ctx, providers, req, opts)
	if err != nil || result == nil {
		return "", err
	}
	var body string
	for chunk := range result.Chunks {
		body += string(chunk.Payload)
		if chunk.Err != nil {
			err = chunk.Err
		}
	}
	return body, err
}

func countKind(calls []string, kind string) int {
	n := 0
	for _, call := range calls {
		if call == kind {
			n++
		}
	}
	return n
}

func TestAPIFallbackTriggers(t *testing.T) {
	withQuotaCooldownEnabled(t)
	invalid := &Error{HTTPStatus: http.StatusBadRequest, Message: `{"error":{"type":"invalid_request_error","message":"invalid request"}}`}
	for _, provider := range []string{"claude", "codex"} {
		for _, path := range []string{"execute", "count", "stream"} {
			for _, tc := range []struct {
				name     string
				err      error
				fallback bool
			}{
				{"healthy", nil, false},
				{"401", &Error{HTTPStatus: http.StatusUnauthorized}, true},
				{"403", &Error{HTTPStatus: http.StatusForbidden}, true},
				{"408", &Error{HTTPStatus: http.StatusRequestTimeout}, true},
				{"429", &Error{HTTPStatus: http.StatusTooManyRequests}, true},
				{"500", &Error{HTTPStatus: http.StatusInternalServerError}, true},
				{"529", &Error{HTTPStatus: 529}, true},
				{"transport", io.ErrUnexpectedEOF, true},
				{"invalid request", invalid, false},
				{"canceled", context.Canceled, false},
			} {
				t.Run(provider+"/"+path+"/"+tc.name, func(t *testing.T) {
					f := newAPIFallbackFixture(t, provider, 2, nil)
					f.primaryErr = tc.err
					body, err := runFallbackPath(context.Background(), f.m, path, []string{provider}, cliproxyexecutor.Request{Model: apiFallbackModel}, cliproxyexecutor.Options{})
					if got := countKind(f.calls, AuthKindAPIKey); got != map[bool]int{false: 0, true: 1}[tc.fallback] {
						t.Fatalf("api key calls = %d in %v; err=%v", got, f.calls, err)
					}
					if tc.fallback {
						if err != nil || body != "api" || countKind(f.calls, AuthKindOAuth) != 2 || f.calls[len(f.calls)-1] != AuthKindAPIKey {
							t.Fatalf("calls=%v body=%q err=%v, want both subscriptions then the API key", f.calls, body, err)
						}
						return
					}
					if (err == nil) != (tc.err == nil) {
						t.Fatalf("err = %v, want primary outcome %v", err, tc.err)
					}
				})
			}
		}
	}
}

// With no subscription at all the primary leg fails with auth_not_found, which
// triggers the API leg.
func TestAPIFallbackWithoutSubscriptions(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, path := range []string{"execute", "count", "stream"} {
			t.Run(provider+"/"+path, func(t *testing.T) {
				f := newAPIFallbackFixture(t, provider, 0, nil)
				body, err := runFallbackPath(context.Background(), f.m, path, []string{provider}, cliproxyexecutor.Request{Model: apiFallbackModel}, cliproxyexecutor.Options{})
				if err != nil || body != "api" || !reflect.DeepEqual(f.calls, []string{AuthKindAPIKey}) {
					t.Fatalf("calls=%v body=%q err=%v", f.calls, body, err)
				}
			})
		}
	}
}

// A completed round with cooling subscriptions goes to the API key instead of
// waiting for the cooldown.
func TestAPIFallbackPrefersFallbackToCooldownWait(t *testing.T) {
	withQuotaCooldownEnabled(t)
	for _, provider := range []string{"claude", "codex"} {
		for _, path := range []string{"execute", "count", "stream"} {
			t.Run(provider+"/"+path, func(t *testing.T) {
				f := newAPIFallbackFixture(t, provider, 2, nil)
				f.primaryErr = &Error{HTTPStatus: http.StatusServiceUnavailable}
				f.m.SetRetryConfig(3, time.Minute, 0)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				body, err := runFallbackPath(ctx, f.m, path, []string{provider}, cliproxyexecutor.Request{Model: apiFallbackModel}, cliproxyexecutor.Options{})
				if err != nil || body != "api" || !reflect.DeepEqual(f.calls, []string{AuthKindOAuth, AuthKindOAuth, AuthKindAPIKey}) {
					t.Fatalf("calls=%v body=%q err=%v", f.calls, body, err)
				}
			})
		}
	}
}

// Subscriptions that are already cooling down when the request arrives send it
// to the API key without an attempt on them.
func TestAPIFallbackServesWhileSubscriptionsCool(t *testing.T) {
	withQuotaCooldownEnabled(t)
	retry := time.Hour
	for _, provider := range []string{"claude", "codex"} {
		for _, path := range []string{"execute", "count", "stream"} {
			t.Run(provider+"/"+path, func(t *testing.T) {
				f := newAPIFallbackFixture(t, provider, 2, nil)
				for _, id := range f.oauthIDs {
					f.m.MarkResult(context.Background(), Result{AuthID: id, Provider: provider, Model: apiFallbackModel, Error: &Error{HTTPStatus: http.StatusTooManyRequests}, RetryAfter: &retry})
				}
				body, err := runFallbackPath(context.Background(), f.m, path, []string{provider}, cliproxyexecutor.Request{Model: apiFallbackModel}, cliproxyexecutor.Options{})
				if err != nil || body != "api" || !reflect.DeepEqual(f.calls, []string{AuthKindAPIKey}) {
					t.Fatalf("calls=%v body=%q err=%v", f.calls, body, err)
				}
			})
		}
	}
}

func TestAPIFallbackHonorsRequestScopedStop(t *testing.T) {
	withQuotaCooldownEnabled(t)
	for _, provider := range []string{"claude", "codex"} {
		for _, path := range []string{"execute", "count", "stream"} {
			t.Run(provider+"/"+path, func(t *testing.T) {
				cfg := apiFallbackConfig(provider, provider+"-key-idx")
				cfg.OAuthRequestScopedErrors = map[string][]internalconfig.RequestScopedErrorRule{
					provider: {{Status: http.StatusTooManyRequests, Match: []string{"account_suspended"}, Action: RequestScopedActionStop}},
				}
				f := newAPIFallbackFixture(t, provider, 2, cfg)
				primaryErr := &Error{HTTPStatus: http.StatusTooManyRequests, Message: "account_suspended"}
				f.primaryErr = primaryErr
				_, err := runFallbackPath(context.Background(), f.m, path, []string{provider}, cliproxyexecutor.Request{Model: apiFallbackModel}, cliproxyexecutor.Options{})
				if !reflect.DeepEqual(f.calls, []string{AuthKindOAuth}) {
					t.Fatalf("calls=%v, want only the stopped attempt; err=%v", f.calls, err)
				}
				if err != primaryErr {
					t.Fatalf("err = %T/%v, want the original error", err, err)
				}
			})
		}
	}
}

// The API leg runs without session affinity: it neither binds the session to the
// API key nor reads a binding, and the next request of the session selects a
// subscription again.
func TestAPIFallbackLeavesSessionBindingsAlone(t *testing.T) {
	f := newAPIFallbackFixture(t, "claude", 1, nil)
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: NewPipelineSelector(f.m.SharedSelection), TTL: time.Hour})
	t.Cleanup(selector.Stop)
	f.m.SetSelector(selector)
	opts := cliproxyexecutor.Options{Headers: http.Header{"X-Session-Id": []string{"api-fallback-session"}}}
	req := cliproxyexecutor.Request{Model: apiFallbackModel}

	f.primaryErr = io.ErrUnexpectedEOF
	if body, err := runFallbackPath(context.Background(), f.m, "execute", []string{"claude"}, req, opts); err != nil || body != "api" {
		t.Fatalf("fallback request: body=%q err=%v calls=%v", body, err, f.calls)
	}
	if authID, status := selector.LookupAffinity("claude", apiFallbackModel, "api-fallback-session"); authID == f.keyID {
		t.Fatalf("session bound to the API key (status %s)", status)
	}

	f.primaryErr = nil
	f.calls, f.ids = nil, nil
	if body, err := runFallbackPath(context.Background(), f.m, "execute", []string{"claude"}, req, opts); err != nil || body != "primary" {
		t.Fatalf("next request: body=%q err=%v calls=%v", body, err, f.calls)
	}
	if !reflect.DeepEqual(f.ids, []string{f.oauthIDs[0]}) {
		t.Fatalf("next request ran on %v, want the subscription", f.ids)
	}
	if authID, _ := selector.LookupAffinity("claude", apiFallbackModel, "api-fallback-session"); authID != f.oauthIDs[0] {
		t.Fatalf("session bound to %q, want the subscription", authID)
	}

	// The derived profile of the API leg has session affinity off.
	ctx := withRoutingProfile(context.Background(), f.m.resolveRoutingProfile(context.Background()).apiFallbackProfile("claude"))
	if profile := routingProfileFromContext(ctx); profile.sticky || profile.providers.Claude.Mode != internalconfig.ProviderModeAPI || profile.providers.Claude.Fallback.Target != internalconfig.ProviderFallbackOff {
		t.Fatalf("derived profile = %+v", profile)
	}
}

// Under rebind-on: never the API leg still ignores the session's binding to the
// subscription that failed in the primary leg: a failed API key returns its own
// error, not the bound-session retry error.
func TestAPIFallbackIgnoresSessionBindingUnderRebindNever(t *testing.T) {
	prevTransient := transientErrorCooldownSeconds.Load()
	transientErrorCooldownSeconds.Store(-1)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })
	for _, path := range []string{"execute", "count", "stream"} {
		t.Run(path, func(t *testing.T) {
			f := newAPIFallbackFixture(t, "claude", 1, nil)
			selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: NewPipelineSelector(f.m.SharedSelection), TTL: time.Hour, RebindOn: SessionAffinityRebindOnNever})
			t.Cleanup(selector.Stop)
			f.m.SetSelector(selector)
			opts := cliproxyexecutor.Options{Headers: http.Header{"X-Session-Id": []string{"api-fallback-never-session"}}}
			req := cliproxyexecutor.Request{Model: apiFallbackModel}

			if body, err := runFallbackPath(context.Background(), f.m, path, []string{"claude"}, req, opts); err != nil || body != "primary" {
				t.Fatalf("binding request: body=%q err=%v", body, err)
			}
			f.primaryErr = &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "subscription down"}
			f.apiErr = &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "api key down"}
			f.calls, f.ids = nil, nil
			_, err := runFallbackPath(context.Background(), f.m, path, []string{"claude"}, req, opts)
			var authErr *Error
			if !errors.As(err, &authErr) || authErr.Code == "session_auth_retry" || authErr.Message != "api key down" {
				t.Fatalf("err = %#v, want the API key's own error", err)
			}
			if countKind(f.calls, AuthKindAPIKey) != 1 {
				t.Fatalf("calls = %v, want one API key attempt", f.calls)
			}
		})
	}
}

// When the API key cannot be selected the client gets the primary error with
// its status and Retry-After.
func TestAPIFallbackKeepsPrimaryErrorWhenKeyUnavailable(t *testing.T) {
	withQuotaCooldownEnabled(t)
	retry := time.Hour
	for _, provider := range []string{"claude", "codex"} {
		for _, unavailable := range []string{"cooling", "disabled", "unknown"} {
			for _, primary := range []string{"upstream", "cooldown"} {
				for _, path := range []string{"execute", "count", "stream"} {
					t.Run(provider+"/"+unavailable+"/"+primary+"/"+path, func(t *testing.T) {
						var cfg *internalconfig.Config
						if unavailable == "unknown" {
							cfg = apiFallbackConfig(provider, "missing-idx")
						}
						f := newAPIFallbackFixture(t, provider, 2, cfg)
						switch unavailable {
						case "cooling":
							f.m.MarkResult(context.Background(), Result{AuthID: f.keyID, Provider: provider, Model: apiFallbackModel, Error: &Error{HTTPStatus: http.StatusTooManyRequests}, RetryAfter: &retry})
						case "disabled":
							key, _ := f.m.GetByID(f.keyID)
							key.Disabled = true
							key.Status = StatusDisabled
							if _, errUpdate := f.m.Update(WithSkipPersist(context.Background()), key); errUpdate != nil {
								t.Fatal(errUpdate)
							}
						}
						wantStatus := http.StatusServiceUnavailable
						if primary == "cooldown" {
							wantStatus = http.StatusTooManyRequests
							for _, id := range f.oauthIDs {
								f.m.MarkResult(context.Background(), Result{AuthID: id, Provider: provider, Model: apiFallbackModel, Error: &Error{HTTPStatus: http.StatusTooManyRequests}, RetryAfter: &retry})
							}
						} else {
							f.primaryErr = &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "primary down"}
						}
						_, err := runFallbackPath(context.Background(), f.m, path, []string{provider}, cliproxyexecutor.Request{Model: apiFallbackModel}, cliproxyexecutor.Options{})
						if countKind(f.calls, AuthKindAPIKey) != 0 {
							t.Fatalf("API key ran: %v", f.calls)
						}
						if statusCodeFromError(err) != wantStatus {
							t.Fatalf("err = %v (status %d), want the primary %d", err, statusCodeFromError(err), wantStatus)
						}
						if primary == "cooldown" && cooldownRetryAfterHeader(err) == "" {
							t.Fatalf("err = %v, want the primary Retry-After", err)
						}
					})
				}
			}
		}
	}
}

// A failed API leg returns its own error and runs nothing else; the Codex leg of
// a Claude -> Codex fallback never chains into the Codex API fallback.
func TestAPIFallbackDoesNotChain(t *testing.T) {
	withQuotaCooldownEnabled(t)
	t.Run("api leg fails", func(t *testing.T) {
		f := newAPIFallbackFixture(t, "claude", 2, nil)
		f.primaryErr = &Error{HTTPStatus: http.StatusServiceUnavailable}
		f.apiErr = &Error{HTTPStatus: http.StatusBadGateway, Message: "api down"}
		_, err := runFallbackPath(context.Background(), f.m, "execute", []string{"claude"}, cliproxyexecutor.Request{Model: apiFallbackModel}, cliproxyexecutor.Options{})
		if statusCodeFromError(err) != http.StatusBadGateway || !reflect.DeepEqual(f.calls, []string{AuthKindOAuth, AuthKindOAuth, AuthKindAPIKey}) {
			t.Fatalf("calls=%v err=%v", f.calls, err)
		}
	})
	t.Run("codex leg does not use the codex api fallback", func(t *testing.T) {
		cfg := &internalconfig.Config{}
		cfg.Routing.Providers.Claude.Fallback = internalconfig.ProviderFallbackConfig{Target: internalconfig.ProviderFallbackCodex, Mappings: []internalconfig.ClaudeFallbackMapping{{From: apiFallbackModel, To: apiFallbackModel}}}
		cfg.Routing.Providers.Codex = apiFallbackPolicy("codex-key-idx")
		f := newAPIFallbackFixture(t, "codex", 1, cfg)
		claudeID := t.Name() + "-claude-oauth"
		registry.GetGlobalRegistry().RegisterClient(claudeID, "claude", []*registry.ModelInfo{{ID: apiFallbackModel}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(claudeID) })
		if _, errRegister := f.m.Register(WithSkipPersist(context.Background()), providerPolicyAuth(providerPolicyTestAuth{claudeID, "claude", AuthKindOAuth, ""})); errRegister != nil {
			t.Fatal(errRegister)
		}
		f.primaryErr = &Error{HTTPStatus: http.StatusServiceUnavailable}
		_, err := runFallbackPath(context.Background(), f.m, "execute", []string{"claude"}, cliproxyexecutor.Request{Model: apiFallbackModel}, cliproxyexecutor.Options{})
		if statusCodeFromError(err) != http.StatusServiceUnavailable || !reflect.DeepEqual(f.ids, []string{claudeID, f.oauthIDs[0]}) {
			t.Fatalf("ids=%v err=%v, want the Claude and Codex subscriptions only", f.ids, err)
		}
	})
}

// The API leg runs only before the response starts.
func TestAPIFallbackStreamingBoundary(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(map[bool]string{false: "bootstrap", true: "started"}[started], func(t *testing.T) {
			f := newAPIFallbackFixture(t, "codex", 1, nil)
			f.executors["codex"].stream = func(a *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				f.calls = append(f.calls, a.AuthKind())
				if a.AuthKind() == AuthKindAPIKey {
					return fallbackTestStream(cliproxyexecutor.StreamChunk{Payload: []byte("api")}), nil
				}
				chunks := []cliproxyexecutor.StreamChunk{}
				if started {
					chunks = append(chunks, cliproxyexecutor.StreamChunk{Payload: []byte("data: partial\n\n")})
				}
				chunks = append(chunks, cliproxyexecutor.StreamChunk{Err: &Error{HTTPStatus: http.StatusServiceUnavailable}})
				return fallbackTestStream(chunks...), nil
			}
			body, err := runFallbackPath(context.Background(), f.m, "stream", []string{"codex"}, cliproxyexecutor.Request{Model: apiFallbackModel}, cliproxyexecutor.Options{})
			if started {
				if body != "data: partial\n\n" || err == nil || !reflect.DeepEqual(f.calls, []string{AuthKindOAuth}) {
					t.Fatalf("started stream: body=%q err=%v calls=%v", body, err, f.calls)
				}
				return
			}
			if body != "api" || err != nil || f.calls[len(f.calls)-1] != AuthKindAPIKey {
				t.Fatalf("bootstrap fallback: body=%q err=%v calls=%v", body, err, f.calls)
			}
		})
	}
}

// The API leg runs only on the provider that owns the fallback: another provider
// of the request that serves the same model stays out of the leg even when its
// subscription is still selectable.
func TestAPIFallbackLegRunsOnlyOnFallbackProvider(t *testing.T) {
	prevTransient := transientErrorCooldownSeconds.Load()
	transientErrorCooldownSeconds.Store(-1)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })
	for _, path := range []string{"execute", "count", "stream"} {
		t.Run(path, func(t *testing.T) {
			f := newAPIFallbackFixture(t, "claude", 1, nil)
			codexID := t.Name() + "-codex-oauth"
			registry.GetGlobalRegistry().RegisterClient(codexID, "codex", []*registry.ModelInfo{{ID: apiFallbackModel}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(codexID) })
			if _, errRegister := f.m.Register(WithSkipPersist(context.Background()), providerPolicyAuth(providerPolicyTestAuth{codexID, "codex", AuthKindOAuth, ""})); errRegister != nil {
				t.Fatal(errRegister)
			}
			// Both legs fail and nothing cools down, so a leg that still had the
			// Codex subscription in its pool would run it again.
			f.primaryErr = &Error{HTTPStatus: http.StatusServiceUnavailable}
			f.apiErr = &Error{HTTPStatus: http.StatusServiceUnavailable}
			_, err := runFallbackPath(context.Background(), f.m, path, []string{"claude", "codex"}, cliproxyexecutor.Request{Model: apiFallbackModel}, cliproxyexecutor.Options{})
			if statusCodeFromError(err) != http.StatusServiceUnavailable {
				t.Fatalf("err = %v, want the API leg's 503", err)
			}
			if countKind(f.calls, AuthKindOAuth) != 2 || countKind(f.calls, AuthKindAPIKey) != 1 || f.calls[len(f.calls)-1] != AuthKindAPIKey {
				t.Fatalf("calls = %v (ids %v), want both subscriptions once, then only the API key", f.calls, f.ids)
			}
		})
	}
}

// Fallback mode always applies only to target codex: target api always runs
// the primary leg first.
func TestAPIFallbackIgnoresAlwaysMode(t *testing.T) {
	cfg := &internalconfig.Config{}
	cfg.Routing.Providers.Claude = apiFallbackPolicy("claude-key-idx")
	cfg.Routing.Providers.Claude.Fallback.Mode = "always"
	f := newAPIFallbackFixture(t, "claude", 1, cfg)
	body, err := runFallbackPath(context.Background(), f.m, "execute", []string{"claude"}, cliproxyexecutor.Request{Model: apiFallbackModel}, cliproxyexecutor.Options{})
	if err != nil || body != "primary" || !reflect.DeepEqual(f.calls, []string{AuthKindOAuth}) {
		t.Fatalf("calls=%v body=%q err=%v", f.calls, body, err)
	}
}

func TestProviderFallbackForResolution(t *testing.T) {
	codexTarget := internalconfig.ProviderFallbackConfig{Target: internalconfig.ProviderFallbackCodex, Mappings: []internalconfig.ClaudeFallbackMapping{{From: "claude-opus-*", To: "gpt-6-sol"}}}
	profileCtx := func(claude, codex internalconfig.ProviderRoutingConfig) context.Context {
		return withRoutingProfile(context.Background(), &routingProfile{providerPolicy: true, providers: internalconfig.RoutingProfile{Providers: internalconfig.ProvidersRoutingConfig{Claude: claude, Codex: codex}}.EffectiveProviders()})
	}
	apiClaude := apiFallbackPolicy("claude-idx")
	apiCodex := apiFallbackPolicy("codex-idx")
	m := NewManager(nil, nil, nil)
	req := cliproxyexecutor.Request{Model: "claude-opus-5"}
	for _, tc := range []struct {
		name                   string
		ctx                    context.Context
		providers              []string
		opts                   cliproxyexecutor.Options
		wantProvider, wantType string
	}{
		{name: "claude api", ctx: profileCtx(apiClaude, internalconfig.ProviderRoutingConfig{}), providers: []string{"claude"}, wantProvider: "claude", wantType: "api"},
		{name: "codex api", ctx: profileCtx(internalconfig.ProviderRoutingConfig{}, apiCodex), providers: []string{"codex"}, wantProvider: "codex", wantType: "api"},
		{name: "claude first", ctx: profileCtx(apiClaude, apiCodex), providers: []string{"codex", "claude"}, wantProvider: "claude", wantType: "api"},
		{name: "codex when claude has none", ctx: profileCtx(internalconfig.ProviderRoutingConfig{}, apiCodex), providers: []string{"claude", "codex"}, wantProvider: "codex", wantType: "api"},
		{name: "claude codex target", ctx: profileCtx(internalconfig.ProviderRoutingConfig{Fallback: codexTarget}, apiCodex), providers: []string{"claude", "codex"}, wantProvider: "claude", wantType: "codex"},
		{name: "provider not requested", ctx: profileCtx(apiClaude, internalconfig.ProviderRoutingConfig{}), providers: []string{"codex"}},
		{name: "other provider", ctx: profileCtx(apiClaude, apiCodex), providers: []string{"gemini"}},
		{name: "api mode", ctx: profileCtx(internalconfig.ProviderRoutingConfig{Mode: internalconfig.ProviderModeAPI, APICredential: "claude-idx", Fallback: internalconfig.ProviderFallbackConfig{Target: internalconfig.ProviderFallbackAPI}}, internalconfig.ProviderRoutingConfig{}), providers: []string{"claude"}},
		{name: "required kind", ctx: withRequiredAuthKind(profileCtx(apiClaude, internalconfig.ProviderRoutingConfig{}), AuthKindOAuth), providers: []string{"claude"}},
		{name: "credential policy", ctx: withCredentialPolicy(profileCtx(internalconfig.ProviderRoutingConfig{}, apiCodex), "api-key"), providers: []string{"codex"}},
		{name: "pinned", ctx: profileCtx(apiClaude, internalconfig.ProviderRoutingConfig{}), providers: []string{"claude"}, opts: cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: "pinned"}}},
		{name: "fallback leg", ctx: withRoutingFallbackLeg(profileCtx(apiClaude, internalconfig.ProviderRoutingConfig{}), "api"), providers: []string{"claude"}},
		{name: "no profile", ctx: context.Background(), providers: []string{"claude"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := m.providerFallbackFor(tc.ctx, tc.providers, req, tc.opts)
			if got.provider != tc.wantProvider || got.target != tc.wantType {
				t.Fatalf("fallback = %+v, want %s/%s", got, tc.wantProvider, tc.wantType)
			}
			if got.target == internalconfig.ProviderFallbackAPI && (got.model != req.Model || got.mode != "auto") {
				t.Fatalf("api fallback = %+v, want the same model in auto mode", got)
			}
		})
	}
}

// The API leg logs its selection under the fallback stage with the api stage and
// target, and the fallback message names the target without the raw key.
func TestAPIFallbackLogging(t *testing.T) {
	hook := newStandardLogHook(t)
	const key = "sk-api-fallback-logging"
	cfg := &internalconfig.Config{SDKConfig: internalconfig.SDKConfig{APIKeys: []string{key}}}
	cfg.Routing.KeyProfiles = map[string]*internalconfig.RoutingProfile{key: {Providers: internalconfig.ProvidersRoutingConfig{Claude: apiFallbackPolicy("claude-key-idx")}}}
	f := newAPIFallbackFixture(t, "claude", 1, cfg)
	f.primaryErr = &Error{HTTPStatus: http.StatusTooManyRequests}
	if body, err := runFallbackPath(ginContextWithKey(key), f.m, "execute", []string{"claude"}, cliproxyexecutor.Request{Model: apiFallbackModel}, cliproxyexecutor.Options{}); err != nil || body != "api" {
		t.Fatalf("body=%q err=%v calls=%v", body, err, f.calls)
	}
	var apiSelection, message bool
	for _, entry := range hook.AllEntries() {
		if line, _ := entry.String(); strings.Contains(line, key) || strings.Contains(line, "secret-") {
			t.Fatalf("log line leaked a secret: %s", line)
		}
		switch {
		case entry.Message == "Routing profile selection" && entry.Data["auth_index"] == f.keyIndex:
			apiSelection = entry.Data["routing_stage"] == routingStageFallback && entry.Data["fallback_stage"] == routingStageAPI && entry.Data["fallback_target"] == "api"
		case strings.Contains(entry.Message, "API key fallback"):
			message = entry.Data["fallback_target"] == "api" && entry.Data["provider"] == "claude" && entry.Data["primary_status"] == http.StatusTooManyRequests
		}
	}
	if !apiSelection || !message {
		t.Fatalf("api selection logged=%t, fallback message logged=%t; entries=%v", apiSelection, message, hook.AllEntries())
	}
}
