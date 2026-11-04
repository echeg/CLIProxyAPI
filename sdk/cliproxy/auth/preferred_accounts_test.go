package auth

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestPreferredAccountsPreserveExistingBindings(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		for _, mixed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/mixed=%t", provider, mixed), func(t *testing.T) {
				selector := NewSessionAffinitySelector(&FillFirstSelector{})
				t.Cleanup(selector.Stop)
				manager, accounts, model := preferredAccountTestManager(t, provider, selector)
				pick := preferredAccountTestPicker(t, manager, provider, model, mixed)
				none := preferredAccountContext(provider, "")
				pick(none, "old", accounts[0].ID)
				second := preferredAccountContext(provider, accounts[1].Index)
				pick(second, "new", accounts[1].ID)
				pick(second, "old", accounts[0].ID)
				third := preferredAccountContext(provider, accounts[2].Index)
				pick(third, "newer", accounts[2].ID)
				pick(third, "new", accounts[1].ID)
				pick(none, "automatic", accounts[0].ID)
				pick(none, "newer", accounts[2].ID)
			})
		}
	}
}

func TestPreferredAccountsWithoutAffinityUseEveryStrategy(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		for _, selector := range []Selector{&RoundRobinSelector{}, &WeightedRoundRobinSelector{}, &FillFirstSelector{}, &EarliestResetSelector{}} {
			t.Run(fmt.Sprintf("%T/mixed=%t", selector, mixed), func(t *testing.T) {
				manager, accounts, model := preferredAccountTestManager(t, "codex", selector)
				pick := preferredAccountTestPicker(t, manager, "codex", model, mixed)
				preferred := preferredAccountContext("codex", accounts[2].Index)
				pick(preferred, "first", accounts[2].ID)
				pick(preferred, "second", accounts[2].ID)
				pick(preferredAccountContext("codex", ""), "automatic", accounts[0].ID)
			})
		}
	}
}

func TestPreferredAccountsUnavailableFallback(t *testing.T) {
	for _, condition := range []string{"disabled", "cooldown", "unsupported", "removed", "zero-weight", "request-pin"} {
		t.Run(condition, func(t *testing.T) {
			selector := NewSessionAffinitySelector(&WeightedRoundRobinSelector{})
			t.Cleanup(selector.Stop)
			manager, accounts, model := preferredAccountTestManager(t, "codex", selector)
			ctx := preferredAccountContext("codex", accounts[1].Index)
			preferred := accounts[1].Clone()
			opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: "new"}}
			switch condition {
			case "disabled":
				preferred.Disabled = true
			case "cooldown":
				preferred.ModelStates = map[string]*ModelState{model: {
					Unavailable: true, NextRetryAfter: time.Now().Add(time.Hour),
					Quota: QuotaState{Exceeded: true, NextRecoverAt: time.Now().Add(time.Hour)},
				}}
			case "unsupported":
				registry.GetGlobalRegistry().UnregisterClient(preferred.ID)
			case "removed":
				ctx = preferredAccountContext("codex", "missing-account")
			case "zero-weight":
				preferred.Attributes["weight"] = "0"
			case "request-pin":
				opts.Metadata[cliproxyexecutor.PinnedAuthMetadataKey] = accounts[0].ID
			}
			if _, errUpdate := manager.Update(WithSkipPersist(context.Background()), preferred); errUpdate != nil {
				t.Fatal(errUpdate)
			}
			selected, _, errPick := manager.pickNext(ctx, "codex", model, opts, nil)
			if errPick != nil || selected == nil || selected.ID != accounts[0].ID {
				t.Fatalf("fallback = %+v, %v; want %s", selected, errPick, accounts[0].ID)
			}
		})
	}
}

func preferredAccountTestManager(t *testing.T, provider string, selector Selector) (*Manager, []*Auth, string) {
	t.Helper()
	manager := NewManager(nil, selector, nil)
	manager.RegisterExecutor(schedulerTestExecutor{provider: provider})
	model := "preferred-account-model"
	var accounts []*Auth
	for i := range 3 {
		priority := "0"
		if i == 0 {
			priority = "10"
		}
		auth := &Auth{ID: fmt.Sprintf("%s-%d", t.Name(), i), Provider: provider, Status: StatusActive,
			Attributes: map[string]string{"priority": priority}}
		registered, errRegister := manager.Register(WithSkipPersist(context.Background()), auth)
		if errRegister != nil {
			t.Fatal(errRegister)
		}
		accounts = append(accounts, registered)
		registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	}
	return manager, accounts, model
}

// setPreferredAccount writes the shared preference; requests without a context
// profile resolve it through the shared profile.
func setPreferredAccount(manager *Manager, provider, index string) {
	preferences := map[string]string{}
	if index != "" {
		preferences[provider] = index
	}
	manager.SetConfig(&config.Config{Routing: config.RoutingConfig{PreferredAccounts: preferences}})
}

// preferredAccountContext carries a request profile that prefers index for provider.
func preferredAccountContext(provider, index string) context.Context {
	profile := &routingProfile{}
	if index != "" {
		profile.preferred = map[string]string{provider: index}
	}
	return withRoutingProfile(context.Background(), profile)
}

func preferredAccountTestPicker(t *testing.T, manager *Manager, provider, model string, mixed bool) func(context.Context, string, string) {
	t.Helper()
	return func(ctx context.Context, session, want string) {
		t.Helper()
		opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: session}}
		var selected *Auth
		var errPick error
		if mixed {
			selected, _, _, errPick = manager.pickNextMixed(ctx, []string{provider}, model, opts, nil)
		} else {
			selected, _, errPick = manager.pickNext(ctx, provider, model, opts, nil)
		}
		if errPick != nil || selected == nil || selected.ID != want {
			t.Fatalf("session %s selected %+v, %v; want %s", session, selected, errPick, want)
		}
	}
}
