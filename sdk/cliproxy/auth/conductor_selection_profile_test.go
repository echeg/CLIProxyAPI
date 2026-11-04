package auth

import (
	"context"
	"strings"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

const (
	selectionLogKeyPreferred = "sk-selection-log-preferred"
	selectionLogKeyPipeline  = "sk-selection-log-pipeline"
	selectionLogKeyShared    = "sk-selection-log-shared"
)

// routingSelectionEntries returns the routing profile selection log entries and
// fails when any of them carries a raw client key.
func routingSelectionEntries(t *testing.T, hook *logtest.Hook) []*log.Entry {
	t.Helper()
	var entries []*log.Entry
	for _, entry := range hook.AllEntries() {
		if entry.Message != "Routing profile selection" {
			continue
		}
		line, errLine := entry.String()
		if errLine != nil {
			t.Fatal(errLine)
		}
		for _, key := range []string{selectionLogKeyPreferred, selectionLogKeyPipeline, selectionLogKeyShared} {
			if strings.Contains(line, key) {
				t.Fatalf("log line leaked a raw key: %s", line)
			}
		}
		entries = append(entries, entry)
	}
	return entries
}

func TestPipelineSelectionLogsProfileStages(t *testing.T) {
	for _, tc := range []struct {
		name      string
		wrap      bool
		selection internalconfig.SelectionConfig
	}{
		{name: "narrowing primary", selection: internalconfig.SelectionConfig{Primary: "priority", Tiebreak: "round-robin"}},
		{name: "picker primary", selection: internalconfig.SelectionConfig{Primary: "fill-first"}},
		{name: "affinity cold path", wrap: true, selection: internalconfig.SelectionConfig{Primary: "reserves"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hook := newStandardLogHook(t)
			var selector Selector = NewPipelineSelector(nil)
			if tc.wrap {
				selector = NewSessionAffinitySelector(selector)
			}
			m, accounts, model := preferredAccountTestManager(t, "codex", selector)
			m.SetSelector(selector)
			profile := func(preferred map[string]string) *internalconfig.RoutingProfile {
				return &internalconfig.RoutingProfile{PreferredAccounts: preferred, Selection: tc.selection}
			}
			m.SetConfig(&internalconfig.Config{
				SDKConfig: internalconfig.SDKConfig{APIKeys: []string{selectionLogKeyPreferred, selectionLogKeyPipeline, selectionLogKeyShared}},
				Routing: internalconfig.RoutingConfig{
					// The shared preference proves a shared profile never logs.
					PreferredAccounts: map[string]string{"codex": accounts[1].Index},
					KeyProfiles: map[string]*internalconfig.RoutingProfile{
						selectionLogKeyPreferred: profile(map[string]string{"codex": accounts[2].Index}),
						selectionLogKeyPipeline:  profile(nil),
					},
				},
			})

			for _, step := range []struct {
				key       string
				ctx       context.Context
				wantStage string
				wantAuth  *Auth
			}{
				{key: selectionLogKeyPreferred, wantStage: routingStagePreferred, wantAuth: accounts[2]},
				{key: selectionLogKeyPipeline, wantStage: routingStagePipeline},
				{key: selectionLogKeyShared},
				{key: "no gin context", ctx: context.Background()},
			} {
				hook.Reset()
				ctx := step.ctx
				if ctx == nil {
					ctx = ginContextWithKey(step.key)
				}
				selected, _, errPick := m.pickNext(m.ensureRoutingProfile(ctx), "codex", model, cliproxyexecutor.Options{}, nil)
				if errPick != nil {
					t.Fatalf("%s: %v", step.key, errPick)
				}
				if step.wantAuth != nil && selected.ID != step.wantAuth.ID {
					t.Fatalf("%s selected %s, want %s", step.key, selected.ID, step.wantAuth.ID)
				}
				entries := routingSelectionEntries(t, hook)
				if step.wantStage == "" {
					if len(entries) != 0 {
						t.Fatalf("%s logged %d selections without a key profile: %v", step.key, len(entries), entries[0].Data)
					}
					continue
				}
				if len(entries) != 1 {
					t.Fatalf("%s logged %d selections, want 1", step.key, len(entries))
				}
				got := entries[0].Data
				if got["routing_stage"] != step.wantStage || got["routing_key"] != internalconfig.KeyHashPrefix(step.key) ||
					got["provider"] != "codex" || got["auth_index"] != selected.Index || got["fallback_stage"] != nil {
					t.Fatalf("%s log fields = %v", step.key, got)
				}
			}
		})
	}
}

func TestPipelineSelectionLogsNothingOnError(t *testing.T) {
	hook := newStandardLogHook(t)
	ctx := withRoutingProfile(context.Background(), &routingProfile{keyHash: "abc123def456", fromKey: true,
		selection: internalconfig.SelectionConfig{Primary: "weighted-round-robin"}})
	auths := []*Auth{{ID: "zero-weight", Provider: "codex", Status: StatusActive, Attributes: map[string]string{"weight": "0"}}}
	if _, errPick := NewPipelineSelector(nil).Pick(ctx, "codex", "", cliproxyexecutor.Options{}, auths); errPick == nil {
		t.Fatal("expected no positive-weight credential")
	}
	if entries := routingSelectionEntries(t, hook); len(entries) != 0 {
		t.Fatalf("failed selection logged: %v", entries[0].Data)
	}
}
