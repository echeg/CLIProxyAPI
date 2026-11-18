package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
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

const (
	disabledKeyMessage    = "claude is disabled by the routing profile of this API key"
	disabledSharedMessage = "claude is disabled by the routing profile"
	disabledRefusalLog    = "Refusing request: provider disabled by routing profile"
)

var (
	disabledClaudeOAuth = providerPolicyTestAuth{"pd-claude-oauth", "claude", AuthKindOAuth, "claude-oauth"}
	disabledCodexOAuth  = providerPolicyTestAuth{"pd-codex-oauth", "codex", AuthKindOAuth, "codex-oauth"}
	disabledGeminiKey   = providerPolicyTestAuth{"pd-gemini-key", "gemini", AuthKindAPIKey, "gemini-key"}
)

// disabledProviderConfig builds a config whose shared profile and key A profile
// (when keyProfile is non-nil) carry the given providers blocks.
func disabledProviderConfig(shared internalconfig.ProvidersRoutingConfig, keyProfile *internalconfig.ProvidersRoutingConfig) *internalconfig.Config {
	cfg := &internalconfig.Config{SDKConfig: internalconfig.SDKConfig{APIKeys: []string{routingTestKeyA, routingTestKeyB}}}
	cfg.Routing.Providers = shared
	if keyProfile != nil {
		cfg.Routing.KeyProfiles = map[string]*internalconfig.RoutingProfile{routingTestKeyA: {Providers: *keyProfile}}
	}
	return cfg
}

func disabledProviders(claude, codex bool) internalconfig.ProvidersRoutingConfig {
	return internalconfig.ProvidersRoutingConfig{Claude: internalconfig.ProviderRoutingConfig{Disabled: claude}, Codex: internalconfig.ProviderRoutingConfig{Disabled: codex}}
}

// assertProviderDisabledError checks the 403 request-scoped refusal and its exact
// client-facing message.
func assertProviderDisabledError(t *testing.T, err error, message string) {
	t.Helper()
	var disabledErr *providerDisabledError
	if !errors.As(err, &disabledErr) {
		t.Fatalf("err = %v (%T), want a provider disabled refusal", err, err)
	}
	if got := err.Error(); got != message {
		t.Fatalf("err.Error() = %q, want %q", got, message)
	}
	if status := statusCodeFromError(err); status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", status)
	}
	if !isRequestScopedError(err) {
		t.Fatal("refusal is not request-scoped")
	}
	if !IsProviderDisabledError(fmt.Errorf("wrapped: %w", err)) {
		t.Fatal("IsProviderDisabledError misses the wrapped refusal")
	}
}

func TestIsProviderDisabledErrorRejectsOtherErrors(t *testing.T) {
	for _, err := range []error{nil, errors.New("forbidden"), &Error{HTTPStatus: http.StatusForbidden}, NewRequestScopedError("refused", http.StatusForbidden)} {
		if IsProviderDisabledError(err) {
			t.Fatalf("IsProviderDisabledError(%v) = true", err)
		}
	}
}

// assertNoAuthFailures checks that no registered auth recorded a failure.
func assertNoAuthFailures(t *testing.T, m *Manager) {
	t.Helper()
	for _, auth := range m.List() {
		if auth.Unavailable || auth.LastError != nil || !auth.NextRetryAfter.IsZero() || len(auth.ModelStates) != 0 {
			t.Fatalf("auth %s recorded a failure: unavailable=%t lastError=%v states=%d", auth.ID, auth.Unavailable, auth.LastError, len(auth.ModelStates))
		}
	}
}

func disabledRefusalEntries(hook interface{ AllEntries() []*log.Entry }) []*log.Entry {
	var entries []*log.Entry
	for _, entry := range hook.AllEntries() {
		if entry.Message == disabledRefusalLog {
			entries = append(entries, entry)
		}
	}
	return entries
}

func TestDisabledProviderRefusesEveryExecutePath(t *testing.T) {
	keyProfile := disabledProviders(true, false)
	for _, path := range []string{"execute", "count", "stream"} {
		t.Run(path, func(t *testing.T) {
			m, calls := providerPolicyTestManager(t, disabledProviderConfig(internalconfig.ProvidersRoutingConfig{}, &keyProfile), disabledClaudeOAuth, disabledCodexOAuth)
			hook := newStandardLogHook(t)
			_, err := runFallbackPath(ginContextWithKey(routingTestKeyA), m, path, []string{"claude"}, cliproxyexecutor.Request{Model: providerPolicyTestModel}, cliproxyexecutor.Options{})
			assertProviderDisabledError(t, err, disabledKeyMessage)
			if len(*calls) != 0 {
				t.Fatalf("executor calls = %v, want none", *calls)
			}
			assertNoAuthFailures(t, m)
			entries := disabledRefusalEntries(hook)
			if len(entries) != 1 {
				t.Fatalf("refusal log entries = %d, want 1", len(entries))
			}
			entry := entries[0]
			if entry.Level != log.InfoLevel || entry.Data["provider"] != "claude" || entry.Data["profile"] != internalconfig.KeyHashPrefix(routingTestKeyA) {
				t.Fatalf("refusal log = level %s fields %v", entry.Level, entry.Data)
			}
			line, errLine := entry.String()
			if errLine != nil {
				t.Fatal(errLine)
			}
			if strings.Contains(line, routingTestKeyA) {
				t.Fatalf("refusal log leaked the raw key: %s", line)
			}

			// The enabled provider of the same key still runs.
			if _, errCodex := runFallbackPath(ginContextWithKey(routingTestKeyA), m, path, []string{"codex"}, cliproxyexecutor.Request{Model: providerPolicyTestModel}, cliproxyexecutor.Options{}); errCodex != nil {
				t.Fatalf("codex request: %v", errCodex)
			}
			if !reflect.DeepEqual(*calls, []string{disabledCodexOAuth.id}) {
				t.Fatalf("calls = %v, want only codex", *calls)
			}
		})
	}
}

func TestDisabledProviderSharedProfile(t *testing.T) {
	keyProfile := internalconfig.ProvidersRoutingConfig{}
	cfg := disabledProviderConfig(disabledProviders(true, false), &keyProfile)
	m, calls := providerPolicyTestManager(t, cfg, disabledClaudeOAuth, disabledCodexOAuth)
	hook := newStandardLogHook(t)
	req := cliproxyexecutor.Request{Model: providerPolicyTestModel}

	// Key B has no own profile, so the shared disable applies.
	for _, ctx := range []context.Context{ginContextWithKey(routingTestKeyB), ginContextWithKey(""), context.Background()} {
		_, err := m.Execute(ctx, []string{"claude"}, req, cliproxyexecutor.Options{})
		assertProviderDisabledError(t, err, disabledSharedMessage)
	}
	entries := disabledRefusalEntries(hook)
	if len(entries) != 3 {
		t.Fatalf("refusal log entries = %d, want 3", len(entries))
	}
	for _, entry := range entries {
		if entry.Data["provider"] != "claude" || entry.Data["profile"] != "shared" {
			t.Fatalf("refusal log fields = %v, want profile=shared", entry.Data)
		}
		line, errLine := entry.String()
		if errLine != nil {
			t.Fatal(errLine)
		}
		if strings.Contains(line, routingTestKeyB) || strings.Contains(line, internalconfig.KeyHashPrefix(routingTestKeyB)) {
			t.Fatalf("shared refusal log names the key: %s", line)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("executor calls = %v, want none", *calls)
	}

	// Key A has its own profile without a disable; it replaces the shared one.
	if _, err := m.Execute(ginContextWithKey(routingTestKeyA), []string{"claude"}, req, cliproxyexecutor.Options{}); err != nil {
		t.Fatalf("key A claude request: %v", err)
	}
	if !reflect.DeepEqual(*calls, []string{disabledClaudeOAuth.id}) {
		t.Fatalf("calls = %v, want claude", *calls)
	}
}

func TestDisabledProviderFiltersRequestProviders(t *testing.T) {
	req := cliproxyexecutor.Request{Model: providerPolicyTestModel}
	for _, tc := range []struct {
		name      string
		claude    bool
		codex     bool
		providers []string
		want      string
		message   string
	}{
		{"claude disabled runs codex", true, false, []string{"claude", "codex"}, disabledCodexOAuth.id, ""},
		{"codex disabled runs claude", false, true, []string{"codex", "claude"}, disabledClaudeOAuth.id, ""},
		{"mixed case names are compared normalized", true, false, []string{" Claude ", "codex"}, disabledCodexOAuth.id, ""},
		{"both disabled names the first", true, true, []string{"claude", "codex"}, "", disabledKeyMessage},
		{"both disabled names codex first", true, true, []string{"codex", "claude"}, "", "codex is disabled by the routing profile of this API key"},
		{"other provider untouched", true, true, []string{"gemini"}, disabledGeminiKey.id, ""},
		{"other provider kept beside a disabled one", true, false, []string{"claude", "gemini"}, disabledGeminiKey.id, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			keyProfile := disabledProviders(tc.claude, tc.codex)
			m, calls := providerPolicyTestManager(t, disabledProviderConfig(internalconfig.ProvidersRoutingConfig{}, &keyProfile), disabledClaudeOAuth, disabledCodexOAuth, disabledGeminiKey)
			for i := 0; i < 3; i++ {
				_, err := m.Execute(ginContextWithKey(routingTestKeyA), tc.providers, req, cliproxyexecutor.Options{})
				if tc.message != "" {
					assertProviderDisabledError(t, err, tc.message)
					continue
				}
				if err != nil {
					t.Fatalf("Execute #%d: %v", i, err)
				}
			}
			if tc.message != "" {
				if len(*calls) != 0 {
					t.Fatalf("executor calls = %v, want none", *calls)
				}
				return
			}
			for _, id := range *calls {
				if id != tc.want {
					t.Fatalf("calls = %v, want only %s", *calls, tc.want)
				}
			}
		})
	}
}

func TestDisabledProviderRefusesPinnedRequest(t *testing.T) {
	keyProfile := disabledProviders(true, false)
	pin := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: disabledClaudeOAuth.id}}
	for _, providers := range [][]string{{"claude"}, {"claude", "codex"}, {"codex"}} {
		for _, path := range []string{"execute", "count", "stream"} {
			t.Run(strings.Join(providers, ",")+"/"+path, func(t *testing.T) {
				m, calls := providerPolicyTestManager(t, disabledProviderConfig(internalconfig.ProvidersRoutingConfig{}, &keyProfile), disabledClaudeOAuth, disabledCodexOAuth)
				_, err := runFallbackPath(ginContextWithKey(routingTestKeyA), m, path, providers, cliproxyexecutor.Request{Model: providerPolicyTestModel}, pin)
				assertProviderDisabledError(t, err, disabledKeyMessage)
				if len(*calls) != 0 {
					t.Fatalf("executor calls = %v, want none", *calls)
				}
				assertNoAuthFailures(t, m)
			})
		}
	}

	// A pin to an auth of the enabled provider is served.
	m, calls := providerPolicyTestManager(t, disabledProviderConfig(internalconfig.ProvidersRoutingConfig{}, &keyProfile), disabledClaudeOAuth, disabledCodexOAuth)
	codexPin := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: disabledCodexOAuth.id}}
	if _, err := m.Execute(ginContextWithKey(routingTestKeyA), []string{"claude", "codex"}, cliproxyexecutor.Request{Model: providerPolicyTestModel}, codexPin); err != nil {
		t.Fatalf("pinned codex request: %v", err)
	}
	if !reflect.DeepEqual(*calls, []string{disabledCodexOAuth.id}) {
		t.Fatalf("calls = %v, want the pinned codex auth", *calls)
	}
}

func TestDisabledProviderNotEnforcedWithoutProviderPolicy(t *testing.T) {
	keyProfile := disabledProviders(true, true)
	cfg := disabledProviderConfig(disabledProviders(true, true), &keyProfile)

	t.Run("plugin scheduler", func(t *testing.T) {
		m, calls := providerPolicyTestManager(t, cfg, disabledClaudeOAuth, disabledCodexOAuth)
		m.SetPluginScheduler(&fakePluginScheduler{})
		for _, ctx := range []context.Context{ginContextWithKey(routingTestKeyA), ginContextWithKey(routingTestKeyB)} {
			if _, err := m.Execute(ctx, []string{"claude"}, cliproxyexecutor.Request{Model: providerPolicyTestModel}, cliproxyexecutor.Options{}); err != nil {
				t.Fatalf("Execute under a plugin scheduler: %v", err)
			}
		}
		if len(*calls) != 2 {
			t.Fatalf("calls = %v, want two claude calls", *calls)
		}
	})

	t.Run("home", func(t *testing.T) {
		homeCfg := *cfg
		homeCfg.Home.Enabled = true
		m := NewManager(nil, &RoundRobinSelector{}, nil)
		m.SetConfig(&homeCfg)
		ctx := m.ensureRoutingProfile(ginContextWithKey(routingTestKeyA))
		providers, err := m.filterDisabledProviders(ctx, []string{"claude", "codex"}, cliproxyexecutor.Options{})
		if err != nil || !reflect.DeepEqual(providers, []string{"claude", "codex"}) {
			t.Fatalf("Home filter = %v, %v; want both providers kept", providers, err)
		}
		for _, provider := range []string{"claude", "codex"} {
			if _, errSelect := m.refuseDisabledSelection(ginContextWithKey(routingTestKeyA), provider); errSelect != nil {
				t.Fatalf("Home direct selection of %s refused: %v", provider, errSelect)
			}
		}
	})
}

// directSelections runs every public direct-selection method for provider, as
// Codex search (credential policy) and live/realtime (by kind) use them.
func directSelections(m *Manager, provider string) map[string]func(context.Context) (*Auth, error) {
	return map[string]func(context.Context) (*Auth, error){
		"SelectAuthExcluding": func(ctx context.Context) (*Auth, error) {
			return m.SelectAuthExcluding(ctx, provider, providerPolicyTestModel, cliproxyexecutor.Options{}, nil)
		},
		"SelectAuthByKind": func(ctx context.Context) (*Auth, error) {
			return m.SelectAuthByKind(ctx, provider, providerPolicyTestModel, AuthKindOAuth, cliproxyexecutor.Options{})
		},
		"SelectAuthWithCredentialPolicy": func(ctx context.Context) (*Auth, error) {
			return m.SelectAuthWithCredentialPolicy(ctx, provider, providerPolicyTestModel, CredentialPolicyCodexAlphaSearchV1, cliproxyexecutor.Options{})
		},
	}
}

func TestDisabledProviderRefusesDirectSelection(t *testing.T) {
	const codexKeyMessage = "codex is disabled by the routing profile of this API key"
	const codexSharedMessage = "codex is disabled by the routing profile"
	for _, tc := range []struct {
		name    string
		shared  internalconfig.ProvidersRoutingConfig
		key     internalconfig.ProvidersRoutingConfig
		ctx     context.Context
		message string
		profile string
	}{
		{"key profile", internalconfig.ProvidersRoutingConfig{}, disabledProviders(false, true), ginContextWithKey(routingTestKeyA), codexKeyMessage, internalconfig.KeyHashPrefix(routingTestKeyA)},
		{"shared profile", disabledProviders(false, true), internalconfig.ProvidersRoutingConfig{}, ginContextWithKey(routingTestKeyB), codexSharedMessage, "shared"},
		{"shared profile without a key", disabledProviders(false, true), internalconfig.ProvidersRoutingConfig{}, context.Background(), codexSharedMessage, "shared"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := tc.key
			m, _ := providerPolicyTestManager(t, disabledProviderConfig(tc.shared, &key), disabledClaudeOAuth, disabledCodexOAuth)
			for name, selectAuth := range directSelections(m, "codex") {
				hook := newStandardLogHook(t)
				selected, err := selectAuth(tc.ctx)
				if selected != nil {
					t.Fatalf("%s selected %s, want a refusal", name, selected.ID)
				}
				assertProviderDisabledError(t, err, tc.message)
				entries := disabledRefusalEntries(hook)
				if len(entries) != 1 || entries[0].Data["provider"] != "codex" || entries[0].Data["profile"] != tc.profile {
					t.Fatalf("%s refusal log entries = %d %v, want one with profile=%s", name, len(entries), hook.AllEntries(), tc.profile)
				}
			}
			assertNoAuthFailures(t, m)
		})
	}
}

func TestDirectSelectionWithCodexEnabled(t *testing.T) {
	// Claude disabled does not affect a Codex selection; a key profile that keeps
	// Codex enabled replaces a shared Codex disable.
	for _, tc := range []struct {
		name   string
		shared internalconfig.ProvidersRoutingConfig
		key    internalconfig.ProvidersRoutingConfig
	}{
		{"claude disabled", internalconfig.ProvidersRoutingConfig{}, disabledProviders(true, false)},
		{"key profile replaces shared disable", disabledProviders(false, true), internalconfig.ProvidersRoutingConfig{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := tc.key
			m, _ := providerPolicyTestManager(t, disabledProviderConfig(tc.shared, &key), disabledClaudeOAuth, disabledCodexOAuth)
			for name, selectAuth := range directSelections(m, "codex") {
				selected, err := selectAuth(ginContextWithKey(routingTestKeyA))
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				if selected == nil || selected.ID != disabledCodexOAuth.id {
					t.Fatalf("%s selected %v, want %s", name, selected, disabledCodexOAuth.id)
				}
			}
		})
	}

	t.Run("plugin scheduler", func(t *testing.T) {
		key := disabledProviders(true, true)
		m, _ := providerPolicyTestManager(t, disabledProviderConfig(disabledProviders(true, true), &key), disabledClaudeOAuth, disabledCodexOAuth)
		m.SetPluginScheduler(&fakePluginScheduler{})
		_, err := m.SelectAuthByKind(ginContextWithKey(routingTestKeyA), "codex", providerPolicyTestModel, AuthKindOAuth, cliproxyexecutor.Options{})
		var disabledErr *providerDisabledError
		if errors.As(err, &disabledErr) {
			t.Fatalf("plugin scheduler refused the selection: %v", err)
		}
	})
}

// codexFallbackIntoDisabledConfig configures a Claude -> Codex fallback in mode
// with Codex disabled or enabled.
func codexFallbackIntoDisabledConfig(mode string, codexDisabled bool) *internalconfig.Config {
	cfg := &internalconfig.Config{}
	cfg.Routing.Providers.Claude.Fallback = internalconfig.ProviderFallbackConfig{
		Target: internalconfig.ProviderFallbackCodex, Mode: mode,
		Mappings: []internalconfig.ClaudeFallbackMapping{{From: "claude-opus-*", To: "gpt-6-astra"}},
	}
	cfg.Routing.Providers.Codex.Disabled = codexDisabled
	return cfg
}

func TestClaudeFallbackSkipsDisabledCodex(t *testing.T) {
	withQuotaCooldownEnabled(t)
	for _, path := range []string{"execute", "count", "stream"} {
		t.Run(path+"/auto", func(t *testing.T) {
			m, calls, _, _ := fallbackTestManager(t, "off", &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"})
			m.SetConfig(codexFallbackIntoDisabledConfig("auto", true))
			_, err := runFallbackPath(context.Background(), m, path, []string{"claude"}, cliproxyexecutor.Request{Model: "claude-opus-5"}, cliproxyexecutor.Options{})
			if status := statusCodeFromError(err); status != http.StatusTooManyRequests {
				t.Fatalf("err = %v (status %d), want the primary 429", err, status)
			}
			if len(*calls) == 0 || countKind(*calls, "codex") != 0 {
				t.Fatalf("calls = %v, want claude only", *calls)
			}
		})
		t.Run(path+"/always", func(t *testing.T) {
			m, calls, _, _ := fallbackTestManager(t, "off", nil)
			m.SetConfig(codexFallbackIntoDisabledConfig("always", true))
			body, err := runFallbackPath(context.Background(), m, path, []string{"claude"}, cliproxyexecutor.Request{Model: "claude-opus-5"}, cliproxyexecutor.Options{})
			if err != nil || body != "primary" {
				t.Fatalf("body = %q, err = %v, want the Claude primary", body, err)
			}
			if !reflect.DeepEqual(*calls, []string{"claude"}) {
				t.Fatalf("calls = %v, want [claude]", *calls)
			}
		})
	}
}

func TestHasProviderFallbackAfterErrorWithDisabledCodex(t *testing.T) {
	req := cliproxyexecutor.Request{Model: "claude-opus-5"}
	for _, codexDisabled := range []bool{true, false} {
		for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
			m, _, _, _ := fallbackTestManager(t, "off", nil)
			m.SetConfig(codexFallbackIntoDisabledConfig("auto", codexDisabled))
			ctx := m.ensureRoutingProfile(context.Background())
			got := m.hasProviderFallbackAfterError(ctx, []string{"claude"}, req, cliproxyexecutor.Options{}, &Error{HTTPStatus: status})
			if got == codexDisabled {
				t.Fatalf("codex disabled=%t status %d: hasProviderFallbackAfterError = %t", codexDisabled, status, got)
			}
		}
	}
}
