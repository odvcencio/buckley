// Package execution provides execution strategies for processing user requests.
//
// The package defines the ExecutionStrategy interface and provides multiple
// implementations:
//   - ClassicStrategy: Single-agent execution using the ToolRunner loop
//   - RLMStrategy: Coordinator pattern with sub-agents and filtered tools
package execution

import (
	"context"

	"github.com/odvcencio/buckley/pkg/conversation"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/tool"
	"github.com/odvcencio/buckley/pkg/toolrunner"
)

// ExecutionRequest contains all inputs needed for request execution.
type ExecutionRequest struct {
	// Prompt is the user's input message
	Prompt string

	// Conversation provides message history and context
	Conversation *conversation.Conversation

	// SessionID identifies the current session for storage/telemetry
	SessionID string

	// SystemPrompt overrides the default system prompt if non-empty
	SystemPrompt string

	// AllowedTools filters which tools can be used (nil = all tools)
	AllowedTools []string

	// MaxIterations limits tool call loops (0 = use default)
	MaxIterations int

	// Stream enables streaming responses when supported
	Stream bool
}

// ExecutionResult contains the output from request execution.
type ExecutionResult struct {
	// Content is the final text response
	Content string

	// Reasoning contains model reasoning (for thinking models like Kimi K2)
	Reasoning string

	// ToolCalls lists all tool calls made during execution
	ToolCalls []ToolCallRecord

	// Usage tracks token consumption
	Usage model.Usage

	// Artifacts contains references to generated files or data
	Artifacts []string

	// Confidence indicates result confidence (0.0-1.0, for RLM strategy)
	Confidence float64

	// Iterations counts how many tool loops were executed
	Iterations int

	// FinishReason records the model finish reason (e.g., "stop", "length")
	FinishReason string
}

// ToolCallRecord captures a single tool invocation.
type ToolCallRecord = toolrunner.ToolCallRecord

// ExecutionStrategy defines how user requests are processed.
//
// Implementations handle the tool call loop, model interaction, and result
// synthesis differently based on the strategy:
//
//   - ClassicStrategy: Uses ToolRunner for automatic tool selection and loops
//   - RLMStrategy: Coordinator delegates to sub-agents with filtered tools
type ExecutionStrategy interface {
	// Execute processes a request and returns the result.
	Execute(ctx context.Context, req ExecutionRequest) (*ExecutionResult, error)

	// Name returns the strategy identifier for logging/config.
	Name() string

	// SupportsStreaming indicates if the strategy supports streaming responses.
	SupportsStreaming() bool
}

// StrategyFactory creates execution strategies based on configuration.
type StrategyFactory interface {
	// Create returns a strategy for the given mode name.
	// Supported modes: "classic", "rlm", "auto"
	Create(mode string) (ExecutionStrategy, error)
}

// StreamHandler receives streaming events during execution.
type StreamHandler interface {
	// OnText is called when text content is generated.
	OnText(text string)

	// OnReasoning is called when reasoning content is generated (thinking models).
	OnReasoning(reasoning string)

	// OnToolStart is called when a tool execution begins.
	OnToolStart(name string, arguments string)

	// OnToolEnd is called when a tool execution completes.
	OnToolEnd(name string, result string, err error)

	// OnComplete is called when execution finishes.
	OnComplete(result *ExecutionResult)
}

// ModelClient defines the interface for LLM interactions used by strategies.
// This allows strategies to be tested with mock implementations.
type ModelClient interface {
	// ChatCompletion sends a chat request and returns the response.
	ChatCompletion(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error)

	// GetExecutionModel returns the model ID to use for execution.
	GetExecutionModel() string
}

// StrategyConfig provides configuration for strategy creation.
type StrategyConfig struct {
	// Models provides access to LLM clients
	Models ModelClient

	// Registry provides tool definitions and execution
	Registry *tool.Registry

	// DefaultMaxIterations sets the default tool loop limit
	DefaultMaxIterations int

	// ConfidenceThreshold for RLM strategy (default 0.7)
	ConfidenceThreshold float64

	// EnableReasoning extracts reasoning from thinking models
	EnableReasoning bool

	// UseTOON enables compact tool result encoding
	UseTOON bool
}
