package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/headless"
	"github.com/odvcencio/buckley/pkg/ipc/command"
	"github.com/odvcencio/buckley/pkg/orchestrator"
	"github.com/odvcencio/buckley/pkg/storage"
)

type fakeHeadlessRegistry struct {
	mu sync.Mutex

	createReq      headless.CreateSessionRequest
	createdSession *headless.SessionInfo

	sessions map[string]*headless.SessionInfo

	lastCommand command.SessionCommand
	adopted     *storage.Session
}

type captureEventForwarder struct {
	events []Event
}

func (f *captureEventForwarder) BroadcastEvent(event Event) {
	f.events = append(f.events, event)
}

func newFakeHeadlessRegistry() *fakeHeadlessRegistry {
	return &fakeHeadlessRegistry{
		sessions: make(map[string]*headless.SessionInfo),
	}
}

func (f *fakeHeadlessRegistry) CreateSession(req headless.CreateSessionRequest) (*headless.SessionInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createReq = req
	if f.createdSession == nil {
		now := time.Now().UTC()
		f.createdSession = &headless.SessionInfo{
			ID:         "headless-1",
			Project:    req.Project,
			Branch:     req.Branch,
			Model:      req.Model,
			State:      headless.StateIdle,
			CreatedAt:  now,
			LastActive: now,
		}
	}
	copy := *f.createdSession
	f.sessions[copy.ID] = &copy
	return f.createdSession, nil
}

func (f *fakeHeadlessRegistry) GetSession(_ string) (*headless.Runner, bool) {
	return nil, false
}

func (f *fakeHeadlessRegistry) GetSessionInfo(sessionID string) (*headless.SessionInfo, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	info, ok := f.sessions[sessionID]
	if !ok || info == nil {
		return nil, false
	}
	copy := *info
	return &copy, true
}

func (f *fakeHeadlessRegistry) ListSessions() []headless.SessionInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]headless.SessionInfo, 0, len(f.sessions))
	for _, info := range f.sessions {
		if info == nil {
			continue
		}
		out = append(out, *info)
	}
	return out
}

func (f *fakeHeadlessRegistry) RemoveSession(sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.sessions[sessionID]; !ok {
		return fmt.Errorf("session not found")
	}
	delete(f.sessions, sessionID)
	return nil
}

func (f *fakeHeadlessRegistry) DispatchCommand(cmd command.SessionCommand) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastCommand = cmd
	return nil
}

func (f *fakeHeadlessRegistry) AdoptSession(sessionID string) (*storage.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.adopted != nil {
		return f.adopted, nil
	}
	return &storage.Session{ID: sessionID}, nil
}

func (f *fakeHeadlessRegistry) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sessions)
}

func newHeadlessTestServer(t *testing.T) (*Server, *storage.Store, string) {
	t.Helper()

	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "buckley.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	planStore := orchestrator.NewFilePlanStore(filepath.Join(tmpDir, "plans"))
	server := NewServer(
		Config{ProjectRoot: tmpDir, AllowedOrigins: []string{"*"}},
		store,
		nil,
		command.NewGateway(),
		planStore,
		&config.Config{},
		nil,
		nil,
	)
	return server, store, tmpDir
}

func withScope(req *http.Request, scope string) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), principalContextKey, &requestPrincipal{
		Name:  "test",
		Scope: scope,
	}))
}

func TestHeadlessEmitterBroadcastsToHubForwarder(t *testing.T) {
	server, _, _ := newHeadlessTestServer(t)

	fwd := &captureEventForwarder{}
	server.hub.AddForwarder(fwd)

	emitter := server.NewHeadlessEmitter()
	emitter.Emit(headless.RunnerEvent{
		Type:      "runner.test",
		SessionID: "s1",
		Timestamp: time.Now().UTC(),
		Data:      map[string]any{"ok": true},
	})

	if len(fwd.events) != 1 {
		t.Fatalf("expected 1 forwarded event, got %d", len(fwd.events))
	}
	if fwd.events[0].Type != "runner.test" {
		t.Fatalf("event type=%q want runner.test", fwd.events[0].Type)
	}
	if fwd.events[0].SessionID != "s1" {
		t.Fatalf("event session=%q want s1", fwd.events[0].SessionID)
	}
}

func TestHeadlessEmitterNilHubDoesNothing(t *testing.T) {
	var e headlessEmitter
	e.Emit(headless.RunnerEvent{
		Type:      "noop",
		SessionID: "s1",
		Timestamp: time.Now().UTC(),
	})
}

func TestCreateHeadlessSessionDefaultsProjectRoot(t *testing.T) {
	server, _, root := newHeadlessTestServer(t)
	registry := newFakeHeadlessRegistry()
	server.SetHeadlessRegistry(registry)

	r := chi.NewRouter()
	server.setupHeadlessRoutes(r)

	body := strings.NewReader(`{"project":"","model":"test-model"}`)
	req := httptest.NewRequest(http.MethodPost, "/headless/sessions", body)
	req.Header.Set("Content-Type", "application/json")
	req = withScope(req, storage.TokenScopeMember)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d want %d body=%s", rr.Code, http.StatusCreated, rr.Body.String())
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.createReq.Project != root {
		t.Fatalf("create project=%q want %q", registry.createReq.Project, root)
	}

	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json: %v", err)
	}
	if payload["session"] == nil {
		t.Fatalf("expected session in response")
	}
	if payload["stream"] != "/buckley.ipc.v1.BuckleyIPC/Subscribe" {
		t.Fatalf("stream=%v want %q", payload["stream"], "/buckley.ipc.v1.BuckleyIPC/Subscribe")
	}
}

func TestCreateHeadlessSessionAcceptsGitURLProjects(t *testing.T) {
	server, _, _ := newHeadlessTestServer(t)
	registry := newFakeHeadlessRegistry()
	server.SetHeadlessRegistry(registry)

	r := chi.NewRouter()
	server.setupHeadlessRoutes(r)

	body := strings.NewReader(`{"project":"https://example.com/acme/repo.git","model":"test-model"}`)
	req := httptest.NewRequest(http.MethodPost, "/headless/sessions", body)
	req.Header.Set("Content-Type", "application/json")
	req = withScope(req, storage.TokenScopeMember)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d want %d body=%s", rr.Code, http.StatusCreated, rr.Body.String())
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.createReq.Project != "https://example.com/acme/repo.git" {
		t.Fatalf("create project=%q want %q", registry.createReq.Project, "https://example.com/acme/repo.git")
	}
}

func TestListHeadlessSessionsReturnsCount(t *testing.T) {
	server, store, _ := newHeadlessTestServer(t)
	registry := newFakeHeadlessRegistry()
	registry.sessions["s1"] = &headless.SessionInfo{ID: "s1", State: headless.StateIdle, CreatedAt: time.Now().UTC(), LastActive: time.Now().UTC()}
	registry.sessions["s2"] = &headless.SessionInfo{ID: "s2", State: headless.StateIdle, CreatedAt: time.Now().UTC(), LastActive: time.Now().UTC()}
	server.SetHeadlessRegistry(registry)

	now := time.Now().UTC()
	if err := store.CreateSession(&storage.Session{ID: "s1", Principal: "test", CreatedAt: now, LastActive: now, Status: storage.SessionStatusActive}); err != nil {
		t.Fatalf("CreateSession s1: %v", err)
	}
	if err := store.CreateSession(&storage.Session{ID: "s2", Principal: "test", CreatedAt: now, LastActive: now, Status: storage.SessionStatusActive}); err != nil {
		t.Fatalf("CreateSession s2: %v", err)
	}

	r := chi.NewRouter()
	server.setupHeadlessRoutes(r)

	req := httptest.NewRequest(http.MethodGet, "/headless/sessions", nil)
	req = withScope(req, storage.TokenScopeViewer)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json: %v", err)
	}
	if got, ok := payload["count"].(float64); !ok || got != 2 {
		t.Fatalf("count=%v want 2", payload["count"])
	}
}

func TestGetHeadlessSessionIncludesMessagesAndTodos(t *testing.T) {
	server, store, _ := newHeadlessTestServer(t)
	registry := newFakeHeadlessRegistry()
	now := time.Now().UTC()
	registry.sessions["s1"] = &headless.SessionInfo{ID: "s1", Project: "/tmp", State: headless.StateIdle, CreatedAt: now, LastActive: now}
	server.SetHeadlessRegistry(registry)

	if err := store.CreateSession(&storage.Session{ID: "s1", Principal: "test", CreatedAt: now, LastActive: now, Status: storage.SessionStatusActive}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := store.SaveMessage(&storage.Message{SessionID: "s1", Role: "user", Content: "hello", Timestamp: now, Tokens: 1}); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}
	if err := store.CreateTodo(&storage.Todo{
		SessionID:  "s1",
		Content:    "ship it",
		ActiveForm: "shipping it",
		Status:     "pending",
		OrderIndex: 0,
		CreatedAt:  now,
		UpdatedAt:  now,
	}); err != nil {
		t.Fatalf("CreateTodo: %v", err)
	}

	r := chi.NewRouter()
	server.setupHeadlessRoutes(r)

	req := httptest.NewRequest(http.MethodGet, "/headless/sessions/s1", nil)
	req = withScope(req, storage.TokenScopeViewer)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var payload struct {
		Session  headless.SessionInfo `json:"session"`
		Messages []storage.Message    `json:"messages"`
		Todos    []storage.Todo       `json:"todos"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json: %v", err)
	}
	if payload.Session.ID != "s1" {
		t.Fatalf("session id=%q want s1", payload.Session.ID)
	}
	if len(payload.Messages) != 1 {
		t.Fatalf("messages=%d want 1", len(payload.Messages))
	}
	if len(payload.Todos) != 1 {
		t.Fatalf("todos=%d want 1", len(payload.Todos))
	}
}

func TestHeadlessCommandDefaultsToInput(t *testing.T) {
	server, store, _ := newHeadlessTestServer(t)
	registry := newFakeHeadlessRegistry()
	server.SetHeadlessRegistry(registry)

	now := time.Now().UTC()
	if err := store.CreateSession(&storage.Session{ID: "s1", Principal: "test", CreatedAt: now, LastActive: now, Status: storage.SessionStatusActive}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := store.SaveSessionToken("s1", "session-token"); err != nil {
		t.Fatalf("SaveSessionToken: %v", err)
	}

	r := chi.NewRouter()
	server.setupHeadlessRoutes(r)

	body := strings.NewReader(`{"content":"ls -la"}`)
	req := httptest.NewRequest(http.MethodPost, "/headless/sessions/s1/commands", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Buckley-Session-Token", "session-token")
	req = withScope(req, storage.TokenScopeMember)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status=%d want %d body=%s", rr.Code, http.StatusAccepted, rr.Body.String())
	}
	registry.mu.Lock()
	cmd := registry.lastCommand
	registry.mu.Unlock()

	if cmd.SessionID != "s1" {
		t.Fatalf("command session=%q want s1", cmd.SessionID)
	}
	if cmd.Type != "input" {
		t.Fatalf("command type=%q want input", cmd.Type)
	}
	if cmd.Content != "ls -la" {
		t.Fatalf("command content=%q want ls -la", cmd.Content)
	}
}

func TestDeleteHeadlessSessionNoContent(t *testing.T) {
	server, store, _ := newHeadlessTestServer(t)
	registry := newFakeHeadlessRegistry()
	registry.sessions["s1"] = &headless.SessionInfo{ID: "s1", State: headless.StateIdle, CreatedAt: time.Now().UTC(), LastActive: time.Now().UTC()}
	server.SetHeadlessRegistry(registry)

	now := time.Now().UTC()
	if err := store.CreateSession(&storage.Session{ID: "s1", Principal: "test", CreatedAt: now, LastActive: now, Status: storage.SessionStatusActive}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := store.SaveSessionToken("s1", "session-token"); err != nil {
		t.Fatalf("SaveSessionToken: %v", err)
	}

	r := chi.NewRouter()
	server.setupHeadlessRoutes(r)

	req := httptest.NewRequest(http.MethodDelete, "/headless/sessions/s1", nil)
	req.Header.Set("X-Buckley-Session-Token", "session-token")
	req = withScope(req, storage.TokenScopeMember)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status=%d want %d body=%s", rr.Code, http.StatusNoContent, rr.Body.String())
	}
}

func TestAdoptHeadlessSessionReturnsSession(t *testing.T) {
	server, store, _ := newHeadlessTestServer(t)
	registry := newFakeHeadlessRegistry()
	registry.adopted = &storage.Session{ID: "s1"}
	server.SetHeadlessRegistry(registry)

	now := time.Now().UTC()
	if err := store.CreateSession(&storage.Session{ID: "s1", Principal: "test", CreatedAt: now, LastActive: now, Status: storage.SessionStatusActive}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := store.SaveSessionToken("s1", "session-token"); err != nil {
		t.Fatalf("SaveSessionToken: %v", err)
	}

	r := chi.NewRouter()
	server.setupHeadlessRoutes(r)

	req := httptest.NewRequest(http.MethodPost, "/headless/sessions/s1/adopt", nil)
	req.Header.Set("X-Buckley-Session-Token", "session-token")
	req = withScope(req, storage.TokenScopeMember)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var payload struct {
		Session storage.Session `json:"session"`
		Message string          `json:"message"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json: %v", err)
	}
	if payload.Session.ID != "s1" {
		t.Fatalf("session id=%q want s1", payload.Session.ID)
	}
	if payload.Message == "" {
		t.Fatalf("expected adoption message")
	}
}

func TestInitHeadlessRegistryReturnsNilWithoutModels(t *testing.T) {
	server, _, _ := newHeadlessTestServer(t)
	if got := server.InitHeadlessRegistry(context.Background()); got != nil {
		t.Fatalf("expected nil registry without model manager")
	}
}
