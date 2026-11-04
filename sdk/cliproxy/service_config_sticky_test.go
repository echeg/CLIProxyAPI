package cliproxy

import (
	"context"
	"net/http"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func stickyTestConfig(shared, key bool, selection internalconfig.SelectionConfig) *internalconfig.Config {
	return &internalconfig.Config{
		SDKConfig: internalconfig.SDKConfig{APIKeys: []string{"sk-sticky"}},
		Routing: internalconfig.RoutingConfig{
			SessionAffinity:    shared,
			SessionAffinityTTL: "1h",
			Selection:          selection,
			KeyProfiles: map[string]*internalconfig.RoutingProfile{
				"sk-sticky": {SessionAffinity: key},
			},
		},
	}
}

func TestRoutingSelectorWrapsAffinityWhenStickyAnywhere(t *testing.T) {
	for _, tt := range []struct {
		name        string
		shared, key bool
		wantWrapper bool
	}{
		{name: "nothing sticky", wantWrapper: false},
		{name: "shared only", shared: true, wantWrapper: true},
		{name: "key only", key: true, wantWrapper: true},
		{name: "both", shared: true, key: true, wantWrapper: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := stickyTestConfig(tt.shared, tt.key, internalconfig.SelectionConfig{})
			state := normalizedRoutingRuntimeState(cfg)
			if state.stickyAnywhere != tt.wantWrapper {
				t.Fatalf("stickyAnywhere = %t, want %t", state.stickyAnywhere, tt.wantWrapper)
			}
			selector := newRoutingSelector(state, cfg.Routing.EffectiveSelection)
			if stoppable, ok := selector.(coreauth.StoppableSelector); ok {
				defer stoppable.Stop()
			}
			_, wrapped := selector.(*coreauth.SessionAffinitySelector)
			if wrapped != tt.wantWrapper {
				t.Fatalf("selector type = %T, want wrapper=%t", selector, tt.wantWrapper)
			}
		})
	}
}

func TestStickyAnywhereToggleResetsBindings(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	service := &Service{coreManager: manager}
	apply := func(cfg *internalconfig.Config) coreauth.Selector {
		t.Helper()
		if !service.applyManagerConfig(context.Background(), service.commitConfigUpdate(cfg)) {
			t.Fatal("configuration application failed")
		}
		return manager.Selector()
	}
	defer func() {
		if stoppable, ok := manager.Selector().(coreauth.StoppableSelector); ok {
			stoppable.Stop()
		}
	}()
	auths := []*coreauth.Auth{
		{ID: "a", Provider: "codex", Status: coreauth.StatusActive},
		{ID: "b", Provider: "codex", Status: coreauth.StatusActive},
	}
	affinity := func(selector coreauth.Selector) *coreauth.SessionAffinitySelector {
		t.Helper()
		wrapper, ok := selector.(*coreauth.SessionAffinitySelector)
		if !ok {
			t.Fatalf("selector type = %T, want *auth.SessionAffinitySelector", selector)
		}
		return wrapper
	}
	status := func(selector coreauth.Selector) string {
		t.Helper()
		_, got := affinity(selector).LookupAffinity("codex", "m", "sess-toggle")
		return got
	}

	// A key profile alone builds the wrapper.
	first := apply(stickyTestConfig(false, true, internalconfig.SelectionConfig{}))
	opts := cliproxyexecutor.Options{Headers: http.Header{"X-Session-Id": []string{"sess-toggle"}}, Metadata: map[string]any{}}
	if _, errPick := affinity(first).Pick(context.Background(), "codex", "m", opts, auths); errPick != nil {
		t.Fatal(errPick)
	}
	if got := status(first); got != "bound" {
		t.Fatalf("binding status = %s, want bound", got)
	}

	// Moving stickiness between profiles or changing the selection keeps the wrapper
	// and its bindings.
	for _, cfg := range []*internalconfig.Config{
		stickyTestConfig(true, false, internalconfig.SelectionConfig{}),
		stickyTestConfig(true, false, internalconfig.SelectionConfig{Primary: internalconfig.SelectionPriority, Tiebreak: internalconfig.SelectionRoundRobin}),
	} {
		if got := apply(cfg); got != first {
			t.Fatal("a change that keeps stickyAnywhere replaced the selector")
		}
		if got := status(first); got != "bound" {
			t.Fatalf("binding status after reload = %s, want bound", got)
		}
	}

	// Turning stickiness off everywhere drops the wrapper.
	if got := apply(stickyTestConfig(false, false, internalconfig.SelectionConfig{})); got == first {
		t.Fatal("turning stickiness off kept the wrapper")
	} else if _, ok := got.(*coreauth.PipelineSelector); !ok {
		t.Fatalf("selector type = %T, want *auth.PipelineSelector", got)
	}

	// Turning it back on builds a new wrapper with no bindings.
	again := apply(stickyTestConfig(false, true, internalconfig.SelectionConfig{}))
	if again == first {
		t.Fatal("toggling stickiness reused the old wrapper")
	}
	if got := status(again); got != "unbound" {
		t.Fatalf("binding status after toggle = %s, want unbound", got)
	}
}
