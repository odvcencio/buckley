package gosxui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
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

func TestNewHandlerRendersSharedAgentProjection(t *testing.T) {
	backend := &testBackend{data: PageData{
		Authenticated: true,
		PrincipalName: "member",
		Scope:         "member",
		CanWrite:      true,
		Current:       &SessionView{ID: "session-1", Project: "/tmp/repo", Status: "active"},
		AgentRuns: []AgentView{{
			ID:              "agent-parent",
			ParentID:        "session-1",
			ParentSessionID: "session-1",
			Agent:           "reviewer",
			Persona:         "review",
			Model:           "example/frontier",
			Status:          "running",
			Task:            "review the repository",
			Children: []AgentView{{
				ID:              "agent-child",
				ParentID:        "agent-parent",
				ParentSessionID: "session-1",
				Agent:           "researcher",
				Persona:         "research",
				Model:           "example/cheap",
				Status:          "completed",
				Task:            "trace call sites",
			}},
		}},
	}}
	handler := NewHandler(backend)

	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "http://localhost/?session=session-1", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("GET / status=%d", resp.Code)
	}
	body := resp.Body.String()
	for _, want := range []string{
		"Agent activity",
		`data-agent-id="agent-parent"`,
		`data-parent-id="session-1"`,
		"review · reviewer",
		"running · example/frontier · parent session-1",
		"review the repository",
		`data-agent-id="agent-child"`,
		`data-parent-id="agent-parent"`,
		"research · researcher",
		"completed · example/cheap · parent agent-parent",
		"trace call sites",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("agent projection missing %q: %s", want, body)
		}
	}
	if strings.Index(body, `data-agent-id="agent-parent"`) > strings.Index(body, `data-agent-id="agent-child"`) {
		t.Fatalf("child rendered before parent: %s", body)
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse rendered document: %v", err)
	}
	if _, checked := doc.Find("#side-toggle-agent-activity").Attr("checked"); !checked {
		t.Fatal("agent activity should start expanded on narrow layouts when runs exist")
	}
	if _, checked := doc.Find("#side-toggle-agent-catalog").Attr("checked"); checked {
		t.Fatal("agent catalog should start collapsed on narrow layouts")
	}
	if got := doc.Find("#side-toggle-agent-activity + label + .side-section-body [data-agent-id]").Length(); got != 2 {
		t.Fatalf("responsive agent activity contains %d runs, want 2", got)
	}
}

func TestMissionControlCSSKeepsNarrowAgentSectionsReachable(t *testing.T) {
	handler := NewHandler(&testBackend{data: PageData{Authenticated: true}})
	asset := httptest.NewRecorder()
	handler.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, "http://localhost/assets/mission-control.css", nil))
	if asset.Code != http.StatusOK {
		t.Fatalf("CSS status=%d", asset.Code)
	}
	css := asset.Body.String()
	for _, want := range []string{
		".status-running",
		".status-starting",
		".status-queued",
		".side-section-toggle { display: none; }",
		".side-section-toggle { display: block; height: 1px;",
		".side-section-toggle:not(:checked) ~ .side-section-body",
	} {
		if !strings.Contains(css, want) {
			t.Fatalf("responsive CSS missing %q", want)
		}
	}
	if strings.Contains(css, ".side-section:nth-child(n+2)") {
		t.Fatal("narrow CSS still removes the agent sections")
	}
}
