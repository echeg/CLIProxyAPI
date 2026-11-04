package auth

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// pipelineTestNow is a whole second so reset timestamps truncated to seconds stay exact.
var pipelineTestNow = time.Unix(1_800_000_000, 0)

const pipelineTestModel = "pipeline-model"

func pipelineTestAuth(id string, spec reserveSpec) *Auth {
	return reserveSpecAuth(id, spec, pipelineTestNow.Add(-time.Minute))
}

func pipelineDisabled(auth *Auth) *Auth {
	auth.Disabled = true
	return auth
}

func pipelineCooling(auth *Auth, recoverIn time.Duration) *Auth {
	coolReserveAuth(auth, pipelineTestNow.Add(recoverIn))
	return auth
}

func pipelineModelBlocked(auth *Auth, retryIn time.Duration) *Auth {
	auth.ModelStates = map[string]*ModelState{pipelineTestModel: {
		Status: StatusActive, Unavailable: true, NextRetryAfter: pipelineTestNow.Add(retryIn),
	}}
	return auth
}

func TestAvailableForPipelineCandidates(t *testing.T) {
	hard := pipelineTestAuth("a-hard", reserveSpec{priority: "10", mode: QuotaReserveModeHard, used: 90})
	soft1 := pipelineTestAuth("b-soft1", reserveSpec{priority: "10", mode: QuotaReserveModeSoft, used: 80})
	soft3 := pipelineTestAuth("c-soft3", reserveSpec{priority: "0", mode: QuotaReserveModeSoft, steps: []int{15, 5}, used: 97})
	free := pipelineTestAuth("d-free", reserveSpec{priority: "5"})
	healthyHard := pipelineTestAuth("e-healthy-hard", reserveSpec{priority: "0", mode: QuotaReserveModeHard, used: 10})
	disabled := pipelineDisabled(pipelineTestAuth("f-disabled", reserveSpec{priority: "10"}))
	cooling := pipelineCooling(pipelineTestAuth("g-cooling", reserveSpec{priority: "10"}), 10*time.Minute)
	modelBlocked := pipelineModelBlocked(pipelineTestAuth("h-model-blocked", reserveSpec{priority: "10"}), 20*time.Minute)

	in := []*Auth{modelBlocked, free, cooling, soft3, hard, disabled, healthyHard, soft1}
	got, errAvailable := availableForPipeline(context.Background(), in, "codex", pipelineTestModel, pipelineTestNow)
	if errAvailable != nil {
		t.Fatalf("availableForPipeline error = %v", errAvailable)
	}
	// Every tier and every soft level stays, ID-sorted; hard-reserved and blocked are gone.
	want := []string{"b-soft1", "c-soft3", "d-free", "e-healthy-hard"}
	if ids := authIDs(got); !reflect.DeepEqual(ids, want) {
		t.Fatalf("candidates = %v, want %v", ids, want)
	}

	t.Run("reserve skipped keeps hard reserved", func(t *testing.T) {
		got, errAvailable := availableForPipeline(withQuotaReserveSkipped(context.Background()), in, "codex", pipelineTestModel, pipelineTestNow)
		if errAvailable != nil {
			t.Fatalf("availableForPipeline error = %v", errAvailable)
		}
		want := []string{"a-hard", "b-soft1", "c-soft3", "d-free", "e-healthy-hard"}
		if ids := authIDs(got); !reflect.DeepEqual(ids, want) {
			t.Fatalf("candidates = %v, want %v", ids, want)
		}
	})
}

func TestAvailableForPipelineEmptyResult(t *testing.T) {
	for _, tt := range []struct {
		name         string
		auths        []*Auth
		provider     string
		wantCooldown bool
		wantResetIn  time.Duration
		wantCode     string
	}{
		{
			name:         "hard reserve only",
			auths:        []*Auth{pipelineTestAuth("a", reserveSpec{mode: QuotaReserveModeHard, used: 90, reset: time.Hour})},
			wantCooldown: true,
			wantResetIn:  59 * time.Minute,
		},
		{
			name: "cooldown recovers before hard reserve",
			auths: []*Auth{
				pipelineTestAuth("a", reserveSpec{mode: QuotaReserveModeHard, used: 90, reset: time.Hour}),
				pipelineCooling(pipelineTestAuth("b", reserveSpec{}), 30*time.Minute),
			},
			wantCooldown: true,
			wantResetIn:  30 * time.Minute,
		},
		{
			name: "hard reserve recovers before cooldown",
			auths: []*Auth{
				pipelineTestAuth("a", reserveSpec{mode: QuotaReserveModeHard, used: 90, reset: 20 * time.Minute}),
				pipelineCooling(pipelineTestAuth("b", reserveSpec{}), 3*time.Hour),
			},
			provider:     "mixed",
			wantCooldown: true,
			wantResetIn:  19 * time.Minute,
		},
		{
			name: "cooling hard reserve recovers at the later deadline",
			auths: []*Auth{
				pipelineCooling(pipelineTestAuth("a", reserveSpec{mode: QuotaReserveModeHard, used: 90, reset: time.Hour}), 10*time.Minute),
			},
			wantCooldown: true,
			wantResetIn:  59 * time.Minute,
		},
		{
			name: "model blocked is unavailable, not cooldown",
			auths: []*Auth{
				pipelineModelBlocked(pipelineTestAuth("a", reserveSpec{}), 20*time.Minute),
			},
			wantCode: "auth_unavailable",
		},
		{
			name:     "disabled only",
			auths:    []*Auth{pipelineDisabled(pipelineTestAuth("a", reserveSpec{}))},
			wantCode: "auth_unavailable",
		},
		{
			name:     "no candidates",
			wantCode: "auth_not_found",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			provider := tt.provider
			if provider == "" {
				provider = "codex"
			}
			got, errAvailable := availableForPipeline(context.Background(), tt.auths, provider, pipelineTestModel, pipelineTestNow)
			if got != nil {
				t.Fatalf("candidates = %v, want none", authIDs(got))
			}
			var cooldownErr *modelCooldownError
			if tt.wantCooldown {
				if !errors.As(errAvailable, &cooldownErr) {
					t.Fatalf("error = %v, want model cooldown", errAvailable)
				}
				if cooldownErr.resetIn != tt.wantResetIn {
					t.Fatalf("resetIn = %s, want %s", cooldownErr.resetIn, tt.wantResetIn)
				}
				if provider == "mixed" && cooldownErr.provider != "" {
					t.Fatalf("mixed cooldown provider = %q, want empty", cooldownErr.provider)
				}
				if retryAfter := cooldownErr.Headers().Get("Retry-After"); retryAfter == "" {
					t.Fatal("Retry-After header missing")
				}
				return
			}
			if errors.As(errAvailable, &cooldownErr) {
				t.Fatalf("error = %v, want %s", errAvailable, tt.wantCode)
			}
			var authErr *Error
			if !errors.As(errAvailable, &authErr) || authErr.Code != tt.wantCode {
				t.Fatalf("error = %v, want code %s", errAvailable, tt.wantCode)
			}
		})
	}
}

func preferredPipelineContext(preferences map[string]string) context.Context {
	return withRoutingProfile(context.Background(), &routingProfile{preferred: preferences})
}

func TestPreferredAvailableAuth(t *testing.T) {
	newPool := func() (preferred *Auth, auths []*Auth) {
		preferred = pipelineTestAuth("b-preferred", reserveSpec{priority: "0", mode: QuotaReserveModeSoft, steps: []int{15, 5}, used: 97})
		preferred.Index = "idx-preferred"
		other := pipelineTestAuth("a-other", reserveSpec{priority: "10"})
		other.Index = "idx-other"
		return preferred, []*Auth{other, preferred}
	}
	ctx := preferredPipelineContext(map[string]string{"codex": "idx-preferred"})

	t.Run("soft3 preferred is taken", func(t *testing.T) {
		preferred, auths := newPool()
		available, errAvailable := availableForPipeline(ctx, auths, "codex", pipelineTestModel, pipelineTestNow)
		if errAvailable != nil {
			t.Fatal(errAvailable)
		}
		if level, _ := QuotaReserveLevel(preferred, pipelineTestNow); level != 3 {
			t.Fatalf("preferred reserve level = %d, want 3", level)
		}
		if got := preferredAvailableAuth(ctx, "codex", available); got != preferred {
			t.Fatalf("preferred = %v, want %s", got, preferred.ID)
		}
		if got := preferredAvailableAuth(ctx, "mixed", available); got != preferred {
			t.Fatalf("mixed preferred = %v, want %s", got, preferred.ID)
		}
	})

	for _, tt := range []struct {
		name   string
		mutate func(preferred *Auth, auths []*Auth) []*Auth
	}{
		{name: "hard reserve", mutate: func(preferred *Auth, auths []*Auth) []*Auth {
			preferred.Attributes[AttributeQuotaReserveMode] = QuotaReserveModeHard
			delete(preferred.Attributes, AttributeQuotaReserveSteps)
			return auths
		}},
		{name: "cooldown", mutate: func(preferred *Auth, auths []*Auth) []*Auth {
			pipelineCooling(preferred, 10*time.Minute)
			return auths
		}},
		{name: "disabled", mutate: func(preferred *Auth, auths []*Auth) []*Auth {
			pipelineDisabled(preferred)
			return auths
		}},
		{name: "model blocked", mutate: func(preferred *Auth, auths []*Auth) []*Auth {
			pipelineModelBlocked(preferred, 10*time.Minute)
			return auths
		}},
		// The manager drops credentials that do not support the model before selection,
		// and a removed credential is never a candidate: both are simply absent.
		{name: "unsupported model or removed credential", mutate: func(preferred *Auth, auths []*Auth) []*Auth {
			return auths[:1]
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			preferred, auths := newPool()
			auths = tt.mutate(preferred, auths)
			available, errAvailable := availableForPipeline(ctx, auths, "codex", pipelineTestModel, pipelineTestNow)
			if errAvailable != nil {
				t.Fatal(errAvailable)
			}
			if got := preferredAvailableAuth(ctx, "codex", available); got != nil {
				t.Fatalf("preferred = %s, want nil", got.ID)
			}
		})
	}

	t.Run("no preference for the provider", func(t *testing.T) {
		_, auths := newPool()
		for _, tt := range []struct {
			name     string
			ctx      context.Context
			provider string
		}{
			{name: "no profile", ctx: context.Background(), provider: "codex"},
			{name: "empty preferences", ctx: preferredPipelineContext(nil), provider: "codex"},
			{name: "other provider preferred", ctx: preferredPipelineContext(map[string]string{"claude": "idx-preferred"}), provider: "codex"},
			{name: "requested provider differs", ctx: ctx, provider: "claude"},
		} {
			if got := preferredAvailableAuth(tt.ctx, tt.provider, auths); got != nil {
				t.Fatalf("%s: preferred = %s, want nil", tt.name, got.ID)
			}
		}
	})
}

func TestPickerOnlyContextLeavesCandidatesUntouched(t *testing.T) {
	observed := time.Now().Add(-time.Minute)
	// Without picker-only, the reserve, the preference, and the top tier would all
	// narrow this set to c-top.
	hard := reserveSpecAuth("a-hard", reserveSpec{priority: "0", mode: QuotaReserveModeHard, used: 90}, observed)
	low := reserveSpecAuth("b-low", reserveSpec{priority: "0"}, observed)
	top := reserveSpecAuth("c-top", reserveSpec{priority: "10"}, observed)
	top.Index = "idx-top"
	auths := []*Auth{hard, low, top}
	ctx := preferredPipelineContext(map[string]string{"codex": "idx-top"})

	for _, tt := range []struct {
		name      string
		newPicker func() Selector
		picks     int
		want      []string
	}{
		{name: "round robin", newPicker: func() Selector { return &RoundRobinSelector{} }, picks: 3, want: []string{"a-hard", "b-low", "c-top"}},
		{name: "weighted round robin", newPicker: func() Selector { return &WeightedRoundRobinSelector{} }, picks: 3, want: []string{"a-hard", "b-low", "c-top"}},
		{name: "fill first", newPicker: func() Selector { return &FillFirstSelector{} }, picks: 3, want: []string{"a-hard"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			picked := func(ctx context.Context) []string {
				t.Helper()
				picker := tt.newPicker()
				seen := map[string]bool{}
				for i := 0; i < tt.picks; i++ {
					selected, errPick := picker.Pick(ctx, "codex", pipelineTestModel, cliproxyexecutor.Options{}, auths)
					if errPick != nil {
						t.Fatalf("Pick error = %v", errPick)
					}
					seen[selected.ID] = true
				}
				var ids []string
				for _, auth := range auths {
					if seen[auth.ID] {
						ids = append(ids, auth.ID)
					}
				}
				return ids
			}
			if got := picked(withPickerOnly(ctx)); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("picker-only picks = %v, want %v", got, tt.want)
			}
			if got := picked(ctx); !reflect.DeepEqual(got, []string{"c-top"}) {
				t.Fatalf("legacy picks = %v, want [c-top]", got)
			}
		})
	}

	t.Run("empty input still errors", func(t *testing.T) {
		if _, errPick := (&RoundRobinSelector{}).Pick(withPickerOnly(ctx), "codex", pipelineTestModel, cliproxyexecutor.Options{}, nil); errPick == nil {
			t.Fatal("Pick on empty candidates returned no error")
		}
	})
}
