package ipc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestSecurityHeadersMiddlewareSetsBaselineHeaders(t *testing.T) {
	s := &Server{cfg: Config{EnableBrowser: true}}
	handler := s.securityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("expected X-Content-Type-Options nosniff, got %q", got)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("expected X-Frame-Options DENY, got %q", got)
	}
	if got := rec.Header().Get("Referrer-Policy"); got == "" {
		t.Fatalf("expected Referrer-Policy to be set")
	}
	if got := rec.Header().Get("Permissions-Policy"); got == "" {
		t.Fatalf("expected Permissions-Policy to be set")
	}
	if got := rec.Header().Get("Content-Security-Policy"); got == "" {
		t.Fatalf("expected Content-Security-Policy to be set when browser enabled")
	}
}

func TestSecurityHeadersMiddlewareSkipsCSPWhenBrowserDisabled(t *testing.T) {
	s := &Server{cfg: Config{EnableBrowser: false}}
	handler := s.securityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Security-Policy"); got != "" {
		t.Fatalf("expected no CSP when browser disabled, got %q", got)
	}
}

func TestCLITicketPageOverridesCSPToAllowInlineScript(t *testing.T) {
	s := &Server{cfg: Config{EnableBrowser: true}}
	handler := s.securityHeadersMiddleware(http.HandlerFunc(s.handleCliTicketPage))

	req := httptest.NewRequest(http.MethodGet, "/cli/login/ticket-1", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("ticket", "ticket-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src") || !strings.Contains(csp, "unsafe-inline") {
		t.Fatalf("expected CSP to allow inline script, got %q", csp)
	}
}
