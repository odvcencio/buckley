package ipc

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsOriginAllowed_LocalhostAllowsAnyPort(t *testing.T) {
	s := &Server{cfg: Config{AllowedOrigins: []string{"http://localhost", "http://127.0.0.1"}}}

	allowed, wildcard := s.isOriginAllowed("http://localhost:5173")
	if !allowed || wildcard {
		t.Fatalf("expected localhost origin allowed without wildcard, got allowed=%v wildcard=%v", allowed, wildcard)
	}

	allowed, wildcard = s.isOriginAllowed("http://127.0.0.1:4488")
	if !allowed || wildcard {
		t.Fatalf("expected loopback ip origin allowed without wildcard, got allowed=%v wildcard=%v", allowed, wildcard)
	}
}

func TestIsOriginAllowed_NonLoopbackDefaultsToSchemePort(t *testing.T) {
	s := &Server{cfg: Config{AllowedOrigins: []string{"https://example.com"}}}

	allowed, wildcard := s.isOriginAllowed("https://example.com")
	if !allowed || wildcard {
		t.Fatalf("expected origin allowed without wildcard, got allowed=%v wildcard=%v", allowed, wildcard)
	}

	allowed, wildcard = s.isOriginAllowed("https://example.com:443")
	if !allowed || wildcard {
		t.Fatalf("expected default port origin allowed without wildcard, got allowed=%v wildcard=%v", allowed, wildcard)
	}

	allowed, _ = s.isOriginAllowed("https://example.com:444")
	if allowed {
		t.Fatalf("expected non-default port to be rejected")
	}
}

func TestCORSMiddlewareWildcardDoesNotAllowCredentials(t *testing.T) {
	s := &Server{cfg: Config{AllowedOrigins: []string{"*"}}}
	handler := s.corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.Header.Set("Origin", "https://evil.com")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "https://evil.com" {
		t.Fatalf("allow-origin=%q want %q", got, "https://evil.com")
	}
	if got := rr.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("expected allow-credentials absent for wildcard, got %q", got)
	}
}

func TestCORSMiddlewareExplicitAllowsCredentialsEvenWithWildcard(t *testing.T) {
	s := &Server{cfg: Config{AllowedOrigins: []string{"*", "http://localhost"}}}
	handler := s.corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Fatalf("allow-origin=%q want %q", got, "http://localhost:5173")
	}
	if got := rr.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("allow-credentials=%q want true", got)
	}
}

func TestWebSocketOriginAllowed_SameHostAllowed(t *testing.T) {
	s := &Server{cfg: Config{AllowedOrigins: []string{"http://other.test"}}}

	req := httptest.NewRequest(http.MethodGet, "http://buckley.example.com/ws", nil)
	req.Header.Set("Origin", "http://buckley.example.com")

	if !s.isWebSocketOriginAllowed(req) {
		t.Fatalf("expected same-host websocket origin to be allowed")
	}
}

func TestWebSocketOriginAllowed_DisallowedOriginRejected(t *testing.T) {
	s := &Server{cfg: Config{AllowedOrigins: []string{"http://good.test"}}}

	req := httptest.NewRequest(http.MethodGet, "http://buckley.example.com/ws", nil)
	req.Header.Set("Origin", "http://evil.test")

	if s.isWebSocketOriginAllowed(req) {
		t.Fatalf("expected websocket origin to be rejected")
	}
}
