package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	acppb "github.com/odvcencio/buckley/pkg/acp/proto"
	"github.com/odvcencio/buckley/pkg/agent"
	"github.com/odvcencio/buckley/pkg/bus"
	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/coordination/coordinator"
	"github.com/odvcencio/buckley/pkg/coordination/security"
	"github.com/odvcencio/buckley/pkg/mission"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/orchestrator"
	"github.com/odvcencio/buckley/pkg/rlm"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/tool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
)

// Server implements the Zed ACP gRPC service.
type Server struct {
	acppb.UnimplementedAgentCommunicationServer
	coordinator   *coordinator.Coordinator // Buckley-internal coordination (not the ACP protocol).
	models        *model.Manager
	cfg           *config.Config
	store         *storage.Store
	projectRoot   string
	docsRoot      string
	sessions      map[string]*acppb.Session
	toolApprover  *security.ToolApprover
	telemetryHub  *telemetry.Hub
	liveWorkflows map[string]*orchestrator.WorkflowManager
	liveMux       sync.RWMutex

	// MessageBus for agent-to-agent communication
	messageBus   bus.MessageBus
	taskHistory  agent.TaskHistory
	agentSubs    map[string]bus.Subscription
	agentSubsMux sync.RWMutex

	// Capability grants tracking
	grants   map[string]*acppb.CapabilityGrant
	grantMux sync.RWMutex

	// Session context tracking (files and metadata per session)
	sessionContexts   map[string]*SessionContext
	sessionContextMux sync.RWMutex

	// Pending tool approvals
	pendingApprovals   map[string]*PendingApproval
	pendingApprovalMux sync.RWMutex

	// Context handles storage
	contextHandles   map[string]*ContextHandleData
	contextHandleMux sync.RWMutex
}

// SessionContext tracks files and metadata for a session.
type SessionContext struct {
	SessionID string
	Files     map[string]bool // file paths currently in context
	Metadata  map[string]string
	UpdatedAt time.Time
}

// PendingApproval represents a tool execution awaiting approval.
type PendingApproval struct {
	ExecutionID string
	AgentID     string
	Tool        string
	Parameters  map[string]string
	CreatedAt   time.Time
	ResultChan  chan ApprovalResult
}

// ApprovalResult is the outcome of an approval decision.
type ApprovalResult struct {
	Approved bool
	Remember bool
	Reason   string
}

// ContextHandleData stores the actual data for a context handle.
type ContextHandleData struct {
	HandleID  string
	Type      string
	Data      []byte
	CreatedAt time.Time
}

// NewServer creates a new ACP gRPC server
func NewServer(coord *coordinator.Coordinator, models *model.Manager, cfg *config.Config, store *storage.Store) (*Server, error) {
	if coord == nil {
		return nil, fmt.Errorf("coordinator is required")
	}

	projectRoot := config.ResolveProjectRoot(cfg)
	docsRoot := filepath.Join(projectRoot, "docs")

	telemetryHub := telemetry.NewHub()

	// Default to in-memory bus if none configured
	msgBus := bus.NewMemoryBus()

	// Default to in-memory task history
	taskHistory := agent.NewInMemoryTaskHistory()

	return &Server{
		coordinator:      coord,
		models:           models,
		cfg:              cfg,
		store:            store,
		projectRoot:      projectRoot,
		docsRoot:         docsRoot,
		sessions:         make(map[string]*acppb.Session),
		toolApprover:     security.NewToolApprover(security.DefaultToolPolicy()),
		telemetryHub:     telemetryHub,
		liveWorkflows:    make(map[string]*orchestrator.WorkflowManager),
		messageBus:       msgBus,
		taskHistory:      taskHistory,
		agentSubs:        make(map[string]bus.Subscription),
		grants:           make(map[string]*acppb.CapabilityGrant),
		sessionContexts:  make(map[string]*SessionContext),
		pendingApprovals: make(map[string]*PendingApproval),
		contextHandles:   make(map[string]*ContextHandleData),
	}, nil
}

// SetMessageBus configures the server to use a specific message bus.
func (s *Server) SetMessageBus(b bus.MessageBus) {
	s.messageBus = b
}

// SetTaskHistory configures the server to use a specific task history store.
func (s *Server) SetTaskHistory(h agent.TaskHistory) {
	s.taskHistory = h
}

// GetServerCapabilities returns advertised capabilities.
func (s *Server) GetServerCapabilities(_ context.Context, _ *emptypb.Empty) (*acppb.ServerCapabilities, error) {
	maxAgents := s.coordinator.Config().MaxAgents
	if maxAgents < 0 {
		maxAgents = 0
	}
	if maxAgents > math.MaxInt32 {
		maxAgents = math.MaxInt32
	}
	supportedAuth := []string{"mtls"}
	if s.cfg != nil && s.cfg.ACP.AllowInsecureLocal {
		supportedAuth = append(supportedAuth, "insecure_local")
	}
	return &acppb.ServerCapabilities{
		ProtocolVersion: "1.0",
		Features:        []string{"chat", "stream_task", "tool_approval", "context_handles", "inline_completion", "propose_edits", "apply_edits", "editor_state"},
		// #nosec G115 -- maxAgents is clamped to int32 range above.
		MaxAgents:     int32(maxAgents),
		SupportedAuth: supportedAuth,
	}, nil
}

// GetAgentInfo returns info about a registered agent.
func (s *Server) GetAgentInfo(ctx context.Context, req *acppb.GetAgentInfoRequest) (*acppb.AgentInfo, error) {
	if req.GetAgentId() == "" {
		return nil, statusError(codes.InvalidArgument, "agent_id required")
	}
	agent, err := s.coordinator.GetAgent(ctx, req.AgentId)
	if err != nil {
		return nil, statusError(codes.NotFound, err.Error())
	}
	return &acppb.AgentInfo{
		Id:           agent.ID,
		Type:         agent.Type,
		Endpoint:     agent.Endpoint,
		Capabilities: agent.Capabilities,
		Metadata:     agent.Metadata,
	}, nil
}

// DiscoverAgents proxies to the coordinator.
func (s *Server) DiscoverAgents(ctx context.Context, req *acppb.DiscoverAgentsRequest) (*acppb.DiscoverAgentsResponse, error) {
	agents, err := s.coordinator.DiscoverAgents(ctx, &coordinator.DiscoveryQuery{
		Type:         req.Type,
		Capabilities: req.Capabilities,
		Tags:         req.Tags,
	})
	if err != nil {
		return nil, err
	}
	resp := &acppb.DiscoverAgentsResponse{}
	for _, a := range agents {
		resp.Agents = append(resp.Agents, &acppb.AgentInfo{
			Id:           a.ID,
			Type:         a.Type,
			Endpoint:     a.Endpoint,
			Capabilities: a.Capabilities,
			Metadata:     a.Metadata,
		})
	}
	return resp, nil
}

// RequestCapabilities grants requested capabilities and stores the grant for later revocation.
func (s *Server) RequestCapabilities(ctx context.Context, req *acppb.CapabilityRequest) (*acppb.CapabilityGrant, error) {
	if req == nil {
		return nil, statusError(codes.InvalidArgument, "request required")
	}
	if len(req.Capabilities) == 0 {
		return nil, statusError(codes.InvalidArgument, "at least one capability required")
	}

	// Extract agent ID from context if available
	agentID := ""
	if claims, ok := security.ClaimsFromContext(ctx); ok {
		agentID = claims.AgentID
	}

	// Calculate expiration
	duration := time.Duration(req.DurationSeconds) * time.Second
	if duration <= 0 {
		duration = 24 * time.Hour // Default to 24h if not specified
	}

	grant := &acppb.CapabilityGrant{
		GrantId:      ulid.Make().String(),
		AgentId:      agentID,
		Capabilities: req.Capabilities,
		IssuedAt:     timestamppb.Now(),
		ExpiresAt:    timestamppb.New(time.Now().Add(duration)),
		Context:      req.Context,
	}

	// Store the grant for later revocation
	s.grantMux.Lock()
	s.grants[grant.GrantId] = grant
	s.grantMux.Unlock()

	return grant, nil
}

// RevokeCapabilities revokes a previously issued capability grant.
func (s *Server) RevokeCapabilities(_ context.Context, req *acppb.CapabilityRevocation) (*emptypb.Empty, error) {
	if req == nil {
		return nil, statusError(codes.InvalidArgument, "request required")
	}
	if strings.TrimSpace(req.GrantId) == "" {
		return nil, statusError(codes.InvalidArgument, "grant_id required")
	}

	s.grantMux.Lock()
	defer s.grantMux.Unlock()

	if _, exists := s.grants[req.GrantId]; !exists {
		return nil, statusError(codes.NotFound, fmt.Sprintf("grant %s not found", req.GrantId))
	}

	delete(s.grants, req.GrantId)
	return &emptypb.Empty{}, nil
}

// CreateSession creates a lightweight session record for chat routing.
func (s *Server) CreateSession(_ context.Context, req *acppb.CreateSessionRequest) (*acppb.Session, error) {
	if req.AgentId == "" {
		return nil, statusError(codes.InvalidArgument, "agent_id required")
	}
	id := ulid.Make().String()
	sess := &acppb.Session{
		SessionId: id,
		AgentId:   req.AgentId,
		Metadata:  req.Metadata,
		CreatedAt: timestamppb.New(time.Now().UTC()),
	}
	if s.sessions == nil {
		s.sessions = make(map[string]*acppb.Session)
	}
	s.sessions[id] = sess
	return sess, nil
}

// UpdateSessionContext stores context delta (added/removed files and metadata) for a session.
func (s *Server) UpdateSessionContext(_ context.Context, req *acppb.ContextDelta) (*emptypb.Empty, error) {
	if req == nil {
		return nil, statusError(codes.InvalidArgument, "request required")
	}
	if strings.TrimSpace(req.SessionId) == "" {
		return nil, statusError(codes.InvalidArgument, "session_id required")
	}

	s.sessionContextMux.Lock()
	defer s.sessionContextMux.Unlock()

	// Get or create session context
	ctx, exists := s.sessionContexts[req.SessionId]
	if !exists {
		ctx = &SessionContext{
			SessionID: req.SessionId,
			Files:     make(map[string]bool),
			Metadata:  make(map[string]string),
		}
		s.sessionContexts[req.SessionId] = ctx
	}

	// Apply added files
	for _, file := range req.AddedFiles {
		if strings.TrimSpace(file) != "" {
			ctx.Files[file] = true
		}
	}

	// Apply removed files
	for _, file := range req.RemovedFiles {
		delete(ctx.Files, file)
	}

	// Apply metadata updates
	for k, v := range req.Metadata {
		if v == "" {
			delete(ctx.Metadata, k)
		} else {
			ctx.Metadata[k] = v
		}
	}

	ctx.UpdatedAt = time.Now()
	return &emptypb.Empty{}, nil
}

// SendMessage handles a simple request/response for editor integrations.
func (s *Server) SendMessage(_ context.Context, req *acppb.SendMessageRequest) (*acppb.SendMessageResponse, error) {
	if req.GetMessage() == nil || strings.TrimSpace(req.Message.Content) == "" {
		return nil, statusError(codes.InvalidArgument, "message required")
	}

	if s.models == nil {
		return nil, statusError(codes.FailedPrecondition, "model manager unavailable")
	}

	// Prefer orchestrator-driven response; fall back to plain LLM if storage/config missing.
	if s.store == nil || s.cfg == nil {
		execModel := s.models.GetExecutionModel()
		chatReq := model.ChatRequest{
			Model: execModel,
			Messages: []model.Message{
				{Role: "system", Content: "You are Buckley answering inline code questions for an editor."},
				{Role: req.Message.Role, Content: req.Message.Content},
			},
			Temperature: 0.2,
		}
		resp, err := s.models.ChatCompletion(context.Background(), chatReq)
		if err != nil {
			return nil, statusError(codes.Internal, err.Error())
		}
		content := ""
		if len(resp.Choices) > 0 {
			content = extractMessageText(resp.Choices[0].Message)
		}
		return &acppb.SendMessageResponse{
			Response: &acppb.Message{
				Role:    "assistant",
				Content: content,
			},
		}, nil
	}

	if s.cfg.ExecutionMode() == config.ExecutionModeRLM {
		sessionID := ulid.Make().String()
		runtime, cleanup, err := s.buildRLMRuntime(sessionID, req.AgentId)
		if err == nil && runtime != nil {
			answer, execErr := runtime.Execute(context.Background(), req.Message.Content)
			if cleanup != nil {
				cleanup()
			}
			if execErr == nil && answer != nil && strings.TrimSpace(answer.Content) != "" {
				return &acppb.SendMessageResponse{
					Response: &acppb.Message{
						Role:    "assistant",
						Content: answer.Content,
					},
				}, nil
			}
			if execErr != nil {
				return nil, statusError(codes.Internal, execErr.Error())
			}
		}
	}

	sessionID := req.GetAgentId()
	if strings.TrimSpace(sessionID) == "" {
		sessionID = ulid.Make().String()
	}

	orch, cleanup, err := s.buildOrchestratorContext(sessionID, req.AgentId)
	if err != nil {
		return nil, statusError(codes.Internal, err.Error())
	}
	defer cleanup()

	featureName := fmt.Sprintf("acp-send-%s", sessionID)
	desc := req.Message.Content
	if _, err := orch.PlanFeature(featureName, desc); err != nil {
		return nil, statusError(codes.Internal, err.Error())
	}
	if err := orch.ExecutePlan(); err != nil {
		return nil, statusError(codes.Internal, err.Error())
	}

	reply := &acppb.Message{
		Role:    "assistant",
		Content: fmt.Sprintf("Plan %s executed for request: %s", featureName, truncate(desc, 140)),
	}
	return &acppb.SendMessageResponse{Response: reply}, nil
}

// StreamTask returns a simple streamed response for Zed bridge.
func (s *Server) StreamTask(req *acppb.TaskStreamRequest, stream acppb.AgentCommunication_StreamTaskServer) error {
	if strings.TrimSpace(req.Query) == "" {
		return statusError(codes.InvalidArgument, "query required")
	}
	// If orchestrator wiring unavailable, fall back to direct LLM stream.
	if s.models == nil || s.store == nil || s.cfg == nil {
		msgs := []string{
			"Processing task...",
			fmt.Sprintf("Task: %s", req.Query),
			"Done.",
		}
		for _, msg := range msgs {
			event := &acppb.TaskEvent{
				TaskId:  req.TaskId,
				Message: msg,
			}
			if err := stream.Send(event); err != nil {
				return err
			}
		}
		return nil
	}

	if s.cfg.ExecutionMode() == config.ExecutionModeRLM {
		sessionID := req.TaskId
		if strings.TrimSpace(sessionID) == "" {
			sessionID = ulid.Make().String()
		}

		runtime, cleanup, err := s.buildRLMRuntime(sessionID, req.AgentId)
		if err == nil && runtime != nil {
			runtime.OnIteration(func(event rlm.IterationEvent) {
				message := fmt.Sprintf("Iteration %d/%d (ready=%t, tokens=%d)", event.Iteration, event.MaxIterations, event.Ready, event.TokensUsed)
				if event.Ready && strings.TrimSpace(event.Summary) != "" {
					message = event.Summary
				}
				_ = stream.Send(&acppb.TaskEvent{TaskId: req.TaskId, Message: message})
			})

			answer, execErr := runtime.Execute(stream.Context(), req.Query)
			if cleanup != nil {
				cleanup()
			}
			if execErr != nil {
				_ = stream.Send(&acppb.TaskEvent{TaskId: req.TaskId, Message: fmt.Sprintf("Execution failed: %v", execErr)})
				return statusError(codes.Internal, execErr.Error())
			}
			if answer != nil && strings.TrimSpace(answer.Content) != "" {
				_ = stream.Send(&acppb.TaskEvent{TaskId: req.TaskId, Message: answer.Content})
			}
			return nil
		}
	}

	if err := stream.Send(&acppb.TaskEvent{TaskId: req.TaskId, Message: "Planning task…"}); err != nil {
		return err
	}

	sessionID := req.TaskId
	if sessionID == "" {
		sessionID = ulid.Make().String()
	}

	orch, cleanup, err := s.buildOrchestratorContext(sessionID, req.AgentId)
	if err != nil {
		return statusError(codes.Internal, err.Error())
	}
	defer cleanup()

	// Support cancel by listening to stream.Context().Done()
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()

	featureName := fmt.Sprintf("acp-%s", req.TaskId)
	if strings.TrimSpace(featureName) == "" {
		featureName = "acp-task"
	}

	if _, err := orch.PlanFeature(featureName, req.Query); err != nil {
		_ = stream.Send(&acppb.TaskEvent{TaskId: req.TaskId, Message: fmt.Sprintf("Plan failed: %v", err)})
		return statusError(codes.Internal, err.Error())
	}

	if err := stream.Send(&acppb.TaskEvent{TaskId: req.TaskId, Message: "Executing plan…"}); err != nil {
		return err
	}

	execDone := make(chan error, 1)
	go func() {
		execDone <- orch.ExecutePlan()
	}()

	select {
	case <-ctx.Done():
		orch.Cancel() // propagate cancel to orchestrator
		_ = stream.Send(&acppb.TaskEvent{TaskId: req.TaskId, Message: "Task cancelled"})
		return context.Canceled
	case err := <-execDone:
		if err != nil {
			_ = stream.Send(&acppb.TaskEvent{TaskId: req.TaskId, Message: fmt.Sprintf("Execution failed: %v", err)})
			return statusError(codes.Internal, err.Error())
		}
	}

	if err := stream.Send(&acppb.TaskEvent{TaskId: req.TaskId, Message: "✅ Task completed"}); err != nil {
		_ = stream.Send(&acppb.TaskEvent{TaskId: req.TaskId, Message: fmt.Sprintf("Execution failed: %v", err)})
		return err
	}

	return nil
}

func extractChunkText(chunk model.StreamChunk) string {
	var sb strings.Builder
	for _, choice := range chunk.Choices {
		if choice.Delta.Content != "" {
			sb.WriteString(choice.Delta.Content)
		}
	}
	return sb.String()
}

func extractMessageText(msg model.Message) string {
	switch v := msg.Content.(type) {
	case string:
		return v
	default:
		return ""
	}
}

// buildRLMRuntime constructs an RLM runtime for ACP requests.
func (s *Server) buildRLMRuntime(sessionID, agentID string) (*rlm.Runtime, func(), error) {
	registry := tool.NewRegistry()
	registry.ConfigureContainers(s.cfg, s.projectRoot)

	missionStore := mission.NewStore(s.store.DB())
	requireApproval := strings.ToLower(s.cfg.Orchestrator.TrustLevel) != "autonomous"
	registry.EnableMissionControl(missionStore, agentID, requireApproval, 15*time.Minute)
	registry.UpdateMissionSession(sessionID)

	runtime, err := rlm.NewRuntime(resolveRLMConfig(s.cfg), rlm.RuntimeDeps{
		Models:       s.models,
		Store:        s.store,
		Registry:     registry,
		ToolApprover: s.toolApprover,
		Bus:          s.messageBus,
		Telemetry:    s.telemetryHub,
		SessionID:    sessionID,
	})
	if err != nil {
		return nil, nil, err
	}

	cleanup := func() {}
	return runtime, cleanup, nil
}

func resolveRLMConfig(cfg *config.Config) rlm.Config {
	base := rlm.DefaultConfig()
	if cfg == nil || cfg.RLM.IsZero() {
		return base
	}

	rlmCfg := cfg.RLM
	if strings.TrimSpace(rlmCfg.Coordinator.Model) != "" {
		base.Coordinator.Model = rlmCfg.Coordinator.Model
	}
	if rlmCfg.Coordinator.MaxIterations != 0 {
		base.Coordinator.MaxIterations = rlmCfg.Coordinator.MaxIterations
	}
	if rlmCfg.Coordinator.MaxTokensBudget != 0 {
		base.Coordinator.MaxTokensBudget = rlmCfg.Coordinator.MaxTokensBudget
	}
	if rlmCfg.Coordinator.MaxWallTime != 0 {
		base.Coordinator.MaxWallTime = rlmCfg.Coordinator.MaxWallTime
	}
	if rlmCfg.Coordinator.ConfidenceThreshold != 0 {
		base.Coordinator.ConfidenceThreshold = rlmCfg.Coordinator.ConfidenceThreshold
	}
	base.Coordinator.StreamPartials = rlmCfg.Coordinator.StreamPartials

	if rlmCfg.Scratchpad.MaxEntriesMemory != 0 {
		base.Scratchpad.MaxEntriesMemory = rlmCfg.Scratchpad.MaxEntriesMemory
	}
	if rlmCfg.Scratchpad.MaxRawBytesMemory != 0 {
		base.Scratchpad.MaxRawBytesMemory = rlmCfg.Scratchpad.MaxRawBytesMemory
	}
	if strings.TrimSpace(rlmCfg.Scratchpad.EvictionPolicy) != "" {
		base.Scratchpad.EvictionPolicy = rlmCfg.Scratchpad.EvictionPolicy
	}
	if rlmCfg.Scratchpad.DefaultTTL != 0 {
		base.Scratchpad.DefaultTTL = rlmCfg.Scratchpad.DefaultTTL
	}
	base.Scratchpad.PersistArtifacts = rlmCfg.Scratchpad.PersistArtifacts
	base.Scratchpad.PersistDecisions = rlmCfg.Scratchpad.PersistDecisions

	if strings.TrimSpace(rlmCfg.SubAgent.Model) != "" {
		base.SubAgent.Model = rlmCfg.SubAgent.Model
	}
	if rlmCfg.SubAgent.MaxConcurrent != 0 {
		base.SubAgent.MaxConcurrent = rlmCfg.SubAgent.MaxConcurrent
	}
	if rlmCfg.SubAgent.Timeout != 0 {
		base.SubAgent.Timeout = rlmCfg.SubAgent.Timeout
	}

	base.Normalize()
	return base
}

// buildOrchestratorContext constructs a fresh orchestrator stack for ACP requests.
func (s *Server) buildOrchestratorContext(sessionID, agentID string) (*orchestrator.Orchestrator, func(), error) {
	registry := tool.NewRegistry()
	registry.ConfigureContainers(s.cfg, s.projectRoot)

	missionStore := mission.NewStore(s.store.DB())
	requireApproval := strings.ToLower(s.cfg.Orchestrator.TrustLevel) != "autonomous"
	registry.EnableMissionControl(missionStore, agentID, requireApproval, 15*time.Minute)
	registry.UpdateMissionSession(sessionID)

	planStore := orchestrator.NewFilePlanStore(s.cfg.Artifacts.PlanningDir)

	// Reuse or build a live workflow per session for stateful editor status.
	s.liveMux.RLock()
	workflow, ok := s.liveWorkflows[sessionID]
	s.liveMux.RUnlock()
	if !ok {
		workflow = orchestrator.NewWorkflowManager(s.cfg, s.models, registry, s.store, s.docsRoot, s.projectRoot, s.telemetryHub)
		workflow.SetSessionID(sessionID)
		s.liveMux.Lock()
		s.liveWorkflows[sessionID] = workflow
		s.liveMux.Unlock()
	}

	orch := orchestrator.NewOrchestrator(s.store, s.models, registry, s.cfg, workflow, planStore)

	cleanup := func() {}
	return orch, cleanup, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func rankInt32(value int) int32 {
	if value < 0 {
		return 0
	}
	if value > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(value)
}

// SubscribeTaskEvents streams task events from the message bus.
func (s *Server) SubscribeTaskEvents(req *acppb.TaskSubscription, stream acppb.AgentCommunication_SubscribeTaskEventsServer) error {
	if req == nil {
		return statusError(codes.InvalidArgument, "subscription request required")
	}
	if s.messageBus == nil {
		return statusError(codes.FailedPrecondition, "message bus not configured")
	}

	ctx := stream.Context()

	// Build subscription subject based on plan_id or task_ids
	var subject string
	if len(req.TaskIds) > 0 {
		// Subscribe to specific tasks - use first one for now
		// In production, would subscribe to multiple subjects
		subject = fmt.Sprintf("buckley.task.%s.events", req.TaskIds[0])
	} else if req.PlanId != "" {
		subject = fmt.Sprintf("buckley.plan.%s.events", req.PlanId)
	} else {
		subject = "buckley.task.>.events" // All task events
	}

	sub, err := s.messageBus.Subscribe(ctx, subject, func(msg *bus.Message) []byte {
		// Parse the event and send to stream
		var event map[string]any
		if err := json.Unmarshal(msg.Data, &event); err != nil {
			return nil
		}

		taskEvent := &acppb.TaskEvent{
			TaskId:    getString(event, "task_id"),
			Message:   getString(event, "type"),
			Timestamp: timestamppb.Now(),
		}

		// Best-effort send; if stream is closed, subscription will be cleaned up
		_ = stream.Send(taskEvent)
		return nil
	})
	if err != nil {
		return statusError(codes.Internal, fmt.Sprintf("subscribe failed: %v", err))
	}
	defer sub.Unsubscribe()

	// Block until context is cancelled
	<-ctx.Done()
	return nil
}

// GetP2PEndpoint returns the message bus endpoint for direct agent communication.
func (s *Server) GetP2PEndpoint(ctx context.Context, req *acppb.P2PEndpointRequest) (*acppb.P2PEndpoint, error) {
	if req == nil {
		return nil, statusError(codes.InvalidArgument, "request required")
	}
	if strings.TrimSpace(req.TargetAgentId) == "" {
		return nil, statusError(codes.InvalidArgument, "target_agent_id required")
	}

	// Generate a token for this P2P connection
	token := ulid.Make().String()

	// Build the NATS subject address for the target agent
	address := fmt.Sprintf("nats://buckley.agent.%s.inbox", req.TargetAgentId)

	return &acppb.P2PEndpoint{
		Address:   address,
		Token:     token,
		ExpiresAt: timestamppb.New(time.Now().Add(24 * time.Hour)),
	}, nil
}

// EstablishP2PConnection sets up a P2P communication channel between agents.
func (s *Server) EstablishP2PConnection(ctx context.Context, req *acppb.P2PHandshake) (*acppb.P2PConnectionInfo, error) {
	if req == nil {
		return nil, statusError(codes.InvalidArgument, "handshake required")
	}
	if strings.TrimSpace(req.Token) == "" {
		return nil, statusError(codes.InvalidArgument, "token required")
	}
	if s.messageBus == nil {
		return nil, statusError(codes.FailedPrecondition, "message bus not configured")
	}

	// Validate token and establish connection
	connectionID := ulid.Make().String()

	// Default capabilities for P2P connections via MessageBus
	capabilities := []string{
		"message.send",
		"message.receive",
		"task.subscribe",
		"status.broadcast",
	}

	return &acppb.P2PConnectionInfo{
		ConnectionId: connectionID,
		Capabilities: capabilities,
	}, nil
}

// getString safely extracts a string from a map.
func getString(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// RequestToolExecution acknowledges tool execution requests.
func (s *Server) RequestToolExecution(req *acppb.ToolExecutionRequest, stream acppb.AgentCommunication_RequestToolExecutionServer) error {
	if req.Tool == "" {
		return statusError(codes.InvalidArgument, "tool required")
	}
	if s.cfg == nil || s.store == nil {
		return statusError(codes.FailedPrecondition, "server missing config/storage")
	}

	claims, haveClaims := security.ClaimsFromContext(stream.Context())
	if !haveClaims {
		return statusError(codes.Unauthenticated, "missing client identity")
	}
	if claims.AgentID != "" && req.AgentId != "" && claims.AgentID != req.AgentId {
		return statusError(codes.PermissionDenied, "agent mismatch for tool execution")
	}

	agentID := claims.AgentID
	if agentID == "" {
		agentID = req.AgentId
	}

	var capabilities []string
	if agent, err := s.coordinator.GetAgent(stream.Context(), agentID); err == nil && agent != nil {
		capabilities = agent.Capabilities
	} else {
		return statusError(codes.PermissionDenied, "agent not registered")
	}

	sessionID := req.AgentId
	if strings.TrimSpace(sessionID) == "" {
		sessionID = ulid.Make().String()
	}

	registry := tool.NewRegistry()
	registry.ConfigureContainers(s.cfg, s.projectRoot)
	missionStore := mission.NewStore(s.store.DB())
	requireApproval := strings.ToLower(s.cfg.Orchestrator.TrustLevel) != "autonomous"
	registry.EnableMissionControl(missionStore, req.AgentId, requireApproval, 15*time.Minute)
	registry.UpdateMissionSession(sessionID)

	if err := stream.Send(&acppb.ToolExecutionEvent{ExecutionId: req.Tool, Status: "started", Timestamp: timestamppb.Now()}); err != nil {
		return err
	}

	claimsCtx := security.ContextWithClaims(stream.Context(), &security.Claims{
		AgentID:      agentID,
		Capabilities: capabilities,
	})
	if s.toolApprover != nil {
		if err := s.toolApprover.CheckToolAccess(claimsCtx, req.Tool); err != nil {
			_ = stream.Send(&acppb.ToolExecutionEvent{
				ExecutionId: req.Tool,
				Status:      "denied",
				Output:      err.Error(),
				Timestamp:   timestamppb.Now(),
			})
			return statusError(codes.PermissionDenied, err.Error())
		}
	}

	params := make(map[string]any, len(req.Parameters))
	for k, v := range req.Parameters {
		params[k] = v
	}
	res, err := registry.Execute(req.Tool, params)
	if err != nil {
		_ = stream.Send(&acppb.ToolExecutionEvent{ExecutionId: req.Tool, Status: "failed", Output: err.Error(), Timestamp: timestamppb.Now()})
		return statusError(codes.Internal, err.Error())
	}

	out := ""
	if res != nil {
		if msg, ok := res.DisplayData["summary"].(string); ok {
			out = msg
		} else if msg, ok := res.Data["message"].(string); ok {
			out = msg
		}
	}

	return stream.Send(&acppb.ToolExecutionEvent{ExecutionId: req.Tool, Status: "completed", Output: out, Timestamp: timestamppb.Now()})
}

// ApproveToolExecution approves a pending tool execution request.
func (s *Server) ApproveToolExecution(_ context.Context, req *acppb.ToolApproval) (*emptypb.Empty, error) {
	if req == nil {
		return nil, statusError(codes.InvalidArgument, "request required")
	}
	if strings.TrimSpace(req.ExecutionId) == "" {
		return nil, statusError(codes.InvalidArgument, "execution_id required")
	}

	s.pendingApprovalMux.Lock()
	pending, exists := s.pendingApprovals[req.ExecutionId]
	if exists {
		delete(s.pendingApprovals, req.ExecutionId)
	}
	s.pendingApprovalMux.Unlock()

	if !exists {
		return nil, statusError(codes.NotFound, fmt.Sprintf("no pending approval for execution %s", req.ExecutionId))
	}

	// Send approval result to waiting goroutine
	if pending.ResultChan != nil {
		select {
		case pending.ResultChan <- ApprovalResult{Approved: true, Remember: req.Remember}:
		default:
			// Channel might be closed or full, continue anyway
		}
		close(pending.ResultChan)
	}

	return &emptypb.Empty{}, nil
}

// RejectToolExecution rejects a pending tool execution request.
func (s *Server) RejectToolExecution(_ context.Context, req *acppb.ToolRejection) (*emptypb.Empty, error) {
	if req == nil {
		return nil, statusError(codes.InvalidArgument, "request required")
	}
	if strings.TrimSpace(req.ExecutionId) == "" {
		return nil, statusError(codes.InvalidArgument, "execution_id required")
	}

	s.pendingApprovalMux.Lock()
	pending, exists := s.pendingApprovals[req.ExecutionId]
	if exists {
		delete(s.pendingApprovals, req.ExecutionId)
	}
	s.pendingApprovalMux.Unlock()

	if !exists {
		return nil, statusError(codes.NotFound, fmt.Sprintf("no pending approval for execution %s", req.ExecutionId))
	}

	// Send rejection result to waiting goroutine
	if pending.ResultChan != nil {
		select {
		case pending.ResultChan <- ApprovalResult{Approved: false, Reason: req.Reason}:
		default:
			// Channel might be closed or full, continue anyway
		}
		close(pending.ResultChan)
	}

	return &emptypb.Empty{}, nil
}

// CreatePendingApproval creates a pending approval entry for human-in-the-loop tool execution.
// The executionID should be cryptographically random to prevent guessing attacks.
func (s *Server) CreatePendingApproval(executionID, agentID, tool string, params map[string]string) chan ApprovalResult {
	resultChan := make(chan ApprovalResult, 1)

	s.pendingApprovalMux.Lock()
	s.pendingApprovals[executionID] = &PendingApproval{
		ExecutionID: executionID,
		AgentID:     agentID,
		Tool:        tool,
		Parameters:  params,
		CreatedAt:   time.Now(),
		ResultChan:  resultChan,
	}
	s.pendingApprovalMux.Unlock()

	return resultChan
}

// GetPendingApprovals returns all pending approval requests (useful for UI).
func (s *Server) GetPendingApprovals() []*PendingApproval {
	s.pendingApprovalMux.RLock()
	defer s.pendingApprovalMux.RUnlock()

	result := make([]*PendingApproval, 0, len(s.pendingApprovals))
	for _, p := range s.pendingApprovals {
		result = append(result, &PendingApproval{
			ExecutionID: p.ExecutionID,
			AgentID:     p.AgentID,
			Tool:        p.Tool,
			Parameters:  p.Parameters,
			CreatedAt:   p.CreatedAt,
			// Don't expose ResultChan
		})
	}
	return result
}

// StreamInlineCompletions streams inline suggestions using the execution model.
func (s *Server) StreamInlineCompletions(req *acppb.InlineCompletionRequest, stream acppb.AgentCommunication_StreamInlineCompletionsServer) error {
	if req == nil {
		return statusError(codes.InvalidArgument, "request cannot be nil")
	}
	if s.models == nil {
		return statusError(codes.FailedPrecondition, "model manager unavailable")
	}
	if req.Context == nil || req.Context.Document == nil {
		return statusError(codes.InvalidArgument, "document context required")
	}
	doc := req.Context.Document
	if strings.TrimSpace(doc.Content) == "" {
		return statusError(codes.InvalidArgument, "document content required")
	}

	hasSelection := doc.Selection != nil && (doc.Selection.Start != nil || doc.Selection.End != nil)
	var selectionText string
	if hasSelection {
		text, err := sliceContent(doc.Content, doc.Selection)
		if err != nil {
			return statusError(codes.InvalidArgument, fmt.Sprintf("invalid selection: %v", err))
		}
		selectionText = text
	}

	completionID := req.SessionId
	if strings.TrimSpace(completionID) == "" {
		completionID = ulid.Make().String()
	}

	prompt := buildCompletionPrompt(req.Prompt, doc, selectionText, hasSelection, req.Context.RelatedDocuments)
	execModel := s.models.GetExecutionModel()
	chatReq := model.ChatRequest{
		Model: execModel,
		Messages: []model.Message{
			{Role: "system", Content: "You are Buckley providing inline completions. Respond with code/text to insert at the cursor. No markdown, no fences."},
			{Role: "user", Content: prompt},
		},
		Temperature: 0.4,
		Stream:      true,
	}

	s.recordActivity(req.AgentId, req.SessionId, "inline_completion", truncate(req.Prompt, 120), "working")
	if strings.TrimSpace(req.SessionId) != "" {
		s.publishTelemetry(telemetry.EventModelStreamStarted, req.SessionId, "", map[string]any{
			"source": "inline_completion",
		})
		defer s.publishTelemetry(telemetry.EventModelStreamEnded, req.SessionId, "", map[string]any{
			"source": "inline_completion",
		})
	}
	chunks, errs := s.models.ChatCompletionStream(stream.Context(), chatReq)
	var finishReason string
	var lastUsage *model.Usage
	for chunks != nil || errs != nil {
		select {
		case <-stream.Context().Done():
			s.recordActivity(req.AgentId, req.SessionId, "inline_completion", "cancelled", "stopped")
			return context.Canceled
		case chunk, ok := <-chunks:
			if !ok {
				chunks = nil
				continue
			}
			for _, choice := range chunk.Choices {
				if choice.FinishReason != nil && *choice.FinishReason != "" {
					finishReason = *choice.FinishReason
				}
			}
			if chunk.Usage != nil {
				lastUsage = chunk.Usage
			}
			text := extractChunkText(chunk)
			if strings.TrimSpace(text) == "" {
				continue
			}
			if err := stream.Send(&acppb.InlineCompletionEvent{
				CompletionId: completionID,
				Text:         text,
				IsFinal:      false,
				Score:        1.0,
			}); err != nil {
				return err
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				s.recordActivity(req.AgentId, req.SessionId, "inline_completion", err.Error(), "error")
				return statusError(codes.Internal, fmt.Sprintf("stream error: %v", err))
			}
		}
	}

	s.recordActivity(req.AgentId, req.SessionId, "inline_completion", "completed", "working")
	s.publishTelemetry(telemetry.EventEditorInline, req.SessionId, "", map[string]any{
		"model":         execModel,
		"finish_reason": finishReason,
	})
	if lastUsage != nil {
		s.publishUsage(execModel, lastUsage, req.SessionId, "")
	}
	return stream.Send(&acppb.InlineCompletionEvent{CompletionId: completionID, IsFinal: true, FinishReason: finishReason, Score: 1.0})
}

// ProposeEdits generates editor-ready edits using the execution model.
// Strategy: focus on the active document/selection; return a single ProposedEdit
// that either replaces the current selection or the whole file.
func (s *Server) ProposeEdits(ctx context.Context, req *acppb.ProposeEditsRequest) (*acppb.ProposeEditsResponse, error) {
	if req == nil {
		return nil, statusError(codes.InvalidArgument, "request cannot be nil")
	}
	if strings.TrimSpace(req.AgentId) == "" {
		return nil, statusError(codes.InvalidArgument, "agent_id required")
	}
	if strings.TrimSpace(req.Instruction) == "" {
		return nil, statusError(codes.InvalidArgument, "instruction required")
	}
	if req.Context == nil || req.Context.Document == nil {
		return nil, statusError(codes.InvalidArgument, "document context required")
	}
	if s.models == nil {
		return nil, statusError(codes.FailedPrecondition, "model manager unavailable")
	}

	doc := req.Context.Document
	if strings.TrimSpace(doc.Content) == "" {
		return nil, statusError(codes.InvalidArgument, "document content required")
	}

	hasSelection := doc.Selection != nil && (doc.Selection.Start != nil || doc.Selection.End != nil)
	var selectionText string
	if hasSelection {
		text, err := sliceContent(doc.Content, doc.Selection)
		if err != nil {
			return nil, statusError(codes.InvalidArgument, fmt.Sprintf("invalid selection: %v", err))
		}
		selectionText = text
	}

	prompt := buildEditPrompt(req.Instruction, doc, selectionText, hasSelection, req.Context.RelatedDocuments)

	execModel := s.models.GetExecutionModel()
	suggestionCount := int(req.MaxSuggestions)
	if suggestionCount <= 0 {
		suggestionCount = 1
	}

	var proposed []*acppb.ProposedEdit
	for i := 0; i < suggestionCount; i++ {
		chatReq := model.ChatRequest{
			Model: execModel,
			Messages: []model.Message{
				{Role: "system", Content: "You are Buckley generating precise code edits. Respond with code only—no markdown, fences, or explanations."},
				{Role: "user", Content: prompt},
			},
			Temperature: 0.15 + 0.05*float64(i), // slight variance for alt suggestions
			Stream:      false,
		}

		modelResp, err := s.models.ChatCompletion(ctx, chatReq)
		if err != nil {
			return nil, statusError(codes.Internal, fmt.Sprintf("model error: %v", err))
		}
		if len(modelResp.Choices) == 0 {
			return nil, statusError(codes.Internal, "model returned no choices")
		}
		if modelResp.Usage.TotalTokens > 0 {
			s.publishUsage(execModel, &modelResp.Usage, req.SessionId, "")
		}

		newText := extractMessageText(modelResp.Choices[0].Message)
		if strings.TrimSpace(newText) == "" {
			return nil, statusError(codes.Internal, "model returned empty edit")
		}

		editRange := doc.Selection
		if !hasSelection {
			editRange = nil // replace whole document
		}

		score := 1.0 - 0.1*float64(i)
		if score < 0.1 {
			score = 0.1
		}
		proposed = append(proposed, &acppb.ProposedEdit{
			Uri: doc.Uri,
			Edits: []*acppb.TextEdit{
				{
					Uri:     doc.Uri,
					Range:   editRange,
					NewText: newText,
				},
			},
			Summary: truncate(req.Instruction, 120),
			Title:   truncate(req.Instruction, 60),
			Score:   score,
			Rank:    rankInt32(i + 1),
		})
	}

	editResp := &acppb.ProposeEditsResponse{
		PlanId:  fmt.Sprintf("ad-hoc-%s", req.SessionId),
		TaskId:  ulid.Make().String(),
		Edits:   proposed,
		Summary: fmt.Sprintf("Proposed %d edit(s) for %s", len(proposed), doc.Uri),
	}

	if req.Apply && len(proposed) > 0 {
		_, applyErr := s.ApplyEdits(ctx, &acppb.ApplyEditsRequest{
			AgentId:   req.AgentId,
			SessionId: req.SessionId,
			Edits:     proposed[0].Edits,
			Title:     proposed[0].Title,
			DryRun:    false,
		})
		if applyErr != nil {
			return nil, statusError(codes.Internal, fmt.Sprintf("apply edits: %v", applyErr))
		}
		editResp.Summary = fmt.Sprintf("Applied edit for %s", doc.Uri)
	}

	s.publishTelemetry(telemetry.EventEditorPropose, req.SessionId, "", map[string]any{
		"model":             execModel,
		"suggestions":       len(proposed),
		"auto_applied":      req.Apply && len(proposed) > 0,
		"instruction_first": truncate(req.Instruction, 120),
	})

	return editResp, nil
}

// ApplyEdits applies editor-supplied text edits to files under the project root.
func (s *Server) ApplyEdits(_ context.Context, req *acppb.ApplyEditsRequest) (*acppb.ApplyEditsResponse, error) {
	if req == nil {
		return nil, statusError(codes.InvalidArgument, "request cannot be nil")
	}
	if strings.TrimSpace(req.AgentId) == "" {
		return nil, statusError(codes.InvalidArgument, "agent_id required")
	}
	if len(req.Edits) == 0 {
		return nil, statusError(codes.InvalidArgument, "edits required")
	}

	// Group edits by resolved file path.
	fileEdits := make(map[string][]*acppb.TextEdit)
	for _, edit := range req.Edits {
		if edit == nil {
			return nil, statusError(codes.InvalidArgument, "nil edit provided")
		}
		path, err := s.resolvePath(edit.Uri)
		if err != nil {
			return nil, statusError(codes.InvalidArgument, err.Error())
		}
		fileEdits[path] = append(fileEdits[path], edit)
	}

	appliedPaths := make([]string, 0, len(fileEdits))
	for path, edits := range fileEdits {
		content := ""
		if data, err := os.ReadFile(path); err == nil {
			content = string(data)
		} else if !os.IsNotExist(err) {
			return nil, statusError(codes.Internal, fmt.Sprintf("read %s: %v", path, err))
		}

		updated, err := applyTextEdits(content, edits)
		if err != nil {
			return nil, statusError(codes.InvalidArgument, fmt.Sprintf("apply edits for %s: %v", path, err))
		}

		appliedPaths = append(appliedPaths, path)
		if req.DryRun {
			continue
		}

		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, statusError(codes.Internal, fmt.Sprintf("prepare dir for %s: %v", path, err))
		}
		if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
			return nil, statusError(codes.Internal, fmt.Sprintf("write %s: %v", path, err))
		}
	}

	msg := "edits applied"
	if req.DryRun {
		msg = "dry-run only (no files written)"
	}

	s.recordActivity(req.AgentId, req.SessionId, "apply_edits", msg, "working")
	s.publishTelemetry(telemetry.EventEditorApply, req.SessionId, "", map[string]any{
		"files":    len(appliedPaths),
		"dry_run":  req.DryRun,
		"agent_id": req.AgentId,
	})
	return &acppb.ApplyEditsResponse{
		Applied: !req.DryRun,
		Files:   appliedPaths,
		Message: msg,
	}, nil
}

// UpdateEditorState returns lightweight status for editor gutters/status bars.
func (s *Server) UpdateEditorState(_ context.Context, req *acppb.UpdateEditorStateRequest) (*acppb.UpdateEditorStateResponse, error) {
	if req == nil {
		return nil, statusError(codes.InvalidArgument, "request cannot be nil")
	}
	// Returns current orchestrator state. Future: wire to mission approvals.
	resp := &acppb.UpdateEditorStateResponse{
		PlanState:       "unknown",
		TodoState:       "",
		Approvals:       nil,
		ToolsInProgress: nil,
	}

	if s.store == nil {
		return resp, nil
	}
	if strings.TrimSpace(req.SessionId) == "" {
		return resp, nil
	}
	if planID, err := s.store.GetSessionPlanID(req.SessionId); err == nil && strings.TrimSpace(planID) != "" {
		resp.PlanState = fmt.Sprintf("plan:%s", planID)
		planStore := orchestrator.NewFilePlanStore(s.cfg.Artifacts.PlanningDir)
		if plan, err := planStore.LoadPlan(planID); err == nil && plan != nil {
			completed := 0
			inProgressTitles := []string{}
			for _, task := range plan.Tasks {
				if task.Status == orchestrator.TaskCompleted {
					completed++
				}
				if task.Status == orchestrator.TaskInProgress {
					inProgressTitles = append(inProgressTitles, task.Title)
				}
			}
			resp.PlanState = fmt.Sprintf("plan:%s:%d/%d", planID, completed, len(plan.Tasks))
			for i, title := range inProgressTitles {
				if i >= 3 {
					break
				}
				resp.ToolsInProgress = append(resp.ToolsInProgress, fmt.Sprintf("task:%s", truncate(title, 80)))
			}
		}
	} else {
		resp.PlanState = "none"
	}

	todos, err := s.store.GetTodos(req.SessionId)
	if err != nil {
		return nil, statusError(codes.Internal, fmt.Sprintf("get todos: %v", err))
	}
	if len(todos) == 0 {
		resp.TodoState = "none"
	} else {
		active, err := s.store.GetActiveTodo(req.SessionId)
		if err != nil {
			return nil, statusError(codes.Internal, fmt.Sprintf("get active todo: %v", err))
		}
		completed := 0
		for _, t := range todos {
			if t.Status == "completed" {
				completed++
			}
		}

		switch {
		case active != nil:
			resp.TodoState = fmt.Sprintf("in_progress:%s", truncate(active.Content, 80))
		case completed == len(todos):
			resp.TodoState = "completed"
		default:
			resp.TodoState = fmt.Sprintf("pending:%d", len(todos)-completed)
		}
	}

	// Pending approvals from mission control
	mStore := mission.NewStore(s.store.DB())
	pendingChanges, err := mStore.ListPendingChanges("pending", 50)
	if err == nil {
		for _, change := range pendingChanges {
			if change != nil && change.SessionID == req.SessionId {
				resp.Approvals = append(resp.Approvals, change.FilePath)
			}
		}
	}

	// Active tools/agents for this session (mission activity)
	activities, err := mStore.ListSessionActivity(req.SessionId, 20)
	if err == nil {
		for _, act := range activities {
			if act == nil {
				continue
			}
			// Surface working/waiting states to editors.
			if act.Status == "working" || act.Status == "waiting" || act.Status == "active" {
				label := act.Action
				if strings.TrimSpace(label) == "" {
					label = act.AgentType
				}
				if strings.TrimSpace(label) == "" {
					label = "agent"
				}
				resp.ToolsInProgress = append(resp.ToolsInProgress, fmt.Sprintf("%s:%s", act.AgentID, label))
			}
		}
	}

	// Live workflow state (executor tasks/tools) if available
	s.liveMux.RLock()
	workflow := s.liveWorkflows[req.SessionId]
	s.liveMux.RUnlock()
	if workflow != nil {
		if plan := workflow.GetCurrentPlan(); plan != nil {
			completed := 0
			var activeTitles []string
			for _, task := range plan.Tasks {
				if task.Status == orchestrator.TaskCompleted {
					completed++
				}
				if task.Status == orchestrator.TaskInProgress {
					activeTitles = append(activeTitles, task.Title)
				}
			}
			resp.PlanState = fmt.Sprintf("plan:%s:%d/%d", plan.ID, completed, len(plan.Tasks))
			for i, title := range activeTitles {
				if i >= 3 {
					break
				}
				resp.ToolsInProgress = append(resp.ToolsInProgress, fmt.Sprintf("task:%s", truncate(title, 80)))
			}
		}
		for _, summary := range workflow.GetActivitySummaries() {
			if summary != "" {
				resp.ToolsInProgress = append(resp.ToolsInProgress, fmt.Sprintf("tool:%s", truncate(summary, 80)))
			}
		}
	}

	return resp, nil
}

func (s *Server) resolvePath(uri string) (string, error) {
	if strings.TrimSpace(uri) == "" {
		return "", fmt.Errorf("uri required")
	}

	parsed, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("invalid uri %q: %w", uri, err)
	}

	path := uri
	if parsed.Scheme != "" {
		if parsed.Scheme != "file" {
			return "", fmt.Errorf("unsupported uri scheme %q", parsed.Scheme)
		}
		path = parsed.Path
		if path == "" {
			path = parsed.Opaque
		}
	}

	if !filepath.IsAbs(path) {
		path = filepath.Join(s.projectRoot, path)
	}
	cleaned := filepath.Clean(path)
	root := filepath.Clean(s.projectRoot)
	if root != cleaned && !strings.HasPrefix(cleaned, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes project root", uri)
	}
	return cleaned, nil
}

func applyTextEdits(content string, edits []*acppb.TextEdit) (string, error) {
	base := []rune(content)
	type resolvedEdit struct {
		start   int
		end     int
		newText string
	}

	resolved := make([]resolvedEdit, 0, len(edits))
	for _, edit := range edits {
		if edit == nil {
			return "", fmt.Errorf("nil edit")
		}
		if edit.Range == nil {
			resolved = append(resolved, resolvedEdit{
				start:   0,
				end:     len(base),
				newText: edit.NewText,
			})
			continue
		}

		start, err := offsetFromPosition(base, edit.Range.Start)
		if err != nil {
			return "", err
		}
		end, err := offsetFromPosition(base, edit.Range.End)
		if err != nil {
			return "", err
		}
		if end < start {
			return "", fmt.Errorf("end before start (start %d end %d)", start, end)
		}
		resolved = append(resolved, resolvedEdit{
			start:   start,
			end:     end,
			newText: edit.NewText,
		})
	}

	// Apply from highest offset to lowest to keep offsets stable.
	sort.Slice(resolved, func(i, j int) bool {
		if resolved[i].start == resolved[j].start {
			return resolved[i].end > resolved[j].end
		}
		return resolved[i].start > resolved[j].start
	})

	out := base
	for _, e := range resolved {
		if e.start > len(out) || e.end > len(out) {
			return "", fmt.Errorf("range [%d,%d) out of bounds %d", e.start, e.end, len(out))
		}
		head := append([]rune{}, out[:e.start]...)
		head = append(head, []rune(e.newText)...)
		out = append(head, out[e.end:]...)
	}
	return string(out), nil
}

func offsetFromPosition(content []rune, pos *acppb.Position) (int, error) {
	if pos == nil {
		return 0, fmt.Errorf("position required")
	}
	if pos.Line < 0 || pos.Character < 0 {
		return 0, fmt.Errorf("negative position (line %d char %d)", pos.Line, pos.Character)
	}

	targetLine := int(pos.Line)
	targetChar := int(pos.Character)

	line := 0
	char := 0
	for idx, r := range content {
		if line == targetLine && char == targetChar {
			return idx, nil
		}
		if r == '\n' {
			line++
			char = 0
		} else {
			char++
		}
	}

	// Permit position at EOF (after last rune)
	if line == targetLine && char == targetChar {
		return len(content), nil
	}

	return 0, fmt.Errorf("position out of range (line %d char %d)", targetLine, targetChar)
}

func sliceContent(content string, r *acppb.Range) (string, error) {
	if r == nil {
		return content, nil
	}
	base := []rune(content)
	start, err := offsetFromPosition(base, r.Start)
	if err != nil {
		return "", err
	}
	end, err := offsetFromPosition(base, r.End)
	if err != nil {
		return "", err
	}
	if end < start || end > len(base) {
		return "", fmt.Errorf("invalid range [%d,%d) for content length %d", start, end, len(base))
	}
	return string(base[start:end]), nil
}

func buildEditPrompt(instruction string, doc *acppb.DocumentSnapshot, selectionText string, hasSelection bool, related []*acppb.DocumentSnapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Instruction:\n%s\n\n", instruction)
	fmt.Fprintf(&b, "File: %s\nLanguage: %s\n", doc.Uri, doc.LanguageId)
	if hasSelection {
		b.WriteString("You MUST replace only the selected code. Return the replacement text only.\n")
		fmt.Fprintf(&b, "Selected code:\n```\n%s\n```\n\n", selectionText)
	} else {
		b.WriteString("Return the full file content after applying the instruction.\n")
	}
	fmt.Fprintf(&b, "Full file:\n```\n%s\n```\n", doc.Content)

	// Add at most 2 related documents to keep prompt bounded.
	added := 0
	for _, rel := range related {
		if rel == nil || strings.TrimSpace(rel.Content) == "" {
			continue
		}
		b.WriteString("\nRelated file:\n")
		fmt.Fprintf(&b, "Path: %s\nLanguage: %s\n", rel.Uri, rel.LanguageId)
		fmt.Fprintf(&b, "Content (truncated):\n```\n%s\n```\n", clampText(rel.Content, 4000))
		added++
		if added >= 2 {
			break
		}
	}
	return b.String()
}

func buildCompletionPrompt(prompt string, doc *acppb.DocumentSnapshot, selectionText string, hasSelection bool, related []*acppb.DocumentSnapshot) string {
	var b strings.Builder
	if strings.TrimSpace(prompt) != "" {
		fmt.Fprintf(&b, "User prompt: %s\n\n", prompt)
	}
	fmt.Fprintf(&b, "File: %s\nLanguage: %s\n", doc.Uri, doc.LanguageId)
	if hasSelection {
		b.WriteString("Cursor selection range provided. Return text to replace the selection.\n")
		fmt.Fprintf(&b, "Selection:\n```\n%s\n```\n", selectionText)
	} else {
		b.WriteString("Cursor is inside the file. Return completion text to insert at the cursor (no fences).\n")
	}
	fmt.Fprintf(&b, "Full file:\n```\n%s\n```\n", doc.Content)

	added := 0
	for _, rel := range related {
		if rel == nil || strings.TrimSpace(rel.Content) == "" {
			continue
		}
		fmt.Fprintf(&b, "\nRelated file %s:\n```\n%s\n```\n", rel.Uri, clampText(rel.Content, 3000))
		added++
		if added >= 2 {
			break
		}
	}
	return b.String()
}

func clampText(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max]
}

func (s *Server) recordActivity(agentID, sessionID, action, details, status string) {
	if s.store == nil || strings.TrimSpace(agentID) == "" {
		return
	}
	mStore := mission.NewStore(s.store.DB())
	_ = mStore.RecordAgentActivity(&mission.AgentActivity{
		AgentID:   agentID,
		SessionID: sessionID,
		AgentType: "editor",
		Action:    action,
		Details:   details,
		Status:    status,
		Timestamp: time.Now(),
	})
}

// publishUsage emits telemetry for token/cost usage when available.
func (s *Server) publishUsage(modelID string, usage *model.Usage, sessionID, planID string) {
	if s.telemetryHub == nil || usage == nil {
		return
	}

	data := map[string]any{
		"model":             modelID,
		"prompt_tokens":     usage.PromptTokens,
		"completion_tokens": usage.CompletionTokens,
		"total_tokens":      usage.TotalTokens,
	}

	if price, err := s.models.GetPricing(modelID); err == nil && price != nil {
		promptCost := price.Prompt * float64(usage.PromptTokens) / 1_000_000.0
		completionCost := price.Completion * float64(usage.CompletionTokens) / 1_000_000.0
		data["cost"] = promptCost + completionCost
	}

	s.telemetryHub.Publish(telemetry.Event{
		Type:      telemetry.EventTokenUsageUpdated,
		SessionID: sessionID,
		PlanID:    planID,
		Data:      data,
	})
}

func (s *Server) publishTelemetry(eventType telemetry.EventType, sessionID, planID string, data map[string]any) {
	if s.telemetryHub == nil {
		return
	}
	s.telemetryHub.Publish(telemetry.Event{
		Type:      eventType,
		SessionID: sessionID,
		PlanID:    planID,
		Data:      data,
	})
}

// CreateContextHandle stores context data and returns a handle for later retrieval.
func (s *Server) CreateContextHandle(_ context.Context, req *acppb.ContextHandleRequest) (*acppb.ContextHandle, error) {
	if req == nil {
		return nil, statusError(codes.InvalidArgument, "request required")
	}
	if strings.TrimSpace(req.Type) == "" {
		return nil, statusError(codes.InvalidArgument, "type required")
	}

	handleID := ulid.Make().String()
	now := time.Now()

	// Store the context data
	s.contextHandleMux.Lock()
	s.contextHandles[handleID] = &ContextHandleData{
		HandleID:  handleID,
		Type:      req.Type,
		Data:      req.Data,
		CreatedAt: now,
	}
	s.contextHandleMux.Unlock()

	return &acppb.ContextHandle{
		HandleId:  handleID,
		Type:      req.Type,
		SizeBytes: int64(len(req.Data)),
		CreatedAt: timestamppb.New(now),
	}, nil
}

// ResolveContextHandle retrieves the stored data for a context handle.
func (s *Server) ResolveContextHandle(_ context.Context, req *acppb.ContextHandle) (*acppb.ContextData, error) {
	if req == nil {
		return nil, statusError(codes.InvalidArgument, "request required")
	}
	if strings.TrimSpace(req.HandleId) == "" {
		return nil, statusError(codes.InvalidArgument, "handle_id required")
	}

	s.contextHandleMux.RLock()
	handle, exists := s.contextHandles[req.HandleId]
	s.contextHandleMux.RUnlock()

	if !exists {
		return nil, statusError(codes.NotFound, fmt.Sprintf("context handle %s not found", req.HandleId))
	}

	return &acppb.ContextData{
		Type: handle.Type,
		Data: handle.Data,
	}, nil
}

// DeleteContextHandle removes a context handle from storage.
func (s *Server) DeleteContextHandle(handleID string) bool {
	s.contextHandleMux.Lock()
	defer s.contextHandleMux.Unlock()

	if _, exists := s.contextHandles[handleID]; !exists {
		return false
	}
	delete(s.contextHandles, handleID)
	return true
}

// Helpers for status errors without importing grpc/status everywhere.
func statusError(code codes.Code, msg string) error {
	return status.Error(code, msg)
}

// UnaryAuthInterceptor enforces mTLS identity and injects claims.
func (s *Server) UnaryAuthInterceptor(ctx context.Context, req interface{}, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	authCtx, err := s.authorizeContext(ctx, req)
	if err != nil {
		return nil, err
	}
	return handler(authCtx, req)
}

// StreamAuthInterceptor enforces mTLS identity for streaming RPCs.
func (s *Server) StreamAuthInterceptor(srv interface{}, stream grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	authCtx, err := s.authorizeContext(stream.Context(), nil)
	if err != nil {
		return err
	}
	wrapped := &authStream{ServerStream: stream, ctx: authCtx}
	return handler(srv, wrapped)
}

type authStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *authStream) Context() context.Context {
	return s.ctx
}

func (s *Server) authorizeContext(ctx context.Context, req interface{}) (context.Context, error) {
	peerID, err := s.peerAgentID(ctx, req)
	if err != nil {
		return nil, statusError(codes.Unauthenticated, err.Error())
	}

	if reqID := requestAgentID(req); reqID != "" && peerID != reqID {
		return nil, statusError(codes.PermissionDenied, fmt.Sprintf("agent mismatch: peer %s cannot act as %s", peerID, reqID))
	}

	caps := s.agentCapabilities(ctx, peerID)
	claims := &security.Claims{
		AgentID:      peerID,
		Capabilities: caps,
	}
	return security.ContextWithClaims(ctx, claims), nil
}

func (s *Server) agentCapabilities(ctx context.Context, agentID string) []string {
	if s.coordinator == nil || strings.TrimSpace(agentID) == "" {
		return nil
	}
	agent, err := s.coordinator.GetAgent(ctx, agentID)
	if err != nil || agent == nil {
		return nil
	}
	return agent.Capabilities
}

func requestAgentID(req interface{}) string {
	switch v := req.(type) {
	case *acppb.RegisterAgentRequest:
		return v.GetAgentId()
	case *acppb.GetAgentInfoRequest:
		return v.GetAgentId()
	case *acppb.TaskStreamRequest:
		return v.GetAgentId()
	case *acppb.ToolExecutionRequest:
		return v.GetAgentId()
	case *acppb.CreateSessionRequest:
		return v.GetAgentId()
	case *acppb.InlineCompletionRequest:
		return v.GetAgentId()
	case *acppb.ProposeEditsRequest:
		return v.GetAgentId()
	case *acppb.ApplyEditsRequest:
		return v.GetAgentId()
	case *acppb.UpdateEditorStateRequest:
		return v.GetAgentId()
	default:
		return ""
	}
}

const insecureAgentIDMetadataKey = "x-buckley-agent-id"

func (s *Server) peerAgentID(ctx context.Context, req interface{}) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("missing context")
	}
	p, ok := peer.FromContext(ctx)
	if !ok {
		return "", fmt.Errorf("missing peer info")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if ok && len(tlsInfo.State.PeerCertificates) > 0 {
		return tlsInfo.State.PeerCertificates[0].Subject.CommonName, nil
	}

	if s == nil || s.cfg == nil || !s.cfg.ACP.AllowInsecureLocal {
		return "", fmt.Errorf("client certificate required")
	}
	if !isLoopbackPeer(p.Addr) {
		return "", fmt.Errorf("insecure ACP requires loopback client")
	}
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if vals := md.Get(insecureAgentIDMetadataKey); len(vals) > 0 {
			if agentID := strings.TrimSpace(vals[0]); agentID != "" {
				return agentID, nil
			}
		}
	}
	if agentID := strings.TrimSpace(requestAgentID(req)); agentID != "" {
		return agentID, nil
	}
	return "local", nil
}

func isLoopbackPeer(addr net.Addr) bool {
	if addr == nil {
		return false
	}
	if tcp, ok := addr.(*net.TCPAddr); ok && tcp.IP != nil {
		return tcp.IP.IsLoopback()
	}
	host := strings.TrimSpace(addr.String())
	if host == "" {
		return false
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = strings.TrimSpace(h)
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	return false
}
