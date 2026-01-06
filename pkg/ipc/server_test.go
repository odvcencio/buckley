package ipc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/ipc/command"
	"github.com/odvcencio/buckley/pkg/orchestrator"
	"github.com/odvcencio/buckley/pkg/storage"
)

func TestHandleSessionDetailReturnsPlanSnapshot(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "buckley.db")
	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	session := &storage.Session{
		ID:         "session-plan",
		Principal:  "test",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("CreateSession error: %v", err)
	}

	planStore := orchestrator.NewFilePlanStore(filepath.Join(tmpDir, "plans"))
	plan := &orchestrator.Plan{
		ID:          "plan-1",
		FeatureName: "Demo",
		Description: "demo plan",
		Tasks: []orchestrator.Task{
			{ID: "1", Title: "First", Description: "do it"},
		},
	}
	if err := planStore.SavePlan(plan); err != nil {
		t.Fatalf("SavePlan error: %v", err)
	}

	if err := store.LinkSessionToPlan(session.ID, plan.ID); err != nil {
		t.Fatalf("LinkSessionToPlan error: %v", err)
	}

	appCfg := &config.Config{}
	server := NewServer(Config{AllowedOrigins: []string{"*"}, ProjectRoot: tmpDir}, store, nil, command.NewGateway(), planStore, appCfg, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+session.ID, nil)
	ctx := chi.NewRouteContext()
	ctx.URLParams.Add("sessionID", session.ID)
	req = req.WithContext(context.WithValue(req.Context(), principalContextKey, &requestPrincipal{Name: "test", Scope: storage.TokenScopeViewer}))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, ctx))

	rr := httptest.NewRecorder()
	server.handleSessionDetail(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", rr.Code, rr.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("response json error: %v", err)
	}

	if body["plan"] == nil {
		t.Fatalf("expected plan snapshot in response")
	}
}

func TestValidateSessionTokenRequiresIssuedToken(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "buckley.db")
	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	server := NewServer(Config{ProjectRoot: tmpDir}, store, nil, command.NewGateway(), nil, &config.Config{}, nil, nil)
	if err := store.CreateSession(&storage.Session{ID: "abc123", CreatedAt: time.Now(), LastActive: time.Now(), Status: storage.SessionStatusActive}); err != nil {
		t.Fatalf("failed to seed session: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if server.validateSessionToken(req, "missing") {
		t.Fatalf("expected validation to fail when no token issued")
	}

	token, err := server.issueSessionToken("abc123")
	if err != nil {
		t.Fatalf("issueSessionToken error: %v", err)
	}

	reqWithToken := httptest.NewRequest(http.MethodGet, "/", nil)
	reqWithToken.Header.Set("X-Buckley-Session-Token", token)
	if !server.validateSessionToken(reqWithToken, "abc123") {
		t.Fatalf("expected validation to succeed with issued token")
	}

	reqWithQuery := httptest.NewRequest(http.MethodGet, "/?session_token="+token, nil)
	if !server.validateSessionToken(reqWithQuery, "abc123") {
		t.Fatalf("expected validation to succeed with session_token query parameter on loopback bind")
	}

	reqMissing := httptest.NewRequest(http.MethodGet, "/", nil)
	if server.validateSessionToken(reqMissing, "abc123") {
		t.Fatalf("expected validation to fail without header for issued token")
	}
}

func TestValidateSessionTokenQueryParamRejectedOnRemoteBind(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "buckley.db")
	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	server := NewServer(Config{BindAddress: "0.0.0.0:4488", ProjectRoot: tmpDir}, store, nil, command.NewGateway(), nil, &config.Config{}, nil, nil)
	if err := store.CreateSession(&storage.Session{ID: "abc123", CreatedAt: time.Now(), LastActive: time.Now(), Status: storage.SessionStatusActive}); err != nil {
		t.Fatalf("failed to seed session: %v", err)
	}
	token, err := server.issueSessionToken("abc123")
	if err != nil {
		t.Fatalf("issueSessionToken error: %v", err)
	}

	reqWithQuery := httptest.NewRequest(http.MethodGet, "/?session_token="+token, nil)
	if server.validateSessionToken(reqWithQuery, "abc123") {
		t.Fatalf("expected validation to reject session_token query parameter on remote bind")
	}
}

func TestHandleWorkflowActionDispatchesCommand(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "buckley.db")
	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	gateway := command.NewGateway()
	server := NewServer(Config{
		AllowedOrigins: []string{"*"},
		RequireToken:   true,
		AuthToken:      "unit-token",
		ProjectRoot:    tmpDir,
	}, store, nil, gateway, nil, &config.Config{}, nil, nil)
	server.commandLimiter = newRateLimiter(0)

	sessionID := "workflow-session"
	if err := store.CreateSession(&storage.Session{ID: sessionID, CreatedAt: time.Now(), LastActive: time.Now(), Status: storage.SessionStatusActive}); err != nil {
		t.Fatalf("failed to seed session: %v", err)
	}
	sessionToken, err := server.issueSessionToken(sessionID)
	if err != nil {
		t.Fatalf("issueSessionToken error: %v", err)
	}

	var dispatched command.SessionCommand
	gateway.Register(command.HandlerFunc(func(cmd command.SessionCommand) error {
		dispatched = cmd
		return nil
	}))

	body := strings.NewReader(`{"action":"plan","feature":"Cool Feature","description":"ship it"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/workflow/"+sessionID, body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Buckley-Session-Token", sessionToken)
	req.Header.Set("Authorization", "Bearer unit-token")
	ctx := chi.NewRouteContext()
	ctx.URLParams.Add("sessionID", sessionID)
	baseCtx := context.WithValue(req.Context(), chi.RouteCtxKey, ctx)
	baseCtx = context.WithValue(baseCtx, principalContextKey, &requestPrincipal{
		Name:  "unit",
		Scope: storage.TokenScopeOperator,
	})
	req = req.WithContext(baseCtx)

	rr := httptest.NewRecorder()
	server.handleWorkflowAction(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", rr.Code, rr.Body.String())
	}
	if dispatched.SessionID != sessionID {
		t.Fatalf("expected session %s, got %s", sessionID, dispatched.SessionID)
	}
	if dispatched.Type != "slash" {
		t.Fatalf("expected slash command, got %s", dispatched.Type)
	}
	if dispatched.Content != "/plan cool-feature ship it" {
		t.Fatalf("unexpected command content: %s", dispatched.Content)
	}
}

func TestHandleCreateProject(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "buckley.db")
	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	server := NewServer(Config{ProjectRoot: tmpDir}, store, nil, command.NewGateway(), nil, &config.Config{}, nil, nil)
	body := strings.NewReader(`{"name":"alpha"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/projects", body)
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), principalContextKey, &requestPrincipal{
		Name:  "test",
		Scope: storage.TokenScopeOperator,
	})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	server.handleCreateProject(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Project   projectSummary `json:"project"`
		SessionID string         `json:"sessionId"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Project.Slug == "" || resp.SessionID == "" {
		t.Fatalf("expected project slug and session id")
	}
	if _, err := os.Stat(filepath.Join(resp.Project.Path, ".git")); err != nil {
		t.Fatalf("expected git repo at %s: %v", resp.Project.Path, err)
	}
}

func TestBasicAuthMiddlewareAcceptsBearerToken(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "buckley.db")
	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	server := NewServer(Config{
		BasicAuthEnabled:  true,
		BasicAuthUsername: "user",
		BasicAuthPassword: "pass",
		AuthToken:         "unit-token",
		ProjectRoot:       tmpDir,
	}, store, nil, command.NewGateway(), nil, &config.Config{}, nil, nil)

	router := chi.NewRouter()
	router.Use(server.basicAuthMiddleware)
	router.Get("/protected", func(w http.ResponseWriter, r *http.Request) {
		principal := principalFromContext(r.Context())
		if principal == nil {
			http.Error(w, "missing principal", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(principal.Name))
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer unit-token")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected bearer status %d: %s", rr.Code, rr.Body.String())
	}
	if rr.Body.String() != "builtin" {
		t.Fatalf("unexpected bearer principal %q", rr.Body.String())
	}
	if hdr := rr.Header().Get("WWW-Authenticate"); hdr != "" {
		t.Fatalf("unexpected bearer WWW-Authenticate header: %q", hdr)
	}

	reqBasic := httptest.NewRequest(http.MethodGet, "/protected", nil)
	reqBasic.SetBasicAuth("user", "pass")
	rrBasic := httptest.NewRecorder()
	router.ServeHTTP(rrBasic, reqBasic)
	if rrBasic.Code != http.StatusOK {
		t.Fatalf("unexpected basic status %d: %s", rrBasic.Code, rrBasic.Body.String())
	}
	if rrBasic.Body.String() != "user" {
		t.Fatalf("unexpected basic principal %q", rrBasic.Body.String())
	}

	reqMissing := httptest.NewRequest(http.MethodGet, "/protected", nil)
	rrMissing := httptest.NewRecorder()
	router.ServeHTTP(rrMissing, reqMissing)
	if rrMissing.Code != http.StatusUnauthorized {
		t.Fatalf("unexpected missing auth status %d", rrMissing.Code)
	}
	if hdr := rrMissing.Header().Get("WWW-Authenticate"); hdr == "" {
		t.Fatalf("expected missing auth WWW-Authenticate header")
	}

	reqInvalidBearer := httptest.NewRequest(http.MethodGet, "/protected", nil)
	reqInvalidBearer.Header.Set("Authorization", "Bearer wrong-token")
	rrInvalidBearer := httptest.NewRecorder()
	router.ServeHTTP(rrInvalidBearer, reqInvalidBearer)
	if rrInvalidBearer.Code != http.StatusUnauthorized {
		t.Fatalf("unexpected invalid bearer status %d", rrInvalidBearer.Code)
	}
	if hdr := rrInvalidBearer.Header().Get("WWW-Authenticate"); hdr != "" {
		t.Fatalf("unexpected invalid bearer WWW-Authenticate header: %q", hdr)
	}
}

func TestCLITicketLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "buckley.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := NewServer(Config{ProjectRoot: tmpDir}, store, nil, command.NewGateway(), orchestrator.NewFilePlanStore(filepath.Join(tmpDir, "plans")), &config.Config{}, nil, nil)

	createReq := httptest.NewRequest(http.MethodPost, "/api/cli/tickets", strings.NewReader(`{"label":"laptop"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	server.handleCreateCliTicket(createRec, createReq)
	if createRec.Code != http.StatusOK {
		t.Fatalf("unexpected create status %d: %s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		Ticket string `json:"ticket"`
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.Ticket == "" || created.Secret == "" {
		t.Fatalf("expected ticket and secret")
	}

	approveReq := httptest.NewRequest(http.MethodPost, "/api/cli/tickets/"+created.Ticket+"/approve", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("ticket", created.Ticket)
	ctx := context.WithValue(approveReq.Context(), chi.RouteCtxKey, routeCtx)
	ctx = context.WithValue(ctx, principalContextKey, &requestPrincipal{Name: "alice", Scope: storage.TokenScopeOperator})
	approveReq = approveReq.WithContext(ctx)
	approveRec := httptest.NewRecorder()
	server.handleApproveCliTicket(approveRec, approveReq)
	if approveRec.Code != http.StatusOK {
		t.Fatalf("unexpected approve status %d: %s", approveRec.Code, approveRec.Body.String())
	}

	pollReq := httptest.NewRequest(http.MethodGet, "/api/cli/tickets/"+created.Ticket, nil)
	pollReq.Header.Set("X-Buckley-CLI-Ticket-Secret", created.Secret)
	pollCtx := chi.NewRouteContext()
	pollCtx.URLParams.Add("ticket", created.Ticket)
	pollReq = pollReq.WithContext(context.WithValue(pollReq.Context(), chi.RouteCtxKey, pollCtx))
	pollRec := httptest.NewRecorder()
	server.handleGetCliTicket(pollRec, pollReq)
	if pollRec.Code != http.StatusOK {
		t.Fatalf("unexpected poll status %d: %s", pollRec.Code, pollRec.Body.String())
	}
	if cookie := pollRec.Header().Get("Set-Cookie"); cookie == "" {
		t.Fatalf("expected session cookie in response")
	}
	var pollBody map[string]any
	if err := json.Unmarshal(pollRec.Body.Bytes(), &pollBody); err != nil {
		t.Fatalf("decode poll response: %v", err)
	}
	if pollBody["status"] != "approved" {
		t.Fatalf("expected approved status, got %v", pollBody["status"])
	}
}

func TestHandleGeneratePersonaAsset(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "buckley.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := NewServer(Config{ProjectRoot: tmpDir}, store, nil, command.NewGateway(), orchestrator.NewFilePlanStore(filepath.Join(tmpDir, "plans")), &config.Config{}, nil, nil)

	body := strings.NewReader(`{"kind":"persona","name":"Nitpicky Reviewer"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/generate", body)
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), principalContextKey, &requestPrincipal{
		Name:  "tester",
		Scope: storage.TokenScopeOperator,
	})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	server.handleGenerateAsset(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", rr.Code, rr.Body.String())
	}
	personaPath := filepath.Join(tmpDir, ".buckley", "personas", "nitpicky-reviewer.yaml")
	if _, err := os.Stat(personaPath); err != nil {
		t.Fatalf("expected persona file at %s: %v", personaPath, err)
	}
}

func TestHandleGenerateAgentsAsset(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "buckley.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := &config.Config{}
	server := NewServer(Config{ProjectRoot: tmpDir}, store, nil, command.NewGateway(), orchestrator.NewFilePlanStore(filepath.Join(tmpDir, "plans")), cfg, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/generate", strings.NewReader(`{"kind":"agents"}`))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), principalContextKey, &requestPrincipal{
		Name:  "tester",
		Scope: storage.TokenScopeOperator,
	})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	server.handleGenerateAsset(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", rr.Code, rr.Body.String())
	}
	expected := filepath.Join(tmpDir, "AGENTS.md")
	if _, err := os.Stat(expected); err != nil {
		t.Fatalf("expected AGENTS.md at %s: %v", expected, err)
	}
}
