package auth

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestRoutingActivityTracksLatestSelectionAndRejectsStaleCredentials(t *testing.T) {
	ctx := WithSkipPersist(context.Background())
	manager := NewManager(nil, nil, nil)
	register := func(id, provider string) *Auth {
		t.Helper()
		auth, errRegister := manager.Register(ctx, &Auth{ID: id, Provider: provider})
		if errRegister != nil {
			t.Fatal(errRegister)
		}
		return auth
	}
	codex := register("routing-codex", "codex")
	claude := register("routing-claude", "claude")
	now := time.Date(2026, 10, 2, 14, 0, 0, 123, time.UTC)
	manager.recordRoutingSelection(codex, now)
	manager.recordRoutingSelection(claude, now.Add(time.Minute))
	manager.recordRoutingSelection(codex, now.Add(-time.Minute))
	accounts := manager.RoutingActivity()
	if len(accounts) != 2 || accounts[0].AuthIndex != claude.Index || accounts[1].AuthIndex != codex.Index {
		t.Fatalf("unexpected account order: %+v", accounts)
	}
	if !accounts[1].LastSelectedAt.Equal(now) {
		t.Fatalf("older observation replaced latest selection: %+v", accounts[1])
	}
	accounts[1].AuthIndex = "changed"
	if manager.RoutingActivity()[1].AuthIndex != codex.Index {
		t.Fatal("snapshot shares mutable state with manager")
	}

	manager.Remove(ctx, codex.ID)
	manager.recordRoutingSelection(codex, now.Add(time.Hour))
	if got := manager.RoutingActivity(); len(got) != 1 || got[0].AuthIndex != claude.Index {
		t.Fatalf("removed credential remained visible: %+v", got)
	}
	newCodex := register(codex.ID, codex.Provider)
	manager.recordRoutingSelection(codex, now.Add(2*time.Hour))
	if got := manager.RoutingActivity(); len(got) != 1 {
		t.Fatalf("stale selection revived replaced credential: %+v", got)
	}
	manager.recordRoutingSelection(newCodex, now.Add(3*time.Hour))
	if got := manager.RoutingActivity(); len(got) != 2 || !got[1].LastSelectedAt.Equal(now.Add(3*time.Hour)) {
		t.Fatalf("new credential selection missing: %+v", got)
	}
	register(claude.ID, claude.Provider)
	if got := manager.RoutingActivity(); len(got) != 1 || got[0].AuthIndex != newCodex.Index {
		t.Fatalf("re-registration inherited old selection: %+v", got)
	}
}

func TestRoutingActivityRecordsWithoutMetadata(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth, errRegister := manager.Register(WithSkipPersist(context.Background()), &Auth{ID: "no-metadata", Provider: "codex"})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	manager.publishSelectedAuthMetadata(nil, auth)
	accounts := manager.RoutingActivity()
	if len(accounts) != 1 || accounts[0].AuthIndex != auth.Index || accounts[0].LastSelectedAt.IsZero() {
		t.Fatalf("selection without metadata missing: %+v", accounts)
	}
	manager.publishSelectedAuthMetadata(nil, nil)
	if len(manager.RoutingActivity()) != 1 {
		t.Fatal("nil selection changed history")
	}
}

type routingActivityExecutor struct{ schedulerTestExecutor }

func (routingActivityExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	chunks := make(chan cliproxyexecutor.StreamChunk)
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func TestRoutingActivityExecutionPaths(t *testing.T) {
	for _, mode := range []string{"execute", "count", "stream"} {
		t.Run(mode, func(t *testing.T) {
			ctx := WithSkipPersist(context.Background())
			provider := "activity-" + mode
			model := "activity-model"
			manager := NewManager(nil, nil, nil)
			manager.RegisterExecutor(routingActivityExecutor{schedulerTestExecutor{provider: provider}})
			auth, errRegister := manager.Register(ctx, &Auth{ID: provider, Provider: provider, Status: StatusActive})
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			request := cliproxyexecutor.Request{Model: model}
			var errExecute error
			switch mode {
			case "execute":
				_, errExecute = manager.Execute(ctx, []string{provider}, request, cliproxyexecutor.Options{})
			case "count":
				_, errExecute = manager.ExecuteCount(ctx, []string{provider}, request, cliproxyexecutor.Options{})
			case "stream":
				var stream *cliproxyexecutor.StreamResult
				stream, errExecute = manager.ExecuteStream(ctx, []string{provider}, request, cliproxyexecutor.Options{})
				if errExecute == nil {
					for range stream.Chunks {
					}
				}
			}
			if errExecute != nil {
				t.Fatal(errExecute)
			}
			if got := manager.RoutingActivity(); len(got) != 1 || got[0].AuthIndex != auth.Index || got[0].LastSelectedAt.IsZero() {
				t.Fatalf("execution selection missing: %+v", got)
			}
		})
	}
}
