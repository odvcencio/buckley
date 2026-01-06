package ipc

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/storage"
)

func TestGRPCListProjectsFiltersByPrincipal(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now().UTC()
	if err := store.CreateSession(&storage.Session{ID: "s-alice", Principal: "alice", GitRepo: "/tmp/repo-alice", CreatedAt: now, LastActive: now, Status: storage.SessionStatusActive}); err != nil {
		t.Fatalf("CreateSession alice: %v", err)
	}
	if err := store.CreateSession(&storage.Session{ID: "s-bob", Principal: "bob", GitRepo: "/tmp/repo-bob", CreatedAt: now, LastActive: now, Status: storage.SessionStatusActive}); err != nil {
		t.Fatalf("CreateSession bob: %v", err)
	}

	server := NewServer(Config{}, store, nil, nil, nil, config.DefaultConfig(), nil, nil)
	svc := NewGRPCService(server)

	ctx := context.WithValue(context.Background(), principalContextKey, &requestPrincipal{
		Name:  "alice",
		Scope: storage.TokenScopeViewer,
	})
	resp, err := svc.ListProjects(ctx, connect.NewRequest(&emptypb.Empty{}))
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if resp.Msg == nil {
		t.Fatalf("expected non-nil response")
	}
	if got := len(resp.Msg.Projects); got != 1 {
		t.Fatalf("len(projects)=%d want 1", got)
	}
	if resp.Msg.Projects[0].GetPath() != "/tmp/repo-alice" {
		t.Fatalf("project[0].path=%q want %q", resp.Msg.Projects[0].GetPath(), "/tmp/repo-alice")
	}
}
