package ipc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/ipc/command"
	"github.com/odvcencio/buckley/pkg/storage"
)

func TestListProjectsFiltersByPrincipal(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "buckley.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now()
	aliceRepo := filepath.Join(tmpDir, "repo-alice")
	bobRepo := filepath.Join(tmpDir, "repo-bob")
	if err := store.CreateSession(&storage.Session{ID: "s-alice", Principal: "alice", GitRepo: aliceRepo, CreatedAt: now, LastActive: now, Status: storage.SessionStatusActive}); err != nil {
		t.Fatalf("CreateSession alice: %v", err)
	}
	if err := store.CreateSession(&storage.Session{ID: "s-bob", Principal: "bob", GitRepo: bobRepo, CreatedAt: now, LastActive: now, Status: storage.SessionStatusActive}); err != nil {
		t.Fatalf("CreateSession bob: %v", err)
	}

	server := NewServer(Config{ProjectRoot: tmpDir}, store, nil, command.NewGateway(), nil, config.DefaultConfig(), nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	req = req.WithContext(context.WithValue(req.Context(), principalContextKey, &requestPrincipal{Name: "alice", Scope: storage.TokenScopeViewer}))
	rr := httptest.NewRecorder()
	server.handleListProjects(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", rr.Code, rr.Body.String())
	}

	var body struct {
		Projects []projectSummary `json:"projects"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(body.Projects))
	}
	if body.Projects[0].Path != aliceRepo {
		t.Fatalf("project[0].Path=%q want %q", body.Projects[0].Path, aliceRepo)
	}
}

func TestProjectSessionsHidesOtherPrincipalsProjects(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "buckley.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now()
	aliceRepo := filepath.Join(tmpDir, "repo-alice")
	bobRepo := filepath.Join(tmpDir, "repo-bob")
	if err := store.CreateSession(&storage.Session{ID: "s-alice", Principal: "alice", GitRepo: aliceRepo, CreatedAt: now, LastActive: now, Status: storage.SessionStatusActive}); err != nil {
		t.Fatalf("CreateSession alice: %v", err)
	}
	if err := store.CreateSession(&storage.Session{ID: "s-bob", Principal: "bob", GitRepo: bobRepo, CreatedAt: now, LastActive: now, Status: storage.SessionStatusActive}); err != nil {
		t.Fatalf("CreateSession bob: %v", err)
	}

	server := NewServer(Config{ProjectRoot: tmpDir}, store, nil, command.NewGateway(), nil, config.DefaultConfig(), nil, nil)

	bobSlug := slugifyProjectName(filepath.Base(bobRepo))
	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+bobSlug+"/sessions", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("project", bobSlug)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	req = req.WithContext(context.WithValue(req.Context(), principalContextKey, &requestPrincipal{Name: "alice", Scope: storage.TokenScopeViewer}))
	rr := httptest.NewRecorder()
	server.handleProjectSessions(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("unexpected status %d: %s", rr.Code, rr.Body.String())
	}
}
