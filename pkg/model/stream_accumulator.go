package model

import (
	"regexp"
	"strings"
	"sync"
)

// streamAccumulatorPool provides memory-efficient recycling of StreamAccumulator
// instances to reduce GC pressure during streaming operations.
var streamAccumulatorPool = sync.Pool{
	New: func() any {
		return &StreamAccumulator{}
	},
}

// AcquireStreamAccumulator retrieves a StreamAccumulator from the pool.
// The accumulator is reset and ready for use.
func AcquireStreamAccumulator() *StreamAccumulator {
	a := streamAccumulatorPool.Get().(*StreamAccumulator)
	a.Reset()
	return a
}

// ReleaseStreamAccumulator returns a StreamAccumulator to the pool for reuse.
// The accumulator should not be used after this call.
func ReleaseStreamAccumulator(a *StreamAccumulator) {
	if a == nil {
		return
	}
	a.Reset()
	streamAccumulatorPool.Put(a)
}

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
	return AcquireStreamAccumulator()
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

// Kimi K2 special token markers for tool calls
const (
	toolCallSectionBegin = "<|tool_calls_section_begin|>"
	toolCallSectionEnd   = "<|tool_calls_section_end|>"
	toolCallBegin        = "<|tool_call_begin|>"
	toolCallEnd          = "<|tool_call_end|>"
	toolCallArgBegin     = "<|tool_call_argument_begin|>"
)

// toolCallTokens lists all special tokens to filter from streaming output
var toolCallTokens = []string{
	toolCallSectionBegin,
	toolCallSectionEnd,
	toolCallBegin,
	toolCallEnd,
	toolCallArgBegin,
}

// Patterns to match tool call related content
var (
	// Matches full tool call IDs: functions.something:0
	toolCallIDPattern = regexp.MustCompile(`functions\.[\w_]+:\d+`)
	// Matches orphaned tool name:index after functions. is stripped: something:0
	orphanedIDPattern = regexp.MustCompile(`\b[\w_]+:\d+\b`)
	// Matches partial token fragments
	partialTokenPattern = regexp.MustCompile(`[_a-z]*call[_a-z]*\|?>?`)
)

// FilterToolCallTokens removes Kimi K2 style tool call tokens from streaming content.
// This prevents special tokens from being displayed to users during streaming.
func FilterToolCallTokens(content string) string {
	// Quick check - if no special markers, return as-is
	if !strings.Contains(content, "<|") && !strings.Contains(content, "|>") &&
		!strings.Contains(content, "functions.") && !strings.Contains(content, "_call") {
		return content
	}

	result := content

	// First, remove full tool call IDs before removing tokens
	result = toolCallIDPattern.ReplaceAllString(result, "")

	// Remove all known tool call tokens
	for _, token := range toolCallTokens {
		result = strings.ReplaceAll(result, token, "")
	}

	// Remove partial token fragments that appear at chunk boundaries
	result = partialTokenPattern.ReplaceAllString(result, "")

	// Remove delimiter fragments
	result = strings.ReplaceAll(result, "<|", "")
	result = strings.ReplaceAll(result, "|>", "")

	// Remove any orphaned name:index patterns (leftovers after functions. removed)
	result = orphanedIDPattern.ReplaceAllString(result, "")

	// Clean up extra whitespace
	result = strings.TrimSpace(result)

	return result
}

// Regex patterns for parsing Kimi K2 tool call format
var (
	toolCallSectionPattern = regexp.MustCompile(`<\|tool_calls_section_begin\|>(.*?)<\|tool_calls_section_end\|>`)
	toolCallPattern        = regexp.MustCompile(`<\|tool_call_begin\|>\s*(?P<id>[\w\.]+:\d+)\s*<\|tool_call_argument_begin\|>\s*(?P<args>.*?)\s*<\|tool_call_end\|>`)
)

// ParseToolCallsFromContent extracts tool calls from Kimi K2's special token format.
// This is a fallback for when the provider doesn't parse these tokens server-side.
// Returns the extracted tool calls and the content with tool call tokens stripped.
func ParseToolCallsFromContent(content string) ([]ToolCall, string) {
	if !strings.Contains(content, toolCallSectionBegin) {
		return nil, content
	}

	var toolCalls []ToolCall

	// Find all tool call sections
	sections := toolCallSectionPattern.FindAllStringSubmatch(content, -1)
	for _, section := range sections {
		if len(section) < 2 {
			continue
		}
		sectionContent := section[1]

		// Extract individual tool calls from the section
		matches := toolCallPattern.FindAllStringSubmatch(sectionContent, -1)
		for _, match := range matches {
			if len(match) < 3 {
				continue
			}

			toolCallID := match[1]   // e.g., "functions.get_weather:0"
			arguments := match[2]    // JSON arguments

			// Extract function name from ID (format: functions.{name}:{idx})
			funcName := extractFunctionName(toolCallID)

			toolCalls = append(toolCalls, ToolCall{
				ID:   toolCallID,
				Type: "function",
				Function: FunctionCall{
					Name:      funcName,
					Arguments: strings.TrimSpace(arguments),
				},
			})
		}
	}

	// Strip tool call sections from content
	cleanContent := toolCallSectionPattern.ReplaceAllString(content, "")
	cleanContent = strings.TrimSpace(cleanContent)

	return toolCalls, cleanContent
}

// extractFunctionName extracts the function name from a Kimi K2 tool call ID.
// Format: "functions.{func_name}:{idx}" -> returns "{func_name}"
func extractFunctionName(toolCallID string) string {
	// Remove "functions." prefix
	toolCallID = strings.TrimPrefix(toolCallID, "functions.")
	// Remove ":{idx}" suffix
	if idx := strings.LastIndex(toolCallID, ":"); idx != -1 {
		toolCallID = toolCallID[:idx]
	}
	return toolCallID
}

// FinalizeWithTokenParsing returns the accumulated message, parsing any
// embedded tool call tokens from the content if no structured tool calls
// were received. This handles models like Kimi K2 when the provider doesn't
// parse the special tokens server-side.
func (a *StreamAccumulator) FinalizeWithTokenParsing() Message {
	content := a.content.String()
	toolCalls := a.toolCalls

	// If no structured tool calls but content has special tokens, parse them
	if len(toolCalls) == 0 && strings.Contains(content, toolCallSectionBegin) {
		toolCalls, content = ParseToolCallsFromContent(content)
	}

	// Always filter any remaining tool call tokens from content.
	// This handles cases where structured tool calls exist but tokens
	// also leaked into the content field.
	content = FilterToolCallTokens(content)

	return Message{
		Role:      a.role,
		Content:   content,
		Reasoning: a.reasoning.String(),
		ToolCalls: toolCalls,
	}
}
