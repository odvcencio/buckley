package ipc

import (
	"container/heap"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/agentspec"
	"m31labs.dev/buckley/pkg/headless"
	"m31labs.dev/buckley/pkg/ipc/command"
	"m31labs.dev/buckley/pkg/ipc/gosxui"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/ui/viewmodel"
)

type gosxBackend struct {
	server *Server
}

func (s *Server) newGoSXUIHandler() http.Handler {
	backend := gosxBackend{server: s}
	ui := gosxui.NewHandler(backend)
	handler := s.authContextMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			ui.ServeHTTP(w, r)
			return
		}

		principal := principalFromContext(r.Context())
		_, requestSessionActive := requestBrowserSessionValue(r)
		if requestSessionActive {
			sessionValue, _ := requestBrowserSessionValue(r)
			requestSessionActive = s.activeBrowserSession(sessionValue, principal)
		}
		needsSession := principal != nil && principal.Name != "anonymous" && !requestSessionActive
		if needsSession {
			responseSession, responseHasSession := responseBrowserSessionValue(w)
			if !responseHasSession || !s.activeBrowserSession(responseSession, principal) {
				if s.store == nil {
					http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
					return
				}
				sessionToken, err := s.issueAuthSession(principal)
				if err != nil {
					http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
					return
				}
				s.setSessionCookie(w, r, sessionToken)
			}
		}
		if needsSession || browserQueryHasToken(r) {
			http.Redirect(w, r, browserLocalRedirect(r), http.StatusSeeOther)
			return
		}
		ui.ServeHTTP(w, r)
	}))
	handler = s.basicAuthMiddleware(handler)
	handler = s.sessionMiddleware(handler)
	handler = s.securityHeadersMiddleware(handler)
	handler = s.corsMiddleware(handler)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		handler.ServeHTTP(w, r)
	})
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
		CSRFToken:     s.browserCSRFTokenForRequest(r),
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
		var liveRuns []viewmodel.AgentRun
		if s.runtimeTracker != nil {
			liveRuns = s.runtimeTracker.GetAgentRuns(data.Current.ID)
		}
		durableRuns, durableErr := b.durableAgentRuns(r.Context(), data.Current.ID)
		if durableErr != nil {
			if data.Error == "" {
				data.Error = fmt.Sprintf("load durable agent runs: %v", durableErr)
			}
			liveRuns = nil
		} else {
			canonicalLiveRuns, reconciledLiveRuns, reconcileErr := b.reconcileLiveAgentRuns(r.Context(), data.Current.ID, durableRuns, liveRuns)
			if reconcileErr != nil {
				if data.Error == "" {
					data.Error = fmt.Sprintf("reconcile live agent runs: %v", reconcileErr)
				}
				liveRuns = nil
			} else {
				durableRuns = viewmodel.MergeAgentRuns(durableRuns, canonicalLiveRuns)
				liveRuns = reconciledLiveRuns
			}
		}
		mergedRuns := viewmodel.MergeAgentRuns(durableRuns, liveRuns)
		data.AgentRuns = agentViews(boundAgentRuns(mergedRuns, gosxAgentRunLimit), s.hudTaskMarkerKey)
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
	data.Models = modelViews(s.models)
	return data, nil
}

func (b gosxBackend) StartWork(_ context.Context, r *http.Request, req gosxui.StartWorkRequest) (string, error) {
	s := b.server
	principal, err := b.member(r)
	if err != nil {
		return "", err
	}
	registry := s.getHeadlessRegistry()
	if registry == nil {
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
	info, err := registry.CreateSession(create)
	if err != nil {
		return "", err
	}
	if info == nil || strings.TrimSpace(info.ID) == "" {
		return "", fmt.Errorf("headless session created without an id")
	}
	return info.ID, nil
}

func (b gosxBackend) Dispatch(ctx context.Context, r *http.Request, req gosxui.CommandRequest) error {
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
	if !commandTargetAvailable(s) {
		return fmt.Errorf("commands not enabled")
	}
	typ := strings.TrimSpace(req.Type)
	if typ == "" {
		typ = "input"
	}
	content := strings.TrimSpace(req.Content)
	if strings.EqualFold(typ, "approval") {
		typ = "approval"
		approvalID := strings.TrimSpace(req.ApprovalID)
		if approvalID == "" {
			return fmt.Errorf("approval id required")
		}
		var approved bool
		switch content {
		case "approve":
			approved = true
		case "reject":
			approved = false
		default:
			return fmt.Errorf("invalid approval decision")
		}
		payload, err := json.Marshal(headless.ApprovalResponse{ID: approvalID, Approved: approved})
		if err != nil {
			return fmt.Errorf("encode approval decision: %w", err)
		}
		content = string(payload)
	}
	if command.RequiresContent(typ) && content == "" {
		return fmt.Errorf("content required")
	}
	if s.commandLimiter != nil && !s.commandLimiter.Allow(sessionID) {
		return fmt.Errorf("rate limit exceeded")
	}
	cmd := command.SessionCommand{
		SessionID: sessionID, Type: typ, Content: content,
		AcceptedBy: strings.TrimSpace(principal.Name),
	}
	if _, err := s.dispatchCommandWithReceipt(ctx, &cmd, commandDispatchGateway); err != nil {
		if isAuthoritativeCommandError(err) {
			_, safeErr := commandAcceptanceHTTPError(err)
			return safeErr
		}
		return fmt.Errorf("dispatch command: %w", err)
	}
	return nil
}

func (b gosxBackend) Logout(_ context.Context, r *http.Request, w http.ResponseWriter) error {
	s := b.server
	if s == nil {
		return fmt.Errorf("ipc server unavailable")
	}
	if principalFromContext(r.Context()) == nil {
		return fmt.Errorf("unauthorized")
	}
	if err := s.revokeLogoutSessions(r); err != nil {
		return fmt.Errorf("revoke browser logout sessions: %w", err)
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
	if b.server == nil {
		return nil
	}
	registry := b.server.getHeadlessRegistry()
	if registry == nil {
		return nil
	}
	runner, ok := registry.GetSession(sessionID)
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

func agentViews(runs []viewmodel.AgentRun, taskMarkerKey [32]byte) []gosxui.AgentView {
	if len(runs) == 0 {
		return nil
	}
	result := make([]gosxui.AgentView, 0, len(runs))
	for _, run := range runs {
		result = append(result, gosxui.AgentView{
			ID:              safeHUDIdentifier(run.ID, "run", 128),
			ParentID:        safeHUDIdentifier(run.ParentID, "run", 128),
			ParentSessionID: safeHUDIdentifier(run.ParentSessionID, "session", 128),
			Agent:           safeHUDText(run.Agent, 128),
			Persona:         safeHUDText(run.Persona, 128),
			Model:           safeHUDText(run.Model, 128),
			Status:          safeHUDAgentStatus(run.Status),
			Task:            safeHUDAgentTask(run, taskMarkerKey),
			Children:        agentViews(run.Children, taskMarkerKey),
		})
	}
	return result
}

const gosxAgentRunLimit = 256

func boundAgentRuns(runs []viewmodel.AgentRun, limit int) []viewmodel.AgentRun {
	if limit <= 0 || len(runs) == 0 {
		return nil
	}
	candidates := &agentRunPriorityQueue{}
	heap.Init(candidates)
	var selectCandidates func([]viewmodel.AgentRun)
	selectCandidates = func(current []viewmodel.AgentRun) {
		for _, run := range current {
			children := run.Children
			run.Children = nil
			if strings.TrimSpace(run.ID) != "" {
				if candidates.Len() < limit {
					heap.Push(candidates, run)
				} else if agentRunHigherPriority(run, (*candidates)[0]) {
					heap.Pop(candidates)
					heap.Push(candidates, run)
				}
			}
			selectCandidates(children)
		}
	}
	selectCandidates(runs)

	// Retain the highest-value nodes, then rebuild the tree. When a retained
	// child loses an older parent outside the window, the existing tree builder
	// deterministically promotes it to a root while preserving ParentID as a
	// breadcrumb instead of dropping the child.
	return viewmodel.MergeAgentRuns([]viewmodel.AgentRun(*candidates), nil)
}

type agentRunPriorityQueue []viewmodel.AgentRun

func (q agentRunPriorityQueue) Len() int      { return len(q) }
func (q agentRunPriorityQueue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q agentRunPriorityQueue) Less(i, j int) bool {
	return agentRunHigherPriority(q[j], q[i])
}
func (q *agentRunPriorityQueue) Push(value any) {
	*q = append(*q, value.(viewmodel.AgentRun))
}
func (q *agentRunPriorityQueue) Pop() any {
	old := *q
	last := len(old) - 1
	value := old[last]
	*q = old[:last]
	return value
}

func agentRunHigherPriority(left, right viewmodel.AgentRun) bool {
	leftCurrent, rightCurrent := isCurrentAgentRun(left.Status), isCurrentAgentRun(right.Status)
	if leftCurrent != rightCurrent {
		return leftCurrent
	}
	leftTime, rightTime := agentRunActivityTime(left), agentRunActivityTime(right)
	if !leftTime.Equal(rightTime) {
		return leftTime.After(rightTime)
	}
	if !left.StartedAt.Equal(right.StartedAt) {
		return left.StartedAt.After(right.StartedAt)
	}
	return left.ID < right.ID
}

func isCurrentAgentRun(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "queued", "pending", "running":
		return true
	default:
		return false
	}
}

func agentRunActivityTime(run viewmodel.AgentRun) time.Time {
	if !run.UpdatedAt.IsZero() {
		return run.UpdatedAt
	}
	return run.StartedAt
}

func (b gosxBackend) durableAgentRuns(ctx context.Context, sessionID string) ([]viewmodel.AgentRun, error) {
	if b.server == nil || strings.TrimSpace(sessionID) == "" {
		return nil, nil
	}
	ledger := b.server.getDurableLedger()
	if ledger == nil {
		return nil, nil
	}
	runs, err := ledger.ListRuns(ctx, runledger.RunQuery{
		SessionID: strings.TrimSpace(sessionID),
		Limit:     gosxAgentRunLimit,
		Order:     runledger.RunOrderNewestFirst,
	})
	if err != nil {
		return nil, err
	}
	result := make([]viewmodel.AgentRun, 0, len(runs))
	for _, run := range runs {
		result = append(result, durableAgentRunView(run))
	}
	return result, nil
}

func (b gosxBackend) reconcileLiveAgentRuns(ctx context.Context, sessionID string, durableRuns, liveRuns []viewmodel.AgentRun) ([]viewmodel.AgentRun, []viewmodel.AgentRun, error) {
	liveRuns = boundAgentRuns(liveRuns, gosxAgentRunLimit)
	if len(liveRuns) == 0 || b.server == nil {
		return nil, liveRuns, nil
	}
	ledger := b.server.getDurableLedger()
	if ledger == nil {
		return nil, liveRuns, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	knownDurable := make(map[string]struct{}, gosxAgentRunLimit)
	for _, run := range flattenAgentRuns(durableRuns) {
		knownDurable[run.ID] = struct{}{}
	}

	canonical := make([]viewmodel.AgentRun, 0, len(liveRuns))
	filteredLive := make([]viewmodel.AgentRun, 0, len(liveRuns))
	for _, live := range flattenAgentRuns(liveRuns) {
		if _, ok := knownDurable[live.ID]; ok {
			filteredLive = append(filteredLive, live)
			continue
		}
		run, err := ledger.GetRun(ctx, live.ID)
		if errors.Is(err, runledger.ErrNotFound) {
			filteredLive = append(filteredLive, live)
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if strings.TrimSpace(run.SessionID) != sessionID {
			continue
		}
		canonical = append(canonical, durableAgentRunView(run))
		filteredLive = append(filteredLive, live)
	}
	return canonical, filteredLive, nil
}

func flattenAgentRuns(runs []viewmodel.AgentRun) []viewmodel.AgentRun {
	result := make([]viewmodel.AgentRun, 0, len(runs))
	var flatten func([]viewmodel.AgentRun)
	flatten = func(current []viewmodel.AgentRun) {
		for _, run := range current {
			children := run.Children
			run.Children = nil
			result = append(result, run)
			flatten(children)
		}
	}
	flatten(runs)
	return result
}

func durableAgentRunView(run runledger.AgentRun) viewmodel.AgentRun {
	status := strings.ToLower(strings.TrimSpace(run.Status))
	if status == "canceled" {
		status = "cancelled"
	}
	if status != "completed" && status != "failed" && status != "cancelled" && status != "blocked" {
		// No local tracker after a daemon restart means the worker is detached;
		// retain the run as resumable instead of claiming it is live.
		status = "resumable"
	}
	updatedAt := run.StartedAt
	if run.EndedAt != nil {
		updatedAt = *run.EndedAt
	}
	return viewmodel.AgentRun{
		ID:              run.RunID,
		ParentID:        run.ParentRunID,
		ParentSessionID: run.SessionID,
		Agent:           firstNonEmptyAgentValue(run.AgentID, run.ProviderID, run.Backend),
		Model:           run.ModelID,
		Status:          status,
		// TaskID is a safe identifier; task bodies remain in evidence and are
		// intentionally not loaded into the HUD projection.
		Task:      run.TaskID,
		TaskIsID:  strings.TrimSpace(run.TaskID) != "",
		StartedAt: run.StartedAt,
		UpdatedAt: updatedAt,
	}
}

func firstNonEmptyAgentValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func safeHUDIdentifier(value, kind string, maxRunes int) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	valid := utf8.RuneCountInString(value) <= maxRunes
	for _, r := range value {
		if !isHUDIdentifierRune(r) {
			valid = false
			break
		}
	}
	if valid {
		return value
	}
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%s-%x", kind, digest[:8])
}

func safeHUDAgentTask(run viewmodel.AgentRun, taskMarkerKey [32]byte) string {
	value := strings.TrimSpace(run.Task)
	if value == "" || strings.TrimSpace(run.ID) == "" || taskMarkerKey == ([32]byte{}) {
		return ""
	}
	source := "body"
	if run.TaskIsID {
		source = "durable-id"
	}
	marker := hmac.New(sha256.New, taskMarkerKey[:])
	_, _ = marker.Write([]byte("buckley.hud.task.v1\x00"))
	_, _ = marker.Write([]byte(strings.TrimSpace(run.ParentSessionID)))
	_, _ = marker.Write([]byte{'\x00'})
	_, _ = marker.Write([]byte(strings.TrimSpace(run.ID)))
	_, _ = marker.Write([]byte{'\x00'})
	_, _ = marker.Write([]byte(source))
	_, _ = marker.Write([]byte{'\x00'})
	_, _ = marker.Write([]byte(value))
	return fmt.Sprintf("task-%x", marker.Sum(nil)[:8])
}

func isHUDIdentifierRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_.:/@", r)
}

func safeHUDText(value string, maxRunes int) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(value))
	if maxRunes <= 0 || utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	runes := []rune(value)
	if maxRunes == 1 {
		return "…"
	}
	return string(runes[:maxRunes-1]) + "…"
}

func safeHUDAgentStatus(status string) string {
	status = strings.ToLower(strings.TrimSpace(status))
	if status == "canceled" {
		status = "cancelled"
	}
	switch status {
	case "pending", "running", "resumable", "completed", "failed", "cancelled", "blocked":
		return status
	default:
		return "unknown"
	}
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
