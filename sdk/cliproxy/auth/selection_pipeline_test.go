package auth

import (
	"context"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// pipelineFixture builds one credential set covering two priorities, reserve levels
// 0-3, known and unknown resets and remaining percent, and weights including zero.
// Reserves use percent 30 with steps 20 and 10. Resets are relative to the
// observation one minute before pipelineTestNow.
//
//	id  priority  reserve  remaining  level  reset  weight
//	a   10        soft     25         1      3h     1
//	b   10        none     60         0      2h     0
//	c   0         soft     90         0      2h     3
//	d   0         soft     5          3      30m    1
//	e   0         soft     unknown    0      none   2
//	f   10        soft     15         2      30m    1
//	g   0         none     90         0      90m    5
func pipelineFixture() []*Auth {
	steps := []int{20, 10}
	specs := []struct {
		id   string
		spec reserveSpec
	}{
		{"a", reserveSpec{priority: "10", mode: QuotaReserveModeSoft, steps: steps, used: 75, reset: 3 * time.Hour, weight: "1"}},
		{"b", reserveSpec{priority: "10", used: 40, reset: 2 * time.Hour, weight: "0"}},
		{"c", reserveSpec{priority: "0", mode: QuotaReserveModeSoft, steps: steps, used: 10, reset: 2 * time.Hour, weight: "3"}},
		{"d", reserveSpec{priority: "0", mode: QuotaReserveModeSoft, steps: steps, used: 95, reset: 30 * time.Minute, weight: "1"}},
		{"e", reserveSpec{priority: "0", mode: QuotaReserveModeSoft, steps: steps, weight: "2"}},
		{"f", reserveSpec{priority: "10", mode: QuotaReserveModeSoft, steps: steps, used: 85, reset: 30 * time.Minute, weight: "1"}},
		{"g", reserveSpec{priority: "0", used: 10, reset: 90 * time.Minute, weight: "5"}},
	}
	auths := make([]*Auth, 0, len(specs))
	for _, item := range specs {
		auth := pipelineTestAuth(item.id, item.spec)
		auth.Index = "idx-" + item.id
		auths = append(auths, auth)
	}
	auths[4].Quota = QuotaState{}
	return auths
}

func newTestPipelineSelector(shared func() config.SelectionConfig) *PipelineSelector {
	selector := NewPipelineSelector(shared)
	selector.nowFunc = func() time.Time { return pipelineTestNow }
	return selector
}

func fixedSelection(primary, tiebreak string) func() config.SelectionConfig {
	return func() config.SelectionConfig { return config.SelectionConfig{Primary: primary, Tiebreak: tiebreak} }
}

func pipelinePick(t *testing.T, selector *PipelineSelector, ctx context.Context, auths []*Auth) string {
	t.Helper()
	selected, errPick := selector.Pick(ctx, "codex", pipelineTestModel, cliproxyexecutor.Options{}, auths)
	if errPick != nil {
		t.Fatalf("Pick error = %v", errPick)
	}
	if selected == nil {
		t.Fatal("Pick returned nil")
	}
	return selected.ID
}

func TestPipelineFixtureNarrowers(t *testing.T) {
	auths := pipelineFixture()
	for _, tt := range []struct {
		name   string
		narrow func([]*Auth, string, time.Time) []*Auth
		want   []string
	}{
		{"reserves", narrowByReserves, []string{"b", "c", "e", "g"}},
		{"priority", narrowByPriority, []string{"a", "b", "f"}},
		{"earliest-reset", narrowByEarliestReset, []string{"d", "f"}},
		{"most-remaining", narrowByMostRemaining, []string{"c", "g"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := authIDs(tt.narrow(auths, pipelineTestModel, pipelineTestNow)); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("%s = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestPipelineSelectorSlotMatrix(t *testing.T) {
	tiebreaks := []string{"", config.SelectionPriority, config.SelectionReserves, config.SelectionEarliestReset,
		config.SelectionMostRemaining, config.SelectionRoundRobin, config.SelectionWeightedRoundRobin, config.SelectionFillFirst}
	// want lists the expected credential per tiebreak, in the order of tiebreaks.
	matrix := map[string][]string{
		config.SelectionReserves:      {"b", "b", "b", "g", "c", "b", "g", "b"},
		config.SelectionPriority:      {"a", "a", "b", "f", "b", "a", "a", "a"},
		config.SelectionEarliestReset: {"d", "f", "f", "d", "f", "d", "d", "d"},
		config.SelectionMostRemaining: {"c", "c", "c", "g", "c", "c", "g", "c"},
		// A picker primary ignores the tiebreak.
		config.SelectionRoundRobin:         {"a", "a", "a", "a", "a", "a", "a", "a"},
		config.SelectionWeightedRoundRobin: {"g", "g", "g", "g", "g", "g", "g", "g"},
		config.SelectionFillFirst:          {"a", "a", "a", "a", "a", "a", "a", "a"},
	}
	for primary, wants := range matrix {
		for i, tiebreak := range tiebreaks {
			t.Run(primary+"/"+tiebreak, func(t *testing.T) {
				selector := newTestPipelineSelector(fixedSelection(primary, tiebreak))
				if got := pipelinePick(t, selector, context.Background(), pipelineFixture()); got != wants[i] {
					t.Fatalf("{%s, %s} picked %s, want %s", primary, tiebreak, got, wants[i])
				}
			})
		}
	}
}

func TestPipelineNarrowersUnknownData(t *testing.T) {
	unknown := func(id string) *Auth {
		auth := pipelineTestAuth(id, reserveSpec{})
		auth.Quota = QuotaState{}
		return auth
	}
	known := pipelineTestAuth("known", reserveSpec{used: 50, reset: time.Hour})
	for _, tt := range []struct {
		name   string
		narrow func([]*Auth, string, time.Time) []*Auth
	}{
		{"earliest-reset", narrowByEarliestReset},
		{"most-remaining", narrowByMostRemaining},
	} {
		t.Run(tt.name, func(t *testing.T) {
			allUnknown := []*Auth{unknown("u1"), unknown("u2")}
			if got := authIDs(tt.narrow(allUnknown, pipelineTestModel, pipelineTestNow)); !reflect.DeepEqual(got, []string{"u1", "u2"}) {
				t.Fatalf("all unknown = %v, want every candidate kept", got)
			}
			mixed := []*Auth{unknown("u1"), known, unknown("u2")}
			if got := authIDs(tt.narrow(mixed, pipelineTestModel, pipelineTestNow)); !reflect.DeepEqual(got, []string{"known"}) {
				t.Fatalf("mixed = %v, want only the known candidate", got)
			}
		})
	}
}

func TestPipelineSelectorLeftoverTiesRotate(t *testing.T) {
	selector := newTestPipelineSelector(fixedSelection(config.SelectionReserves, ""))
	auths := pipelineFixture()
	for _, want := range []string{"b", "c", "e", "g", "b"} {
		if got := pipelinePick(t, selector, context.Background(), auths); got != want {
			t.Fatalf("picked %s, want %s", got, want)
		}
	}
}

func TestPipelineSelectorEqualResetsRotate(t *testing.T) {
	// b and c share the 2h reset; e has none and is dropped by earliest-reset.
	fixture := pipelineFixture()
	auths := []*Auth{fixture[1], fixture[2], fixture[4]}
	selector := newTestPipelineSelector(fixedSelection(config.SelectionReserves, config.SelectionEarliestReset))
	for _, want := range []string{"b", "c", "b"} {
		if got := pipelinePick(t, selector, context.Background(), auths); got != want {
			t.Fatalf("picked %s, want %s", got, want)
		}
	}
	// The legacy selector keeps choosing the lowest ID for equal resets.
	legacy := &EarliestResetSelector{nowFunc: func() time.Time { return pipelineTestNow }}
	for range 3 {
		selected, errPick := legacy.Pick(context.Background(), "codex", pipelineTestModel, cliproxyexecutor.Options{}, auths)
		if errPick != nil || selected == nil || selected.ID != "b" {
			t.Fatalf("legacy selected = %v, error = %v; want b", selected, errPick)
		}
	}
}

func TestPipelineSelectorProfileSelection(t *testing.T) {
	shared := config.SelectionConfig{Primary: config.SelectionPriority}
	selector := newTestPipelineSelector(func() config.SelectionConfig { return shared })
	auths := pipelineFixture()
	if got := pipelinePick(t, selector, context.Background(), auths); got != "a" {
		t.Fatalf("shared selection picked %s, want a", got)
	}
	keyCtx := withRoutingProfile(context.Background(), &routingProfile{fromKey: true,
		selection: config.SelectionConfig{Primary: config.SelectionMostRemaining, Tiebreak: config.SelectionWeightedRoundRobin}})
	if got := pipelinePick(t, selector, keyCtx, auths); got != "g" {
		t.Fatalf("key selection picked %s, want g", got)
	}
	// A profile without a selection falls back to the shared selection; the shared
	// round-robin cursor continues from a, so b follows within {a, b, f}.
	emptyCtx := withRoutingProfile(context.Background(), &routingProfile{})
	if got := pipelinePick(t, selector, emptyCtx, pipelineFixture()); got != "b" {
		t.Fatalf("empty profile selection picked %s, want b", got)
	}
	// A shared selection change is visible on the next Pick of the same selector.
	shared = config.SelectionConfig{Primary: config.SelectionEarliestReset, Tiebreak: config.SelectionPriority}
	if got := pipelinePick(t, selector, context.Background(), auths); got != "f" {
		t.Fatalf("changed shared selection picked %s, want f", got)
	}
}

func TestPipelineSelectorDefaultSelection(t *testing.T) {
	for _, shared := range []func() config.SelectionConfig{nil, fixedSelection("", "")} {
		selector := newTestPipelineSelector(shared)
		// The default {reserves, round-robin} keeps level 0 and rotates from its head.
		if got := pipelinePick(t, selector, context.Background(), pipelineFixture()); got != "b" {
			t.Fatalf("default selection picked %s, want b", got)
		}
	}
}

func TestPipelineSelectorPreferredShortcut(t *testing.T) {
	selector := newTestPipelineSelector(fixedSelection(config.SelectionReserves, config.SelectionRoundRobin))
	auths := pipelineFixture()
	// d is at soft level 3 and still taken because it is the key's preference.
	ctx := withRoutingProfile(context.Background(), &routingProfile{preferred: map[string]string{"codex": "idx-d"}})
	for range 2 {
		if got := pipelinePick(t, selector, ctx, auths); got != "d" {
			t.Fatalf("picked %s, want preferred d", got)
		}
	}
	// A preferred credential that is unavailable leaves selection to the slots.
	auths[3].Disabled = true
	if got := pipelinePick(t, selector, ctx, auths); got != "b" {
		t.Fatalf("picked %s with disabled preference, want b", got)
	}
	// An explicit preference wins over a zero weight under a weighted slot, while
	// without it the zero-weight b is never picked there.
	weighted := newTestPipelineSelector(fixedSelection(config.SelectionReserves, config.SelectionWeightedRoundRobin))
	zeroCtx := withRoutingProfile(context.Background(), &routingProfile{preferred: map[string]string{"codex": "idx-b"}})
	if got := pipelinePick(t, weighted, zeroCtx, pipelineFixture()); got != "b" {
		t.Fatalf("picked %s, want the zero-weight preferred b", got)
	}
	for range 3 {
		if got := pipelinePick(t, weighted, context.Background(), pipelineFixture()); got == "b" {
			t.Fatal("picked the zero-weight b without a preference")
		}
	}
}

func TestPipelineSelectorCandidates(t *testing.T) {
	t.Run("blocked and hard-reserved candidates are removed", func(t *testing.T) {
		selector := newTestPipelineSelector(fixedSelection(config.SelectionFillFirst, ""))
		hard := pipelineTestAuth("a-hard", reserveSpec{mode: QuotaReserveModeHard, used: 90})
		disabled := pipelineDisabled(pipelineTestAuth("b-disabled", reserveSpec{}))
		cooling := pipelineCooling(pipelineTestAuth("c-cooling", reserveSpec{}), time.Hour)
		ok := pipelineTestAuth("d-ok", reserveSpec{})
		if got := pipelinePick(t, selector, context.Background(), []*Auth{hard, disabled, cooling, ok}); got != "d-ok" {
			t.Fatalf("picked %s, want d-ok", got)
		}
	})
	t.Run("prefiltered candidates are not refiltered", func(t *testing.T) {
		selector := newTestPipelineSelector(fixedSelection(config.SelectionFillFirst, ""))
		disabled := pipelineDisabled(pipelineTestAuth("a-disabled", reserveSpec{}))
		ok := pipelineTestAuth("b-ok", reserveSpec{})
		if got := pipelinePick(t, selector, withPipelineCandidates(context.Background()), []*Auth{disabled, ok}); got != "a-disabled" {
			t.Fatalf("picked %s, want the prefiltered head a-disabled", got)
		}
	})
	t.Run("no candidates yields an error", func(t *testing.T) {
		selector := newTestPipelineSelector(nil)
		hard := pipelineTestAuth("a-hard", reserveSpec{mode: QuotaReserveModeHard, used: 90})
		if _, errPick := selector.Pick(context.Background(), "codex", pipelineTestModel, cliproxyexecutor.Options{}, []*Auth{hard}); errPick == nil {
			t.Fatal("expected an error when every candidate is hard-reserved")
		}
		if _, errPick := selector.Pick(withPipelineCandidates(context.Background()), "codex", pipelineTestModel, cliproxyexecutor.Options{}, nil); errPick == nil {
			t.Fatal("expected an error for empty prefiltered candidates")
		}
	})
	t.Run("skipped reserve does not narrow by reserves", func(t *testing.T) {
		selector := newTestPipelineSelector(fixedSelection(config.SelectionReserves, config.SelectionFillFirst))
		if got := pipelinePick(t, selector, withQuotaReserveSkipped(context.Background()), pipelineFixture()); got != "a" {
			t.Fatalf("picked %s, want a", got)
		}
	})
	t.Run("zero weights only", func(t *testing.T) {
		selector := newTestPipelineSelector(fixedSelection(config.SelectionWeightedRoundRobin, ""))
		zero := pipelineTestAuth("a-zero", reserveSpec{weight: "0"})
		if _, errPick := selector.Pick(context.Background(), "codex", pipelineTestModel, cliproxyexecutor.Options{}, []*Auth{zero}); errPick == nil {
			t.Fatal("expected an error when no candidate has a positive weight")
		}
	})
}

func subscriptionWindowAuth(provider string, observedAt time.Time, signals map[string]string) *Auth {
	return &Auth{ID: provider, Provider: provider, Status: StatusActive, Quota: QuotaState{ObservedAt: observedAt, Signals: signals}}
}

func TestSubscriptionWindows(t *testing.T) {
	now := pipelineTestNow
	observed := now.Add(-time.Minute)
	future := strconv.FormatInt(now.Add(time.Hour).Unix(), 10)
	later := strconv.FormatInt(now.Add(2*time.Hour).Unix(), 10)
	past := strconv.FormatInt(now.Add(-time.Second).Unix(), 10)
	for _, tt := range []struct {
		name          string
		auth          *Auth
		wantWindows   []subscriptionWindow
		wantRemaining float64
		wantOK        bool
	}{
		{
			name: "claude windows",
			auth: subscriptionWindowAuth("claude", observed, map[string]string{
				"anthropic-ratelimit-unified-5h-utilization": "0.4", "anthropic-ratelimit-unified-5h-reset": future,
				"anthropic-ratelimit-unified-7d-utilization": "0.7", "anthropic-ratelimit-unified-7d-reset": later,
			}),
			wantWindows: []subscriptionWindow{
				{reset: now.Add(time.Hour), remaining: 60},
				{reset: now.Add(2 * time.Hour), remaining: 30},
			},
			wantRemaining: 30, wantOK: true,
		},
		{
			name: "expired window skipped",
			auth: subscriptionWindowAuth("claude", observed, map[string]string{
				"anthropic-ratelimit-unified-5h-utilization": "0.9", "anthropic-ratelimit-unified-5h-reset": past,
				"anthropic-ratelimit-unified-7d-utilization": "0.2", "anthropic-ratelimit-unified-7d-reset": later,
			}),
			wantWindows:   []subscriptionWindow{{reset: now.Add(2 * time.Hour), remaining: 80}},
			wantRemaining: 80, wantOK: true,
		},
		{
			name: "malformed value skipped",
			auth: subscriptionWindowAuth("codex", observed, map[string]string{
				"x-codex-primary-used-percent": "abc", "x-codex-primary-reset-at": future,
				"x-codex-secondary-used-percent": "-1", "x-codex-secondary-reset-at": later,
			}),
		},
		{
			name: "exhausted window",
			auth: subscriptionWindowAuth("codex", observed, map[string]string{
				"x-codex-primary-used-percent": "100", "x-codex-primary-reset-at": future,
				"x-codex-secondary-used-percent": "20", "x-codex-secondary-reset-at": later,
			}),
			wantWindows: []subscriptionWindow{
				{reset: now.Add(time.Hour), remaining: 0},
				{reset: now.Add(2 * time.Hour), remaining: 80},
			},
			wantRemaining: 0, wantOK: true,
		},
		{
			name: "overage above 100 percent",
			auth: subscriptionWindowAuth("claude", observed, map[string]string{
				"anthropic-ratelimit-unified-5h-utilization": "1.25", "anthropic-ratelimit-unified-5h-reset": future,
			}),
			wantWindows:   []subscriptionWindow{{reset: now.Add(time.Hour), remaining: 0}},
			wantRemaining: 0, wantOK: true,
		},
		{
			name: "future observation ignored",
			auth: subscriptionWindowAuth("codex", now.Add(time.Minute), map[string]string{
				"x-codex-primary-used-percent": "10", "x-codex-primary-reset-at": future,
			}),
		},
		{name: "unsupported provider", auth: subscriptionWindowAuth("gemini", observed, map[string]string{"x-codex-primary-used-percent": "10"})},
		{name: "nil auth"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := subscriptionWindows(tt.auth, now); !reflect.DeepEqual(got, tt.wantWindows) {
				t.Fatalf("windows = %+v, want %+v", got, tt.wantWindows)
			}
			remaining, ok := subscriptionRemainingForAuth(tt.auth, now)
			if ok != tt.wantOK || remaining != tt.wantRemaining {
				t.Fatalf("remaining = %v, %v; want %v, %v", remaining, ok, tt.wantRemaining, tt.wantOK)
			}
		})
	}
}

func TestQuotaReserveLevelFromWindows(t *testing.T) {
	// The level derives from the worst window; until is the latest reset holding a reserve.
	now := pipelineTestNow
	auth := subscriptionWindowAuth("codex", now.Add(-time.Minute), map[string]string{
		"x-codex-primary-used-percent": "85", "x-codex-primary-reset-at": strconv.FormatInt(now.Add(time.Hour).Unix(), 10),
		"x-codex-secondary-used-percent": "75", "x-codex-secondary-reset-at": strconv.FormatInt(now.Add(2*time.Hour).Unix(), 10),
	})
	level, until := quotaReserveLevel(auth, QuotaReservePolicy{Percent: 30, Steps: []int{20, 10}}, now)
	if level != 2 || !until.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("level = %d, until = %v; want 2, %v", level, until, now.Add(2*time.Hour))
	}
}
