// Package agent provides the agent runtime for multi-agent orchestration.
// Agents are task-scoped processes that communicate via MessageBus.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/odvcencio/buckley/pkg/bus"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/tool"
)

// State represents the lifecycle state of an agent.
type State string

const (
	StateStarting  State = "starting"  // Agent is initializing
	StateActive    State = "active"    // Agent is processing messages
	StateAwaiting  State = "awaiting"  // Agent is waiting for external input
	StateResolving State = "resolving" // Agent is proposing completion
	StateResolved  State = "resolved"  // Agent has completed and is terminating
)

// Role defines the type of work an agent performs.
type Role string

const (
	RoleResearcher Role = "researcher" // Gathers context and information
	RoleCoder      Role = "coder"      // Writes and modifies code
	RoleReviewer   Role = "reviewer"   // Reviews and validates work
	RolePlanner    Role = "planner"    // Creates plans and strategies
	RoleExecutor   Role = "executor"   // Executes tasks from queue
)

// Agent is a task-scoped process that communicates via MessageBus.
type Agent struct {
	ID     string
	Role   Role
	TaskID string
	State  State
	Config AgentConfig

	bus      bus.MessageBus
	models   *model.Manager
	tools    *tool.Registry
	sub      bus.Subscription
	handlers map[string]MessageHandlerFunc

	mu       sync.RWMutex
	messages []AgentMessage
	metadata map[string]string

	started   time.Time
	resolved  time.Time
	cancelled atomic.Bool
}

// AgentConfig holds configuration for creating an agent.
type AgentConfig struct {
	// Model to use for LLM calls (defaults to execution model)
	Model string

	// SystemPrompt is injected into all LLM calls
	SystemPrompt string

	// Tools allowed for this agent (empty = all)
	AllowedTools []string

	// Timeout for the entire agent lifecycle
	Timeout time.Duration

	// HeartbeatInterval for status updates
	HeartbeatInterval time.Duration
}

// DefaultAgentConfig returns sensible defaults.
func DefaultAgentConfig() AgentConfig {
	return AgentConfig{
		Timeout:           30 * time.Minute,
		HeartbeatInterval: 10 * time.Second,
	}
}

// AgentMessage is a message sent to/from an agent.
type AgentMessage struct {
	ID        string          `json:"id"`
	From      string          `json:"from"`     // Agent ID or "user" or "system"
	To        string          `json:"to"`       // Target agent ID
	Type      string          `json:"type"`     // Message type
	Content   json.RawMessage `json:"content"`  // Payload
	ReplyTo   string          `json:"reply_to"` // For request/reply
	Timestamp time.Time       `json:"timestamp"`
}

// MessageType constants for agent communication.
const (
	MsgTypeTask      = "task"      // New task assignment
	MsgTypeResult    = "result"    // Task completion result
	MsgTypeQuestion  = "question"  // Agent needs clarification
	MsgTypeAnswer    = "answer"    // Response to question
	MsgTypeHandoff   = "handoff"   // Transfer work to another agent
	MsgTypeStatus    = "status"    // Status update
	MsgTypeCancel    = "cancel"    // Cancel current work
	MsgTypeHeartbeat = "heartbeat" // Heartbeat/keepalive
)

// MessageHandlerFunc processes incoming messages.
type MessageHandlerFunc func(ctx context.Context, msg AgentMessage) error

// NewAgent creates a new agent instance.
func NewAgent(taskID string, role Role, b bus.MessageBus, models *model.Manager, tools *tool.Registry, cfg AgentConfig) *Agent {
	if cfg.Timeout == 0 {
		cfg = DefaultAgentConfig()
	}

	return &Agent{
		ID:       fmt.Sprintf("%s-%s-%s", role, taskID[:8], ulid.Make().String()[:8]),
		Role:     role,
		TaskID:   taskID,
		State:    StateStarting,
		Config:   cfg,
		bus:      b,
		models:   models,
		tools:    tools,
		handlers: make(map[string]MessageHandlerFunc),
		metadata: make(map[string]string),
		started:  time.Now(),
	}
}

// OnMessage registers a handler for a message type.
func (a *Agent) OnMessage(msgType string, handler MessageHandlerFunc) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.handlers[msgType] = handler
}

// Start begins the agent's message processing loop.
func (a *Agent) Start(ctx context.Context) error {
	subject := fmt.Sprintf("buckley.agent.%s.inbox", a.ID)

	sub, err := a.bus.Subscribe(ctx, subject, func(msg *bus.Message) []byte {
		var agentMsg AgentMessage
		if err := json.Unmarshal(msg.Data, &agentMsg); err != nil {
			return nil
		}

		a.mu.Lock()
		a.messages = append(a.messages, agentMsg)
		handler, ok := a.handlers[agentMsg.Type]
		a.mu.Unlock()

		if ok {
			if err := handler(ctx, agentMsg); err != nil {
				// Log error but don't crash
				a.publishStatus(ctx, "error", err.Error())
			}
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("subscribe failed: %w", err)
	}

	a.sub = sub
	a.setState(StateActive)

	// Start heartbeat
	go a.heartbeatLoop(ctx)

	// Publish that we're ready
	a.publishStatus(ctx, "started", "")

	return nil
}

// Run starts the agent and blocks until completion or cancellation.
func (a *Agent) Run(ctx context.Context) error {
	if err := a.Start(ctx); err != nil {
		return err
	}

	// Create timeout context
	timeoutCtx, cancel := context.WithTimeout(ctx, a.Config.Timeout)
	defer cancel()

	// Wait for resolution or cancellation
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-timeoutCtx.Done():
			a.Cancel()
			return timeoutCtx.Err()
		case <-ticker.C:
			if a.State == StateResolved || a.cancelled.Load() {
				return nil
			}
		}
	}
}

// SendTo sends a message to another agent.
func (a *Agent) SendTo(ctx context.Context, targetAgentID string, msgType string, content any) error {
	data, err := json.Marshal(content)
	if err != nil {
		return err
	}

	msg := AgentMessage{
		ID:        ulid.Make().String(),
		From:      a.ID,
		To:        targetAgentID,
		Type:      msgType,
		Content:   data,
		Timestamp: time.Now(),
	}

	msgData, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	subject := fmt.Sprintf("buckley.agent.%s.inbox", targetAgentID)
	return a.bus.Publish(ctx, subject, msgData)
}

// Request sends a message and waits for a reply.
func (a *Agent) Request(ctx context.Context, targetAgentID string, msgType string, content any, timeout time.Duration) (*AgentMessage, error) {
	data, err := json.Marshal(content)
	if err != nil {
		return nil, err
	}

	msg := AgentMessage{
		ID:        ulid.Make().String(),
		From:      a.ID,
		To:        targetAgentID,
		Type:      msgType,
		Content:   data,
		Timestamp: time.Now(),
	}

	msgData, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}

	subject := fmt.Sprintf("buckley.agent.%s.inbox", targetAgentID)
	replyData, err := a.bus.Request(ctx, subject, msgData, timeout)
	if err != nil {
		return nil, err
	}

	var reply AgentMessage
	if err := json.Unmarshal(replyData, &reply); err != nil {
		return nil, err
	}

	return &reply, nil
}

// PublishTaskEvent publishes an event to the task's event stream.
func (a *Agent) PublishTaskEvent(ctx context.Context, eventType string, data any) error {
	event := map[string]any{
		"agent_id":  a.ID,
		"task_id":   a.TaskID,
		"type":      eventType,
		"data":      data,
		"timestamp": time.Now(),
	}

	eventData, err := json.Marshal(event)
	if err != nil {
		return err
	}

	subject := fmt.Sprintf("buckley.task.%s.events", a.TaskID)
	return a.bus.Publish(ctx, subject, eventData)
}

// Resolve marks the agent as completing with a result.
func (a *Agent) Resolve(ctx context.Context, result any) error {
	a.setState(StateResolving)

	// Publish result
	if err := a.PublishTaskEvent(ctx, "resolved", result); err != nil {
		return err
	}

	a.setState(StateResolved)
	a.resolved = time.Now()
	a.publishStatus(ctx, "resolved", "")

	return nil
}

// Cancel stops the agent.
func (a *Agent) Cancel() {
	a.cancelled.Store(true)
	if a.sub != nil {
		a.sub.Unsubscribe()
	}
	a.setState(StateResolved)
}

// InboxSubject returns the NATS subject for this agent's inbox.
func (a *Agent) InboxSubject() string {
	return fmt.Sprintf("buckley.agent.%s.inbox", a.ID)
}

// SetMetadata sets agent metadata.
func (a *Agent) SetMetadata(key, value string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.metadata[key] = value
}

// GetMetadata gets agent metadata.
func (a *Agent) GetMetadata(key string) string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.metadata[key]
}

func (a *Agent) setState(state State) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.State = state
}

func (a *Agent) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(a.Config.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if a.cancelled.Load() || a.State == StateResolved {
				return
			}
			a.publishStatus(ctx, "heartbeat", "")
		}
	}
}

func (a *Agent) publishStatus(ctx context.Context, status, details string) {
	a.mu.RLock()
	state := a.State
	a.mu.RUnlock()

	statusMsg := map[string]any{
		"agent_id": a.ID,
		"task_id":  a.TaskID,
		"role":     a.Role,
		"state":    state,
		"status":   status,
		"details":  details,
		"uptime":   time.Since(a.started).String(),
	}

	data, _ := json.Marshal(statusMsg)
	subject := fmt.Sprintf("buckley.agent.%s.status", a.ID)
	a.bus.Publish(ctx, subject, data)
}

// Chat sends a message to the LLM and returns the response.
func (a *Agent) Chat(ctx context.Context, messages []model.Message) (*model.ChatResponse, error) {
	if a.models == nil {
		return nil, fmt.Errorf("model manager not available")
	}

	modelID := a.Config.Model
	if modelID == "" {
		modelID = a.models.GetExecutionModel()
	}

	// Prepend system prompt if configured
	if a.Config.SystemPrompt != "" {
		messages = append([]model.Message{
			{Role: "system", Content: a.Config.SystemPrompt},
		}, messages...)
	}

	// Build tool definitions if tools are available
	var toolDefs []map[string]any
	if a.tools != nil {
		for _, t := range a.filterTools().List() {
			toolDefs = append(toolDefs, tool.ToOpenAIFunction(t))
		}
	}

	req := model.ChatRequest{
		Model:       modelID,
		Messages:    messages,
		Temperature: 0.3,
		Tools:       toolDefs,
		ToolChoice:  "auto",
	}

	return a.models.ChatCompletion(ctx, req)
}

func (a *Agent) filterTools() *tool.Registry {
	if len(a.Config.AllowedTools) == 0 {
		return a.tools
	}

	filtered := tool.NewEmptyRegistry()
	allowed := make(map[string]bool)
	for _, name := range a.Config.AllowedTools {
		allowed[name] = true
	}

	for _, t := range a.tools.List() {
		if allowed[t.Name()] {
			filtered.Register(t)
		}
	}

	return filtered
}
