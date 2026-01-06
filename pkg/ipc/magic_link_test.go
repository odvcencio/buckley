package ipc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestMagicLinkCannotEscalateScope(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "buckley.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	server := NewServer(Config{ProjectRoot: tmpDir}, store, nil, command.NewGateway(), orchestrator.NewFilePlanStore(filepath.Join(tmpDir, "plans")), config.DefaultConfig(), nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/magic-link", strings.NewReader(`{"scope":"operator"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), principalContextKey, &requestPrincipal{
		Name:  "alice",
		Scope: storage.TokenScopeMember,
	}))
	rr := httptest.NewRecorder()
	server.handleCreateMagicLink(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("unexpected status %d: %s", rr.Code, rr.Body.String())
	}
}

func TestMagicLinkRedeemCreatesSessionWithBoundScope(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "buckley.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	server := NewServer(Config{ProjectRoot: tmpDir}, store, nil, command.NewGateway(), orchestrator.NewFilePlanStore(filepath.Join(tmpDir, "plans")), config.DefaultConfig(), nil, nil)

	createReq := httptest.NewRequest(http.MethodPost, "/api/auth/magic-link", strings.NewReader(`{}`))
	createReq.Header.Set("Content-Type", "application/json")
	createReq = createReq.WithContext(context.WithValue(createReq.Context(), principalContextKey, &requestPrincipal{
		Name:  "alice",
		Scope: storage.TokenScopeMember,
	}))
	createRec := httptest.NewRecorder()
	server.handleCreateMagicLink(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("unexpected create status %d: %s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	parsed, err := url.Parse(created.URL)
	if err != nil {
		t.Fatalf("parse create url: %v", err)
	}
	token := strings.TrimPrefix(parsed.Path, "/auth/magic/")
	if token == "" {
		t.Fatalf("expected token in path, got %q", parsed.Path)
	}
	ticketID := parsed.Query().Get("id")
	if ticketID == "" {
		t.Fatalf("expected ticket id query param")
	}

	redeemReq := httptest.NewRequest(http.MethodGet, "/auth/magic/"+token+"?id="+url.QueryEscape(ticketID), nil)
	redeemReq.Header.Set("Accept", "application/json")
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("token", token)
	redeemReq = redeemReq.WithContext(context.WithValue(redeemReq.Context(), chi.RouteCtxKey, routeCtx))
	redeemRec := httptest.NewRecorder()
	server.handleRedeemMagicLink(redeemRec, redeemReq)
	if redeemRec.Code != http.StatusOK {
		t.Fatalf("unexpected redeem status %d: %s", redeemRec.Code, redeemRec.Body.String())
	}
	cookie := redeemRec.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookie {
		if c.Name == sessionCookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil || strings.TrimSpace(sessionCookie.Value) == "" {
		t.Fatalf("expected %s cookie", sessionCookieName)
	}

	auth, err := store.GetAuthSession(sessionCookie.Value)
	if err != nil {
		t.Fatalf("GetAuthSession: %v", err)
	}
	if auth == nil {
		t.Fatalf("expected auth session record")
	}
	if auth.Principal != "alice" {
		t.Fatalf("auth.Principal=%q want %q", auth.Principal, "alice")
	}
	if auth.Scope != storage.TokenScopeMember {
		t.Fatalf("auth.Scope=%q want %q", auth.Scope, storage.TokenScopeMember)
	}
	if time.Until(auth.ExpiresAt) <= 0 {
		t.Fatalf("expected expiresAt in the future")
	}
}
