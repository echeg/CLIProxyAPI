package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
)

const stickyProfileModel = "sticky-profile-model"

func stickyProfileContext(hash string, sticky bool) context.Context {
	return withRoutingProfile(context.Background(), &routingProfile{keyHash: hash, fromKey: true, sticky: sticky})
}

func stickyProfileSelector(t *testing.T, ttl time.Duration, selection internalconfig.SelectionConfig) *SessionAffinitySelector {
	t.Helper()
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: NewPipelineSelector(func() internalconfig.SelectionConfig { return selection }),
		TTL:      ttl,
	})
	t.Cleanup(selector.Stop)
	return selector
}

// stickyProfileOptions builds a request carrying either an explicit session header or
// an LCP-compatible body.
func stickyProfileOptions(mode, session string) cliproxyexecutor.Options {
	opts := cliproxyexecutor.Options{Metadata: map[string]any{}}
	switch mode {
	case "explicit":
		opts.Headers = http.Header{"X-Session-Id": []string{session}}
	case "lcp":
		opts.SourceFormat = sdktranslator.FormatOpenAI
		opts.OriginalRequest = []byte(fmt.Sprintf(`{"messages":[{"role":"system","content":%q},{"role":"user","content":"hello"}]}`, session))
		opts.Metadata[cliproxyexecutor.CallerScopeMetadataKey] = "sticky-profile-caller"
	}
	return opts
}

// stickyProfileBound reports whether the selector holds a binding for the request:
// a session cache entry for an explicit session, a prefix match for an LCP body.
func stickyProfileBound(selector *SessionAffinitySelector, mode, session string) (string, bool) {
	if mode == "explicit" {
		authID, status := selector.LookupAffinity("codex", stickyProfileModel, session)
		return authID, status == "bound"
	}
	opts := stickyProfileOptions(mode, session)
	namespace := lcpAffinityNamespace("codex", stickyProfileModel, opts.Metadata)
	turns := cliproxysession.ExtractCanonicalTurns(opts.SourceFormat, opts.OriginalRequest)
	fingerprints, minPrefixLength, tailFingerprints, envDigest := selector.matcher.PrepareExt(turns)
	match, ok := selector.matcher.MatchFingerprintsWithContext(namespace, fingerprints, tailFingerprints, envDigest, minPrefixLength)
	if !ok {
		return "", false
	}
	return match.AuthID, true
}

func stickyProfileAuths() []*Auth {
	return []*Auth{
		{ID: "a", Provider: "codex", Status: StatusActive},
		{ID: "b", Provider: "codex", Status: StatusActive},
	}
}

func TestSessionAffinityStickyOffLeavesNoBinding(t *testing.T) {
	roundRobin := internalconfig.SelectionConfig{Primary: internalconfig.SelectionRoundRobin}
	for _, mode := range []string{"explicit", "lcp"} {
		for _, success := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/success=%t", mode, success), func(t *testing.T) {
				selector := stickyProfileSelector(t, time.Hour, roundRobin)
				auths := stickyProfileAuths()
				off := stickyProfileContext("key-off", false)
				on := stickyProfileContext("key-on", true)
				result := func(authID string, opts cliproxyexecutor.Options) Result {
					res := Result{AuthID: authID, Provider: "codex", Model: stickyProfileModel, Success: success, Options: opts}
					if !success {
						res.Error = &Error{Code: "rate_limited", Message: "rate limited", HTTPStatus: http.StatusTooManyRequests}
					}
					return res
				}

				// A sticky-off key neither binds nor reuses: the same session rotates.
				var picked []string
				for range 2 {
					opts := stickyProfileOptions(mode, "sess-off")
					auth, errPick := selector.Pick(off, "codex", stickyProfileModel, opts, auths)
					if errPick != nil {
						t.Fatalf("sticky-off Pick error = %v", errPick)
					}
					if disabled, _ := opts.Metadata[cliproxyexecutor.SessionAffinityDisabledMetadataKey].(bool); !disabled {
						t.Fatal("sticky-off Pick did not mark the request")
					}
					if _, ok := opts.Metadata[cliproxyexecutor.LCPFingerprintMetadataKey]; ok {
						t.Fatal("sticky-off Pick ran the LCP matcher")
					}
					selector.OnResult(result(auth.ID, opts))
					picked = append(picked, auth.ID)
				}
				if picked[0] == picked[1] {
					t.Fatalf("sticky-off picks = %v, want round-robin rotation", picked)
				}
				if n := selector.cache.Len(); n != 0 {
					t.Fatalf("sticky-off left %d cache entries", n)
				}
				if authID, ok := stickyProfileBound(selector, mode, "sess-off"); ok {
					t.Fatalf("sticky-off left a binding to %s", authID)
				}

				// Another key on the same selector still binds.
				onOpts := stickyProfileOptions(mode, "sess-shared")
				bound, errPick := selector.Pick(on, "codex", stickyProfileModel, onOpts, auths)
				if errPick != nil {
					t.Fatalf("sticky-on Pick error = %v", errPick)
				}
				if disabled, _ := onOpts.Metadata[cliproxyexecutor.SessionAffinityDisabledMetadataKey].(bool); disabled {
					t.Fatal("sticky-on Pick carries the disabled marker")
				}
				if authID, ok := stickyProfileBound(selector, mode, "sess-shared"); !ok || authID != bound.ID {
					t.Fatalf("sticky-on binding = %q/%t, want %s", authID, ok, bound.ID)
				}
				var expiresBefore time.Time
				if mode == "explicit" {
					expiresBefore = stickyProfileOnlyEntry(t, selector).expiresAt
				}

				// A sticky-off result for the same session and credential neither deletes nor
				// refreshes the other key's binding.
				offOpts := stickyProfileOptions(mode, "sess-shared")
				if _, errPick := selector.Pick(off, "codex", stickyProfileModel, offOpts, auths); errPick != nil {
					t.Fatalf("sticky-off Pick error = %v", errPick)
				}
				selector.OnResult(result(bound.ID, offOpts))
				if authID, ok := stickyProfileBound(selector, mode, "sess-shared"); !ok || authID != bound.ID {
					t.Fatalf("sticky-off result changed the binding: %q/%t, want %s", authID, ok, bound.ID)
				}
				if mode == "explicit" {
					if after := stickyProfileOnlyEntry(t, selector).expiresAt; !after.Equal(expiresBefore) {
						t.Fatalf("sticky-off result refreshed the binding: %v -> %v", expiresBefore, after)
					}
				}
			})
		}
	}
}

func stickyProfileOnlyEntry(t *testing.T, selector *SessionAffinitySelector) sessionEntry {
	t.Helper()
	selector.cache.mu.RLock()
	defer selector.cache.mu.RUnlock()
	if len(selector.cache.entries) != 1 {
		t.Fatalf("cache entries = %d, want 1", len(selector.cache.entries))
	}
	for _, entry := range selector.cache.entries {
		return entry
	}
	return sessionEntry{}
}

func TestSessionAffinityOnResultSkipsDisabledMarker(t *testing.T) {
	selector := stickyProfileSelector(t, time.Hour, internalconfig.DefaultSelection())
	auths := stickyProfileAuths()
	opts := stickyProfileOptions("explicit", "sess-marker")
	bound, errPick := selector.Pick(context.Background(), "codex", stickyProfileModel, opts, auths)
	if errPick != nil {
		t.Fatal(errPick)
	}
	marked := stickyProfileOptions("explicit", "sess-marker")
	marked.Metadata[cliproxyexecutor.SessionAffinityDisabledMetadataKey] = true
	selector.OnResult(Result{AuthID: bound.ID, Provider: "codex", Model: stickyProfileModel, Error: &Error{HTTPStatus: http.StatusTooManyRequests}, Options: marked})
	if authID, status := selector.LookupAffinity("codex", stickyProfileModel, "sess-marker"); status != "bound" || authID != bound.ID {
		t.Fatalf("marked failure released the binding: %s/%s", authID, status)
	}
	selector.OnResult(Result{AuthID: bound.ID, Provider: "codex", Model: stickyProfileModel, Error: &Error{HTTPStatus: http.StatusTooManyRequests}, Options: opts})
	if _, status := selector.LookupAffinity("codex", stickyProfileModel, "sess-marker"); status != "unbound" {
		t.Fatalf("unmarked failure kept the binding: %s", status)
	}
}

func TestSharedProfileStickyFollowsConfiguredWrapper(t *testing.T) {
	for _, tt := range []struct {
		name       string
		shared     bool
		keySticky  bool
		wantShared bool
	}{
		// Shared on: requests without a key profile bind.
		{name: "shared on", shared: true, keySticky: false, wantShared: true},
		// Only a key profile enables affinity: the shared profile must not bind.
		{name: "key only", shared: false, keySticky: true, wantShared: false},
		// Nothing configured: a wrapper exists only when an SDK embedder installed it.
		{name: "embedder wrapper", shared: false, keySticky: false, wantShared: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := routingProfileTestConfig()
			cfg.Routing.SessionAffinity = tt.shared
			cfg.Routing.KeyProfiles[routingTestKeyA].SessionAffinity = tt.keySticky
			m := NewManager(nil, &RoundRobinSelector{}, nil)
			m.SetConfig(cfg)
			if got := m.resolveRoutingProfile(ginContextWithKey(routingTestKeyB)); got.sticky != tt.wantShared {
				t.Fatalf("shared profile sticky = %t, want %t", got.sticky, tt.wantShared)
			}
			if got := m.resolveRoutingProfile(ginContextWithKey(routingTestKeyA)); got.sticky != tt.keySticky {
				t.Fatalf("key profile sticky = %t, want %t", got.sticky, tt.keySticky)
			}
		})
	}
}

func TestSessionAffinitySharedOffKeyOnBindsOnlyThatKey(t *testing.T) {
	cfg := routingProfileTestConfig()
	cfg.Routing.SessionAffinity = false
	cfg.Routing.KeyProfiles[routingTestKeyA].SessionAffinity = true
	m := NewManager(nil, &RoundRobinSelector{}, nil)
	m.SetConfig(cfg)
	selector := stickyProfileSelector(t, time.Hour, internalconfig.DefaultSelection())
	auths := stickyProfileAuths()

	sharedCtx := withRoutingProfile(context.Background(), m.resolveRoutingProfile(ginContextWithKey(routingTestKeyB)))
	if _, errPick := selector.Pick(sharedCtx, "codex", stickyProfileModel, stickyProfileOptions("explicit", "sess-shared"), auths); errPick != nil {
		t.Fatal(errPick)
	}
	if _, status := selector.LookupAffinity("codex", stickyProfileModel, "sess-shared"); status != "unbound" {
		t.Fatalf("shared request bound a session: %s", status)
	}

	keyCtx := withRoutingProfile(context.Background(), m.resolveRoutingProfile(ginContextWithKey(routingTestKeyA)))
	bound, errPick := selector.Pick(keyCtx, "codex", stickyProfileModel, stickyProfileOptions("explicit", "sess-key"), auths)
	if errPick != nil {
		t.Fatal(errPick)
	}
	if authID, status := selector.LookupAffinity("codex", stickyProfileModel, "sess-key"); status != "bound" || authID != bound.ID {
		t.Fatalf("key request binding = %s/%s, want %s", authID, status, bound.ID)
	}
}

func TestSessionAffinityKeyBindingUsesSharedTTL(t *testing.T) {
	const ttl = 2 * time.Hour
	selector := stickyProfileSelector(t, ttl, internalconfig.DefaultSelection())
	auths := stickyProfileAuths()
	ctx := stickyProfileContext("key-ttl", true)

	before := time.Now()
	first, errPick := selector.Pick(ctx, "codex", stickyProfileModel, stickyProfileOptions("explicit", "sess-ttl"), auths)
	if errPick != nil {
		t.Fatal(errPick)
	}
	after := time.Now()
	entry := stickyProfileOnlyEntry(t, selector)
	if entry.expiresAt.Before(before.Add(ttl)) || entry.expiresAt.After(after.Add(ttl)) {
		t.Fatalf("binding expires at %v, want within [%v, %v]", entry.expiresAt, before.Add(ttl), after.Add(ttl))
	}

	// Expire the binding by editing its deadline; the next request is a new selection.
	selector.cache.mu.Lock()
	for key, current := range selector.cache.entries {
		current.expiresAt = time.Now().Add(-time.Minute)
		selector.cache.entries[key] = current
	}
	selector.cache.mu.Unlock()
	if _, status := selector.LookupAffinity("codex", stickyProfileModel, "sess-ttl"); status != "unbound" {
		t.Fatalf("expired binding status = %s, want unbound", status)
	}
	second, errPick := selector.Pick(ctx, "codex", stickyProfileModel, stickyProfileOptions("explicit", "sess-ttl"), auths)
	if errPick != nil {
		t.Fatal(errPick)
	}
	if second.ID == first.ID {
		t.Fatalf("expired binding was reused: %s; want the round-robin cursor to move on", second.ID)
	}
}

func TestSessionAffinityColdSelectionReachesPipelineAcrossTiers(t *testing.T) {
	observedAt := time.Now().Add(-time.Minute)
	auths := []*Auth{
		reserveSpecAuth("a-hard", reserveSpec{priority: "10", mode: QuotaReserveModeHard, used: 90}, observedAt),
		reserveSpecAuth("b-soft1", reserveSpec{priority: "10", mode: QuotaReserveModeSoft, used: 80}, observedAt),
		reserveSpecAuth("c-soft3", reserveSpec{priority: "0", mode: QuotaReserveModeSoft, steps: []int{15, 5}, used: 97}, observedAt),
		reserveSpecAuth("d-free", reserveSpec{priority: "5"}, observedAt),
	}
	want := []string{"b-soft1", "c-soft3", "d-free"}
	roundRobin := internalconfig.SelectionConfig{Primary: internalconfig.SelectionRoundRobin}

	for _, tt := range []struct {
		name   string
		sticky bool
		mode   string
	}{
		{name: "cold binding", sticky: true, mode: "explicit"},
		{name: "cold LCP binding", sticky: true, mode: "lcp"},
		{name: "no session", sticky: true, mode: ""},
		{name: "sticky off", sticky: false, mode: "explicit"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			selector := stickyProfileSelector(t, time.Hour, roundRobin)
			ctx := stickyProfileContext("key-cold", tt.sticky)
			seen := map[string]bool{}
			for i := range 2 * len(want) {
				auth, errPick := selector.Pick(ctx, "codex", stickyProfileModel, stickyProfileOptions(tt.mode, fmt.Sprintf("sess-%d", i)), auths)
				if errPick != nil {
					t.Fatalf("Pick error = %v", errPick)
				}
				seen[auth.ID] = true
			}
			got := make([]string, 0, len(seen))
			for id := range seen {
				got = append(got, id)
			}
			sort.Strings(got)
			// Every tier and soft level reaches the pipeline; the hard reserve does not.
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("cold selections = %v, want %v", got, want)
			}
		})
	}

	t.Run("preferred at soft3", func(t *testing.T) {
		selector := stickyProfileSelector(t, time.Hour, internalconfig.DefaultSelection())
		preferred := auths[2].Clone()
		preferred.EnsureIndex()
		withIndex := []*Auth{auths[0], auths[1], preferred, auths[3]}
		ctx := withRoutingProfile(context.Background(), &routingProfile{fromKey: true, sticky: true, preferred: map[string]string{"codex": preferred.Index}})
		auth, errPick := selector.Pick(ctx, "codex", stickyProfileModel, stickyProfileOptions("explicit", "sess-preferred"), withIndex)
		if errPick != nil {
			t.Fatal(errPick)
		}
		if auth.ID != "c-soft3" {
			t.Fatalf("cold preferred selection = %s, want c-soft3", auth.ID)
		}
	})

	t.Run("only hard reserved", func(t *testing.T) {
		selector := stickyProfileSelector(t, time.Hour, roundRobin)
		_, errPick := selector.Pick(stickyProfileContext("key-cold", true), "codex", stickyProfileModel, stickyProfileOptions("explicit", "sess-hard"), auths[:1])
		var cooldown *modelCooldownError
		if !errors.As(errPick, &cooldown) {
			t.Fatalf("Pick error = %v, want the reserve cooldown", errPick)
		}
	})
}

func TestSessionAffinityStickyStageLog(t *testing.T) {
	hook := newStandardLogHook(t)
	previous := log.GetLevel()
	log.SetLevel(log.InfoLevel)
	defer log.SetLevel(previous)

	selector := stickyProfileSelector(t, time.Hour, internalconfig.DefaultSelection())
	auths := stickyProfileAuths()
	for _, tt := range []struct {
		name    string
		ctx     context.Context
		wantLog bool
	}{
		{name: "key profile", ctx: stickyProfileContext("abc123def456", true), wantLog: true},
		{name: "shared profile", ctx: withRoutingProfile(context.Background(), &routingProfile{keyHash: "shared", sticky: true})},
	} {
		t.Run(tt.name, func(t *testing.T) {
			session := "sess-log-" + tt.name
			if _, errPick := selector.Pick(tt.ctx, "codex", stickyProfileModel, stickyProfileOptions("explicit", session), auths); errPick != nil {
				t.Fatal(errPick)
			}
			hook.Reset()
			reused, errPick := selector.Pick(tt.ctx, "codex", stickyProfileModel, stickyProfileOptions("explicit", session), auths)
			if errPick != nil {
				t.Fatal(errPick)
			}
			var found bool
			for _, entry := range hook.AllEntries() {
				if entry.Message != "Routing profile selection" {
					continue
				}
				found = true
				if entry.Data["routing_stage"] != routingStageSticky || entry.Data["routing_key"] != "abc123def456" || entry.Data["auth_index"] != reused.Clone().EnsureIndex() {
					t.Fatalf("log fields = %v", entry.Data)
				}
			}
			if found != tt.wantLog {
				t.Fatalf("sticky log found = %t, want %t", found, tt.wantLog)
			}
		})
	}
}

// A binding to a credential whose weight drops to zero is not served again while
// weighted-round-robin is an active pipeline slot, as with the legacy weighted
// fallback; under any other selection the binding is kept.
func TestSessionAffinityPipelineWeightedBindingRebindsAfterWeightBecomesZero(t *testing.T) {
	cases := []struct {
		name      string
		selection internalconfig.SelectionConfig
		want      string
	}{
		{name: "weighted tiebreak", selection: internalconfig.SelectionConfig{Primary: internalconfig.SelectionReserves, Tiebreak: internalconfig.SelectionWeightedRoundRobin}, want: "auth-b"},
		{name: "weighted primary", selection: internalconfig.SelectionConfig{Primary: internalconfig.SelectionWeightedRoundRobin}, want: "auth-b"},
		{name: "round-robin keeps binding", selection: internalconfig.SelectionConfig{Primary: internalconfig.SelectionReserves, Tiebreak: internalconfig.SelectionRoundRobin}, want: "auth-a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			selector := stickyProfileSelector(t, time.Hour, tc.selection)
			authA := &Auth{ID: "auth-a", Provider: "codex", Attributes: map[string]string{AttributeWeight: "1"}}
			authB := &Auth{ID: "auth-b", Provider: "codex", Attributes: map[string]string{AttributeWeight: "1"}}
			auths := []*Auth{authA, authB}
			opts := stickyProfileOptions("explicit", "weight-change")
			first, errFirst := selector.Pick(context.Background(), "codex", stickyProfileModel, opts, auths)
			if errFirst != nil || first.ID != authA.ID {
				t.Fatalf("first Pick() = %v, %v; want auth-a", first, errFirst)
			}
			authA.Attributes[AttributeWeight] = "0"
			second, errSecond := selector.Pick(context.Background(), "codex", stickyProfileModel, stickyProfileOptions("explicit", "weight-change"), auths)
			if errSecond != nil || second.ID != tc.want {
				t.Fatalf("Pick() after weight update = %v, %v; want %s", second, errSecond, tc.want)
			}
		})
	}
}

// An explicit preference wins over a zero weight, so a zero-weight preferred
// credential stays selectable and keeps its binding under a weighted slot.
func TestSessionAffinityPipelineWeightedKeepsZeroWeightPreferredBinding(t *testing.T) {
	selector := stickyProfileSelector(t, time.Hour, internalconfig.SelectionConfig{Primary: internalconfig.SelectionReserves, Tiebreak: internalconfig.SelectionWeightedRoundRobin})
	preferred := &Auth{ID: "auth-a", Provider: "codex", Index: "idx-a", Attributes: map[string]string{AttributeWeight: "0"}}
	other := &Auth{ID: "auth-b", Provider: "codex", Index: "idx-b", Attributes: map[string]string{AttributeWeight: "5"}}
	ctx := withRoutingProfile(context.Background(), &routingProfile{fromKey: true, sticky: true, preferred: map[string]string{"codex": "idx-a"},
		selection: internalconfig.SelectionConfig{Primary: internalconfig.SelectionReserves, Tiebreak: internalconfig.SelectionWeightedRoundRobin}})
	for i := 0; i < 2; i++ {
		picked, errPick := selector.Pick(ctx, "codex", stickyProfileModel, stickyProfileOptions("explicit", "preferred-zero"), []*Auth{preferred, other})
		if errPick != nil || picked.ID != preferred.ID {
			t.Fatalf("Pick() #%d = %v, %v; want the zero-weight preferred auth-a", i, picked, errPick)
		}
	}
}

// An established binding outranks a preference set later: the session keeps its
// credential, and only a new session takes the preferred one.
func TestSessionAffinityPipelineBindingOutranksLaterPreference(t *testing.T) {
	selector := stickyProfileSelector(t, time.Hour, internalconfig.SelectionConfig{Primary: internalconfig.SelectionReserves, Tiebreak: internalconfig.SelectionRoundRobin})
	authA := &Auth{ID: "auth-a", Provider: "codex", Index: "idx-a"}
	authB := &Auth{ID: "auth-b", Provider: "codex", Index: "idx-b"}
	auths := []*Auth{authA, authB}
	first, errFirst := selector.Pick(stickyProfileContext("k", true), "codex", stickyProfileModel, stickyProfileOptions("explicit", "bound"), auths)
	if errFirst != nil || first.ID != authA.ID {
		t.Fatalf("first Pick() = %v, %v; want auth-a", first, errFirst)
	}
	ctx := withRoutingProfile(context.Background(), &routingProfile{keyHash: "k", fromKey: true, sticky: true, preferred: map[string]string{"codex": "idx-b"}})
	bound, errBound := selector.Pick(ctx, "codex", stickyProfileModel, stickyProfileOptions("explicit", "bound"), auths)
	if errBound != nil || bound.ID != authA.ID {
		t.Fatalf("bound session Pick() = %v, %v; want the bound auth-a", bound, errBound)
	}
	fresh, errFresh := selector.Pick(ctx, "codex", stickyProfileModel, stickyProfileOptions("explicit", "fresh"), auths)
	if errFresh != nil || fresh.ID != authB.ID {
		t.Fatalf("new session Pick() = %v, %v; want the preferred auth-b", fresh, errFresh)
	}
}

// Explicit identities such as prompt_cache_key are chosen by the client and can
// repeat across keys. A binding made under one key profile must not route another
// key profile, while keys on the shared profile keep sharing bindings.
func TestSessionAffinityExplicitBindingsAreScopedPerKeyProfile(t *testing.T) {
	selector := stickyProfileSelector(t, time.Hour, internalconfig.SelectionConfig{Primary: internalconfig.SelectionReserves, Tiebreak: internalconfig.SelectionRoundRobin})
	authA := &Auth{ID: "auth-a", Provider: "codex", Index: "idx-a"}
	authB := &Auth{ID: "auth-b", Provider: "codex", Index: "idx-b"}
	auths := []*Auth{authA, authB}
	keyA := withRoutingProfile(context.Background(), &routingProfile{keyHash: "key-a", fromKey: true, sticky: true, preferred: map[string]string{"codex": "idx-a"}})
	keyB := withRoutingProfile(context.Background(), &routingProfile{keyHash: "key-b", fromKey: true, sticky: true, preferred: map[string]string{"codex": "idx-b"}})
	pick := func(ctx context.Context) (*Auth, cliproxyexecutor.Options) {
		t.Helper()
		opts := stickyProfileOptions("explicit", "shared-session")
		auth, errPick := selector.Pick(ctx, "codex", stickyProfileModel, opts, auths)
		if errPick != nil {
			t.Fatal(errPick)
		}
		return auth, opts
	}

	if got, _ := pick(keyA); got.ID != authA.ID {
		t.Fatalf("key A Pick() = %s, want its preferred auth-a", got.ID)
	}
	gotB, optsB := pick(keyB)
	if gotB.ID != authB.ID {
		t.Fatalf("key B Pick() = %s, want its preferred auth-b, not key A's binding", gotB.ID)
	}
	if got, _ := pick(keyA); got.ID != authA.ID {
		t.Fatalf("key A rebinding = %s, want auth-a", got.ID)
	}

	// A failed result for key B releases only key B's binding.
	selector.OnResult(Result{AuthID: authB.ID, Provider: "codex", Model: stickyProfileModel, Options: optsB,
		Error: &Error{Code: "rate_limited", HTTPStatus: http.StatusTooManyRequests}})
	if _, ok := selector.cache.Get(sessionBindingKey("codex", cliproxysession.BoundSessionIdentity("shared-session"), canonicalModelKey(stickyProfileModel), "key:key-b")); ok {
		t.Fatal("key B failure kept key B's binding")
	}
	if got, _ := pick(keyA); got.ID != authA.ID {
		t.Fatalf("key A after key B failure = %s, want auth-a", got.ID)
	}

	// Two keys on the shared profile share one binding, as before.
	sharedOne := withRoutingProfile(context.Background(), &routingProfile{keyHash: "shared-1", sticky: true})
	sharedTwo := withRoutingProfile(context.Background(), &routingProfile{keyHash: "shared-2", sticky: true})
	opts := stickyProfileOptions("explicit", "shared-profile-session")
	first, errFirst := selector.Pick(sharedOne, "codex", stickyProfileModel, opts, auths)
	if errFirst != nil {
		t.Fatal(errFirst)
	}
	if _, scoped := opts.Metadata[cliproxyexecutor.SessionAffinityScopeMetadataKey]; scoped {
		t.Fatal("shared-profile request carries a binding scope")
	}
	for range 3 {
		second, errSecond := selector.Pick(sharedTwo, "codex", stickyProfileModel, stickyProfileOptions("explicit", "shared-profile-session"), auths)
		if errSecond != nil || second.ID != first.ID {
			t.Fatalf("shared-profile key Pick() = %v, %v; want the shared binding %s", second, errSecond, first.ID)
		}
	}
}
