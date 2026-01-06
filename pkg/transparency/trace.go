package transparency

import (
	"encoding/json"
	"time"

	"github.com/odvcencio/buckley/pkg/tools"
)

// Trace captures everything about an LLM invocation.
// This is the core of radical transparency - nothing is hidden.
type Trace struct {
	// ID uniquely identifies this invocation
	ID string `json:"id"`

	// Timestamp when the invocation started
	Timestamp time.Time `json:"timestamp"`

	// Model used for this invocation
	Model string `json:"model"`

	// Provider (e.g., "openrouter", "anthropic")
	Provider string `json:"provider"`

	// Duration of the request
	Duration time.Duration `json:"duration"`

	// Context audit showing what was sent
	Context *ContextAudit `json:"context,omitempty"`

	// Tokens consumed
	Tokens TokenUsage `json:"tokens"`

	// Cost in USD
	Cost float64 `json:"cost"`

	// Request contains the raw request (for --trace mode)
	Request *RequestTrace `json:"request,omitempty"`

	// Response contains the raw response (for --trace mode)
	Response *ResponseTrace `json:"response,omitempty"`

	// ToolCalls made by the model
	ToolCalls []tools.ToolCall `json:"tool_calls,omitempty"`

	// Reasoning content from the model (for thinking models)
	Reasoning string `json:"reasoning,omitempty"`

	// Content is the text content (if any)
	Content string `json:"content,omitempty"`

	// Error if the invocation failed
	Error string `json:"error,omitempty"`
}

// RequestTrace captures request details for debugging.
type RequestTrace struct {
	// Messages sent to the model
	Messages []MessageTrace `json:"messages"`

	// Tools provided to the model
	Tools []string `json:"tools,omitempty"`

	// Temperature setting
	Temperature float64 `json:"temperature"`

	// MaxTokens limit
	MaxTokens int `json:"max_tokens,omitempty"`
}

// MessageTrace is a simplified message for tracing.
type MessageTrace struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// Truncated length for display
	ContentLength int `json:"content_length"`
}

// ResponseTrace captures response details for debugging.
type ResponseTrace struct {
	// Raw response body (may be large)
	Raw json.RawMessage `json:"raw,omitempty"`

	// FinishReason from the model
	FinishReason string `json:"finish_reason"`

	// StopReason for Anthropic-style responses
	StopReason string `json:"stop_reason,omitempty"`
}

// HasToolCalls returns true if the model made tool calls.
func (t *Trace) HasToolCalls() bool {
	return len(t.ToolCalls) > 0
}

// FirstToolCall returns the first tool call, if any.
func (t *Trace) FirstToolCall() (tools.ToolCall, bool) {
	if len(t.ToolCalls) == 0 {
		return tools.ToolCall{}, false
	}
	return t.ToolCalls[0], true
}

// UnmarshalToolCall unmarshals the first tool call into the given type.
func (t *Trace) UnmarshalToolCall(v any) error {
	tc, ok := t.FirstToolCall()
	if !ok {
		return &NoToolCallError{Expected: "any"}
	}
	return tc.Unmarshal(v)
}

// NoToolCallError indicates the model didn't make an expected tool call.
type NoToolCallError struct {
	Expected string
	Got      string
}

func (e *NoToolCallError) Error() string {
	if e.Got != "" {
		return "model returned " + e.Got + " instead of tool call " + e.Expected
	}
	return "model did not call expected tool: " + e.Expected
}

// TraceBuilder constructs a trace incrementally.
type TraceBuilder struct {
	trace Trace
	start time.Time
}

// NewTraceBuilder starts building a new trace.
func NewTraceBuilder(id, model, provider string) *TraceBuilder {
	return &TraceBuilder{
		trace: Trace{
			ID:        id,
			Model:     model,
			Provider:  provider,
			Timestamp: time.Now(),
		},
		start: time.Now(),
	}
}

// WithContext attaches context audit information.
func (tb *TraceBuilder) WithContext(ctx *ContextAudit) *TraceBuilder {
	tb.trace.Context = ctx
	return tb
}

// WithRequest captures request details.
func (tb *TraceBuilder) WithRequest(req *RequestTrace) *TraceBuilder {
	tb.trace.Request = req
	return tb
}

// Complete finalizes the trace with response data.
func (tb *TraceBuilder) Complete(tokens TokenUsage, cost float64) *Trace {
	tb.trace.Duration = time.Since(tb.start)
	tb.trace.Tokens = tokens
	tb.trace.Cost = cost
	return &tb.trace
}

// WithToolCalls adds tool calls to the trace.
func (tb *TraceBuilder) WithToolCalls(calls []tools.ToolCall) *TraceBuilder {
	tb.trace.ToolCalls = calls
	return tb
}

// WithReasoning adds reasoning content.
func (tb *TraceBuilder) WithReasoning(reasoning string) *TraceBuilder {
	tb.trace.Reasoning = reasoning
	return tb
}

// WithContent adds text content.
func (tb *TraceBuilder) WithContent(content string) *TraceBuilder {
	tb.trace.Content = content
	return tb
}

// WithError marks the trace as failed.
func (tb *TraceBuilder) WithError(err error) *TraceBuilder {
	if err != nil {
		tb.trace.Error = err.Error()
	}
	return tb
}

// Build returns the trace without completing it (for error cases).
func (tb *TraceBuilder) Build() *Trace {
	tb.trace.Duration = time.Since(tb.start)
	return &tb.trace
}
