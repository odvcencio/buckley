package ipc

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/ipc/command"
	"github.com/odvcencio/buckley/pkg/storage"
)

// Smoke test for /workflow/{sessionID} endpoint to ensure it handles basic actions.
func TestWorkflowActionStart(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.IPC.EnableBrowser = true // enable IPC commands
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	session := &storage.Session{
		ID:          "workflow-session",
		Principal:   "member",
		Status:      storage.SessionStatusActive,
		ProjectPath: ".",
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := store.SaveSessionToken(session.ID, "token"); err != nil {
		t.Fatalf("SaveSessionToken: %v", err)
	}

	gateway := command.NewGateway()
	gateway.Register(command.HandlerFunc(func(cmd command.SessionCommand) error {
		if cmd.SessionID == "" || cmd.Content == "" {
			t.Fatalf("command missing data: %+v", cmd)
		}
		return nil
	}))

	server := NewServer(Config{EnableBrowser: true}, store, nil, gateway, nil, cfg, nil, nil)

	body := bytes.NewBufferString(`{"action":"pause","note":"test pause"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/workflow/workflow-session", body)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("sessionID", "workflow-session")
	req = req.WithContext(context.WithValue(req.Context(), principalContextKey, &requestPrincipal{
		Name:  "member",
		Scope: storage.TokenScopeMember,
	}))
	req.Header.Set("X-Buckley-Session-Token", "token")
	q := req.URL.Query()
	q.Set("session_token", "token")
	req.URL.RawQuery = q.Encode()
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))

	if ok, err := store.ValidateSessionToken(session.ID, "token"); !ok || err != nil {
		var count int
		_ = store.DB().QueryRow(`SELECT COUNT(*) FROM session_tokens`).Scan(&count)
		t.Fatalf("ValidateSessionToken failed: ok=%v err=%v rows=%d", ok, err, count)
	}
	if !server.validateSessionToken(req, session.ID) {
		t.Fatalf("expected session token to validate via server")
	}

	rr := httptest.NewRecorder()
	server.handleWorkflowAction(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status code: %d body=%s", rr.Code, rr.Body.String())
	}
}
