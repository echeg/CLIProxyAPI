package auth

import (
	"context"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// coolAlternativeTarget puts authID into a model cooldown until recoverAt.
func coolAlternativeTarget(t *testing.T, m *Manager, authID string, recoverAt time.Time) {
	t.Helper()
	auth, ok := m.GetByID(authID)
	if !ok {
		t.Fatalf("auth %s not found", authID)
	}
	if auth.ModelStates == nil {
		auth.ModelStates = map[string]*ModelState{}
	}
	auth.ModelStates[apiFallbackModel] = &ModelState{Status: StatusError, Unavailable: true, NextRetryAfter: recoverAt}
	if _, errUpdate := m.Update(WithSkipPersist(context.Background()), auth); errUpdate != nil {
		t.Fatal(errUpdate)
	}
}

// disableAlternativeTarget disables authID.
func disableAlternativeTarget(t *testing.T, m *Manager, authID string) {
	t.Helper()
	auth, ok := m.GetByID(authID)
	if !ok {
		t.Fatalf("auth %s not found", authID)
	}
	auth.Disabled = true
	auth.Status = StatusDisabled
	if _, errUpdate := m.Update(WithSkipPersist(context.Background()), auth); errUpdate != nil {
		t.Fatal(errUpdate)
	}
}

func hasAlternativeTarget(m *Manager, provider, excluded string) bool {
	return m.HasAlternativeTargetForModel(context.Background(), []string{provider}, apiFallbackModel, cliproxyexecutor.Options{}, excluded)
}

// Baseline: without a fallback leg only primary-policy credentials count.
func TestHasAlternativeTargetForModelPrimaryOnly(t *testing.T) {
	noFallback := func() *internalconfig.Config { return &internalconfig.Config{} }

	t.Run("other subscription available", func(t *testing.T) {
		f := newAPIFallbackFixture(t, "codex", 2, noFallback())
		if !hasAlternativeTarget(f.m, "codex", f.oauthIDs[0]) {
			t.Fatal("expected the other subscription to count")
		}
	})
	t.Run("only the excluded credential", func(t *testing.T) {
		f := newAPIFallbackFixture(t, "codex", 1, noFallback())
		if hasAlternativeTarget(f.m, "codex", f.oauthIDs[0]) {
			t.Fatal("expected no alternative")
		}
	})
	t.Run("other subscription cooling down", func(t *testing.T) {
		f := newAPIFallbackFixture(t, "codex", 2, noFallback())
		coolAlternativeTarget(t, f.m, f.oauthIDs[1], time.Now().Add(time.Hour))
		if hasAlternativeTarget(f.m, "codex", f.oauthIDs[0]) {
			t.Fatal("expected a cooling subscription not to count")
		}
	})
	t.Run("other subscription recovered", func(t *testing.T) {
		f := newAPIFallbackFixture(t, "codex", 2, noFallback())
		coolAlternativeTarget(t, f.m, f.oauthIDs[1], time.Now().Add(-time.Minute))
		if !hasAlternativeTarget(f.m, "codex", f.oauthIDs[0]) {
			t.Fatal("expected a recovered subscription to count")
		}
	})
}

func TestHasAlternativeTargetForModelCountsAPIFallbackLeg(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider+"/api key available", func(t *testing.T) {
			f := newAPIFallbackFixture(t, provider, 1, nil)
			if !hasAlternativeTarget(f.m, provider, f.oauthIDs[0]) {
				t.Fatal("expected the API fallback leg to count")
			}
		})
		t.Run(provider+"/api key cooling down", func(t *testing.T) {
			f := newAPIFallbackFixture(t, provider, 1, nil)
			coolAlternativeTarget(t, f.m, f.keyID, time.Now().Add(time.Hour))
			if hasAlternativeTarget(f.m, provider, f.oauthIDs[0]) {
				t.Fatal("expected a cooling API key not to count")
			}
		})
		t.Run(provider+"/api key disabled", func(t *testing.T) {
			f := newAPIFallbackFixture(t, provider, 1, nil)
			disableAlternativeTarget(t, f.m, f.keyID)
			if hasAlternativeTarget(f.m, provider, f.oauthIDs[0]) {
				t.Fatal("expected a disabled API key not to count")
			}
		})
		t.Run(provider+"/api key excluded", func(t *testing.T) {
			f := newAPIFallbackFixture(t, provider, 1, nil)
			coolAlternativeTarget(t, f.m, f.oauthIDs[0], time.Now().Add(time.Hour))
			if hasAlternativeTarget(f.m, provider, f.keyID) {
				t.Fatal("expected the excluded API key not to count")
			}
		})
		t.Run(provider+"/no fallback configured", func(t *testing.T) {
			cfg := &internalconfig.Config{}
			policy := internalconfig.ProviderRoutingConfig{APICredential: provider + "-key-idx"}
			if provider == "claude" {
				cfg.Routing.Providers.Claude = policy
			} else {
				cfg.Routing.Providers.Codex = policy
			}
			f := newAPIFallbackFixture(t, provider, 1, cfg)
			if hasAlternativeTarget(f.m, provider, f.oauthIDs[0]) {
				t.Fatal("expected no alternative without a fallback")
			}
		})
		t.Run(provider+"/primary alternative wins", func(t *testing.T) {
			f := newAPIFallbackFixture(t, provider, 2, nil)
			// The API key is unusable, so only the primary check can return true.
			disableAlternativeTarget(t, f.m, f.keyID)
			if !hasAlternativeTarget(f.m, provider, f.oauthIDs[0]) {
				t.Fatal("expected the other subscription to count")
			}
		})
	}
}

func TestHasAlternativeTargetForModelIgnoresCodexFallbackTarget(t *testing.T) {
	const codexModel = "alt-target-codex-model"
	cfg := &internalconfig.Config{}
	cfg.Routing.Providers.Claude = internalconfig.ProviderRoutingConfig{Fallback: internalconfig.ProviderFallbackConfig{
		Target:   internalconfig.ProviderFallbackCodex,
		Mappings: []internalconfig.ClaudeFallbackMapping{{From: apiFallbackModel, To: codexModel}},
	}}
	f := newAPIFallbackFixture(t, "claude", 1, cfg)
	codexID := t.Name() + "-codex-oauth"
	registry.GetGlobalRegistry().RegisterClient(codexID, "codex", []*registry.ModelInfo{{ID: codexModel}, {ID: apiFallbackModel}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(codexID) })
	if _, errRegister := f.m.Register(WithSkipPersist(context.Background()), providerPolicyAuth(providerPolicyTestAuth{codexID, "codex", AuthKindOAuth, ""})); errRegister != nil {
		t.Fatal(errRegister)
	}
	if hasAlternativeTarget(f.m, "claude", f.oauthIDs[0]) {
		t.Fatal("expected a codex fallback target not to count")
	}
}
