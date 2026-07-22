package model

import (
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// streamAccumulatorPool provides memory-efficient recycling of StreamAccumulator
// instances to reduce GC pressure during streaming operations.
var streamAccumulatorPool = sync.Pool{
	New: func() any {
		return &StreamAccumulator{}
	},
}

// AcquireStreamAccumulator retrieves a StreamAccumulator from the pool.
func AcquireStreamAccumulator() *StreamAccumulator {
	a := streamAccumulatorPool.Get().(*StreamAccumulator)
	a.Reset()
	return a
}

// ReleaseStreamAccumulator returns a StreamAccumulator to the pool after resetting it.
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
	content          strings.Builder
	reasoning        strings.Builder
	reasoningDetails []ReasoningDetail
	toolCalls        []ToolCall
	usage            *Usage
	role             string
}

// NewStreamAccumulator creates a new accumulator for streaming responses.
func NewStreamAccumulator() *StreamAccumulator {
	return &StreamAccumulator{}
}

// Add processes a streaming chunk and accumulates its contents.
func (a *StreamAccumulator) Add(chunk StreamChunk) {
	// OpenRouter can send usage in a terminal chunk with no choices.
	if chunk.Usage != nil {
		a.usage = chunk.Usage
	}
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
	if len(delta.ReasoningDetails) > 0 {
		a.reasoningDetails = append(a.reasoningDetails, delta.ReasoningDetails...)
	}

	// Accumulate tool calls by index
	for _, tc := range delta.ToolCalls {
		a.accumulateToolCall(tc)
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
		Role:             a.role,
		Content:          a.content.String(),
		Reasoning:        NormalizeReasoningText(a.reasoning.String()),
		ReasoningDetails: a.reasoningDetails,
		ToolCalls:        a.toolCalls,
	}
}

// Content returns the accumulated text content.
func (a *StreamAccumulator) Content() string {
	return a.content.String()
}

// Reasoning returns the accumulated reasoning/thinking content.
func (a *StreamAccumulator) Reasoning() string {
	return NormalizeReasoningText(a.reasoning.String())
}

// NormalizeReasoningText removes provider chunk separators that occasionally
// arrive as a newline before nearly every reasoning token. Deliberately
// formatted prose is left untouched.
func NormalizeReasoningText(text string) string {
	if !reasoningIsSplitPerToken(text) {
		return text
	}
	return joinReasoningFragments(text)
}

func joinReasoningFragments(text string) string {
	var joined strings.Builder
	start := 0
	newlines := 0
	for start < len(text) {
		end := strings.IndexByte(text[start:], '\n')
		if end < 0 {
			writeReasoningFragment(&joined, text[start:], newlines)
			break
		}
		end += start
		writeReasoningFragment(&joined, text[start:end], newlines)
		start = end
		newlines = 0
		for start < len(text) && text[start] == '\n' {
			newlines++
			start++
		}
	}
	return joined.String()
}

func writeReasoningFragment(joined *strings.Builder, fragment string, newlines int) {
	if strings.Trim(fragment, " \t") == "" {
		if joined.Len() > 0 && !endsWithSpace(joined.String()) {
			joined.WriteByte(' ')
		}
		return
	}
	if joined.Len() > 0 {
		if breaks := reasoningStructuralBreak(joined.String(), fragment, newlines); breaks > 0 {
			joined.WriteString(strings.Repeat("\n", breaks))
		} else if !startsWithSpace(fragment) && reasoningFragmentNeedsSpace(joined.String(), fragment) {
			joined.WriteByte(' ')
		}
	}
	joined.WriteString(fragment)
}

func reasoningStructuralBreak(joined, next string, newlines int) int {
	trimmed := strings.TrimLeft(next, " \t")
	if newlines < 3 && strings.HasPrefix(trimmed, "- ") {
		return 1
	}
	if startsWithSpace(next) {
		return 0
	}
	previous, _ := utf8.DecodeLastRuneInString(strings.TrimRight(joined, " \t"))
	first, _ := utf8.DecodeRuneInString(trimmed)
	if newlines < 3 && previous == ':' && unicode.IsDigit(first) {
		return 2
	}
	if newlines != 2 {
		return 0
	}
	if strings.HasPrefix(trimmed, "**") {
		return 2
	}
	if previous == '.' && (unicode.IsUpper(first) || unicode.IsDigit(first) && utf8.RuneCountInString(trimmed) == 1) {
		return 2
	}
	return 0
}

func startsWithSpace(text string) bool {
	first, _ := utf8.DecodeRuneInString(text)
	return unicode.IsSpace(first)
}

func endsWithSpace(text string) bool {
	last, _ := utf8.DecodeLastRuneInString(text)
	return unicode.IsSpace(last)
}

func reasoningFragmentNeedsSpace(joined, next string) bool {
	previous, _ := utf8.DecodeLastRuneInString(joined)
	first, _ := utf8.DecodeRuneInString(next)
	if !unicode.IsLetter(first) && !unicode.IsDigit(first) {
		return false
	}
	if previous == '.' && unicode.IsDigit(first) {
		return false
	}
	return strings.ContainsRune(".,:;!?)]}", previous)
}

func reasoningIsSplitPerToken(text string) bool {
	if strings.Count(text, "\n") < 8 {
		return false
	}

	lines := strings.Split(text, "\n")
	nonEmpty := 0
	short := 0
	indented := 0
	for i, line := range lines {
		if line == "" {
			continue
		}
		nonEmpty++
		if utf8.RuneCountInString(line) <= 16 {
			short++
		}
		if i > 0 && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) {
			indented++
		}
	}
	if nonEmpty < 8 || short*100 < nonEmpty*80 {
		return false
	}

	empty := len(lines) - nonEmpty
	return indented*100 >= nonEmpty*40 || empty*100 >= len(lines)*35
}

// ReasoningDetails returns accumulated OpenRouter reasoning detail blocks.
func (a *StreamAccumulator) ReasoningDetails() []ReasoningDetail {
	return a.reasoningDetails
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
	a.reasoningDetails = nil
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
	// Matches orphaned tool name:index after functions. is stripped
	// Anchored to require at least one underscore or "call" to avoid matching legitimate content
	orphanedIDPattern = regexp.MustCompile(`\b(?:[\w]*_[\w]*|tool_call|function_call):\d+\b`)
	// Matches partial token fragments
	partialTokenPattern = regexp.MustCompile(`[_a-z]*call[_a-z]*\|?>?`)
)

// FilterToolCallTokens removes tool call markup tokens from streamed content.
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

// ParseToolCallsFromContent extracts tool calls embedded in text content.
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

			toolCallID := match[1] // e.g., "functions.get_weather:0"
			arguments := match[2]  // JSON arguments

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
		Role:             a.role,
		Content:          content,
		Reasoning:        NormalizeReasoningText(a.reasoning.String()),
		ReasoningDetails: a.reasoningDetails,
		ToolCalls:        toolCalls,
	}
}
