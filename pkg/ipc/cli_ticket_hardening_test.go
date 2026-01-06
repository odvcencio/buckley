package ipc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestHandleCreateCliTicketRequiresBearerWhenRequireToken(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "buckley.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	server := NewServer(Config{
		ProjectRoot:  tmpDir,
		RequireToken: true,
		AuthToken:    "unit-token",
	}, store, nil, command.NewGateway(), orchestrator.NewFilePlanStore(filepath.Join(tmpDir, "plans")), &config.Config{}, nil, nil)

	reqMissing := httptest.NewRequest(http.MethodPost, "/api/cli/tickets", strings.NewReader(`{"label":"laptop"}`))
	reqMissing.Header.Set("Content-Type", "application/json")
	rrMissing := httptest.NewRecorder()
	server.handleCreateCliTicket(rrMissing, reqMissing)
	if rrMissing.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized, got %d: %s", rrMissing.Code, rrMissing.Body.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/api/cli/tickets", strings.NewReader(`{"label":"laptop"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer unit-token")
	rr := httptest.NewRecorder()
	server.handleCreateCliTicket(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected ok, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestHandleMetricsRequiresBearerWhenRequireTokenAndNotPublic(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "buckley.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	server := NewServer(Config{
		ProjectRoot:  tmpDir,
		RequireToken: true,
		AuthToken:    "unit-token",
	}, store, nil, command.NewGateway(), orchestrator.NewFilePlanStore(filepath.Join(tmpDir, "plans")), &config.Config{}, nil, nil)

	rrMissing := httptest.NewRecorder()
	server.handleMetrics(rrMissing, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rrMissing.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized, got %d: %s", rrMissing.Code, rrMissing.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer unit-token")
	rr := httptest.NewRecorder()
	server.handleMetrics(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected ok, got %d: %s", rr.Code, rr.Body.String())
	}
	if body := rr.Body.String(); !strings.Contains(body, "buckley_sessions_active_total") {
		t.Fatalf("expected metrics body, got %q", body)
	}
}

func TestHandleMetricsIsUnauthenticatedWhenPublicMetrics(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "buckley.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	server := NewServer(Config{
		ProjectRoot:    tmpDir,
		RequireToken:   true,
		PublicMetrics:  true,
		AuthToken:      "unit-token",
		AllowedOrigins: []string{"*"},
	}, store, nil, command.NewGateway(), orchestrator.NewFilePlanStore(filepath.Join(tmpDir, "plans")), &config.Config{}, nil, nil)

	rrMissing := httptest.NewRecorder()
	server.handleMetrics(rrMissing, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rrMissing.Code != http.StatusOK {
		t.Fatalf("expected ok, got %d: %s", rrMissing.Code, rrMissing.Body.String())
	}
	if body := rrMissing.Body.String(); !strings.Contains(body, "buckley_sessions_active_total") {
		t.Fatalf("expected metrics body, got %q", body)
	}
}

func TestCLITicketPollRateLimited(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "buckley.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	server := NewServer(Config{ProjectRoot: tmpDir}, store, nil, command.NewGateway(), orchestrator.NewFilePlanStore(filepath.Join(tmpDir, "plans")), &config.Config{}, nil, nil)
	server.cliTicketLimiter = newRateLimiter(1 * time.Minute)

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

	poll := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/cli/tickets/"+created.Ticket, nil)
		req.Header.Set("X-Buckley-CLI-Ticket-Secret", created.Secret)
		pollCtx := chi.NewRouteContext()
		pollCtx.URLParams.Add("ticket", created.Ticket)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, pollCtx))
		rec := httptest.NewRecorder()
		server.handleGetCliTicket(rec, req)
		return rec
	}

	first := poll()
	if first.Code != http.StatusOK {
		t.Fatalf("unexpected first poll status %d: %s", first.Code, first.Body.String())
	}
	second := poll()
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("expected rate limited poll, got %d: %s", second.Code, second.Body.String())
	}
}

func TestHandleGetCliTicketRejectsQuerySecretOnRemoteBind(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "buckley.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	server := NewServer(Config{
		BindAddress:  "0.0.0.0:4488",
		ProjectRoot:  tmpDir,
		RequireToken: true,
		AuthToken:    "unit-token",
	}, store, nil, command.NewGateway(), orchestrator.NewFilePlanStore(filepath.Join(tmpDir, "plans")), &config.Config{}, nil, nil)

	createReq := httptest.NewRequest(http.MethodPost, "/api/cli/tickets", strings.NewReader(`{"label":"laptop"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Authorization", "Bearer unit-token")
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

	pollQueryReq := httptest.NewRequest(http.MethodGet, "/api/cli/tickets/"+created.Ticket+"?secret="+created.Secret, nil)
	pollQueryCtx := chi.NewRouteContext()
	pollQueryCtx.URLParams.Add("ticket", created.Ticket)
	pollQueryReq = pollQueryReq.WithContext(context.WithValue(pollQueryReq.Context(), chi.RouteCtxKey, pollQueryCtx))
	pollQueryRec := httptest.NewRecorder()
	server.handleGetCliTicket(pollQueryRec, pollQueryReq)
	if pollQueryRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected query secret rejected on remote bind, got %d: %s", pollQueryRec.Code, pollQueryRec.Body.String())
	}

	pollHeaderReq := httptest.NewRequest(http.MethodGet, "/api/cli/tickets/"+created.Ticket, nil)
	pollHeaderReq.Header.Set("X-Buckley-CLI-Ticket-Secret", created.Secret)
	pollHeaderCtx := chi.NewRouteContext()
	pollHeaderCtx.URLParams.Add("ticket", created.Ticket)
	pollHeaderReq = pollHeaderReq.WithContext(context.WithValue(pollHeaderReq.Context(), chi.RouteCtxKey, pollHeaderCtx))
	pollHeaderRec := httptest.NewRecorder()
	server.handleGetCliTicket(pollHeaderRec, pollHeaderReq)
	if pollHeaderRec.Code != http.StatusOK {
		t.Fatalf("expected header secret accepted on remote bind, got %d: %s", pollHeaderRec.Code, pollHeaderRec.Body.String())
	}
}
