package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/bus"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/tool"
	"github.com/odvcencio/buckley/pkg/toolrunner"
)

// TaskResult represents the outcome of a task execution.
type TaskResult struct {
	TaskID     string        `json:"task_id"`
	AgentID    string        `json:"agent_id"`
	Success    bool          `json:"success"`
	Output     string        `json:"output"`
	Error      string        `json:"error,omitempty"`
	Artifacts  []Artifact    `json:"artifacts,omitempty"`
	Duration   time.Duration `json:"duration"`
	TokensUsed int           `json:"tokens_used"`
	ToolCalls  []ToolCall    `json:"tool_calls,omitempty"`
}

// Artifact represents a work product from task execution.
type Artifact struct {
	Type    string `json:"type"`    // file, pr, commit, etc.
	Path    string `json:"path"`    // File path or URL
	Content string `json:"content"` // Content or description
}

// ToolCall records a tool invocation during execution.
type ToolCall struct {
	Name      string        `json:"name"`
	Arguments string        `json:"arguments"`
	Result    string        `json:"result"`
	Duration  time.Duration `json:"duration"`
	Success   bool          `json:"success"`
}

// TaskExecutor executes a single task using an agent.
type TaskExecutor struct {
	bus    bus.MessageBus
	models *model.Manager
	tools  *tool.Registry
	config ExecutorConfig
}

// ExecutorConfig configures task execution behavior.
type ExecutorConfig struct {
	// MaxIterations limits LLM call loops (prevents runaway)
	MaxIterations int

	// ToolTimeout is the max time for a single tool execution
	ToolTimeout time.Duration

	// TotalTimeout is the max time for entire task
	TotalTimeout time.Duration
}

// DefaultExecutorConfig returns sensible defaults.
func DefaultExecutorConfig() ExecutorConfig {
	return ExecutorConfig{
		MaxIterations: 50,
		ToolTimeout:   5 * time.Minute,
		TotalTimeout:  30 * time.Minute,
	}
}

// NewTaskExecutor creates a new executor.
func NewTaskExecutor(b bus.MessageBus, models *model.Manager, tools *tool.Registry, cfg ExecutorConfig) *TaskExecutor {
	if cfg.MaxIterations == 0 {
		cfg = DefaultExecutorConfig()
	}

	return &TaskExecutor{
		bus:    b,
		models: models,
		tools:  tools,
		config: cfg,
	}
}

// Execute runs a task to completion.
func (e *TaskExecutor) Execute(ctx context.Context, taskID string, role Role, task string, cfg AgentConfig) (*TaskResult, error) {
	start := time.Now()

	// Create agent
	agent := NewAgent(taskID, role, e.bus, e.models, e.tools, cfg)

	// Set up result tracking
	result := &TaskResult{
		TaskID:  taskID,
		AgentID: agent.ID,
	}

	// Start agent (subscribes to messages)
	if err := agent.Start(ctx); err != nil {
		result.Error = err.Error()
		return result, err
	}
	defer agent.Cancel()

	// Create timeout context
	execCtx, cancel := context.WithTimeout(ctx, e.config.TotalTimeout)
	defer cancel()

	messages := buildExecutorMessages(task, cfg.SystemPrompt)

	registry := e.tools
	if registry == nil {
		registry = tool.NewEmptyRegistry()
	}

	maxToolsPhase1 := len(cfg.AllowedTools)
	if maxToolsPhase1 == 0 {
		maxToolsPhase1 = len(registry.List())
	}

	iteration := 0
	runner, err := toolrunner.New(toolrunner.Config{
		Models:               &executorModelClient{models: e.models, iteration: &iteration},
		Registry:             registry,
		DefaultMaxIterations: e.config.MaxIterations,
		MaxToolsPhase1:       maxToolsPhase1,
	})
	if err != nil {
		result.Error = err.Error()
		result.Duration = time.Since(start)
		return result, err
	}

	runner.SetStreamHandler(&executorStreamHandler{
		agent:     agent,
		ctx:       execCtx,
		iteration: &iteration,
	})

	runResult, err := runner.Run(execCtx, toolrunner.Request{
		Messages:      messages,
		AllowedTools:  cfg.AllowedTools,
		MaxIterations: e.config.MaxIterations,
		Model:         cfg.Model,
	})

	result.Duration = time.Since(start)
	if runResult != nil {
		result.TokensUsed = runResult.Usage.TotalTokens
		result.ToolCalls = toTaskToolCalls(runResult.ToolCalls)
	}

	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			result.Error = "execution timeout"
		} else {
			result.Error = err.Error()
		}
		return result, err
	}

	if maxIterationsReached(runResult, e.config.MaxIterations) {
		result.Error = "max iterations reached"
		return result, fmt.Errorf("max iterations")
	}

	if runResult != nil {
		result.Success = true
		result.Output = runResult.Content
	}

	// Resolve agent
	agent.Resolve(execCtx, result)

	return result, nil
}

// ExecuteSimple is a convenience method for simple task execution.
func (e *TaskExecutor) ExecuteSimple(ctx context.Context, task string) (*TaskResult, error) {
	taskID := fmt.Sprintf("simple-%d", time.Now().UnixNano())
	return e.Execute(ctx, taskID, RoleExecutor, task, DefaultAgentConfig())
}

type executorModelClient struct {
	models    *model.Manager
	iteration *int
}

func (c *executorModelClient) ChatCompletion(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	if req.Temperature == 0 {
		req.Temperature = 0.3
	}
	if c.iteration != nil && (req.ToolChoice == "auto" || len(req.Tools) > 0) {
		*c.iteration++
	}
	return c.models.ChatCompletion(ctx, req)
}

func (c *executorModelClient) GetExecutionModel() string {
	if c == nil || c.models == nil {
		return ""
	}
	return c.models.GetExecutionModel()
}

func (c *executorModelClient) ChatCompletionStream(ctx context.Context, req model.ChatRequest) (<-chan model.StreamChunk, <-chan error) {
	if c == nil || c.models == nil {
		errChan := make(chan error, 1)
		errChan <- fmt.Errorf("models not available")
		close(errChan)
		return nil, errChan
	}
	return c.models.ChatCompletionStream(ctx, req)
}

type executorStreamHandler struct {
	agent     *Agent
	ctx       context.Context
	iteration *int
	lastIter  int
	toolCalls int
}

func (h *executorStreamHandler) OnText(text string) {}

func (h *executorStreamHandler) OnReasoning(reasoning string) {}

func (h *executorStreamHandler) OnReasoningEnd() {}

func (h *executorStreamHandler) OnToolStart(name string, arguments string) {}

func (h *executorStreamHandler) OnToolEnd(name string, result string, err error) {
	if h == nil || h.agent == nil || h.ctx == nil {
		return
	}

	currentIter := 0
	if h.iteration != nil {
		currentIter = *h.iteration
	}
	if currentIter != h.lastIter {
		h.lastIter = currentIter
		h.toolCalls = 0
	}
	h.toolCalls++

	_ = h.agent.PublishTaskEvent(h.ctx, "progress", map[string]any{
		"iteration":  currentIter,
		"tool_calls": h.toolCalls,
	})
}

func (h *executorStreamHandler) OnError(err error) {
	if h == nil || h.agent == nil || h.ctx == nil || err == nil {
		return
	}
	_ = h.agent.PublishTaskEvent(h.ctx, "error", map[string]any{
		"error": err.Error(),
	})
}

func (h *executorStreamHandler) OnComplete(result *toolrunner.Result) {}

func buildExecutorMessages(task string, systemPrompt string) []model.Message {
	messages := []model.Message{
		{Role: "user", Content: task},
	}
	if strings.TrimSpace(systemPrompt) == "" {
		return messages
	}
	return append([]model.Message{{Role: "system", Content: systemPrompt}}, messages...)
}

func maxIterationsReached(result *toolrunner.Result, maxIterations int) bool {
	if result == nil || maxIterations <= 0 {
		return false
	}
	if result.Iterations < maxIterations {
		return false
	}
	return strings.TrimSpace(result.Content) == toolrunnerMaxIterationsMessage
}

func toTaskToolCalls(records []toolrunner.ToolCallRecord) []ToolCall {
	if len(records) == 0 {
		return nil
	}
	calls := make([]ToolCall, 0, len(records))
	for _, record := range records {
		calls = append(calls, ToolCall{
			Name:      record.Name,
			Arguments: record.Arguments,
			Result:    record.Result,
			Duration:  time.Duration(record.Duration) * time.Millisecond,
			Success:   record.Success,
		})
	}
	return calls
}

const toolrunnerMaxIterationsMessage = "Maximum iterations reached. Please try a simpler request."
