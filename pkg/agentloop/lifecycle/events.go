package lifecycle

import (
	"strings"
	"time"
)

// EventType is the provider-neutral live event vocabulary exposed by the
// shared agent-loop controller.
type EventType string

const (
	TurnStart     EventType = "turn/start"
	AttemptStart  EventType = "turn/attempt_start"
	AttemptEnd    EventType = "turn/attempt_end"
	StepStart     EventType = "step/start"
	ModelRequest  EventType = "model/request"
	ModelResponse EventType = "model/response"
	ModelError    EventType = "model/error"
	ToolCall      EventType = "tool/call"
	ToolStart     EventType = "tool/start"
	ToolResult    EventType = "tool/result"
	ToolError     EventType = "tool/error"
	TurnStopping  EventType = "turn/stopping"
	TurnEnd       EventType = "turn/end"
)

// Event is a bounded, content-free projection of one controller transition.
// It deliberately excludes prompts, tool arguments, and model output. Exact
// bodies remain in the evidence store/runledger when those are enabled.
type Event struct {
	Sequence  uint64    `json:"sequence"`
	Type      EventType `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	RunID     string    `json:"run_id,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	TaskID    string    `json:"task_id,omitempty"`
	TurnID    string    `json:"turn_id,omitempty"`
	Round     int       `json:"round,omitempty"`
	Attempt   int       `json:"attempt,omitempty"`
	// RunAttempt identifies one Controller.Run call within the stable logical
	// TurnID. Continuation is true from the second Run attempt onward.
	RunAttempt   int      `json:"run_attempt,omitempty"`
	Continuation bool     `json:"continuation,omitempty"`
	StepID       string   `json:"step_id,omitempty"`
	Phase        string   `json:"phase,omitempty"`
	ModelID      string   `json:"model_id,omitempty"`
	ProviderID   string   `json:"provider_id,omitempty"`
	ToolName     string   `json:"tool_name,omitempty"`
	ToolCallID   string   `json:"tool_call_id,omitempty"`
	Replayed     bool     `json:"replayed,omitempty"`
	Success      *bool    `json:"success,omitempty"`
	Usage        Usage    `json:"usage,omitempty"`
	CostUSD      float64  `json:"cost_usd,omitempty"`
	Status       string   `json:"status,omitempty"`
	FinishReason string   `json:"finish_reason,omitempty"`
	StopReason   string   `json:"stop_reason,omitempty"`
	Error        string   `json:"error,omitempty"`
	Stderr       string   `json:"stderr,omitempty"`
	EvidenceIDs  []string `json:"evidence_ids,omitempty"`
}

// Usage is intentionally defined without importing pkg/model: model already
// depends on telemetry, so this neutral contract must stay below both of them.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens,omitempty"`
}

// Observer receives best-effort immutable metadata events. Observer failures
// and panics are isolated from the turn; a live UI must never be able to
// change model/tool semantics or make a durable turn fail.
type Observer func(Event)

func TypeForLedger(eventType string) EventType {
	switch eventType {
	case "model.request_planned":
		return StepStart
	case "model.request_started":
		return ModelRequest
	case "model.request_completed", "model.request_replayed":
		return ModelResponse
	case "model.request_failed":
		return ModelError
	case "tool.requested":
		return ToolCall
	case "tool.started":
		return ToolStart
	case "tool.completed", "tool.replayed":
		return ToolResult
	case "tool.failed":
		return ToolError
	case "controller.decision":
		return TurnStopping
	default:
		return EventType(strings.TrimSpace(eventType))
	}
}
