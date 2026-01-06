package ipc

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/odvcencio/buckley/pkg/storage"
)

func TestBrowserUIDoesNotShadowAPI(t *testing.T) {
	tmpDir := t.TempDir()
	uiDir := filepath.Join(tmpDir, "ui")
	if err := os.MkdirAll(filepath.Join(uiDir, "assets"), 0o755); err != nil {
		t.Fatalf("mkdir ui: %v", err)
	}
	if err := os.WriteFile(filepath.Join(uiDir, "index.html"), []byte("<!doctype html><html><body>ok</body></html>\n"), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	if err := os.WriteFile(filepath.Join(uiDir, "assets", "app.js"), []byte("console.log('ok');\n"), 0o644); err != nil {
		t.Fatalf("write asset: %v", err)
	}

	store, err := storage.New(filepath.Join(tmpDir, "buckley.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now()
	if err := store.CreateSession(&storage.Session{
		ID:          "s1",
		ProjectPath: tmpDir,
		CreatedAt:   now,
		LastActive:  now,
		Status:      storage.SessionStatusActive,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	server := NewServer(Config{
		BindAddress:    "127.0.0.1:0",
		StaticDir:      uiDir,
		EnableBrowser:  true,
		AllowedOrigins: []string{"*"},
		RequireToken:   true,
		AuthToken:      "unit-token",
		ProjectRoot:    tmpDir,
	}, store, nil, nil, nil, nil, nil, nil)

	router := chi.NewRouter()
	router.Use(server.corsMiddleware)
	router.Use(server.securityHeadersMiddleware)
	router.Use(server.sessionMiddleware)
	router.Use(server.basicAuthMiddleware)

	api := chi.NewRouter()
	api.Get("/sessions", server.handleListSessions)
	router.Route("/api", func(r chi.Router) {
		r.Use(server.authMiddleware)
		r.Mount("/", api)
	})

	server.mountBrowserUI(router)

	ts := httptest.NewServer(router)
	t.Cleanup(ts.Close)

	resp, err := ts.Client().Get(ts.URL + "/api/sessions")
	if err != nil {
		t.Fatalf("GET /api/sessions: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for /api/sessions without token, got %d", resp.StatusCode)
	}

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/sessions", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer unit-token")
	resp, err = ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /api/sessions with token: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for /api/sessions with token, got %d", resp.StatusCode)
	}

	resp, err = ts.Client().Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for /, got %d", resp.StatusCode)
	}
	buf := make([]byte, 64)
	n, _ := resp.Body.Read(buf)
	if !strings.Contains(strings.ToLower(string(buf[:n])), "<!doctype html") {
		t.Fatalf("expected HTML response for /")
	}
}
