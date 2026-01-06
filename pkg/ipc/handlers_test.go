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

// testServer creates a test server with a temporary database.
func testServer(t *testing.T) (*Server, *storage.Store) {
	t.Helper()
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "buckley.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	planStore := orchestrator.NewFilePlanStore(filepath.Join(tmpDir, "plans"))
	server := NewServer(
		Config{
			ProjectRoot:    tmpDir,
			AllowedOrigins: []string{"*"},
		},
		store,
		nil,
		command.NewGateway(),
		planStore,
		&config.Config{},
		nil,
		nil,
	)
	return server, store
}

// withPrincipal adds a principal to the request context.
func withPrincipal(r *http.Request, name, scope string) *http.Request {
	ctx := context.WithValue(r.Context(), principalContextKey, &requestPrincipal{
		Name:  name,
		Scope: scope,
	})
	return r.WithContext(ctx)
}

// withURLParam adds a chi URL parameter to the request.
func withURLParam(r *http.Request, key, value string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, value)
	ctx := context.WithValue(r.Context(), chi.RouteCtxKey, rctx)
	return r.WithContext(ctx)
}

// =============================================================================
// API Token Handler Tests
// =============================================================================

func TestHandleListAPITokens_Success(t *testing.T) {
	server, store := testServer(t)

	// Create a token to list
	_, err := store.CreateAPIToken("test-token", "owner", storage.TokenScopeOperator, "secret123")
	if err != nil {
		t.Fatalf("failed to create token: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/config/api-tokens", nil)
	req = withPrincipal(req, "admin", storage.TokenScopeOperator)
	rr := httptest.NewRecorder()

	server.handleListAPITokens(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var resp struct {
		Tokens []storage.APIToken `json:"tokens"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp.Tokens) != 1 {
		t.Errorf("expected 1 token, got %d", len(resp.Tokens))
	}
	if resp.Tokens[0].Name != "test-token" {
		t.Errorf("expected token name 'test-token', got %s", resp.Tokens[0].Name)
	}
}

func TestHandleListAPITokens_ForbiddenForViewer(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/config/api-tokens", nil)
	req = withPrincipal(req, "viewer", storage.TokenScopeViewer)
	rr := httptest.NewRecorder()

	server.handleListAPITokens(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected status %d, got %d", http.StatusForbidden, rr.Code)
	}
}

func TestHandleListAPITokens_ForbiddenForMember(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/config/api-tokens", nil)
	req = withPrincipal(req, "member", storage.TokenScopeMember)
	rr := httptest.NewRecorder()

	server.handleListAPITokens(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected status %d, got %d", http.StatusForbidden, rr.Code)
	}
}

func TestHandleListAPITokens_Unauthorized(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/config/api-tokens", nil)
	// No principal attached
	rr := httptest.NewRecorder()

	server.handleListAPITokens(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, rr.Code)
	}
}

func TestHandleListAPITokens_NilStore(t *testing.T) {
	server := &Server{}

	req := httptest.NewRequest(http.MethodGet, "/api/config/api-tokens", nil)
	req = withPrincipal(req, "admin", storage.TokenScopeOperator)
	rr := httptest.NewRecorder()

	server.handleListAPITokens(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status %d, got %d", http.StatusServiceUnavailable, rr.Code)
	}
}

func TestHandleCreateAPIToken_Success(t *testing.T) {
	server, _ := testServer(t)

	body := strings.NewReader(`{"name":"new-token","owner":"alice","scope":"viewer"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/config/api-tokens", body)
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "admin", storage.TokenScopeOperator)
	rr := httptest.NewRecorder()

	server.handleCreateAPIToken(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var resp struct {
		Token  string             `json:"token"`
		Record storage.APIToken   `json:"record"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Token == "" {
		t.Error("expected non-empty token secret")
	}
	if resp.Record.Name != "new-token" {
		t.Errorf("expected name 'new-token', got %s", resp.Record.Name)
	}
	if resp.Record.Owner != "alice" {
		t.Errorf("expected owner 'alice', got %s", resp.Record.Owner)
	}
}

func TestHandleCreateAPIToken_ForbiddenForViewer(t *testing.T) {
	server, _ := testServer(t)

	body := strings.NewReader(`{"name":"test","owner":"test","scope":"viewer"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/config/api-tokens", body)
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "viewer", storage.TokenScopeViewer)
	rr := httptest.NewRecorder()

	server.handleCreateAPIToken(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected status %d, got %d", http.StatusForbidden, rr.Code)
	}
}

func TestHandleCreateAPIToken_InvalidJSON(t *testing.T) {
	server, _ := testServer(t)

	body := strings.NewReader(`{invalid json}`)
	req := httptest.NewRequest(http.MethodPost, "/api/config/api-tokens", body)
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "admin", storage.TokenScopeOperator)
	rr := httptest.NewRecorder()

	server.handleCreateAPIToken(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rr.Code)
	}
}

func TestHandleCreateAPIToken_NilStore(t *testing.T) {
	server := &Server{}

	body := strings.NewReader(`{"name":"test","owner":"test","scope":"viewer"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/config/api-tokens", body)
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "admin", storage.TokenScopeOperator)
	rr := httptest.NewRecorder()

	server.handleCreateAPIToken(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status %d, got %d", http.StatusServiceUnavailable, rr.Code)
	}
}

func TestHandleRevokeAPIToken_Success(t *testing.T) {
	server, store := testServer(t)

	// Create a token to revoke
	record, err := store.CreateAPIToken("to-revoke", "owner", storage.TokenScopeOperator, "secret123")
	if err != nil {
		t.Fatalf("failed to create token: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/config/api-tokens/"+record.ID, nil)
	req = withPrincipal(req, "admin", storage.TokenScopeOperator)
	req = withURLParam(req, "tokenID", record.ID)
	rr := httptest.NewRecorder()

	server.handleRevokeAPIToken(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("expected status %d, got %d: %s", http.StatusNoContent, rr.Code, rr.Body.String())
	}

	// Verify token was revoked
	tokens, _ := store.ListAPITokens()
	for _, tok := range tokens {
		if tok.ID == record.ID && !tok.Revoked {
			t.Error("expected token to be revoked")
		}
	}
}

func TestHandleRevokeAPIToken_MissingTokenID(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/config/api-tokens/", nil)
	req = withPrincipal(req, "admin", storage.TokenScopeOperator)
	req = withURLParam(req, "tokenID", "")
	rr := httptest.NewRecorder()

	server.handleRevokeAPIToken(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rr.Code)
	}
}

func TestHandleRevokeAPIToken_ForbiddenForViewer(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/config/api-tokens/some-id", nil)
	req = withPrincipal(req, "viewer", storage.TokenScopeViewer)
	req = withURLParam(req, "tokenID", "some-id")
	rr := httptest.NewRecorder()

	server.handleRevokeAPIToken(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected status %d, got %d", http.StatusForbidden, rr.Code)
	}
}

func TestHandleRevokeAPIToken_NilStore(t *testing.T) {
	server := &Server{}

	req := httptest.NewRequest(http.MethodDelete, "/api/config/api-tokens/some-id", nil)
	req = withPrincipal(req, "admin", storage.TokenScopeOperator)
	req = withURLParam(req, "tokenID", "some-id")
	rr := httptest.NewRecorder()

	server.handleRevokeAPIToken(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status %d, got %d", http.StatusServiceUnavailable, rr.Code)
	}
}

// =============================================================================
// Settings Handler Tests
// =============================================================================

func TestHandleListSettings_Success(t *testing.T) {
	server, store := testServer(t)

	// Set a value first
	if err := store.SetSetting("remote.base_url", "https://example.com"); err != nil {
		t.Fatalf("failed to set setting: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/config/settings", nil)
	req = withPrincipal(req, "admin", storage.TokenScopeOperator)
	rr := httptest.NewRecorder()

	server.handleListSettings(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var resp struct {
		Settings map[string]string `json:"settings"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Settings["remote.base_url"] != "https://example.com" {
		t.Errorf("expected setting value 'https://example.com', got %s", resp.Settings["remote.base_url"])
	}
}

func TestHandleListSettings_ForbiddenForViewer(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/config/settings", nil)
	req = withPrincipal(req, "viewer", storage.TokenScopeViewer)
	rr := httptest.NewRecorder()

	server.handleListSettings(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected status %d, got %d", http.StatusForbidden, rr.Code)
	}
}

func TestHandleListSettings_NilStore(t *testing.T) {
	server := &Server{}

	req := httptest.NewRequest(http.MethodGet, "/api/config/settings", nil)
	req = withPrincipal(req, "admin", storage.TokenScopeOperator)
	rr := httptest.NewRecorder()

	server.handleListSettings(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status %d, got %d", http.StatusServiceUnavailable, rr.Code)
	}
}

func TestHandleUpdateSetting_Success(t *testing.T) {
	server, store := testServer(t)

	body := strings.NewReader(`{"value":"https://new-url.com"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/config/settings/remote.base_url", body)
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "admin", storage.TokenScopeOperator)
	req = withURLParam(req, "key", "remote.base_url")
	rr := httptest.NewRecorder()

	server.handleUpdateSetting(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	// Verify the setting was updated
	values, err := store.GetSettings([]string{"remote.base_url"})
	if err != nil {
		t.Fatalf("failed to get settings: %v", err)
	}
	if values["remote.base_url"] != "https://new-url.com" {
		t.Errorf("expected value 'https://new-url.com', got %s", values["remote.base_url"])
	}
}

func TestHandleUpdateSetting_InvalidKey(t *testing.T) {
	server, _ := testServer(t)

	body := strings.NewReader(`{"value":"test"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/config/settings/invalid.key", body)
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "admin", storage.TokenScopeOperator)
	req = withURLParam(req, "key", "invalid.key")
	rr := httptest.NewRecorder()

	server.handleUpdateSetting(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rr.Code)
	}
}

func TestHandleUpdateSetting_ForbiddenForViewer(t *testing.T) {
	server, _ := testServer(t)

	body := strings.NewReader(`{"value":"test"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/config/settings/remote.base_url", body)
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "viewer", storage.TokenScopeViewer)
	req = withURLParam(req, "key", "remote.base_url")
	rr := httptest.NewRecorder()

	server.handleUpdateSetting(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected status %d, got %d", http.StatusForbidden, rr.Code)
	}
}

func TestHandleUpdateSetting_InvalidJSON(t *testing.T) {
	server, _ := testServer(t)

	body := strings.NewReader(`{invalid}`)
	req := httptest.NewRequest(http.MethodPut, "/api/config/settings/remote.base_url", body)
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "admin", storage.TokenScopeOperator)
	req = withURLParam(req, "key", "remote.base_url")
	rr := httptest.NewRecorder()

	server.handleUpdateSetting(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rr.Code)
	}
}

func TestHandleUpdateSetting_NilStore(t *testing.T) {
	server := &Server{}

	body := strings.NewReader(`{"value":"test"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/config/settings/remote.base_url", body)
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "admin", storage.TokenScopeOperator)
	req = withURLParam(req, "key", "remote.base_url")
	rr := httptest.NewRecorder()

	server.handleUpdateSetting(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status %d, got %d", http.StatusServiceUnavailable, rr.Code)
	}
}

// =============================================================================
// Audit Log Handler Tests
// =============================================================================

func TestHandleListAuditLogs_Success(t *testing.T) {
	server, store := testServer(t)

	// Create some audit entries
	if err := store.RecordAuditLog("admin", "operator", "api_token.create", map[string]any{"name": "test"}); err != nil {
		t.Fatalf("failed to record audit log: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/config/audit-logs", nil)
	req = withPrincipal(req, "admin", storage.TokenScopeOperator)
	rr := httptest.NewRecorder()

	server.handleListAuditLogs(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var resp struct {
		Audit []map[string]any `json:"audit"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp.Audit) < 1 {
		t.Error("expected at least 1 audit entry")
	}
}

func TestHandleListAuditLogs_WithLimit(t *testing.T) {
	server, store := testServer(t)

	// Create multiple audit entries
	for i := 0; i < 5; i++ {
		if err := store.RecordAuditLog("admin", "operator", "test.action", nil); err != nil {
			t.Fatalf("failed to record audit log: %v", err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/config/audit-logs?limit=2", nil)
	req = withPrincipal(req, "admin", storage.TokenScopeOperator)
	rr := httptest.NewRecorder()

	server.handleListAuditLogs(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rr.Code)
	}

	var resp struct {
		Audit []map[string]any `json:"audit"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp.Audit) > 2 {
		t.Errorf("expected at most 2 entries, got %d", len(resp.Audit))
	}
}

func TestHandleListAuditLogs_ForbiddenForViewer(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/config/audit-logs", nil)
	req = withPrincipal(req, "viewer", storage.TokenScopeViewer)
	rr := httptest.NewRecorder()

	server.handleListAuditLogs(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected status %d, got %d", http.StatusForbidden, rr.Code)
	}
}

func TestHandleListAuditLogs_NilStore(t *testing.T) {
	server := &Server{}

	req := httptest.NewRequest(http.MethodGet, "/api/config/audit-logs", nil)
	req = withPrincipal(req, "admin", storage.TokenScopeOperator)
	rr := httptest.NewRecorder()

	server.handleListAuditLogs(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status %d, got %d", http.StatusServiceUnavailable, rr.Code)
	}
}

// =============================================================================
// Project Handler Tests
// =============================================================================

func TestHandleListProjects_Success(t *testing.T) {
	server, store := testServer(t)

	// Create a session for a project
	sess := &storage.Session{
		ID:         "test-session",
		Principal:  "test",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}
	if err := store.CreateSession(sess); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	req = withPrincipal(req, "test", storage.TokenScopeViewer)
	rr := httptest.NewRecorder()

	server.handleListProjects(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var resp struct {
		Projects []projectSummary `json:"projects"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
}

func TestHandleListProjects_Unauthorized(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	// No principal
	rr := httptest.NewRecorder()

	server.handleListProjects(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, rr.Code)
	}
}

func TestHandleListProjects_NilStore(t *testing.T) {
	server := &Server{}

	req := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	req = withPrincipal(req, "test", storage.TokenScopeViewer)
	rr := httptest.NewRecorder()

	server.handleListProjects(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status %d, got %d", http.StatusServiceUnavailable, rr.Code)
	}
}

func TestHandleCreateProject_ForbiddenForViewer(t *testing.T) {
	server, _ := testServer(t)

	body := strings.NewReader(`{"name":"my-project"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/projects", body)
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "viewer", storage.TokenScopeViewer)
	rr := httptest.NewRecorder()

	server.handleCreateProject(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected status %d, got %d", http.StatusForbidden, rr.Code)
	}
}

func TestHandleCreateProject_NilStore(t *testing.T) {
	server := &Server{}

	body := strings.NewReader(`{"name":"my-project"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/projects", body)
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "member", storage.TokenScopeMember)
	rr := httptest.NewRecorder()

	server.handleCreateProject(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status %d, got %d", http.StatusServiceUnavailable, rr.Code)
	}
}

func TestHandleCreateProject_NoProjectRoot(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "buckley.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	server := NewServer(
		Config{
			ProjectRoot: "", // Empty project root
		},
		store,
		nil,
		command.NewGateway(),
		nil,
		&config.Config{},
		nil,
		nil,
	)
	// Override projectRoot to empty to test the error case
	server.projectRoot = ""

	body := strings.NewReader(`{"name":"my-project"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/projects", body)
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "member", storage.TokenScopeMember)
	rr := httptest.NewRecorder()

	server.handleCreateProject(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d: %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
}

func TestHandleCreateProject_InvalidJSON(t *testing.T) {
	server, _ := testServer(t)

	body := strings.NewReader(`{invalid}`)
	req := httptest.NewRequest(http.MethodPost, "/api/projects", body)
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "member", storage.TokenScopeMember)
	rr := httptest.NewRecorder()

	server.handleCreateProject(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rr.Code)
	}
}

// =============================================================================
// Session Handler Tests
// =============================================================================

func TestHandleListSessions_Success(t *testing.T) {
	server, store := testServer(t)

	sess := &storage.Session{
		ID:         "test-session",
		Principal:  "test",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}
	if err := store.CreateSession(sess); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	req = withPrincipal(req, "test", storage.TokenScopeViewer)
	rr := httptest.NewRecorder()

	server.handleListSessions(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var resp struct {
		Sessions []storage.Session `json:"sessions"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp.Sessions) < 1 {
		t.Error("expected at least 1 session")
	}
}

func TestHandleListSessions_WithLimit(t *testing.T) {
	server, store := testServer(t)

	// Create multiple sessions
	for i := 0; i < 5; i++ {
		sess := &storage.Session{
			ID:         "session-" + string(rune('a'+i)),
			Principal:  "test",
			CreatedAt:  time.Now(),
			LastActive: time.Now(),
			Status:     storage.SessionStatusActive,
		}
		if err := store.CreateSession(sess); err != nil {
			t.Fatalf("failed to create session: %v", err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions?limit=2", nil)
	req = withPrincipal(req, "test", storage.TokenScopeViewer)
	rr := httptest.NewRecorder()

	server.handleListSessions(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
}

func TestHandleListSessions_Unauthorized(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	// No principal
	rr := httptest.NewRecorder()

	server.handleListSessions(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, rr.Code)
	}
}

func TestHandleSessionDetail_NotFound(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/nonexistent", nil)
	req = withPrincipal(req, "test", storage.TokenScopeViewer)
	req = withURLParam(req, "sessionID", "nonexistent")
	rr := httptest.NewRecorder()

	server.handleSessionDetail(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected status %d, got %d", http.StatusNotFound, rr.Code)
	}
}

func TestHandleSessionDetail_Unauthorized(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/test", nil)
	// No principal
	req = withURLParam(req, "sessionID", "test")
	rr := httptest.NewRecorder()

	server.handleSessionDetail(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, rr.Code)
	}
}

// =============================================================================
// Healthz Handler Tests
// =============================================================================

func TestHandleHealthz_Success(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()

	server.handleHealthz(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rr.Code)
	}

	var resp struct {
		Status string `json:"status"`
		Time   string `json:"time"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Status != "ok" {
		t.Errorf("expected status 'ok', got %s", resp.Status)
	}
}

func TestHandleHealthz_NilStore(t *testing.T) {
	server := &Server{}

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()

	server.handleHealthz(rr, req)

	// Should still return OK with nil store (skips DB check)
	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
}

// =============================================================================
// Cost Metrics Handler Tests
// =============================================================================

func TestHandleCostMetrics_Success(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/metrics/cost", nil)
	req = withPrincipal(req, "admin", storage.TokenScopeOperator)
	rr := httptest.NewRecorder()

	server.handleCostMetrics(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}
}

func TestHandleCostMetrics_ForbiddenForViewer(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/metrics/cost", nil)
	req = withPrincipal(req, "test", storage.TokenScopeViewer)
	rr := httptest.NewRecorder()

	server.handleCostMetrics(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected status %d, got %d", http.StatusForbidden, rr.Code)
	}
}

func TestHandleCostMetrics_Unauthorized(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/metrics/cost", nil)
	// No principal
	rr := httptest.NewRecorder()

	server.handleCostMetrics(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, rr.Code)
	}
}

// =============================================================================
// Utility Function Tests
// =============================================================================

func TestParseIntDefault(t *testing.T) {
	tests := []struct {
		raw      string
		def      int
		expected int
	}{
		{"", 10, 10},
		{"5", 10, 5},
		{"invalid", 10, 10},
		{"0", 10, 10},    // 0 is not > 0, so returns default
		{"-5", 10, 10},   // negative is not > 0
		{"100", 10, 100},
	}

	for _, tt := range tests {
		result := parseIntDefault(tt.raw, tt.def)
		if result != tt.expected {
			t.Errorf("parseIntDefault(%q, %d) = %d, want %d", tt.raw, tt.def, result, tt.expected)
		}
	}
}

func TestRequireScope_UnknownScope(t *testing.T) {
	_, _ = testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	ctx := context.WithValue(req.Context(), principalContextKey, &requestPrincipal{
		Name:  "test",
		Scope: "unknown", // Invalid scope
	})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	// Requires operator scope
	_, ok := requireScope(rr, req, storage.TokenScopeOperator)

	if ok {
		t.Error("expected requireScope to return false for unknown scope")
	}
	if rr.Code != http.StatusForbidden {
		t.Errorf("expected status %d, got %d", http.StatusForbidden, rr.Code)
	}
}

func TestRespondJSON(t *testing.T) {
	rr := httptest.NewRecorder()
	respondJSON(rr, map[string]string{"test": "value"})

	if rr.Header().Get("Content-Type") != "application/json" {
		t.Error("expected Content-Type to be application/json")
	}
	if rr.Header().Get("Cache-Control") != "no-store" {
		t.Error("expected Cache-Control to be no-store")
	}
}

func TestRespondError(t *testing.T) {
	rr := httptest.NewRecorder()
	respondError(rr, http.StatusBadRequest, nil)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rr.Code)
	}

	var resp struct {
		Status  int    `json:"status"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Status != http.StatusBadRequest {
		t.Errorf("expected status in body to be %d, got %d", http.StatusBadRequest, resp.Status)
	}
}

// =============================================================================
// Session Messages Handler Tests
// =============================================================================

func TestHandleSessionMessages_Success(t *testing.T) {
	server, store := testServer(t)

	sess := &storage.Session{
		ID:         "test-session",
		Principal:  "test",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}
	if err := store.CreateSession(sess); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/test-session/messages", nil)
	req = withPrincipal(req, "test", storage.TokenScopeViewer)
	req = withURLParam(req, "sessionID", "test-session")
	rr := httptest.NewRecorder()

	server.handleSessionMessages(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var resp struct {
		SessionID string `json:"sessionId"`
		Messages  []any  `json:"messages"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.SessionID != "test-session" {
		t.Errorf("expected sessionId 'test-session', got %s", resp.SessionID)
	}
}

func TestHandleSessionMessages_NotFound(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/nonexistent/messages", nil)
	req = withPrincipal(req, "test", storage.TokenScopeViewer)
	req = withURLParam(req, "sessionID", "nonexistent")
	rr := httptest.NewRecorder()

	server.handleSessionMessages(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected status %d, got %d", http.StatusNotFound, rr.Code)
	}
}

func TestHandleSessionMessages_Unauthorized(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/test/messages", nil)
	req = withURLParam(req, "sessionID", "test")
	rr := httptest.NewRecorder()

	server.handleSessionMessages(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, rr.Code)
	}
}

// =============================================================================
// Session Todos Handler Tests
// =============================================================================

func TestHandleSessionTodos_Success(t *testing.T) {
	server, store := testServer(t)

	sess := &storage.Session{
		ID:         "test-session",
		Principal:  "test",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}
	if err := store.CreateSession(sess); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/test-session/todos", nil)
	req = withPrincipal(req, "test", storage.TokenScopeViewer)
	req = withURLParam(req, "sessionID", "test-session")
	rr := httptest.NewRecorder()

	server.handleSessionTodos(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var resp struct {
		SessionID string `json:"sessionId"`
		Todos     []any  `json:"todos"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.SessionID != "test-session" {
		t.Errorf("expected sessionId 'test-session', got %s", resp.SessionID)
	}
}

func TestHandleSessionTodos_NotFound(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/nonexistent/todos", nil)
	req = withPrincipal(req, "test", storage.TokenScopeViewer)
	req = withURLParam(req, "sessionID", "nonexistent")
	rr := httptest.NewRecorder()

	server.handleSessionTodos(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected status %d, got %d", http.StatusNotFound, rr.Code)
	}
}

// =============================================================================
// Session Skills Handler Tests
// =============================================================================

func TestHandleSessionSkills_Success(t *testing.T) {
	server, store := testServer(t)

	sess := &storage.Session{
		ID:         "test-session",
		Principal:  "test",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}
	if err := store.CreateSession(sess); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/test-session/skills", nil)
	req = withPrincipal(req, "test", storage.TokenScopeViewer)
	req = withURLParam(req, "sessionID", "test-session")
	rr := httptest.NewRecorder()

	server.handleSessionSkills(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var resp struct {
		SessionID string `json:"sessionId"`
		Skills    []any  `json:"skills"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.SessionID != "test-session" {
		t.Errorf("expected sessionId 'test-session', got %s", resp.SessionID)
	}
}

func TestHandleSessionSkills_NotFound(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/nonexistent/skills", nil)
	req = withPrincipal(req, "test", storage.TokenScopeViewer)
	req = withURLParam(req, "sessionID", "nonexistent")
	rr := httptest.NewRecorder()

	server.handleSessionSkills(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected status %d, got %d", http.StatusNotFound, rr.Code)
	}
}

// =============================================================================
// Session Token Handler Tests
// =============================================================================

func TestHandleSessionToken_Success(t *testing.T) {
	server, store := testServer(t)

	sess := &storage.Session{
		ID:         "test-session",
		Principal:  "test",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}
	if err := store.CreateSession(sess); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/test-session/tokens", nil)
	req = withPrincipal(req, "test", storage.TokenScopeMember)
	req = withURLParam(req, "sessionID", "test-session")
	rr := httptest.NewRecorder()

	server.handleSessionToken(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var resp struct {
		SessionID string `json:"sessionId"`
		Token     string `json:"token"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Token == "" {
		t.Error("expected non-empty token")
	}
}

func TestHandleSessionToken_ForbiddenForViewer(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/test/tokens", nil)
	req = withPrincipal(req, "test", storage.TokenScopeViewer)
	req = withURLParam(req, "sessionID", "test")
	rr := httptest.NewRecorder()

	server.handleSessionToken(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected status %d, got %d", http.StatusForbidden, rr.Code)
	}
}

func TestHandleSessionToken_MissingSessionID(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions//tokens", nil)
	req = withPrincipal(req, "test", storage.TokenScopeMember)
	req = withURLParam(req, "sessionID", "")
	rr := httptest.NewRecorder()

	server.handleSessionToken(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rr.Code)
	}
}

func TestHandleSessionToken_NotFound(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/nonexistent/tokens", nil)
	req = withPrincipal(req, "test", storage.TokenScopeMember)
	req = withURLParam(req, "sessionID", "nonexistent")
	rr := httptest.NewRecorder()

	server.handleSessionToken(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected status %d, got %d", http.StatusNotFound, rr.Code)
	}
}

// =============================================================================
// Auth Session Handler Tests
// =============================================================================

func TestHandleAuthSession_Success(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/auth/session", nil)
	req = withPrincipal(req, "test", storage.TokenScopeOperator)
	rr := httptest.NewRecorder()

	server.handleAuthSession(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var resp struct {
		Principal map[string]any `json:"principal"`
		Session   map[string]any `json:"session"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Principal == nil {
		t.Error("expected principal in response")
	}
}

func TestHandleAuthSession_Unauthorized(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/auth/session", nil)
	// No principal
	rr := httptest.NewRecorder()

	server.handleAuthSession(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, rr.Code)
	}
}

func TestHandleAuthLogout_Success(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req = withPrincipal(req, "test", storage.TokenScopeOperator)
	rr := httptest.NewRecorder()

	server.handleAuthLogout(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("expected status %d, got %d", http.StatusNoContent, rr.Code)
	}
}

func TestHandleAuthLogout_Unauthorized(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	// No principal
	rr := httptest.NewRecorder()

	server.handleAuthLogout(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, rr.Code)
	}
}

// =============================================================================
// Plans Handler Tests
// =============================================================================

func TestHandleListPlans_Success(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/plans", nil)
	req = withPrincipal(req, "admin", storage.TokenScopeOperator)
	rr := httptest.NewRecorder()

	server.handleListPlans(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var resp struct {
		Plans []any `json:"plans"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
}

func TestHandleListPlans_Unauthorized(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/plans", nil)
	// No principal
	rr := httptest.NewRecorder()

	server.handleListPlans(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, rr.Code)
	}
}

func TestHandleListPlans_NilPlanStore(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "buckley.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	server := NewServer(
		Config{ProjectRoot: tmpDir},
		store,
		nil,
		command.NewGateway(),
		nil, // nil plan store
		&config.Config{},
		nil,
		nil,
	)
	server.planStore = nil // Force nil to test error path

	req := httptest.NewRequest(http.MethodGet, "/api/plans", nil)
	req = withPrincipal(req, "admin", storage.TokenScopeOperator)
	rr := httptest.NewRecorder()

	server.handleListPlans(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status %d, got %d", http.StatusServiceUnavailable, rr.Code)
	}
}

func TestHandleGetPlan_NotFound(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/plans/nonexistent", nil)
	req = withPrincipal(req, "admin", storage.TokenScopeOperator)
	req = withURLParam(req, "planID", "nonexistent")
	rr := httptest.NewRecorder()

	server.handleGetPlan(rr, req)

	// The planStore may return an error (500) or not found (404) depending on implementation
	if rr.Code != http.StatusNotFound && rr.Code != http.StatusInternalServerError {
		t.Errorf("expected status 404 or 500, got %d", rr.Code)
	}
}

func TestHandleGetPlan_Unauthorized(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/plans/test", nil)
	req = withURLParam(req, "planID", "test")
	rr := httptest.NewRecorder()

	server.handleGetPlan(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, rr.Code)
	}
}

// =============================================================================
// Prompts Handler Tests
// =============================================================================

func TestHandleListPrompts_Success(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/prompts", nil)
	req = withPrincipal(req, "admin", storage.TokenScopeOperator)
	rr := httptest.NewRecorder()

	server.handleListPrompts(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var resp struct {
		Prompts []any `json:"prompts"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
}

func TestHandleListPrompts_ForbiddenForViewer(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/prompts", nil)
	req = withPrincipal(req, "viewer", storage.TokenScopeViewer)
	rr := httptest.NewRecorder()

	server.handleListPrompts(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected status %d, got %d", http.StatusForbidden, rr.Code)
	}
}

func TestHandleGetPrompt_Success(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/prompts/commit", nil)
	req = withPrincipal(req, "admin", storage.TokenScopeOperator)
	req = withURLParam(req, "kind", "commit")
	rr := httptest.NewRecorder()

	server.handleGetPrompt(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}
}

func TestHandleGetPrompt_ForbiddenForViewer(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/prompts/system", nil)
	req = withPrincipal(req, "viewer", storage.TokenScopeViewer)
	req = withURLParam(req, "kind", "system")
	rr := httptest.NewRecorder()

	server.handleGetPrompt(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected status %d, got %d", http.StatusForbidden, rr.Code)
	}
}

func TestHandleGetPrompt_InvalidKind(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/prompts/invalid-kind", nil)
	req = withPrincipal(req, "admin", storage.TokenScopeOperator)
	req = withURLParam(req, "kind", "invalid-kind")
	rr := httptest.NewRecorder()

	server.handleGetPrompt(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rr.Code)
	}
}

func TestHandleUpdatePrompt_ForbiddenForViewer(t *testing.T) {
	server, _ := testServer(t)

	body := strings.NewReader(`{"content":"test content"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/prompts/system", body)
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "viewer", storage.TokenScopeViewer)
	req = withURLParam(req, "kind", "system")
	rr := httptest.NewRecorder()

	server.handleUpdatePrompt(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected status %d, got %d", http.StatusForbidden, rr.Code)
	}
}

func TestHandleDeletePrompt_ForbiddenForViewer(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/prompts/system", nil)
	req = withPrincipal(req, "viewer", storage.TokenScopeViewer)
	req = withURLParam(req, "kind", "system")
	rr := httptest.NewRecorder()

	server.handleDeletePrompt(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected status %d, got %d", http.StatusForbidden, rr.Code)
	}
}

// =============================================================================
// Personas Handler Tests
// =============================================================================

func TestHandleListPersonas_ForbiddenForViewer(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/personas", nil)
	req = withPrincipal(req, "viewer", storage.TokenScopeViewer)
	rr := httptest.NewRecorder()

	server.handleListPersonas(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected status %d, got %d", http.StatusForbidden, rr.Code)
	}
}

func TestHandleSetPersona_ForbiddenForViewer(t *testing.T) {
	server, _ := testServer(t)

	body := strings.NewReader(`{"phase":"planning","persona":"test"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/personas", body)
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "viewer", storage.TokenScopeViewer)
	rr := httptest.NewRecorder()

	server.handleSetPersona(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected status %d, got %d", http.StatusForbidden, rr.Code)
	}
}

// =============================================================================
// Project Sessions Handler Tests
// =============================================================================

func TestHandleProjectSessions_NotFound(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/projects/nonexistent/sessions", nil)
	req = withPrincipal(req, "test", storage.TokenScopeViewer)
	req = withURLParam(req, "project", "nonexistent")
	rr := httptest.NewRecorder()

	server.handleProjectSessions(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected status %d, got %d", http.StatusNotFound, rr.Code)
	}
}

func TestHandleProjectSessions_MissingSlug(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/projects//sessions", nil)
	req = withPrincipal(req, "test", storage.TokenScopeViewer)
	req = withURLParam(req, "project", "")
	rr := httptest.NewRecorder()

	server.handleProjectSessions(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rr.Code)
	}
}

func TestHandleProjectSessions_Unauthorized(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/projects/test/sessions", nil)
	req = withURLParam(req, "project", "test")
	rr := httptest.NewRecorder()

	server.handleProjectSessions(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, rr.Code)
	}
}

func TestHandleProjectSessions_NilStore(t *testing.T) {
	server := &Server{}

	req := httptest.NewRequest(http.MethodGet, "/api/projects/test/sessions", nil)
	req = withPrincipal(req, "test", storage.TokenScopeViewer)
	req = withURLParam(req, "project", "test")
	rr := httptest.NewRecorder()

	server.handleProjectSessions(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status %d, got %d", http.StatusServiceUnavailable, rr.Code)
	}
}

// =============================================================================
// Generate Asset Handler Tests
// =============================================================================

func TestHandleGenerateAsset_ForbiddenForViewer(t *testing.T) {
	server, _ := testServer(t)

	body := strings.NewReader(`{"kind":"agents"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/generate", body)
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "viewer", storage.TokenScopeViewer)
	rr := httptest.NewRecorder()

	server.handleGenerateAsset(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected status %d, got %d", http.StatusForbidden, rr.Code)
	}
}

func TestHandleGenerateAsset_MissingKind(t *testing.T) {
	server, _ := testServer(t)

	body := strings.NewReader(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/api/generate", body)
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "admin", storage.TokenScopeOperator)
	rr := httptest.NewRecorder()

	server.handleGenerateAsset(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rr.Code)
	}
}

func TestHandleGenerateAsset_InvalidJSON(t *testing.T) {
	server, _ := testServer(t)

	body := strings.NewReader(`{invalid}`)
	req := httptest.NewRequest(http.MethodPost, "/api/generate", body)
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "admin", storage.TokenScopeOperator)
	rr := httptest.NewRecorder()

	server.handleGenerateAsset(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rr.Code)
	}
}

func TestHandleGenerateAsset_UnsupportedKind(t *testing.T) {
	server, _ := testServer(t)

	body := strings.NewReader(`{"kind":"unknown"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/generate", body)
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "admin", storage.TokenScopeOperator)
	rr := httptest.NewRecorder()

	server.handleGenerateAsset(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rr.Code)
	}
}

// =============================================================================
// Session Command Handler Tests
// =============================================================================

func TestHandleSessionCommand_NilGateway(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "buckley.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	server := NewServer(
		Config{ProjectRoot: tmpDir},
		store,
		nil,
		nil, // nil gateway
		nil,
		&config.Config{},
		nil,
		nil,
	)
	server.commandGW = nil

	body := strings.NewReader(`{"content":"test"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/test/commands", body)
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "test", storage.TokenScopeMember)
	req = withURLParam(req, "sessionID", "test")
	rr := httptest.NewRecorder()

	server.handleSessionCommand(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status %d, got %d", http.StatusServiceUnavailable, rr.Code)
	}
}

func TestHandleSessionCommand_ForbiddenForViewer(t *testing.T) {
	server, _ := testServer(t)

	body := strings.NewReader(`{"content":"test"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/test/commands", body)
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "viewer", storage.TokenScopeViewer)
	req = withURLParam(req, "sessionID", "test")
	rr := httptest.NewRecorder()

	server.handleSessionCommand(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected status %d, got %d", http.StatusForbidden, rr.Code)
	}
}

// =============================================================================
// Plan Tasks Handler Tests
// =============================================================================

func TestHandleGetPlanTasks_Unauthorized(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/plans/test/tasks", nil)
	req = withURLParam(req, "planID", "test")
	rr := httptest.NewRecorder()

	server.handleGetPlanTasks(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, rr.Code)
	}
}

func TestHandleGetPlanTasks_NilPlanStore(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "buckley.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	server := NewServer(
		Config{ProjectRoot: tmpDir},
		store,
		nil,
		command.NewGateway(),
		nil,
		&config.Config{},
		nil,
		nil,
	)
	server.planStore = nil

	req := httptest.NewRequest(http.MethodGet, "/api/plans/test/tasks", nil)
	req = withPrincipal(req, "admin", storage.TokenScopeOperator)
	req = withURLParam(req, "planID", "test")
	rr := httptest.NewRecorder()

	server.handleGetPlanTasks(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status %d, got %d", http.StatusServiceUnavailable, rr.Code)
	}
}

// =============================================================================
// Plan Log Handler Tests
// =============================================================================

func TestHandlePlanLog_Unauthorized(t *testing.T) {
	server, _ := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/plans/test/logs/builder", nil)
	req = withURLParam(req, "planID", "test")
	req = withURLParam(req, "kind", "builder")
	rr := httptest.NewRecorder()

	server.handlePlanLog(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, rr.Code)
	}
}

// =============================================================================
// Additional Edge Case Tests
// =============================================================================

func TestPrincipalFromContext_NilContext(t *testing.T) {
	result := principalFromContext(nil)
	if result != nil {
		t.Error("expected nil principal for nil context")
	}
}

func TestRandomHex(t *testing.T) {
	// Test with valid size
	hex, err := randomHex(16)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hex) != 32 { // 16 bytes = 32 hex chars
		t.Errorf("expected 32 chars, got %d", len(hex))
	}

	// Test with zero size (defaults to 16)
	hex, err = randomHex(0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hex) != 32 {
		t.Errorf("expected 32 chars for default, got %d", len(hex))
	}
}

func TestExtractBearerToken(t *testing.T) {
	tests := []struct {
		name          string
		authHeader    string
		queryToken    string
		expectedToken string
		expectedQuery bool
	}{
		{
			name:          "bearer in header",
			authHeader:    "Bearer mytoken123",
			expectedToken: "mytoken123",
			expectedQuery: false,
		},
		{
			name:          "bearer in header with extra spaces",
			authHeader:    "Bearer   mytoken123  ",
			expectedToken: "mytoken123",
			expectedQuery: false,
		},
		{
			name:          "token in query",
			queryToken:    "querytoken",
			expectedToken: "querytoken",
			expectedQuery: true,
		},
		{
			name:          "no token",
			expectedToken: "",
			expectedQuery: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := "/test"
			if tt.queryToken != "" {
				url += "?token=" + tt.queryToken
			}
			req := httptest.NewRequest(http.MethodGet, url, nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}

			token, fromQuery := extractBearerToken(req)
			if token != tt.expectedToken {
				t.Errorf("expected token %q, got %q", tt.expectedToken, token)
			}
			if fromQuery != tt.expectedQuery {
				t.Errorf("expected fromQuery %v, got %v", tt.expectedQuery, fromQuery)
			}
		})
	}
}

// =============================================================================
// Workflow Action Handler Tests
// =============================================================================

func TestHandleWorkflowAction_Unauthorized(t *testing.T) {
	server, _ := testServer(t)

	body := strings.NewReader(`{"action":"status"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/workflow/test-session", body)
	req.Header.Set("Content-Type", "application/json")
	req = withURLParam(req, "sessionID", "test-session")
	rr := httptest.NewRecorder()

	server.handleWorkflowAction(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, rr.Code)
	}
}

func TestHandleWorkflowAction_ForbiddenForViewer(t *testing.T) {
	server, _ := testServer(t)

	body := strings.NewReader(`{"action":"status"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/workflow/test-session", body)
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "viewer", storage.TokenScopeViewer)
	req = withURLParam(req, "sessionID", "test-session")
	rr := httptest.NewRecorder()

	server.handleWorkflowAction(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected status %d, got %d", http.StatusForbidden, rr.Code)
	}
}

func TestHandleWorkflowAction_InvalidJSON(t *testing.T) {
	server, store := testServer(t)

	sess := &storage.Session{
		ID:         "test-session",
		Principal:  "test",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}
	if err := store.CreateSession(sess); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	token, err := server.issueSessionToken("test-session")
	if err != nil {
		t.Fatalf("failed to issue session token: %v", err)
	}

	body := strings.NewReader(`{invalid}`)
	req := httptest.NewRequest(http.MethodPost, "/api/workflow/test-session", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Buckley-Session-Token", token)
	req = withPrincipal(req, "test", storage.TokenScopeOperator)
	req = withURLParam(req, "sessionID", "test-session")
	rr := httptest.NewRecorder()

	server.handleWorkflowAction(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rr.Code)
	}
}

// =============================================================================
// Scheme and Security Helper Tests
// =============================================================================

func TestSchemeForRequest(t *testing.T) {
	tests := []struct {
		name       string
		forwarded  string
		tls        bool
		wantScheme string
	}{
		{"http default", "", false, "http"},
		{"forwarded https", "https", false, "https"},
		{"forwarded http", "http", false, "http"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.forwarded != "" {
				req.Header.Set("X-Forwarded-Proto", tt.forwarded)
			}
			scheme := schemeForRequest(req)
			if scheme != tt.wantScheme {
				t.Errorf("expected %q, got %q", tt.wantScheme, scheme)
			}
		})
	}
}

func TestIsRequestSecure(t *testing.T) {
	tests := []struct {
		name      string
		forwarded string
		wantBool  bool
	}{
		{"http", "", false},
		{"forwarded https", "https", true},
		{"forwarded http", "http", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.forwarded != "" {
				req.Header.Set("X-Forwarded-Proto", tt.forwarded)
			}
			secure := isRequestSecure(req)
			if secure != tt.wantBool {
				t.Errorf("expected %v, got %v", tt.wantBool, secure)
			}
		})
	}
}

func TestLoginURL(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "localhost:4488"

	url := loginURL(req, "ticket123")
	if url != "http://localhost:4488/cli/login/ticket123" {
		t.Errorf("unexpected url: %s", url)
	}
}

// =============================================================================
// Set Persona Handler Tests
// =============================================================================

func TestHandleSetPersona_InvalidJSON(t *testing.T) {
	server, _ := testServer(t)

	body := strings.NewReader(`{invalid}`)
	req := httptest.NewRequest(http.MethodPost, "/api/personas", body)
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "admin", storage.TokenScopeOperator)
	rr := httptest.NewRecorder()

	server.handleSetPersona(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rr.Code)
	}
}

func TestHandleSetPersona_UnknownPhase(t *testing.T) {
	server, _ := testServer(t)

	body := strings.NewReader(`{"phase":"invalid_phase","persona":"test"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/personas", body)
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "admin", storage.TokenScopeOperator)
	rr := httptest.NewRecorder()

	server.handleSetPersona(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rr.Code)
	}
}

// =============================================================================
// Update/Delete Prompts Tests
// =============================================================================

func TestHandleUpdatePrompt_InvalidJSON(t *testing.T) {
	server, _ := testServer(t)

	body := strings.NewReader(`{invalid}`)
	req := httptest.NewRequest(http.MethodPut, "/api/prompts/commit", body)
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "admin", storage.TokenScopeOperator)
	req = withURLParam(req, "kind", "commit")
	rr := httptest.NewRecorder()

	server.handleUpdatePrompt(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rr.Code)
	}
}

// =============================================================================
// Default Remediation Tests
// =============================================================================

func TestDefaultRemediation(t *testing.T) {
	tests := []struct {
		name   string
		code   string
		status int
	}{
		{"unauthorized", "", http.StatusUnauthorized},
		{"forbidden", "", http.StatusForbidden},
		{"not found", "", http.StatusNotFound},
		{"too many requests", "", http.StatusTooManyRequests},
		{"service unavailable", "", http.StatusServiceUnavailable},
		{"unknown error", "", http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := defaultRemediation(tt.code, tt.status)
			if len(result) == 0 {
				t.Error("expected at least one remediation step")
			}
		})
	}
}

// =============================================================================
// RateLimiter Tests
// =============================================================================

func TestRateLimiter(t *testing.T) {
	// Test nil limiter
	var nilLimiter *rateLimiter
	if !nilLimiter.Allow("key") {
		t.Error("nil limiter should always allow")
	}

	// Test rate limiting
	limiter := newRateLimiter(100 * time.Millisecond)
	if !limiter.Allow("key1") {
		t.Error("first request should be allowed")
	}
	if limiter.Allow("key1") {
		t.Error("immediate second request should be blocked")
	}

	// Different key should be allowed
	if !limiter.Allow("key2") {
		t.Error("different key should be allowed")
	}

	// Wait for interval
	time.Sleep(150 * time.Millisecond)
	if !limiter.Allow("key1") {
		t.Error("request after interval should be allowed")
	}
}

// =============================================================================
// Middleware Tests
// =============================================================================

func TestCORSMiddleware_Preflight(t *testing.T) {
	server, _ := testServer(t)

	handler := server.corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodOptions, "/api/test", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("expected status %d, got %d", http.StatusNoContent, rr.Code)
	}
	if rr.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Error("expected Access-Control-Allow-Methods header")
	}
}

func TestCORSMiddleware_WithOrigin(t *testing.T) {
	server, _ := testServer(t)

	handler := server.corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
}

func TestSecurityHeadersMiddleware(t *testing.T) {
	server, _ := testServer(t)

	handler := server.securityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("expected X-Content-Type-Options header")
	}
	if rr.Header().Get("X-Frame-Options") != "DENY" {
		t.Error("expected X-Frame-Options header")
	}
}

func TestAuthMiddleware_Unauthorized(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "buckley.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	server := NewServer(
		Config{
			ProjectRoot:  tmpDir,
			RequireToken: true,
		},
		store,
		nil,
		command.NewGateway(),
		nil,
		&config.Config{},
		nil,
		nil,
	)

	handler := server.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, rr.Code)
	}
}

func TestAuthMiddleware_Authorized(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "buckley.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	server := NewServer(
		Config{
			ProjectRoot: tmpDir,
			AuthToken:   "test-token",
		},
		store,
		nil,
		command.NewGateway(),
		nil,
		&config.Config{},
		nil,
		nil,
	)

	handler := server.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal := principalFromContext(r.Context())
		if principal == nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
}

// =============================================================================
// Origin Validation Tests
// =============================================================================

func TestIsOriginAllowed(t *testing.T) {
	// Create a server with specific allowed origins (not wildcard)
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "buckley.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	server := NewServer(
		Config{
			ProjectRoot:    tmpDir,
			AllowedOrigins: []string{"http://localhost", "http://127.0.0.1"},
		},
		store,
		nil,
		command.NewGateway(),
		nil,
		&config.Config{},
		nil,
		nil,
	)

	tests := []struct {
		name    string
		origin  string
		allowed bool
	}{
		{"empty origin", "", false},
		{"matching origin", "http://localhost", true},
		{"matching origin with port", "http://127.0.0.1:3000", true},
		{"non-matching origin", "http://evil.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			allowed, _ := server.isOriginAllowed(tt.origin)
			if allowed != tt.allowed {
				t.Errorf("expected allowed=%v, got %v", tt.allowed, allowed)
			}
		})
	}
}

func TestIsWebSocketOriginAllowed(t *testing.T) {
	server, _ := testServer(t)

	tests := []struct {
		name    string
		origin  string
		host    string
		allowed bool
	}{
		{"no origin", "", "localhost:4488", true},
		{"same host", "http://localhost:4488", "localhost:4488", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/ws", nil)
			req.Host = tt.host
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			allowed := server.isWebSocketOriginAllowed(req)
			if allowed != tt.allowed {
				t.Errorf("expected allowed=%v, got %v", tt.allowed, allowed)
			}
		})
	}
}

// =============================================================================
// Session Cookie Tests
// =============================================================================

func TestSetAndClearSessionCookie(t *testing.T) {
	server, _ := testServer(t)

	// Test setting cookie
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	server.setSessionCookie(rr, req, "test-token")

	cookies := rr.Result().Cookies()
	found := false
	for _, c := range cookies {
		if c.Name == sessionCookieName {
			found = true
			if c.Value != "test-token" {
				t.Errorf("expected cookie value 'test-token', got %s", c.Value)
			}
		}
	}
	if !found {
		t.Error("expected session cookie to be set")
	}

	// Test clearing cookie
	rr2 := httptest.NewRecorder()
	server.clearSessionCookie(rr2, req)

	cookies2 := rr2.Result().Cookies()
	for _, c := range cookies2 {
		if c.Name == sessionCookieName && c.MaxAge > 0 {
			t.Error("expected cookie to be cleared")
		}
	}
}

// =============================================================================
// Generate Asset Tests (additional cases)
// =============================================================================

func TestHandleGenerateAsset_SkillKind(t *testing.T) {
	server, _ := testServer(t)

	body := strings.NewReader(`{"kind":"skill","name":"test-skill"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/generate", body)
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "admin", storage.TokenScopeOperator)
	rr := httptest.NewRecorder()

	server.handleGenerateAsset(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}
}

func TestHandleGenerateAsset_PluginKind(t *testing.T) {
	server, _ := testServer(t)

	body := strings.NewReader(`{"kind":"plugin","name":"test-plugin"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/generate", body)
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "admin", storage.TokenScopeOperator)
	rr := httptest.NewRecorder()

	server.handleGenerateAsset(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}
}
