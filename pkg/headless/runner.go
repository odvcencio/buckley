// Package headless provides API-driven conversation sessions without a TUI.
// These sessions implement the same command.Handler interface as the TUI,
// allowing web and mobile clients to drive conversations entirely via API.
package headless

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/conversation"
	"github.com/odvcencio/buckley/pkg/ipc/command"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/orchestrator"
	"github.com/odvcencio/buckley/pkg/policy"
	"github.com/odvcencio/buckley/pkg/push"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/tool"
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
	EventMessageCreated   = "message.created"
	EventMessageUpdated   = "message.updated"
	EventToolCallStarted  = "tool.started"
	EventToolCallComplete = "tool.completed"
	EventApprovalRequired = "approval.required"
	EventStateChanged     = "state.changed"
	EventError            = "error"
	EventWarning          = "warning"
)

// defaultHeadlessSystemPrompt provides core agent instructions for headless sessions.
// This ensures models understand how to use tools and continue working on tasks.
const defaultHeadlessSystemPrompt = `You are an AI development assistant with access to various tools.

CRITICAL BEHAVIOR:
- You MUST use tools to complete tasks, not just describe what you would do
- Continue calling tools until the task is fully complete
- Do not stop after one tool call if more work is needed
- After each tool result, evaluate if more actions are required

TOOL USAGE:
- Use search_text to find files and code locations
- Use read_file to examine file contents
- Use edit_file to make changes
- Use run_shell for commands, builds, and tests
- Chain multiple tool calls as needed

ANTI-PATTERNS TO AVOID:
- Do NOT respond with just text when tools are needed
- Do NOT stop after acknowledging a task without executing it
- Do NOT describe what you would do without actually doing it

Always take action with tools. If you're uncertain, use tools to investigate.`

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
	compactor     *conversation.CompactionManager

	workflow     *orchestrator.WorkflowManager
	orchestrator *orchestrator.Orchestrator

	// Policy and push notification support
	policyEngine *policy.Engine
	pushWorker   *push.Worker
	toolPolicy   *ToolPolicy

	requiredApprovalTools map[string]struct{}
	maxToolExecTime       time.Duration
	maxRuntime            time.Duration

	state       RunnerState
	lastActive  time.Time
	idleTimeout time.Duration
	cancelFunc  context.CancelFunc

	// Pending approval state
	pendingApproval *PendingApproval
	approvalChan    chan ApprovalResponse

	commandQueue   chan command.SessionCommand
	commandStop    chan struct{}
	commandStopped chan struct{}
	stopOnce       sync.Once
	ctx            context.Context
}

// PendingApproval represents a tool call awaiting user approval.
type PendingApproval struct {
	ID        string         `json:"id"`
	ToolName  string         `json:"toolName"`
	ToolArgs  map[string]any `json:"toolArgs"`
	CreatedAt time.Time      `json:"createdAt"`
	ExpiresAt time.Time      `json:"expiresAt"`
}

// ApprovalResponse carries the user's decision on a pending approval.
type ApprovalResponse struct {
	ID       string `json:"id"`
	Approved bool   `json:"approved"`
	Reason   string `json:"reason,omitempty"`
}

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
	Context       context.Context
}

// NewRunner creates a new headless session runner.
func NewRunner(cfg RunnerConfig) (*Runner, error) {
	if cfg.Session == nil {
		return nil, fmt.Errorf("session required")
	}
	if cfg.ModelManager == nil {
		return nil, fmt.Errorf("model manager required")
	}
	if cfg.Store == nil {
		return nil, fmt.Errorf("store required")
	}

	idleTimeout := cfg.IdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = 30 * time.Minute
	}

	conv := conversation.New(cfg.Session.ID)
	baseCtx := cfg.Context
	if baseCtx == nil {
		baseCtx = context.Background()
	}

	// Load existing conversation from storage
	if err := conv.LoadFromStorage(cfg.Store); err != nil {
		// Non-fatal - start fresh
		conv = conversation.New(cfg.Session.ID)
	}

	// Inject system prompt if this is a fresh conversation (no messages yet)
	if len(conv.Messages) == 0 {
		systemPrompt := cfg.SystemPrompt
		if systemPrompt == "" {
			systemPrompt = defaultHeadlessSystemPrompt
		}
		conv.AddSystemMessage(systemPrompt)
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
		policyEngine:          policyEngine,
		pushWorker:            cfg.PushWorker,
		toolPolicy:            cfg.ToolPolicy,
		requiredApprovalTools: requiredApprovalTools,
		maxToolExecTime:       maxToolExecTime,
		maxRuntime:            cfg.MaxRuntime,
		state:                 StateIdle,
		lastActive:            time.Now(),
		idleTimeout:           idleTimeout,
		approvalChan:          make(chan ApprovalResponse, 1),
		commandQueue:          make(chan command.SessionCommand, 64),
		commandStop:           make(chan struct{}),
		commandStopped:        make(chan struct{}),
		ctx:                   baseCtx,
	}

	compactor := conversation.NewCompactionManager(cfg.ModelManager, sessionCfg)
	compactor.SetConversation(conv)
	compactor.SetOnComplete(func(_ *conversation.CompactionResult) {
		if err := conv.SaveAllMessages(cfg.Store); err != nil {
			r.emit(RunnerEvent{
				Type:      EventWarning,
				SessionID: r.sessionID,
				Timestamp: time.Now(),
				Data:      map[string]any{"message": fmt.Sprintf("Failed to persist compaction: %v", err)},
			})
		}
	})
	r.compactor = compactor
	if r.tools != nil {
		r.tools.SetCompactionManager(compactor)
	}

	go r.commandLoop()
	r.startMaxRuntimeTimer(cfg.MaxRuntime)

	return r, nil
}

func (r *Runner) baseContext() context.Context {
	if r == nil {
		return context.Background()
	}
	r.mu.RLock()
	ctx := r.ctx
	r.mu.RUnlock()
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// SetContext updates the base context for headless execution.
func (r *Runner) SetContext(ctx context.Context) {
	if r == nil || ctx == nil {
		return
	}
	r.mu.Lock()
	r.ctx = ctx
	r.mu.Unlock()
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

// SetModelOverride updates the model override used for execution.
func (r *Runner) SetModelOverride(modelID string) {
	if r == nil {
		return
	}
	modelID = strings.TrimSpace(modelID)
	r.mu.Lock()
	r.modelOverride = modelID
	r.mu.Unlock()
}

// SetMode updates the execution mode for this runner.
func (r *Runner) SetMode(mode string) error {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return fmt.Errorf("mode required")
	}
	if mode != config.ExecutionModeClassic && mode != config.ExecutionModeRLM && mode != "auto" {
		return fmt.Errorf("invalid execution mode: %s", mode)
	}

	r.mu.Lock()
	if r.config == nil {
		r.config = config.DefaultConfig()
	}
	r.config.Execution.Mode = mode
	r.mu.Unlock()

	return nil
}

// LastActive returns the last activity timestamp.
func (r *Runner) LastActive() time.Time {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.lastActive
}

// PendingApproval returns any pending approval, or nil.
func (r *Runner) GetPendingApproval() *PendingApproval {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.pendingApproval
}

// HandleSessionCommand implements the command.Handler interface.
func (r *Runner) HandleSessionCommand(cmd command.SessionCommand) error {
	r.mu.Lock()
	r.lastActive = time.Now()
	stopped := r.state == StateStopped
	r.mu.Unlock()

	if stopped {
		return fmt.Errorf("session stopped")
	}

	if r.commandQueue == nil {
		return r.handleSessionCommand(cmd)
	}

	select {
	case r.commandQueue <- cmd:
		return nil
	default:
		return fmt.Errorf("command queue full")
	}
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
			if err := r.handleSessionCommand(cmd); err != nil {
				_ = r.persistSystemMessage(r.formatCommandError(err))
			}
		}
	}
}

func (r *Runner) handleSessionCommand(cmd command.SessionCommand) error {
	switch cmd.Type {
	case "input":
		return r.processUserInput(cmd.Content)
	case "slash":
		return r.processSlashCommand(cmd.Content)
	case "approval":
		return r.processApproval(cmd.Content)
	case "pause":
		return r.pause()
	case "resume":
		return r.resume()
	default:
		return fmt.Errorf("unknown command type: %s", cmd.Type)
	}
}

// Stop gracefully stops the runner.
func (r *Runner) Stop() {
	r.stopOnce.Do(func() {
		r.mu.Lock()
		r.state = StateStopped
		if r.cancelFunc != nil {
			r.cancelFunc()
		}
		r.mu.Unlock()

		close(r.commandStop)

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
			_ = r.persistSystemMessage(fmt.Sprintf("Session timed out after %s.", maxRuntime))
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
	if r.state == StatePaused {
		return false
	}
	return time.Since(r.lastActive) > r.idleTimeout
}
