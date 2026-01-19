package model

import "strings"

// StreamAccumulator accumulates streaming chunks into complete responses.
// It handles tool call delta accumulation following the OpenAI-compatible pattern
// used by Kimi K2 and other models.
type StreamAccumulator struct {
	content   strings.Builder
	reasoning strings.Builder
	toolCalls []ToolCall
	usage     *Usage
	role      string
}

// NewStreamAccumulator creates a new accumulator for streaming responses.
func NewStreamAccumulator() *StreamAccumulator {
	return &StreamAccumulator{}
}

// Add processes a streaming chunk and accumulates its contents.
func (a *StreamAccumulator) Add(chunk StreamChunk) {
	if len(chunk.Choices) == 0 {
		return
	}

	choice := chunk.Choices[0]
	delta := choice.Delta

	// Accumulate role (usually only in first chunk)
	if delta.Role != "" {
		a.role = delta.Role
	}

	// Accumulate text content
	if delta.Content != "" {
		a.content.WriteString(delta.Content)
	}

	// Accumulate reasoning/thinking content
	if delta.Reasoning != "" {
		a.reasoning.WriteString(delta.Reasoning)
	}

	// Accumulate tool calls by index
	for _, tc := range delta.ToolCalls {
		a.accumulateToolCall(tc)
	}

	// Capture usage from final chunk
	if chunk.Usage != nil {
		a.usage = chunk.Usage
	}
}

// accumulateToolCall accumulates a tool call delta into the appropriate slot.
// This follows the Kimi K2 / OpenAI streaming pattern where:
// - Each delta has an index indicating which tool call it belongs to
// - ID, name, and arguments are accumulated incrementally
func (a *StreamAccumulator) accumulateToolCall(delta ToolCallDelta) {
	// Expand the slice if needed
	for len(a.toolCalls) <= delta.Index {
		a.toolCalls = append(a.toolCalls, ToolCall{
			Type: "function",
			Function: FunctionCall{
				Arguments: "",
			},
		})
	}

	tc := &a.toolCalls[delta.Index]

	// Accumulate ID (usually comes in first chunk for this index)
	if delta.ID != "" {
		tc.ID += delta.ID
	}

	// Accumulate type
	if delta.Type != "" {
		tc.Type = delta.Type
	}

	// Accumulate function name and arguments
	if delta.Function != nil {
		if delta.Function.Name != "" {
			tc.Function.Name += delta.Function.Name
		}
		if delta.Function.Arguments != "" {
			tc.Function.Arguments += delta.Function.Arguments
		}
	}
}

// Message returns the accumulated message.
func (a *StreamAccumulator) Message() Message {
	return Message{
		Role:      a.role,
		Content:   a.content.String(),
		Reasoning: a.reasoning.String(),
		ToolCalls: a.toolCalls,
	}
}

// Content returns the accumulated text content.
func (a *StreamAccumulator) Content() string {
	return a.content.String()
}

// Reasoning returns the accumulated reasoning/thinking content.
func (a *StreamAccumulator) Reasoning() string {
	return a.reasoning.String()
}

// ToolCalls returns the accumulated tool calls.
func (a *StreamAccumulator) ToolCalls() []ToolCall {
	return a.toolCalls
}

// HasToolCalls returns true if any tool calls have been accumulated.
func (a *StreamAccumulator) HasToolCalls() bool {
	return len(a.toolCalls) > 0
}

// Usage returns the usage information from the final chunk.
func (a *StreamAccumulator) Usage() *Usage {
	return a.usage
}

// Reset clears the accumulator for reuse.
func (a *StreamAccumulator) Reset() {
	a.content.Reset()
	a.reasoning.Reset()
	a.toolCalls = nil
	a.usage = nil
	a.role = ""
}
