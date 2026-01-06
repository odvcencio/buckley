package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/odvcencio/buckley/pkg/giturl"
	"github.com/odvcencio/buckley/pkg/headless"
	"github.com/odvcencio/buckley/pkg/ipc/command"
	ipcpb "github.com/odvcencio/buckley/pkg/ipc/proto"
	"github.com/odvcencio/buckley/pkg/orchestrator"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/ui/viewmodel"
)

// GRPCService implements the BuckleyIPC Connect service.
type GRPCService struct {
	server *Server

	// Agent management
	agentsMu sync.RWMutex
	agents   map[string]*connectedAgent

	// Subscriber management for event streaming
	subscribersMu   sync.RWMutex
	subscribers     map[string]*eventSubscriber
	subsByPrincipal map[string]int

	// Session ownership cache to support access filtering on event streams.
	sessionOwnersMu sync.RWMutex
	sessionOwners   map[string]string

	subscribeLimiter           *rateLimiter
	maxSubscribersTotal        int
	maxSubscribersPerPrincipal int

	// Event broadcast channel
	eventCh chan Event
}

type connectedAgent struct {
	info      *ipcpb.AgentInfo
	commands  chan *ipcpb.AgentCommand
	results   chan *ipcpb.AgentResult
	cancel    context.CancelFunc
	sessionID string // Current session using this agent
}

type eventSubscriber struct {
	id           string
	principalKey string
	principal    string
	operator     bool
	filter       *ipcpb.SubscribeRequest
	events       chan *ipcpb.Event
	cancel       context.CancelFunc
	lastSeen     time.Time
}

func requireGRPCScope(ctx context.Context, required string) error {
	principal := principalFromContext(ctx)
	if principal == nil {
		return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("unauthorized"))
	}
	if scopeRank[strings.ToLower(principal.Scope)] < scopeRank[strings.ToLower(required)] {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("forbidden"))
	}
	return nil
}

func (s *GRPCService) dispatchSessionCommand(cmd command.SessionCommand) error {
	if s.server == nil {
		return fmt.Errorf("server unavailable")
	}
	if s.server.headlessRegistry != nil {
		if err := s.server.headlessRegistry.DispatchCommand(cmd); err == nil {
			return nil
		}
	}
	if s.server.commandGW != nil {
		return s.server.commandGW.Dispatch(cmd)
	}
	return fmt.Errorf("no command handler available")
}

// NewGRPCService creates a new gRPC service wrapping the IPC server.
func NewGRPCService(server *Server) *GRPCService {
	svc := &GRPCService{
		server:          server,
		agents:          make(map[string]*connectedAgent),
		subscribers:     make(map[string]*eventSubscriber),
		subsByPrincipal: make(map[string]int),
		sessionOwners:   make(map[string]string),
		eventCh:         make(chan Event, 256),

		subscribeLimiter:           newRateLimiter(200 * time.Millisecond),
		maxSubscribersTotal:        maxGRPCSubscribersTotal,
		maxSubscribersPerPrincipal: maxGRPCSubscribersPerPrincipal,
	}

	// Start event forwarder
	go svc.runEventForwarder()

	return svc
}

// BroadcastEvent sends an event to all gRPC subscribers.
// Called by the hub or other parts of the system.
func (s *GRPCService) BroadcastEvent(event Event) {
	select {
	case s.eventCh <- event:
	default:
		// Channel full, drop event
	}
}

// runEventForwarder distributes events to subscribers.
func (s *GRPCService) runEventForwarder() {
	for event := range s.eventCh {
		s.noteSessionOwner(event)
		owner := s.sessionOwner(event.SessionID)

		var protoEvent *ipcpb.Event
		s.subscribersMu.RLock()
		for _, sub := range s.subscribers {
			if !s.subscriberAllowsEvent(sub, event, owner) {
				continue
			}
			if !matchesEventFilter(event, sub.filter) {
				continue
			}
			if protoEvent == nil {
				protoEvent = convertToProtoEvent(event)
			}
			cloned := proto.Clone(protoEvent).(*ipcpb.Event)
			select {
			case sub.events <- cloned:
			default:
				// Slow subscriber: disconnect to protect the server.
				if sub.cancel != nil {
					sub.cancel()
				}
			}
		}
		s.subscribersMu.RUnlock()

		if event.Type == string(storage.EventSessionDeleted) && event.SessionID != "" {
			s.forgetSessionOwner(event.SessionID)
		}
	}
}

func convertToProtoEvent(event Event) *ipcpb.Event {
	payload := payloadToStruct(event.Payload)
	return &ipcpb.Event{
		Type:      event.Type,
		SessionId: event.SessionID,
		Payload:   payload,
		Timestamp: timestamppb.New(event.Timestamp),
		EventId:   fmt.Sprintf("%d", event.Timestamp.UnixNano()),
	}
}

func payloadToStruct(v any) *structpb.Struct {
	if v == nil {
		return nil
	}

	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil
	}
	if m, ok := decoded.(map[string]any); ok {
		payload, _ := structpb.NewStruct(m)
		return payload
	}
	payload, _ := structpb.NewStruct(map[string]any{"data": decoded})
	return payload
}

func matchesEventFilter(event Event, filter *ipcpb.SubscribeRequest) bool {
	if filter == nil {
		return false
	}

	// Session filter
	if filter.SessionId != "" && event.SessionID != "" && event.SessionID != filter.SessionId {
		return false
	}

	if strings.HasPrefix(event.Type, "agent.") && !filter.IncludeAgentEvents {
		return false
	}

	// Event type filter
	if len(filter.EventTypes) > 0 {
		matched := false
		for _, t := range filter.EventTypes {
			if event.Type == t || matchesPrefix(event.Type, t) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	return true
}

func matchesPrefix(eventType, pattern string) bool {
	// Support wildcards like "session.*"
	if len(pattern) > 0 && pattern[len(pattern)-1] == '*' {
		prefix := pattern[:len(pattern)-1]
		return len(eventType) >= len(prefix) && eventType[:len(prefix)] == prefix
	}
	return eventType == pattern
}

func normalizePrincipalName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func (s *GRPCService) noteSessionOwner(event Event) {
	if s == nil || event.SessionID == "" {
		return
	}
	if event.Type != string(storage.EventSessionCreated) {
		return
	}
	switch payload := event.Payload.(type) {
	case storage.Session:
		s.cacheSessionOwner(event.SessionID, payload.Principal)
	case *storage.Session:
		if payload != nil {
			s.cacheSessionOwner(event.SessionID, payload.Principal)
		}
	case map[string]any:
		if v, ok := payload["principal"].(string); ok {
			s.cacheSessionOwner(event.SessionID, v)
		}
	}
}

func (s *GRPCService) cacheSessionOwner(sessionID, principal string) {
	if s == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	normalized := normalizePrincipalName(principal)
	if sessionID == "" || normalized == "" {
		return
	}
	s.sessionOwnersMu.Lock()
	s.sessionOwners[sessionID] = normalized
	s.sessionOwnersMu.Unlock()
}

func (s *GRPCService) forgetSessionOwner(sessionID string) {
	if s == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	s.sessionOwnersMu.Lock()
	delete(s.sessionOwners, sessionID)
	s.sessionOwnersMu.Unlock()
}

func (s *GRPCService) sessionOwner(sessionID string) string {
	if s == nil {
		return ""
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	s.sessionOwnersMu.RLock()
	owner := s.sessionOwners[sessionID]
	s.sessionOwnersMu.RUnlock()
	return owner
}

func (s *GRPCService) subscriberAllowsEvent(sub *eventSubscriber, event Event, sessionOwner string) bool {
	if sub == nil || sub.filter == nil {
		return false
	}

	if strings.HasPrefix(event.Type, "agent.") && !sub.filter.IncludeAgentEvents {
		return false
	}

	if sub.operator {
		return true
	}

	if strings.HasPrefix(event.Type, "mission.") || strings.HasPrefix(event.Type, "agent.") {
		return false
	}

	if strings.HasPrefix(event.Type, "server.") {
		return true
	}

	if event.SessionID == "" {
		return false
	}

	if sub.principal == "" || sessionOwner == "" {
		return false
	}
	return sub.principal == sessionOwner
}

// =============================================================================
// Event Streaming
// =============================================================================

func (s *GRPCService) Subscribe(
	ctx context.Context,
	req *connect.Request[ipcpb.SubscribeRequest],
	stream *connect.ServerStream[ipcpb.Event],
) error {
	if err := requireGRPCScope(ctx, storage.TokenScopeViewer); err != nil {
		return err
	}

	principal := principalFromContext(ctx)
	if principal == nil {
		return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("unauthorized"))
	}

	sessID := strings.TrimSpace(req.Msg.SessionId)
	if !isOperatorPrincipal(principal) {
		if req.Msg.IncludeAgentEvents {
			return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("include_agent_events requires operator scope"))
		}
		if s.server == nil || s.server.store == nil {
			return connect.NewError(connect.CodeUnavailable, fmt.Errorf("storage unavailable"))
		}
		if sessID != "" {
			session, err := s.server.store.GetSession(sessID)
			if err != nil {
				return connect.NewError(connect.CodeInternal, err)
			}
			if session == nil || !principalCanAccessSession(principal, session) {
				return connect.NewError(connect.CodeNotFound, fmt.Errorf("session not found"))
			}
			s.cacheSessionOwner(sessID, session.Principal)
		}
	}

	principalKey := "unknown"
	if principal.TokenID != "" {
		principalKey = principal.Name + ":" + principal.TokenID
	} else if principal.Name != "" {
		principalKey = principal.Name
	}
	if s.subscribeLimiter != nil && !s.subscribeLimiter.Allow("grpc_subscribe:"+principalKey) {
		return connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("rate limit exceeded"))
	}

	subID := fmt.Sprintf("sub-%d", time.Now().UnixNano())
	subCtx, cancel := context.WithCancel(ctx)

	sub := &eventSubscriber{
		id:           subID,
		principalKey: principalKey,
		principal:    normalizePrincipalName(principal.Name),
		operator:     isOperatorPrincipal(principal),
		filter:       req.Msg,
		events:       make(chan *ipcpb.Event, 128),
		cancel:       cancel,
		lastSeen:     time.Now(),
	}

	s.subscribersMu.Lock()
	if s.maxSubscribersTotal > 0 && len(s.subscribers) >= s.maxSubscribersTotal {
		s.subscribersMu.Unlock()
		cancel()
		return connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("too many subscribers"))
	}
	if s.maxSubscribersPerPrincipal > 0 && principalKey != "" {
		if s.subsByPrincipal[principalKey] >= s.maxSubscribersPerPrincipal {
			s.subscribersMu.Unlock()
			cancel()
			return connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("too many subscribers"))
		}
	}
	s.subscribers[subID] = sub
	if principalKey != "" {
		s.subsByPrincipal[principalKey]++
	}
	s.subscribersMu.Unlock()

	defer func() {
		s.subscribersMu.Lock()
		if existing, ok := s.subscribers[subID]; ok {
			delete(s.subscribers, subID)
			if existing.principalKey != "" {
				s.subsByPrincipal[existing.principalKey]--
				if s.subsByPrincipal[existing.principalKey] <= 0 {
					delete(s.subsByPrincipal, existing.principalKey)
				}
			}
		}
		s.subscribersMu.Unlock()
		cancel()
	}()

	// Send initial hello event
	if err := stream.Send(&ipcpb.Event{
		Type:      "server.hello",
		Timestamp: timestamppb.Now(),
		EventId:   "hello",
	}); err != nil {
		return err
	}

	// Opportunistically send an initial snapshot to help UI clients render immediately.
	if sessID == "" && s.server != nil && s.server.store != nil {
		if sessions, err := s.server.store.ListSessions(50); err == nil {
			filtered := make([]storage.Session, 0, len(sessions))
			for _, sess := range sessions {
				sess := sess
				s.cacheSessionOwner(sess.ID, sess.Principal)
				if !principalCanAccessSession(principal, &sess) {
					continue
				}
				filtered = append(filtered, sess)
			}
			_ = stream.Send(convertToProtoEvent(Event{
				Type:      "sessions.snapshot",
				SessionID: "",
				Payload:   map[string]any{"sessions": filtered},
				Timestamp: time.Now(),
			}))
		}
	}
	if sessID != "" && s.server != nil && s.server.viewAssembler != nil {
		if state, err := s.server.viewAssembler.BuildSessionState(ctx, sessID); err == nil && state != nil {
			_ = stream.Send(convertToProtoEvent(Event{
				Type:      "view.patch",
				SessionID: sessID,
				Payload:   viewmodel.Patch{Session: state},
				Timestamp: time.Now(),
			}))
		}
	}

	// Send keepalive every 20 seconds
	keepaliveTicker := time.NewTicker(20 * time.Second)
	defer keepaliveTicker.Stop()

	for {
		select {
		case <-subCtx.Done():
			return subCtx.Err()
		case event := <-sub.events:
			if err := stream.Send(event); err != nil {
				return err
			}
		case <-keepaliveTicker.C:
			if err := stream.Send(&ipcpb.Event{
				Type:      "server.keepalive",
				Timestamp: timestamppb.Now(),
			}); err != nil {
				return err
			}
		}
	}
}

// =============================================================================
// Session Management
// =============================================================================

func (s *GRPCService) SendCommand(
	ctx context.Context,
	req *connect.Request[ipcpb.CommandRequest],
) (*connect.Response[ipcpb.CommandResponse], error) {
	msg := req.Msg

	// If targeting an agent, route to agent
	if msg.AgentId != "" {
		if err := requireGRPCScope(ctx, storage.TokenScopeOperator); err != nil {
			return nil, err
		}
		return s.sendAgentCommand(ctx, msg)
	}
	if err := requireGRPCScope(ctx, storage.TokenScopeMember); err != nil {
		return nil, err
	}
	if s.server == nil || s.server.store == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("storage unavailable"))
	}

	principal := principalFromContext(ctx)
	if principal == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("unauthorized"))
	}

	// Otherwise route to session
	sessionID := strings.TrimSpace(msg.SessionId)
	if sessionID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("missing session id"))
	}

	session, err := s.server.store.GetSession(sessionID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if session == nil || !principalCanAccessSession(principal, session) {
		return connect.NewResponse(&ipcpb.CommandResponse{
			Status:  "rejected",
			Message: "session not found",
		}), nil
	}

	sessionToken := strings.TrimSpace(msg.SessionToken)
	if sessionToken == "" {
		sessionToken = strings.TrimSpace(req.Header().Get("X-Buckley-Session-Token"))
	}
	if !s.server.validateSessionTokenValue(sessionID, sessionToken) {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid session token"))
	}

	cmdType := strings.TrimSpace(msg.Type)
	if cmdType == "" {
		cmdType = "input"
	}
	if (cmdType == "input" || cmdType == "slash") && strings.TrimSpace(msg.Content) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("content required"))
	}

	cmd := command.SessionCommand{
		SessionID: sessionID,
		Type:      cmdType,
		Content:   msg.Content,
	}

	// Try headless registry first
	if s.server.headlessRegistry != nil {
		if err := s.server.headlessRegistry.DispatchCommand(cmd); err == nil {
			return connect.NewResponse(&ipcpb.CommandResponse{
				Status:  "accepted",
				Message: "Command dispatched to headless session",
			}), nil
		}
	}

	// Try command gateway
	if s.server.commandGW != nil {
		if err := s.server.commandGW.Dispatch(cmd); err != nil {
			return connect.NewResponse(&ipcpb.CommandResponse{
				Status:  "rejected",
				Message: err.Error(),
			}), nil
		}
		return connect.NewResponse(&ipcpb.CommandResponse{
			Status:  "accepted",
			Message: "Command dispatched",
		}), nil
	}

	return connect.NewResponse(&ipcpb.CommandResponse{
		Status:  "rejected",
		Message: "No command handler available",
	}), nil
}

func (s *GRPCService) ListSessions(
	ctx context.Context,
	req *connect.Request[ipcpb.ListSessionsRequest],
) (*connect.Response[ipcpb.ListSessionsResponse], error) {
	if err := requireGRPCScope(ctx, storage.TokenScopeViewer); err != nil {
		return nil, err
	}
	if s.server.store == nil {
		return connect.NewResponse(&ipcpb.ListSessionsResponse{}), nil
	}

	principal := principalFromContext(ctx)
	if principal == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("unauthorized"))
	}

	limit := int(req.Msg.Limit)
	if limit == 0 {
		limit = 50
	}

	sessions, err := s.server.store.ListSessions(limit)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	var summaries []*ipcpb.SessionSummary
	for _, sess := range sessions {
		sess := sess
		if !principalCanAccessSession(principal, &sess) {
			continue
		}
		summaries = append(summaries, &ipcpb.SessionSummary{
			Id:          sess.ID,
			ProjectPath: sess.ProjectPath,
			GitRepo:     sess.GitRepo,
			GitBranch:   sess.GitBranch,
			Status:      sess.Status,
			CreatedAt:   timestamppb.New(sess.CreatedAt),
			LastActive:  timestamppb.New(sess.LastActive),
		})
	}

	return connect.NewResponse(&ipcpb.ListSessionsResponse{
		Sessions:   summaries,
		TotalCount: clampInt32(len(summaries)),
	}), nil
}

func (s *GRPCService) GetSession(
	ctx context.Context,
	req *connect.Request[ipcpb.GetSessionRequest],
) (*connect.Response[ipcpb.SessionDetail], error) {
	if err := requireGRPCScope(ctx, storage.TokenScopeViewer); err != nil {
		return nil, err
	}
	if s.server.store == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("session not found"))
	}

	principal := principalFromContext(ctx)
	if principal == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("unauthorized"))
	}

	sessionID := req.Msg.SessionId
	session, err := s.server.store.GetSession(sessionID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	if session == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("session not found: %s", sessionID))
	}
	if !principalCanAccessSession(principal, session) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("session not found: %s", sessionID))
	}

	messageLimit := int(req.Msg.MessageLimit)
	if messageLimit == 0 {
		messageLimit = 50
	}

	messages, _ := s.server.store.GetMessages(sessionID, messageLimit, 0)
	todos, _ := s.server.store.GetTodos(sessionID)

	detail := &ipcpb.SessionDetail{
		Session: &ipcpb.SessionSummary{
			Id:           session.ID,
			ProjectPath:  session.ProjectPath,
			GitRepo:      session.GitRepo,
			GitBranch:    session.GitBranch,
			Status:       session.Status,
			CreatedAt:    timestamppb.New(session.CreatedAt),
			LastActive:   timestamppb.New(session.LastActive),
			MessageCount: clampInt32(len(messages)),
			TodoCount:    clampInt32(len(todos)),
		},
	}

	for _, msg := range messages {
		detail.RecentMessages = append(detail.RecentMessages, &ipcpb.Message{
			Id:        strconv.FormatInt(msg.ID, 10),
			Role:      msg.Role,
			Content:   msg.Content,
			Timestamp: timestamppb.New(msg.Timestamp),
		})
	}

	for _, todo := range todos {
		detail.Todos = append(detail.Todos, &ipcpb.Todo{
			Id:         strconv.FormatInt(todo.ID, 10),
			Content:    todo.Content,
			Status:     todo.Status,
			OrderIndex: clampInt32(todo.OrderIndex),
			CreatedAt:  timestamppb.New(todo.CreatedAt),
			UpdatedAt:  timestamppb.New(todo.UpdatedAt),
		})
	}

	return connect.NewResponse(detail), nil
}

// =============================================================================
// Headless Sessions
// =============================================================================

func (s *GRPCService) CreateHeadlessSession(
	ctx context.Context,
	req *connect.Request[ipcpb.CreateHeadlessRequest],
) (*connect.Response[ipcpb.HeadlessSession], error) {
	if err := requireGRPCScope(ctx, storage.TokenScopeMember); err != nil {
		return nil, err
	}
	if s.server.headlessRegistry == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("headless sessions not enabled"))
	}

	principal := ""
	if p := principalFromContext(ctx); p != nil {
		principal = strings.TrimSpace(p.Name)
	}

	project := strings.TrimSpace(req.Msg.Project)
	if project != "" && headless.IsGitURL(project) {
		if parsed, err := url.Parse(project); err == nil && strings.EqualFold(strings.TrimSpace(parsed.Scheme), "file") {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("file:// git URLs are not supported; provide a local path instead"))
		}
		policy := giturl.ClonePolicy{}
		if s.server.appConfig != nil {
			policy = s.server.appConfig.GitClone
		}
		if err := giturl.ValidateCloneURLWithContext(ctx, policy, project); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("git clone blocked by policy: %w", err))
		}
	}

	createReq := headless.CreateSessionRequest{
		Principal: principal,
		Project:   project,
		Branch:    req.Msg.Branch,
		Env:       req.Msg.Env,
		Prompt:    req.Msg.InitialPrompt,
		Model:     req.Msg.Model,
	}
	if req.Msg.Limits != nil {
		createReq.Limits = &headless.ResourceLimits{
			CPU:            req.Msg.Limits.Cpu,
			Memory:         req.Msg.Limits.Memory,
			Storage:        req.Msg.Limits.Storage,
			TimeoutSeconds: req.Msg.Limits.TimeoutSeconds,
		}
	}
	if req.Msg.ToolPolicy != nil {
		createReq.ToolPolicy = &headless.ToolPolicy{
			AllowedTools:       req.Msg.ToolPolicy.AllowedTools,
			DeniedTools:        req.Msg.ToolPolicy.DeniedTools,
			RequireApproval:    req.Msg.ToolPolicy.RequireApproval,
			MaxExecTimeSeconds: req.Msg.ToolPolicy.MaxExecTimeSeconds,
			MaxFileSizeBytes:   req.Msg.ToolPolicy.MaxFileSizeBytes,
		}
	}

	info, err := s.server.headlessRegistry.CreateSession(createReq)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&ipcpb.HeadlessSession{
		Id:        info.ID,
		Status:    headlessStatusFromRunnerState(info.State),
		Project:   info.Project,
		Branch:    info.Branch,
		CreatedAt: timestamppb.New(info.CreatedAt),
	}), nil
}

func (s *GRPCService) DeleteHeadlessSession(
	ctx context.Context,
	req *connect.Request[ipcpb.DeleteHeadlessRequest],
) (*connect.Response[emptypb.Empty], error) {
	if err := requireGRPCScope(ctx, storage.TokenScopeMember); err != nil {
		return nil, err
	}
	if s.server.headlessRegistry == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("headless sessions not enabled"))
	}
	if s.server.store == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("storage unavailable"))
	}

	principal := principalFromContext(ctx)
	if principal == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("unauthorized"))
	}

	sessionID := strings.TrimSpace(req.Msg.SessionId)
	if sessionID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("missing session id"))
	}

	session, err := s.server.store.GetSession(sessionID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if session == nil || !principalCanAccessSession(principal, session) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("session not found"))
	}

	sessionToken := strings.TrimSpace(req.Header().Get("X-Buckley-Session-Token"))
	if !s.server.validateSessionTokenValue(sessionID, sessionToken) {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid session token"))
	}

	if reg, ok := s.server.headlessRegistry.(interface {
		RemoveSessionWithCleanup(sessionID string, cleanupWorkspace bool) error
	}); ok {
		err = reg.RemoveSessionWithCleanup(sessionID, req.Msg.CleanupWorkspace)
	} else {
		err = s.server.headlessRegistry.RemoveSession(sessionID)
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}

	return connect.NewResponse(&emptypb.Empty{}), nil
}

func (s *GRPCService) ListHeadlessSessions(
	ctx context.Context,
	req *connect.Request[emptypb.Empty],
) (*connect.Response[ipcpb.HeadlessSessionList], error) {
	if err := requireGRPCScope(ctx, storage.TokenScopeViewer); err != nil {
		return nil, err
	}
	if s.server.headlessRegistry == nil {
		return connect.NewResponse(&ipcpb.HeadlessSessionList{}), nil
	}

	principal := principalFromContext(ctx)
	if principal == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("unauthorized"))
	}

	sessions := s.server.headlessRegistry.ListSessions()
	var list []*ipcpb.HeadlessSession
	for _, sess := range sessions {
		if s.server.store != nil {
			stored, err := s.server.store.GetSession(sess.ID)
			if err != nil || stored == nil || !principalCanAccessSession(principal, stored) {
				continue
			}
		}
		list = append(list, &ipcpb.HeadlessSession{
			Id:        sess.ID,
			Status:    headlessStatusFromRunnerState(sess.State),
			Project:   sess.Project,
			Branch:    sess.Branch,
			CreatedAt: timestamppb.New(sess.CreatedAt),
		})
	}

	return connect.NewResponse(&ipcpb.HeadlessSessionList{Sessions: list}), nil
}

func headlessStatusFromRunnerState(state headless.RunnerState) string {
	switch state {
	case headless.StateStopped:
		return "completed"
	case headless.StateError:
		return "failed"
	default:
		return "running"
	}
}

// =============================================================================
// Workflow
// =============================================================================

func (s *GRPCService) WorkflowAction(
	ctx context.Context,
	req *connect.Request[ipcpb.WorkflowActionRequest],
) (*connect.Response[ipcpb.WorkflowActionResponse], error) {
	if err := requireGRPCScope(ctx, storage.TokenScopeMember); err != nil {
		return nil, err
	}

	if s.server == nil {
		return connect.NewResponse(&ipcpb.WorkflowActionResponse{
			Status:  "rejected",
			Message: "server unavailable",
		}), nil
	}

	msg := req.Msg
	sessionID := strings.TrimSpace(msg.SessionId)
	if sessionID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("missing session id"))
	}
	action := strings.ToLower(strings.TrimSpace(msg.Action))
	if action == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("action required"))
	}

	if s.server.store == nil {
		return connect.NewResponse(&ipcpb.WorkflowActionResponse{
			Status:  "rejected",
			Message: "storage unavailable",
		}), nil
	}

	principal := principalFromContext(ctx)
	if principal == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("unauthorized"))
	}

	session, err := s.server.store.GetSession(sessionID)
	if err != nil {
		return connect.NewResponse(&ipcpb.WorkflowActionResponse{
			Status:  "rejected",
			Message: "failed to load session",
			PlanId:  msg.PlanId,
			TaskId:  msg.TaskId,
		}), nil
	}
	if session == nil || !principalCanAccessSession(principal, session) {
		return connect.NewResponse(&ipcpb.WorkflowActionResponse{
			Status:  "rejected",
			Message: "session not found",
			PlanId:  msg.PlanId,
			TaskId:  msg.TaskId,
		}), nil
	}

	sessionToken := strings.TrimSpace(req.Header().Get("X-Buckley-Session-Token"))
	if !s.server.validateSessionTokenValue(sessionID, sessionToken) {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid session token"))
	}

	if s.server.commandLimiter != nil && !s.server.commandLimiter.Allow(sessionID) {
		return connect.NewResponse(&ipcpb.WorkflowActionResponse{
			Status:  "rejected",
			Message: "rate limit exceeded",
		}), nil
	}

	dispatch := func(content string) error {
		cmd := command.SessionCommand{
			SessionID: sessionID,
			Type:      "slash",
			Content:   content,
		}

		if s.server.headlessRegistry != nil {
			if err := s.server.headlessRegistry.DispatchCommand(cmd); err == nil {
				return nil
			}
		}

		if s.server.commandGW != nil {
			return s.server.commandGW.Dispatch(cmd)
		}
		return fmt.Errorf("no command handler available")
	}

	if action == "execute" {
		planID := strings.TrimSpace(msg.PlanId)
		taskID := strings.TrimSpace(msg.TaskId)
		if planID != "" {
			if err := dispatch(fmt.Sprintf("/resume %s", planID)); err != nil {
				return connect.NewResponse(&ipcpb.WorkflowActionResponse{
					Status:  "rejected",
					Message: err.Error(),
					PlanId:  planID,
					TaskId:  taskID,
				}), nil
			}
		}

		execCmd := "/execute"
		if taskID != "" {
			execCmd = fmt.Sprintf("/execute %s", taskID)
		}
		if err := dispatch(execCmd); err != nil {
			return connect.NewResponse(&ipcpb.WorkflowActionResponse{
				Status:  "rejected",
				Message: err.Error(),
				PlanId:  planID,
				TaskId:  taskID,
			}), nil
		}

		return connect.NewResponse(&ipcpb.WorkflowActionResponse{
			Status:  "accepted",
			Message: "Action execute queued",
			PlanId:  planID,
			TaskId:  taskID,
		}), nil
	}

	workflowReq := workflowActionRequest{
		Action:      msg.Action,
		FeatureName: msg.Feature,
		Description: msg.Description,
		PlanID:      msg.PlanId,
		Note:        msg.Note,
	}
	cmd, err := buildWorkflowCommand(workflowReq)
	if err != nil {
		return connect.NewResponse(&ipcpb.WorkflowActionResponse{
			Status:  "rejected",
			Message: err.Error(),
			PlanId:  msg.PlanId,
			TaskId:  msg.TaskId,
		}), nil
	}
	if err := dispatch(cmd); err != nil {
		return connect.NewResponse(&ipcpb.WorkflowActionResponse{
			Status:  "rejected",
			Message: err.Error(),
			PlanId:  msg.PlanId,
			TaskId:  msg.TaskId,
		}), nil
	}

	return connect.NewResponse(&ipcpb.WorkflowActionResponse{
		Status:  "accepted",
		Message: fmt.Sprintf("Action %s queued", action),
		PlanId:  msg.PlanId,
		TaskId:  msg.TaskId,
	}), nil
}

// =============================================================================
// Host Agent Management
// =============================================================================

func (s *GRPCService) RegisterAgent(
	ctx context.Context,
	req *connect.Request[ipcpb.RegisterAgentRequest],
	stream *connect.ServerStream[ipcpb.AgentCommand],
) error {
	if err := requireGRPCScope(ctx, storage.TokenScopeOperator); err != nil {
		return err
	}
	msg := req.Msg
	agentID := msg.AgentId
	if agentID == "" {
		agentID = fmt.Sprintf("agent-%d", time.Now().UnixNano())
	}

	agentCtx, cancel := context.WithCancel(ctx)
	agent := &connectedAgent{
		info: &ipcpb.AgentInfo{
			AgentId:       agentID,
			Name:          msg.Name,
			Hostname:      msg.Hostname,
			Capabilities:  msg.Capabilities,
			Status:        "connected",
			ConnectedAt:   timestamppb.Now(),
			LastHeartbeat: timestamppb.Now(),
			Os:            msg.Os,
			Arch:          msg.Arch,
			Version:       msg.Version,
		},
		commands: make(chan *ipcpb.AgentCommand, 32),
		results:  make(chan *ipcpb.AgentResult, 32),
		cancel:   cancel,
	}

	s.agentsMu.Lock()
	s.agents[agentID] = agent
	s.agentsMu.Unlock()

	defer func() {
		s.agentsMu.Lock()
		delete(s.agents, agentID)
		s.agentsMu.Unlock()
		cancel()
	}()

	// Broadcast agent connected event
	s.server.hub.Broadcast(Event{
		Type:      "agent.connected",
		Payload:   map[string]any{"agent_id": agentID, "hostname": msg.Hostname},
		Timestamp: time.Now(),
	})

	// Stream commands to agent
	for {
		select {
		case <-agentCtx.Done():
			return agentCtx.Err()
		case cmd := <-agent.commands:
			if err := stream.Send(cmd); err != nil {
				return err
			}
		}
	}
}

func (s *GRPCService) ReportAgentResult(
	ctx context.Context,
	req *connect.Request[ipcpb.AgentResult],
) (*connect.Response[emptypb.Empty], error) {
	if err := requireGRPCScope(ctx, storage.TokenScopeOperator); err != nil {
		return nil, err
	}
	msg := req.Msg

	s.agentsMu.RLock()
	agent, ok := s.agents[msg.AgentId]
	s.agentsMu.RUnlock()

	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("agent not found: %s", msg.AgentId))
	}

	// Forward result to waiting handler
	select {
	case agent.results <- msg:
	default:
		// Result channel full, log and continue
	}

	// Broadcast result event
	s.server.hub.Broadcast(Event{
		Type:      "agent.result",
		SessionID: agent.sessionID,
		Payload: map[string]any{
			"agent_id":   msg.AgentId,
			"command_id": msg.CommandId,
			"success":    msg.Success,
			"exit_code":  msg.ExitCode,
		},
		Timestamp: time.Now(),
	})

	return connect.NewResponse(&emptypb.Empty{}), nil
}

func (s *GRPCService) AgentHeartbeat(
	ctx context.Context,
	req *connect.Request[ipcpb.AgentHeartbeatRequest],
) (*connect.Response[ipcpb.AgentHeartbeatResponse], error) {
	if err := requireGRPCScope(ctx, storage.TokenScopeOperator); err != nil {
		return nil, err
	}
	msg := req.Msg

	s.agentsMu.Lock()
	agent, ok := s.agents[msg.AgentId]
	if ok {
		agent.info.LastHeartbeat = timestamppb.Now()
		agent.info.ActiveCommands = msg.ActiveCommands
	}
	s.agentsMu.Unlock()

	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("agent not found"))
	}

	return connect.NewResponse(&ipcpb.AgentHeartbeatResponse{Ok: true}), nil
}

func (s *GRPCService) ListAgents(
	ctx context.Context,
	req *connect.Request[emptypb.Empty],
) (*connect.Response[ipcpb.AgentList], error) {
	if err := requireGRPCScope(ctx, storage.TokenScopeOperator); err != nil {
		return nil, err
	}
	s.agentsMu.RLock()
	defer s.agentsMu.RUnlock()

	var list []*ipcpb.AgentInfo
	for _, agent := range s.agents {
		list = append(list, agent.info)
	}

	return connect.NewResponse(&ipcpb.AgentList{Agents: list}), nil
}

// sendAgentCommand routes a command to a connected agent.
func (s *GRPCService) sendAgentCommand(
	ctx context.Context,
	req *ipcpb.CommandRequest,
) (*connect.Response[ipcpb.CommandResponse], error) {
	s.agentsMu.RLock()
	agent, ok := s.agents[req.AgentId]
	s.agentsMu.RUnlock()

	if !ok {
		return connect.NewResponse(&ipcpb.CommandResponse{
			Status:  "rejected",
			Message: fmt.Sprintf("Agent not connected: %s", req.AgentId),
		}), nil
	}

	cmdID := fmt.Sprintf("cmd-%d", time.Now().UnixNano())
	cmd := &ipcpb.AgentCommand{
		CommandId: cmdID,
		SessionId: req.SessionId,
		Command: &ipcpb.AgentCommand_Shell{
			Shell: &ipcpb.ShellCommand{
				Command:       req.Content,
				CaptureOutput: true,
			},
		},
		TimeoutSeconds: 300,
	}

	select {
	case agent.commands <- cmd:
		return connect.NewResponse(&ipcpb.CommandResponse{
			Status:    "accepted",
			Message:   "Command sent to agent",
			CommandId: cmdID,
		}), nil
	default:
		return connect.NewResponse(&ipcpb.CommandResponse{
			Status:  "rejected",
			Message: "Agent command queue full",
		}), nil
	}
}

// =============================================================================
// Plans
// =============================================================================

func (s *GRPCService) ListPlans(
	ctx context.Context,
	req *connect.Request[ipcpb.ListPlansRequest],
) (*connect.Response[ipcpb.ListPlansResponse], error) {
	if err := requireGRPCScope(ctx, storage.TokenScopeViewer); err != nil {
		return nil, err
	}
	if s.server.planStore == nil {
		return connect.NewResponse(&ipcpb.ListPlansResponse{}), nil
	}
	principal := principalFromContext(ctx)
	if principal == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("unauthorized"))
	}

	plans, err := s.server.planStore.ListPlans()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	var allowed map[string]struct{}
	if !isOperatorPrincipal(principal) {
		if s.server.store == nil {
			return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("storage unavailable"))
		}
		allowed, err = s.server.store.ListPlanIDsForPrincipal(principal.Name)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}

	var summaries []*ipcpb.PlanSummary
	for i := range plans {
		plan := &plans[i]
		if allowed != nil {
			if _, ok := allowed[plan.ID]; !ok {
				continue
			}
		}
		completedTasks := 0
		for _, task := range plan.Tasks {
			if task.Status == 2 { // Completed
				completedTasks++
			}
		}
		summaries = append(summaries, &ipcpb.PlanSummary{
			Id:             plan.ID,
			FeatureName:    plan.FeatureName,
			Description:    plan.Description,
			Status:         planStatusToString(plan),
			TotalTasks:     clampInt32(len(plan.Tasks)),
			CompletedTasks: clampInt32(completedTasks),
		})
	}

	return connect.NewResponse(&ipcpb.ListPlansResponse{
		Plans:      summaries,
		TotalCount: clampInt32(len(summaries)),
	}), nil
}

func (s *GRPCService) GetPlan(
	ctx context.Context,
	req *connect.Request[ipcpb.GetPlanRequest],
) (*connect.Response[ipcpb.Plan], error) {
	if err := requireGRPCScope(ctx, storage.TokenScopeViewer); err != nil {
		return nil, err
	}
	if s.server.planStore == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("plan store unavailable"))
	}
	principal := principalFromContext(ctx)
	if principal == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("unauthorized"))
	}

	planID := req.Msg.PlanId
	if planID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("plan_id required"))
	}

	if !isOperatorPrincipal(principal) {
		if s.server.store == nil {
			return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("storage unavailable"))
		}
		allowed, err := s.server.store.PrincipalHasPlan(principal.Name, planID)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		if !allowed {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("plan not found"))
		}
	}

	plan, err := s.server.planStore.LoadPlan(planID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if plan == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("plan not found: %s", planID))
	}

	var tasks []*ipcpb.PlanTask
	for i, task := range plan.Tasks {
		tasks = append(tasks, &ipcpb.PlanTask{
			Id:          task.ID,
			Title:       task.Title,
			Description: task.Description,
			Status:      taskStatusToString(task.Status),
			OrderIndex:  clampInt32(i),
			CreatedAt:   timestamppb.New(plan.CreatedAt),
		})
	}

	return connect.NewResponse(&ipcpb.Plan{
		Id:          plan.ID,
		FeatureName: plan.FeatureName,
		Description: plan.Description,
		Status:      planStatusToString(plan),
		Tasks:       tasks,
		CreatedAt:   timestamppb.New(plan.CreatedAt),
	}), nil
}

func planStatusToString(plan *orchestrator.Plan) string {
	if plan == nil || len(plan.Tasks) == 0 {
		return "pending"
	}
	allCompleted := true
	anyInProgress := false
	anyFailed := false
	for _, task := range plan.Tasks {
		switch task.Status {
		case 1: // InProgress
			anyInProgress = true
			allCompleted = false
		case 2: // Completed
			// continue
		case 3: // Failed
			anyFailed = true
			allCompleted = false
		default:
			allCompleted = false
		}
	}
	if anyFailed {
		return "failed"
	}
	if allCompleted {
		return "completed"
	}
	if anyInProgress {
		return "in_progress"
	}
	return "pending"
}

func taskStatusToString(status orchestrator.TaskStatus) string {
	switch status {
	case 0:
		return "pending"
	case 1:
		return "in_progress"
	case 2:
		return "completed"
	case 3:
		return "failed"
	default:
		return "pending"
	}
}

// =============================================================================
// Projects
// =============================================================================

func (s *GRPCService) ListProjects(
	ctx context.Context,
	req *connect.Request[emptypb.Empty],
) (*connect.Response[ipcpb.ProjectList], error) {
	if err := requireGRPCScope(ctx, storage.TokenScopeViewer); err != nil {
		return nil, err
	}
	if s.server.store == nil {
		return connect.NewResponse(&ipcpb.ProjectList{}), nil
	}

	principal := principalFromContext(ctx)
	if principal == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("unauthorized"))
	}

	projects, err := s.server.collectProjects(ctx, principal)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	var list []*ipcpb.Project
	for _, proj := range projects {
		list = append(list, &ipcpb.Project{
			Slug:         proj.Slug,
			Name:         proj.Name,
			Path:         proj.Path,
			SessionCount: clampInt32(proj.SessionCount),
			LastActive:   timestamppb.New(proj.LastActive),
		})
	}

	return connect.NewResponse(&ipcpb.ProjectList{Projects: list}), nil
}

func (s *GRPCService) CreateProject(
	ctx context.Context,
	req *connect.Request[ipcpb.CreateProjectRequest],
) (*connect.Response[ipcpb.Project], error) {
	if err := requireGRPCScope(ctx, storage.TokenScopeMember); err != nil {
		return nil, err
	}
	if s.server.store == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("storage unavailable"))
	}

	projectRoot := strings.TrimSpace(s.server.projectRoot)
	if projectRoot == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("project root not configured"))
	}

	name := strings.TrimSpace(req.Msg.Name)
	if name == "" {
		name = fmt.Sprintf("project-%d", time.Now().Unix())
	}

	slug := slugifyProjectName(name)
	if slug == "" {
		slug = fmt.Sprintf("project-%d", time.Now().Unix())
	}

	targetDir := projectRoot + "/" + slug
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create project: %w", err))
	}

	return connect.NewResponse(&ipcpb.Project{
		Slug: slug,
		Name: name,
		Path: targetDir,
	}), nil
}

// =============================================================================
// Personas
// =============================================================================

func (s *GRPCService) ListPersonas(
	ctx context.Context,
	req *connect.Request[emptypb.Empty],
) (*connect.Response[ipcpb.PersonaList], error) {
	if err := requireGRPCScope(ctx, storage.TokenScopeOperator); err != nil {
		return nil, err
	}

	provider := s.server.resolvePersonaProvider()
	if provider == nil {
		return connect.NewResponse(&ipcpb.PersonaList{}), nil
	}

	profiles := provider.Profiles()
	var list []*ipcpb.Persona
	for _, profile := range profiles {
		list = append(list, &ipcpb.Persona{
			Id:          profile.ID,
			Name:        profile.Name,
			Description: profile.Description,
			Tone:        profile.Style.Tone,
			Active:      false, // Would need to check overrides
		})
	}

	return connect.NewResponse(&ipcpb.PersonaList{Personas: list}), nil
}

// =============================================================================
// Tool Approvals
// =============================================================================

func (s *GRPCService) ListPendingApprovals(
	ctx context.Context,
	req *connect.Request[ipcpb.ListPendingApprovalsRequest],
) (*connect.Response[ipcpb.PendingApprovalsList], error) {
	if err := requireGRPCScope(ctx, storage.TokenScopeViewer); err != nil {
		return nil, err
	}
	if s.server.store == nil {
		return connect.NewResponse(&ipcpb.PendingApprovalsList{}), nil
	}

	principal := principalFromContext(ctx)
	if principal == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("unauthorized"))
	}

	approvals, err := s.server.store.ListPendingApprovals(req.Msg.SessionId)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	sessionCache := make(map[string]*storage.Session)

	var list []*ipcpb.PendingApproval
	for _, approval := range approvals {
		session, ok := sessionCache[approval.SessionID]
		if !ok {
			session, err = s.server.store.GetSession(approval.SessionID)
			if err != nil {
				continue
			}
			sessionCache[approval.SessionID] = session
		}
		if session == nil || !principalCanAccessSession(principal, session) {
			continue
		}

		toolInput := payloadToStruct(json.RawMessage(approval.ToolInput))
		rich := extractApprovalRichFields(approval.ToolName, approval.ToolInput)
		diffLines := approvalDiffLinesToProto(rich.diffLines)
		list = append(list, &ipcpb.PendingApproval{
			Id:            approval.ID,
			SessionId:     approval.SessionID,
			ToolName:      approval.ToolName,
			ToolInput:     toolInput,
			RiskScore:     clampInt32(approval.RiskScore),
			RiskReasons:   approval.RiskReasons,
			Status:        approval.Status,
			ExpiresAt:     timestamppb.New(approval.ExpiresAt),
			CreatedAt:     timestamppb.New(approval.CreatedAt),
			OperationType: rich.operationType,
			Description:   rich.description,
			Command:       rich.command,
			FilePath:      rich.filePath,
			DiffLines:     diffLines,
			AddedLines:    rich.addedLines,
			RemovedLines:  rich.removedLines,
		})
	}

	return connect.NewResponse(&ipcpb.PendingApprovalsList{Approvals: list}), nil
}

func (s *GRPCService) ApproveToolCall(
	ctx context.Context,
	req *connect.Request[ipcpb.ApproveToolCallRequest],
) (*connect.Response[ipcpb.ApproveToolCallResponse], error) {
	if err := requireGRPCScope(ctx, storage.TokenScopeMember); err != nil {
		return nil, err
	}
	if s.server.store == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("storage unavailable"))
	}

	principal := principalFromContext(ctx)
	if principal == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("unauthorized"))
	}

	approval, err := s.server.store.GetPendingApproval(req.Msg.ApprovalId)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if approval == nil {
		return connect.NewResponse(&ipcpb.ApproveToolCallResponse{
			Success: false,
			Message: "Approval not found",
		}), nil
	}

	session, err := s.server.store.GetSession(approval.SessionID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if session == nil || !principalCanAccessSession(principal, session) {
		return connect.NewResponse(&ipcpb.ApproveToolCallResponse{
			Success: false,
			Message: "Approval not found",
		}), nil
	}

	if approval.Status == "pending" && !approval.ExpiresAt.IsZero() && time.Now().After(approval.ExpiresAt) {
		approval.Status = "expired"
		approval.DecidedBy = ""
		approval.DecidedAt = time.Now()
		approval.DecisionReason = "timeout"
		if err := s.server.store.UpdatePendingApproval(approval); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		return connect.NewResponse(&ipcpb.ApproveToolCallResponse{
			Success: false,
			Message: "Approval expired",
		}), nil
	}
	if approval.Status != "pending" {
		if approval.Status == "approved" {
			payload, _ := json.Marshal(headless.ApprovalResponse{ID: approval.ID, Approved: true})
			cmd := command.SessionCommand{
				SessionID: approval.SessionID,
				Type:      "approval",
				Content:   string(payload),
			}
			if err := s.dispatchSessionCommand(cmd); err != nil {
				return connect.NewResponse(&ipcpb.ApproveToolCallResponse{
					Success: true,
					Message: fmt.Sprintf("Tool call already approved, but failed to notify session: %v", err),
				}), nil
			}
			return connect.NewResponse(&ipcpb.ApproveToolCallResponse{
				Success: true,
				Message: "Tool call already approved",
			}), nil
		}
		return connect.NewResponse(&ipcpb.ApproveToolCallResponse{
			Success: false,
			Message: fmt.Sprintf("Approval already %s", approval.Status),
		}), nil
	}

	// Get principal for audit
	decidedBy := "unknown"
	if principal := principalFromContext(ctx); principal != nil {
		decidedBy = principal.Name
	}

	approval.Status = "approved"
	approval.DecidedBy = decidedBy
	approval.DecidedAt = time.Now()

	if err := s.server.store.UpdatePendingApproval(approval); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Broadcast approval event
	s.server.hub.Broadcast(Event{
		Type:      "approval.decided",
		SessionID: approval.SessionID,
		Payload: map[string]any{
			"approval_id": approval.ID,
			"status":      "approved",
			"decided_by":  decidedBy,
		},
		Timestamp: time.Now(),
	})

	payload, _ := json.Marshal(headless.ApprovalResponse{ID: approval.ID, Approved: true})
	cmd := command.SessionCommand{
		SessionID: approval.SessionID,
		Type:      "approval",
		Content:   string(payload),
	}
	if err := s.dispatchSessionCommand(cmd); err != nil {
		return connect.NewResponse(&ipcpb.ApproveToolCallResponse{
			Success: true,
			Message: fmt.Sprintf("Tool call approved, but failed to notify session: %v", err),
		}), nil
	}

	return connect.NewResponse(&ipcpb.ApproveToolCallResponse{
		Success: true,
		Message: "Tool call approved",
	}), nil
}

func (s *GRPCService) RejectToolCall(
	ctx context.Context,
	req *connect.Request[ipcpb.RejectToolCallRequest],
) (*connect.Response[ipcpb.RejectToolCallResponse], error) {
	if err := requireGRPCScope(ctx, storage.TokenScopeMember); err != nil {
		return nil, err
	}
	if s.server.store == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("storage unavailable"))
	}

	principal := principalFromContext(ctx)
	if principal == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("unauthorized"))
	}

	approval, err := s.server.store.GetPendingApproval(req.Msg.ApprovalId)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if approval == nil {
		return connect.NewResponse(&ipcpb.RejectToolCallResponse{
			Success: false,
			Message: "Approval not found",
		}), nil
	}

	session, err := s.server.store.GetSession(approval.SessionID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if session == nil || !principalCanAccessSession(principal, session) {
		return connect.NewResponse(&ipcpb.RejectToolCallResponse{
			Success: false,
			Message: "Approval not found",
		}), nil
	}

	if approval.Status == "pending" && !approval.ExpiresAt.IsZero() && time.Now().After(approval.ExpiresAt) {
		approval.Status = "expired"
		approval.DecidedBy = ""
		approval.DecidedAt = time.Now()
		approval.DecisionReason = "timeout"
		if err := s.server.store.UpdatePendingApproval(approval); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		return connect.NewResponse(&ipcpb.RejectToolCallResponse{
			Success: false,
			Message: "Approval expired",
		}), nil
	}
	if approval.Status != "pending" {
		if approval.Status == "rejected" {
			reason := strings.TrimSpace(approval.DecisionReason)
			if reason == "" {
				reason = strings.TrimSpace(req.Msg.Reason)
			}
			payload, _ := json.Marshal(headless.ApprovalResponse{
				ID:       approval.ID,
				Approved: false,
				Reason:   reason,
			})
			cmd := command.SessionCommand{
				SessionID: approval.SessionID,
				Type:      "approval",
				Content:   string(payload),
			}
			if err := s.dispatchSessionCommand(cmd); err != nil {
				return connect.NewResponse(&ipcpb.RejectToolCallResponse{
					Success: true,
					Message: fmt.Sprintf("Tool call already rejected, but failed to notify session: %v", err),
				}), nil
			}
			return connect.NewResponse(&ipcpb.RejectToolCallResponse{
				Success: true,
				Message: "Tool call already rejected",
			}), nil
		}
		return connect.NewResponse(&ipcpb.RejectToolCallResponse{
			Success: false,
			Message: fmt.Sprintf("Approval already %s", approval.Status),
		}), nil
	}

	// Get principal for audit
	decidedBy := "unknown"
	if principal := principalFromContext(ctx); principal != nil {
		decidedBy = principal.Name
	}

	approval.Status = "rejected"
	approval.DecidedBy = decidedBy
	approval.DecidedAt = time.Now()
	approval.DecisionReason = strings.TrimSpace(req.Msg.Reason)

	if err := s.server.store.UpdatePendingApproval(approval); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	reason := strings.TrimSpace(req.Msg.Reason)

	// Broadcast rejection event
	s.server.hub.Broadcast(Event{
		Type:      "approval.decided",
		SessionID: approval.SessionID,
		Payload: map[string]any{
			"approval_id": approval.ID,
			"status":      "rejected",
			"decided_by":  decidedBy,
			"reason":      reason,
		},
		Timestamp: time.Now(),
	})

	payload, _ := json.Marshal(headless.ApprovalResponse{
		ID:       approval.ID,
		Approved: false,
		Reason:   reason,
	})
	cmd := command.SessionCommand{
		SessionID: approval.SessionID,
		Type:      "approval",
		Content:   string(payload),
	}
	if err := s.dispatchSessionCommand(cmd); err != nil {
		return connect.NewResponse(&ipcpb.RejectToolCallResponse{
			Success: true,
			Message: fmt.Sprintf("Tool call rejected, but failed to notify session: %v", err),
		}), nil
	}

	return connect.NewResponse(&ipcpb.RejectToolCallResponse{
		Success: true,
		Message: "Tool call rejected",
	}), nil
}

func (s *GRPCService) GetApprovalPolicy(
	ctx context.Context,
	req *connect.Request[emptypb.Empty],
) (*connect.Response[ipcpb.ApprovalPolicy], error) {
	if err := requireGRPCScope(ctx, storage.TokenScopeViewer); err != nil {
		return nil, err
	}
	if s.server.store == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("storage unavailable"))
	}

	policy, err := s.server.store.GetActivePolicy()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if policy == nil {
		// Return default policy info
		return connect.NewResponse(&ipcpb.ApprovalPolicy{
			Name:     "default",
			IsActive: true,
			Config:   "{}",
		}), nil
	}

	return connect.NewResponse(&ipcpb.ApprovalPolicy{
		Id:        policy.ID,
		Name:      policy.Name,
		IsActive:  policy.IsActive,
		Config:    policy.Config,
		CreatedAt: timestamppb.New(policy.CreatedAt),
		UpdatedAt: timestamppb.New(policy.UpdatedAt),
	}), nil
}

func (s *GRPCService) UpdateApprovalPolicy(
	ctx context.Context,
	req *connect.Request[ipcpb.UpdateApprovalPolicyRequest],
) (*connect.Response[ipcpb.ApprovalPolicy], error) {
	if err := requireGRPCScope(ctx, storage.TokenScopeOperator); err != nil {
		return nil, err
	}
	if s.server.store == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("storage unavailable"))
	}

	policy := &storage.ApprovalPolicy{
		Name:     req.Msg.Name,
		IsActive: req.Msg.Activate,
		Config:   req.Msg.Config,
	}

	if err := s.server.store.SavePolicy(policy); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&ipcpb.ApprovalPolicy{
		Id:        policy.ID,
		Name:      policy.Name,
		IsActive:  policy.IsActive,
		Config:    policy.Config,
		CreatedAt: timestamppb.New(policy.CreatedAt),
		UpdatedAt: timestamppb.New(policy.UpdatedAt),
	}), nil
}

func (s *GRPCService) GetAuditLog(
	ctx context.Context,
	req *connect.Request[ipcpb.GetAuditLogRequest],
) (*connect.Response[ipcpb.AuditLogResponse], error) {
	if err := requireGRPCScope(ctx, storage.TokenScopeViewer); err != nil {
		return nil, err
	}
	if s.server.store == nil {
		return connect.NewResponse(&ipcpb.AuditLogResponse{}), nil
	}

	principal := principalFromContext(ctx)
	if principal == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("unauthorized"))
	}

	limit := int(req.Msg.Limit)
	if limit <= 0 {
		limit = 100
	}

	sessionID := strings.TrimSpace(req.Msg.SessionId)
	if sessionID != "" {
		session, err := s.server.store.GetSession(sessionID)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		if session == nil || !principalCanAccessSession(principal, session) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("session not found"))
		}
	}

	entries, err := s.server.store.GetAuditLog(sessionID, limit)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	var list []*ipcpb.AuditEntry
	for _, entry := range entries {
		list = append(list, &ipcpb.AuditEntry{
			Id:         entry.ID,
			SessionId:  entry.SessionID,
			ApprovalId: entry.ApprovalID,
			ToolName:   entry.ToolName,
			ToolInput:  entry.ToolInput,
			ToolOutput: entry.ToolOutput,
			RiskScore:  clampInt32(entry.RiskScore),
			Decision:   entry.Decision,
			DecidedBy:  entry.DecidedBy,
			ExecutedAt: timestamppb.New(entry.ExecutedAt),
			DurationMs: entry.DurationMs,
		})
	}

	return connect.NewResponse(&ipcpb.AuditLogResponse{Entries: list}), nil
}

func clampInt32(value int) int32 {
	if value > math.MaxInt32 {
		return math.MaxInt32
	}
	if value < math.MinInt32 {
		return math.MinInt32
	}
	return int32(value)
}

// =============================================================================
// Push Notifications
// =============================================================================

func (s *GRPCService) SubscribePush(
	ctx context.Context,
	req *connect.Request[ipcpb.PushSubscriptionRequest],
) (*connect.Response[ipcpb.PushSubscriptionResponse], error) {
	if err := requireGRPCScope(ctx, storage.TokenScopeMember); err != nil {
		return nil, err
	}
	if s.server.store == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("storage unavailable"))
	}

	// Get principal for subscription
	principal := "anonymous"
	if p := principalFromContext(ctx); p != nil && p.Name != "" {
		principal = p.Name
	}

	endpoint := strings.TrimSpace(req.Msg.Endpoint)
	p256dh := strings.TrimSpace(req.Msg.P256Dh)
	auth := strings.TrimSpace(req.Msg.Auth)
	if endpoint == "" || p256dh == "" || auth == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("endpoint and keys are required"))
	}

	subID, err := s.server.store.CreatePushSubscription(
		principal,
		endpoint,
		p256dh,
		auth,
		strings.TrimSpace(req.Msg.UserAgent),
	)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&ipcpb.PushSubscriptionResponse{
		Success:        true,
		SubscriptionId: subID,
	}), nil
}

func (s *GRPCService) UnsubscribePush(
	ctx context.Context,
	req *connect.Request[ipcpb.UnsubscribePushRequest],
) (*connect.Response[emptypb.Empty], error) {
	if err := requireGRPCScope(ctx, storage.TokenScopeMember); err != nil {
		return nil, err
	}
	if s.server.store == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("storage unavailable"))
	}

	endpoint := strings.TrimSpace(req.Msg.Endpoint)
	if endpoint == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("endpoint is required"))
	}

	principal := principalFromContext(ctx)
	if principal == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("unauthorized"))
	}

	sub, err := s.server.store.GetPushSubscriptionByEndpoint(endpoint)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if sub == nil {
		return connect.NewResponse(&emptypb.Empty{}), nil
	}
	if sub.Principal != principal.Name && !isOperatorPrincipal(principal) {
		return connect.NewResponse(&emptypb.Empty{}), nil
	}
	if err := s.server.store.DeletePushSubscription(sub.ID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&emptypb.Empty{}), nil
}

func (s *GRPCService) GetVAPIDPublicKey(
	ctx context.Context,
	req *connect.Request[emptypb.Empty],
) (*connect.Response[ipcpb.VAPIDPublicKeyResponse], error) {
	// No auth required for VAPID public key
	if s.server.store == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("storage unavailable"))
	}

	publicKey, err := s.server.store.GetVAPIDPublicKey()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&ipcpb.VAPIDPublicKeyResponse{
		PublicKey: publicKey,
	}), nil
}
