package auth

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestPipelineSelectorDisablesSchedulerFastPath(t *testing.T) {
	shared := func() config.SelectionConfig { return config.DefaultSelection() }
	for _, tt := range []struct {
		name     string
		selector func() Selector
		fastPath bool
	}{
		{name: "built-in round-robin", selector: func() Selector { return &RoundRobinSelector{} }, fastPath: true},
		{name: "pipeline", selector: func() Selector { return NewPipelineSelector(shared) }},
		{name: "affinity over pipeline", selector: func() Selector { return NewSessionAffinitySelector(NewPipelineSelector(shared)) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			selector := tt.selector()
			if stoppable, ok := selector.(StoppableSelector); ok {
				t.Cleanup(stoppable.Stop)
			}
			manager := NewManager(nil, selector, nil)
			if got := manager.useSchedulerFastPath(context.Background()); got != tt.fastPath {
				t.Fatalf("useSchedulerFastPath() = %t, want %t", got, tt.fastPath)
			}
			if !tt.fastPath && manager.scheduler.strategy != schedulerStrategyCustom {
				t.Fatalf("scheduler strategy = %v, want custom", manager.scheduler.strategy)
			}

			// Switching a built-in manager to the pipeline turns the fast path off.
			builtIn := NewManager(nil, &RoundRobinSelector{}, nil)
			builtIn.SetSelector(tt.selector())
			if stoppable, ok := builtIn.Selector().(StoppableSelector); ok {
				t.Cleanup(stoppable.Stop)
			}
			if got := builtIn.useSchedulerFastPath(context.Background()); got != tt.fastPath {
				t.Fatalf("useSchedulerFastPath() after SetSelector = %t, want %t", got, tt.fastPath)
			}
		})
	}
}

func TestAvailableAuthsForSelectorHandsPipelineEveryTier(t *testing.T) {
	specs := []reserveSpec{
		{priority: "10", mode: QuotaReserveModeHard, used: 90},
		{priority: "10", mode: QuotaReserveModeSoft, used: 90},
		{priority: "0"},
	}
	ids := func(auths []*Auth) []string { return authIDs(auths) }
	available := func(t *testing.T, manager *Manager, selector Selector, reserve bool) (priorityAuths, selectorAuths []*Auth) {
		t.Helper()
		manager.mu.RLock()
		candidates := make([]*Auth, 0, len(manager.auths))
		for _, auth := range manager.auths {
			candidates = append(candidates, auth)
		}
		var errNewSelection, errAvailable error
		priorityAuths, selectorAuths, errNewSelection, errAvailable = manager.availableAuthsForSelector(context.Background(), selector, candidates, "codex", "", cliproxyexecutor.Options{}, time.Now(), pluginSchedulerWantsAcrossPriorities(manager.pluginScheduler), reserve)
		manager.mu.RUnlock()
		if errAvailable != nil || errNewSelection != nil {
			t.Fatalf("availableAuthsForSelector() errors = %v, %v", errAvailable, errNewSelection)
		}
		return priorityAuths, selectorAuths
	}

	t.Run("pipeline", func(t *testing.T) {
		selector := NewPipelineSelector(nil)
		manager, accounts, _ := reserveTestManager(t, selector, specs)
		priorityAuths, selectorAuths := available(t, manager, selector, true)
		if want := ids(accounts[1:]); !reflect.DeepEqual(ids(selectorAuths), want) {
			t.Fatalf("selector candidates = %v, want soft and lower tier %v", ids(selectorAuths), want)
		}
		if want := ids(accounts[2:]); !reflect.DeepEqual(ids(priorityAuths), want) {
			t.Fatalf("priority candidates = %v, want legacy reserve view %v", ids(priorityAuths), want)
		}
		_, selectorAuths = available(t, manager, selector, false)
		if want := ids(accounts); !reflect.DeepEqual(ids(selectorAuths), want) {
			t.Fatalf("pinned selector candidates = %v, want every credential %v", ids(selectorAuths), want)
		}

		ctx := selectorContextForAvailableAuths(context.Background(), selector, "model")
		if !pipelineCandidatesPrefiltered(ctx) {
			t.Fatal("selector context does not mark pipeline candidates as prefiltered")
		}
		if validated, _ := ctx.Value(prevalidatedAuthCandidatesKey{}).(bool); !validated {
			t.Fatal("selector context does not mark candidates as prevalidated")
		}
	})

	t.Run("affinity over pipeline", func(t *testing.T) {
		selector := NewSessionAffinitySelector(NewPipelineSelector(nil))
		t.Cleanup(selector.Stop)
		manager, accounts, _ := reserveTestManager(t, selector, specs)
		_, selectorAuths := available(t, manager, selector, true)
		// Bound sessions are validated against every available credential.
		if want := ids(accounts); !reflect.DeepEqual(ids(selectorAuths), want) {
			t.Fatalf("selector candidates = %v, want every tier %v", ids(selectorAuths), want)
		}
	})

	t.Run("selection reaches every tier and soft level", func(t *testing.T) {
		selection := config.SelectionConfig{Primary: config.SelectionRoundRobin}
		selector := NewPipelineSelector(func() config.SelectionConfig { return selection })
		manager, accounts, model := reserveTestManager(t, selector, specs)
		seen := map[string]int{}
		for range 4 {
			selected, errPick := reservePick(manager, model, "", false)
			if errPick != nil {
				t.Fatal(errPick)
			}
			seen[selected.ID]++
		}
		if seen[accounts[1].ID] != 2 || seen[accounts[2].ID] != 2 || seen[accounts[0].ID] != 0 {
			t.Fatalf("round-robin picks = %v, want the soft high tier and the lower tier twice each", seen)
		}
	})

	t.Run("all hard-reserved reports the reserve cooldown", func(t *testing.T) {
		selector := NewPipelineSelector(nil)
		manager, _, model := reserveTestManager(t, selector, []reserveSpec{
			{mode: QuotaReserveModeHard, used: 90, reset: 3 * time.Hour},
			{mode: QuotaReserveModeHard, used: 90, reset: time.Hour},
		})
		selected, errPick := reservePick(manager, model, "", false)
		assertReserveCooldown(t, selected, errPick, time.Hour)
	})
}

func TestPipelineSelectorPreferredBeforeSoftReserveThroughManager(t *testing.T) {
	selector := NewPipelineSelector(nil)
	manager, accounts, model := reserveTestManager(t, selector, []reserveSpec{
		{mode: QuotaReserveModeSoft, used: 10},
		{mode: QuotaReserveModeSoft, used: 90},
	})
	setPreferredAccount(manager, "codex", accounts[1].Index)
	for range 3 {
		if selected, errPick := reservePick(manager, model, "", false); errPick != nil || selected.ID != accounts[1].ID {
			t.Fatalf("selected %v, %v; want the soft-reserved preferred %s", selected, errPick, accounts[1].ID)
		}
	}
}
