package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/ipc/command"
	"m31labs.dev/buckley/pkg/mission"
	"m31labs.dev/buckley/pkg/sessionexec"
	"m31labs.dev/buckley/pkg/storage"
)

func TestMissionHandlersRecordActivityAndListAgents(t *testing.T) {
	cfg := config.DefaultConfig()
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	// Seed session and token for agent activity foreign key
	sessionID := "session-1"
	now := time.Now()
	if err := store.CreateSession(&storage.Session{
		ID:          sessionID,
		Principal:   "test",
		Status:      storage.SessionStatusActive,
		CreatedAt:   now,
		LastActive:  now,
		ProjectPath: ".",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := store.SaveSessionToken(sessionID, "token123"); err != nil {
		t.Fatalf("SaveSessionToken: %v", err)
	}
	if err := store.CreateSession(&storage.Session{
		ID:          "session-2",
		Principal:   "other",
		Status:      storage.SessionStatusActive,
		CreatedAt:   now,
		LastActive:  now,
		ProjectPath: ".",
	}); err != nil {
		t.Fatalf("CreateSession session-2: %v", err)
	}

	server := NewServer(Config{BindAddress: "127.0.0.1:0"}, store, nil, nil, nil, cfg, nil, nil)

	// Simulate sending a message to an agent (no session token required)
	body, _ := json.Marshal(mission.AgentMessageRequest{Message: "ping", SessionID: sessionID})
	req := httptest.NewRequest(http.MethodPost, "/api/mission/agents/agent-1/message", bytes.NewReader(body))
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("agentID", "agent-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	req = req.WithContext(context.WithValue(req.Context(), principalContextKey, &requestPrincipal{
		Name:  "test",
		Scope: storage.TokenScopeMember,
	}))
	req.Header.Set("X-Buckley-Session-Token", "token123")
	rr := httptest.NewRecorder()

	server.handleSendAgentMessage(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("handleSendAgentMessage status = %d, body=%s", rr.Code, rr.Body.String())
	}

	if err := server.missionStore.RecordAgentActivity(&mission.AgentActivity{
		AgentID:   "agent-2",
		SessionID: "session-2",
		Action:    "ping",
		Status:    "active",
		Timestamp: now,
	}); err != nil {
		t.Fatalf("RecordAgentActivity: %v", err)
	}

	// List agents should now surface agent-1 as active
	req2 := httptest.NewRequest(http.MethodGet, "/api/mission/agents?since=1h", nil)
	routeCtx2 := chi.NewRouteContext()
	req2 = req2.WithContext(context.WithValue(req2.Context(), chi.RouteCtxKey, routeCtx2))
	req2 = req2.WithContext(context.WithValue(req2.Context(), principalContextKey, &requestPrincipal{
		Name:  "test",
		Scope: storage.TokenScopeMember,
	}))
	rr2 := httptest.NewRecorder()
	server.handleListAgents(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("handleListAgents status = %d, body=%s", rr2.Code, rr2.Body.String())
	}
	if !bytes.Contains(rr2.Body.Bytes(), []byte("agent-1")) {
		t.Fatalf("expected agent-1 to be listed, got %s", rr2.Body.String())
	}
	if bytes.Contains(rr2.Body.Bytes(), []byte("agent-2")) {
		t.Fatalf("expected agent-2 to be filtered out, got %s", rr2.Body.String())
	}
}

func TestMissionMessage_AcceptedCommandSurvivesActivityFailure(t *testing.T) {
	cfg := config.DefaultConfig()
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessionID := "mission-accepted-session"
	now := time.Now().UTC()
	if err := store.CreateSession(&storage.Session{
		ID: sessionID, Principal: "test", Status: storage.SessionStatusActive,
		CreatedAt: now, LastActive: now, ProjectPath: ".",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSessionToken(sessionID, "token123"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`CREATE TRIGGER fail_mission_activity
		BEFORE INSERT ON agent_activity BEGIN SELECT RAISE(FAIL, 'forced activity failure'); END`); err != nil {
		t.Fatal(err)
	}

	gateway := command.NewGateway()
	var acceptedCommand command.SessionCommand
	gateway.Register(command.HandlerFunc(func(cmd command.SessionCommand) error {
		acceptedCommand = cmd
		_, err := store.Accept(context.Background(), sessionexec.AcceptRequest{
			SessionID: cmd.SessionID, CommandID: cmd.ID, Type: cmd.Type,
			Content: cmd.Content, AcceptedBy: cmd.AcceptedBy,
		})
		return err
	}))
	server := NewServer(Config{BindAddress: "127.0.0.1:0"}, store, nil, gateway, nil, cfg, nil, nil)
	var logs bytes.Buffer
	server.logger = log.New(&logs, "", 0)

	body, _ := json.Marshal(mission.AgentMessageRequest{Message: "accepted once", SessionID: sessionID})
	req := httptest.NewRequest(http.MethodPost, "/api/mission/agents/agent-1/message", bytes.NewReader(body))
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("agentID", "agent-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	req = req.WithContext(context.WithValue(req.Context(), principalContextKey, &requestPrincipal{
		Name: "test", Scope: storage.TokenScopeMember,
	}))
	req.Header.Set("X-Buckley-Session-Token", "token123")
	rr := httptest.NewRecorder()

	server.handleSendAgentMessage(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	summary, err := store.Summary(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Total != 1 || summary.Accepted != 1 {
		t.Fatalf("accepted command summary = %+v", summary)
	}
	if acceptedCommand.ID == "" || acceptedCommand.AcceptedBy != "test" || acceptedCommand.SessionID != sessionID {
		t.Fatalf("accepted mission command = %+v", acceptedCommand)
	}
	if !bytes.Contains(logs.Bytes(), []byte("activity after accepted command failed")) {
		t.Fatalf("activity failure was not observable: %q", logs.String())
	}
}
