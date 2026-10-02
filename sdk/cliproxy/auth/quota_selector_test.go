package auth

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func resetSelectorAuth(id, provider string, observedAt time.Time, untilReset time.Duration) *Auth {
	signals := map[string]string{
		"X-Codex-Primary-Used-Percent": "20",
		"X-Codex-Primary-Reset-At":     strconv.FormatInt(observedAt.Add(untilReset).Unix(), 10),
	}
	if provider == "claude" {
		signals = map[string]string{
			"Anthropic-Ratelimit-Unified-5h-Utilization": "0.2",
			"Anthropic-Ratelimit-Unified-5h-Reset":       observedAt.Add(untilReset).Format(time.RFC3339),
		}
	}
	return &Auth{ID: id, Provider: provider, Status: StatusActive, Quota: QuotaState{ObservedAt: observedAt, Signals: signals}}
}

func TestSubscriptionResetForAuth(t *testing.T) {
	now := time.Unix(2000000000, 0)
	for _, testCase := range []struct {
		name     string
		provider string
		modify   func(*Auth)
		want     time.Duration
		known    bool
	}{
		{name: "codex primary", provider: "codex", want: time.Hour, known: true},
		{name: "claude shared window", provider: "claude", want: time.Hour, known: true},
		{name: "unsupported provider", provider: "gemini"},
		{name: "no observation time", provider: "codex", modify: func(auth *Auth) { auth.Quota.ObservedAt = time.Time{} }},
		{name: "future observation", provider: "codex", modify: func(auth *Auth) { auth.Quota.ObservedAt = now.Add(time.Second) }},
		{name: "relative reset anchored to observation", provider: "codex", want: 30 * time.Minute, known: true, modify: func(auth *Auth) {
			auth.Quota.ObservedAt = now.Add(-30 * time.Minute)
			delete(auth.Quota.Signals, "X-Codex-Primary-Reset-At")
			auth.Quota.Signals["X-Codex-Primary-Reset-After-Seconds"] = "3600"
		}},
		{name: "absolute reset wins over relative", provider: "codex", want: time.Hour, known: true, modify: func(auth *Auth) {
			auth.Quota.Signals["X-Codex-Primary-Reset-After-Seconds"] = "1"
		}},
		{name: "expired absolute reset cannot be extended by relative", provider: "codex", modify: func(auth *Auth) {
			auth.Quota.Signals["X-Codex-Primary-Reset-At"] = strconv.FormatInt(now.Unix(), 10)
			auth.Quota.Signals["X-Codex-Primary-Reset-After-Seconds"] = "3600"
		}},
		{name: "secondary may reset first", provider: "codex", want: time.Minute, known: true, modify: func(auth *Auth) {
			auth.Quota.Signals["X-Codex-Secondary-Used-Percent"] = "95"
			auth.Quota.Signals["X-Codex-Secondary-Reset-At"] = strconv.FormatInt(now.Add(time.Minute).Unix(), 10)
		}},
		{name: "shared exhausted window prevents rank", provider: "codex", modify: func(auth *Auth) {
			auth.Quota.Signals["X-Codex-Secondary-Used-Percent"] = "100"
			auth.Quota.Signals["X-Codex-Secondary-Reset-At"] = strconv.FormatInt(now.Add(time.Minute).Unix(), 10)
		}},
		{name: "exhausted window without reset prevents rank", provider: "codex", modify: func(auth *Auth) {
			auth.Quota.Signals["X-Codex-Secondary-Used-Percent"] = "100"
		}},
		{name: "expired exhausted window is unknown", provider: "codex", want: time.Hour, known: true, modify: func(auth *Auth) {
			auth.Quota.Signals["X-Codex-Secondary-Used-Percent"] = "100"
			auth.Quota.Signals["X-Codex-Secondary-Reset-At"] = strconv.FormatInt(now.Add(-time.Minute).Unix(), 10)
		}},
		{name: "codex explicitly disallowed", provider: "codex", modify: func(auth *Auth) { auth.Quota.Signals["X-Codex-Allowed"] = "false" }},
		{name: "codex limit reached", provider: "codex", modify: func(auth *Auth) { auth.Quota.Signals["X-Codex-Limit-Reached"] = "true" }},
		{name: "claude exhausted", provider: "claude", modify: func(auth *Auth) { auth.Quota.Signals["Anthropic-Ratelimit-Unified-5h-Utilization"] = "1" }},
		{name: "claude rejected", provider: "claude", modify: func(auth *Auth) { auth.Quota.Signals["Anthropic-Ratelimit-Unified-5h-Status"] = "rejected" }},
		{name: "claude rejection without reset prevents rank", provider: "claude", modify: func(auth *Auth) {
			auth.Quota.Signals["Anthropic-Ratelimit-Unified-7d-Status"] = "rejected"
		}},
		{name: "claude warning is usable", provider: "claude", want: time.Hour, known: true, modify: func(auth *Auth) { auth.Quota.Signals["Anthropic-Ratelimit-Unified-5h-Status"] = "allowed_warning" }},
		{name: "additional and code review limits ignored", provider: "codex", want: time.Hour, known: true, modify: func(auth *Auth) {
			for _, prefix := range []string{"X-Codex-Code-Review-Primary-", "X-Codex-Additional-Bengalfox-Primary-", "X-Codex-Bengalfox-Primary-"} {
				auth.Quota.Signals[prefix+"Used-Percent"] = "100"
				auth.Quota.Signals[prefix+"Reset-At"] = strconv.FormatInt(now.Add(time.Second).Unix(), 10)
			}
		}},
		{name: "fable and overage limits ignored", provider: "claude", want: time.Hour, known: true, modify: func(auth *Auth) {
			for _, prefix := range []string{"Anthropic-Ratelimit-Unified-7d_oi-", "Anthropic-Ratelimit-Unified-Overage-"} {
				auth.Quota.Signals[prefix+"Utilization"] = "1"
				auth.Quota.Signals[prefix+"Status"] = "rejected"
				auth.Quota.Signals[prefix+"Reset"] = strconv.FormatInt(now.Add(time.Second).Unix(), 10)
			}
		}},
		{name: "header case is ignored", provider: "codex", want: time.Hour, known: true, modify: func(auth *Auth) {
			auth.Quota.Signals = map[string]string{"x-codex-primary-used-percent": " 20 ", "x-codex-primary-reset-at": strconv.FormatInt(now.Add(time.Hour).Unix(), 10)}
		}},
		{name: "nan utilization", provider: "codex", modify: func(auth *Auth) { auth.Quota.Signals["X-Codex-Primary-Used-Percent"] = "NaN" }},
		{name: "infinite utilization", provider: "codex", modify: func(auth *Auth) { auth.Quota.Signals["X-Codex-Primary-Used-Percent"] = "+Inf" }},
		{name: "negative utilization", provider: "codex", modify: func(auth *Auth) { auth.Quota.Signals["X-Codex-Primary-Used-Percent"] = "-1" }},
		{name: "missing utilization", provider: "codex", modify: func(auth *Auth) { delete(auth.Quota.Signals, "X-Codex-Primary-Used-Percent") }},
		{name: "malformed reset", provider: "codex", modify: func(auth *Auth) { auth.Quota.Signals["X-Codex-Primary-Reset-At"] = "invalid" }},
		{name: "millisecond reset rejected", provider: "codex", modify: func(auth *Auth) {
			auth.Quota.Signals["X-Codex-Primary-Reset-At"] = strconv.FormatInt(now.Add(time.Hour).UnixMilli(), 10)
		}},
		{name: "overflow relative reset rejected", provider: "codex", modify: func(auth *Auth) {
			delete(auth.Quota.Signals, "X-Codex-Primary-Reset-At")
			auth.Quota.Signals["X-Codex-Primary-Reset-After-Seconds"] = "9223372036854775807"
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			auth := resetSelectorAuth("account", testCase.provider, now, time.Hour)
			if testCase.modify != nil {
				testCase.modify(auth)
			}
			reset, known := subscriptionResetForAuth(auth, "model", now)
			if known != testCase.known || (known && !reset.Equal(now.Add(testCase.want))) {
				t.Fatalf("reset = %v, known = %t; want offset %v, known %t", reset, known, testCase.want, testCase.known)
			}
		})
	}
}

func TestSubscriptionResetUsesNewestRelevantSnapshot(t *testing.T) {
	now := time.Unix(2000000000, 0)
	auth := resetSelectorAuth("account", "codex", now.Add(-time.Minute), time.Hour)
	newer := resetSelectorAuth("account", "codex", now, 2*time.Hour)
	auth.ModelStates = map[string]*ModelState{
		"model": {Quota: newer.Quota},
		"other": {Quota: resetSelectorAuth("account", "codex", now, time.Second).Quota},
	}
	reset, known := subscriptionResetForAuth(auth, "model(high)", now)
	if !known || !reset.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("model reset = %v, known %t; want newest relevant observation", reset, known)
	}
	auth.ModelStates["model"].Quota.Signals["X-Codex-Primary-Used-Percent"] = "100"
	if _, known := subscriptionResetForAuth(auth, "model", now); known {
		t.Fatal("new exhausted observation must not revive older healthy credential snapshot")
	}
	auth.ModelStates["model"].Quota.Signals = map[string]string{"X-Codex-Code-Review-Primary-Reset-At": "2000000001"}
	reset, known = subscriptionResetForAuth(auth, "model", now)
	if !known || !reset.Equal(now.Add(59*time.Minute)) {
		t.Fatalf("reset = %v, known %t; unrelated snapshot must not replace main subscription observation", reset, known)
	}
}

func TestSubscriptionResetNewRejectionDoesNotReviveOlderModelObservation(t *testing.T) {
	now := time.Unix(2000000000, 0)
	for _, testCase := range []struct {
		provider string
		header   string
		value    string
	}{
		{provider: "codex", header: "X-Codex-Limit-Reached", value: "true"},
		{provider: "codex", header: "X-Codex-Allowed", value: "false"},
		{provider: "claude", header: "Anthropic-Ratelimit-Unified-Status", value: "rejected"},
	} {
		t.Run(testCase.header, func(t *testing.T) {
			auth := resetSelectorAuth("account", testCase.provider, now.Add(-time.Minute), time.Hour)
			auth.ModelStates = map[string]*ModelState{"model": {Quota: auth.Quota}}
			auth.Quota = QuotaState{ObservedAt: now, Signals: map[string]string{testCase.header: testCase.value}}
			if _, known := subscriptionResetForAuth(auth, "model", now); known {
				t.Fatal("new rejection-only response must not resurrect older healthy model snapshot")
			}
		})
	}
}

func TestEarliestResetSelectorSelection(t *testing.T) {
	now := time.Unix(2000000000, 0)
	selector := &EarliestResetSelector{nowFunc: func() time.Time { return now }}
	early := resetSelectorAuth("z-early", "codex", now, time.Minute)
	later := resetSelectorAuth("a-later", "codex", now, time.Hour)
	unknown := &Auth{ID: "b-unknown", Provider: "codex", Status: StatusActive}
	assertPick := func(ctx context.Context, want string, auths ...*Auth) {
		t.Helper()
		selected, errPick := selector.Pick(ctx, "codex", "model", cliproxyexecutor.Options{}, auths)
		if errPick != nil || selected == nil || selected.ID != want {
			t.Fatalf("selected = %v, error = %v; want %s", selected, errPick, want)
		}
	}
	assertPick(context.Background(), early.ID, unknown, later, early)
	assertPick(context.Background(), early.ID, later, unknown, early)
	tied := early.Clone()
	tied.ID = "a-tied"
	assertPick(context.Background(), tied.ID, early, tied)
	later.Attributes = map[string]string{"priority": "1"}
	assertPick(context.Background(), later.ID, early, later)
	later.Disabled = true
	assertPick(context.Background(), early.ID, early, later)
	later.Disabled = false
	later.Attributes = map[string]string{"websockets": "true"}
	assertPick(cliproxyexecutor.WithDownstreamWebsocket(context.Background()), later.ID, early, later)
	early.ModelStates = map[string]*ModelState{"model": {Unavailable: true, NextRetryAfter: now.Add(time.Hour)}}
	assertPick(context.Background(), later.ID, early, later)
}

func TestEarliestResetSelectorUnknownObservationsRoundRobin(t *testing.T) {
	now := time.Unix(2000000000, 0)
	selector := &EarliestResetSelector{nowFunc: func() time.Time { return now }}
	unknown := &Auth{ID: "a-unknown", Provider: "codex", Status: StatusActive}
	exhausted := resetSelectorAuth("b-exhausted", "codex", now, time.Hour)
	exhausted.Quota.Signals["X-Codex-Primary-Used-Percent"] = "100"
	expired := resetSelectorAuth("c-expired", "codex", now.Add(-time.Hour), time.Minute)
	for _, want := range []string{unknown.ID, exhausted.ID, expired.ID, unknown.ID} {
		selected, errPick := selector.Pick(context.Background(), "codex", "model", cliproxyexecutor.Options{}, []*Auth{expired, exhausted, unknown})
		if errPick != nil || selected == nil || selected.ID != want {
			t.Fatalf("selected = %v, error = %v; want %s", selected, errPick, want)
		}
	}
	if exhausted.Unavailable || exhausted.Quota.Exceeded {
		t.Fatal("passive observations must not mutate execution cooldown state")
	}
}

func TestManagerEarliestResetAffinityAndFailover(t *testing.T) {
	withQuotaCooldownEnabled(t)
	for _, provider := range []string{"codex", "claude"} {
		for _, mixed := range []bool{false, true} {
			t.Run(provider+"/mixed="+strconv.FormatBool(mixed), func(t *testing.T) {
				now := time.Now().Truncate(time.Second)
				fallback := &EarliestResetSelector{nowFunc: func() time.Time { return now }}
				affinity := NewSessionAffinitySelector(fallback)
				defer affinity.Stop()
				manager := NewManager(nil, affinity, nil)
				manager.RegisterExecutor(schedulerTestExecutor{provider: provider})
				const model = "earliest-reset-model"
				early := resetSelectorAuth("early-"+t.Name(), provider, now, time.Hour)
				later := resetSelectorAuth("later-"+t.Name(), provider, now, 2*time.Hour)
				for _, auth := range []*Auth{early, later} {
					if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
						t.Fatal(errRegister)
					}
					registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
				}
				pick := func(session, want string) {
					t.Helper()
					opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: session}}
					var selected *Auth
					var errPick error
					if mixed {
						selected, _, _, errPick = manager.pickNextMixed(context.Background(), []string{provider}, model, opts, nil)
					} else {
						selected, _, errPick = manager.pickNext(context.Background(), provider, model, opts, nil)
					}
					if errPick != nil || selected == nil || selected.ID != want {
						t.Fatalf("selected = %v, error = %v; want %s", selected, errPick, want)
					}
				}
				pick("long-thread", early.ID)
				manager.mu.Lock()
				manager.auths[later.ID].Quota = resetSelectorAuth(later.ID, provider, now, time.Minute).Quota
				manager.mu.Unlock()
				pick("long-thread", early.ID)
				pick("new-thread", later.ID)
				retryAfter := time.Hour
				manager.MarkResult(context.Background(), Result{AuthID: early.ID, Provider: provider, Model: model,
					Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"}, RetryAfter: &retryAfter})
				pick("long-thread", later.ID)
			})
		}
	}
}

func TestManagerEarliestResetHonorsResolvedModelAvailability(t *testing.T) {
	withQuotaCooldownEnabled(t)
	now := time.Now().Truncate(time.Second)
	manager := NewManager(nil, &EarliestResetSelector{nowFunc: func() time.Time { return now }}, nil)
	manager.RegisterExecutor(schedulerTestExecutor{provider: "codex"})
	const route, target, other = "earliest-route", "earliest-target", "earliest-other"
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		"codex": {{Name: target, Alias: route, Fork: true}},
	})
	early := resetSelectorAuth("early-alias", "codex", now, time.Hour)
	later := resetSelectorAuth("later-alias", "codex", now, 2*time.Hour)
	for _, auth := range []*Auth{early, later} {
		if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
			t.Fatal(errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: route}, {ID: target}, {ID: other}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	}
	retryAfter := time.Hour
	manager.MarkResult(context.Background(), Result{AuthID: early.ID, Provider: "codex", Model: other,
		Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "other model quota"}, RetryAfter: &retryAfter})
	selected, _, errPick := manager.pickNext(context.Background(), "codex", route, cliproxyexecutor.Options{}, nil)
	if errPick != nil || selected == nil || selected.ID != early.ID {
		t.Fatalf("selected = %v, error = %v; unrelated model cooldown must not override resolved availability", selected, errPick)
	}
	manager.MarkResult(context.Background(), Result{AuthID: early.ID, Provider: "codex", Model: target,
		Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "target model quota"}, RetryAfter: &retryAfter})
	selected, _, errPick = manager.pickNext(context.Background(), "codex", route, cliproxyexecutor.Options{}, nil)
	if errPick != nil || selected == nil || selected.ID != later.ID {
		t.Fatalf("selected = %v, error = %v; target model cooldown must trigger failover", selected, errPick)
	}
}
