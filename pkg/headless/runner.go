// Package headless provides API-driven conversation sessions without a TUI.
// These sessions implement the same command.Handler interface as the TUI,
// allowing web and mobile clients to drive conversations entirely via API.
package headless

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/config"
	projectcontext "m31labs.dev/buckley/pkg/context"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/ipc/command"
	knowledgehyphae "m31labs.dev/buckley/pkg/knowledge/hyphae"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/orchestrator"
	"m31labs.dev/buckley/pkg/policy"
	"m31labs.dev/buckley/pkg/prompts"
	"m31labs.dev/buckley/pkg/push"
	"m31labs.dev/buckley/pkg/rules"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/sessionexec"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/telemetry"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
	"m31labs.dev/buckley/pkg/types"
)

// RunnerState represents the current state of a headless session.
type RunnerState string

const (
	StateIdle       RunnerState = "idle"
	StateProcessing RunnerState = "processing"
	StatePaused     RunnerState = "paused"
	StateError      RunnerState = "error"
	StateStopped    RunnerState = "stopped"
)

// Event types emitted by the runner.
const (
	EventMessageCreated     = "message.created"
	EventMessageUpdated     = "message.updated"
	EventToolCallStarted    = "tool.started"
	EventToolCallComplete   = "tool.completed"
	EventApprovalRequired   = "approval.required"
	EventStateChanged       = "state.changed"
	EventCommandQueued      = "command.queued"
	EventCommandStarted     = "command.started"
	EventCommandCompleted   = "command.completed"
	EventCommandFailed      = "command.failed"
	EventCommandInterrupted = "command.interrupted"
	EventCommandBlocked     = "command.blocked"
	EventError              = "error"
	EventWarning            = "warning"
)

// RunnerEvent represents an event emitted during conversation processing.
type RunnerEvent struct {
	Type      string         `json:"type"`
	SessionID string         `json:"sessionId"`
	Timestamp time.Time      `json:"timestamp"`
	Data      map[string]any `json:"data,omitempty"`
}

// EventEmitter receives events from the runner.
type EventEmitter interface {
	Emit(event RunnerEvent)
}

// Runner drives a conversation loop without a TUI.
type Runner struct {
	mu sync.RWMutex

	sessionID     string
	session       *storage.Session
	conv          *conversation.Conversation
	modelManager  *model.Manager
	tools         *tool.Registry
	store         *storage.Store
	config        *config.Config
	emitter       EventEmitter
	telemetry     *telemetry.Hub
	modelOverride string
	systemPrompt  string
	projectCtx    *projectcontext.ProjectContext
	rulesEngine   *rules.Engine
	evaluator     types.RuleEvaluator
	resolver      *model.Resolver
	riskDetector  *orchestrator.RiskDetector

	workflow     *orchestrator.WorkflowManager
	orchestrator *orchestrator.Orchestrator

	// Policy and push notification support
	policyEngine *policy.Engine
	pushWorker   *push.Worker
	toolPolicy   *ToolPolicy

	// posture is the active glob-permission posture (pkg/policy) resolved at
	// construction time; parkedDecisions collects "ask" decisions the active
	// posture chose to park instead of blocking on human approval.
	posture         string
	parkedDecisions *policy.ParkedDecisionLog

	requiredApprovalTools map[string]struct{}
	maxToolExecTime       time.Duration
	maxRuntime            time.Duration

	// continuation lazily holds this session's provider continuation cursor
	// (decision 0001), behind the models.provider_continuation flag.
	continuation       *model.ContinuationCoordinator
	commandJournal     sessionexec.Journal
	runLedger          runledger.Store
	evidenceStore      evidence.Store
	stepJournal        agentloop.DurableStepJournal
	leaseOwner         string
	durable            bool
	durableWorkWake    chan struct{}
	durableControlWake chan struct{}
	durableTiming      DurableTiming
	transcriptLoader   DurableTranscriptLoader
	durableWG          sync.WaitGroup
	durableBuffer      []sessionexec.TranscriptEntry
	durableBufferNext  int
	durableBufferErr   error
	durableBuffering   bool
	durableEffects     int

	// usage accumulates model.Usage across every round of every turn this
	// session has run, closing the gap where each round's usage was
	// published as telemetry but never summed anywhere (the shared turn
	// engine, pkg/agentloop, now owns that accumulation per turn).
	usage model.Usage

	state               RunnerState
	lastActive          time.Time
	idleTimeout         time.Duration
	cancelFunc          context.CancelFunc
	hookCloser          io.Closer
	activeCommandID     string
	interruptedCommands map[string]struct{}

	// Pending approval state
	pendingApproval *PendingApproval
	approvalChan    chan ApprovalResponse

	commandQueue     chan command.SessionCommand
	commandStop      chan struct{}
	commandStopped   chan struct{}
	stopOnce         sync.Once
	activationOnce   sync.Once
	activationErr    error
	activated        bool
	lifecycleManaged bool
}

// PendingApproval represents a tool call awaiting user approval.
type PendingApproval struct {
	ID                 string         `json:"id"`
	ProviderToolCallID string         `json:"providerToolCallId,omitempty"`
	ToolName           string         `json:"toolName"`
	ToolArgs           map[string]any `json:"toolArgs"`
	CreatedAt          time.Time      `json:"createdAt"`
	ExpiresAt          time.Time      `json:"expiresAt"`
}

// ApprovalResponse carries the user's decision on a pending approval.
type ApprovalResponse struct {
	ID       string `json:"id"`
	Approved bool   `json:"approved"`
	Reason   string `json:"reason,omitempty"`
}

type DurableTranscriptLoader func(*conversation.Conversation, *storage.Store) error

// RunnerConfig configures a new headless runner.
type RunnerConfig struct {
	Session       *storage.Session
	ModelManager  *model.Manager
	Tools         *tool.Registry
	Store         *storage.Store
	Config        *config.Config
	Emitter       EventEmitter
	Telemetry     *telemetry.Hub
	IdleTimeout   time.Duration
	ModelOverride string
	PolicyEngine  *policy.Engine
	PushWorker    *push.Worker
	ToolPolicy    *ToolPolicy
	MaxRuntime    time.Duration
	SystemPrompt  string // If empty, uses default system prompt for tool-using agents
	AgentProfile  string // Optional rendered buckley.agent/v1 prompt section

	CommandJournal   sessionexec.Journal
	RunLedger        runledger.Store
	EvidenceStore    evidence.Store
	StepJournal      agentloop.DurableStepJournal
	LeaseOwner       string
	DurableTiming    *DurableTiming
	TranscriptLoader DurableTranscriptLoader
}

// NewRunner creates a new headless session runner.
func NewRunner(cfg RunnerConfig) (*Runner, error) {
	runner, err := newInertRunner(cfg)
	if err != nil {
		return nil, err
	}
	if err := runner.activate(); err != nil {
		runner.disposeBeforeStart()
		return nil, err
	}
	return runner, nil
}

// newInertRunner constructs a runner without starting its command loop or
// max-runtime timer. Registry publication uses this path so durable session
// persistence can finish before any runner-side work becomes observable.
func newInertRunner(cfg RunnerConfig) (*Runner, error) {
	if cfg.Session == nil {
		return nil, fmt.Errorf("session required")
	}
	if cfg.ModelManager == nil {
		return nil, fmt.Errorf("model manager required")
	}
	if cfg.Store == nil {
		return nil, fmt.Errorf("store required")
	}
	durable, leaseOwner, err := normalizeRunnerDurability(cfg)
	if err != nil {
		return nil, err
	}

	idleTimeout := cfg.IdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = 30 * time.Minute
	}

	conv := conversation.New(cfg.Session.ID)

	// Load existing conversation from storage
	if err := conv.LoadFromStorage(cfg.Store); err != nil {
		// Non-fatal - start fresh
		conv = conversation.New(cfg.Session.ID)
	}

	tools := cfg.Tools
	if tools == nil {
		tools = tool.NewRegistry()
	}

	baseCfg := cfg.Config
	if baseCfg == nil {
		baseCfg = config.DefaultConfig()
	}
	sessionCfg := resolveSessionConfig(baseCfg, cfg.Session)

	projectCtx := loadRunnerProjectContext(cfg.Session)
	var rulesEngine *rules.Engine
	if e, err := rules.NewDefaultEngine(); err == nil {
		rulesEngine = e
	}
	var evaluator types.RuleEvaluator
	if rulesEngine != nil {
		evaluator = rules.NewEngineAdapter(rulesEngine)
	}
	if candidate, ok := tools.Get("spawn_subagent"); ok {
		if subagents, ok := candidate.(*builtin.SubagentTool); ok {
			subagents.SetEvaluator(evaluator)
		}
	}

	// Wire the layered glob-permission engine (pkg/policy) as an additional
	// approval layer: every tool call now also consults posture, project,
	// user, and built-in-default rules. A deny blocks the call outright; an
	// "ask" is parked instead of blocked under postures with nobody present
	// to answer (see gate.ParkAskDecisions). The existing isDangerousTool
	// check further down keeps working as an additional, coarser layer.
	posture := policy.SelectPosture(sessionCfg.Postures.Default)
	parkedDecisions := policy.NewParkedDecisionLog()
	tools.Use(tool.NewPermissionMiddleware(buildHeadlessPermissionGate(sessionCfg, posture, cfg.Session, evaluator, parkedDecisions)))

	resolver := model.NewResolver(rulesEngine, model.ResolverConfig{
		Planning:  sessionCfg.Models.Planning,
		Execution: sessionCfg.Models.Execution,
		Review:    sessionCfg.Models.Review,
	}, cfg.ModelManager)
	riskDetector := orchestrator.NewRiskDetector(orchestrator.WithRiskRulesEngine(rulesEngine))

	systemPrompt := buildHeadlessSystemPrompt(cfg.SystemPrompt, cfg.AgentProfile, projectCtx, cfg.Session, evaluator, headlessHyphaeProjectKnowledgeContext(sessionCfg, cfg.Session))
	// Inject system prompt if this is a fresh conversation (no messages yet)
	if len(conv.Messages) == 0 {
		conv.AddSystemMessage(systemPrompt)
	}

	// Initialize policy engine if not provided
	policyEngine := cfg.PolicyEngine
	if policyEngine == nil {
		// Create engine without store - will use default policy
		policyEngine = policy.NewEngine(nil)
	}

	var requiredApprovalTools map[string]struct{}
	var maxToolExecTime time.Duration
	if cfg.ToolPolicy != nil {
		requiredApprovalTools = make(map[string]struct{}, len(cfg.ToolPolicy.RequireApproval))
		for _, name := range cfg.ToolPolicy.RequireApproval {
			name = strings.TrimSpace(strings.ToLower(name))
			if name == "" {
				continue
			}
			requiredApprovalTools[name] = struct{}{}
		}
		if cfg.ToolPolicy.MaxExecTimeSeconds > 0 {
			maxToolExecTime = time.Duration(cfg.ToolPolicy.MaxExecTimeSeconds) * time.Second
		}
	}

	initialState := StateIdle
	if cfg.Session.Status == storage.SessionStatusPaused {
		initialState = StatePaused
	}
	transcriptLoader := cfg.TranscriptLoader
	if transcriptLoader == nil {
		transcriptLoader = func(conv *conversation.Conversation, store *storage.Store) error {
			return conv.LoadFromStorage(store)
		}
	}
	r := &Runner{
		sessionID:             cfg.Session.ID,
		session:               cfg.Session,
		conv:                  conv,
		modelManager:          cfg.ModelManager,
		tools:                 tools,
		store:                 cfg.Store,
		config:                sessionCfg,
		emitter:               cfg.Emitter,
		telemetry:             cfg.Telemetry,
		modelOverride:         cfg.ModelOverride,
		systemPrompt:          systemPrompt,
		projectCtx:            projectCtx,
		rulesEngine:           rulesEngine,
		evaluator:             evaluator,
		resolver:              resolver,
		riskDetector:          riskDetector,
		policyEngine:          policyEngine,
		pushWorker:            cfg.PushWorker,
		toolPolicy:            cfg.ToolPolicy,
		posture:               posture,
		parkedDecisions:       parkedDecisions,
		requiredApprovalTools: requiredApprovalTools,
		maxToolExecTime:       maxToolExecTime,
		maxRuntime:            cfg.MaxRuntime,
		commandJournal:        cfg.CommandJournal,
		runLedger:             cfg.RunLedger,
		evidenceStore:         cfg.EvidenceStore,
		stepJournal:           cfg.StepJournal,
		leaseOwner:            leaseOwner,
		durable:               durable,
		durableTiming:         normalizeDurableTiming(cfg.DurableTiming),
		transcriptLoader:      transcriptLoader,
		state:                 initialState,
		lastActive:            time.Now(),
		idleTimeout:           idleTimeout,
		approvalChan:          make(chan ApprovalResponse, 1),
		commandQueue:          make(chan command.SessionCommand, 64),
		commandStop:           make(chan struct{}),
		commandStopped:        make(chan struct{}),
		interruptedCommands:   make(map[string]struct{}),
		lifecycleManaged:      true,
	}
	if durable {
		r.durableWorkWake = make(chan struct{}, 1)
		r.durableControlWake = make(chan struct{}, 1)
	}
	return r, nil
}

func normalizeRunnerDurability(cfg RunnerConfig) (bool, string, error) {
	values := []struct {
		name  string
		value any
	}{
		{"command journal", cfg.CommandJournal},
		{"run ledger", cfg.RunLedger},
		{"evidence store", cfg.EvidenceStore},
		{"step journal", cfg.StepJournal},
	}
	configured := 0
	for _, value := range values {
		if isRegistryTypedNil(value.value) {
			return false, "", fmt.Errorf("headless %s is typed nil", value.name)
		}
		if value.value != nil {
			configured++
		}
	}
	if configured == 0 {
		return false, "", nil
	}
	if configured != len(values) {
		return false, "", fmt.Errorf("headless durable foreground execution requires command journal, run ledger, evidence store, and fenced step journal")
	}
	owner := strings.TrimSpace(cfg.LeaseOwner)
	if owner == "" {
		owner = "headless-" + sessionexec.NewCommandID()
	}
	if err := sessionexec.ValidateClaimRequest(sessionexec.ClaimRequest{
		SessionID: cfg.Session.ID, Lane: sessionexec.LaneWork, Owner: owner, LeaseDuration: 30 * time.Second,
	}); err != nil {
		return false, "", fmt.Errorf("headless durable lease owner: %w", err)
	}
	return true, owner, nil
}

func (r *Runner) activate() error {
	if r == nil {
		return fmt.Errorf("runner unavailable")
	}
	r.activationOnce.Do(func() {
		r.mu.Lock()
		if r.state == StateStopped {
			r.activationErr = fmt.Errorf("runner stopped before activation")
			r.mu.Unlock()
			return
		}
		r.activated = true
		r.lastActive = time.Now()
		maxRuntime := r.maxRuntime
		r.mu.Unlock()

		if r.durable {
			r.startDurablePumps()
		} else {
			go r.commandLoop()
		}
		r.startMaxRuntimeTimer(maxRuntime)
	})
	return r.activationErr
}

func (r *Runner) disposeBeforeStart() {
	if r == nil {
		return
	}
	r.Stop()
}

// SessionID returns the session identifier.
func (r *Runner) SessionID() string {
	return r.sessionID
}

// State returns the current runner state.
func (r *Runner) State() RunnerState {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state
}

// LastActive returns the last activity timestamp.
func (r *Runner) LastActive() time.Time {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.lastActive
}

// Model returns the model selected for subsequent turns.
func (r *Runner) Model() string {
	if r == nil {
		return ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return strings.TrimSpace(r.modelOverride)
}

// Usage returns the token usage accumulated across every turn this session
// has run so far (model.AddUsage applied per round by the shared turn
// engine, pkg/agentloop.Controller).
func (r *Runner) Usage() model.Usage {
	if r == nil {
		return model.Usage{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.usage
}

// PendingApproval returns any pending approval, or nil.
func (r *Runner) GetPendingApproval() *PendingApproval {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.pendingApproval
}

// HandleSessionCommand implements the command.Handler interface.
func (r *Runner) HandleSessionCommand(cmd command.SessionCommand) error {
	_, err := r.AcceptCommand(context.Background(), cmd)
	return err
}

// AcceptCommand accepts one command and returns its durable receipt when the
// runner has foreground durability enabled. Legacy runners retain their
// in-memory queue behavior and return a synthetic accepted receipt.
func (r *Runner) AcceptCommand(ctx context.Context, cmd command.SessionCommand) (sessionexec.Receipt, error) {
	if r == nil {
		return sessionexec.Receipt{}, fmt.Errorf("headless runner unavailable")
	}
	if r != nil && r.durable {
		return r.acceptDurableCommand(ctx, cmd, true, false, true)
	}
	cmd.EnsureID()
	err := r.handleLegacySessionCommand(cmd)
	if err != nil {
		return sessionexec.Receipt{}, err
	}
	return sessionexec.Receipt{
		Identity: sessionexec.Identity{SessionID: r.sessionID, CommandID: cmd.ID},
		State:    sessionexec.StateAccepted,
	}, nil
}

func (r *Runner) handleLegacySessionCommand(cmd command.SessionCommand) error {
	cmd.EnsureID()
	r.mu.Lock()
	if r.lifecycleManaged && !r.activated {
		r.mu.Unlock()
		return fmt.Errorf("session not active")
	}
	r.lastActive = time.Now()
	stopped := r.state == StateStopped
	r.mu.Unlock()

	if stopped {
		return fmt.Errorf("session stopped")
	}
	if cmd.Type == "interrupt" {
		return r.interruptCommand(cmd)
	}
	if cmd.Type == "steer" {
		r.interruptActiveCommand()
	}

	if r.commandQueue == nil {
		return r.runSessionCommand(cmd)
	}

	select {
	case r.commandQueue <- cmd:
		r.emitCommandEvent(EventCommandQueued, cmd, nil)
		return nil
	default:
		return fmt.Errorf("command queue full")
	}
}

func (r *Runner) acceptDurableCommand(ctx context.Context, cmd command.SessionCommand, wake, allowInactive, announce bool) (sessionexec.Receipt, error) {
	if r == nil || r.commandJournal == nil {
		return sessionexec.Receipt{}, fmt.Errorf("headless durable command journal unavailable")
	}
	cmd.EnsureID()
	if strings.TrimSpace(cmd.SessionID) == "" {
		cmd.SessionID = r.sessionID
	}
	if cmd.SessionID != r.sessionID {
		return sessionexec.Receipt{}, fmt.Errorf("command session mismatch")
	}
	r.mu.Lock()
	if r.state == StateStopped {
		r.mu.Unlock()
		return sessionexec.Receipt{}, fmt.Errorf("session stopped")
	}
	if !allowInactive && r.lifecycleManaged && !r.activated {
		r.mu.Unlock()
		return sessionexec.Receipt{}, fmt.Errorf("session not active")
	}
	r.lastActive = time.Now()
	principal := strings.TrimSpace(cmd.AcceptedBy)
	if principal == "" && r.session != nil {
		principal = strings.TrimSpace(r.session.Principal)
	}
	r.mu.Unlock()
	if principal == "" {
		return sessionexec.Receipt{}, fmt.Errorf("authenticated command principal required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	acceptCtx, cancel := context.WithTimeout(ctx, r.durableTiming.OperationTimeout)
	defer cancel()
	receipt, err := r.commandJournal.Accept(acceptCtx, sessionexec.AcceptRequest{
		SessionID: r.sessionID, CommandID: cmd.ID, Type: cmd.Type,
		Content: cmd.Content, AcceptedBy: principal,
	})
	if err != nil {
		return sessionexec.Receipt{}, err
	}
	cmd.ID = receipt.CommandID
	cmd.Type = strings.ToLower(strings.TrimSpace(cmd.Type))
	if announce && !receipt.Duplicate {
		r.emitCommandEvent(EventCommandQueued, cmd, nil)
	}
	if receipt.TargetCommandID != "" && (cmd.Type == "steer" || cmd.Type == "interrupt") {
		r.interruptTarget(receipt.TargetCommandID)
	}
	if wake {
		r.wakeDurableLane(receipt.Lane)
	}
	return receipt, nil
}

func (r *Runner) commandLoop() {
	defer close(r.commandStopped)
	for {
		select {
		case <-r.commandStop:
			return
		case cmd, ok := <-r.commandQueue:
			if !ok {
				return
			}
			if err := r.runSessionCommand(cmd); err != nil {
				_ = r.persistSystemMessage(r.formatCommandError(err))
			}
		}
	}
}

func (r *Runner) runSessionCommand(cmd command.SessionCommand) error {
	r.mu.Lock()
	r.activeCommandID = cmd.ID
	r.mu.Unlock()
	r.emitCommandEvent(EventCommandStarted, cmd, nil)

	err := r.handleSessionCommand(cmd)

	r.mu.Lock()
	_, interrupted := r.interruptedCommands[cmd.ID]
	delete(r.interruptedCommands, cmd.ID)
	if r.activeCommandID == cmd.ID {
		r.activeCommandID = ""
	}
	r.mu.Unlock()

	if interrupted {
		r.emitCommandEvent(EventCommandInterrupted, cmd, nil)
		return nil
	}
	if err != nil {
		r.emitCommandEvent(EventCommandFailed, cmd, err)
		return err
	}
	r.emitCommandEvent(EventCommandCompleted, cmd, nil)
	return nil
}

func (r *Runner) interruptCommand(cmd command.SessionCommand) error {
	r.emitCommandEvent(EventCommandStarted, cmd, nil)
	target := r.interruptActiveCommand()
	r.emitCommandEvent(EventCommandCompleted, cmd, nil, map[string]any{
		"interruptedCommandId": target,
	})
	return nil
}

func (r *Runner) interruptActiveCommand() string {
	r.mu.Lock()
	target := r.activeCommandID
	cancel := r.cancelFunc
	if target != "" {
		if r.interruptedCommands == nil {
			r.interruptedCommands = make(map[string]struct{})
		}
		r.interruptedCommands[target] = struct{}{}
	}
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return target
}

func (r *Runner) interruptTarget(target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	r.mu.Lock()
	if r.activeCommandID != target {
		r.mu.Unlock()
		return false
	}
	if r.interruptedCommands == nil {
		r.interruptedCommands = make(map[string]struct{})
	}
	r.interruptedCommands[target] = struct{}{}
	cancel := r.cancelFunc
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return true
}

func (r *Runner) wakeDurableLane(lane sessionexec.Lane) {
	if r == nil || !r.durable {
		return
	}
	var wake chan struct{}
	if lane == sessionexec.LaneControl {
		wake = r.durableControlWake
	} else {
		wake = r.durableWorkWake
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (r *Runner) wakeDurableLanes() {
	r.wakeDurableLane(sessionexec.LaneWork)
	r.wakeDurableLane(sessionexec.LaneControl)
}

func (r *Runner) emitCommandEvent(eventType string, cmd command.SessionCommand, err error, extras ...map[string]any) {
	data := map[string]any{
		"commandId": cmd.ID,
		"type":      cmd.Type,
	}
	if err != nil {
		data["error"] = err.Error()
	}
	for _, extra := range extras {
		for key, value := range extra {
			data[key] = value
		}
	}
	r.emit(RunnerEvent{
		Type:      eventType,
		SessionID: r.sessionID,
		Timestamp: time.Now(),
		Data:      data,
	})
}

func (r *Runner) handleSessionCommand(cmd command.SessionCommand) error {
	switch cmd.Type {
	case "input", "steer", "queue":
		return r.processUserInput(cmd.Content)
	case "model":
		return r.setModel(cmd.Content)
	case "slash":
		return r.processSlashCommand(cmd.Content)
	case "approval":
		return r.processApproval(cmd.Content)
	case "pause":
		return r.pause()
	case "resume":
		return r.resumeForCommand(cmd.ID)
	default:
		return fmt.Errorf("unknown command type: %s", cmd.Type)
	}
}

func (r *Runner) setModel(modelID string) error {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return fmt.Errorf("model required")
	}
	if r.modelManager == nil {
		return fmt.Errorf("model manager unavailable")
	}
	if _, err := r.modelManager.GetModelInfo(modelID); err != nil {
		return fmt.Errorf("select model %s: %w", modelID, err)
	}
	if err := r.store.UpdateSessionModel(r.sessionID, modelID); err != nil {
		return fmt.Errorf("persist model: %w", err)
	}
	r.mu.Lock()
	r.modelOverride = modelID
	r.session.Model = modelID
	r.mu.Unlock()
	r.emit(RunnerEvent{
		Type:      EventStateChanged,
		SessionID: r.sessionID,
		Timestamp: time.Now(),
		Data:      map[string]any{"model": modelID},
	})
	return nil
}

// Stop gracefully stops the runner.
func (r *Runner) Stop() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() {
		r.mu.Lock()
		activated := r.activated
		lifecycleManaged := r.lifecycleManaged
		r.state = StateStopped
		if r.cancelFunc != nil {
			r.cancelFunc()
		}
		if r.hookCloser != nil {
			_ = r.hookCloser.Close()
			r.hookCloser = nil
		}
		r.mu.Unlock()

		if r.commandStop != nil {
			close(r.commandStop)
		}
		if lifecycleManaged && !activated {
			if r.commandStopped != nil {
				close(r.commandStopped)
			}
			return
		}
		if r.durable && r.commandStopped != nil {
			<-r.commandStopped
		}

		r.emit(RunnerEvent{
			Type:      EventStateChanged,
			SessionID: r.sessionID,
			Timestamp: time.Now(),
			Data:      map[string]any{"state": string(StateStopped)},
		})
	})
}

func (r *Runner) startMaxRuntimeTimer(maxRuntime time.Duration) {
	if r == nil || maxRuntime <= 0 || r.commandStop == nil {
		return
	}

	timer := time.NewTimer(maxRuntime)
	go func() {
		defer timer.Stop()
		select {
		case <-timer.C:
			if !r.durable {
				_ = r.persistSystemMessage(fmt.Sprintf("Session timed out after %s.", maxRuntime))
			}
			r.Stop()
		case <-r.commandStop:
			return
		}
	}()
}

// IsIdle returns true if the session has been idle longer than the timeout.
func (r *Runner) IsIdle() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.state != StateIdle || r.activeCommandID != "" || r.durableBuffering || len(r.durableBuffer) > 0 || r.durableEffects != 0 {
		return false
	}
	return time.Since(r.lastActive) > r.idleTimeout
}

func (r *Runner) processUserInput(content string) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return fmt.Errorf("empty input")
	}

	r.setState(StateProcessing)
	defer func() {
		if r.State() == StateProcessing {
			r.setState(StateIdle)
		}
	}()

	// Add user message to conversation
	r.conv.AddUserMessage(content)

	// Save to storage
	userMsg := r.conv.Messages[len(r.conv.Messages)-1]
	if err := r.conv.SaveMessage(r.store, userMsg); err != nil {
		r.emitError("failed to save user message", err)
	}

	// Run the conversation loop
	return r.runConversationLoop()
}

// runConversationLoop drives one turn -- one or more model rounds until the
// model stops requesting tools -- through the shared turn engine
// (pkg/agentloop.Controller). It preserves the exact continuation wiring,
// posture/permission integration, and danger checks handleToolCalls and
// buildChatRequest already had (both stay directly callable and directly
// tested); the engine only owns the round loop, projection, ID backfill,
// usage accumulation, and the loop guard around them.
func (r *Runner) runConversationLoop() error {
	ctx, cancel := context.WithCancel(context.Background())
	r.mu.Lock()
	r.cancelFunc = cancel
	r.mu.Unlock()
	defer func() {
		cancel()
		r.mu.Lock()
		r.cancelFunc = nil
		r.mu.Unlock()
	}()
	return r.runConversationLoopForCommand(ctx, nil)
}

func (r *Runner) runConversationLoopForCommand(ctx context.Context, command *sessionexec.Command) error {
	if r.State() == StateStopped || (command == nil && r.State() == StatePaused) {
		return nil
	}

	controller, err := r.newTurnControllerForCommand(command)
	if err != nil {
		r.emitError("failed to build turn controller", err)
		return err
	}

	result, err := controller.Run(ctx)
	if result != nil {
		r.mu.Lock()
		r.usage = model.AddUsage(r.usage, result.Usage)
		r.mu.Unlock()
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return err
		}
		r.emitError("model call failed", err)
		return err
	}
	if err := result.RequireConclusive(); err != nil {
		r.emitError("agent turn incomplete", err)
		return err
	}

	switch result.FinishReason {
	case agentloop.FinishReasonEmptyChoices:
		r.emit(RunnerEvent{
			Type:      EventWarning,
			SessionID: r.sessionID,
			Timestamp: time.Now(),
			Data:      map[string]any{"message": "Model returned empty response - ending conversation"},
		})
		return nil
	case agentloop.FinishReasonLoopGuard, agentloop.FinishReasonStepCap:
		r.emit(RunnerEvent{
			Type:      EventWarning,
			SessionID: r.sessionID,
			Timestamp: time.Now(),
			Data: map[string]any{
				"message":     "Harness stopped further tools and finalized from existing evidence",
				"stop_reason": result.Termination.Reason,
			},
		})
	}

	content := getMessageContent(result.Message.Content)
	if content != "" {
		r.persistFinalAssistantMessage(content, result.Message.Reasoning, result.Message.ReasoningDetails)
		return nil
	}

	// No content and no tool calls - unusual, emit warning.
	r.emit(RunnerEvent{
		Type:      EventWarning,
		SessionID: r.sessionID,
		Timestamp: time.Now(),
		Data:      map[string]any{"message": "Model returned no content and no tool calls - ending conversation"},
	})
	return nil
}

// persistFinalAssistantMessage appends and saves the turn's closing
// assistant message, exactly as the pre-engine runConversationLoop did for
// its "regular text response" branch.
func (r *Runner) persistFinalAssistantMessage(content, reasoning string, reasoningDetails []model.ReasoningDetail) {
	r.conv.AddAssistantMessageWithReasoningDetails(content, reasoning, reasoningDetails)
	assistantMsg := r.conv.Messages[len(r.conv.Messages)-1]
	if buffered, err := r.bufferDurableConversationMessage(assistantMsg); buffered {
		if err != nil {
			r.emitError("failed to buffer assistant message", err)
		}
		return
	}
	if err := r.conv.SaveMessage(r.store, assistantMsg); err != nil {
		r.emitError("failed to save assistant message", err)
	}
}

// newTurnController wires the shared turn engine for one conversation turn.
// A fresh Governor is created per turn (per user message), matching how the
// pre-engine loop had no cross-turn round budget either.
func (r *Runner) newTurnController() (*agentloop.Controller, error) {
	return r.newTurnControllerForCommand(nil)
}

func (r *Runner) newTurnControllerForCommand(command *sessionexec.Command) (*agentloop.Controller, error) {
	dispatchTools := agentloop.ToolDispatcher(agentloop.ToolDispatcherFunc(r.dispatchToolCalls))
	callModel := agentloop.ModelCaller(agentloop.ModelCallerFunc(r.callModel))
	if command != nil {
		dispatchTools = agentloop.ContextualToolDispatcherFunc(func(ctx context.Context, calls []agentloop.ToolDispatchCall) ([]agentloop.ToolOutcome, error) {
			return r.dispatchToolCallsForCommand(ctx, command, calls)
		})
		callModel = agentloop.ContextualModelCallerFunc(func(ctx context.Context, call agentloop.ModelDispatchCall) (*model.ChatResponse, error) {
			return r.callModelForCommand(ctx, command, call)
		})
	}
	cfg := agentloop.ControllerConfig{
		Governor:          agentloop.New(agentloop.DefaultConfig()),
		FinalizeOnStop:    true,
		LifecycleObserver: telemetry.NewAgentLoopObserver(r.telemetry),
		BuildRequest: func(ctx context.Context, round int) (model.ChatRequest, error) {
			if command != nil {
				if err := r.currentDurableBufferError(); err != nil {
					return model.ChatRequest{}, fmt.Errorf("durable transcript unavailable: %w", err)
				}
			}
			modelID, err := model.ResolvePhaseModelRequired(r.config, r.modelManager, r.rulesEngine, "execution", r.modelOverride)
			if err != nil {
				return model.ChatRequest{}, err
			}
			return r.buildRawChatRequest(modelID), nil
		},
		CallModel:     callModel,
		DispatchTools: dispatchTools,
		// The sink records the mid-loop tool exchange only: assistant
		// tool-call messages and their tool results, each persisted as it
		// lands. Plain assistant messages are the turn's terminal output,
		// which runConversationLoop persists once via
		// persistFinalAssistantMessage — appending them here too would
		// duplicate the final message.
		History: agentloop.HistorySinkFunc(func(msg model.Message) {
			switch {
			case msg.Role == "assistant" && len(msg.ToolCalls) > 0:
				r.conv.AddToolCallMessageWithReasoning(msg.ToolCalls, msg.Reasoning, msg.ReasoningDetails)
				r.persistLatestConversationMessage()
			case msg.Role == "tool":
				text, _ := model.ExtractTextContent(msg.Content)
				r.conv.AddToolResponseMessage(msg.ToolCallID, msg.Name, text)
				r.persistLatestConversationMessage()
			}
		}),
		ContextWindow: func(modelID string) int {
			if r.modelManager == nil {
				return 0
			}
			window, _ := r.modelManager.GetContextLength(modelID)
			return window
		},
	}
	if command != nil {
		cfg.RunLedger = r.runLedger
		cfg.Evidence = r.evidenceStore
		cfg.StepJournal = r.stepJournal
		cfg.RunID = command.RunID
		cfg.SessionID = command.SessionID
		cfg.TaskID = command.TaskID
		cfg.TurnID = command.TurnID
	} else {
		cfg.Continuation = r.continuationCoordinator()
		cfg.ContinuationEligible = r.continuationEligible
		cfg.ProviderID = func(modelID string) string {
			if r.modelManager == nil {
				return ""
			}
			return r.modelManager.ProviderIDForModel(modelID)
		}
	}
	return agentloop.NewController(cfg)
}

// continuationEligible reports whether this turn should attempt provider
// continuation (decision 0001): the opt-in flag is on, and the resolved
// provider implements ContinuationClient for modelID.
func (r *Runner) continuationEligible(modelID string) bool {
	if r == nil || r.config == nil || !r.config.Models.ProviderContinuation || r.modelManager == nil {
		return false
	}
	return r.modelManager.SupportsContinuation(modelID)
}

// continuationCoordinator lazily creates and caches this session's
// ContinuationCursor coordinator (decision 0001).
func (r *Runner) continuationCoordinator() *model.ContinuationCoordinator {
	if r == nil || r.modelManager == nil {
		return nil
	}
	if r.continuation == nil {
		var store model.ContinuationStore
		if r.store != nil {
			store = r.store
		}
		r.continuation = model.NewContinuationCoordinator(r.modelManager, store, r.sessionID)
	}
	return r.continuation
}

// buildChatRequest builds the next turn's request and reports whether it
// should be sent through the continuation-aware path. It gathers the raw
// request (buildRawChatRequest) and projects it (agentloop.ProjectForContinuation)
// itself, sharing the exact continuation-pin and epoch-rule logic
// pkg/agentloop.Controller uses for its own projection step, so this
// directly-tested method and the Controller-driven turn loop never diverge.
func (r *Runner) buildChatRequest() (model.ChatRequest, bool) {
	modelID := r.resolveExecutionModel()

	useContinuation := r.continuationEligible(modelID)
	var coordinator *model.ContinuationCoordinator
	if useContinuation {
		coordinator = r.continuationCoordinator()
		useContinuation = coordinator != nil
	}

	req := r.buildRawChatRequest(modelID)

	contextWindow := 0
	if r.modelManager != nil {
		contextWindow, _ = r.modelManager.GetContextLength(modelID)
	}
	providerID := ""
	if useContinuation {
		providerID = r.modelManager.ProviderIDForModel(modelID)
	}
	req = agentloop.ProjectForContinuation(req, contextWindow, coordinator, providerID, useContinuation)
	return req, useContinuation
}

// buildRawChatRequest gathers modelID, tools, reasoning configuration, and
// the full unprojected message transcript for one turn. Callers project
// req.Messages afterward (buildChatRequest above, or Controller.Run's own
// projection step) -- always using the full portable transcript here:
// ToEfficientModelMessages's independent compaction pass would otherwise run
// first -- redundant work always, and unsafe when a continuation window is
// active because it is pin-unaware and could strip reasoning from the
// region the window represents.
func (r *Runner) buildRawChatRequest(modelID string) model.ChatRequest {
	req := model.ChatRequest{
		Model:     modelID,
		SessionID: r.sessionID,
	}
	req.Messages = r.conv.ToModelMessages()
	if r.tools != nil && r.modelManager != nil && r.modelManager.SupportsTools(modelID) {
		req.Tools = r.tools.ToOpenAIFunctionsGoverned(r.evaluator, "interactive", "coding", nil, 0)
		if len(req.Tools) > 0 {
			req.ToolChoice = "auto"
		}
	}
	if effort := model.ResolveReasoningEffort(r.config, r.modelManager, r.rulesEngine, modelID, "execution"); effort != "" {
		req.Reasoning = &model.ReasoningConfig{Effort: effort}
	}
	if r.modelManager != nil && r.modelManager.SupportsParameter(modelID, "include_reasoning") {
		include := true
		req.IncludeReasoning = &include
	}
	return req
}

// callModel executes one model turn. When useContinuation is set, it calls
// through the session's ContinuationCursor coordinator (decision 0001)
// instead of the normal ChatCompletion path. On any continuation error it
// resets the cursor and retries once via the normal path -- a broken
// continuation never fails the turn.
func (r *Runner) callModel(ctx context.Context, req model.ChatRequest, useContinuation bool) (*model.ChatResponse, error) {
	startTime := time.Now()

	if r.telemetry != nil {
		r.telemetry.Publish(telemetry.Event{
			Type:      telemetry.EventBuilderStarted,
			SessionID: r.sessionID,
			Timestamp: startTime,
			Data: map[string]any{
				"model":  req.Model,
				"source": "headless",
			},
		})
	}

	continuationStatus := ""
	var resp *model.ChatResponse
	var err error
	if useContinuation && r.continuation != nil {
		resp, err = r.continuation.Call(ctx, req)
		if err != nil {
			if resp != nil {
				// The provider may have emitted billable material before the
				// continuation failed. Returning it with the error lets the
				// shared Controller preserve the partial result instead of
				// retrying the logical request and risking duplicate spend.
				return resp, err
			}
			r.continuation.Reset()
			resp, err = r.modelManager.ChatCompletion(ctx, req)
			continuationStatus = "reset"
		} else if r.continuation.Hit() {
			continuationStatus = "hit"
		} else {
			continuationStatus = "reset"
		}
	} else {
		resp, err = r.modelManager.ChatCompletion(ctx, req)
	}

	if r.telemetry != nil {
		duration := time.Since(startTime)
		eventType := telemetry.EventBuilderCompleted
		data := map[string]any{
			"model":       req.Model,
			"duration_ms": duration.Milliseconds(),
			"source":      "headless",
		}
		if continuationStatus != "" {
			data["continuation"] = continuationStatus
		}
		if err != nil {
			eventType = telemetry.EventBuilderFailed
			data["error"] = err.Error()
		} else if resp != nil {
			data["input_tokens"] = resp.Usage.PromptTokens
			data["output_tokens"] = resp.Usage.CompletionTokens
			if resp.Usage.PromptTokensDetails != nil {
				data["cached_input_tokens"] = resp.Usage.PromptTokensDetails.CachedTokens
			}
			data["cache_write_tokens"] = resp.Usage.CacheWriteTokens
		}
		r.telemetry.Publish(telemetry.Event{
			Type:      eventType,
			SessionID: r.sessionID,
			Timestamp: time.Now(),
			Data:      data,
		})
	}

	return resp, err
}

func (r *Runner) callModelForCommand(ctx context.Context, command *sessionexec.Command, call agentloop.ModelDispatchCall) (*model.ChatResponse, error) {
	if command == nil || call.RunID != command.RunID || call.TaskID != command.TaskID ||
		call.TurnID != command.TurnID || strings.TrimSpace(call.StepID) == "" ||
		(call.Kind != "model" && call.Kind != "finalize") {
		return nil, fmt.Errorf("durable model effect identity mismatch")
	}
	permit, err := r.beginDurableEffect(ctx, *command, call.StepID, sessionexec.EffectKindModel)
	if err != nil {
		return nil, err
	}
	response, callErr := r.callModel(ctx, call.Request, call.UseContinuation)
	endErr := r.endDurableEffect(permit)
	if endErr != nil {
		endErr = fmt.Errorf("close durable model effect permit: %w", endErr)
	}
	return response, errors.Join(callErr, endErr)
}

// handleToolCalls appends msg's tool-call turn to the conversation, then
// dispatches every call (dispatchToolCalls) and appends each result. It is
// the direct entry point pkg/headless tests exercise; the Controller-driven
// turn loop (newTurnController) uses dispatchToolCalls too, so both share
// one implementation of approval, posture/permission gating, audit
// logging, and danger checks.
func (r *Runner) handleToolCalls(ctx context.Context, msg model.Message) error {
	toolCalls := msg.ToolCalls
	r.conv.AddToolCallMessageWithReasoning(toolCalls, msg.Reasoning, msg.ReasoningDetails)
	r.persistLatestConversationMessage()

	outcomes, err := r.dispatchToolCalls(ctx, toolCalls)
	if err != nil {
		return err
	}
	for i, tc := range toolCalls {
		outcome := agentloop.ToolOutcome{}
		if i < len(outcomes) {
			outcome = outcomes[i]
		}
		r.conv.AddToolResponseMessage(tc.ID, tc.Function.Name, outcome.Content)
		r.persistLatestConversationMessage()
	}
	return nil
}

// dispatchToolCalls executes toolCalls in order -- interactive-shell
// rejection, approval gating, execution, audit logging, and event emission
// -- and returns one agentloop.ToolOutcome per call. It never touches
// r.conv: handleToolCalls above and the Controller-driven turn loop both
// append the returned outcomes to history themselves, so a posture-parked
// decision or a rejected approval passes straight through to the model as
// ordinary (non-error) tool content. A non-nil error means ctx was
// cancelled while waiting on an approval; the caller aborts the turn.
func (r *Runner) dispatchToolCalls(ctx context.Context, toolCalls []model.ToolCall) ([]agentloop.ToolOutcome, error) {
	contextual := make([]agentloop.ToolDispatchCall, len(toolCalls))
	for index, call := range toolCalls {
		contextual[index] = agentloop.ToolDispatchCall{
			Call:               call,
			ProviderToolCallID: call.ID,
		}
	}
	return r.dispatchToolCallsWithContext(ctx, contextual)
}

func (r *Runner) dispatchToolCallsWithContext(ctx context.Context, toolCalls []agentloop.ToolDispatchCall) ([]agentloop.ToolOutcome, error) {
	return r.dispatchToolCallsForCommand(ctx, nil, toolCalls)
}

func (r *Runner) dispatchToolCallsForCommand(ctx context.Context, command *sessionexec.Command, toolCalls []agentloop.ToolDispatchCall) ([]agentloop.ToolOutcome, error) {
	if err := r.currentDurableBufferError(); err != nil {
		return nil, fmt.Errorf("durable transcript unavailable before tool dispatch: %w", err)
	}
	outcomes := make([]agentloop.ToolOutcome, 0, len(toolCalls))

	for _, dispatchCall := range toolCalls {
		if err := r.requireDurableExecutionEnabled(ctx); err != nil {
			return outcomes, err
		}
		tc := dispatchCall.Call
		providerToolCallID := strings.TrimSpace(dispatchCall.ProviderToolCallID)
		if providerToolCallID == "" {
			providerToolCallID = tc.ID
		}
		approvalID := strings.TrimSpace(dispatchCall.ApprovalID)
		if approvalID == "" {
			approvalID = tc.ID
		}
		decision := "auto"

		r.emit(RunnerEvent{
			Type:      EventToolCallStarted,
			SessionID: r.sessionID,
			Timestamp: time.Now(),
			Data: map[string]any{
				"toolCallId": providerToolCallID,
				"approvalId": approvalID,
				"toolName":   tc.Function.Name,
				"arguments":  tc.Function.Arguments,
			},
		})

		// Parse arguments
		var args map[string]any
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			args = map[string]any{"raw": tc.Function.Arguments}
		}
		if args != nil && providerToolCallID != "" {
			args[tool.ToolCallIDParam] = providerToolCallID
		}

		if strings.EqualFold(tc.Function.Name, "run_shell") {
			if interactive, ok := args["interactive"].(bool); ok && interactive {
				message := "Tool execution denied: interactive shell sessions are not supported in headless mode"
				decision = "rejected"
				r.emit(RunnerEvent{
					Type:      EventToolCallComplete,
					SessionID: r.sessionID,
					Timestamp: time.Now(),
					Data: map[string]any{
						"toolCallId": providerToolCallID,
						"approvalId": approvalID,
						"toolName":   tc.Function.Name,
						"success":    false,
						"error":      message,
					},
				})
				if r.store != nil {
					decidedBy := "system"
					riskScore := 0
					auditApprovalID, approvalDecision, score := r.approvalAuditMetadata(approvalID)
					if approvalDecision != "" || score != 0 {
						if approvalDecision != "" {
							decidedBy = approvalDecision
						}
						riskScore = score
					}
					if logErr := r.store.LogToolExecution(&storage.ToolAuditEntry{
						SessionID:  r.sessionID,
						ApprovalID: auditApprovalID,
						ToolName:   tc.Function.Name,
						ToolInput:  tc.Function.Arguments,
						RiskScore:  riskScore,
						Decision:   decision,
						DecidedBy:  decidedBy,
						ExecutedAt: time.Now(),
						DurationMs: 0,
						ToolOutput: message,
					}); logErr != nil {
						r.emitError("failed to log tool execution", logErr)
					}
				}
				outcomes = append(outcomes, agentloop.ToolOutcome{Content: message, Success: false})
				continue
			}
		}
		r.clampToolTimeoutArgs(tc.Function.Name, args)

		// Check if tool requires approval
		if r.requiresApproval(tc.Function.Name, args) {
			approved, err := r.waitForApproval(ctx, approvalID, providerToolCallID, tc.Function.Name, args)
			if err != nil {
				return outcomes, err
			}
			if !approved {
				message := "Tool execution rejected by user"
				decision = "rejected"
				r.emit(RunnerEvent{
					Type:      EventToolCallComplete,
					SessionID: r.sessionID,
					Timestamp: time.Now(),
					Data: map[string]any{
						"toolCallId": providerToolCallID,
						"approvalId": approvalID,
						"toolName":   tc.Function.Name,
						"success":    false,
						"error":      message,
					},
				})
				if r.store != nil {
					auditApprovalID, decidedBy, riskScore := r.approvalAuditMetadata(approvalID)
					if logErr := r.store.LogToolExecution(&storage.ToolAuditEntry{
						SessionID:  r.sessionID,
						ApprovalID: auditApprovalID,
						ToolName:   tc.Function.Name,
						ToolInput:  tc.Function.Arguments,
						RiskScore:  riskScore,
						Decision:   decision,
						DecidedBy:  decidedBy,
						ExecutedAt: time.Now(),
						DurationMs: 0,
						ToolOutput: message,
					}); logErr != nil {
						r.emitError("failed to log tool execution", logErr)
					}
				}
				outcomes = append(outcomes, agentloop.ToolOutcome{Content: message, Success: false})
				continue
			}
			decision = "approved"
		}
		if err := r.requireDurableExecutionEnabled(ctx); err != nil {
			return outcomes, err
		}
		var permit sessionexec.EffectPermit
		if command != nil {
			if dispatchCall.RunID != command.RunID || dispatchCall.TaskID != command.TaskID ||
				dispatchCall.TurnID != command.TurnID || strings.TrimSpace(dispatchCall.StepID) == "" {
				return outcomes, fmt.Errorf("durable tool effect identity mismatch")
			}
			var err error
			permit, err = r.beginDurableEffect(ctx, *command, dispatchCall.StepID, sessionexec.EffectKindTool)
			if err != nil {
				return outcomes, err
			}
		}

		// Execute tool with timing
		startTime := time.Now()
		result, err := r.tools.ExecuteWithContext(ctx, tc.Function.Name, args)
		if command != nil {
			if endErr := r.endDurableEffect(permit); endErr != nil {
				err = errors.Join(err, fmt.Errorf("close durable tool effect permit: %w", endErr))
			}
		}
		duration := time.Since(startTime)

		// Log to audit trail
		auditApprovalID, decidedBy, riskScore := r.approvalAuditMetadata(approvalID)
		auditEntry := &storage.ToolAuditEntry{
			SessionID:  r.sessionID,
			ApprovalID: auditApprovalID,
			ToolName:   tc.Function.Name,
			ToolInput:  tc.Function.Arguments,
			RiskScore:  riskScore,
			Decision:   decision,
			DecidedBy:  decidedBy,
			ExecutedAt: startTime,
			DurationMs: duration.Milliseconds(),
		}

		if err != nil {
			errorResult := fmt.Sprintf("Error: %v", err)
			auditEntry.ToolOutput = errorResult

			r.emit(RunnerEvent{
				Type:      EventToolCallComplete,
				SessionID: r.sessionID,
				Timestamp: time.Now(),
				Data: map[string]any{
					"toolCallId": providerToolCallID,
					"approvalId": approvalID,
					"toolName":   tc.Function.Name,
					"success":    false,
					"error":      err.Error(),
				},
			})

			// Log failed execution
			if logErr := r.store.LogToolExecution(auditEntry); logErr != nil {
				r.emitError("failed to log tool execution", logErr)
			}
			outcomes = append(outcomes, agentloop.ToolOutcome{Content: errorResult, Success: false})
			continue
		}

		// Format result
		resultContent := r.formatToolResult(result)
		auditEntry.ToolOutput = truncateOutput(resultContent, 10000)

		r.emit(RunnerEvent{
			Type:      EventToolCallComplete,
			SessionID: r.sessionID,
			Timestamp: time.Now(),
			Data: map[string]any{
				"toolCallId": providerToolCallID,
				"approvalId": approvalID,
				"toolName":   tc.Function.Name,
				"success":    result.Success,
				"output":     truncateOutput(resultContent, 1000),
			},
		})

		// Log successful execution
		if logErr := r.store.LogToolExecution(auditEntry); logErr != nil {
			r.emitError("failed to log tool execution", logErr)
		}
		yield := tool.ResultYieldForTool(tc.Function.Name, result, nil)
		outcomes = append(outcomes, agentloop.ToolOutcome{
			Content:       resultContent,
			Success:       result.Success,
			YieldObserved: yield.Observed,
			YieldCount:    yield.Count,
			YieldUnit:     yield.Unit,
		})
	}

	return outcomes, nil
}

func (r *Runner) approvalAuditMetadata(approvalID string) (string, string, int) {
	if r == nil || r.store == nil || strings.TrimSpace(approvalID) == "" {
		return "", "", 0
	}
	approval, err := r.store.GetPendingApproval(approvalID)
	if err != nil || approval == nil {
		return "", "", 0
	}
	return approval.ID, approval.DecidedBy, approval.RiskScore
}

// evaluatePolicy runs the policy engine to determine if approval is needed.
// Returns the evaluation result.
func (r *Runner) evaluatePolicy(toolName string, args map[string]any) policy.EvaluationResult {
	call := policy.ToolCall{
		Name:      toolName,
		Input:     args,
		SessionID: r.sessionID,
	}
	return r.policyEngine.Evaluate(call)
}

func (r *Runner) requiresApproval(toolName string, args map[string]any) bool {
	toolName = strings.TrimSpace(strings.ToLower(toolName))
	if toolName != "" && len(r.requiredApprovalTools) > 0 {
		if _, ok := r.requiredApprovalTools[toolName]; ok {
			return true
		}
	}

	if r.rulesEngine != nil {
		assessment := r.assessToolRisk(toolName, args)
		result, err := r.rulesEngine.EvalStrategy("approval", "approval_gate", map[string]any{
			"approval": map[string]any{"mode": r.approvalMode()},
			"risk":     map[string]any{"level": assessment.Level},
		})
		if err == nil {
			action, _ := result.Params["action"].(string)
			return action != "allow"
		}
	}

	// Use policy engine if available
	if r.policyEngine != nil {
		result := r.evaluatePolicy(toolName, nil)
		return result.RequiresApproval
	}

	// Fallback to simple check
	return r.isDangerousTool(toolName)
}

func (r *Runner) isDangerousTool(toolName string) bool {
	dangerousTools := map[string]bool{
		"write_file":     true,
		"apply_patch":    true,
		"run_shell":      true,
		"run_code":       true,
		"search_replace": true,
	}
	return dangerousTools[toolName]
}

func (r *Runner) clampToolTimeoutArgs(toolName string, args map[string]any) {
	if r == nil || args == nil || r.maxToolExecTime <= 0 {
		return
	}
	maxSeconds := int(r.maxToolExecTime.Seconds())
	if maxSeconds <= 0 {
		return
	}

	switch strings.TrimSpace(strings.ToLower(toolName)) {
	case "run_shell", "run_code", "run_tests":
		clampTimeoutSeconds(args, "timeout_seconds", maxSeconds)
	}
}

func clampTimeoutSeconds(args map[string]any, key string, maxSeconds int) {
	if args == nil || strings.TrimSpace(key) == "" || maxSeconds <= 0 {
		return
	}

	raw, ok := args[key]
	if !ok {
		args[key] = maxSeconds
		return
	}

	current, ok := anyToInt(raw)
	if !ok || current <= 0 || current > maxSeconds {
		args[key] = maxSeconds
	}
}

func anyToInt(value any) (int, bool) {
	switch v := value.(type) {
	case float64:
		return int(v), true
	case float32:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	case int32:
		return int(v), true
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return int(n), true
		}
		return 0, false
	case string:
		v = strings.TrimSpace(v)
		if v == "" {
			return 0, false
		}
		if n, err := strconv.Atoi(v); err == nil {
			return n, true
		}
		return 0, false
	default:
		return 0, false
	}
}

func (r *Runner) waitForApproval(ctx context.Context, approvalID, providerToolCallID, toolName string, args map[string]any) (bool, error) {
	// Evaluate approval risk for display and audit storage.
	var riskScore int
	var riskReasons []string
	if r.rulesEngine != nil {
		assessment := r.assessToolRisk(toolName, args)
		riskScore = assessment.Score
		riskReasons = assessment.Reasons
	} else if r.policyEngine != nil {
		result := r.evaluatePolicy(toolName, args)
		riskScore = result.RiskScore
		riskReasons = result.RiskReasons
	}

	expiresAt := time.Now().Add(5 * time.Minute)

	approval := &PendingApproval{
		ID:                 approvalID,
		ProviderToolCallID: providerToolCallID,
		ToolName:           toolName,
		ToolArgs:           args,
		CreatedAt:          time.Now(),
		ExpiresAt:          expiresAt,
	}

	// Persist to storage
	toolInputJSON, _ := json.Marshal(args)
	storedApproval := &storage.PendingApproval{
		ID:          approvalID,
		SessionID:   r.sessionID,
		ToolName:    toolName,
		ToolInput:   string(toolInputJSON),
		RiskScore:   riskScore,
		RiskReasons: riskReasons,
		Status:      "pending",
		ExpiresAt:   expiresAt,
		CreatedAt:   time.Now(),
	}
	if r.durable {
		return r.waitForDurableApproval(ctx, approval, storedApproval)
	}

	if err := r.store.CreatePendingApproval(storedApproval); err != nil {
		// Log but continue - approval can still work via channel
		r.emitError("failed to persist pending approval", err)
	}

	r.mu.Lock()
	r.pendingApproval = approval
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		r.pendingApproval = nil
		r.mu.Unlock()
	}()

	r.emit(RunnerEvent{
		Type:      EventApprovalRequired,
		SessionID: r.sessionID,
		Timestamp: time.Now(),
		Data: map[string]any{
			"id":          approvalID,
			"toolCallId":  providerToolCallID,
			"toolName":    toolName,
			"toolArgs":    args,
			"riskScore":   riskScore,
			"riskReasons": riskReasons,
			"expiresAt":   approval.ExpiresAt,
		},
	})

	// Send push notification if worker is available
	if r.pushWorker != nil {
		if err := r.pushWorker.NotifyApprovalRequired(ctx, storedApproval); err != nil {
			// Log but don't fail - user can still approve via other channels
			r.emitError("failed to send push notification", err)
		}
	}

	// Wait for approval response or timeout
	select {
	case <-ctx.Done():
		r.updateApprovalStatus(approvalID, "expired", "", "")
		return false, ctx.Err()
	case resp := <-r.approvalChan:
		if resp.ID == approvalID {
			status := "rejected"
			if resp.Approved {
				status = "approved"
			}
			r.updateApprovalStatus(approvalID, status, "headless-runner", resp.Reason)
			return resp.Approved, nil
		}
		return false, fmt.Errorf("approval ID mismatch")
	case <-time.After(5 * time.Minute):
		r.updateApprovalStatus(approvalID, "expired", "", "timeout")
		return false, fmt.Errorf("approval timeout")
	}
}

func (r *Runner) waitForDurableApproval(ctx context.Context, approval *PendingApproval, candidate *storage.PendingApproval) (bool, error) {
	if r == nil || r.store == nil || approval == nil || candidate == nil {
		return false, fmt.Errorf("durable approval storage unavailable")
	}
	stored, err := r.store.GetPendingApproval(candidate.ID)
	if err != nil {
		return false, fmt.Errorf("read durable approval: %w", err)
	}
	created := false
	if stored == nil {
		createErr := r.store.CreatePendingApproval(candidate)
		if createErr == nil {
			stored = candidate
			created = true
		} else {
			// Another process may have inserted the same stable tool-call
			// approval between our read and insert. Reconcile only the exact
			// immutable request; every other conflict fails closed.
			stored, err = r.store.GetPendingApproval(candidate.ID)
			if err != nil {
				return false, fmt.Errorf("reconcile durable approval: %w", err)
			}
			if stored == nil {
				return false, fmt.Errorf("persist durable approval: %w", createErr)
			}
		}
	}
	if err := validateDurablePendingApproval(stored, candidate); err != nil {
		return false, err
	}
	if decided, done, err := durableApprovalDecision(stored); done {
		return decided, err
	}

	approval.CreatedAt = stored.CreatedAt
	approval.ExpiresAt = stored.ExpiresAt
	r.mu.Lock()
	r.pendingApproval = approval
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		if r.pendingApproval != nil && r.pendingApproval.ID == approval.ID {
			r.pendingApproval = nil
		}
		r.mu.Unlock()
	}()

	r.emit(RunnerEvent{
		Type: EventApprovalRequired, SessionID: r.sessionID, Timestamp: time.Now(),
		Data: map[string]any{
			"id": approval.ID, "toolCallId": approval.ProviderToolCallID,
			"toolName": approval.ToolName, "toolArgs": approval.ToolArgs,
			"riskScore": stored.RiskScore, "riskReasons": stored.RiskReasons, "expiresAt": approval.ExpiresAt,
		},
	})
	if created && r.pushWorker != nil {
		if err := r.pushWorker.NotifyApprovalRequired(ctx, stored); err != nil {
			r.emitError("failed to send push notification", err)
		}
	}

	const durableApprovalPollInterval = 250 * time.Millisecond
	poll := time.NewTicker(durableApprovalPollInterval)
	defer poll.Stop()
	expiresIn := time.Until(approval.ExpiresAt)
	if expiresIn < 0 {
		expiresIn = 0
	}
	timer := time.NewTimer(expiresIn)
	defer timer.Stop()
	for {
		var expiredTimer bool
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-timer.C:
			expiredTimer = true
		case <-poll.C:
		case <-r.approvalChan:
		}
		if expiredTimer {
			// ExpirePendingApproval performs the authoritative clock check
			// and rereads the row under the same write lock. Never discard
			// a concurrent approval or rejection returned by that call.
			current, _, err := r.store.ExpirePendingApproval(candidate.ID, candidate.SessionID)
			if err != nil {
				return false, fmt.Errorf("expire durable approval: %w", err)
			}
			if err := validateDurablePendingApproval(current, candidate); err != nil {
				return false, err
			}
			if decided, done, err := durableApprovalDecision(current); done {
				return decided, err
			}
			// The local clock reached the candidate expiry first. The
			// canonical row is still pending, so retry at its authoritative
			// expiry (or soon when the clocks disagree) instead of timing
			// out and discarding the pending approval.
			expiresIn = time.Until(current.ExpiresAt)
			if expiresIn <= 0 {
				expiresIn = durableApprovalPollInterval
			}
			timer.Reset(expiresIn)
			continue
		}
		current, err := r.store.GetPendingApproval(candidate.ID)
		if err != nil {
			return false, fmt.Errorf("poll durable approval: %w", err)
		}
		if err := validateDurablePendingApproval(current, candidate); err != nil {
			return false, err
		}
		if decided, done, err := durableApprovalDecision(current); done {
			return decided, err
		}
		// Expiration is decided by the database clock, not the local timer.
		// A local clock can reach the stored expiry first; in that case the
		// atomic expiry call returns the still-pending canonical row and the
		// loop must continue until the database deadline or ctx expires.
		if !current.ExpiresAt.After(time.Now().UTC()) {
			current, _, err = r.store.ExpirePendingApproval(candidate.ID, candidate.SessionID)
			if err != nil {
				return false, fmt.Errorf("expire durable approval: %w", err)
			}
			if err := validateDurablePendingApproval(current, candidate); err != nil {
				return false, err
			}
			if decided, done, err := durableApprovalDecision(current); done {
				return decided, err
			}
			expiresIn = time.Until(current.ExpiresAt)
			if expiresIn <= 0 {
				expiresIn = durableApprovalPollInterval
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(expiresIn)
		}
	}
}

func validateDurablePendingApproval(stored, candidate *storage.PendingApproval) error {
	if stored == nil || candidate == nil {
		return fmt.Errorf("durable approval missing")
	}
	if stored.ID != candidate.ID || stored.SessionID != candidate.SessionID ||
		stored.ToolName != candidate.ToolName || stored.ToolInput != candidate.ToolInput {
		return fmt.Errorf("durable approval identity mismatch")
	}
	switch stored.Status {
	case "pending", "approved", "rejected", "expired":
		return nil
	default:
		return fmt.Errorf("durable approval has invalid status")
	}
}

func durableApprovalDecision(stored *storage.PendingApproval) (bool, bool, error) {
	if stored == nil {
		return false, true, fmt.Errorf("durable approval missing")
	}
	switch stored.Status {
	case "approved":
		return true, true, nil
	case "rejected":
		return false, true, nil
	case "expired":
		return false, true, fmt.Errorf("approval expired")
	default:
		return false, false, nil
	}
}

// updateApprovalStatus updates the approval status in storage.
func (r *Runner) updateApprovalStatus(id, status, decidedBy, reason string) {
	if r == nil || r.store == nil {
		return
	}
	approval, err := r.store.GetPendingApproval(id)
	if err != nil || approval == nil {
		return
	}

	switch strings.TrimSpace(status) {
	case "expired":
		if _, _, err := r.store.ExpirePendingApproval(approval.ID, approval.SessionID); err != nil {
			r.emitError("failed to expire pending approval", err)
		}
	case "approved", "rejected":
		if _, _, err := r.store.DecidePendingApproval(
			approval.ID, approval.SessionID, status, decidedBy, reason, time.Now().UTC(),
		); err != nil && !errors.Is(err, storage.ErrApprovalDecisionConflict) {
			r.emitError("failed to decide pending approval", err)
		}
	default:
		if approval.Status != "pending" {
			return
		}
		approval.Status = status
		if decidedBy != "" {
			approval.DecidedBy = decidedBy
		}
		approval.DecidedAt = time.Now()
		approval.DecisionReason = strings.TrimSpace(reason)
		if err := r.store.UpdatePendingApproval(approval); err != nil {
			r.emitError("failed to update approval status", err)
		}
	}
}

func (r *Runner) formatToolResult(result *builtin.Result) string {
	if result == nil {
		return "No result"
	}
	if !result.Success {
		return fmt.Sprintf("Error: %s", result.Error)
	}

	// Try to get meaningful output from DisplayData first
	if msg, ok := result.DisplayData["message"].(string); ok && msg != "" {
		return msg
	}

	// Serialize Data as JSON
	if len(result.Data) > 0 {
		data, err := json.MarshalIndent(result.Data, "", "  ")
		if err == nil {
			return string(data)
		}
	}

	return "Success"
}

func (r *Runner) processSlashCommand(content string) error {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "/") {
		return fmt.Errorf("not a slash command")
	}

	fields := strings.Fields(content)
	if len(fields) == 0 {
		return fmt.Errorf("empty slash command")
	}
	cmd := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	args := fields[1:]
	if strings.Contains(cmd, "/") || strings.Contains(cmd, "\\") {
		// Treat absolute/relative paths as regular input, not commands.
		return r.processUserInput(content)
	}

	switch cmd {
	case "clear":
		r.conv.Clear()
		return r.persistSystemMessage("Conversation cleared.")
	case "plan":
		return r.runPlanCommand(args)
	case "execute":
		return r.runExecuteCommand(args)
	case "status":
		return r.runStatusCommand()
	case "plans":
		return r.runPlansCommand()
	case "resume":
		return r.runResumePlanCommand(args)
	case "workflow":
		return r.runWorkflowCommand(args)
	case "model":
		if len(args) != 1 {
			return fmt.Errorf("usage: /model <model-id>")
		}
		return r.setModel(args[0])
	default:
		return fmt.Errorf("unknown command: %s", cmd)
	}
}

func (r *Runner) processApproval(content string) error {
	var resp ApprovalResponse
	if err := json.Unmarshal([]byte(content), &resp); err != nil {
		// Try simple format: "approve" or "reject"
		content = strings.ToLower(strings.TrimSpace(content))
		r.mu.RLock()
		pending := r.pendingApproval
		r.mu.RUnlock()

		if pending == nil {
			return fmt.Errorf("no pending approval")
		}

		resp = ApprovalResponse{
			ID:       pending.ID,
			Approved: content == "approve" || content == "yes" || content == "y",
		}
	}

	select {
	case r.approvalChan <- resp:
		return nil
	default:
		return fmt.Errorf("no pending approval")
	}
}

func (r *Runner) pause() error {
	r.setState(StatePaused)
	return nil
}

func (r *Runner) resume() error {
	return r.resumeForCommand("")
}

// resumeForCommand restores the session to processing when another command
// is still active. A legacy command loop marks the resume command itself as
// active, so its ID is excluded from that check.
func (r *Runner) resumeForCommand(commandID string) error {
	r.mu.Lock()
	if r.state != StatePaused {
		r.mu.Unlock()
		return fmt.Errorf("session not paused")
	}
	r.mu.Unlock()
	r.setResumedState(commandID)
	return nil
}

// setResumedState applies the resumed projection without requiring the
// caller's current state to be paused. Durable control commands preserve
// their existing idempotent behavior while still reflecting active work.
func (r *Runner) setResumedState(commandID string) {
	r.mu.Lock()
	activeCommandID := r.activeCommandID
	if strings.TrimSpace(commandID) != "" && activeCommandID == commandID {
		activeCommandID = ""
	}
	nextState := StateIdle
	if activeCommandID != "" {
		nextState = StateProcessing
	}
	oldState := r.state
	r.state = nextState
	r.lastActive = time.Now()
	r.mu.Unlock()

	if oldState != nextState {
		r.emit(RunnerEvent{
			Type:      EventStateChanged,
			SessionID: r.sessionID,
			Timestamp: time.Now(),
			Data: map[string]any{
				"state":     string(nextState),
				"prevState": string(oldState),
			},
		})
	}
}

func (r *Runner) setState(state RunnerState) {
	r.mu.Lock()
	oldState := r.state
	r.state = state
	r.lastActive = time.Now()
	r.mu.Unlock()

	if oldState != state {
		r.emit(RunnerEvent{
			Type:      EventStateChanged,
			SessionID: r.sessionID,
			Timestamp: time.Now(),
			Data: map[string]any{
				"state":     string(state),
				"prevState": string(oldState),
			},
		})
	}
}

func (r *Runner) emit(event RunnerEvent) {
	if r.emitter != nil {
		r.emitter.Emit(event)
	}
}

func (r *Runner) emitError(msg string, err error) {
	r.setState(StateError)
	errorText := ""
	if err != nil {
		errorText = err.Error()
		if r.durable {
			errorText = telemetry.SanitizeText(errorText, sessionexec.MaxErrorTextBytes)
		}
	}
	r.emit(RunnerEvent{
		Type:      EventError,
		SessionID: r.sessionID,
		Timestamp: time.Now(),
		Data: map[string]any{
			"message": msg,
			"error":   errorText,
		},
	})
}

func (r *Runner) persistSystemMessage(content string) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	if r.conv == nil || r.store == nil {
		return nil
	}
	r.conv.AddSystemMessage(content)
	msg := r.conv.Messages[len(r.conv.Messages)-1]
	if buffered, err := r.bufferDurableConversationMessage(msg); buffered {
		if err != nil {
			r.emitError("failed to buffer system message", err)
		}
		return err
	}
	if err := r.conv.SaveMessage(r.store, msg); err != nil {
		r.emitError("failed to save system message", err)
		return err
	}
	return nil
}

func (r *Runner) persistLatestConversationMessage() {
	if r == nil || r.conv == nil || r.store == nil || len(r.conv.Messages) == 0 {
		return
	}
	msg := r.conv.Messages[len(r.conv.Messages)-1]
	if buffered, err := r.bufferDurableConversationMessage(msg); buffered {
		if err != nil {
			r.emitError("failed to buffer conversation message", err)
		}
		return
	}
	if err := r.conv.SaveMessage(r.store, msg); err != nil {
		r.emitError("failed to save conversation message", err)
	}
}

func (r *Runner) ensureOrchestrator() (*orchestrator.Orchestrator, *orchestrator.WorkflowManager, error) {
	r.mu.RLock()
	if r.orchestrator != nil && r.workflow != nil {
		orch := r.orchestrator
		wf := r.workflow
		r.mu.RUnlock()
		return orch, wf, nil
	}
	r.mu.RUnlock()

	if r.modelManager == nil {
		return nil, nil, fmt.Errorf("model manager not configured")
	}

	projectRoot := ""
	if r.session != nil {
		projectRoot = strings.TrimSpace(r.session.ProjectPath)
		if projectRoot == "" {
			projectRoot = strings.TrimSpace(r.session.GitRepo)
		}
	}
	if projectRoot != "" {
		if abs, err := filepath.Abs(projectRoot); err == nil {
			projectRoot = abs
		}
		projectRoot = filepath.Clean(projectRoot)
	}

	cfg := r.config
	if cfg == nil {
		cfg = config.DefaultConfig()
	}
	// Ensure any relative artifact paths resolve within the session project.
	cfg = resolveSessionConfig(cfg, r.session)
	docsRoot := docsRootFromConfig(cfg)

	wf := orchestrator.NewWorkflowManager(cfg, r.modelManager, r.tools, r.store, docsRoot, projectRoot, r.telemetry)
	wf.SetSessionID(r.sessionID)
	if err := wf.InitializeDocumentation(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to initialize docs hierarchy: %v\n", err)
	}

	orch := orchestrator.NewOrchestrator(r.store, r.modelManager, r.tools, cfg, wf, nil, nil, nil)

	r.mu.Lock()
	r.workflow = wf
	r.orchestrator = orch
	r.mu.Unlock()

	return orch, wf, nil
}

func docsRootFromConfig(cfg *config.Config) string {
	if cfg == nil {
		return "docs"
	}
	planDir := strings.TrimSpace(cfg.Artifacts.PlanningDir)
	if planDir == "" {
		planDir = filepath.Join("docs", "plans")
	}
	return filepath.Dir(planDir)
}

func resolveSessionConfig(cfg *config.Config, sess *storage.Session) *config.Config {
	if cfg == nil {
		return config.DefaultConfig()
	}
	projectRoot := ""
	if sess != nil {
		projectRoot = strings.TrimSpace(sess.ProjectPath)
		if projectRoot == "" {
			projectRoot = strings.TrimSpace(sess.GitRepo)
		}
	}
	next := *cfg
	if strings.TrimSpace(projectRoot) == "" {
		return &next
	}
	if abs, err := filepath.Abs(projectRoot); err == nil {
		projectRoot = abs
	}
	projectRoot = filepath.Clean(projectRoot)

	resolve := func(path string) string {
		path = strings.TrimSpace(path)
		if path == "" || filepath.IsAbs(path) {
			return path
		}
		return filepath.Clean(filepath.Join(projectRoot, path))
	}

	next.Artifacts.PlanningDir = resolve(next.Artifacts.PlanningDir)
	next.Artifacts.ExecutionDir = resolve(next.Artifacts.ExecutionDir)
	next.Artifacts.ReviewDir = resolve(next.Artifacts.ReviewDir)
	next.Artifacts.ArchiveDir = resolve(next.Artifacts.ArchiveDir)
	return &next
}

func (r *Runner) runPlanCommand(args []string) error {
	orch, _, err := r.ensureOrchestrator()
	if err != nil {
		return err
	}
	if len(args) < 2 {
		return fmt.Errorf("usage: /plan <feature-name> <description>")
	}

	featureName := args[0]
	description := strings.Join(args[1:], " ")

	r.setState(StateProcessing)
	defer func() {
		if r.State() == StateProcessing {
			r.setState(StateIdle)
		}
	}()

	_ = r.persistSystemMessage(fmt.Sprintf("⏳ Planning %q…", featureName))

	plan, err := orch.PlanFeature(featureName, description)
	if err != nil {
		if handled := r.handleWorkflowPause(err); handled {
			return nil
		}
		return err
	}

	summary := formatPlanSummary(plan, r.config)
	summary += "\nPlan created. Use /execute to start implementation or /status to inspect details."
	return r.persistSystemMessage(summary)
}

func (r *Runner) runExecuteCommand(args []string) error {
	orch, _, err := r.ensureOrchestrator()
	if err != nil {
		return err
	}

	if orch.GetCurrentPlan() == nil {
		return fmt.Errorf("no active plan. Use /plan to create one or /resume <plan-id> to load an existing plan")
	}

	r.setState(StateProcessing)
	defer r.setState(StateIdle)

	_ = r.persistSystemMessage("⏳ Executing…")

	if len(args) > 0 {
		taskID := args[0]
		if err := orch.ExecuteTask(taskID); err != nil {
			if handled := r.handleWorkflowPause(err); handled {
				return nil
			}
			return err
		}
		return r.persistSystemMessage(fmt.Sprintf("✓ Task %s completed.", taskID))
	}

	if err := orch.ExecutePlan(); err != nil {
		if handled := r.handleWorkflowPause(err); handled {
			return nil
		}
		return err
	}
	return r.persistSystemMessage("✓ Plan execution completed.")
}

func (r *Runner) runStatusCommand() error {
	orch, wf, err := r.ensureOrchestrator()
	if err != nil {
		return err
	}
	plan := orch.GetCurrentPlan()
	if plan == nil {
		return r.persistSystemMessage("No active plan. Use /plan to create one or /resume <plan-id> to load an existing plan.")
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Plan: %s\n\n", plan.FeatureName))
	b.WriteString(fmt.Sprintf("Plan ID: %s\n", plan.ID))
	b.WriteString(fmt.Sprintf("Created: %s\n", plan.CreatedAt.Format("2006-01-02 15:04")))

	completed := 0
	total := len(plan.Tasks)
	for _, task := range plan.Tasks {
		if task.Status == orchestrator.TaskCompleted {
			completed++
		}
	}
	percent := 0.0
	if total > 0 {
		percent = float64(completed) / float64(total) * 100
	}
	b.WriteString(fmt.Sprintf("Progress: %d/%d tasks completed (%.0f%%)\n\n", completed, total, percent))

	b.WriteString("Tasks:\n")
	for i, task := range plan.Tasks {
		status := planTaskStatus(task.Status)
		b.WriteString(fmt.Sprintf("  %s %d. %s\n", status, i+1, task.Title))
	}

	if wf != nil {
		b.WriteString("\nWorkflow:\n")
		phase := string(wf.GetCurrentPhase())
		if phase == "" {
			phase = "unknown"
		}
		b.WriteString(fmt.Sprintf("  Phase: %s\n", phase))
		agent := wf.GetActiveAgent()
		if agent == "" {
			agent = "N/A"
		}
		b.WriteString(fmt.Sprintf("  Active Agent: %s\n", agent))
		if paused, reason, question, at := wf.GetPauseInfo(); paused {
			if reason == "" {
				reason = "Awaiting user input"
			}
			if question == "" {
				question = "Confirm next steps"
			}
			when := ""
			if !at.IsZero() {
				when = fmt.Sprintf(" (since %s)", at.Format("15:04:05"))
			}
			b.WriteString(fmt.Sprintf("  Status: PAUSED%s\n", when))
			b.WriteString(fmt.Sprintf("    Reason: %s\n", reason))
			b.WriteString(fmt.Sprintf("    Action: %s\n", question))
		}
	}

	return r.persistSystemMessage(b.String())
}

func (r *Runner) runPlansCommand() error {
	orch, _, err := r.ensureOrchestrator()
	if err != nil {
		return err
	}

	plans, err := orch.ListPlans()
	if err != nil {
		return err
	}
	return r.persistPlanList(plans)
}

func (r *Runner) persistPlanList(plans []orchestrator.Plan) error {
	if len(plans) == 0 {
		return r.persistSystemMessage("No saved plans found. Use /plan to create one.")
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Saved Plans (%d):\n\n", len(plans)))
	for _, plan := range plans {
		completed := 0
		for _, task := range plan.Tasks {
			if task.Status == orchestrator.TaskCompleted {
				completed++
			}
		}
		b.WriteString(fmt.Sprintf("  %s\n", plan.ID))
		b.WriteString(fmt.Sprintf("    Feature: %s\n", plan.FeatureName))
		b.WriteString(fmt.Sprintf("    Created: %s\n", plan.CreatedAt.Format("2006-01-02 15:04")))
		b.WriteString(fmt.Sprintf("    Progress: %d/%d tasks\n", completed, len(plan.Tasks)))
		b.WriteString("\n")
	}
	b.WriteString("Use /resume <plan-id> to continue work on a plan.\n")
	return r.persistSystemMessage(b.String())
}

func (r *Runner) runResumePlanCommand(args []string) error {
	orch, _, err := r.ensureOrchestrator()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return fmt.Errorf("usage: /resume <plan-id>")
	}
	planID := args[0]
	if err := orch.ResumeFeature(planID); err != nil {
		return err
	}
	plan := orch.GetCurrentPlan()
	if plan == nil {
		return fmt.Errorf("plan not loaded")
	}
	completed := 0
	for _, task := range plan.Tasks {
		if task.Status == orchestrator.TaskCompleted {
			completed++
		}
	}
	return r.persistSystemMessage(fmt.Sprintf("✓ Resumed plan: %s (%d/%d tasks completed)\nUse /status to see details.", plan.FeatureName, completed, len(plan.Tasks)))
}

func (r *Runner) runWorkflowCommand(args []string) error {
	_, wf, err := r.ensureOrchestrator()
	if err != nil {
		return err
	}
	if wf == nil {
		return fmt.Errorf("workflow manager not initialized")
	}

	action := "status"
	if len(args) > 0 {
		action = strings.ToLower(args[0])
	}

	switch action {
	case "status":
		return r.persistSystemMessage(formatWorkflowStatus(wf))
	case "pause":
		reason := "Manual pause via /workflow pause"
		if len(args) > 1 {
			reason = strings.Join(args[1:], " ")
		}
		if err := wf.Pause(reason, "Awaiting user instructions"); err != nil && !errors.Is(err, orchestrator.ErrWorkflowPaused) {
			return err
		}
		r.setState(StatePaused)
		return r.persistSystemMessage(fmt.Sprintf("⚠ Workflow paused (%s)", reason))
	case "resume":
		note := "Manual resume via /workflow resume"
		if len(args) > 1 {
			note = strings.Join(args[1:], " ")
		}
		wf.Resume(note)
		r.mu.RLock()
		currentCommandID := r.activeCommandID
		r.mu.RUnlock()
		r.setResumedState(currentCommandID)
		return r.persistSystemMessage(fmt.Sprintf("✓ Workflow resumed (%s)", note))
	case "phases":
		return r.persistSystemMessage(formatWorkflowPhases(wf.TaskPhases()))
	default:
		return fmt.Errorf("unknown workflow action: %s (try status|pause|resume|phases)", action)
	}
}

func (r *Runner) handleWorkflowPause(err error) bool {
	var pauseErr *orchestrator.WorkflowPauseError
	if err == nil || !errors.As(err, &pauseErr) {
		return false
	}

	reason := strings.TrimSpace(pauseErr.Reason)
	if reason == "" {
		reason = "Awaiting user input"
	}
	action := strings.TrimSpace(pauseErr.Question)
	if action == "" {
		action = "Confirm next steps"
	}

	_ = r.persistSystemMessage(fmt.Sprintf("⚠ Workflow paused: %s\nAction required: %s", reason, action))
	r.setState(StatePaused)
	return true
}

func (r *Runner) formatCommandError(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("Error: %v", err)
}

func formatPlanSummary(plan *orchestrator.Plan, cfg *config.Config) string {
	if plan == nil {
		return "Plan unavailable."
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("✓ Created plan: %s\n", plan.FeatureName))
	b.WriteString(fmt.Sprintf("Plan ID: %s\n", plan.ID))
	b.WriteString(fmt.Sprintf("Tasks: %d\n", len(plan.Tasks)))

	if cfg != nil && strings.TrimSpace(cfg.Artifacts.PlanningDir) != "" {
		base := strings.TrimRight(cfg.Artifacts.PlanningDir, string(filepath.Separator))
		b.WriteString(fmt.Sprintf("Plan file: %s\n", filepath.Join(base, plan.ID+".md")))
	}

	b.WriteString("\nTasks:\n")
	for i, task := range plan.Tasks {
		b.WriteString(fmt.Sprintf("  %d. %s\n", i+1, task.Title))
	}
	return b.String()
}

func planTaskStatus(status orchestrator.TaskStatus) string {
	switch status {
	case orchestrator.TaskPending:
		return "[ ]"
	case orchestrator.TaskInProgress:
		return "[→]"
	case orchestrator.TaskCompleted:
		return "[✓]"
	case orchestrator.TaskFailed:
		return "[✗]"
	case orchestrator.TaskSkipped:
		return "[-]"
	default:
		return "[?]"
	}
}

func formatWorkflowStatus(wf *orchestrator.WorkflowManager) string {
	if wf == nil {
		return "Workflow manager not initialized."
	}
	phase := string(wf.GetCurrentPhase())
	if phase == "" {
		phase = "unknown"
	}
	agent := wf.GetActiveAgent()
	if strings.TrimSpace(agent) == "" {
		agent = "N/A"
	}

	var b strings.Builder
	b.WriteString("Workflow Status\n")
	b.WriteString(fmt.Sprintf("  Phase: %s\n", phase))
	b.WriteString(fmt.Sprintf("  Active Agent: %s\n", agent))

	if paused, reason, question, at := wf.GetPauseInfo(); paused {
		if reason == "" {
			reason = "Awaiting user input"
		}
		if question == "" {
			question = "Confirm how to proceed"
		}
		when := ""
		if !at.IsZero() {
			when = fmt.Sprintf(" (since %s)", at.Format("15:04:05"))
		}
		b.WriteString(fmt.Sprintf("  Status: PAUSED%s\n", when))
		b.WriteString(fmt.Sprintf("    Reason: %s\n", reason))
		b.WriteString(fmt.Sprintf("    Action: %s\n", question))
	} else {
		b.WriteString("  Status: Running\n")
	}

	return b.String()
}

func formatWorkflowPhases(phases []orchestrator.TaskPhase) string {
	if len(phases) == 0 {
		return "No task phases configured."
	}
	var b strings.Builder
	b.WriteString("Task Phases:\n")
	for _, phase := range phases {
		b.WriteString(fmt.Sprintf("- %s (%s)\n", phase.Title(), phase.Stage))
		desc := strings.TrimSpace(phase.Description)
		if desc != "" {
			b.WriteString(fmt.Sprintf("    • %s\n", desc))
		}
		if len(phase.Targets) > 0 {
			for _, target := range phase.Targets {
				b.WriteString(fmt.Sprintf("    → %s\n", target))
			}
		}
	}
	return b.String()
}

func (r *Runner) resolveExecutionModel() string {
	if r == nil {
		return ""
	}
	return strings.TrimSpace(model.ResolvePhaseModel(r.config, r.modelManager, r.rulesEngine, "execution", r.modelOverride))
}

type toolRiskAssessment struct {
	Level   string
	Score   int
	Reasons []string
}

func (r *Runner) assessToolRisk(toolName string, args map[string]any) toolRiskAssessment {
	assessment := toolRiskAssessment{
		Level: "none",
		Score: 0,
	}

	if r != nil && r.riskDetector != nil {
		switch strings.ToLower(strings.TrimSpace(toolName)) {
		case "run_shell":
			command := extractCommandArg(args)
			result := r.riskDetector.Analyze(command)
			assessment.Level = strings.ToLower(result.Level.String())
			assessment.Score = riskLevelScore(assessment.Level)
			assessment.Reasons = append(assessment.Reasons, result.Reasons...)
			if len(assessment.Reasons) == 0 && command != "" {
				assessment.Reasons = append(assessment.Reasons, "shell command: "+command)
			}
			return assessment
		case "run_code":
			// Inspect the raw code argument, not the base64-wrapped shell
			// command run_code builds around it.
			code := extractCodeArg(args)
			result := r.riskDetector.Analyze(code)
			assessment.Level = strings.ToLower(result.Level.String())
			assessment.Score = riskLevelScore(assessment.Level)
			assessment.Reasons = append(assessment.Reasons, result.Reasons...)
			if len(assessment.Reasons) == 0 && code != "" {
				assessment.Reasons = append(assessment.Reasons, "code snippet: "+truncateOutput(code, 200))
			}
			return assessment
		}
	}

	if r != nil && r.tools != nil {
		if t, ok := r.tools.Get(toolName); ok {
			switch tool.RequiredTierForTool(t) {
			case types.TierShellExec, types.TierFullAccess:
				assessment.Level = "high"
				assessment.Score = 75
				assessment.Reasons = []string{"shell-level tool"}
			case types.TierWorkspaceWrite:
				assessment.Level = "low"
				assessment.Score = 25
				assessment.Reasons = []string{"workspace modification"}
			default:
				assessment.Level = "none"
				assessment.Score = 0
				assessment.Reasons = []string{"read-only tool"}
			}
		}
	}

	if len(assessment.Reasons) == 0 {
		assessment.Reasons = []string{"default tool risk"}
	}
	return assessment
}

func (r *Runner) approvalMode() string {
	if r == nil || r.config == nil {
		return "ask"
	}

	mode := strings.ToLower(strings.TrimSpace(r.config.Approval.Mode))
	switch mode {
	case "safe", "auto", "yolo", "ask":
		return mode
	case "readonly":
		return "safe"
	case "automatic":
		return "auto"
	case "full", "dangerous":
		return "yolo"
	}

	switch strings.ToLower(strings.TrimSpace(r.config.Orchestrator.TrustLevel)) {
	case "autonomous":
		return "yolo"
	case "balanced":
		return "auto"
	case "conservative":
		return "safe"
	default:
		return "ask"
	}
}

func buildHeadlessSystemPrompt(basePrompt string, agentProfile string, projectCtx *projectcontext.ProjectContext, sess *storage.Session, evaluator types.RuleEvaluator, knowledgeContext string) string {
	projectRaw := ""
	if projectCtx != nil {
		projectRaw = projectCtx.RawContent
	}

	workDir := ""
	rootDir := ""
	if sess != nil {
		workDir = strings.TrimSpace(sess.ProjectPath)
		rootDir = workDir
	}

	return prompts.BuildRuntimeSystemPrompt(prompts.RuntimePromptInput{
		Evaluator:        evaluator,
		BasePrompt:       defaultIfEmpty(basePrompt, prompts.DefaultToolUseSystemPrompt),
		AgentProfile:     agentProfile,
		ProjectContext:   projectRaw,
		KnowledgeContext: knowledgeContext,
		WorkDir:          workDir,
		RootDir:          rootDir,
		TaskType:         "coding",
		ModelTier:        model.InferModelTier(""),
		GTSAvailable:     binaryAvailable("gts"),
	})
}

func headlessHyphaeProjectKnowledgeContext(cfg *config.Config, sess *storage.Session) string {
	if cfg == nil || !cfg.Memory.HyphaeRecall || sess == nil {
		return ""
	}
	return knowledgehyphae.ProjectKnowledgeContext(context.Background(), sess.ProjectPath, cfg.Memory.HyphaeSpace)
}

// buildHeadlessPermissionGate assembles the layered glob-permission
// configuration (pkg/policy) for a headless session: the active posture's
// rule layer (highest priority), then project rules, then user rules, with
// built-in defaults always consulted last (see
// policy.EvaluatePermissionLayersWithBuiltins). The workspace root is the
// session's project path, used to decide whether a shell command or file
// path resolves inside the workspace.
func buildHeadlessPermissionGate(sessionCfg *config.Config, posture string, sess *storage.Session, evaluator types.RuleEvaluator, sink *policy.ParkedDecisionLog) *tool.PermissionGate {
	workspaceRoot := ""
	if sess != nil {
		workspaceRoot = strings.TrimSpace(sess.ProjectPath)
	}

	var postureCfg config.PostureConfig
	var permissions config.PermissionsConfig
	if sessionCfg != nil {
		postureCfg = sessionCfg.Postures.Layers[posture]
		permissions = sessionCfg.Permissions
	}

	layers := []policy.PermissionLayer{
		{Name: "posture:" + posture, Rules: postureCfg.Rules},
		{Name: "project", Rules: permissions.Project},
		{Name: "user", Rules: permissions.User},
	}

	return &tool.PermissionGate{
		Layers:           layers,
		WorkspaceRoot:    workspaceRoot,
		Posture:          posture,
		ParkAskDecisions: postureCfg.ParkAskDecisions,
		Evaluator:        evaluator,
		ParkedSink:       sink,
	}
}

// Posture returns the glob-permission posture active for this session.
func (r *Runner) Posture() string {
	if r == nil {
		return ""
	}
	return r.posture
}

// ParkedDecisions returns every "ask" decision the active posture parked
// instead of blocking on human approval (see policy.ParkedDecision). It is
// non-empty only under postures that set park_ask_decisions, e.g.
// "unattended".
func (r *Runner) ParkedDecisions() []policy.ParkedDecision {
	if r == nil || r.parkedDecisions == nil {
		return nil
	}
	return r.parkedDecisions.List()
}

func loadRunnerProjectContext(sess *storage.Session) *projectcontext.ProjectContext {
	if sess == nil {
		return nil
	}
	root := strings.TrimSpace(sess.ProjectPath)
	if root == "" {
		return nil
	}
	ctx, err := projectcontext.NewLoader(root).Load()
	if err != nil {
		return nil
	}
	return ctx
}

func extractCommandArg(args map[string]any) string {
	if args == nil {
		return ""
	}
	for _, key := range []string{"command", "cmd"} {
		if value, ok := args[key].(string); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func extractCodeArg(args map[string]any) string {
	if args == nil {
		return ""
	}
	if value, ok := args["code"].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func riskLevelScore(level string) int {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "low":
		return 25
	case "medium":
		return 50
	case "high":
		return 75
	case "critical":
		return 100
	default:
		return 0
	}
}

func binaryAvailable(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func defaultIfEmpty(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func truncateOutput(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// getMessageContent extracts string content from a message content field.
func getMessageContent(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []model.ContentPart:
		var texts []string
		for _, part := range v {
			if part.Type == "text" && part.Text != "" {
				texts = append(texts, part.Text)
			}
		}
		return strings.Join(texts, "\n")
	default:
		return fmt.Sprintf("%v", v)
	}
}
