package auth

import (
	"context"
	"reflect"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const (
	edgeTestOldKey = "sk-edge-rotation-old"
	edgeTestNewKey = "sk-edge-rotation-new"
)

func edgeProfileConfig(key string, preferredIndex string) *internalconfig.Config {
	return &internalconfig.Config{
		SDKConfig: internalconfig.SDKConfig{APIKeys: []string{key}},
		Routing: internalconfig.RoutingConfig{
			KeyProfiles: map[string]*internalconfig.RoutingProfile{
				key: {PreferredAccounts: map[string]string{"codex": preferredIndex}},
			},
		},
	}
}

// A pinned execution serves an existing binding: the key profile's preference must
// not move it to another credential.
func TestPinnedExecutionIgnoresKeyProfilePreference(t *testing.T) {
	m, accounts, model := preferredAccountTestManager(t, "codex", NewPipelineSelector(nil))
	m.SetConfig(edgeProfileConfig(edgeTestOldKey, accounts[2].Index))
	ctx := m.ensureRoutingProfile(ginContextWithKey(edgeTestOldKey))

	opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: accounts[0].ID}}
	selected, _, errPick := m.pickNext(ctx, "codex", model, opts, nil)
	if errPick != nil {
		t.Fatal(errPick)
	}
	if selected.ID != accounts[0].ID {
		t.Fatalf("pinned execution selected %s, want pinned %s", selected.ID, accounts[0].ID)
	}

	selected, _, errPick = m.pickNext(ctx, "codex", model, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatal(errPick)
	}
	if selected.ID != accounts[2].ID {
		t.Fatalf("unpinned execution selected %s, want preferred %s", selected.ID, accounts[2].ID)
	}
}

// A key rotated while a request is in flight: the request keeps the profile it
// resolved at the start, while new requests resolve against the rotated config.
func TestKeyRotatedInFlightKeepsResolvedProfile(t *testing.T) {
	m, accounts, model := preferredAccountTestManager(t, "codex", NewPipelineSelector(nil))
	m.SetConfig(edgeProfileConfig(edgeTestOldKey, accounts[2].Index))
	inFlight := m.ensureRoutingProfile(ginContextWithKey(edgeTestOldKey))
	resolved := routingProfileFromContext(inFlight)

	// The management API moves the profile to the new key.
	m.SetConfig(edgeProfileConfig(edgeTestNewKey, accounts[1].Index))

	if got := routingProfileFromContext(m.ensureRoutingProfile(inFlight)); got != resolved {
		t.Fatal("the in-flight request re-resolved its profile after the rotation")
	}
	selected, _, errPick := m.pickNext(inFlight, "codex", model, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatal(errPick)
	}
	if selected.ID != accounts[2].ID {
		t.Fatalf("in-flight request selected %s, want its resolved preference %s", selected.ID, accounts[2].ID)
	}

	fresh := m.ensureRoutingProfile(ginContextWithKey(edgeTestNewKey))
	selected, _, errPick = m.pickNext(fresh, "codex", model, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatal(errPick)
	}
	if selected.ID != accounts[1].ID {
		t.Fatalf("new key selected %s, want the moved preference %s", selected.ID, accounts[1].ID)
	}

	if profile := m.resolveRoutingProfile(ginContextWithKey(edgeTestOldKey)); profile == nil || profile.fromKey {
		t.Fatalf("old key resolved %+v after rotation, want the shared profile", profile)
	}
}

// Without quota data on any credential, every slot keeps every candidate and the
// leftover ties rotate; no combination fails.
func TestPipelineSelectorUnknownQuotaEverywhere(t *testing.T) {
	newAuths := func() []*Auth {
		auths := make([]*Auth, 0, 3)
		for _, id := range []string{"u1", "u2", "u3"} {
			auth := pipelineTestAuth(id, reserveSpec{})
			auth.Quota = QuotaState{}
			auths = append(auths, auth)
		}
		return auths
	}
	slots := append(append([]string{}, internalconfig.SelectionNarrowingSlots...), internalconfig.SelectionPickerSlots...)
	for _, primary := range slots {
		for _, tiebreak := range append([]string{""}, slots...) {
			t.Run(primary+"/"+tiebreak, func(t *testing.T) {
				selector := newTestPipelineSelector(fixedSelection(primary, tiebreak))
				auths := newAuths()
				seen := map[string]bool{}
				for range len(auths) {
					seen[pipelinePick(t, selector, context.Background(), auths)] = true
				}
				// Only fill-first sticks to one credential; every other slot ends in a rotation.
				stops := primary == internalconfig.SelectionFillFirst ||
					(!internalconfig.IsPickerSlot(primary) && tiebreak == internalconfig.SelectionFillFirst)
				if want := len(auths); !stops && len(seen) != want {
					t.Fatalf("{%s, %s} picked %v, want a rotation over %d credentials", primary, tiebreak, seen, want)
				}
				if stops && len(seen) != 1 {
					t.Fatalf("{%s, %s} picked %v, want fill-first to stay on one credential", primary, tiebreak, seen)
				}
			})
		}
	}
}

func TestManagerSharedSelectionAndProfileWithoutConfig(t *testing.T) {
	m := NewManager(nil, NewPipelineSelector(nil), nil)
	if got := m.SharedSelection(); got != internalconfig.DefaultSelection() {
		t.Fatalf("shared selection without config = %+v, want the default", got)
	}
	if profile := m.resolveRoutingProfile(ginContextWithKey(edgeTestOldKey)); profile == nil || profile.fromKey ||
		profile.selection != internalconfig.DefaultSelection() {
		t.Fatalf("profile without config = %+v, want the default shared profile", profile)
	}

	m.SetConfig(&internalconfig.Config{Routing: internalconfig.RoutingConfig{Strategy: "fill-first"}})
	want := internalconfig.SelectionConfig{Primary: internalconfig.SelectionReserves, Tiebreak: internalconfig.SelectionFillFirst}
	if got := m.SharedSelection(); got != want {
		t.Fatalf("shared selection = %+v, want the legacy derivation %+v", got, want)
	}
}

// When only hard-reserved credentials reach the reserves slot (the reserve is
// skipped for the request), the slot keeps the group instead of emptying it.
func TestNarrowByReservesKeepsHardOnlyGroup(t *testing.T) {
	auths := []*Auth{
		pipelineTestAuth("h1", reserveSpec{mode: QuotaReserveModeHard, used: 90, reset: time.Hour}),
		pipelineTestAuth("h2", reserveSpec{mode: QuotaReserveModeHard, used: 95, reset: time.Hour}),
	}
	if got := authIDs(narrowByReserves(auths, pipelineTestModel, pipelineTestNow)); !reflect.DeepEqual(got, []string{"h1", "h2"}) {
		t.Fatalf("narrowByReserves = %v, want the hard-reserved group kept", got)
	}
}
