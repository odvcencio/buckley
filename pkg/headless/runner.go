// Package headless provides API-driven conversation sessions without a TUI.
// These sessions implement the same command.Handler interface as the TUI,
// allowing web and mobile clients to drive conversations entirely via API.
package headless

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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
	"github.com/odvcencio/buckley/pkg/tool/builtin"
	"github.com/odvcencio/buckley/pkg/toolrunner"
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
	}

	go r.commandLoop()
	r.startMaxRuntimeTimer(cfg.MaxRuntime)

	return r, nil
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

func (r *Runner) runConversationLoop() error {
	ctx, cancel := context.WithCancel(context.Background())
	r.mu.Lock()
	r.cancelFunc = cancel
	r.mu.Unlock()
	defer cancel()

	if r.State() == StateStopped || r.State() == StatePaused {
		return nil
	}

	if r.tools == nil {
		return fmt.Errorf("tool registry required")
	}

	maxIterations := 50 // Prevent runaway loops
	modelID := r.executionModelID()
	runner, err := toolrunner.New(toolrunner.Config{
		Models:               &headlessModelClient{runner: r},
		Registry:             r.tools,
		DefaultMaxIterations: maxIterations,
		MaxToolsPhase1:       len(r.tools.List()),
		EnableReasoning:      true,
		ToolExecutor:         r.executeToolCall,
	})
	if err != nil {
		r.emitError("tool runner init failed", err)
		return err
	}

	stopWatcher := make(chan struct{})
	go r.watchRunState(ctx, cancel, stopWatcher)
	defer close(stopWatcher)

	result, err := runner.Run(ctx, toolrunner.Request{
		Messages:      buildHeadlessMessages(r, modelID),
		MaxIterations: maxIterations,
		Model:         modelID,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		var toolErr toolExecutionError
		if errors.As(err, &toolErr) {
			return err
		}
		r.emitError("model call failed", err)
		return err
	}

	if result == nil || strings.TrimSpace(result.Content) == "" {
		r.emit(RunnerEvent{
			Type:      EventWarning,
			SessionID: r.sessionID,
			Timestamp: time.Now(),
			Data:      map[string]any{"message": "Model returned no content and no tool calls - ending conversation"},
		})
		return nil
	}

	r.conv.AddAssistantMessageWithReasoning(result.Content, result.Reasoning)
	assistantMsg := r.conv.Messages[len(r.conv.Messages)-1]
	if err := r.conv.SaveMessage(r.store, assistantMsg); err != nil {
		r.emitError("failed to save assistant message", err)
	}

	return nil
}

func (r *Runner) executionModelID() string {
	modelID := r.config.Models.Execution
	if modelID == "" {
		modelID = r.config.Models.Planning
	}
	if r.modelOverride != "" {
		modelID = r.modelOverride
	}
	return modelID
}

func (r *Runner) watchRunState(ctx context.Context, cancel context.CancelFunc, stop <-chan struct{}) {
	if r == nil {
		return
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
			state := r.State()
			if state == StatePaused || state == StateStopped {
				cancel()
				return
			}
		}
	}
}

func (r *Runner) buildChatRequest() model.ChatRequest {
	return model.ChatRequest{
		Model:    r.executionModelID(),
		Messages: buildHeadlessMessages(r, r.executionModelID()),
		Tools:    r.tools.ToOpenAIFunctions(),
	}
}

func (r *Runner) callModel(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
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

	resp, err := r.modelManager.ChatCompletion(ctx, req)

	if r.telemetry != nil {
		duration := time.Since(startTime)
		eventType := telemetry.EventBuilderCompleted
		data := map[string]any{
			"model":       req.Model,
			"duration_ms": duration.Milliseconds(),
			"source":      "headless",
		}
		if err != nil {
			eventType = telemetry.EventBuilderFailed
			data["error"] = err.Error()
		} else if resp != nil {
			data["input_tokens"] = resp.Usage.PromptTokens
			data["output_tokens"] = resp.Usage.CompletionTokens
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

type headlessModelClient struct {
	runner *Runner
}

func (c *headlessModelClient) ChatCompletion(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	if c == nil || c.runner == nil {
		return nil, fmt.Errorf("runner not available")
	}

	resp, err := c.runner.callModel(ctx, req)
	if err != nil || resp == nil {
		return resp, err
	}
	if len(req.Tools) == 0 {
		return resp, err
	}
	if len(resp.Choices) == 0 {
		return resp, err
	}

	msg := resp.Choices[0].Message
	if len(msg.ToolCalls) > 0 {
		c.runner.conv.AddToolCallMessage(msg.ToolCalls)
	}

	return resp, err
}

func (c *headlessModelClient) GetExecutionModel() string {
	if c == nil || c.runner == nil || c.runner.modelManager == nil {
		return ""
	}
	return c.runner.modelManager.GetExecutionModel()
}

type toolExecutionError struct {
	err error
}

func (e toolExecutionError) Error() string {
	if e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e toolExecutionError) Unwrap() error {
	return e.err
}

func (r *Runner) executeToolCall(ctx context.Context, tc model.ToolCall, args map[string]any, _ map[string]tool.Tool) (toolrunner.ToolExecutionResult, error) {
	decision := "auto"
	if args == nil {
		args = map[string]any{}
	}

	r.emit(RunnerEvent{
		Type:      EventToolCallStarted,
		SessionID: r.sessionID,
		Timestamp: time.Now(),
		Data: map[string]any{
			"toolCallId": tc.ID,
			"toolName":   tc.Function.Name,
			"arguments":  tc.Function.Arguments,
		},
	})

	if strings.EqualFold(tc.Function.Name, "run_shell") {
		if interactive, ok := args["interactive"].(bool); ok && interactive {
			message := "Tool execution denied: interactive shell sessions are not supported in headless mode"
			decision = "rejected"
			r.conv.AddToolResponseMessage(tc.ID, tc.Function.Name, message)
			r.emit(RunnerEvent{
				Type:      EventToolCallComplete,
				SessionID: r.sessionID,
				Timestamp: time.Now(),
				Data: map[string]any{
					"toolCallId": tc.ID,
					"toolName":   tc.Function.Name,
					"success":    false,
					"error":      message,
				},
			})
			if r.store != nil {
				decidedBy := "system"
				riskScore := 0
				if approvalDecision, score := r.approvalAuditFields(tc.ID); approvalDecision != "" || score != 0 {
					if approvalDecision != "" {
						decidedBy = approvalDecision
					}
					riskScore = score
				}
				if logErr := r.store.LogToolExecution(&storage.ToolAuditEntry{
					SessionID:  r.sessionID,
					ApprovalID: tc.ID,
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
			return toolrunner.ToolExecutionResult{Result: message, Error: message, Success: false}, nil
		}
	}

	r.clampToolTimeoutArgs(tc.Function.Name, args)

	if r.requiresApproval(tc.Function.Name) {
		approved, err := r.waitForApproval(ctx, tc.ID, tc.Function.Name, args)
		if err != nil {
			return toolrunner.ToolExecutionResult{}, toolExecutionError{err: err}
		}
		if !approved {
			message := "Tool execution rejected by user"
			decision = "rejected"
			r.conv.AddToolResponseMessage(tc.ID, tc.Function.Name, message)
			r.emit(RunnerEvent{
				Type:      EventToolCallComplete,
				SessionID: r.sessionID,
				Timestamp: time.Now(),
				Data: map[string]any{
					"toolCallId": tc.ID,
					"toolName":   tc.Function.Name,
					"success":    false,
					"error":      message,
				},
			})
			if r.store != nil {
				decidedBy, riskScore := r.approvalAuditFields(tc.ID)
				if logErr := r.store.LogToolExecution(&storage.ToolAuditEntry{
					SessionID:  r.sessionID,
					ApprovalID: tc.ID,
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
			return toolrunner.ToolExecutionResult{Result: message, Error: message, Success: false}, nil
		}
		decision = "approved"
	}

	startTime := time.Now()
	result, err := r.tools.Execute(tc.Function.Name, args)
	duration := time.Since(startTime)

	decidedBy, riskScore := r.approvalAuditFields(tc.ID)
	auditEntry := &storage.ToolAuditEntry{
		SessionID:  r.sessionID,
		ApprovalID: tc.ID,
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

		r.conv.AddToolResponseMessage(tc.ID, tc.Function.Name, errorResult)
		r.emit(RunnerEvent{
			Type:      EventToolCallComplete,
			SessionID: r.sessionID,
			Timestamp: time.Now(),
			Data: map[string]any{
				"toolCallId": tc.ID,
				"toolName":   tc.Function.Name,
				"success":    false,
				"error":      err.Error(),
			},
		})

		if r.store != nil {
			if logErr := r.store.LogToolExecution(auditEntry); logErr != nil {
				r.emitError("failed to log tool execution", logErr)
			}
		}

		return toolrunner.ToolExecutionResult{
			Result:  errorResult,
			Error:   err.Error(),
			Success: false,
		}, nil
	}

	resultContent := r.formatToolResult(result)
	auditEntry.ToolOutput = truncateOutput(resultContent, 10000)

	r.conv.AddToolResponseMessage(tc.ID, tc.Function.Name, resultContent)

	r.emit(RunnerEvent{
		Type:      EventToolCallComplete,
		SessionID: r.sessionID,
		Timestamp: time.Now(),
		Data: map[string]any{
			"toolCallId": tc.ID,
			"toolName":   tc.Function.Name,
			"success":    result.Success,
			"output":     truncateOutput(resultContent, 1000),
		},
	})

	if r.store != nil {
		if logErr := r.store.LogToolExecution(auditEntry); logErr != nil {
			r.emitError("failed to log tool execution", logErr)
		}
	}

	return toolrunner.ToolExecutionResult{
		Result:  resultContent,
		Success: result.Success,
	}, nil
}

func (r *Runner) handleToolCalls(ctx context.Context, toolCalls []model.ToolCall) error {
	// Add the tool call message to conversation
	r.conv.AddToolCallMessage(toolCalls)

	for _, tc := range toolCalls {
		decision := "auto"

		r.emit(RunnerEvent{
			Type:      EventToolCallStarted,
			SessionID: r.sessionID,
			Timestamp: time.Now(),
			Data: map[string]any{
				"toolCallId": tc.ID,
				"toolName":   tc.Function.Name,
				"arguments":  tc.Function.Arguments,
			},
		})

		// Parse arguments
		var args map[string]any
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			args = map[string]any{"raw": tc.Function.Arguments}
		}
		if args != nil && tc.ID != "" {
			args[tool.ToolCallIDParam] = tc.ID
		}

		if strings.EqualFold(tc.Function.Name, "run_shell") {
			if interactive, ok := args["interactive"].(bool); ok && interactive {
				message := "Tool execution denied: interactive shell sessions are not supported in headless mode"
				decision = "rejected"
				r.conv.AddToolResponseMessage(tc.ID, tc.Function.Name, message)
				r.emit(RunnerEvent{
					Type:      EventToolCallComplete,
					SessionID: r.sessionID,
					Timestamp: time.Now(),
					Data: map[string]any{
						"toolCallId": tc.ID,
						"toolName":   tc.Function.Name,
						"success":    false,
						"error":      message,
					},
				})
				if r.store != nil {
					decidedBy := "system"
					riskScore := 0
					if approvalDecision, score := r.approvalAuditFields(tc.ID); approvalDecision != "" || score != 0 {
						if approvalDecision != "" {
							decidedBy = approvalDecision
						}
						riskScore = score
					}
					if logErr := r.store.LogToolExecution(&storage.ToolAuditEntry{
						SessionID:  r.sessionID,
						ApprovalID: tc.ID,
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
				continue
			}
		}
		r.clampToolTimeoutArgs(tc.Function.Name, args)

		// Check if tool requires approval
		if r.requiresApproval(tc.Function.Name) {
			approved, err := r.waitForApproval(ctx, tc.ID, tc.Function.Name, args)
			if err != nil {
				return err
			}
			if !approved {
				message := "Tool execution rejected by user"
				decision = "rejected"
				r.conv.AddToolResponseMessage(tc.ID, tc.Function.Name, message)
				r.emit(RunnerEvent{
					Type:      EventToolCallComplete,
					SessionID: r.sessionID,
					Timestamp: time.Now(),
					Data: map[string]any{
						"toolCallId": tc.ID,
						"toolName":   tc.Function.Name,
						"success":    false,
						"error":      message,
					},
				})
				if r.store != nil {
					decidedBy, riskScore := r.approvalAuditFields(tc.ID)
					if logErr := r.store.LogToolExecution(&storage.ToolAuditEntry{
						SessionID:  r.sessionID,
						ApprovalID: tc.ID,
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
				continue
			}
			decision = "approved"
		}

		// Execute tool with timing
		startTime := time.Now()
		result, err := r.tools.Execute(tc.Function.Name, args)
		duration := time.Since(startTime)

		// Log to audit trail
		decidedBy, riskScore := r.approvalAuditFields(tc.ID)
		auditEntry := &storage.ToolAuditEntry{
			SessionID:  r.sessionID,
			ApprovalID: tc.ID, // Use tool call ID as approval reference if approved
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

			r.conv.AddToolResponseMessage(tc.ID, tc.Function.Name, errorResult)
			r.emit(RunnerEvent{
				Type:      EventToolCallComplete,
				SessionID: r.sessionID,
				Timestamp: time.Now(),
				Data: map[string]any{
					"toolCallId": tc.ID,
					"toolName":   tc.Function.Name,
					"success":    false,
					"error":      err.Error(),
				},
			})

			// Log failed execution
			if logErr := r.store.LogToolExecution(auditEntry); logErr != nil {
				r.emitError("failed to log tool execution", logErr)
			}
			continue
		}

		// Format result
		resultContent := r.formatToolResult(result)
		auditEntry.ToolOutput = truncateOutput(resultContent, 10000)

		r.conv.AddToolResponseMessage(tc.ID, tc.Function.Name, resultContent)

		r.emit(RunnerEvent{
			Type:      EventToolCallComplete,
			SessionID: r.sessionID,
			Timestamp: time.Now(),
			Data: map[string]any{
				"toolCallId": tc.ID,
				"toolName":   tc.Function.Name,
				"success":    result.Success,
				"output":     truncateOutput(resultContent, 1000),
			},
		})

		// Log successful execution
		if logErr := r.store.LogToolExecution(auditEntry); logErr != nil {
			r.emitError("failed to log tool execution", logErr)
		}
	}

	return nil
}

func (r *Runner) approvalAuditFields(approvalID string) (string, int) {
	if r == nil || r.store == nil || strings.TrimSpace(approvalID) == "" {
		return "", 0
	}
	approval, err := r.store.GetPendingApproval(approvalID)
	if err != nil || approval == nil {
		return "", 0
	}
	return approval.DecidedBy, approval.RiskScore
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

func (r *Runner) requiresApproval(toolName string) bool {
	toolName = strings.TrimSpace(strings.ToLower(toolName))
	if toolName != "" && len(r.requiredApprovalTools) > 0 {
		if _, ok := r.requiredApprovalTools[toolName]; ok {
			return true
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
	case "run_shell", "run_tests":
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

func (r *Runner) waitForApproval(ctx context.Context, toolCallID, toolName string, args map[string]any) (bool, error) {
	// Evaluate policy for risk score
	var riskScore int
	var riskReasons []string
	if r.policyEngine != nil {
		result := r.evaluatePolicy(toolName, args)
		riskScore = result.RiskScore
		riskReasons = result.RiskReasons
	}

	expiresAt := time.Now().Add(5 * time.Minute)

	approval := &PendingApproval{
		ID:        toolCallID,
		ToolName:  toolName,
		ToolArgs:  args,
		CreatedAt: time.Now(),
		ExpiresAt: expiresAt,
	}

	// Persist to storage
	toolInputJSON, _ := json.Marshal(args)
	storedApproval := &storage.PendingApproval{
		ID:          toolCallID,
		SessionID:   r.sessionID,
		ToolName:    toolName,
		ToolInput:   string(toolInputJSON),
		RiskScore:   riskScore,
		RiskReasons: riskReasons,
		Status:      "pending",
		ExpiresAt:   expiresAt,
		CreatedAt:   time.Now(),
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
			"id":          toolCallID,
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
		r.updateApprovalStatus(toolCallID, "expired", "", "")
		return false, ctx.Err()
	case resp := <-r.approvalChan:
		if resp.ID == toolCallID {
			status := "rejected"
			if resp.Approved {
				status = "approved"
			}
			r.updateApprovalStatus(toolCallID, status, "headless-runner", resp.Reason)
			return resp.Approved, nil
		}
		return false, fmt.Errorf("approval ID mismatch")
	case <-time.After(5 * time.Minute):
		r.updateApprovalStatus(toolCallID, "expired", "", "timeout")
		return false, fmt.Errorf("approval timeout")
	}
}

// updateApprovalStatus updates the approval status in storage.
func (r *Runner) updateApprovalStatus(id, status, decidedBy, reason string) {
	approval, err := r.store.GetPendingApproval(id)
	if err != nil || approval == nil {
		return
	}

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
	if r.State() != StatePaused {
		return fmt.Errorf("session not paused")
	}
	r.setState(StateIdle)
	return nil
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
	r.emit(RunnerEvent{
		Type:      EventError,
		SessionID: r.sessionID,
		Timestamp: time.Now(),
		Data: map[string]any{
			"message": msg,
			"error":   err.Error(),
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
	if err := r.conv.SaveMessage(r.store, msg); err != nil {
		r.emitError("failed to save system message", err)
		return err
	}
	return nil
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

	orch := orchestrator.NewOrchestrator(r.store, r.modelManager, r.tools, cfg, wf, nil)

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
		r.setState(StateIdle)
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
