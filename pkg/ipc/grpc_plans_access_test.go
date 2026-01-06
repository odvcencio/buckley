package ipc

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/odvcencio/buckley/pkg/config"
	ipcpb "github.com/odvcencio/buckley/pkg/ipc/proto"
	"github.com/odvcencio/buckley/pkg/orchestrator"
	"github.com/odvcencio/buckley/pkg/storage"
)

func TestGRPCListPlansFiltersByPrincipal(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "buckley.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now().UTC()
	if err := store.CreateSession(&storage.Session{ID: "s-alice", Principal: "alice", CreatedAt: now, LastActive: now, Status: storage.SessionStatusActive}); err != nil {
		t.Fatalf("CreateSession alice: %v", err)
	}
	if err := store.CreateSession(&storage.Session{ID: "s-bob", Principal: "bob", CreatedAt: now, LastActive: now, Status: storage.SessionStatusActive}); err != nil {
		t.Fatalf("CreateSession bob: %v", err)
	}

	planStore := orchestrator.NewFilePlanStore(filepath.Join(tmpDir, "plans"))
	if err := planStore.SavePlan(&orchestrator.Plan{ID: "plan-alice", FeatureName: "Alice", Description: "alice plan"}); err != nil {
		t.Fatalf("SavePlan alice: %v", err)
	}
	if err := planStore.SavePlan(&orchestrator.Plan{ID: "plan-bob", FeatureName: "Bob", Description: "bob plan"}); err != nil {
		t.Fatalf("SavePlan bob: %v", err)
	}
	if err := store.LinkSessionToPlan("s-alice", "plan-alice"); err != nil {
		t.Fatalf("LinkSessionToPlan alice: %v", err)
	}
	if err := store.LinkSessionToPlan("s-bob", "plan-bob"); err != nil {
		t.Fatalf("LinkSessionToPlan bob: %v", err)
	}

	server := NewServer(Config{}, store, nil, nil, planStore, config.DefaultConfig(), nil, nil)
	svc := NewGRPCService(server)

	ctx := context.WithValue(context.Background(), principalContextKey, &requestPrincipal{
		Name:  "alice",
		Scope: storage.TokenScopeViewer,
	})
	resp, err := svc.ListPlans(ctx, connect.NewRequest(&ipcpb.ListPlansRequest{}))
	if err != nil {
		t.Fatalf("ListPlans: %v", err)
	}
	if resp.Msg == nil {
		t.Fatalf("expected non-nil response")
	}
	if got := len(resp.Msg.Plans); got != 1 {
		t.Fatalf("len(plans)=%d want 1", got)
	}
	if resp.Msg.Plans[0].GetId() != "plan-alice" {
		t.Fatalf("plan[0].id=%q want %q", resp.Msg.Plans[0].GetId(), "plan-alice")
	}
}

func TestGRPCGetPlanHiddenWhenNotLinked(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "buckley.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now().UTC()
	if err := store.CreateSession(&storage.Session{ID: "s-alice", Principal: "alice", CreatedAt: now, LastActive: now, Status: storage.SessionStatusActive}); err != nil {
		t.Fatalf("CreateSession alice: %v", err)
	}
	if err := store.CreateSession(&storage.Session{ID: "s-bob", Principal: "bob", CreatedAt: now, LastActive: now, Status: storage.SessionStatusActive}); err != nil {
		t.Fatalf("CreateSession bob: %v", err)
	}

	planStore := orchestrator.NewFilePlanStore(filepath.Join(tmpDir, "plans"))
	if err := planStore.SavePlan(&orchestrator.Plan{ID: "plan-bob", FeatureName: "Bob", Description: "bob plan"}); err != nil {
		t.Fatalf("SavePlan bob: %v", err)
	}
	if err := store.LinkSessionToPlan("s-bob", "plan-bob"); err != nil {
		t.Fatalf("LinkSessionToPlan bob: %v", err)
	}

	server := NewServer(Config{}, store, nil, nil, planStore, config.DefaultConfig(), nil, nil)
	svc := NewGRPCService(server)

	ctx := context.WithValue(context.Background(), principalContextKey, &requestPrincipal{
		Name:  "alice",
		Scope: storage.TokenScopeViewer,
	})
	_, err = svc.GetPlan(ctx, connect.NewRequest(&ipcpb.GetPlanRequest{PlanId: "plan-bob"}))
	assertConnectCode(t, err, connect.CodeNotFound)
}
