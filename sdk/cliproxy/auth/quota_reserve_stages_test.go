package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestParseQuotaReservePolicy(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  string
		want QuotaReservePolicy
		err  string
	}{
		{name: "legacy soft", raw: `{"percent":30}`, want: QuotaReservePolicy{Percent: 30, Mode: "soft"}},
		{name: "three levels", raw: `{"percent":30,"mode":" SOFT ","steps":[15,5]}`, want: QuotaReservePolicy{Percent: 30, Mode: "soft", Steps: []int{15, 5}}},
		{name: "two levels", raw: `{"percent":99,"steps":[1]}`, want: QuotaReservePolicy{Percent: 99, Mode: "soft", Steps: []int{1}}},
		{name: "clear levels", raw: `{"percent":30,"steps":[]}`, want: QuotaReservePolicy{Percent: 30, Mode: "soft"}},
		{name: "hard empty levels", raw: `{"percent":30,"mode":"hard","steps":[]}`, want: QuotaReservePolicy{Percent: 30, Mode: "hard"}},
		{name: "hard levels", raw: `{"percent":30,"mode":"hard","steps":[15]}`, err: "only supported in soft mode"},
		{name: "too many", raw: `{"percent":30,"steps":[20,15,5]}`, err: "at most two"},
		{name: "equal first", raw: `{"percent":30,"steps":[30]}`, err: "strictly decrease"},
		{name: "above first", raw: `{"percent":30,"steps":[40]}`, err: "strictly decrease"},
		{name: "equal steps", raw: `{"percent":30,"steps":[15,15]}`, err: "strictly decrease"},
		{name: "increasing steps", raw: `{"percent":30,"steps":[5,15]}`, err: "strictly decrease"},
		{name: "zero", raw: `{"percent":30,"steps":[0]}`, err: "steps[0] must be an integer"},
		{name: "negative", raw: `{"percent":30,"steps":[-1]}`, err: "steps[0] must be an integer"},
		{name: "hundred", raw: `{"percent":30,"steps":[100]}`, err: "steps[0] must be an integer"},
		{name: "fractional", raw: `{"percent":30,"steps":[15,5.5]}`, err: "steps[1] must be an integer"},
		{name: "string", raw: `{"percent":30,"steps":["15"]}`, err: "steps[0] must be an integer"},
		{name: "null item", raw: `{"percent":30,"steps":[null]}`, err: "steps[0] must be an integer"},
		{name: "not array", raw: `{"percent":30,"steps":15}`, err: "must be an array"},
		{name: "null array", raw: `{"percent":30,"steps":null}`, err: "must be an array"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Both the file loader and the PATCH decoder feed the canonical parser.
			for _, useNumber := range []bool{false, true} {
				var raw any
				decoder := json.NewDecoder(strings.NewReader(tt.raw))
				if useNumber {
					decoder.UseNumber()
				}
				if errDecode := decoder.Decode(&raw); errDecode != nil {
					t.Fatal(errDecode)
				}
				got, errParse := ParseQuotaReservePolicy(raw)
				if tt.err != "" {
					if errParse == nil || !strings.Contains(errParse.Error(), tt.err) {
						t.Fatalf("error = %v, want %q", errParse, tt.err)
					}
				} else if errParse != nil || !reflect.DeepEqual(got, tt.want) {
					t.Fatalf("policy = %+v, %v; want %+v", got, errParse, tt.want)
				}
			}
		})
	}
	t.Run("legacy parser ignores unknown fields", func(t *testing.T) {
		percent, mode, errParse := ParseQuotaReserve(map[string]any{"percent": 30, "mode": "hard", "steps": []int{15, 5}})
		if errParse != nil || percent != 30 || mode != "hard" {
			t.Fatalf("legacy policy = %d, %q, %v", percent, mode, errParse)
		}
	})
}

func TestQuotaReservePolicyMetadataRoundTrip(t *testing.T) {
	auth := &Auth{Provider: "codex"}
	policy := map[string]any{"percent": 30, "mode": "soft", "steps": []int{15, 5}}
	ApplyAuthQuotaReserveMetadata(auth, map[string]any{"quota_reserve": policy})
	got, ok := QuotaReservePolicyForAuth(auth)
	want := QuotaReservePolicy{Percent: 30, Mode: "soft", Steps: []int{15, 5}}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("synced policy = %+v, %t; want %+v", got, ok, want)
	}
	got.Steps[0] = 20
	again, _ := QuotaReservePolicyForAuth(auth)
	if !reflect.DeepEqual(again, want) {
		t.Fatalf("accessor mutated routing policy: %+v", again)
	}
	exempt := withoutQuotaReserveFor([]*Auth{auth}, auth)[0]
	if _, ok := QuotaReservePolicyForAuth(exempt); ok || exempt.Attributes[AttributeQuotaReserveSteps] != "" {
		t.Fatalf("exemption retained a staged reserve: %v", exempt.Attributes)
	}
	if auth.Attributes[AttributeQuotaReserveSteps] != "[15,5]" {
		t.Fatal("exemption modified the original credential")
	}
	for _, raw := range []any{
		map[string]any{"percent": 20, "mode": "hard"},
		map[string]any{"percent": 30, "steps": []int{}},
		map[string]any{"percent": 30, "steps": []int{20, 25}},
		nil,
	} {
		ApplyAuthQuotaReserveMetadata(auth, map[string]any{"quota_reserve": policy})
		ApplyAuthQuotaReserveMetadata(auth, map[string]any{"quota_reserve": raw})
		if _, exists := auth.Attributes[AttributeQuotaReserveSteps]; exists {
			t.Fatalf("reload retained stale steps for %#v", raw)
		}
	}
	for _, raw := range []string{"[15,5,1]", "[15,20]", "[15.5]", "null", "garbage"} {
		ApplyAuthQuotaReserveMetadata(auth, map[string]any{"quota_reserve": policy})
		auth.Attributes[AttributeQuotaReserveSteps] = raw
		if _, ok := QuotaReservePolicyForAuth(auth); ok {
			t.Fatalf("accepted invalid steps attribute %q", raw)
		}
	}
}

func TestQuotaReserveLevelBoundaries(t *testing.T) {
	now := time.Unix(2000000000, 0)
	reset := now.Add(time.Hour)
	for _, provider := range []string{"codex", "claude"} {
		for _, tt := range []struct {
			remaining float64
			level     int
		}{
			{remaining: 40, level: 0},
			{remaining: 30, level: 0},
			{remaining: 29.9, level: 1},
			{remaining: 15, level: 1},
			{remaining: 14.9, level: 2},
			{remaining: 5, level: 2},
			{remaining: 4.9, level: 3},
			{remaining: 0, level: 3},
			{remaining: -10, level: 3},
		} {
			t.Run(fmt.Sprintf("%s/remaining=%g", provider, tt.remaining), func(t *testing.T) {
				auth := &Auth{Provider: provider, Quota: QuotaState{ObservedAt: now.Add(-time.Minute)}}
				ApplyAuthQuotaReserveMetadata(auth, map[string]any{"quota_reserve": map[string]any{"percent": 30, "steps": []int{15, 5}}})
				if provider == "codex" {
					auth.Quota.Signals = map[string]string{
						"X-Codex-Primary-Used-Percent": strconv.FormatFloat(100-tt.remaining, 'f', -1, 64),
						"X-Codex-Primary-Reset-At":     strconv.FormatInt(reset.Unix(), 10),
					}
				} else {
					auth.Quota.Signals = map[string]string{
						"Anthropic-Ratelimit-Unified-5h-Utilization": strconv.FormatFloat((100-tt.remaining)/100, 'f', -1, 64),
						"Anthropic-Ratelimit-Unified-5h-Reset":       reset.Format(time.RFC3339),
					}
				}
				level, until := QuotaReserveLevel(auth, now)
				if level != tt.level || (level > 0 && !until.Equal(reset)) || (level == 0 && !until.IsZero()) {
					t.Fatalf("level, until = %d, %v; want level %d", level, until, tt.level)
				}
			})
		}
	}
}

func TestQuotaReserveLevelWindowRecovery(t *testing.T) {
	now := time.Unix(2000000000, 0)
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			auth := &Auth{Provider: provider, Quota: QuotaState{ObservedAt: now.Add(-time.Minute)}}
			ApplyAuthQuotaReserveMetadata(auth, map[string]any{"quota_reserve": map[string]any{"percent": 30, "steps": []int{15, 5}}})
			if provider == "codex" {
				auth.Quota.Signals = map[string]string{
					"X-Codex-Primary-Used-Percent":   "96",
					"X-Codex-Primary-Reset-At":       strconv.FormatInt(now.Add(time.Hour).Unix(), 10),
					"X-Codex-Secondary-Used-Percent": "80",
					"X-Codex-Secondary-Reset-At":     strconv.FormatInt(now.Add(48*time.Hour).Unix(), 10),
				}
			} else {
				auth.Quota.Signals = map[string]string{
					"Anthropic-Ratelimit-Unified-5h-Utilization": "0.96",
					"Anthropic-Ratelimit-Unified-5h-Reset":       now.Add(time.Hour).Format(time.RFC3339),
					"Anthropic-Ratelimit-Unified-7d-Utilization": "0.8",
					"Anthropic-Ratelimit-Unified-7d-Reset":       now.Add(48 * time.Hour).Format(time.RFC3339),
				}
			}
			for _, tt := range []struct {
				elapsed time.Duration
				level   int
			}{
				{elapsed: 0, level: 3},
				{elapsed: time.Hour, level: 1},
				{elapsed: 48 * time.Hour, level: 0},
			} {
				level, until := QuotaReserveLevel(auth, now.Add(tt.elapsed))
				if level != tt.level || (level > 0 && !until.Equal(now.Add(48*time.Hour))) || (level == 0 && !until.IsZero()) {
					t.Fatalf("after %s: level=%d until=%v; want level %d", tt.elapsed, level, until, tt.level)
				}
			}
		})
	}
	// A newer snapshot on any model replaces, rather than merges with, older windows.
	auth := reserveSpecAuth("snapshot", reserveSpec{mode: "soft", steps: []int{15, 5}, used: 96}, now.Add(-time.Minute))
	auth.ModelStates = map[string]*ModelState{"other-model": {Quota: reserveQuota(now, 90, 2*time.Hour)}}
	if level, until := QuotaReserveLevel(auth, now); level != 2 || !until.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("newer snapshot level=%d until=%v; want level 2 and 2h", level, until)
	}
}

func TestQuotaReserveStagesAcrossStrategies(t *testing.T) {
	for _, newSelector := range reserveSelectors() {
		for _, mixed := range []bool{false, true} {
			for _, tt := range []struct {
				name  string
				specs []reserveSpec
				want  int
			}{
				{name: "normal before soft1", specs: []reserveSpec{
					{priority: "10", mode: "soft", steps: []int{15, 5}, used: 80},
					{priority: "0", mode: "soft", steps: []int{15, 5}, used: 70},
				}, want: 1},
				{name: "soft1 before high priority soft2", specs: []reserveSpec{
					{priority: "10", mode: "soft", steps: []int{15, 5}, used: 90},
					{priority: "0", mode: "soft", steps: []int{15, 5}, used: 80},
				}, want: 1},
				{name: "soft2 before high priority soft3", specs: []reserveSpec{
					{priority: "10", mode: "soft", steps: []int{15, 5}, used: 98},
					{priority: "0", mode: "soft", steps: []int{15, 5}, used: 90},
				}, want: 1},
				{name: "soft3 is last resort", specs: []reserveSpec{
					{mode: "hard", used: 90},
					{mode: "soft", steps: []int{15, 5}, used: 99},
				}, want: 1},
				{name: "priority within same stage", specs: []reserveSpec{
					{priority: "0", mode: "soft", steps: []int{15, 5}, used: 90},
					{priority: "10", mode: "soft", steps: []int{15, 5}, used: 90},
				}, want: 1},
				{name: "legacy soft remains level1", specs: []reserveSpec{
					{mode: "soft", steps: []int{15, 5}, used: 90},
					{mode: "soft", used: 99},
				}, want: 1},
			} {
				t.Run(fmt.Sprintf("%T/mixed=%t/%s", newSelector(), mixed, tt.name), func(t *testing.T) {
					manager, accounts, model := reserveTestManager(t, newSelector(), tt.specs)
					for range 3 {
						selected, errPick := reservePick(manager, model, "", mixed)
						if errPick != nil || selected == nil || selected.ID != accounts[tt.want].ID {
							t.Fatalf("selected %v, %v; want %s", selected, errPick, accounts[tt.want].ID)
						}
					}
				})
			}
		}
	}
}

func TestQuotaReserveStagesKeepBoundAndPinnedSessions(t *testing.T) {
	for _, newSelector := range reserveSelectors() {
		for _, mixed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%T/mixed=%t", newSelector(), mixed), func(t *testing.T) {
				selector := NewSessionAffinitySelector(newSelector())
				t.Cleanup(selector.Stop)
				manager, accounts, model := reserveTestManager(t, selector, []reserveSpec{
					{mode: "soft", steps: []int{15, 5}, used: 10},
					{mode: "soft", steps: []int{15, 5}, used: 80},
				})
				pick := func(session string, want int) {
					t.Helper()
					selected, errPick := reservePick(manager, model, session, mixed)
					if errPick != nil || selected == nil || selected.ID != accounts[want].ID {
						t.Fatalf("session %q selected %v, %v; want %s", session, selected, errPick, accounts[want].ID)
					}
				}
				pick("bound", 0)
				manager.mu.Lock()
				manager.auths[accounts[0].ID].Quota = reserveQuota(time.Now().Add(-time.Second), 98, time.Hour)
				manager.mu.Unlock()
				pick("bound", 0)
				pick("fresh", 1)
				pick("bound", 0)

				opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: accounts[0].ID}}
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

func TestQuotaReserveStagesSelectionDetails(t *testing.T) {
	t.Run("earliest reset within lowest stage", func(t *testing.T) {
		manager, accounts, model := reserveTestManager(t, &EarliestResetSelector{}, []reserveSpec{
			{mode: "soft", steps: []int{15, 5}, used: 98, reset: time.Hour},
			{mode: "soft", steps: []int{15, 5}, used: 90, reset: 3 * time.Hour},
			{mode: "soft", steps: []int{15, 5}, used: 90, reset: 2 * time.Hour},
		})
		selected, errPick := reservePick(manager, model, "", false)
		if errPick != nil || selected == nil || selected.ID != accounts[2].ID {
			t.Fatalf("selected %v, %v; want %s", selected, errPick, accounts[2].ID)
		}
	})
	t.Run("preferred account within lowest stage", func(t *testing.T) {
		manager, accounts, model := reserveTestManager(t, &FillFirstSelector{}, []reserveSpec{
			{mode: "soft", steps: []int{15, 5}, used: 98},
			{mode: "soft", steps: []int{15, 5}, used: 90},
			{mode: "soft", steps: []int{15, 5}, used: 90},
		})
		for _, tt := range []struct{ prefer, want int }{{0, 1}, {2, 2}} {
			setPreferredAccount(manager, "codex", accounts[tt.prefer].Index)
			selected, errPick := reservePick(manager, model, "", false)
			if errPick != nil || selected == nil || selected.ID != accounts[tt.want].ID {
				t.Fatalf("selected %v, %v; want %s", selected, errPick, accounts[tt.want].ID)
			}
		}
	})
	t.Run("zero weight lower stage does not hide available higher stage", func(t *testing.T) {
		manager, accounts, model := reserveTestManager(t, &WeightedRoundRobinSelector{}, []reserveSpec{
			{mode: "soft", steps: []int{15, 5}, used: 80, weight: "0"},
			{mode: "soft", steps: []int{15, 5}, used: 90, weight: "1"},
		})
		selected, errPick := reservePick(manager, model, "", false)
		if errPick != nil || selected == nil || selected.ID != accounts[1].ID {
			t.Fatalf("selected %v, %v; want %s", selected, errPick, accounts[1].ID)
		}
	})
	t.Run("delegated plugin respects stages", func(t *testing.T) {
		manager, accounts, model := reserveTestManager(t, &RoundRobinSelector{}, []reserveSpec{
			{mode: "soft", steps: []int{15, 5}, used: 98},
			{mode: "soft", steps: []int{15, 5}, used: 90},
		})
		manager.SetPluginScheduler(&fakePluginScheduler{
			resp: pluginapi.SchedulerPickResponse{Handled: true, DelegateBuiltin: pluginapi.SchedulerBuiltinFillFirst}, handled: true,
		})
		selected, errPick := reservePick(manager, model, "", false)
		if errPick != nil || selected == nil || selected.ID != accounts[1].ID {
			t.Fatalf("selected %v, %v; want %s", selected, errPick, accounts[1].ID)
		}
	})
}

func TestQuotaReserveStagesFilterRecoversWithoutState(t *testing.T) {
	now := time.Unix(2000000000, 0)
	soon := reserveSpecAuth("soon", reserveSpec{mode: "soft", steps: []int{15, 5}, used: 98, reset: time.Hour}, now)
	later := reserveSpecAuth("later", reserveSpec{mode: "soft", steps: []int{15, 5}, used: 90, reset: 2 * time.Hour}, now)
	accounts := []*Auth{soon, later}
	for _, tt := range []struct {
		elapsed time.Duration
		want    []*Auth
	}{
		{elapsed: 0, want: []*Auth{later}},
		{elapsed: time.Hour, want: []*Auth{soon}},
		{elapsed: 2 * time.Hour, want: accounts},
	} {
		selected, hardCount, _ := quotaReserveFilter(accounts, now.Add(tt.elapsed))
		if hardCount != 0 || !reflect.DeepEqual(selected, tt.want) {
			t.Fatalf("after %s selected %v; want %v", tt.elapsed, authIDs(selected), authIDs(tt.want))
		}
	}
	if level, _ := QuotaReserveLevel(soon, now); level != 3 {
		t.Fatalf("filter mutated observed quota: original level = %d, want 3", level)
	}
}

func TestQuotaReserveStagesRoundRobinWithinLastResort(t *testing.T) {
	manager, accounts, model := reserveTestManager(t, &RoundRobinSelector{}, []reserveSpec{
		{mode: "soft", steps: []int{15, 5}, used: 98},
		{mode: "soft", steps: []int{15, 5}, used: 99},
	})
	seen := map[string]int{}
	for range 4 {
		selected, errPick := reservePick(manager, model, "", false)
		if errPick != nil {
			t.Fatal(errPick)
		}
		seen[selected.ID]++
	}
	if seen[accounts[0].ID] != 2 || seen[accounts[1].ID] != 2 {
		t.Fatalf("round-robin over soft3 = %v", seen)
	}
}
