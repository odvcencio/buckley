package ipc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"m31labs.dev/buckley/pkg/storage"
)

func TestRESTLogoutDurableSuccess(t *testing.T) {
	server, store := newRESTLogoutTestServer(t, Config{EnableBrowser: true, RequireToken: true})
	if err := store.CreateAuthSession("rest-logout-session", "alice", storage.TokenScopeOperator, "", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://localhost/api/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "rest-logout-session"})
	restLogoutRouter(server).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("logout status=%d cache=%q body=%q", rec.Code, rec.Header().Get("Cache-Control"), rec.Body.String())
	}
	if session, err := store.GetAuthSession("rest-logout-session"); err != nil || session != nil {
		t.Fatalf("successful logout retained session %+v, err=%v", session, err)
	}
	cleared := onlyBrowserSessionCookie(t, rec.Result())
	if cleared.MaxAge >= 0 || !cleared.HttpOnly || cleared.SameSite != http.SameSiteLaxMode {
		t.Fatalf("logout cookie = %+v", cleared)
	}
}

func TestRESTLogoutDeleteFailureRetainsSessionAndCookie(t *testing.T) {
	server, store := newRESTLogoutTestServer(t, Config{EnableBrowser: true, RequireToken: true})
	sessionID := "rest-logout-failure"
	if err := store.CreateAuthSession(sessionID, "alice", storage.TokenScopeOperator, "", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`
		CREATE TRIGGER fail_rest_logout_delete
		BEFORE DELETE ON web_sessions
		WHEN OLD.id = 'rest-logout-failure'
		BEGIN
			SELECT RAISE(ABORT, 'raw-rest-delete-secret');
		END
	`); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://localhost/api/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionID})
	restLogoutRouter(server).ServeHTTP(rec, req)
	if rec.Code < 400 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("failed logout status=%d cache=%q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	if countNamedCookies(rec.Result(), sessionCookieName) != 0 {
		t.Fatalf("failed logout changed %d cookies", countNamedCookies(rec.Result(), sessionCookieName))
	}
	if strings.Contains(rec.Body.String(), "raw-rest-delete-secret") || strings.Contains(rec.Body.String(), sessionID) || !strings.Contains(rec.Body.String(), "logout failed") {
		t.Fatalf("failed logout body = %q", rec.Body.String())
	}
	if session, err := store.GetAuthSession(sessionID); err != nil || session == nil {
		t.Fatalf("failed logout removed session %+v, err=%v", session, err)
	}
}

func TestRESTLogoutBasicOnlyRevokesIssuedSession(t *testing.T) {
	server, store := newRESTLogoutTestServer(t, Config{
		EnableBrowser: true, RequireToken: true,
		BasicAuthEnabled: true, BasicAuthUsername: "operator", BasicAuthPassword: "password",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://localhost/api/auth/logout", nil)
	req.SetBasicAuth("operator", "password")
	restLogoutRouter(server).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Basic logout status=%d cache=%q body=%q", rec.Code, rec.Header().Get("Cache-Control"), rec.Body.String())
	}
	var issued string
	cleared := false
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name != sessionCookieName {
			continue
		}
		if cookie.MaxAge < 0 {
			cleared = true
			continue
		}
		if issued != "" {
			t.Fatal("Basic logout issued multiple live sessions")
		}
		issued = cookie.Value
	}
	if issued == "" || !cleared {
		t.Fatalf("Basic logout cookies = %v", rec.Result().Cookies())
	}
	if session, err := store.GetAuthSession(issued); err != nil || session != nil {
		t.Fatalf("Basic-issued session survived logout %+v, err=%v", session, err)
	}
	if count, err := store.CountActiveAuthSessions(time.Now()); err != nil || count != 0 {
		t.Fatalf("active auth sessions=%d err=%v", count, err)
	}
}

func TestRESTLogoutRejectsDuplicateRequestCookiesWithoutMutation(t *testing.T) {
	server, store := newRESTLogoutTestServer(t, Config{EnableBrowser: true, RequireToken: true})
	for _, id := range []string{"duplicate-first", "duplicate-second"} {
		if err := store.CreateAuthSession(id, "alice", storage.TokenScopeOperator, "", time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://localhost/api/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "duplicate-first"})
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "duplicate-second"})
	restLogoutRouter(server).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("duplicate logout status=%d cache=%q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	if countNamedCookies(rec.Result(), sessionCookieName) != 0 || !strings.Contains(rec.Body.String(), "logout failed") {
		t.Fatalf("duplicate logout cookies/body=%d/%q", countNamedCookies(rec.Result(), sessionCookieName), rec.Body.String())
	}
	for _, id := range []string{"duplicate-first", "duplicate-second"} {
		if session, err := store.GetAuthSession(id); err != nil || session == nil {
			t.Fatalf("duplicate logout mutated %q: %+v, err=%v", id, session, err)
		}
	}
}

func TestRESTLogoutRequestAndIssuedSessionsRollbackTogether(t *testing.T) {
	server, store := newRESTLogoutTestServer(t, Config{EnableBrowser: true, RequireToken: true})
	for _, id := range []string{"atomic-request", "atomic-issued"} {
		if err := store.CreateAuthSession(id, "alice", storage.TokenScopeOperator, "", time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.DB().Exec(`
		CREATE TRIGGER fail_issued_logout_delete
		BEFORE DELETE ON web_sessions
		WHEN OLD.id = 'atomic-issued'
		BEGIN
			SELECT RAISE(ABORT, 'injected issued delete failure');
		END
	`); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://localhost/api/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "atomic-request"})
	ctx := context.WithValue(req.Context(), issuedAuthSessionContextKey, "atomic-issued")
	req = req.WithContext(ctx)
	restLogoutRouter(server).ServeHTTP(rec, req)
	if rec.Code < 400 || countNamedCookies(rec.Result(), sessionCookieName) != 0 {
		t.Fatalf("atomic logout status=%d cookies=%d", rec.Code, countNamedCookies(rec.Result(), sessionCookieName))
	}
	for _, id := range []string{"atomic-request", "atomic-issued"} {
		if session, err := store.GetAuthSession(id); err != nil || session == nil {
			t.Fatalf("atomic logout partially deleted %q: %+v, err=%v", id, session, err)
		}
	}
}

func newRESTLogoutTestServer(t *testing.T, cfg Config) (*Server, *storage.Store) {
	t.Helper()
	store, err := storage.New(filepath.Join(t.TempDir(), "rest-logout.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if cfg.BindAddress == "" {
		cfg.BindAddress = "127.0.0.1:0"
	}
	return &Server{cfg: cfg, store: store}, store
}

func restLogoutRouter(server *Server) http.Handler {
	router := chi.NewRouter()
	router.Use(server.corsMiddleware)
	router.Use(server.securityHeadersMiddleware)
	router.Use(server.sessionMiddleware)
	router.Use(server.basicAuthMiddleware)
	router.Route("/api", func(r chi.Router) {
		r.Use(server.authMiddleware)
		r.Post("/auth/logout", server.handleAuthLogout)
	})
	return router
}
