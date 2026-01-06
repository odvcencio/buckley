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
	"github.com/odvcencio/buckley/pkg/orchestrator"
	"github.com/odvcencio/buckley/pkg/storage"
)

func TestHandleListPlansFiltersByPrincipal(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "buckley.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now()
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

	server := NewServer(Config{ProjectRoot: tmpDir}, store, nil, command.NewGateway(), planStore, config.DefaultConfig(), nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/plans", nil)
	req = req.WithContext(context.WithValue(req.Context(), principalContextKey, &requestPrincipal{Name: "alice", Scope: storage.TokenScopeViewer}))
	rr := httptest.NewRecorder()
	server.handleListPlans(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Plans []struct {
			ID string `json:"id"`
		} `json:"plans"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Count != 1 || len(body.Plans) != 1 {
		t.Fatalf("expected 1 plan, got count=%d len=%d", body.Count, len(body.Plans))
	}
	if body.Plans[0].ID != "plan-alice" {
		t.Fatalf("plans[0].id=%q want %q", body.Plans[0].ID, "plan-alice")
	}
}

func TestHandleGetPlanHidesOtherPrincipalsPlans(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "buckley.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now()
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

	server := NewServer(Config{ProjectRoot: tmpDir}, store, nil, command.NewGateway(), planStore, config.DefaultConfig(), nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/plans/plan-bob", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("planID", "plan-bob")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	req = req.WithContext(context.WithValue(req.Context(), principalContextKey, &requestPrincipal{Name: "alice", Scope: storage.TokenScopeViewer}))
	rr := httptest.NewRecorder()
	server.handleGetPlan(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("unexpected status %d: %s", rr.Code, rr.Body.String())
	}
}
