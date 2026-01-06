package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/mission"
	"github.com/odvcencio/buckley/pkg/storage"
)

func TestMissionApproveChange(t *testing.T) {
	cfg := config.DefaultConfig()
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	sessionID := "sess-approve"
	now := time.Now()
	if err := store.CreateSession(&storage.Session{
		ID:          sessionID,
		Status:      storage.SessionStatusActive,
		CreatedAt:   now,
		LastActive:  now,
		ProjectPath: ".",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := store.SaveSessionToken(sessionID, "token"); err != nil {
		t.Fatalf("SaveSessionToken: %v", err)
	}

	server := NewServer(Config{}, store, nil, nil, nil, cfg, nil, nil)

	change := &mission.PendingChange{
		ID:        "change-1",
		AgentID:   "agent-x",
		SessionID: sessionID,
		FilePath:  "file.txt",
		Reason:    "test",
		Status:    "pending",
		CreatedAt: now,
	}
	if err := server.missionStore.CreatePendingChange(change); err != nil {
		t.Fatalf("CreatePendingChange: %v", err)
	}

	body, _ := json.Marshal(mission.DiffApprovalRequest{ReviewedBy: "tester"})
	req := httptest.NewRequest(http.MethodPost, "/api/mission/changes/change-1/approve", bytes.NewReader(body))
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("changeID", "change-1")
	req = req.WithContext(context.WithValue(req.Context(), principalContextKey, &requestPrincipal{
		Name:  "op",
		Scope: storage.TokenScopeOperator,
	}))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	req.Header.Set("X-Buckley-Session-Token", "token")

	rr := httptest.NewRecorder()
	server.handleApproveChange(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	updated, err := server.missionStore.GetPendingChange("change-1")
	if err != nil {
		t.Fatalf("GetPendingChange: %v", err)
	}
	if updated.Status != "approved" {
		t.Fatalf("expected status approved, got %s", updated.Status)
	}
}

func TestMissionRejectChange(t *testing.T) {
	cfg := config.DefaultConfig()
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	sessionID := "sess-reject"
	now := time.Now()
	if err := store.CreateSession(&storage.Session{
		ID:          sessionID,
		Status:      storage.SessionStatusActive,
		CreatedAt:   now,
		LastActive:  now,
		ProjectPath: ".",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := store.SaveSessionToken(sessionID, "token"); err != nil {
		t.Fatalf("SaveSessionToken: %v", err)
	}

	server := NewServer(Config{}, store, nil, nil, nil, cfg, nil, nil)

	change := &mission.PendingChange{
		ID:        "change-2",
		AgentID:   "agent-y",
		SessionID: sessionID,
		FilePath:  "file.txt",
		Reason:    "test",
		Status:    "pending",
		CreatedAt: now,
	}
	if err := server.missionStore.CreatePendingChange(change); err != nil {
		t.Fatalf("CreatePendingChange: %v", err)
	}

	body, _ := json.Marshal(mission.DiffApprovalRequest{ReviewedBy: "tester", Comment: "not ready"})
	req := httptest.NewRequest(http.MethodPost, "/api/mission/changes/change-2/reject", bytes.NewReader(body))
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("changeID", "change-2")
	req = req.WithContext(context.WithValue(req.Context(), principalContextKey, &requestPrincipal{
		Name:  "op",
		Scope: storage.TokenScopeOperator,
	}))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	req.Header.Set("X-Buckley-Session-Token", "token")

	rr := httptest.NewRecorder()
	server.handleRejectChange(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	updated, err := server.missionStore.GetPendingChange("change-2")
	if err != nil {
		t.Fatalf("GetPendingChange: %v", err)
	}
	if updated.Status != "rejected" {
		t.Fatalf("expected status rejected, got %s", updated.Status)
	}
}
