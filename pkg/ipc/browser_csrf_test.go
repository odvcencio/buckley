package ipc

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"

	"m31labs.dev/buckley/pkg/storage"
)

func TestBrowserCSRFValidationFailsClosed(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "browser-csrf.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := &Server{store: store}
	backend := gosxBackend{server: server}
	principal := requestPrincipal{Name: "alice", Scope: storage.TokenScopeOperator, TokenID: createBrowserSourceToken(t, store, "alice", storage.TokenScopeOperator)}
	otherTokenID := createBrowserSourceToken(t, store, "other", storage.TokenScopeOperator)
	now := time.Now()
	for _, session := range []struct {
		id        string
		principal requestPrincipal
		expires   time.Time
	}{
		{id: "session-alice-a", principal: principal, expires: now.Add(time.Hour)},
		{id: "session-alice-b", principal: principal, expires: now.Add(time.Hour)},
		{id: "session-expired", principal: principal, expires: now.Add(-time.Minute)},
		{id: "session-foreign-principal", principal: requestPrincipal{Name: "bob", Scope: principal.Scope, TokenID: principal.TokenID}, expires: now.Add(time.Hour)},
		{id: "session-foreign-scope", principal: requestPrincipal{Name: principal.Name, Scope: storage.TokenScopeMember, TokenID: principal.TokenID}, expires: now.Add(time.Hour)},
		{id: "session-foreign-token", principal: requestPrincipal{Name: principal.Name, Scope: principal.Scope, TokenID: otherTokenID}, expires: now.Add(time.Hour)},
		{id: "session-revoked", principal: principal, expires: now.Add(time.Hour)},
	} {
		if err := store.CreateAuthSession(session.id, session.principal.Name, session.principal.Scope, session.principal.TokenID, session.expires); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.DeleteAuthSession("session-revoked"); err != nil {
		t.Fatal(err)
	}
	validToken, err := browserCSRFToken("session-alice-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(validToken) != browserCSRFEncodedSize || strings.ContainsAny(validToken, "+/=") {
		t.Fatalf("token is not compact base64url: %q", validToken)
	}

	tests := []struct {
		name        string
		session     string
		token       string
		principal   requestPrincipal
		duplicate   bool
		rawCookie   string
		wantAllowed bool
	}{
		{name: "valid", session: "session-alice-a", token: validToken, principal: principal, wantAllowed: true},
		{name: "cross session", session: "session-alice-b", token: validToken, principal: principal},
		{name: "missing token", session: "session-alice-a", principal: principal},
		{name: "malformed token", session: "session-alice-a", token: strings.Repeat("!", browserCSRFEncodedSize), principal: principal},
		{name: "oversized token", session: "session-alice-a", token: strings.Repeat("A", 4096), principal: principal},
		{name: "unicode token", session: "session-alice-a", token: strings.Repeat("秘密", 32), principal: principal},
		{name: "expired session", session: "session-expired", token: validToken, principal: principal},
		{name: "revoked session", session: "session-revoked", token: validToken, principal: principal},
		{name: "foreign principal", session: "session-foreign-principal", token: validToken, principal: principal},
		{name: "foreign scope", session: "session-foreign-scope", token: validToken, principal: principal},
		{name: "foreign token identity", session: "session-foreign-token", token: validToken, principal: principal},
		{name: "duplicate cookie", session: "session-alice-a", token: validToken, principal: principal, duplicate: true},
		{name: "empty cookie", token: validToken, principal: principal, rawCookie: sessionCookieName + "="},
		{name: "oversized cookie", session: strings.Repeat("a", 257), token: validToken, principal: principal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://localhost/__actions/command", nil)
			if test.rawCookie != "" {
				req.Header.Set("Cookie", test.rawCookie)
			} else if test.session != "" {
				req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: test.session})
				if test.duplicate {
					req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: test.session})
				}
			}
			requestPrincipal := test.principal
			req = req.WithContext(context.WithValue(req.Context(), principalContextKey, &requestPrincipal))
			err := backend.ValidateMutation(req.Context(), req, test.token)
			if test.wantAllowed && err != nil {
				t.Fatalf("valid mutation rejected: %v", err)
			}
			if !test.wantAllowed && !errors.Is(err, errBrowserMutationForbidden) {
				t.Fatalf("error = %v, want browser mutation forbidden", err)
			}
		})
	}
}

func TestBrowserCSRFSessionDurabilityAndRevocation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "browser-session.db")
	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	principal := requestPrincipal{Name: "durable-user", Scope: storage.TokenScopeMember, TokenID: createBrowserSourceToken(t, store, "durable-user", storage.TokenScopeMember)}
	sessionID := "durable-browser-session"
	if err := store.CreateAuthSession(sessionID, principal.Name, principal.Scope, principal.TokenID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	token, err := browserCSRFToken(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	restartedStore, err := storage.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restartedStore.Close() })
	restarted := &Server{store: restartedStore}
	request := browserMutationRequest(t, principal, sessionID)
	if err := (gosxBackend{server: restarted}).ValidateMutation(request.Context(), request, token); err != nil {
		t.Fatalf("same-DB active session did not survive restart: %v", err)
	}

	if err := restartedStore.DeleteAuthSession(sessionID); err != nil {
		t.Fatal(err)
	}
	if err := (gosxBackend{server: restarted}).ValidateMutation(request.Context(), request, token); !errors.Is(err, errBrowserMutationForbidden) {
		t.Fatalf("deleted session error = %v", err)
	}

	newStore, err := storage.New(filepath.Join(t.TempDir(), "new-browser-session.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = newStore.Close() })
	if err := (gosxBackend{server: &Server{store: newStore}}).ValidateMutation(request.Context(), request, token); !errors.Is(err, errBrowserMutationForbidden) {
		t.Fatalf("stale cookie against new DB error = %v", err)
	}
}

func TestBrowserAuthenticatedGETBootstrapsLocalCookie(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "browser-bootstrap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := NewServer(Config{
		BindAddress: "127.0.0.1:0", EnableBrowser: true, RequireToken: true, AuthToken: "unit-token",
	}, store, nil, nil, nil, nil, nil, nil)
	handler := browserCSRFRouter(server)

	bootstrap := httptest.NewRecorder()
	bootstrapReq := httptest.NewRequest(http.MethodGet, "http://localhost/?token=unit-token&session=run-1&page=2&tag=a&tag=b", nil)
	handler.ServeHTTP(bootstrap, bootstrapReq)
	if bootstrap.Code != http.StatusSeeOther || bootstrap.Header().Get("Location") != "/?page=2&session=run-1&tag=a&tag=b" {
		t.Fatalf("query bootstrap status=%d location=%q", bootstrap.Code, bootstrap.Header().Get("Location"))
	}
	if bootstrap.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("query bootstrap cache control = %q", bootstrap.Header().Get("Cache-Control"))
	}
	cookie := onlyBrowserSessionCookie(t, bootstrap.Result())
	if cookie.Value == "unit-token" || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Secure {
		t.Fatalf("bootstrap cookie = %+v", cookie)
	}
	issued, err := store.GetAuthSession(cookie.Value)
	if err != nil || issued == nil || issued.Principal != "builtin" || issued.Scope != storage.TokenScopeOperator {
		t.Fatalf("query bootstrap principal = %+v, %v", issued, err)
	}

	follow := httptest.NewRecorder()
	followReq := httptest.NewRequest(http.MethodGet, "http://localhost"+bootstrap.Header().Get("Location"), nil)
	followReq.AddCookie(cookie)
	handler.ServeHTTP(follow, followReq)
	if follow.Code != http.StatusOK || follow.Header().Get("Location") != "" || !strings.Contains(follow.Body.String(), "Mission Control") {
		t.Fatalf("follow status=%d location=%q", follow.Code, follow.Header().Get("Location"))
	}
	if follow.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("authenticated page cache control = %q", follow.Header().Get("Cache-Control"))
	}
	if got := countNamedCookies(follow.Result(), sessionCookieName); got != 0 {
		t.Fatalf("follow response rotated %d session cookies", got)
	}
	expectedCSRF, err := browserCSRFToken(cookie.Value)
	if err != nil || !strings.Contains(follow.Body.String(), `name="_csrf" value="`+expectedCSRF+`"`) {
		t.Fatalf("rendered document missing bound CSRF token: %v", err)
	}
	assertGoSXNavigationCSP(t, follow)

	invalid := httptest.NewRecorder()
	invalidReq := httptest.NewRequest(http.MethodGet, "http://localhost/?token=wrong&session=run-2&page=3", nil)
	handler.ServeHTTP(invalid, invalidReq)
	if invalid.Code != http.StatusSeeOther || invalid.Header().Get("Location") != "/?page=3&session=run-2" || countNamedCookies(invalid.Result(), sessionCookieName) != 0 {
		t.Fatalf("invalid query strip status=%d location=%q cookies=%d", invalid.Code, invalid.Header().Get("Location"), countNamedCookies(invalid.Result(), sessionCookieName))
	}
	if invalid.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("invalid query cache control = %q", invalid.Header().Get("Cache-Control"))
	}
	malformed := httptest.NewRecorder()
	malformedReq := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
	malformedReq.URL.RawQuery = "token=%zz&session=run-4&page=5"
	handler.ServeHTTP(malformed, malformedReq)
	if malformed.Code != http.StatusSeeOther || malformed.Header().Get("Location") != "/?page=5&session=run-4" || countNamedCookies(malformed.Result(), sessionCookieName) != 0 {
		t.Fatalf("malformed query strip status=%d location=%q cookies=%d", malformed.Code, malformed.Header().Get("Location"), countNamedCookies(malformed.Result(), sessionCookieName))
	}

	bearer := httptest.NewRecorder()
	bearerReq := httptest.NewRequest(http.MethodGet, "https://localhost/?session=run-3&page=4", nil)
	bearerReq.Header.Set("Authorization", "Bearer unit-token")
	handler.ServeHTTP(bearer, bearerReq)
	if bearer.Code != http.StatusSeeOther || bearer.Header().Get("Location") != "/?page=4&session=run-3" {
		t.Fatalf("bearer bootstrap status=%d location=%q", bearer.Code, bearer.Header().Get("Location"))
	}
	bearerCookie := onlyBrowserSessionCookie(t, bearer.Result())
	if !bearerCookie.Secure || !bearerCookie.HttpOnly || bearerCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("secure bearer cookie = %+v", bearerCookie)
	}
}

func TestBrowserBasicAuthBootstrapIssuesOneCookie(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "browser-basic.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := NewServer(Config{
		BindAddress: "127.0.0.1:0", EnableBrowser: true, RequireToken: true,
		BasicAuthEnabled: true, BasicAuthUsername: "operator", BasicAuthPassword: "password",
	}, store, nil, nil, nil, nil, nil, nil)
	handler := browserCSRFRouter(server)

	bootstrap := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://localhost/?session=run-basic", nil)
	req.SetBasicAuth("operator", "password")
	handler.ServeHTTP(bootstrap, req)
	if bootstrap.Code != http.StatusSeeOther || bootstrap.Header().Get("Location") != "/?session=run-basic" {
		t.Fatalf("basic bootstrap status=%d location=%q", bootstrap.Code, bootstrap.Header().Get("Location"))
	}
	cookie := onlyBrowserSessionCookie(t, bootstrap.Result())
	issued, err := store.GetAuthSession(cookie.Value)
	if err != nil || issued == nil || issued.Principal != "operator" || issued.Scope != storage.TokenScopeOperator {
		t.Fatalf("basic bootstrap principal = %+v, %v", issued, err)
	}

	follow := httptest.NewRecorder()
	followReq := httptest.NewRequest(http.MethodGet, "http://localhost/?session=run-basic", nil)
	followReq.AddCookie(cookie)
	handler.ServeHTTP(follow, followReq)
	if follow.Code != http.StatusOK {
		t.Fatalf("basic cookie follow status=%d", follow.Code)
	}
}

func TestBrowserLogoutRequiresValidCSRFAndRevokesSession(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "browser-logout.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := NewServer(Config{BindAddress: "127.0.0.1:0", EnableBrowser: true, RequireToken: true}, store, nil, nil, nil, nil, nil, nil)
	handler := browserCSRFRouter(server)
	principal := requestPrincipal{Name: "logout-user", Scope: storage.TokenScopeOperator, TokenID: createBrowserSourceToken(t, store, "logout-user", storage.TokenScopeOperator)}
	sessionID := "logout-browser-session"
	if err := store.CreateAuthSession(sessionID, principal.Name, principal.Scope, principal.TokenID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: sessionCookieName, Value: sessionID}

	invalid := httptest.NewRecorder()
	invalidReq := httptest.NewRequest(http.MethodPost, "http://localhost/__actions/logout", strings.NewReader(url.Values{"_csrf": {"invalid"}}.Encode()))
	invalidReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	invalidReq.AddCookie(cookie)
	handler.ServeHTTP(invalid, invalidReq)
	if invalid.Code != http.StatusForbidden {
		t.Fatalf("invalid logout status=%d", invalid.Code)
	}
	if session, err := store.GetAuthSession(sessionID); err != nil || session == nil {
		t.Fatalf("invalid logout revoked session: %+v, %v", session, err)
	}

	token, err := browserCSRFToken(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	valid := httptest.NewRecorder()
	validReq := httptest.NewRequest(http.MethodPost, "http://localhost/__actions/logout", strings.NewReader(url.Values{"_csrf": {token}}.Encode()))
	validReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	validReq.AddCookie(cookie)
	handler.ServeHTTP(valid, validReq)
	if valid.Code != http.StatusSeeOther || valid.Header().Get("Location") != "/" {
		t.Fatalf("valid logout status=%d location=%q", valid.Code, valid.Header().Get("Location"))
	}
	if valid.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("logout cache control = %q", valid.Header().Get("Cache-Control"))
	}
	if session, err := store.GetAuthSession(sessionID); err != nil || session != nil {
		t.Fatalf("valid logout retained session: %+v, %v", session, err)
	}
	cleared := onlyBrowserSessionCookie(t, valid.Result())
	if cleared.MaxAge >= 0 || !cleared.HttpOnly || cleared.SameSite != http.SameSiteLaxMode {
		t.Fatalf("logout cookie = %+v", cleared)
	}

	replay := httptest.NewRecorder()
	replayReq := httptest.NewRequest(http.MethodPost, "http://localhost/__actions/logout", strings.NewReader(url.Values{"_csrf": {token}}.Encode()))
	replayReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	replayReq.AddCookie(cookie)
	handler.ServeHTTP(replay, replayReq)
	if replay.Code != http.StatusForbidden {
		t.Fatalf("revoked cookie/token replay status=%d", replay.Code)
	}
}

func TestBrowserAPITokenRevocationInvalidatesDerivedSession(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "browser-token-revoke.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	secret, err := storage.GenerateAPITokenValue()
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.CreateAPIToken("browser-bearer", "bearer-user", storage.TokenScopeMember, secret)
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(Config{BindAddress: "127.0.0.1:0", EnableBrowser: true, RequireToken: true}, store, nil, nil, nil, nil, nil, nil)
	handler := browserCSRFRouter(server)

	bootstrap := httptest.NewRecorder()
	bootstrapReq := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
	bootstrapReq.Header.Set("Authorization", "Bearer "+secret)
	handler.ServeHTTP(bootstrap, bootstrapReq)
	if bootstrap.Code != http.StatusSeeOther || bootstrap.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("bearer bootstrap status=%d cache=%q", bootstrap.Code, bootstrap.Header().Get("Cache-Control"))
	}
	cookie := onlyBrowserSessionCookie(t, bootstrap.Result())
	session, err := store.GetAuthSession(cookie.Value)
	if err != nil || session == nil || session.TokenID != record.ID {
		t.Fatalf("derived session = %+v, err=%v", session, err)
	}
	csrf, err := browserCSRFToken(cookie.Value)
	if err != nil {
		t.Fatal(err)
	}

	follow := httptest.NewRecorder()
	followReq := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
	followReq.AddCookie(cookie)
	handler.ServeHTTP(follow, followReq)
	if follow.Code != http.StatusOK || !strings.Contains(follow.Body.String(), "bearer-user") {
		t.Fatalf("pre-revocation browser GET status=%d", follow.Code)
	}

	server.refreshAuthSessionGauge()
	if got := authSessionGaugeValue(t); got != 1 {
		t.Fatalf("pre-revocation auth session gauge=%v", got)
	}
	revokeRoute := chi.NewRouteContext()
	revokeRoute.URLParams.Add("tokenID", record.ID)
	revokeReq := httptest.NewRequest(http.MethodDelete, "http://localhost/api/config/api-tokens/"+record.ID, nil)
	revokePrincipal := &requestPrincipal{Name: "operator", Scope: storage.TokenScopeOperator}
	revokeCtx := context.WithValue(revokeReq.Context(), chi.RouteCtxKey, revokeRoute)
	revokeCtx = context.WithValue(revokeCtx, principalContextKey, revokePrincipal)
	revoke := httptest.NewRecorder()
	server.handleRevokeAPIToken(revoke, revokeReq.WithContext(revokeCtx))
	if revoke.Code != http.StatusNoContent {
		t.Fatalf("token revoke status=%d body=%q", revoke.Code, revoke.Body.String())
	}
	if got := authSessionGaugeValue(t); got != 0 {
		t.Fatalf("post-revocation auth session gauge=%v", got)
	}
	if session, err := store.GetAuthSession(cookie.Value); err != nil || session != nil {
		t.Fatalf("revocation left browser session %+v, err=%v", session, err)
	}

	deniedGET := httptest.NewRecorder()
	deniedGETReq := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
	deniedGETReq.AddCookie(cookie)
	handler.ServeHTTP(deniedGET, deniedGETReq)
	if deniedGET.Code != http.StatusOK || strings.Contains(deniedGET.Body.String(), "bearer-user") || strings.Contains(deniedGET.Body.String(), `value="`+csrf+`"`) {
		t.Fatalf("revoked browser GET remained authenticated: status=%d", deniedGET.Code)
	}
	if deniedGET.Header().Get("Cache-Control") != "no-store" || countNamedCookies(deniedGET.Result(), sessionCookieName) != 0 {
		t.Fatalf("revoked browser GET cache=%q cookies=%d", deniedGET.Header().Get("Cache-Control"), countNamedCookies(deniedGET.Result(), sessionCookieName))
	}

	deniedMutation := httptest.NewRecorder()
	deniedMutationReq := httptest.NewRequest(http.MethodPost, "http://localhost/__actions/command", strings.NewReader(url.Values{
		"_csrf": {csrf}, "session_id": {"run-1"}, "type": {"input"}, "content": {"continue"},
	}.Encode()))
	deniedMutationReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	deniedMutationReq.AddCookie(cookie)
	handler.ServeHTTP(deniedMutation, deniedMutationReq)
	if deniedMutation.Code != http.StatusForbidden || deniedMutation.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("revoked mutation status=%d cache=%q", deniedMutation.Code, deniedMutation.Header().Get("Cache-Control"))
	}
}

func TestBrowserLogoutDeleteFailurePreservesSessionAndCookie(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "browser-logout-failure.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := NewServer(Config{BindAddress: "127.0.0.1:0", EnableBrowser: true, RequireToken: true}, store, nil, nil, nil, nil, nil, nil)
	handler := browserCSRFRouter(server)
	principal := requestPrincipal{Name: "logout-failure-user", Scope: storage.TokenScopeOperator, TokenID: createBrowserSourceToken(t, store, "logout-failure-user", storage.TokenScopeOperator)}
	sessionID := "logout-failure-browser-session"
	if err := store.CreateAuthSession(sessionID, principal.Name, principal.Scope, principal.TokenID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`
		CREATE TRIGGER fail_exact_browser_logout
		BEFORE DELETE ON web_sessions
		WHEN OLD.id = 'logout-failure-browser-session'
		BEGIN
			SELECT RAISE(ABORT, 'raw-delete-secret');
		END
	`); err != nil {
		t.Fatal(err)
	}
	csrf, err := browserCSRFToken(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: sessionCookieName, Value: sessionID}

	failure := httptest.NewRecorder()
	failureReq := httptest.NewRequest(http.MethodPost, "http://localhost/__actions/logout", strings.NewReader(url.Values{"_csrf": {csrf}}.Encode()))
	failureReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	failureReq.AddCookie(cookie)
	handler.ServeHTTP(failure, failureReq)
	if failure.Code != http.StatusSeeOther || failure.Header().Get("Location") != "/?error=action+failed" {
		t.Fatalf("failed logout status=%d location=%q", failure.Code, failure.Header().Get("Location"))
	}
	if failure.Header().Get("Cache-Control") != "no-store" || countNamedCookies(failure.Result(), sessionCookieName) != 0 {
		t.Fatalf("failed logout cache=%q cookies=%d", failure.Header().Get("Cache-Control"), countNamedCookies(failure.Result(), sessionCookieName))
	}
	responseText := failure.Header().Get("Location") + failure.Body.String()
	if strings.Contains(responseText, "raw-delete-secret") || strings.Contains(responseText, sessionID) || strings.Contains(responseText, csrf) {
		t.Fatalf("failed logout reflected sensitive state: %q", responseText)
	}
	if session, err := store.GetAuthSession(sessionID); err != nil || session == nil {
		t.Fatalf("failed logout removed session %+v, err=%v", session, err)
	}
	request := browserMutationRequest(t, principal, sessionID)
	if err := (gosxBackend{server: server}).ValidateMutation(request.Context(), request, csrf); err != nil {
		t.Fatalf("failed logout invalidated retained cookie: %v", err)
	}
}

func TestBrowserDynamicAuthUnavailableIsNoStore(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "auth-unavailable.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := NewServer(Config{
		BindAddress: "127.0.0.1:0", EnableBrowser: true, RequireToken: true, AuthToken: "builtin-token",
	}, store, nil, nil, nil, nil, nil, nil)
	server.store = nil
	handler := browserCSRFRouter(server)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://localhost/?token=builtin-token", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("auth unavailable status=%d cache=%q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	if !strings.Contains(rec.Body.String(), "authentication unavailable") || strings.Contains(rec.Body.String(), "storage") {
		t.Fatalf("auth unavailable response = %q", rec.Body.String())
	}
}

func TestBrowserAnonymousDynamicPageIsNoStore(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "anonymous-browser.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := NewServer(Config{BindAddress: "127.0.0.1:0", EnableBrowser: true}, store, nil, nil, nil, nil, nil, nil)
	rec := httptest.NewRecorder()
	browserCSRFRouter(server).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://localhost/", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("anonymous page status=%d cache=%q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	if countNamedCookies(rec.Result(), sessionCookieName) != 0 {
		t.Fatalf("anonymous page issued %d browser cookies", countNamedCookies(rec.Result(), sessionCookieName))
	}
}

func TestBrowserLocalRedirectCannotEscapeOrigin(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "https://buckley.local//evil.example/phish?token=one&session=run-1&page=2&token=two", nil)
	destination := browserLocalRedirect(req)
	parsed, err := url.Parse(destination)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.IsAbs() || parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/") || parsed.Query().Has("token") {
		t.Fatalf("unsafe redirect destination %q", destination)
	}
	if parsed.Query().Get("session") != "run-1" || parsed.Query().Get("page") != "2" {
		t.Fatalf("safe query values lost in %q", destination)
	}
}

func browserMutationRequest(t *testing.T, principal requestPrincipal, sessionID string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "http://localhost/__actions/command", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionID})
	return req.WithContext(context.WithValue(req.Context(), principalContextKey, &principal))
}

func createBrowserSourceToken(t *testing.T, store *storage.Store, owner, scope string) string {
	t.Helper()
	secret, err := storage.GenerateAPITokenValue()
	if err != nil {
		t.Fatal(err)
	}
	token, err := store.CreateAPIToken("browser-source-"+owner, owner, scope, secret)
	if err != nil {
		t.Fatal(err)
	}
	return token.ID
}

func browserCSRFRouter(server *Server) http.Handler {
	router := chi.NewRouter()
	router.Use(server.securityHeadersMiddleware)
	router.Use(server.sessionMiddleware)
	router.Use(server.basicAuthMiddleware)
	server.mountBrowserUI(router)
	return router
}

func onlyBrowserSessionCookie(t *testing.T, response *http.Response) *http.Cookie {
	t.Helper()
	var found *http.Cookie
	for _, cookie := range response.Cookies() {
		if cookie.Name != sessionCookieName {
			continue
		}
		if found != nil {
			t.Fatalf("response issued duplicate %s cookies", sessionCookieName)
		}
		found = cookie
	}
	if found == nil {
		t.Fatalf("response did not issue %s cookie", sessionCookieName)
	}
	return found
}

func countNamedCookies(response *http.Response, name string) int {
	count := 0
	for _, cookie := range response.Cookies() {
		if cookie.Name == name {
			count++
		}
	}
	return count
}

func authSessionGaugeValue(t *testing.T) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() == "buckley_web_sessions_total" && len(family.Metric) == 1 {
			return family.Metric[0].GetGauge().GetValue()
		}
	}
	t.Fatal("buckley_web_sessions_total metric not found")
	return 0
}

func assertGoSXNavigationCSP(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	body := response.Body.String()
	marker := `data-gosx-navigation="true" nonce="`
	start := strings.Index(body, marker)
	if start < 0 {
		t.Fatal("GoSX progressive navigation script or nonce missing")
	}
	start += len(marker)
	end := strings.Index(body[start:], `"`)
	if end <= 0 {
		t.Fatal("GoSX navigation nonce is malformed")
	}
	nonce := body[start : start+end]
	csp := response.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "'nonce-"+nonce+"'") || !strings.Contains(csp, "form-action 'self'") {
		t.Fatalf("CSP %q does not authorize the exact navigation nonce", csp)
	}
	if response.Header().Get("X-Frame-Options") != "DENY" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("security headers missing: %+v", response.Header())
	}
}
