package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/odvcencio/buckley/pkg/bus"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/tool"
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

	// Build initial messages
	messages := []model.Message{
		{Role: "user", Content: task},
	}

	// Execution loop
	for i := 0; i < e.config.MaxIterations; i++ {
		select {
		case <-execCtx.Done():
			result.Error = "execution timeout"
			result.Duration = time.Since(start)
			return result, execCtx.Err()
		default:
		}

		// Call LLM
		resp, err := agent.Chat(execCtx, messages)
		if err != nil {
			result.Error = fmt.Sprintf("chat error: %v", err)
			result.Duration = time.Since(start)
			return result, err
		}

		result.TokensUsed += resp.Usage.TotalTokens

		if len(resp.Choices) == 0 {
			result.Error = "no response from model"
			result.Duration = time.Since(start)
			return result, fmt.Errorf("no response")
		}

		choice := resp.Choices[0]

		// Check for tool calls
		if len(choice.Message.ToolCalls) > 0 {
			// Execute tools
			toolResults, err := e.executeTools(execCtx, choice.Message.ToolCalls, result)
			if err != nil {
				// Tool execution failed, but we can continue
				messages = append(messages, model.Message{
					Role:    "assistant",
					Content: choice.Message.Content,
				})
				messages = append(messages, model.Message{
					Role:    "user",
					Content: fmt.Sprintf("Tool execution error: %v. Please try a different approach.", err),
				})
				continue
			}

			// Add assistant message with tool calls
			messages = append(messages, model.Message{
				Role:      "assistant",
				Content:   choice.Message.Content,
				ToolCalls: choice.Message.ToolCalls,
			})

			// Add tool results
			for _, tr := range toolResults {
				messages = append(messages, model.Message{
					Role:       "tool",
					Content:    tr.Result,
					ToolCallID: tr.Name, // Simplified - real impl would use proper ID
				})
			}

			// Publish progress
			agent.PublishTaskEvent(execCtx, "progress", map[string]any{
				"iteration":  i + 1,
				"tool_calls": len(choice.Message.ToolCalls),
			})

			continue
		}

		// No tool calls - check finish reason
		if choice.FinishReason == "stop" {
			// Extract final response
			content, err := model.ExtractTextContent(choice.Message.Content)
			if err != nil {
				content = fmt.Sprintf("%v", choice.Message.Content)
			}

			result.Success = true
			result.Output = content
			result.Duration = time.Since(start)

			// Resolve agent
			agent.Resolve(execCtx, result)

			return result, nil
		}

		// Add response and continue
		messages = append(messages, model.Message{
			Role:    "assistant",
			Content: choice.Message.Content,
		})
	}

	result.Error = "max iterations reached"
	result.Duration = time.Since(start)
	return result, fmt.Errorf("max iterations")
}

func (e *TaskExecutor) executeTools(ctx context.Context, toolCalls []model.ToolCall, result *TaskResult) ([]ToolCall, error) {
	var results []ToolCall

	for _, tc := range toolCalls {
		_, cancel := context.WithTimeout(ctx, e.config.ToolTimeout)

		start := time.Now()
		toolResult := ToolCall{
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		}

		// Parse arguments
		var args map[string]any
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			toolResult.Result = fmt.Sprintf("invalid arguments: %v", err)
			toolResult.Success = false
		} else {
			if args != nil && tc.ID != "" {
				args[tool.ToolCallIDParam] = tc.ID
			}
			// Execute tool
			res, err := e.tools.Execute(tc.Function.Name, args)
			if err != nil {
				toolResult.Result = fmt.Sprintf("execution error: %v", err)
				toolResult.Success = false
			} else {
				// Format result
				if res != nil && res.Data != nil {
					resultData, _ := json.Marshal(res.Data)
					toolResult.Result = string(resultData)
				} else {
					toolResult.Result = "success"
				}
				toolResult.Success = true
			}
		}

		toolResult.Duration = time.Since(start)
		results = append(results, toolResult)
		result.ToolCalls = append(result.ToolCalls, toolResult)

		cancel()
	}

	return results, nil
}

// ExecuteSimple is a convenience method for simple task execution.
func (e *TaskExecutor) ExecuteSimple(ctx context.Context, task string) (*TaskResult, error) {
	taskID := fmt.Sprintf("simple-%d", time.Now().UnixNano())
	return e.Execute(ctx, taskID, RoleExecutor, task, DefaultAgentConfig())
}
