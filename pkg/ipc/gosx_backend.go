package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/agentspec"
	"m31labs.dev/buckley/pkg/headless"
	"m31labs.dev/buckley/pkg/ipc/command"
	"m31labs.dev/buckley/pkg/ipc/gosxui"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/storage"
)

type gosxBackend struct {
	server *Server
}

func (s *Server) newGoSXUIHandler() http.Handler {
	ui := gosxui.NewHandler(gosxBackend{server: s})
	return s.authContextMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The login form intentionally uses a loopback query token so it can
		// remain a native HTML form. Exchange it once for the same HTTP-only
		// cookie used by the existing API clients, then all later GoSX actions
		// stay on clean URLs and never need bearer-token JavaScript.
		if token := strings.TrimSpace(r.URL.Query().Get("token")); token != "" && s.store != nil {
			if principal := principalFromContext(r.Context()); principal != nil && principal.Name != "anonymous" {
				if sessionToken, err := s.issueAuthSession(principal); err == nil {
					s.setSessionCookie(w, r, sessionToken)
				}
			}
		}
		ui.ServeHTTP(w, r)
	}))
}

func (b gosxBackend) Load(_ context.Context, r *http.Request) (gosxui.PageData, error) {
	s := b.server
	if s == nil {
		return gosxui.PageData{}, fmt.Errorf("ipc server unavailable")
	}
	principal := principalFromContext(r.Context())
	if principal == nil {
		return gosxui.PageData{RequireToken: s.cfg.RequireToken}, nil
	}
	data := gosxui.PageData{
		Authenticated: true,
		PrincipalName: principal.Name,
		Scope:         principal.Scope,
		CanWrite:      scopeAtLeast(principal.Scope, storage.TokenScopeMember),
		CanOperate:    scopeAtLeast(principal.Scope, storage.TokenScopeOperator),
		RequireToken:  s.cfg.RequireToken,
		ProjectRoot:   s.projectRoot,
	}
	if s.store == nil {
		return data, fmt.Errorf("storage unavailable")
	}

	sessions, err := s.store.ListSessions(200)
	if err != nil {
		return data, fmt.Errorf("list sessions: %w", err)
	}
	data.Sessions = make([]gosxui.SessionView, 0, len(sessions))
	for _, session := range sessions {
		session := session
		if !principalCanAccessSession(principal, &session) {
			continue
		}
		data.Sessions = append(data.Sessions, sessionView(session))
	}
	data.Workspaces = workspaceViews(data.Sessions)

	selectedID := strings.TrimSpace(r.URL.Query().Get("session"))
	if selectedID == "" && len(data.Sessions) > 0 {
		for i := range data.Sessions {
			if isLiveSession(data.Sessions[i].Status) {
				selectedID = data.Sessions[i].ID
				break
			}
		}
		if selectedID == "" {
			selectedID = data.Sessions[0].ID
		}
	}
	for i := range data.Sessions {
		if data.Sessions[i].ID == selectedID {
			current := data.Sessions[i]
			data.Current = &current
			break
		}
	}
	if data.Current != nil {
		messages, messageErr := s.store.GetMessages(data.Current.ID, 100, 0)
		if messageErr != nil {
			data.Error = fmt.Sprintf("load transcript: %v", messageErr)
		} else {
			data.Messages = messageViews(messages)
		}
		todos, todoErr := s.store.GetTodos(data.Current.ID)
		if todoErr == nil {
			data.Todos = todoViews(todos)
		}
		data.Approvals = b.pendingApprovals(data.Current.ID)
		data.Refresh = isLiveSession(data.Current.Status) || len(data.Approvals) > 0
	}

	project := s.projectRoot
	if data.Current != nil && data.Current.Project != "" {
		project = data.Current.Project
	}
	if project != "" {
		if discovery, discoveryErr := agentspec.DiscoverProjectSpecs(project); discoveryErr == nil {
			data.AgentSpecs = agentSpecViews(discovery.Specs)
		}
	}
	if data.CanWrite && s.missionStore != nil {
		if agents, agentErr := s.missionStore.ListActiveAgents(24 * time.Hour); agentErr == nil {
			for _, agent := range agents {
				if agent == nil {
					continue
				}
				if agent.SessionID != "" {
					session, sessionErr := s.store.GetSession(agent.SessionID)
					if sessionErr != nil || session == nil || !principalCanAccessSession(principal, session) {
						continue
					}
				}
				data.MissionAgents = append(data.MissionAgents, gosxui.MissionAgentView{
					ID:            agent.AgentID,
					SessionID:     agent.SessionID,
					Type:          agent.AgentType,
					Status:        agent.Status,
					Action:        agent.CurrentAction,
					LastActivity:  formatGoSXTime(agent.LastActivity),
					PendingChange: agent.PendingChanges,
				})
			}
		}
	}
	data.Models = modelViews(s.models)
	return data, nil
}

func (b gosxBackend) StartWork(_ context.Context, r *http.Request, req gosxui.StartWorkRequest) (string, error) {
	s := b.server
	principal, err := b.member(r)
	if err != nil {
		return "", err
	}
	if s.headlessRegistry == nil {
		return "", fmt.Errorf("headless sessions not enabled")
	}
	project, err := s.resolveAgentProjectPath(req.Project)
	if err != nil {
		return "", err
	}
	create := headless.CreateSessionRequest{
		Principal: principal.Name,
		Project:   project,
		Agent:     strings.TrimSpace(req.Agent),
		Subagent:  strings.TrimSpace(req.Subagent),
		Model:     strings.TrimSpace(req.Model),
		Prompt:    strings.TrimSpace(req.Prompt),
	}
	if create.Agent != "" || create.Subagent != "" {
		profile, profileModel, profilePolicy, profileErr := s.resolveHeadlessAgentSelection(project, create.Agent, create.Subagent)
		if profileErr != nil {
			return "", profileErr
		}
		create.AgentProfile = profile
		create.ToolPolicy = mergeHeadlessToolPolicies(create.ToolPolicy, profilePolicy)
		if create.Model == "" {
			create.Model = profileModel
		}
	}
	info, err := s.headlessRegistry.CreateSession(create)
	if err != nil {
		return "", err
	}
	if info == nil || strings.TrimSpace(info.ID) == "" {
		return "", fmt.Errorf("headless session created without an id")
	}
	return info.ID, nil
}

func (b gosxBackend) Dispatch(_ context.Context, r *http.Request, req gosxui.CommandRequest) error {
	s := b.server
	principal, err := b.member(r)
	if err != nil {
		return err
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return fmt.Errorf("missing session id")
	}
	if s.store == nil {
		return fmt.Errorf("storage unavailable")
	}
	session, err := s.store.GetSession(sessionID)
	if err != nil {
		return fmt.Errorf("load session: %w", err)
	}
	if session == nil || !principalCanAccessSession(principal, session) {
		return fmt.Errorf("session not found")
	}
	if s.commandGW == nil {
		return fmt.Errorf("commands not enabled")
	}
	typ := strings.TrimSpace(req.Type)
	if typ == "" {
		typ = "input"
	}
	content := strings.TrimSpace(req.Content)
	if command.RequiresContent(typ) && content == "" {
		return fmt.Errorf("content required")
	}
	if s.commandLimiter != nil && !s.commandLimiter.Allow(sessionID) {
		return fmt.Errorf("rate limit exceeded")
	}
	cmd := command.SessionCommand{SessionID: sessionID, Type: typ, Content: content}
	cmd.EnsureID()
	if err := s.commandGW.Dispatch(cmd); err != nil {
		return fmt.Errorf("dispatch command: %w", err)
	}
	return nil
}

func (b gosxBackend) Logout(_ context.Context, r *http.Request, w http.ResponseWriter) error {
	s := b.server
	if s == nil {
		return nil
	}
	if principalFromContext(r.Context()) == nil {
		return fmt.Errorf("unauthorized")
	}
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		s.revokeAuthSession(strings.TrimSpace(cookie.Value))
	}
	s.clearSessionCookie(w, r)
	return nil
}

func (b gosxBackend) member(r *http.Request) (*requestPrincipal, error) {
	principal := principalFromContext(r.Context())
	if principal == nil {
		return nil, fmt.Errorf("unauthorized")
	}
	if !scopeAtLeast(principal.Scope, storage.TokenScopeMember) {
		return nil, fmt.Errorf("forbidden")
	}
	return principal, nil
}

func (b gosxBackend) pendingApprovals(sessionID string) []gosxui.ApprovalView {
	if b.server == nil || b.server.headlessRegistry == nil {
		return nil
	}
	runner, ok := b.server.headlessRegistry.GetSession(sessionID)
	if !ok || runner == nil {
		return nil
	}
	pending := runner.GetPendingApproval()
	if pending == nil {
		return nil
	}
	args, _ := json.MarshalIndent(pending.ToolArgs, "", "  ")
	return []gosxui.ApprovalView{{ID: pending.ID, ToolName: pending.ToolName, ToolArgs: string(args), ExpiresAt: formatGoSXTime(pending.ExpiresAt)}}
}

func sessionView(session storage.Session) gosxui.SessionView {
	return gosxui.SessionView{
		ID:            session.ID,
		Project:       session.ProjectPath,
		Branch:        session.GitBranch,
		Model:         session.Model,
		Status:        session.Status,
		PauseReason:   session.PauseReason,
		PauseQuestion: session.PauseQuestion,
		CreatedAt:     formatGoSXTime(session.CreatedAt),
		LastActive:    formatGoSXTime(session.LastActive),
		MessageCount:  session.MessageCount,
		TotalTokens:   session.TotalTokens,
		TotalCost:     session.TotalCost,
	}
}

func workspaceViews(sessions []gosxui.SessionView) []gosxui.WorkspaceView {
	groups := make(map[string]*gosxui.WorkspaceView)
	for _, session := range sessions {
		path := strings.TrimSpace(session.Project)
		if path == "" {
			path = "workspace"
		}
		group := groups[path]
		if group == nil {
			group = &gosxui.WorkspaceView{Path: path, Label: filepath.Base(filepath.Clean(path))}
			if group.Label == "." || group.Label == string(filepath.Separator) || group.Label == "" {
				group.Label = path
			}
			groups[path] = group
		}
		group.Sessions = append(group.Sessions, session)
		if isLiveSession(session.Status) {
			group.Active++
		}
		if attentionSession(session.Status) {
			group.Attention++
		}
	}
	result := make([]gosxui.WorkspaceView, 0, len(groups))
	for _, group := range groups {
		sort.SliceStable(group.Sessions, func(i, j int) bool { return group.Sessions[i].LastActive > group.Sessions[j].LastActive })
		result = append(result, *group)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Attention != result[j].Attention {
			return result[i].Attention > result[j].Attention
		}
		return result[i].Path < result[j].Path
	})
	return result
}

func messageViews(messages []storage.Message) []gosxui.MessageView {
	result := make([]gosxui.MessageView, 0, len(messages))
	for _, message := range messages {
		result = append(result, gosxui.MessageView{ID: message.ID, Role: message.Role, Name: message.Name, Content: message.Content, Reasoning: message.Reasoning, Timestamp: formatGoSXTime(message.Timestamp), Tokens: message.Tokens, Truncated: message.IsTruncated})
	}
	return result
}

func todoViews(todos []storage.Todo) []gosxui.TodoView {
	result := make([]gosxui.TodoView, 0, len(todos))
	for _, todo := range todos {
		result = append(result, gosxui.TodoView{Content: todo.Content, Status: todo.Status})
	}
	return result
}

func agentSpecViews(specs []agentspec.DiscoveredSpec) []gosxui.AgentSpecView {
	result := make([]gosxui.AgentSpecView, 0, len(specs))
	for _, spec := range specs {
		result = append(result, gosxui.AgentSpecView{Path: spec.Path, Name: spec.Name, Kind: spec.Kind, Summary: spec.Summary, Subagents: append([]string(nil), spec.Subagents...), Valid: spec.Valid, Error: spec.Error})
	}
	return result
}

func modelViews(manager *model.Manager) []gosxui.ModelView {
	if manager == nil {
		return nil
	}
	catalog := manager.GetCatalog()
	if catalog == nil {
		return nil
	}
	result := make([]gosxui.ModelView, 0, len(catalog.Data))
	for _, info := range catalog.Data {
		result = append(result, gosxui.ModelView{ID: info.ID, Name: info.Name})
	}
	return result
}

func scopeAtLeast(scope, required string) bool {
	return scopeRank[strings.ToLower(strings.TrimSpace(scope))] >= scopeRank[strings.ToLower(strings.TrimSpace(required))]
}

func isLiveSession(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case storage.SessionStatusActive, "working", "running", "idle", "waiting":
		return true
	default:
		return false
	}
}

func attentionSession(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case storage.SessionStatusPaused, "waiting", "error":
		return true
	default:
		return false
	}
}

func formatGoSXTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Local().Format("Jan 02 · 15:04:05")
}
