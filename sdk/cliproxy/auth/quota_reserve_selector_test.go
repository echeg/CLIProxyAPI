package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// reserveSpec describes one credential in a quota reserve selection scenario.
type reserveSpec struct {
	priority string
	// mode is "", "soft" or "hard"; empty means no reserve is configured.
	mode string
	// used is the observed primary window usage in percent.
	used int
	// reset is the primary window reset relative to the observation.
	reset time.Duration
	// weight is the weighted round-robin weight; empty keeps the default.
	weight string
}

// reserveQuota builds a Codex observation whose primary window used percent and reset are given.
func reserveQuota(observedAt time.Time, used int, reset time.Duration) QuotaState {
	return QuotaState{ObservedAt: observedAt, Signals: map[string]string{
		"X-Codex-Primary-Used-Percent": strconv.Itoa(used),
		"X-Codex-Primary-Reset-At":     strconv.FormatInt(observedAt.Add(reset).Unix(), 10),
	}}
}

func reserveSpecAuth(id string, spec reserveSpec, observedAt time.Time) *Auth {
	attributes := map[string]string{}
	if spec.priority != "" {
		attributes["priority"] = spec.priority
	}
	if spec.weight != "" {
		attributes[AttributeWeight] = spec.weight
	}
	if spec.mode != "" {
		attributes[AttributeQuotaReservePercent] = "30"
		attributes[AttributeQuotaReserveMode] = spec.mode
	}
	reset := spec.reset
	if reset == 0 {
		reset = time.Hour
	}
	return &Auth{ID: id, Provider: "codex", Status: StatusActive, Attributes: attributes,
		Quota: reserveQuota(observedAt, spec.used, reset)}
}

func reserveTestManager(t *testing.T, selector Selector, specs []reserveSpec) (*Manager, []*Auth, string) {
	t.Helper()
	manager := NewManager(nil, selector, nil)
	manager.RegisterExecutor(schedulerTestExecutor{provider: "codex"})
	const model = "quota-reserve-model"
	observedAt := time.Now().Add(-time.Minute)
	var accounts []*Auth
	for i, spec := range specs {
		auth := reserveSpecAuth(fmt.Sprintf("%s-%d", t.Name(), i), spec, observedAt)
		registered, errRegister := manager.Register(WithSkipPersist(context.Background()), auth)
		if errRegister != nil {
			t.Fatal(errRegister)
		}
		accounts = append(accounts, registered)
		registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	}
	return manager, accounts, model
}

func reservePick(manager *Manager, model, session string, mixed bool) (*Auth, error) {
	opts := cliproxyexecutor.Options{Metadata: map[string]any{}}
	if session != "" {
		opts.Metadata[cliproxyexecutor.DerivedSessionIDMetadataKey] = session
	}
	if mixed {
		selected, _, _, errPick := manager.pickNextMixed(context.Background(), []string{"codex"}, model, opts, nil)
		return selected, errPick
	}
	selected, _, errPick := manager.pickNext(context.Background(), "codex", model, opts, nil)
	return selected, errPick
}

func reserveSelectors() []func() Selector {
	return []func() Selector{
		func() Selector { return &RoundRobinSelector{} },
		func() Selector { return &WeightedRoundRobinSelector{} },
		func() Selector { return &FillFirstSelector{} },
		func() Selector { return &EarliestResetSelector{} },
	}
}

func TestQuotaReserveFilter(t *testing.T) {
	now := time.Now()
	observed := now.Add(-time.Minute)
	free := reserveSpecAuth("free", reserveSpec{}, observed)
	healthy := reserveSpecAuth("healthy", reserveSpec{mode: QuotaReserveModeHard, used: 70}, observed)
	soft := reserveSpecAuth("soft", reserveSpec{mode: QuotaReserveModeSoft, used: 80}, observed)
	hardEarly := reserveSpecAuth("hard-early", reserveSpec{mode: QuotaReserveModeHard, used: 90, reset: time.Hour}, observed)
	hardLate := reserveSpecAuth("hard-late", reserveSpec{mode: QuotaReserveModeHard, used: 90, reset: 3 * time.Hour}, observed)

	tests := []struct {
		name      string
		in        []*Auth
		want      []*Auth
		hardCount int
		recoverAt time.Time
	}{
		{name: "no reserve unchanged", in: []*Auth{free}, want: []*Auth{free}},
		{name: "reserve not tripped", in: []*Auth{healthy, free}, want: []*Auth{healthy, free}},
		{name: "hard dropped", in: []*Auth{hardEarly, free}, want: []*Auth{free}, hardCount: 1, recoverAt: observed.Add(time.Hour)},
		{name: "soft skipped while unreserved exists", in: []*Auth{soft, healthy}, want: []*Auth{healthy}},
		{name: "soft pool when nothing else", in: []*Auth{soft, hardEarly}, want: []*Auth{soft}, hardCount: 1, recoverAt: observed.Add(time.Hour)},
		{name: "all hard", in: []*Auth{hardLate, hardEarly}, want: []*Auth{}, hardCount: 2, recoverAt: observed.Add(time.Hour)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, hardCount, recoverAt := quotaReserveFilter(tt.in, now)
			if len(got) != len(tt.want) || (len(got) > 0 && !reflect.DeepEqual(got, tt.want)) {
				t.Fatalf("selectable = %v, want %v", authIDs(got), authIDs(tt.want))
			}
			if hardCount != tt.hardCount || recoverAt.Unix() != tt.recoverAt.Unix() {
				t.Fatalf("hardCount, recoverAt = %d, %v; want %d, %v", hardCount, recoverAt, tt.hardCount, tt.recoverAt)
			}
		})
	}
}

func authIDs(auths []*Auth) []string {
	ids := make([]string, 0, len(auths))
	for _, auth := range auths {
		ids = append(ids, auth.ID)
	}
	return ids
}

func TestQuotaReserveSelectionAcrossStrategies(t *testing.T) {
	for _, newSelector := range reserveSelectors() {
		for _, mixed := range []bool{false, true} {
			name := fmt.Sprintf("%T/mixed=%t", newSelector(), mixed)
			t.Run(name+"/hard excluded across tiers", func(t *testing.T) {
				manager, accounts, model := reserveTestManager(t, newSelector(), []reserveSpec{
					{priority: "10", mode: QuotaReserveModeHard, used: 90},
					{priority: "0"},
				})
				for range 3 {
					if selected, errPick := reservePick(manager, model, "", mixed); errPick != nil || selected.ID != accounts[1].ID {
						t.Fatalf("selected %v, %v; want %s", selected, errPick, accounts[1].ID)
					}
				}
			})
			t.Run(name+"/soft skipped while lower tier unreserved", func(t *testing.T) {
				manager, accounts, model := reserveTestManager(t, newSelector(), []reserveSpec{
					{priority: "10", mode: QuotaReserveModeSoft, used: 90},
					{priority: "0", mode: QuotaReserveModeSoft, used: 10},
				})
				for range 3 {
					if selected, errPick := reservePick(manager, model, "", mixed); errPick != nil || selected.ID != accounts[1].ID {
						t.Fatalf("selected %v, %v; want %s", selected, errPick, accounts[1].ID)
					}
				}
			})
			t.Run(name+"/soft pool used as last resort", func(t *testing.T) {
				manager, accounts, model := reserveTestManager(t, newSelector(), []reserveSpec{
					{mode: QuotaReserveModeHard, used: 95},
					{mode: QuotaReserveModeSoft, used: 90},
				})
				if selected, errPick := reservePick(manager, model, "", mixed); errPick != nil || selected.ID != accounts[1].ID {
					t.Fatalf("selected %v, %v; want %s", selected, errPick, accounts[1].ID)
				}
			})
			t.Run(name+"/all hard returns model cooldown", func(t *testing.T) {
				manager, _, model := reserveTestManager(t, newSelector(), []reserveSpec{
					{mode: QuotaReserveModeHard, used: 90, reset: 3 * time.Hour},
					{mode: QuotaReserveModeHard, used: 90, reset: time.Hour},
				})
				selected, errPick := reservePick(manager, model, "", mixed)
				assertReserveCooldown(t, selected, errPick, time.Hour)
			})
			t.Run(name+"/state untouched", func(t *testing.T) {
				manager, accounts, model := reserveTestManager(t, newSelector(), []reserveSpec{
					{mode: QuotaReserveModeHard, used: 90},
					{mode: QuotaReserveModeSoft, used: 90},
					{},
				})
				before := reserveStateSnapshot(manager, accounts)
				for range 3 {
					if _, errPick := reservePick(manager, model, "", mixed); errPick != nil {
						t.Fatal(errPick)
					}
				}
				if after := reserveStateSnapshot(manager, accounts); !reflect.DeepEqual(before, after) {
					t.Fatalf("selection mutated credential state:\nbefore %+v\nafter  %+v", before, after)
				}
			})
		}
	}
}

func TestQuotaReserveSoftPoolUsesStrategy(t *testing.T) {
	specs := []reserveSpec{
		{mode: QuotaReserveModeSoft, used: 90},
		{mode: QuotaReserveModeSoft, used: 90},
	}
	t.Run("round-robin rotates", func(t *testing.T) {
		manager, accounts, model := reserveTestManager(t, &RoundRobinSelector{}, specs)
		seen := map[string]int{}
		for range 4 {
			selected, errPick := reservePick(manager, model, "", false)
			if errPick != nil {
				t.Fatal(errPick)
			}
			seen[selected.ID]++
		}
		if seen[accounts[0].ID] != 2 || seen[accounts[1].ID] != 2 {
			t.Fatalf("round-robin over soft pool = %v", seen)
		}
	})
	t.Run("fill-first sticks", func(t *testing.T) {
		manager, accounts, model := reserveTestManager(t, &FillFirstSelector{}, specs)
		for range 3 {
			if selected, errPick := reservePick(manager, model, "", false); errPick != nil || selected.ID != accounts[0].ID {
				t.Fatalf("selected %v, %v; want %s", selected, errPick, accounts[0].ID)
			}
		}
	})
}

func TestQuotaReservePreferredAccountFallback(t *testing.T) {
	for _, mode := range []string{QuotaReserveModeHard, QuotaReserveModeSoft} {
		for _, affinity := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/affinity=%t", mode, affinity), func(t *testing.T) {
				var selector Selector = &FillFirstSelector{}
				if affinity {
					sessionSelector := NewSessionAffinitySelector(selector)
					t.Cleanup(sessionSelector.Stop)
					selector = sessionSelector
				}
				manager, accounts, model := reserveTestManager(t, selector, []reserveSpec{
					{mode: QuotaReserveModeSoft, used: 10},
					{mode: mode, used: 90},
				})
				setPreferredAccount(manager, "codex", accounts[1].Index)
				if selected, errPick := reservePick(manager, model, "new", false); errPick != nil || selected.ID != accounts[0].ID {
					t.Fatalf("selected %v, %v; want fallback %s", selected, errPick, accounts[0].ID)
				}
			})
		}
	}
	t.Run("soft preferred wins inside the pool", func(t *testing.T) {
		manager, accounts, model := reserveTestManager(t, &FillFirstSelector{}, []reserveSpec{
			{mode: QuotaReserveModeSoft, used: 90},
			{mode: QuotaReserveModeSoft, used: 90},
		})
		setPreferredAccount(manager, "codex", accounts[1].Index)
		if selected, errPick := reservePick(manager, model, "", false); errPick != nil || selected.ID != accounts[1].ID {
			t.Fatalf("selected %v, %v; want preferred %s", selected, errPick, accounts[1].ID)
		}
	})
}

func TestQuotaReserveWeightedIgnoresZeroWeightAlternatives(t *testing.T) {
	selectors := map[string]func() Selector{
		"weighted":          func() Selector { return &WeightedRoundRobinSelector{} },
		"affinity-weighted": func() Selector { return NewSessionAffinitySelector(&WeightedRoundRobinSelector{}) },
	}
	for name, newSelector := range selectors {
		for _, mixed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/mixed=%t/soft kept as last resort", name, mixed), func(t *testing.T) {
				selector := newSelector()
				if affinity, ok := selector.(*SessionAffinitySelector); ok {
					t.Cleanup(affinity.Stop)
				}
				manager, accounts, model := reserveTestManager(t, selector, []reserveSpec{
					{mode: QuotaReserveModeSoft, used: 90},
					{weight: "0"},
				})
				for _, session := range []string{"", "fresh"} {
					if selected, errPick := reservePick(manager, model, session, mixed); errPick != nil || selected.ID != accounts[0].ID {
						t.Fatalf("session %q selected %v, %v; want %s", session, selected, errPick, accounts[0].ID)
					}
				}
			})
			t.Run(fmt.Sprintf("%s/mixed=%t/hard returns model cooldown", name, mixed), func(t *testing.T) {
				selector := newSelector()
				if affinity, ok := selector.(*SessionAffinitySelector); ok {
					t.Cleanup(affinity.Stop)
				}
				manager, _, model := reserveTestManager(t, selector, []reserveSpec{
					{mode: QuotaReserveModeHard, used: 90},
					{weight: "0"},
				})
				selected, errPick := reservePick(manager, model, "", mixed)
				assertReserveCooldown(t, selected, errPick, time.Hour)
			})
		}
	}
}

func TestQuotaReserveSessionAffinityKeepsBoundSessions(t *testing.T) {
	for _, mode := range []string{QuotaReserveModeHard, QuotaReserveModeSoft} {
		for _, mixed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/mixed=%t", mode, mixed), func(t *testing.T) {
				selector := NewSessionAffinitySelector(&FillFirstSelector{})
				t.Cleanup(selector.Stop)
				manager, accounts, model := reserveTestManager(t, selector, []reserveSpec{
					{mode: mode, used: 10},
					{},
				})
				pick := func(session, want string) {
					t.Helper()
					if selected, errPick := reservePick(manager, model, session, mixed); errPick != nil || selected.ID != want {
						t.Fatalf("session %s selected %v, %v; want %s", session, selected, errPick, want)
					}
				}
				pick("bound", accounts[0].ID)
				manager.mu.Lock()
				manager.auths[accounts[0].ID].Quota = reserveQuota(time.Now().Add(-time.Second), 90, time.Hour)
				manager.mu.Unlock()
				pick("bound", accounts[0].ID)
				pick("fresh", accounts[1].ID)
				pick("bound", accounts[0].ID)
			})
		}
	}
	for _, mixed := range []bool{false, true} {
		t.Run(fmt.Sprintf("all hard keeps bound session and rejects new ones/mixed=%t", mixed), func(t *testing.T) {
			selector := NewSessionAffinitySelector(&RoundRobinSelector{})
			t.Cleanup(selector.Stop)
			manager, accounts, model := reserveTestManager(t, selector, []reserveSpec{{mode: QuotaReserveModeHard, used: 10}})
			if selected, errPick := reservePick(manager, model, "bound", mixed); errPick != nil || selected.ID != accounts[0].ID {
				t.Fatalf("selected %v, %v", selected, errPick)
			}
			manager.mu.Lock()
			manager.auths[accounts[0].ID].Quota = reserveQuota(time.Now().Add(-time.Second), 90, time.Hour)
			manager.mu.Unlock()
			if selected, errPick := reservePick(manager, model, "bound", mixed); errPick != nil || selected.ID != accounts[0].ID {
				t.Fatalf("bound session selected %v, %v", selected, errPick)
			}
			wantProvider := "codex"
			if mixed {
				// A mixed selection reports no single provider, matching the manager cooldown error.
				wantProvider = ""
			}
			for _, session := range []string{"fresh", ""} {
				selected, errPick := reservePick(manager, model, session, mixed)
				assertReserveCooldown(t, selected, errPick, time.Hour)
				var cooldownErr *modelCooldownError
				if errors.As(errPick, &cooldownErr) && cooldownErr.provider != wantProvider {
					t.Fatalf("session %q cooldown provider = %q, want %q", session, cooldownErr.provider, wantProvider)
				}
			}
		})
	}
}

func TestQuotaReserveSelectorDirectAvailability(t *testing.T) {
	now := time.Now()
	observed := now.Add(-time.Minute)
	reserved := reserveSpecAuth("a-reserved", reserveSpec{priority: "10", mode: QuotaReserveModeHard, used: 90}, observed)
	free := reserveSpecAuth("b-free", reserveSpec{priority: "0"}, observed)
	for _, newSelector := range reserveSelectors() {
		selector := newSelector()
		t.Run(fmt.Sprintf("%T", selector), func(t *testing.T) {
			selected, errPick := selector.Pick(context.Background(), "codex", "", cliproxyexecutor.Options{}, []*Auth{reserved, free})
			if errPick != nil || selected.ID != free.ID {
				t.Fatalf("selected %v, %v; want %s", selected, errPick, free.ID)
			}
			selected, errPick = selector.Pick(context.Background(), "codex", "", cliproxyexecutor.Options{}, []*Auth{reserved})
			assertReserveCooldown(t, selected, errPick, time.Hour)
		})
	}
}

func TestQuotaReservePinnedAuthIgnoresReserve(t *testing.T) {
	newSelectors := append(reserveSelectors(), func() Selector {
		return NewSessionAffinitySelector(&RoundRobinSelector{})
	})
	for _, newSelector := range newSelectors {
		for _, mixed := range []bool{false, true} {
			selector := newSelector()
			if sessionSelector, ok := selector.(*SessionAffinitySelector); ok {
				t.Cleanup(sessionSelector.Stop)
			}
			t.Run(fmt.Sprintf("%T/mixed=%t", selector, mixed), func(t *testing.T) {
				manager, accounts, model := reserveTestManager(t, selector, []reserveSpec{
					{mode: QuotaReserveModeHard, used: 90},
					{},
				})
				opts := cliproxyexecutor.Options{Metadata: map[string]any{
					cliproxyexecutor.PinnedAuthMetadataKey:       accounts[0].ID,
					cliproxyexecutor.DerivedSessionIDMetadataKey: "pinned-session",
				}}
				var selected *Auth
				var errPick error
				if mixed {
					selected, _, _, errPick = manager.pickNextMixed(context.Background(), []string{"codex"}, model, opts, nil)
				} else {
					selected, _, errPick = manager.pickNext(context.Background(), "codex", model, opts, nil)
				}
				if errPick != nil || selected == nil || selected.ID != accounts[0].ID {
					t.Fatalf("pinned selection = %v, %v; want %s", selected, errPick, accounts[0].ID)
				}
			})
		}
	}
}

func TestQuotaReservePluginSchedulerDelegatedBuiltinHonorsReserve(t *testing.T) {
	for _, delegate := range []string{pluginapi.SchedulerBuiltinRoundRobin, pluginapi.SchedulerBuiltinFillFirst} {
		t.Run(delegate, func(t *testing.T) {
			manager, accounts, model := reserveTestManager(t, &RoundRobinSelector{}, []reserveSpec{
				{mode: QuotaReserveModeHard, used: 90},
				{},
			})
			manager.SetPluginScheduler(&fakePluginScheduler{
				resp:    pluginapi.SchedulerPickResponse{Handled: true, DelegateBuiltin: delegate},
				handled: true,
			})
			for range 3 {
				if selected, errPick := reservePick(manager, model, "", false); errPick != nil || selected.ID != accounts[1].ID {
					t.Fatalf("selected %v, %v; want %s", selected, errPick, accounts[1].ID)
				}
			}
		})
	}
}

func TestQuotaReserveRetryWaitsForHardReserve(t *testing.T) {
	manager, accounts, model := reserveTestManager(t, &RoundRobinSelector{}, []reserveSpec{
		{mode: QuotaReserveModeHard, used: 90, reset: time.Hour},
	})
	wait, found := manager.closestCooldownWait([]string{"codex"}, model, 0, authSelectionEligibility{}, "", 3)
	if !found || wait < 55*time.Minute || wait > time.Hour {
		t.Fatalf("closestCooldownWait = %s, %t; want about 1h", wait, found)
	}
	wait, found = manager.closestCooldownWait([]string{"codex"}, model, 0, authSelectionEligibility{}, accounts[0].ID, 3)
	if !found || wait != 0 {
		t.Fatalf("pinned closestCooldownWait = %s, %t; want immediate retry", wait, found)
	}
	// A session bound to the held auth was served in the failed round, so a 5xx retries at once.
	attempted := map[string]struct{}{accounts[0].ID: {}}
	wait, found = manager.closestCooldownWaitWithAttempted([]string{"codex"}, model, 0, authSelectionEligibility{}, "", 3, http.StatusBadGateway, attempted)
	if !found || wait != 0 {
		t.Fatalf("bound closestCooldownWait = %s, %t; want immediate retry", wait, found)
	}
}

func TestQuotaReserveCooldownMixesWithRealCooldown(t *testing.T) {
	now := time.Now()
	observed := now.Add(-time.Minute)
	reserved := reserveSpecAuth("a-reserved", reserveSpec{mode: QuotaReserveModeHard, used: 90, reset: time.Hour}, observed)
	for _, tt := range []struct {
		name      string
		cooldown  time.Duration
		wantReset time.Duration
	}{
		{name: "cooldown recovers first", cooldown: 10 * time.Minute, wantReset: 10 * time.Minute},
		{name: "reserve recovers first", cooldown: 3 * time.Hour, wantReset: time.Hour},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cooling := &Auth{ID: "b-cooling", Provider: "codex", Status: StatusActive, Quota: QuotaState{
				Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(tt.cooldown),
			}}
			selected, errPick := (&RoundRobinSelector{}).Pick(context.Background(), "codex", "", cliproxyexecutor.Options{}, []*Auth{reserved, cooling})
			assertReserveCooldown(t, selected, errPick, tt.wantReset)
		})
	}
}

func TestQuotaReserveSessionAffinityCooldownMixesWithRealCooldown(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		for _, session := range []string{"", "reserve-cooldown-session"} {
			t.Run(fmt.Sprintf("mixed=%t/session=%q", mixed, session), func(t *testing.T) {
				manager, accounts, model := reserveTestManager(t, NewSessionAffinitySelector(&RoundRobinSelector{}), []reserveSpec{
					{mode: QuotaReserveModeHard, used: 90, reset: 3 * time.Hour},
					{},
				})
				manager.mu.Lock()
				manager.auths[accounts[1].ID].Quota = QuotaState{
					Exceeded: true, Reason: "credential_quota", NextRecoverAt: time.Now().Add(10 * time.Minute),
				}
				manager.mu.Unlock()
				selected, errPick := reservePick(manager, model, session, mixed)
				assertReserveCooldown(t, selected, errPick, 10*time.Minute)
			})
		}
	}
}

// coolReserveAuth adds a credential-wide quota cooldown to an auth, keeping its window signals.
func coolReserveAuth(auth *Auth, recoverAt time.Time) {
	auth.Quota.Exceeded = true
	auth.Quota.Reason = "credential_quota"
	auth.Quota.NextRecoverAt = recoverAt
}

func TestQuotaReserveCoolingCredentialRecoversAtLaterDeadline(t *testing.T) {
	now := time.Now()
	observed := now.Add(-time.Minute)
	for _, tt := range []struct {
		name      string
		cooldown  time.Duration
		other     bool
		wantReset time.Duration
	}{
		{name: "reserve outlasts cooldown", cooldown: 10 * time.Minute, wantReset: time.Hour},
		{name: "cooldown outlasts reserve", cooldown: 3 * time.Hour, wantReset: 3 * time.Hour},
		{name: "other reserve recovers first", cooldown: 10 * time.Minute, other: true, wantReset: 30 * time.Minute},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cooling := reserveSpecAuth("a-cooling", reserveSpec{mode: QuotaReserveModeHard, used: 90, reset: time.Hour}, observed)
			coolReserveAuth(cooling, now.Add(tt.cooldown))
			auths := []*Auth{cooling}
			if tt.other {
				auths = append(auths, reserveSpecAuth("b-reserved", reserveSpec{mode: QuotaReserveModeHard, used: 90, reset: 30 * time.Minute}, observed))
			}
			selected, errPick := (&RoundRobinSelector{}).Pick(context.Background(), "codex", "", cliproxyexecutor.Options{}, auths)
			assertReserveCooldown(t, selected, errPick, tt.wantReset)
		})
	}
}

func TestQuotaReserveManagerCoolingCredentialRecoversAtLaterDeadline(t *testing.T) {
	affinity := func() Selector { return NewSessionAffinitySelector(&RoundRobinSelector{}) }
	for _, tt := range []struct {
		name      string
		selector  func() Selector
		bound     bool
		wantReset time.Duration
	}{
		{name: "round-robin", selector: func() Selector { return &RoundRobinSelector{} }, wantReset: time.Hour},
		{name: "affinity/new session", selector: affinity, wantReset: time.Hour},
		// A bound session may return to its credential once the cooldown ends, so it reports
		// the reserve-free recovery.
		{name: "affinity/bound session", selector: affinity, bound: true, wantReset: 10 * time.Minute},
	} {
		for _, mixed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/mixed=%t", tt.name, mixed), func(t *testing.T) {
				selector := tt.selector()
				if affinitySelector, ok := selector.(*SessionAffinitySelector); ok {
					t.Cleanup(affinitySelector.Stop)
				}
				manager, accounts, model := reserveTestManager(t, selector, []reserveSpec{
					{mode: QuotaReserveModeHard, used: 10, reset: time.Hour},
				})
				const session = "reserve-cooling-session"
				if tt.bound {
					if selected, errPick := reservePick(manager, model, session, mixed); errPick != nil || selected.ID != accounts[0].ID {
						t.Fatalf("binding pick selected %v, %v", selected, errPick)
					}
				}
				manager.mu.Lock()
				held := manager.auths[accounts[0].ID]
				held.Quota = reserveQuota(time.Now().Add(-time.Minute), 90, time.Hour)
				coolReserveAuth(held, time.Now().Add(10*time.Minute))
				manager.mu.Unlock()
				selected, errPick := reservePick(manager, model, session, mixed)
				assertReserveCooldown(t, selected, errPick, tt.wantReset)

				wait, found := manager.closestCooldownWait([]string{"codex"}, model, 0, authSelectionEligibility{}, "", 3)
				if !found || wait < 55*time.Minute || wait > time.Hour {
					t.Fatalf("closestCooldownWait = %s, %t; want about 1h", wait, found)
				}
			})
		}
	}
}

func TestQuotaReserveBoundSessionExemptsOnlyBoundCredential(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		t.Run(fmt.Sprintf("mixed=%t", mixed), func(t *testing.T) {
			selector := NewSessionAffinitySelector(&FillFirstSelector{})
			t.Cleanup(selector.Stop)
			manager, accounts, model := reserveTestManager(t, selector, []reserveSpec{
				{mode: QuotaReserveModeHard, used: 10, reset: time.Hour},
				{mode: QuotaReserveModeHard, used: 10, reset: time.Hour},
			})
			const session = "reserve-bound-session"
			if selected, errPick := reservePick(manager, model, session, mixed); errPick != nil || selected.ID != accounts[0].ID {
				t.Fatalf("binding pick selected %v, %v", selected, errPick)
			}
			manager.mu.Lock()
			for i, cooldown := range []time.Duration{30 * time.Minute, 10 * time.Minute} {
				held := manager.auths[accounts[i].ID]
				held.Quota = reserveQuota(time.Now().Add(-time.Minute), 90, time.Hour)
				coolReserveAuth(held, time.Now().Add(cooldown))
			}
			manager.mu.Unlock()
			// The bound credential returns after its cooldown, while the other one stays
			// hard-reserved for the session after its shorter cooldown.
			selected, errPick := reservePick(manager, model, session, mixed)
			assertReserveCooldown(t, selected, errPick, 30*time.Minute)
			selected, errPick = reservePick(manager, model, "fresh", mixed)
			assertReserveCooldown(t, selected, errPick, time.Hour)
		})
	}
}

func TestQuotaReserveBoundCandidateFollowsPickLookupOrder(t *testing.T) {
	selector := NewSessionAffinitySelector(&RoundRobinSelector{})
	t.Cleanup(selector.Stop)
	const model = "quota-reserve-model"
	parent, child := &Auth{ID: "parent-auth"}, &Auth{ID: "child-auth"}
	auths := []*Auth{parent, child}
	opts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"sessionId":"claw-c-1","forkSource":{"sessionId":"claw-p-1"}}`),
		Metadata:        map[string]any{},
	}
	key := func(sessionID string) string {
		return "codex::" + cliproxysession.BoundSessionIdentity(sessionID) + "::" + canonicalModelKey(model)
	}
	if bound := selector.boundCandidate("codex", model, opts, auths); bound != nil {
		t.Fatalf("unbound session resolved to %s", bound.ID)
	}
	selector.cache.Set(key("session:claw-p-1"), parent.ID)
	if bound := selector.boundCandidate("codex", model, opts, auths); bound != parent {
		t.Fatalf("fork without own binding resolved to %v, want parent binding", bound)
	}
	// Pick reselects when the fork's own binding is not a candidate, so the parent binding
	// no longer applies.
	selector.cache.Set(key("session:claw-c-1"), "excluded-auth")
	if bound := selector.boundCandidate("codex", model, opts, auths); bound != nil {
		t.Fatalf("fork bound outside candidates resolved to %s", bound.ID)
	}
	selector.cache.Set(key("session:claw-c-1"), child.ID)
	if bound := selector.boundCandidate("codex", model, opts, auths); bound != child {
		t.Fatalf("fork binding resolved to %v, want %s", bound, child.ID)
	}
}

func assertReserveCooldown(t *testing.T, selected *Auth, errPick error, wantResetIn time.Duration) {
	t.Helper()
	var cooldownErr *modelCooldownError
	if selected != nil || !errors.As(errPick, &cooldownErr) {
		t.Fatalf("selected %v, error %v; want model cooldown", selected, errPick)
	}
	retryAfter, errAtoi := strconv.Atoi(cooldownErr.Headers().Get("Retry-After"))
	if errAtoi != nil {
		t.Fatalf("Retry-After = %q", cooldownErr.Headers().Get("Retry-After"))
	}
	// Observations are up to a minute old and resets are truncated to seconds.
	if delta := time.Duration(retryAfter)*time.Second - wantResetIn; delta < -2*time.Minute || delta > 2*time.Minute {
		t.Fatalf("Retry-After = %ds, want about %s", retryAfter, wantResetIn)
	}
}

type reserveState struct {
	Unavailable    bool
	NextRetryAfter time.Time
	Quota          QuotaState
	ModelStates    int
}

func reserveStateSnapshot(manager *Manager, accounts []*Auth) map[string]reserveState {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	snapshot := make(map[string]reserveState, len(accounts))
	for _, account := range accounts {
		current := manager.auths[account.ID]
		snapshot[account.ID] = reserveState{Unavailable: current.Unavailable, NextRetryAfter: current.NextRetryAfter,
			Quota: current.Quota, ModelStates: len(current.ModelStates)}
	}
	return snapshot
}
