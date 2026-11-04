package cliproxy

import (
	"context"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestWeightedRoundRobinRoutingSelector(t *testing.T) {
	cfg := &internalconfig.Config{Routing: internalconfig.RoutingConfig{Strategy: "wrr"}}
	want := internalconfig.SelectionConfig{Primary: internalconfig.SelectionReserves, Tiebreak: internalconfig.SelectionWeightedRoundRobin}
	if got := cfg.Routing.EffectiveSelection(); got != want {
		t.Fatalf("EffectiveSelection() = %+v, want %+v", got, want)
	}
	selector := newRoutingSelector(normalizedRoutingRuntimeState(cfg), cfg.Routing.EffectiveSelection)
	if _, ok := selector.(*coreauth.PipelineSelector); !ok {
		t.Fatalf("selector type = %T, want *auth.PipelineSelector", selector)
	}
}

func TestServiceRejectsInvalidCredentialWeightConfigCommit(t *testing.T) {
	originalCfg := &internalconfig.Config{}
	service := &Service{cfg: originalCfg}
	invalidWeight := internalconfig.MaxCredentialWeight + 1
	newCfg := &internalconfig.Config{
		VertexCompatAPIKey: []internalconfig.VertexCompatKey{{
			APIKey: "vertex-key",
			Weight: &invalidWeight,
		}},
	}

	if service.applyConfigUpdateWithAuthSynthesis(nil, newCfg, true) {
		t.Fatal("hot config application accepted an invalid credential weight")
	}
	if service.cfg != originalCfg {
		t.Fatal("invalid hot config replaced the active config")
	}
	if service.configSequence != 0 {
		t.Fatalf("config sequence = %d, want 0", service.configSequence)
	}
}

type trackingStoppableSelector struct {
	stopped bool
}

func (s *trackingStoppableSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*coreauth.Auth) (*coreauth.Auth, error) {
	return nil, nil
}

func (s *trackingStoppableSelector) Stop() {
	s.stopped = true
}

func TestApplyManagerConfigStopsReplacedServiceAffinitySelector(t *testing.T) {
	tracking := &trackingStoppableSelector{}
	service := &Service{
		coreManager: coreauth.NewManager(nil, tracking, nil),
	}

	newCfg := &internalconfig.Config{
		Routing: internalconfig.RoutingConfig{
			Strategy: "round-robin",
		},
	}
	commit := configCommit{cfg: newCfg, sequence: 1}
	if !service.applyManagerConfig(context.Background(), commit) {
		t.Fatal("applyManagerConfig failed")
	}

	if !tracking.stopped {
		t.Fatal("expected replaced selector to be stopped during routing config apply")
	}
}

func TestSelectionOnlyHotReloadKeepsPipelineSelectorAndCursor(t *testing.T) {
	for _, affinity := range []bool{false, true} {
		t.Run(map[bool]string{false: "pipeline", true: "affinity"}[affinity], func(t *testing.T) {
			manager := coreauth.NewManager(nil, nil, nil)
			service := &Service{coreManager: manager}
			apply := func(selection internalconfig.SelectionConfig, strategy string) {
				t.Helper()
				commit := service.commitConfigUpdate(&internalconfig.Config{Routing: internalconfig.RoutingConfig{
					Strategy: strategy, Selection: selection, SessionAffinity: affinity, SessionAffinityTTL: "1h",
				}})
				if !service.applyManagerConfig(context.Background(), commit) {
					t.Fatal("configuration application failed")
				}
			}
			apply(internalconfig.SelectionConfig{}, "")
			selector := manager.Selector()
			if stoppable, ok := selector.(coreauth.StoppableSelector); ok {
				defer stoppable.Stop()
			}
			auths := []*coreauth.Auth{
				{ID: "a", Provider: "codex", Status: coreauth.StatusActive},
				{ID: "b", Provider: "codex", Status: coreauth.StatusActive},
				{ID: "c", Provider: "codex", Status: coreauth.StatusActive},
			}
			pick := func() string {
				t.Helper()
				got, errPick := selector.Pick(context.Background(), "codex", "", cliproxyexecutor.Options{}, auths)
				if errPick != nil {
					t.Fatal(errPick)
				}
				return got.ID
			}
			if got := pick(); got != "a" {
				t.Fatalf("first pick = %s, want a", got)
			}

			// A new selection and strategy keep the selector and its round-robin cursor.
			apply(internalconfig.SelectionConfig{Primary: internalconfig.SelectionPriority, Tiebreak: internalconfig.SelectionRoundRobin}, "fill-first")
			if manager.Selector() != selector {
				t.Fatal("a selection-only reload replaced the selector")
			}
			if got := pick(); got != "b" {
				t.Fatalf("pick after reload = %s, want the cursor to continue at b", got)
			}

			// The shared selection is read at Pick time.
			apply(internalconfig.SelectionConfig{Primary: internalconfig.SelectionFillFirst}, "")
			if manager.Selector() != selector {
				t.Fatal("a selection-only reload replaced the selector")
			}
			for range 2 {
				if got := pick(); got != "a" {
					t.Fatalf("fill-first pick = %s, want a", got)
				}
			}
		})
	}
}
