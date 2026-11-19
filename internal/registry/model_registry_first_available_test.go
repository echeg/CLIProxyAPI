package registry

import "testing"

// GetFirstAvailableModelMatching keeps the newest-first order of
// GetFirstAvailableModel and skips the models without an available client of an
// accepted provider.
func TestGetFirstAvailableModelMatching(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("claude-client", "claude", []*ModelInfo{{ID: "newest-claude", Created: 300}})
	r.RegisterClient("codex-client", "codex", []*ModelInfo{{ID: "older-codex", Created: 200}})

	if got, err := r.GetFirstAvailableModel(""); err != nil || got != "newest-claude" {
		t.Fatalf("GetFirstAvailableModel = %q, %v; want newest-claude", got, err)
	}
	if got, err := r.GetFirstAvailableModelMatching("", nil); err != nil || got != "newest-claude" {
		t.Fatalf("nil accept = %q, %v; want newest-claude", got, err)
	}
	notClaude := func(provider string) bool { return provider != "claude" }
	if got, err := r.GetFirstAvailableModelMatching("", notClaude); err != nil || got != "older-codex" {
		t.Fatalf("accept without claude = %q, %v; want older-codex", got, err)
	}
	if got, err := r.GetFirstAvailableModelMatching("", func(string) bool { return false }); err == nil {
		t.Fatalf("accept rejecting every provider = %q, want an error", got)
	}
}

// A model shared by two providers counts only the available clients of the
// accepted providers: a healthy client of a rejected provider does not make it
// available when the accepted provider's client is cooling down.
func TestGetFirstAvailableModelMatchingCountsAcceptedProvidersOnly(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("shared-claude", "claude", []*ModelInfo{{ID: "shared-model", Created: 300}})
	r.RegisterClient("shared-codex", "codex", []*ModelInfo{{ID: "shared-model", Created: 300}})
	r.RegisterClient("older-codex", "codex", []*ModelInfo{{ID: "older-codex-model", Created: 200}})
	notClaude := func(provider string) bool { return provider != "claude" }

	if got, err := r.GetFirstAvailableModelMatching("", notClaude); err != nil || got != "shared-model" {
		t.Fatalf("healthy shared codex = %q, %v; want shared-model", got, err)
	}

	r.SetModelQuotaExceeded("shared-codex", "shared-model")
	if count := r.GetModelCount("shared-model"); count != 1 {
		t.Fatalf("GetModelCount(shared-model) = %d, want 1 (the claude client)", count)
	}
	if count := r.GetModelCountMatching("shared-model", notClaude); count != 0 {
		t.Fatalf("GetModelCountMatching(shared-model, not claude) = %d, want 0", count)
	}
	if got, err := r.GetFirstAvailableModelMatching("", notClaude); err != nil || got != "older-codex-model" {
		t.Fatalf("cooling-down shared codex = %q, %v; want older-codex-model", got, err)
	}

	r.ClearModelQuotaExceeded("shared-codex", "shared-model")
	r.SuspendClientModel("shared-codex", "shared-model", "test")
	if got, err := r.GetFirstAvailableModelMatching("", notClaude); err != nil || got != "older-codex-model" {
		t.Fatalf("suspended shared codex = %q, %v; want older-codex-model", got, err)
	}
	if got, err := r.GetFirstAvailableModelMatching("", nil); err != nil || got != "shared-model" {
		t.Fatalf("nil accept with a healthy claude client = %q, %v; want shared-model", got, err)
	}
}

// A client that lists an alias several times (an alias pool) registers it once
// per entry, so its cooldown or suspension removes all of its entries: "auto"
// then skips the pooled alias for an older model with a healthy client.
func TestGetFirstAvailableModelMatchingSkipsUnavailableAliasPool(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("pool-client", "openai-compatibility", []*ModelInfo{
		{ID: "pooled-alias", Created: 300},
		{ID: "pooled-alias", Created: 300},
	})
	r.RegisterClient("older-client", "codex", []*ModelInfo{{ID: "older-model", Created: 200}})
	acceptAll := func(string) bool { return true }

	if got, err := r.GetFirstAvailableModelMatching("", acceptAll); err != nil || got != "pooled-alias" {
		t.Fatalf("healthy pool = %q, %v; want pooled-alias", got, err)
	}

	r.SetModelQuotaExceeded("pool-client", "pooled-alias")
	if count := r.GetModelCountMatching("pooled-alias", acceptAll); count != 0 {
		t.Fatalf("GetModelCountMatching(pooled-alias) during cooldown = %d, want 0", count)
	}
	for _, accept := range []func(string) bool{nil, acceptAll} {
		if got, err := r.GetFirstAvailableModelMatching("", accept); err != nil || got != "older-model" {
			t.Fatalf("cooling-down pool = %q, %v; want older-model", got, err)
		}
	}

	r.ClearModelQuotaExceeded("pool-client", "pooled-alias")
	r.SuspendClientModel("pool-client", "pooled-alias", "test")
	if count := r.GetModelCount("pooled-alias"); count != 0 {
		t.Fatalf("GetModelCount(pooled-alias) while suspended = %d, want 0", count)
	}
	if got, err := r.GetFirstAvailableModelMatching("", acceptAll); err != nil || got != "older-model" {
		t.Fatalf("suspended pool = %q, %v; want older-model", got, err)
	}
}

// GetFirstRegisteredModelMatching ignores availability: a model whose only
// accepted client cools down is still returned, newest first.
func TestGetFirstRegisteredModelMatching(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("claude-client", "claude", []*ModelInfo{{ID: "newest-claude", Created: 300}})
	r.RegisterClient("codex-client", "codex", []*ModelInfo{{ID: "older-codex", Created: 200}})
	r.SetModelQuotaExceeded("claude-client", "newest-claude")
	r.SetModelQuotaExceeded("codex-client", "older-codex")
	onlyCodex := func(provider string) bool { return provider == "codex" }

	if got, err := r.GetFirstAvailableModelMatching("", nil); err == nil {
		t.Fatalf("GetFirstAvailableModelMatching with every client cooling down = %q, want an error", got)
	}
	if got, err := r.GetFirstRegisteredModelMatching("", func(string) bool { return true }); err != nil || got != "newest-claude" {
		t.Fatalf("accept all = %q, %v; want newest-claude", got, err)
	}
	if got, err := r.GetFirstRegisteredModelMatching("", onlyCodex); err != nil || got != "older-codex" {
		t.Fatalf("accept codex = %q, %v; want older-codex", got, err)
	}
	if got, err := r.GetFirstRegisteredModelMatching("", func(string) bool { return false }); err == nil {
		t.Fatalf("accept none = %q, want an error", got)
	}
}
