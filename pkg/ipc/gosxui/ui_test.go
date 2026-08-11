package gosxui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type testBackend struct {
	data       PageData
	start      StartWorkRequest
	command    CommandRequest
	logoutSeen bool
}

func (b *testBackend) Load(context.Context, *http.Request) (PageData, error) { return b.data, nil }
func (b *testBackend) StartWork(_ context.Context, _ *http.Request, req StartWorkRequest) (string, error) {
	b.start = req
	return "run-123", nil
}
func (b *testBackend) Dispatch(_ context.Context, _ *http.Request, req CommandRequest) error {
	b.command = req
	return nil
}
func (b *testBackend) Logout(context.Context, *http.Request, http.ResponseWriter) error {
	b.logoutSeen = true
	return nil
}

func TestNewHandlerRendersGoSXDocumentAndAsset(t *testing.T) {
	backend := &testBackend{data: PageData{
		Authenticated: true,
		PrincipalName: "operator",
		Scope:         "operator",
		CanWrite:      true,
		Workspaces: []WorkspaceView{{Path: "/tmp/repo", Label: "repo", Sessions: []SessionView{{
			ID: "run-1", Project: "/tmp/repo", Branch: "main", Status: "active",
		}}}},
		Current: &SessionView{ID: "run-1", Project: "/tmp/repo", Branch: "main", Status: "active"},
	}}
	handler := NewHandler(backend)

	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "http://localhost/", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("GET / status=%d", resp.Code)
	}
	body := resp.Body.String()
	for _, want := range []string{"Mission Control", "run-1", "/assets/mission-control.css", "data-gosx-navigation"} {
		if !strings.Contains(body, want) {
			t.Fatalf("rendered GoSX document missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "react") || strings.Contains(body, "index.js") {
		t.Fatalf("GoSX document still references the retired browser bundle")
	}

	asset := httptest.NewRecorder()
	handler.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, "http://localhost/assets/mission-control.css", nil))
	if asset.Code != http.StatusOK || !strings.Contains(asset.Body.String(), "--accent") {
		t.Fatalf("CSS asset was not served by GoSX: status=%d body=%q", asset.Code, asset.Body.String())
	}
}

func TestNewHandlerUsesNativeActions(t *testing.T) {
	backend := &testBackend{data: PageData{Authenticated: true, PrincipalName: "member", Scope: "member", CanWrite: true}}
	handler := NewHandler(backend)

	start := httptest.NewRecorder()
	startReq := httptest.NewRequest(http.MethodPost, "http://localhost/__actions/start-work", strings.NewReader("project=%2Ftmp%2Frepo&agent=planner&subagent=researcher&model=test-model&prompt=inspect"))
	startReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handler.ServeHTTP(start, startReq)
	if start.Code != http.StatusSeeOther || start.Header().Get("Location") != "/?session=run-123" {
		t.Fatalf("start action status=%d location=%q", start.Code, start.Header().Get("Location"))
	}
	if backend.start.Project != "/tmp/repo" || backend.start.Subagent != "researcher" {
		t.Fatalf("start action did not receive native form values: %#v", backend.start)
	}

	command := httptest.NewRecorder()
	commandReq := httptest.NewRequest(http.MethodPost, "http://localhost/__actions/command", strings.NewReader("session_id=run-123&type=input&content=continue"))
	commandReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handler.ServeHTTP(command, commandReq)
	if command.Code != http.StatusSeeOther || command.Header().Get("Location") != "/?session=run-123" {
		t.Fatalf("command action status=%d location=%q", command.Code, command.Header().Get("Location"))
	}
	if backend.command.SessionID != "run-123" || backend.command.Content != "continue" {
		t.Fatalf("command action did not receive native form values: %#v", backend.command)
	}
}

func TestNewHandlerRendersTokenGate(t *testing.T) {
	handler := NewHandler(&testBackend{data: PageData{RequireToken: true}})
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "http://localhost/", nil))
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "IPC token") {
		t.Fatalf("expected token gate, status=%d body=%q", resp.Code, resp.Body.String())
	}
}
