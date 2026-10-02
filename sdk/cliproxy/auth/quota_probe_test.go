package auth

import (
	"context"
	"net/http"
	"reflect"
	"testing"
	"time"
)

func TestManagerObserveQuotaProbePreservesRuntimeState(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	reset := time.Now().Add(time.Hour)
	base, errRegister := manager.Register(context.Background(), &Auth{
		ID: "probe-codex", Provider: "codex", Status: StatusError,
		Unavailable: true, NextRetryAfter: reset, Success: 12, Failed: 3,
		Metadata:  map[string]any{"access_token": "unchanged"},
		LastError: &Error{Code: "rate_limit", HTTPStatus: 429},
		Quota:     QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: reset, BackoffLevel: 2},
		ModelStates: map[string]*ModelState{
			"test-model": {Status: StatusError, Unavailable: true, NextRetryAfter: reset,
				Quota: QuotaState{Exceeded: true, NextRecoverAt: reset}},
		},
	})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	startedAt := time.Unix(1700000000, 0)
	headers := http.Header{"X-Codex-Primary-Used-Percent": {"25"}, "Authorization": {"ignored"}}
	if !manager.ObserveQuotaProbe(base, headers, startedAt) {
		t.Fatal("expected observation to be recorded")
	}
	got, exists := manager.GetByID(base.ID)
	if !exists {
		t.Fatal("credential missing")
	}
	want := base.Clone()
	want.Generation++
	want.Quota.ObservedAt = startedAt
	want.Quota.Signals = map[string]string{"X-Codex-Primary-Used-Percent": "25"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("probe changed unrelated state: got %+v, want %+v", got, want)
	}
	manager.scheduler.mu.Lock()
	scheduled := manager.scheduler.providers[base.Provider].auths[base.ID].auth.Clone()
	manager.scheduler.mu.Unlock()
	if !reflect.DeepEqual(scheduled, want) {
		t.Fatal("scheduler did not receive the current quota snapshot")
	}
	headers.Set("X-Codex-Primary-Used-Percent", "90")
	if got.Quota.Signals["X-Codex-Primary-Used-Percent"] != "25" {
		t.Fatal("observation retained mutable caller-owned headers")
	}
}

func TestManagerObserveQuotaProbeRejectsStaleAndUnrelatedObservations(t *testing.T) {
	for _, test := range []string{"newer snapshot", "replaced registration", "changed credentials", "changed account", "wrong provider", "removed", "zero timestamp", "empty headers"} {
		t.Run(test, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			base, errRegister := manager.Register(context.Background(), &Auth{
				ID: "probe-stale", Provider: "codex", Metadata: map[string]any{"access_token": "original"},
			})
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			startedAt := time.Unix(1700000000, 0)
			headers := http.Header{"X-Codex-Primary-Used-Percent": {"20"}}
			switch test {
			case "newer snapshot":
				manager.auths[base.ID].Quota = QuotaState{
					ObservedAt: startedAt.Add(time.Second), Signals: map[string]string{"X-Codex-Primary-Used-Percent": "95"},
				}
			case "replaced registration":
				manager.auths[base.ID].RegistrationEpoch++
			case "changed credentials":
				manager.auths[base.ID].Metadata["access_token"] = "replacement"
			case "changed account":
				manager.auths[base.ID].Metadata["account_id"] = "replacement"
			case "wrong provider":
				base.Provider = "claude"
			case "removed":
				delete(manager.auths, base.ID)
			case "zero timestamp":
				startedAt = time.Time{}
			case "empty headers":
				headers = nil
			}
			before := manager.auths[base.ID].Clone()
			if manager.ObserveQuotaProbe(base, headers, startedAt) {
				t.Fatal("unexpected observation")
			}
			if !reflect.DeepEqual(manager.auths[base.ID], before) {
				t.Fatal("rejected observation changed credential state")
			}
		})
	}
}
