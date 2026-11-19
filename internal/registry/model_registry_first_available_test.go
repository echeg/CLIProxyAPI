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
	if got, err := r.GetFirstAvailableModelMatching("", nil, nil); err != nil || got != "newest-claude" {
		t.Fatalf("nil accept = %q, %v; want newest-claude", got, err)
	}
	notClaude := func(provider string) bool { return provider != "claude" }
	if got, err := r.GetFirstAvailableModelMatching("", notClaude, nil); err != nil || got != "older-codex" {
		t.Fatalf("accept without claude = %q, %v; want older-codex", got, err)
	}
	if got, err := r.GetFirstAvailableModelMatching("", func(string) bool { return false }, nil); err == nil {
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

	if got, err := r.GetFirstAvailableModelMatching("", notClaude, nil); err != nil || got != "shared-model" {
		t.Fatalf("healthy shared codex = %q, %v; want shared-model", got, err)
	}

	r.SetModelQuotaExceeded("shared-codex", "shared-model")
	if count := r.GetModelCount("shared-model"); count != 1 {
		t.Fatalf("GetModelCount(shared-model) = %d, want 1 (the claude client)", count)
	}
	if count := r.GetModelCountMatching("shared-model", notClaude); count != 0 {
		t.Fatalf("GetModelCountMatching(shared-model, not claude) = %d, want 0", count)
	}
	if got, err := r.GetFirstAvailableModelMatching("", notClaude, nil); err != nil || got != "older-codex-model" {
		t.Fatalf("cooling-down shared codex = %q, %v; want older-codex-model", got, err)
	}

	r.ClearModelQuotaExceeded("shared-codex", "shared-model")
	r.SuspendClientModel("shared-codex", "shared-model", "test")
	if got, err := r.GetFirstAvailableModelMatching("", notClaude, nil); err != nil || got != "older-codex-model" {
		t.Fatalf("suspended shared codex = %q, %v; want older-codex-model", got, err)
	}
	if got, err := r.GetFirstAvailableModelMatching("", nil, nil); err != nil || got != "shared-model" {
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

	if got, err := r.GetFirstAvailableModelMatching("", acceptAll, nil); err != nil || got != "pooled-alias" {
		t.Fatalf("healthy pool = %q, %v; want pooled-alias", got, err)
	}

	r.SetModelQuotaExceeded("pool-client", "pooled-alias")
	if count := r.GetModelCountMatching("pooled-alias", acceptAll); count != 0 {
		t.Fatalf("GetModelCountMatching(pooled-alias) during cooldown = %d, want 0", count)
	}
	for _, accept := range []func(string) bool{nil, acceptAll} {
		if got, err := r.GetFirstAvailableModelMatching("", accept, nil); err != nil || got != "older-model" {
			t.Fatalf("cooling-down pool = %q, %v; want older-model", got, err)
		}
	}

	r.ClearModelQuotaExceeded("pool-client", "pooled-alias")
	r.SuspendClientModel("pool-client", "pooled-alias", "test")
	if count := r.GetModelCount("pooled-alias"); count != 0 {
		t.Fatalf("GetModelCount(pooled-alias) while suspended = %d, want 0", count)
	}
	if got, err := r.GetFirstAvailableModelMatching("", acceptAll, nil); err != nil || got != "older-model" {
		t.Fatalf("suspended pool = %q, %v; want older-model", got, err)
	}
}

// GetFirstRegisteredModelMatching ignores availability: a model whose only
// accepted client cools down, or is suspended for a reason that hides it from the
// listing, is still returned, newest first.
func TestGetFirstRegisteredModelMatching(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("claude-client", "claude", []*ModelInfo{{ID: "newest-claude", Created: 300}})
	r.RegisterClient("codex-client", "codex", []*ModelInfo{{ID: "older-codex", Created: 200}})
	r.SetModelQuotaExceeded("claude-client", "newest-claude")
	r.SuspendClientModel("codex-client", "older-codex", "unauthorized")
	onlyCodex := func(provider string) bool { return provider == "codex" }

	if got, err := r.GetFirstAvailableModelMatching("", nil, nil); err == nil {
		t.Fatalf("GetFirstAvailableModelMatching with no available client = %q, want an error", got)
	}
	for _, model := range r.GetAvailableModels("") {
		if model["id"] == "older-codex" {
			t.Fatal("the listing shows older-codex; the test needs a model hidden by its suspension")
		}
	}
	for _, accept := range []func(string) bool{nil, func(string) bool { return true }} {
		if got, err := r.GetFirstRegisteredModelMatching(accept, nil); err != nil || got != "newest-claude" {
			t.Fatalf("accept all = %q, %v; want newest-claude", got, err)
		}
	}
	if got, err := r.GetFirstRegisteredModelMatching(onlyCodex, nil); err != nil || got != "older-codex" {
		t.Fatalf("accept codex = %q, %v; want older-codex", got, err)
	}
	if got, err := r.GetFirstRegisteredModelMatching(func(string) bool { return false }, nil); err == nil {
		t.Fatalf("accept none = %q, want an error", got)
	}

	r.UnregisterClient("codex-client")
	if got, err := r.GetFirstRegisteredModelMatching(onlyCodex, nil); err == nil {
		t.Fatalf("accept codex after unregistering it = %q, want an error", got)
	}
}

// The per-client entry counts follow re-registration and unregistration, so a
// pool that shrinks to one entry, or a client that is gone, is not over-counted.
func TestGetModelCountMatchingFollowsReRegisteredAliasPool(t *testing.T) {
	r := newTestModelRegistry()
	pool := []*ModelInfo{{ID: "pooled-alias", Created: 300}, {ID: "pooled-alias", Created: 300}}
	r.RegisterClient("pool-client", "openai-compatibility", pool)
	r.RegisterClient("other-client", "openai-compatibility", []*ModelInfo{{ID: "pooled-alias", Created: 300}})
	if count := r.GetModelCount("pooled-alias"); count != 3 {
		t.Fatalf("GetModelCount(pooled-alias) = %d, want 3", count)
	}
	r.SetModelQuotaExceeded("pool-client", "pooled-alias")
	if count := r.GetModelCount("pooled-alias"); count != 1 {
		t.Fatalf("count with the two-entry pool cooling down = %d, want 1", count)
	}

	r.RegisterClient("pool-client", "openai-compatibility", pool[:1])
	r.SetModelQuotaExceeded("pool-client", "pooled-alias")
	if count := r.GetModelCount("pooled-alias"); count != 1 {
		t.Fatalf("count with the shrunk pool cooling down = %d, want 1", count)
	}

	r.UnregisterClient("pool-client")
	if _, ok := r.clientModelCounts["pool-client"]; ok {
		t.Fatal("unregistered client keeps its entry counts")
	}
	if count := r.GetModelCount("pooled-alias"); count != 1 {
		t.Fatalf("count after unregistering the pool = %d, want 1", count)
	}
}

// Both lookups skip the models the model filter rejects and keep the
// newest-first order for the rest; the filter may read the registry.
func TestFirstModelMatchingSkipsRejectedModels(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("claude-client", "claude", []*ModelInfo{{ID: "shadowing-model", Created: 300}})
	r.RegisterClient("codex-client", "codex", []*ModelInfo{
		{ID: "shadowed-model", Created: 300},
		{ID: "older-codex", Created: 200},
	})
	notClaude := func(provider string) bool { return provider != "claude" }
	// Reads the registry, as the handler's router does.
	routeNotShadowed := func(modelID string) (ModelRoute, bool) {
		return ModelRoute{Model: modelID, ID: modelID, BaseModel: modelID}, modelID != "shadowed-model" && len(r.GetModelProviders(modelID)) > 0
	}

	if got, err := r.GetFirstAvailableModelMatching("", notClaude, nil); err != nil || got != "shadowed-model" {
		t.Fatalf("without a model filter = %q, %v; want shadowed-model", got, err)
	}
	if got, err := r.GetFirstAvailableModelMatching("", notClaude, routeNotShadowed); err != nil || got != "older-codex" {
		t.Fatalf("available with a model filter = %q, %v; want older-codex", got, err)
	}
	if got, err := r.GetFirstRegisteredModelMatching(notClaude, routeNotShadowed); err != nil || got != "older-codex" {
		t.Fatalf("registered with a model filter = %q, %v; want older-codex", got, err)
	}
	if got, err := r.GetFirstRegisteredModelMatching(nil, func(string) (ModelRoute, bool) { return ModelRoute{}, false }); err == nil {
		t.Fatalf("registered with a model filter rejecting every model = %q, want an error", got)
	}
}

// A listed model is counted on the registration routeModel maps it to: a healthy
// client of the listed ID does not count when the routed model's accepted
// clients are all unavailable.
func TestGetFirstAvailableModelMatchingCountsRoutedModel(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("gemini-client", "gemini", []*ModelInfo{{ID: "routed-base", Created: 300}})
	r.RegisterClient("codex-client", "codex", []*ModelInfo{
		{ID: "routed-base(high)", Created: 400},
		{ID: "older-codex", Created: 200},
	})
	r.SetModelQuotaExceeded("gemini-client", "routed-base")
	acceptAll := func(string) bool { return true }
	toBase := func(modelID string) (ModelRoute, bool) {
		if modelID == "routed-base(high)" {
			return ModelRoute{Model: modelID, ID: "routed-base", BaseModel: "routed-base"}, true
		}
		return ModelRoute{Model: modelID, ID: modelID, BaseModel: modelID}, true
	}

	if got, err := r.GetFirstAvailableModelMatching("", acceptAll, nil); err != nil || got != "routed-base(high)" {
		t.Fatalf("counted on its own registration = %q, %v; want routed-base(high)", got, err)
	}
	if got, err := r.GetFirstAvailableModelMatching("", acceptAll, toBase); err != nil || got != "older-codex" {
		t.Fatalf("counted on the routed model = %q, %v; want older-codex", got, err)
	}
	r.ClearModelQuotaExceeded("gemini-client", "routed-base")
	if got, err := r.GetFirstAvailableModelMatching("", acceptAll, toBase); err != nil || got != "routed-base(high)" {
		t.Fatalf("counted on the recovered routed model = %q, %v; want routed-base(high)", got, err)
	}
}

// A request routed by a full suffixed name runs on the clients of that
// registration that support its base model, compared case-insensitively as
// credential selection does: a client that also registers the base model in
// another case counts, one that registers only the suffixed name does not.
// Availability is that of the requested model's registration.
func TestFirstModelMatchingCountsRoutedClientsSupportingBaseModel(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("suffix-only-client", "codex", []*ModelInfo{{ID: "only-y(high)", Created: 500}})
	r.RegisterClient("mixed-case-client", "codex", []*ModelInfo{
		{ID: "case-x(high)", Created: 400},
		{ID: "Case-X", Created: 100},
	})
	r.RegisterClient("suffixed-copy-client", "codex", []*ModelInfo{{ID: "case-x(high)", Created: 400}})
	r.RegisterClient("older-client", "codex", []*ModelInfo{{ID: "older", Created: 200}})
	acceptAll := func(string) bool { return true }
	// What the handler's router returns when no registration matches the base
	// model exactly: the full name's registration and the base model.
	fullName := func(modelID string) (ModelRoute, bool) {
		switch modelID {
		case "only-y(high)":
			return ModelRoute{Model: modelID, ID: modelID, BaseModel: "only-y"}, true
		case "case-x(high)":
			return ModelRoute{Model: modelID, ID: modelID, BaseModel: "case-x"}, true
		}
		return ModelRoute{Model: modelID, ID: modelID, BaseModel: modelID}, true
	}

	if got, err := r.GetFirstAvailableModelMatching("", acceptAll, fullName); err != nil || got != "case-x(high)" {
		t.Fatalf("available = %q, %v; want case-x(high)", got, err)
	}
	if got, err := r.GetFirstRegisteredModelMatching(acceptAll, fullName); err != nil || got != "case-x(high)" {
		t.Fatalf("registered = %q, %v; want case-x(high)", got, err)
	}
	notCodex := func(provider string) bool { return provider != "codex" }
	if got, err := r.GetFirstRegisteredModelMatching(notCodex, fullName); err == nil {
		t.Fatalf("registered without codex = %q, want an error", got)
	}

	// The healthy client of the suffixed name does not support the base model.
	r.SetModelQuotaExceeded("mixed-case-client", "case-x(high)")
	if got, err := r.GetFirstAvailableModelMatching("", acceptAll, fullName); err != nil || got != "older" {
		t.Fatalf("available with the supporting client cooling down = %q, %v; want older", got, err)
	}
	if got, err := r.GetFirstRegisteredModelMatching(acceptAll, fullName); err != nil || got != "case-x(high)" {
		t.Fatalf("registered with the supporting client cooling down = %q, %v; want case-x(high)", got, err)
	}
}

// Credential selection runs a routed request on any client of the routed
// registration's providers that it accepts, not only on the clients of that
// registration: a client that registers the base model in another case counts
// with its own state for that model, and a client counts for the model its
// selection key names (a forked OAuth alias's upstream model).
func TestFirstModelMatchingCountsAcceptedClientsOfRoutedProviders(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("split-suffixed-client", "codex", []*ModelInfo{{ID: "split-x(high)", Created: 500}})
	r.RegisterClient("split-base-client", "codex", []*ModelInfo{{ID: "Split-X", Created: 100}})
	r.RegisterClient("alias-client", "codex", []*ModelInfo{
		{ID: "alias-x(high)", Created: 400},
		{ID: "upstream-y", Created: 100},
	})
	r.RegisterClient("older-client", "codex", []*ModelInfo{{ID: "older", Created: 200}})
	acceptAll := func(string) bool { return true }
	route := func(keys map[string]SelectionKey) ModelRouter {
		return func(modelID string) (ModelRoute, bool) {
			switch modelID {
			case "split-x(high)":
				return ModelRoute{Model: modelID, ID: modelID, BaseModel: "split-x"}, true
			case "alias-x(high)":
				return ModelRoute{Model: modelID, ID: modelID, BaseModel: "alias-x", SelectionKeys: keys}, true
			}
			return ModelRoute{Model: modelID, ID: modelID, BaseModel: modelID}, true
		}
	}
	withoutKeys := route(nil)
	withAliasKey := route(map[string]SelectionKey{"alias-client": {Model: "upstream-y"}})
	withCoolingAliasKey := route(map[string]SelectionKey{"alias-client": {Model: "upstream-y", Unavailable: true}})

	if got, err := r.GetFirstAvailableModelMatching("", acceptAll, withoutKeys); err != nil || got != "split-x(high)" {
		t.Fatalf("available = %q, %v; want split-x(high) (served by the client of Split-X)", got, err)
	}
	r.SetModelQuotaExceeded("split-base-client", "Split-X")
	if got, err := r.GetFirstAvailableModelMatching("", acceptAll, withoutKeys); err != nil || got != "older" {
		t.Fatalf("available with Split-X cooling down = %q, %v; want older (alias-x(high) has no selection key)", got, err)
	}
	if got, err := r.GetFirstRegisteredModelMatching(acceptAll, withoutKeys); err != nil || got != "split-x(high)" {
		t.Fatalf("registered with Split-X cooling down = %q, %v; want split-x(high)", got, err)
	}
	if got, err := r.GetFirstAvailableModelMatching("", acceptAll, withAliasKey); err != nil || got != "alias-x(high)" {
		t.Fatalf("available with the alias key = %q, %v; want alias-x(high)", got, err)
	}
	if got, err := r.GetFirstAvailableModelMatching("", acceptAll, withCoolingAliasKey); err != nil || got != "older" {
		t.Fatalf("available with the alias client cooling down for its key = %q, %v; want older", got, err)
	}
	notCodex := func(provider string) bool { return provider != "codex" }
	if got, err := r.GetFirstRegisteredModelMatching(notCodex, withAliasKey); err == nil {
		t.Fatalf("registered without codex = %q, want an error", got)
	}
}

// Selection checks a client accepted through its selection key (an OAuth
// alias's upstream model) by the credential's state for that key, which the
// auth manager reports with it. The client's registrations do not decide it:
// the manager projects onto each the state of the model selection maps that
// registration to, so with aliases "b" -> "a" and "a" -> "c" the registration
// of "a" carries the state of "c".
func TestFirstAvailableModelMatchingChecksSelectionKeyState(t *testing.T) {
	for _, tc := range []struct {
		name   string
		models []*ModelInfo
	}{
		{"registers the requested model", []*ModelInfo{{ID: "fork-x(high)", Created: 500}, {ID: "fork-x", Created: 100}, {ID: "fork-y", Created: 100}}},
		{"registers the selection key", []*ModelInfo{{ID: "fork-x", Created: 100}, {ID: "fork-y", Created: 100}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestModelRegistry()
			r.RegisterClient("alias-client", "codex", tc.models)
			// Lists the requested model without serving it: it lacks the base model.
			r.RegisterClient("listing-client", "codex", []*ModelInfo{{ID: "fork-x(high)", Created: 500}})
			r.RegisterClient("older-client", "codex", []*ModelInfo{{ID: "older", Created: 200}})
			acceptAll := func(string) bool { return true }
			route := func(key SelectionKey) ModelRouter {
				return func(modelID string) (ModelRoute, bool) {
					if modelID == "fork-x(high)" {
						return ModelRoute{Model: modelID, ID: "fork-x", BaseModel: "fork-x", SelectionKeys: map[string]SelectionKey{"alias-client": key}}, true
					}
					return ModelRoute{Model: modelID, ID: modelID, BaseModel: modelID}, true
				}
			}
			availableKey := route(SelectionKey{Model: "fork-y"})
			coolingKey := route(SelectionKey{Model: "fork-y", Unavailable: true})

			for _, model := range tc.models {
				r.SetModelQuotaExceeded("alias-client", model.ID)
			}
			if got, err := r.GetFirstAvailableModelMatching("", acceptAll, availableKey); err != nil || got != "fork-x(high)" {
				t.Fatalf("available with the registrations cooling down = %q, %v; want fork-x(high)", got, err)
			}
			for _, model := range tc.models {
				r.ClearModelQuotaExceeded("alias-client", model.ID)
			}
			if got, err := r.GetFirstAvailableModelMatching("", acceptAll, coolingKey); err != nil || got != "older" {
				t.Fatalf("available with the selection key cooling down = %q, %v; want older", got, err)
			}
			if got, err := r.GetFirstRegisteredModelMatching(acceptAll, coolingKey); err != nil || got != "fork-x(high)" {
				t.Fatalf("registered with the selection key cooling down = %q, %v; want fork-x(high)", got, err)
			}
		})
	}
}

// Selection resolves an OAuth alias of the full requested name
// case-insensitively, so with "x" -> "y" and "x(HIGH)" -> "x" a request for
// "x(high)" is checked by the state of "x", which the auth manager projects onto
// the client's registration of "x(HIGH)", while its registration of "x" carries
// the state of "y". The registry reads the state of the registration that
// matches the requested name in another case before falling back to the base.
func TestFirstAvailableModelMatchingReadsSuffixedRegistrationInAnotherCase(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("case-client", "codex", []*ModelInfo{{ID: "case-x", Created: 500}, {ID: "case-x(HIGH)", Created: 100}})
	r.RegisterClient("older-client", "codex", []*ModelInfo{{ID: "older", Created: 200}})
	acceptAll := func(string) bool { return true }
	route := func(modelID string) (ModelRoute, bool) {
		model := modelID + "(high)"
		return ModelRoute{Model: model, ID: modelID, BaseModel: modelID}, true
	}

	r.SetModelQuotaExceeded("case-client", "case-x")
	if got, err := r.GetFirstAvailableModelMatching("", acceptAll, route); err != nil || got != "case-x" {
		t.Fatalf("available with the base registration cooling down = %q, %v; want case-x", got, err)
	}
	r.ClearModelQuotaExceeded("case-client", "case-x")
	r.SetModelQuotaExceeded("case-client", "case-x(HIGH)")
	if got, err := r.GetFirstAvailableModelMatching("", acceptAll, route); err != nil || got != "older" {
		t.Fatalf("available with the suffixed registration cooling down = %q, %v; want older", got, err)
	}
}

// With a routeModel every registered model is a candidate, not only the listed
// ones: the listing hides a model whose every client is suspended for a reason
// other than quota, while the routed request may be checked by another, healthy
// registration. Without a routeModel the listing still decides.
func TestFirstAvailableModelMatchingRoutesModelHiddenFromListing(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("hidden-client", "codex", []*ModelInfo{{ID: "hidden-x", Created: 500}, {ID: "hidden-x(HIGH)", Created: 100}})
	r.RegisterClient("older-client", "codex", []*ModelInfo{{ID: "older", Created: 200}})
	r.SuspendClientModel("hidden-client", "hidden-x", "upstream unavailable")
	for _, listed := range r.GetAvailableModels("") {
		if listed["id"] == "hidden-x" {
			t.Fatalf("listing shows hidden-x; the test needs the suspension to hide it")
		}
	}
	acceptAll := func(string) bool { return true }
	route := func(modelID string) (ModelRoute, bool) {
		return ModelRoute{Model: modelID + "(high)", ID: modelID, BaseModel: modelID}, true
	}

	if got, err := r.GetFirstAvailableModelMatching("", acceptAll, route); err != nil || got != "hidden-x" {
		t.Fatalf("routed with hidden-x suspended = %q, %v; want hidden-x", got, err)
	}
	if got, err := r.GetFirstAvailableModelMatching("", acceptAll, nil); err != nil || got != "older" {
		t.Fatalf("unrouted with hidden-x suspended = %q, %v; want older", got, err)
	}
}
