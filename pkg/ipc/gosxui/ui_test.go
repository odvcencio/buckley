package gosxui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
)

type testBackend struct {
	data        PageData
	start       StartWorkRequest
	command     CommandRequest
	logoutSeen  bool
	startCalls  int
	cmdCalls    int
	guardCalls  int
	guardToken  string
	guardErr    error
	loadErr     error
	startErr    error
	dispatchErr error
	logoutErr   error
}

func (b *testBackend) Load(context.Context, *http.Request) (PageData, error) {
	return b.data, b.loadErr
}
func (b *testBackend) StartWork(_ context.Context, _ *http.Request, req StartWorkRequest) (string, error) {
	b.startCalls++
	b.start = req
	return "run-123", b.startErr
}
func (b *testBackend) Dispatch(_ context.Context, _ *http.Request, req CommandRequest) error {
	b.cmdCalls++
	b.command = req
	return b.dispatchErr
}
func (b *testBackend) Logout(context.Context, *http.Request, http.ResponseWriter) error {
	b.logoutSeen = true
	return b.logoutErr
}
func (b *testBackend) ValidateMutation(_ context.Context, _ *http.Request, token string) error {
	b.guardCalls++
	b.guardToken = token
	return b.guardErr
}

type unguardedBackend struct {
	inner *testBackend
}

func (b unguardedBackend) Load(ctx context.Context, r *http.Request) (PageData, error) {
	return b.inner.Load(ctx, r)
}
func (b unguardedBackend) StartWork(ctx context.Context, r *http.Request, req StartWorkRequest) (string, error) {
	return b.inner.StartWork(ctx, r, req)
}
func (b unguardedBackend) Dispatch(ctx context.Context, r *http.Request, req CommandRequest) error {
	return b.inner.Dispatch(ctx, r, req)
}
func (b unguardedBackend) Logout(ctx context.Context, r *http.Request, w http.ResponseWriter) error {
	return b.inner.Logout(ctx, r, w)
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
	if resp.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("dynamic page cache control = %q", resp.Header().Get("Cache-Control"))
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
	if asset.Header().Get("Cache-Control") != "public, max-age=3600" {
		t.Fatalf("CSS cache control = %q", asset.Header().Get("Cache-Control"))
	}
}

func TestNewHandlerUsesNativeActions(t *testing.T) {
	backend := &testBackend{data: PageData{Authenticated: true, PrincipalName: "member", Scope: "member", CanWrite: true, CSRFToken: "csrf-token"}}
	handler := NewHandler(backend)

	start := httptest.NewRecorder()
	startForm := url.Values{"_csrf": {"csrf-token"}, "project": {"/tmp/repo"}, "agent": {"planner"}, "subagent": {"researcher"}, "model": {"test-model"}, "prompt": {"inspect"}}
	startReq := httptest.NewRequest(http.MethodPost, "http://localhost/__actions/start-work", strings.NewReader(startForm.Encode()))
	startReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handler.ServeHTTP(start, startReq)
	if start.Code != http.StatusSeeOther || start.Header().Get("Location") != "/?session=run-123" {
		t.Fatalf("start action status=%d location=%q", start.Code, start.Header().Get("Location"))
	}
	if start.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("successful action cache control = %q", start.Header().Get("Cache-Control"))
	}
	if backend.start.Project != "/tmp/repo" || backend.start.Subagent != "researcher" {
		t.Fatalf("start action did not receive native form values: %#v", backend.start)
	}

	command := httptest.NewRecorder()
	commandForm := url.Values{"_csrf": {"csrf-token"}, "session_id": {"run-123"}, "type": {"input"}, "content": {"continue"}}
	commandReq := httptest.NewRequest(http.MethodPost, "http://localhost/__actions/command", strings.NewReader(commandForm.Encode()))
	commandReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handler.ServeHTTP(command, commandReq)
	if command.Code != http.StatusSeeOther || command.Header().Get("Location") != "/?session=run-123" {
		t.Fatalf("command action status=%d location=%q", command.Code, command.Header().Get("Location"))
	}
	if backend.command.SessionID != "run-123" || backend.command.Content != "continue" {
		t.Fatalf("command action did not receive native form values: %#v", backend.command)
	}
	approval := httptest.NewRecorder()
	approvalForm := url.Values{"_csrf": {"csrf-token"}, "session_id": {"run-123"}, "type": {"approval"}, "content": {"approve"}, "approval_id": {"approval-123"}}
	approvalReq := httptest.NewRequest(http.MethodPost, "http://localhost/__actions/command", strings.NewReader(approvalForm.Encode()))
	approvalReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handler.ServeHTTP(approval, approvalReq)
	if approval.Code != http.StatusSeeOther || backend.command.Type != "approval" || backend.command.Content != "approve" || backend.command.ApprovalID != "approval-123" {
		t.Fatalf("approval action status=%d command=%#v", approval.Code, backend.command)
	}

	logout := httptest.NewRecorder()
	logoutReq := httptest.NewRequest(http.MethodPost, "http://localhost/__actions/logout", strings.NewReader(url.Values{"_csrf": {"csrf-token"}}.Encode()))
	logoutReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handler.ServeHTTP(logout, logoutReq)
	if logout.Code != http.StatusSeeOther || logout.Header().Get("Location") != "/" || !backend.logoutSeen {
		t.Fatalf("logout action status=%d location=%q seen=%v", logout.Code, logout.Header().Get("Location"), backend.logoutSeen)
	}
	if backend.guardCalls != 4 || backend.guardToken != "csrf-token" || backend.startCalls != 1 || backend.cmdCalls != 2 {
		t.Fatalf("guard/action calls = %d/%d/%d token=%q", backend.guardCalls, backend.startCalls, backend.cmdCalls, backend.guardToken)
	}
}

func TestNewHandlerFailsClosedForInvalidMutationBoundary(t *testing.T) {
	tests := []struct {
		name       string
		target     string
		form       url.Values
		query      string
		guardErr   error
		unguarded  bool
		wantGuards int
	}{
		{name: "guard absent", target: startActionPath, form: url.Values{"_csrf": {"csrf-token"}}, unguarded: true},
		{name: "missing token", target: startActionPath, form: url.Values{}},
		{name: "duplicate token", target: startActionPath, form: url.Values{"_csrf": {"one", "two"}}},
		{name: "query duplicate", target: startActionPath, query: "?_csrf=query", form: url.Values{"_csrf": {"csrf-token"}}},
		{name: "guard rejected", target: startActionPath, form: url.Values{"_csrf": {"malformed"}}, guardErr: errors.New("rejected"), wantGuards: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := &testBackend{guardErr: test.guardErr}
			var handler http.Handler = NewHandler(backend)
			if test.unguarded {
				handler = NewHandler(unguardedBackend{inner: backend})
			}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "http://localhost"+test.target+test.query, strings.NewReader(test.form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden || backend.startCalls != 0 || backend.cmdCalls != 0 || backend.logoutSeen || backend.guardCalls != test.wantGuards {
				t.Fatalf("status=%d calls start/command/logout/guard=%d/%d/%v/%d", rec.Code, backend.startCalls, backend.cmdCalls, backend.logoutSeen, backend.guardCalls)
			}
		})
	}
}

func TestActionHandlerBoundsBodyAndSanitizesFailures(t *testing.T) {
	backend := &testBackend{startErr: errors.New("storage password=secret")}
	handler := NewHandler(backend)

	tooLarge := httptest.NewRecorder()
	largeBody := "_csrf=csrf-token&project=" + strings.Repeat("x", int(maxActionFormSize))
	largeReq := httptest.NewRequest(http.MethodPost, "http://localhost"+startActionPath, strings.NewReader(largeBody))
	largeReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handler.ServeHTTP(tooLarge, largeReq)
	if tooLarge.Code != http.StatusRequestEntityTooLarge || backend.guardCalls != 0 || backend.startCalls != 0 {
		t.Fatalf("oversized action status=%d guard/start=%d/%d", tooLarge.Code, backend.guardCalls, backend.startCalls)
	}

	failure := httptest.NewRecorder()
	failureReq := httptest.NewRequest(http.MethodPost, "http://localhost"+startActionPath, strings.NewReader(url.Values{"_csrf": {"csrf-token"}, "project": {"/tmp/repo"}}.Encode()))
	failureReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handler.ServeHTTP(failure, failureReq)
	if failure.Code != http.StatusSeeOther || failure.Header().Get("Location") != "/?error=action+failed" {
		t.Fatalf("failed action status=%d location=%q", failure.Code, failure.Header().Get("Location"))
	}
	if failure.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("failed action cache control = %q", failure.Header().Get("Cache-Control"))
	}
	if strings.Contains(failure.Header().Get("Location"), "secret") || strings.Contains(failure.Body.String(), "secret") {
		t.Fatal("raw backend error was reflected")
	}

	external := actionHandler(backend, func(context.Context, *http.Request, http.ResponseWriter, url.Values) (string, error) {
		return "https://evil.example/phish", nil
	})
	externalRec := httptest.NewRecorder()
	externalReq := httptest.NewRequest(http.MethodPost, "http://localhost/action", strings.NewReader(url.Values{"_csrf": {"csrf-token"}}.Encode()))
	externalReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	external.ServeHTTP(externalRec, externalReq)
	if externalRec.Code != http.StatusSeeOther || externalRec.Header().Get("Location") != "/" {
		t.Fatalf("external action redirect status=%d location=%q", externalRec.Code, externalRec.Header().Get("Location"))
	}
}

func TestNewHandlerSanitizesPageErrors(t *testing.T) {
	backendFailure := &testBackend{loadErr: errors.New("storage password=secret")}
	rec := httptest.NewRecorder()
	NewHandler(backendFailure).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://localhost/", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), safeLoadFailure) || strings.Contains(rec.Body.String(), "password=secret") {
		t.Fatalf("load failure status=%d leaked=%v", rec.Code, strings.Contains(rec.Body.String(), "password=secret"))
	}

	safe := httptest.NewRecorder()
	NewHandler(&testBackend{}).ServeHTTP(safe, httptest.NewRequest(http.MethodGet, "http://localhost/?error=action+failed", nil))
	if safe.Code != http.StatusOK || !strings.Contains(safe.Body.String(), safeActionFailure) {
		t.Fatalf("safe action failure status=%d", safe.Code)
	}

	arbitrary := httptest.NewRecorder()
	NewHandler(&testBackend{}).ServeHTTP(arbitrary, httptest.NewRequest(http.MethodGet, "http://localhost/?error=attacker-controlled", nil))
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(arbitrary.Body.String()))
	if err != nil {
		t.Fatal(err)
	}
	if notices := doc.Find(".notice-error"); notices.Length() != 0 {
		t.Fatalf("arbitrary query error rendered %d notices: %q", notices.Length(), notices.Text())
	}
}

func TestRenderedUnsafeFormsCarryExactlyOneCSRFToken(t *testing.T) {
	backend := &testBackend{data: PageData{
		Authenticated: true, PrincipalName: "operator", Scope: "operator", CanWrite: true, CSRFToken: "csrf-token",
		Current:   &SessionView{ID: "run-1", Project: "/tmp/repo", Status: "active"},
		Approvals: []ApprovalView{{ID: "approval-1", ToolName: "exec", ToolArgs: "{}", ExpiresAt: "later"}},
	}}
	rec := httptest.NewRecorder()
	NewHandler(backend).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://localhost/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status=%d", rec.Code)
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(rec.Body.String()))
	if err != nil {
		t.Fatal(err)
	}
	forms := doc.Find("form")
	if forms.Length() == 0 {
		t.Fatal("no unsafe forms rendered")
	}
	forms.Each(func(i int, form *goquery.Selection) {
		fields := form.Find(`input[name="_csrf"]`)
		if fields.Length() != 1 {
			t.Errorf("form %d has %d CSRF fields", i, fields.Length())
			return
		}
		if value, _ := fields.Attr("value"); value != "csrf-token" {
			t.Errorf("form %d CSRF value=%q", i, value)
		}
	})
	approvalForms := doc.Find(`form:has(input[name="type"][value="approval"])`)
	if approvalForms.Length() != 2 {
		t.Fatalf("approval forms=%d want 2", approvalForms.Length())
	}
	decisions := map[string]bool{"approve": false, "reject": false}
	approvalForms.Each(func(i int, form *goquery.Selection) {
		approvalFields := form.Find(`input[name="approval_id"]`)
		if approvalFields.Length() != 1 {
			t.Errorf("approval form %d has %d approval IDs", i, approvalFields.Length())
			return
		}
		if value, _ := approvalFields.Attr("value"); value != "approval-1" {
			t.Errorf("approval form %d ID=%q", i, value)
		}
		decision, _ := form.Find(`input[name="content"]`).Attr("value")
		if _, ok := decisions[decision]; !ok {
			t.Errorf("approval form %d decision=%q", i, decision)
			return
		}
		decisions[decision] = true
	})
	if !decisions["approve"] || !decisions["reject"] {
		t.Fatalf("approval decisions=%v", decisions)
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
