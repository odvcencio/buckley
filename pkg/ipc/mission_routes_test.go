package ipc

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/storage"
)

func TestMissionRoutesUseSingleAuthenticatedAPIPrefix(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "buckley.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const token = "mission-route-token"
	server := NewServer(Config{
		BindAddress:  "127.0.0.1:0",
		RequireToken: true,
		AuthToken:    token,
	}, store, nil, nil, nil, config.DefaultConfig(), nil, nil)
	router := chi.NewRouter()
	router.Use(server.corsMiddleware)
	router.Use(server.securityHeadersMiddleware)
	router.Use(server.sessionMiddleware)
	router.Use(server.basicAuthMiddleware)
	api := chi.NewRouter()
	server.setupMissionRoutes(api)
	router.Route("/api", func(r chi.Router) {
		r.Use(server.authMiddleware)
		r.Mount("/", api)
	})

	want := map[string]struct{}{
		"GET /api/mission/events":                      {},
		"GET /api/mission/agents":                      {},
		"GET /api/mission/agents/{agentID}":            {},
		"GET /api/mission/agents/{agentID}/activity":   {},
		"POST /api/mission/agents/{agentID}/message":   {},
		"GET /api/mission/changes":                     {},
		"GET /api/mission/changes/{changeID}":          {},
		"POST /api/mission/changes/{changeID}/approve": {},
		"POST /api/mission/changes/{changeID}/reject":  {},
	}
	got := make(map[string]struct{}, len(want))
	if err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.Contains(route, "/mission/") {
			// chi.Walk represents a router mounted at "/" with an internal
			// wildcard; requests do not contain that segment.
			effective := strings.Replace(route, "/*/", "/", 1)
			got[method+" "+effective] = struct{}{}
		}
		return nil
	}); err != nil {
		t.Fatalf("chi.Walk: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("effective Mission routes=%v want=%v", got, want)
	}
	for pattern := range want {
		if _, ok := got[pattern]; !ok {
			t.Errorf("missing effective Mission route %q (got %v)", pattern, got)
		}
	}
	for pattern := range got {
		if strings.Contains(pattern, "/api/api/") {
			t.Errorf("double-prefixed Mission route remains registered: %q", pattern)
		}
	}

	unauthenticated := httptest.NewRecorder()
	router.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/api/mission/agents", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d want %d body=%s", unauthenticated.Code, http.StatusUnauthorized, unauthenticated.Body.String())
	}

	authenticatedRequest := httptest.NewRequest(http.MethodGet, "/api/mission/agents", nil)
	authenticatedRequest.Header.Set("Authorization", "Bearer "+token)
	authenticated := httptest.NewRecorder()
	router.ServeHTTP(authenticated, authenticatedRequest)
	if authenticated.Code != http.StatusOK || !strings.Contains(authenticated.Body.String(), `"agents"`) {
		t.Fatalf("authenticated intended route status=%d body=%s", authenticated.Code, authenticated.Body.String())
	}

	doubleRequest := httptest.NewRequest(http.MethodGet, "/api/api/mission/agents", nil)
	doubleRequest.Header.Set("Authorization", "Bearer "+token)
	double := httptest.NewRecorder()
	router.ServeHTTP(double, doubleRequest)
	if double.Code != http.StatusNotFound {
		t.Fatalf("double-prefixed route status=%d want %d body=%s", double.Code, http.StatusNotFound, double.Body.String())
	}
}
