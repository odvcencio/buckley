package ipc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/storage"
)

func TestSessionDetailIncludesSummary(t *testing.T) {
	cfg := config.DefaultConfig()
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	session := &storage.Session{
		ID:          "sess-1",
		Principal:   "test",
		Status:      storage.SessionStatusActive,
		CreatedAt:   time.Now(),
		LastActive:  time.Now(),
		ProjectPath: ".",
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := store.SaveSessionSummary(session.ID, "hello summary"); err != nil {
		t.Fatalf("SaveSessionSummary: %v", err)
	}

	server := NewServer(Config{}, store, nil, nil, nil, cfg, nil, nil)

	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("sessionID", session.ID)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+session.ID, nil)
	req = req.WithContext(context.WithValue(req.Context(), principalContextKey, &requestPrincipal{Name: "test", Scope: storage.TokenScopeMember}))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	rr := httptest.NewRecorder()

	server.handleSessionDetail(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "hello summary") {
		t.Fatalf("expected summary in response, got %s", rr.Body.String())
	}
}
