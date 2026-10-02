package cliproxy

import (
	"context"
	"strconv"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"gopkg.in/yaml.v3"
)

func TestEarliestResetRoutingConfigWithAffinity(t *testing.T) {
	var cfg internalconfig.Config
	if errUnmarshal := yaml.Unmarshal([]byte(`routing:
  strategy: earliest-reset
  session-affinity: true
  session-affinity-ttl: 24h
`), &cfg); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	state := normalizedRoutingRuntimeState(&cfg)
	if state.strategy != "earliest-reset" || state.sessionAffinityTTL != 24*time.Hour {
		t.Fatalf("unexpected routing state: %+v", state)
	}
	selector := newRoutingSelector(state)
	affinity, ok := selector.(*coreauth.SessionAffinitySelector)
	if !ok {
		t.Fatalf("selector = %T, want session affinity", selector)
	}
	defer affinity.Stop()

	now := time.Now()
	makeAuth := func(id string, reset time.Time) *coreauth.Auth {
		return &coreauth.Auth{
			ID: id, Provider: "codex", Status: coreauth.StatusActive,
			Quota: coreauth.QuotaState{
				ObservedAt: now,
				Signals: map[string]string{
					"X-Codex-Primary-Used-Percent": "20",
					"X-Codex-Primary-Reset-At":     strconv.FormatInt(reset.Unix(), 10),
				},
			},
		}
	}
	later := makeAuth("a-later", now.Add(4*time.Hour))
	sooner := makeAuth("z-sooner", now.Add(2*time.Hour))
	auths := []*coreauth.Auth{later, sooner}
	pick := func(session, want string) {
		t.Helper()
		got, errPick := selector.Pick(context.Background(), "codex", "gpt-5", cliproxyexecutor.Options{
			Metadata: map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: session},
		}, auths)
		if errPick != nil {
			t.Fatal(errPick)
		}
		if got.ID != want {
			t.Fatalf("session %s picked %s, want %s", session, got.ID, want)
		}
	}
	pick("ongoing", sooner.ID)
	later.Quota.Signals["X-Codex-Primary-Reset-At"] = strconv.FormatInt(now.Add(time.Hour).Unix(), 10)
	pick("ongoing", sooner.ID)
	pick("new-thread", later.ID)

	cfg.Routing.SessionAffinity = false
	if _, ok := newRoutingSelector(normalizedRoutingRuntimeState(&cfg)).(*coreauth.EarliestResetSelector); !ok {
		t.Fatal("earliest-reset without affinity did not construct the quota selector")
	}
}

func TestPreferredAccountsConfigUpdatesPreserveSelector(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	service := &Service{coreManager: manager}
	apply := func(preferences map[string]string) {
		t.Helper()
		commit := service.commitConfigUpdate(&internalconfig.Config{Routing: internalconfig.RoutingConfig{
			Strategy: "earliest-reset", SessionAffinity: true, SessionAffinityTTL: "24h", PreferredAccounts: preferences,
		}})
		if !service.applyManagerConfig(context.Background(), commit) {
			t.Fatal("configuration application failed")
		}
	}
	apply(nil)
	selector := manager.Selector()
	defer selector.(coreauth.StoppableSelector).Stop()
	apply(map[string]string{"codex": "first-index"})
	if manager.Selector() != selector {
		t.Fatal("setting a preferred account replaced the affinity selector")
	}
	apply(map[string]string{"codex": "second-index"})
	if manager.Selector() != selector {
		t.Fatal("changing a preferred account replaced the affinity selector")
	}
	apply(nil)
	if manager.Selector() != selector {
		t.Fatal("clearing a preferred account replaced the affinity selector")
	}
}
