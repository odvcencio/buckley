package toolrunner

import (
	"context"
	"time"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tool"
)

// ModelClient defines the interface for LLM interactions used by the runner.
type ModelClient = model.ExecutionClient

// Config configures the tool runner behavior.
type Config struct {
	Models               ModelClient
	Registry             *tool.Registry
	DefaultMaxIterations int
	MaxToolsPhase1       int
	EnableReasoning      bool
	EnableParallelTools  bool // Enable parallel execution of independent tools
	MaxParallelTools     int  // Max concurrent tool executions (default 5)
	ToolExecutor         ToolExecutor
	CacheSize            int           // Max cache entries (default 100)
	CacheTTL             time.Duration // Cache entry TTL (default 5 minutes)
	ModelTimeout         time.Duration // Timeout for model calls (default 2 minutes)
}

// Request contains inputs for a tool runner execution.
type Request struct {
	Messages        []model.Message
	SelectionPrompt string
	AllowedTools    []string
	MaxIterations   int
	Model           string
	// Reasoning, when set, is forwarded to the provider on every turn so
	// reasoning-capable models (e.g. Kimi K3, GLM) run at the requested effort.
	Reasoning *model.ReasoningConfig
}

// Result contains the output from tool runner execution.
type Result struct {
	Content      string
	Reasoning    string
	ToolCalls    []ToolCallRecord
	Usage        model.Usage
	Iterations   int
	FinishReason string
}

// ToolExecutionResult captures the outcome of a tool execution.
type ToolExecutionResult struct {
	Result  string
	Error   string
	Success bool
}

// ToolExecutor allows customizing tool execution behavior.
type ToolExecutor func(ctx context.Context, call model.ToolCall, args map[string]any, tools map[string]tool.Tool) (ToolExecutionResult, error)

// ToolCallRecord captures a single tool invocation.
type ToolCallRecord struct {
	ID        string
	Name      string
	Arguments string
	Result    string
	Error     string
	Success   bool
	Duration  int64 // milliseconds
}

// StreamHandler receives streaming events during execution.
type StreamHandler interface {
	OnText(text string)
	OnReasoning(reasoning string)
	OnReasoningEnd()
	OnToolStart(name string, arguments string)
	OnToolEnd(name string, result string, err error)
	OnError(err error)
	OnComplete(result *Result)
}

// TurnObserver is an optional interface a StreamHandler may also implement to
// observe each conversation message the loop produces — assistant messages
// (with any preamble content, reasoning, and tool calls), tool-result messages,
// and the final assistant answer — in order. Callers use it to persist a full,
// correctly-shaped turn into their own conversation store, since the runner
// keeps its wire history internally and only surfaces a summarized Result.
type TurnObserver interface {
	OnTurnMessage(msg model.Message)
}

// CacheStats tracks cache performance metrics.
type CacheStats struct {
	Hits      uint64
	Misses    uint64
	Evictions uint64
}

// HitRate returns the cache hit rate as a percentage (0-100).
func (s CacheStats) HitRate() float64 {
	total := s.Hits + s.Misses
	if total == 0 {
		return 0
	}
	return float64(s.Hits) * 100 / float64(total)
}
