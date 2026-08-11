// Package gosxui contains Buckley's browser surface.
//
// The UI is deliberately server-first: GoSX renders the complete Mission
// Control document, while Buckley's IPC server remains the authority for
// authentication, sessions, agent profiles, commands, approvals, and
// telemetry. There is no second browser-side agent runtime to drift from the
// daemon.
package gosxui

import (
	"context"
	"embed"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"m31labs.dev/gosx"
	"m31labs.dev/gosx/server"
)

//go:embed styles.css
var styles embed.FS

const (
	startActionPath   = "/__actions/start-work"
	commandActionPath = "/__actions/command"
	logoutActionPath  = "/__actions/logout"
)

// Backend is the narrow port between GoSX and Buckley's IPC/domain layers.
// Implementations own authorization and persistence; this package only maps
// their result into a server-rendered document and native HTML actions.
type Backend interface {
	Load(context.Context, *http.Request) (PageData, error)
	StartWork(context.Context, *http.Request, StartWorkRequest) (string, error)
	Dispatch(context.Context, *http.Request, CommandRequest) error
	Logout(context.Context, *http.Request, http.ResponseWriter) error
}

type StartWorkRequest struct {
	Project  string
	Agent    string
	Subagent string
	Model    string
	Prompt   string
}

type CommandRequest struct {
	SessionID string
	Type      string
	Content   string
}

type PageData struct {
	Authenticated bool
	PrincipalName string
	Scope         string
	CanWrite      bool
	CanOperate    bool
	RequireToken  bool
	ProjectRoot   string
	Error         string
	Sessions      []SessionView
	Workspaces    []WorkspaceView
	Current       *SessionView
	Messages      []MessageView
	Todos         []TodoView
	Approvals     []ApprovalView
	AgentSpecs    []AgentSpecView
	MissionAgents []MissionAgentView
	Models        []ModelView
	Refresh       bool
}

type SessionView struct {
	ID            string
	Project       string
	Branch        string
	Model         string
	Status        string
	PauseReason   string
	PauseQuestion string
	CreatedAt     string
	LastActive    string
	MessageCount  int
	TotalTokens   int
	TotalCost     float64
}

type WorkspaceView struct {
	Path      string
	Label     string
	Sessions  []SessionView
	Active    int
	Attention int
}

type AgentSpecView struct {
	Path      string
	Name      string
	Kind      string
	Summary   string
	Subagents []string
	Valid     bool
	Error     string
}

type MissionAgentView struct {
	ID            string
	SessionID     string
	Type          string
	Status        string
	Action        string
	LastActivity  string
	PendingChange int
}

type MessageView struct {
	ID        int64
	Role      string
	Name      string
	Content   string
	Reasoning string
	Timestamp string
	Tokens    int
	Truncated bool
}

type TodoView struct {
	Content string
	Status  string
}

type ApprovalView struct {
	ID        string
	ToolName  string
	ToolArgs  string
	ExpiresAt string
}

type ModelView struct {
	ID   string
	Name string
}

// NewHandler builds the embedded GoSX application. It returns a regular
// http.Handler so the parent IPC router can preserve its existing CORS,
// security-header, cookie, and bearer-token middleware.
func NewHandler(backend Backend) http.Handler {
	if backend == nil {
		backend = emptyBackend{}
	}

	app := server.New()
	app.SetPublicDir("")
	app.EnableNavigation()
	app.EnableSecurityPolicy(server.SecurityPolicy{ContentSecurityPolicy: "default-src 'self'; base-uri 'self'; object-src 'none'; frame-ancestors 'none'; img-src 'self' data:; font-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self' 'nonce-{nonce}'; connect-src 'self' ws: wss:; manifest-src 'self'; worker-src 'self'"})
	app.Page("GET /", func(ctx *server.Context) gosx.Node {
		data, err := backend.Load(ctx.Request.Context(), ctx.Request)
		if err != nil {
			data.Error = err.Error()
		}
		if ctx.Request != nil && ctx.Request.URL != nil {
			if raw := strings.TrimSpace(ctx.Request.URL.Query().Get("error")); raw != "" && data.Error == "" {
				data.Error, _ = url.QueryUnescape(raw)
			}
		}
		ctx.SetMetadata(server.Metadata{Title: server.Title{Absolute: "Buckley · Mission Control"}})
		ctx.AddHead(gosx.El("link", gosx.Attrs(
			gosx.Attr("rel", "stylesheet"),
			gosx.Attr("href", "/assets/mission-control.css"),
		)))
		return renderDocument(data)
	})
	app.Mount("/assets/mission-control.css", embeddedAsset("styles.css", "text/css; charset=utf-8"))
	app.Mount(startActionPath, actionHandler(func(ctx context.Context, r *http.Request, _ http.ResponseWriter, values url.Values) (string, error) {
		id, err := backend.StartWork(ctx, r, StartWorkRequest{
			Project:  values.Get("project"),
			Agent:    values.Get("agent"),
			Subagent: values.Get("subagent"),
			Model:    values.Get("model"),
			Prompt:   values.Get("prompt"),
		})
		if err != nil {
			return "", err
		}
		return "/?session=" + url.QueryEscape(id), nil
	}))
	app.Mount(commandActionPath, actionHandler(func(ctx context.Context, r *http.Request, _ http.ResponseWriter, values url.Values) (string, error) {
		id := strings.TrimSpace(values.Get("session_id"))
		if err := backend.Dispatch(ctx, r, CommandRequest{
			SessionID: id,
			Type:      values.Get("type"),
			Content:   values.Get("content"),
		}); err != nil {
			return "", err
		}
		if id == "" {
			return "/", nil
		}
		return "/?session=" + url.QueryEscape(id), nil
	}))
	app.Mount(logoutActionPath, actionHandler(func(ctx context.Context, r *http.Request, w http.ResponseWriter, _ url.Values) (string, error) {
		if err := backend.Logout(ctx, r, w); err != nil {
			return "", err
		}
		return "/", nil
	}))
	return app.Build()
}

func actionHandler(fn func(context.Context, *http.Request, http.ResponseWriter, url.Values) (string, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		destination, err := fn(r.Context(), r, w, r.Form)
		if err != nil {
			redirect := "/?error=" + url.QueryEscape(err.Error())
			if sessionID := strings.TrimSpace(r.Form.Get("session_id")); sessionID != "" {
				redirect += "&session=" + url.QueryEscape(sessionID)
			}
			http.Redirect(w, r, redirect, http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, destination, http.StatusSeeOther)
	})
}

func embeddedAsset(name, contentType string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := styles.ReadFile(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	})
}

type emptyBackend struct{}

func (emptyBackend) Load(context.Context, *http.Request) (PageData, error) {
	return PageData{Authenticated: true, PrincipalName: "anonymous", Scope: "viewer"}, nil
}
func (emptyBackend) StartWork(context.Context, *http.Request, StartWorkRequest) (string, error) {
	return "", fmt.Errorf("mission control backend unavailable")
}
func (emptyBackend) Dispatch(context.Context, *http.Request, CommandRequest) error {
	return fmt.Errorf("mission control backend unavailable")
}
func (emptyBackend) Logout(context.Context, *http.Request, http.ResponseWriter) error { return nil }

func renderDocument(data PageData) gosx.Node {
	if !data.Authenticated {
		return renderLogin(data)
	}
	return renderMissionControl(data)
}

func renderLogin(data PageData) gosx.Node {
	children := []gosx.Node{
		gosx.El("div", cls("login-mark"), gosx.Text("B")),
		gosx.El("p", cls("eyebrow"), gosx.Text("BUCKLEY / GOSX")),
		gosx.El("h1", gosx.Text("Mission Control")),
		gosx.El("p", cls("lede"), gosx.Text("A local control surface for agents, subagents, workspaces, and durable runs.")),
	}
	if data.Error != "" {
		children = append(children, gosx.El("div", cls("notice notice-error"), gosx.Text(data.Error)))
	}
	children = append(children,
		server.Form(
			gosx.Attrs(gosx.Attr("method", http.MethodGet), gosx.Attr("action", "/"), gosx.Attr("class", "login-form")),
			gosx.El("label", gosx.Text("IPC token")),
			gosx.El("input", gosx.Attrs(gosx.Attr("type", "password"), gosx.Attr("name", "token"), gosx.Attr("placeholder", "paste a local token"), gosx.Attr("autocomplete", "current-password"))),
			gosx.El("button", cls("button button-primary"), gosx.Attr("type", "submit"), gosx.Text("Enter Mission Control")),
		),
		gosx.El("p", cls("muted login-foot"), gosx.Text("On loopback, a token query is exchanged for an HTTP-only Buckley session cookie.")),
	)
	return gosx.El("main", cls("login-page"), el("section", cls("login-card"), children...))
}

func renderMissionControl(data PageData) gosx.Node {
	root := []gosx.Node{
		gosx.El("header", cls("topbar"),
			gosx.El("div", cls("brand"), gosx.El("span", cls("brand-mark"), gosx.Text("B")), gosx.El("span", gosx.Text("Buckley")), gosx.El("span", cls("brand-divider"), gosx.Text("/")), gosx.El("span", cls("muted"), gosx.Text("Mission Control"))),
			gosx.El("div", cls("topbar-meta"),
				gosx.El("span", cls("scope-pill"), gosx.Text(strings.ToUpper(data.Scope))),
				gosx.El("span", cls("principal"), gosx.Text(data.PrincipalName)),
				server.Form(gosx.Attrs(gosx.Attr("method", http.MethodPost), gosx.Attr("action", logoutActionPath)), gosx.El("button", cls("button button-quiet"), gosx.Attr("type", "submit"), gosx.Text("Sign out"))),
			),
		),
	}
	if data.Error != "" {
		root = append(root, gosx.El("div", cls("notice notice-error shell-notice"), gosx.Text(data.Error)))
	}
	if data.Refresh {
		root = append(root, gosx.El("meta", gosx.Attrs(gosx.Attr("http-equiv", "refresh"), gosx.Attr("content", "5"))))
	}
	root = append(root, gosx.El("div", cls("app-grid"), renderSidebar(data), renderDetail(data)))
	return el("main", cls("app-shell"), root...)
}

func renderSidebar(data PageData) gosx.Node {
	sections := []gosx.Node{
		gosx.El("section", cls("side-section"),
			gosx.El("div", cls("section-heading"), gosx.El("span", gosx.Text("Directories")), gosx.El("span", cls("count"), gosx.Text(strconv.Itoa(len(data.Workspaces))))),
			renderWorkspaces(data.Workspaces, data.Current),
		),
	}
	sections = append(sections,
		gosx.El("section", cls("side-section"),
			gosx.El("div", cls("section-heading"), gosx.El("span", gosx.Text("Agent catalog")), gosx.El("span", cls("count"), gosx.Text(strconv.Itoa(len(data.AgentSpecs))))),
			renderAgentSpecs(data.AgentSpecs),
		),
		gosx.El("section", cls("side-section"),
			gosx.El("div", cls("section-heading"), gosx.El("span", gosx.Text("Active agents")), gosx.El("span", cls("count"), gosx.Text(strconv.Itoa(len(data.MissionAgents))))),
			renderMissionAgents(data.MissionAgents),
		),
	)
	return gosx.El("aside", cls("sidebar"),
		gosx.El("div", cls("sidebar-head"), gosx.El("div", cls("eyebrow"), gosx.Text("WORKSPACE OPERATIONS")), gosx.El("h2", gosx.Text("Work in motion")), gosx.El("p", cls("muted"), gosx.Text("Directories are the control plane. Runs, agents, and evidence stay attached to the worktree."))),
		el("div", cls("sidebar-scroll"), sections...),
	)
}

func renderWorkspaces(workspaces []WorkspaceView, current *SessionView) gosx.Node {
	if len(workspaces) == 0 {
		return gosx.El("p", cls("muted empty"), gosx.Text("No sessions yet. Start work below."))
	}
	children := make([]gosx.Node, 0, len(workspaces))
	for _, workspace := range workspaces {
		items := []gosx.Node{gosx.El("div", cls("workspace-title"), gosx.El("span", cls("status-dot"), gosx.Text("")), gosx.El("span", cls("truncate"), gosx.Text(workspace.Label)), gosx.El("span", cls("count"), gosx.Text(strconv.Itoa(len(workspace.Sessions))))), gosx.El("div", cls("workspace-path mono"), gosx.Text(workspace.Path))}
		for _, session := range workspace.Sessions {
			selected := current != nil && session.ID == current.ID
			items = append(items, server.Link("/?session="+url.QueryEscape(session.ID), gosx.Attrs(gosx.Attr("class", sessionClass(selected, session.Status))), gosx.El("span", cls("status-dot status-"+safeStatus(session.Status)), gosx.Text("")), gosx.El("span", cls("truncate"), gosx.Text(sessionLabel(session))), gosx.El("span", cls("mono session-branch"), gosx.Text(session.Branch))))
		}
		children = append(children, el("div", cls("workspace-card"), items...))
	}
	return el("div", cls("workspace-list"), children...)
}

func renderAgentSpecs(specs []AgentSpecView) gosx.Node {
	if len(specs) == 0 {
		return gosx.El("p", cls("muted empty"), gosx.Text("No .buckley agent specs discovered."))
	}
	children := make([]gosx.Node, 0, len(specs))
	for _, spec := range specs {
		status := "valid"
		if !spec.Valid {
			status = "invalid"
		}
		parts := []gosx.Node{gosx.El("div", cls("agent-row"), gosx.El("span", cls("status-dot status-"+status), gosx.Text("")), gosx.El("span", cls("truncate"), gosx.Text(firstNonEmpty(spec.Name, spec.Path))), gosx.El("span", cls("mono count"), gosx.Text(strconv.Itoa(len(spec.Subagents))))), gosx.El("p", cls("muted tiny truncate"), gosx.Text(firstNonEmpty(spec.Summary, spec.Kind)))}
		if len(spec.Subagents) > 0 {
			chips := make([]gosx.Node, 0, len(spec.Subagents))
			for _, subagent := range spec.Subagents {
				chips = append(chips, gosx.El("span", cls("chip mono"), gosx.Text(subagent)))
			}
			parts = append(parts, el("div", cls("chips"), chips...))
		}
		if spec.Error != "" {
			parts = append(parts, gosx.El("p", cls("tiny error-text"), gosx.Text(spec.Error)))
		}
		children = append(children, el("div", cls("agent-card "+status), parts...))
	}
	return el("div", cls("agent-list"), children...)
}

func renderMissionAgents(agents []MissionAgentView) gosx.Node {
	if len(agents) == 0 {
		return gosx.El("p", cls("muted empty"), gosx.Text("No recent agent activity."))
	}
	children := make([]gosx.Node, 0, len(agents))
	for _, agent := range agents {
		label := firstNonEmpty(agent.Type, agent.ID)
		children = append(children, gosx.El("div", cls("agent-row"), gosx.El("span", cls("status-dot status-"+safeStatus(agent.Status)), gosx.Text("")), gosx.El("span", cls("truncate"), gosx.Text(label)), gosx.El("span", cls("muted tiny truncate"), gosx.Text(firstNonEmpty(agent.Action, agent.Status)))))
	}
	return el("div", cls("agent-list"), children...)
}

func renderDetail(data PageData) gosx.Node {
	if data.Current == nil {
		return gosx.El("section", cls("detail empty-detail"),
			gosx.El("div", cls("empty-hero"), gosx.El("span", cls("eyebrow"), gosx.Text("READY")), gosx.El("h1", gosx.Text("Start a durable run")), gosx.El("p", cls("lede"), gosx.Text("Choose a directory, profile, and optional subagent. Buckley owns the runner and the audit trail."))),
			renderStartForm(data),
		)
	}
	return gosx.El("section", cls("detail"), renderSessionHeader(data), renderTranscript(data), renderControls(data), renderEvidence(data), renderStartForm(data))
}

func renderSessionHeader(data PageData) gosx.Node {
	session := data.Current
	buttons := []gosx.Node{}
	if data.CanWrite {
		buttons = append(buttons, commandButton(session.ID, "pause", "", "Pause", "button button-quiet"), commandButton(session.ID, "resume", "", "Resume", "button button-quiet"), commandButton(session.ID, "interrupt", "", "Interrupt", "button button-danger"))
	}
	return el("div", cls("session-header"), gosx.El("div", cls("session-title"), gosx.El("span", cls("eyebrow"), gosx.Text("RUN / "+session.ID)), gosx.El("h1", gosx.Text(sessionLabel(*session))), gosx.El("p", cls("mono muted"), gosx.Text(session.Project+branchSuffix(session.Branch)))), el("div", cls("session-actions"), buttons...))
}

func renderTranscript(data PageData) gosx.Node {
	children := []gosx.Node{gosx.El("div", cls("panel-head"), gosx.El("div", gosx.El("span", cls("eyebrow"), gosx.Text("TRANSCRIPT")), gosx.El("h2", gosx.Text("Conversation"))), gosx.El("span", cls("count"), gosx.Text(strconv.Itoa(len(data.Messages)))))}
	if len(data.Messages) == 0 {
		children = append(children, gosx.El("p", cls("muted empty"), gosx.Text("No messages have been persisted for this run yet.")))
	} else {
		messages := make([]gosx.Node, 0, len(data.Messages))
		for _, message := range data.Messages {
			meta := strings.ToUpper(firstNonEmpty(message.Name, message.Role)) + " · " + message.Timestamp
			body := []gosx.Node{gosx.El("div", cls("message-meta mono"), gosx.Text(meta)), gosx.El("div", cls("message-content"), gosx.Text(message.Content))}
			if message.Reasoning != "" {
				body = append(body, gosx.El("details", cls("reasoning"), gosx.El("summary", gosx.Text("thinking trace")), gosx.El("pre", gosx.Text(message.Reasoning))))
			}
			if message.Truncated {
				body = append(body, gosx.El("span", cls("truncated"), gosx.Text("interrupted / truncated")))
			}
			messages = append(messages, el("article", cls("message message-"+safeStatus(message.Role)), body...))
		}
		children = append(children, el("div", cls("transcript"), messages...))
	}
	return el("section", cls("panel transcript-panel"), children...)
}

func renderControls(data PageData) gosx.Node {
	if data.Current == nil {
		return gosx.Text("")
	}
	return gosx.El("section", cls("panel controls-panel"),
		gosx.El("div", cls("panel-head"), gosx.El("div", gosx.El("span", cls("eyebrow"), gosx.Text("STEERING")), gosx.El("h2", gosx.Text("Send an instruction"))), gosx.El("span", cls("scope-pill"), gosx.Text(firstNonEmpty(data.Current.Model, "config model")))),
		server.Form(gosx.Attrs(gosx.Attr("method", http.MethodPost), gosx.Attr("action", commandActionPath), gosx.Attr("class", "command-form")), gosx.El("input", gosx.Attrs(gosx.Attr("type", "hidden"), gosx.Attr("name", "session_id"), gosx.Attr("value", data.Current.ID))), gosx.El("select", gosx.Attrs(gosx.Attr("name", "type")), gosx.El("option", gosx.Attr("value", "input"), gosx.Text("message")), gosx.El("option", gosx.Attr("value", "steer"), gosx.Text("steer / interrupt")), gosx.El("option", gosx.Attr("value", "queue"), gosx.Text("queue")), gosx.El("option", gosx.Attr("value", "slash"), gosx.Text("slash command"))), gosx.El("textarea", gosx.Attrs(gosx.Attr("name", "content"), gosx.Attr("rows", "3"), gosx.Attr("placeholder", "Ask Buckley to continue, inspect, or change direction…"))), gosx.El("button", cls("button button-primary"), gosx.Attr("type", "submit"), gosx.Text("Dispatch"))),
	)
}

func renderEvidence(data PageData) gosx.Node {
	var columns []gosx.Node
	todos := make([]gosx.Node, 0, len(data.Todos))
	for _, todo := range data.Todos {
		mark := "○"
		if strings.EqualFold(todo.Status, "completed") {
			mark = "●"
		}
		todos = append(todos, gosx.El("li", cls("todo todo-"+safeStatus(todo.Status)), gosx.El("span", cls("todo-mark"), gosx.Text(mark)), gosx.El("span", gosx.Text(todo.Content))))
	}
	if len(todos) == 0 {
		todos = append(todos, gosx.El("li", cls("muted"), gosx.Text("No todo checkpoints yet.")))
	}
	columns = append(columns, el("div", cls("evidence-card"), gosx.El("div", cls("eyebrow"), gosx.Text("CHECKPOINTS")), el("ul", cls("todo-list"), todos...)))
	approvals := make([]gosx.Node, 0, len(data.Approvals))
	for _, approval := range data.Approvals {
		approvals = append(approvals, gosx.El("div", cls("approval"), gosx.El("div", cls("eyebrow"), gosx.Text("APPROVAL REQUIRED")), gosx.El("strong", gosx.Text(approval.ToolName)), gosx.El("pre", gosx.Text(approval.ToolArgs)), gosx.El("p", cls("muted tiny"), gosx.Text("expires "+approval.ExpiresAt)), gosx.El("div", cls("approval-actions"), commandButton(data.Current.ID, "approval", "approve", "Approve", "button button-primary"), commandButton(data.Current.ID, "approval", "reject", "Reject", "button button-danger"))))
	}
	if len(approvals) == 0 {
		approvals = append(approvals,
			gosx.El("div", cls("evidence-card"),
				gosx.El("div", cls("eyebrow"), gosx.Text("APPROVALS")),
				gosx.El("p", cls("muted"), gosx.Text("No pending tool approvals.")),
			),
		)
	}
	columns = append(columns, approvals...)
	telemetry := []gosx.Node{
		gosx.El("div", cls("eyebrow"), gosx.Text("RUN TELEMETRY")),
		gosx.El("div", cls("metric-grid"), metric("status", data.Current.Status), metric("messages", strconv.Itoa(data.Current.MessageCount)), metric("tokens", strconv.Itoa(data.Current.TotalTokens)), metric("cost", fmt.Sprintf("$%.4f", data.Current.TotalCost))),
		gosx.El("div", cls("terminal-card"),
			gosx.El("div", cls("eyebrow"), gosx.Text("TERMINAL BRIDGE")),
			gosx.El("p", cls("muted"), gosx.Text("PTY remains daemon-owned. Connect a terminal client to the authenticated /ws/pty endpoint for this run.")),
			gosx.El("code", cls("mono"), gosx.Text("GET /ws/pty · session="+data.Current.ID)),
		),
	}
	columns = append(columns, el("div", cls("evidence-card"), telemetry...))
	return el("section", cls("evidence-grid"), columns...)
}

func renderStartForm(data PageData) gosx.Node {
	if !data.CanWrite {
		return gosx.El("section", cls("start-card muted"), gosx.El("div", cls("eyebrow"), gosx.Text("READ ONLY")), gosx.El("p", gosx.Text("Use a member token to launch work or steer a run.")))
	}
	options := []gosx.Node{gosx.El("option", gosx.Attr("value", ""), gosx.Text("Daemon default"))}
	for _, spec := range data.AgentSpecs {
		attrs := []any{gosx.Attr("value", firstNonEmpty(spec.Name, spec.Path))}
		if !spec.Valid {
			attrs = append(attrs, gosx.BoolAttr("disabled"))
		}
		options = append(options, gosx.El("option", gosx.Attrs(attrs...), gosx.Text(firstNonEmpty(spec.Name, spec.Path))))
	}
	modelOptions := []gosx.Node{gosx.El("option", gosx.Attr("value", ""), gosx.Text("Use agent/config selection"))}
	for _, model := range data.Models {
		modelOptions = append(modelOptions, gosx.El("option", gosx.Attr("value", model.ID), gosx.Text(firstNonEmpty(model.Name, model.ID))))
	}
	return gosx.El("section", cls("start-card"),
		gosx.El("div", cls("eyebrow"), gosx.Text("NEW WORK")),
		gosx.El("h2", gosx.Text("Start an agent in a directory")),
		gosx.El("p", cls("muted"), gosx.Text("The daemon resolves profiles, policy, model routing, and persistence.")),
		server.Form(gosx.Attrs(gosx.Attr("method", http.MethodPost), gosx.Attr("action", startActionPath), gosx.Attr("class", "start-form")),
			field("Directory", "project", "text", firstNonEmpty(data.ProjectRoot, "."), "repository path"),
			el("div", cls("form-grid"), selectField("Agent profile", "agent", options...), field("Subagent", "subagent", "text", "", "optional profile subagent")),
			selectField("Model override", "model", modelOptions...),
			field("Initial task", "prompt", "textarea", "", "optional first instruction"),
			gosx.El("button", cls("button button-primary"), gosx.Attr("type", "submit"), gosx.Text("Start work")),
		),
	)
}

func field(label, name, kind, value, placeholder string) gosx.Node {
	if kind == "textarea" {
		return gosx.El("label", cls("field"), gosx.El("span", gosx.Text(label)), gosx.El("textarea", gosx.Attrs(gosx.Attr("name", name), gosx.Attr("placeholder", placeholder), gosx.Attr("rows", "3")), gosx.Text(value)))
	}
	return gosx.El("label", cls("field"), gosx.El("span", gosx.Text(label)), gosx.El("input", gosx.Attrs(gosx.Attr("type", kind), gosx.Attr("name", name), gosx.Attr("value", value), gosx.Attr("placeholder", placeholder))))
}

func selectField(label, name string, options ...gosx.Node) gosx.Node {
	return gosx.El("label", cls("field"), gosx.El("span", gosx.Text(label)), el("select", gosx.Attrs(gosx.Attr("name", name)), options...))
}

func commandButton(sessionID, typ, content, label, className string) gosx.Node {
	return server.Form(gosx.Attrs(gosx.Attr("method", http.MethodPost), gosx.Attr("action", commandActionPath), gosx.Attr("class", "inline-form")), gosx.El("input", gosx.Attrs(gosx.Attr("type", "hidden"), gosx.Attr("name", "session_id"), gosx.Attr("value", sessionID))), gosx.El("input", gosx.Attrs(gosx.Attr("type", "hidden"), gosx.Attr("name", "type"), gosx.Attr("value", typ))), gosx.El("input", gosx.Attrs(gosx.Attr("type", "hidden"), gosx.Attr("name", "content"), gosx.Attr("value", content))), gosx.El("button", cls(className), gosx.Attr("type", "submit"), gosx.Text(label)))
}

func metric(label, value string) gosx.Node {
	return gosx.El("div", cls("metric"), gosx.El("span", cls("eyebrow"), gosx.Text(label)), gosx.El("strong", cls("mono"), gosx.Text(value)))
}

func cls(className string) gosx.AttrList { return gosx.Attrs(gosx.Attr("class", className)) }

func el(tag string, attrs gosx.AttrList, children ...gosx.Node) gosx.Node {
	args := make([]any, 1, len(children)+1)
	args[0] = attrs
	for _, child := range children {
		args = append(args, child)
	}
	return gosx.El(tag, args...)
}

func sessionClass(selected bool, status string) string {
	if selected {
		return "session-link selected"
	}
	return "session-link status-" + safeStatus(status)
}

func sessionLabel(session SessionView) string {
	if session.Branch != "" {
		return firstNonEmpty(session.Branch, session.ID)
	}
	return firstNonEmpty(session.ID, "run")
}

func branchSuffix(branch string) string {
	if strings.TrimSpace(branch) == "" {
		return ""
	}
	return " · " + branch
}

func safeStatus(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "idle"
	}
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "idle"
	}
	return b.String()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Local().Format("Jan 02 · 15:04:05")
}
