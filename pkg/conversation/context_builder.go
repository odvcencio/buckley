package conversation

import (
	"context"
	"strings"
)

const contextMessageOverheadTokens = 4

// TokenCounter counts tokens for a given text snippet.
type TokenCounter interface {
	Count(text string) int
}

// Compactor triggers asynchronous compaction decisions.
type Compactor interface {
	ShouldAutoCompact(mode string, usageRatio float64) bool
	CompactAsync(ctx context.Context)
}

type defaultTokenCounter struct{}

func (defaultTokenCounter) Count(text string) int {
	return CountTokens(text)
}

// ContextBuilder trims conversation context and triggers auto-compaction when needed.
type ContextBuilder struct {
	tokenCounter TokenCounter
	compactor    Compactor
}

// NewContextBuilder creates a ContextBuilder with default token counting.
func NewContextBuilder(compactor Compactor) *ContextBuilder {
	return &ContextBuilder{
		tokenCounter: defaultTokenCounter{},
		compactor:    compactor,
	}
}

// BuildMessages trims conversation messages to fit the budget and triggers compaction.
func (cb *ContextBuilder) BuildMessages(conv *Conversation, budget int, mode string) []Message {
	if conv == nil || len(conv.Messages) == 0 {
		return nil
	}

	usage := cb.estimateUsage(conv, budget)
	if cb.compactor != nil && usage > 0 && cb.compactor.ShouldAutoCompact(mode, usage) {
		cb.compactor.CompactAsync(context.Background())
	}

	if budget <= 0 {
		return append([]Message{}, conv.Messages...)
	}

	return cb.trimToBudget(conv.Messages, budget)
}

func (cb *ContextBuilder) trimToBudget(messages []Message, budget int) []Message {
	if len(messages) == 0 || budget <= 0 {
		return nil
	}

	used := 0
	start := len(messages)
	lastIdx := len(messages) - 1

	for i := lastIdx; i >= 0; i-- {
		tokens := cb.messageTokens(messages[i])
		if i == lastIdx && tokens > budget {
			start = i
			used += tokens
			break
		}
		if used+tokens > budget {
			break
		}
		used += tokens
		start = i
	}

	if start == len(messages) {
		return nil
	}

	// Ensure we don't orphan tool response messages.
	// Tool responses must have their corresponding assistant message with tool_calls.
	start = cb.adjustStartForToolPairs(messages, start, budget, &used)

	return append([]Message{}, messages[start:]...)
}

// adjustStartForToolPairs moves start backwards to include assistant messages
// that contain tool_calls for any tool responses we're keeping.
// This prevents orphaned tool responses which break API contracts.
func (cb *ContextBuilder) adjustStartForToolPairs(messages []Message, start int, _ int, used *int) int {
	if start >= len(messages) {
		return start
	}

	// Collect tool_call_ids from tool responses we're keeping
	neededToolCallIDs := make(map[string]bool)
	for i := start; i < len(messages); i++ {
		if messages[i].Role == "tool" && messages[i].ToolCallID != "" {
			neededToolCallIDs[messages[i].ToolCallID] = true
		}
	}

	if len(neededToolCallIDs) == 0 {
		return start
	}

	// Find assistant messages with matching tool_calls that we need to include
	for i := start - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != "assistant" || len(msg.ToolCalls) == 0 {
			continue
		}

		// Check if this assistant message has tool_calls we need
		hasNeeded := false
		for _, tc := range msg.ToolCalls {
			if neededToolCallIDs[tc.ID] {
				hasNeeded = true
				// Mark as found so we don't keep searching
				delete(neededToolCallIDs, tc.ID)
			}
		}

		if hasNeeded {
			// Include this message and all between it and current start
			for j := i; j < start; j++ {
				*used += cb.messageTokens(messages[j])
			}
			start = i

			// Also include any tool responses between this assistant and the previous start
			// that reference tool_calls from this same assistant message
			for _, tc := range msg.ToolCalls {
				neededToolCallIDs[tc.ID] = true
			}
		}

		if len(neededToolCallIDs) == 0 {
			break
		}
	}

	// If we still have orphaned tool responses (assistant message not found),
	// we need to skip those tool responses entirely
	if len(neededToolCallIDs) > 0 {
		// Move start forward to skip orphaned tool responses
		for start < len(messages) {
			if messages[start].Role == "tool" && neededToolCallIDs[messages[start].ToolCallID] {
				*used -= cb.messageTokens(messages[start])
				start++
			} else {
				break
			}
		}
	}

	return start
}

func (cb *ContextBuilder) estimateUsage(conv *Conversation, budget int) float64 {
	if conv == nil || budget <= 0 {
		return 0
	}

	if conv.TokenCount > 0 {
		total := conv.TokenCount + len(conv.Messages)*contextMessageOverheadTokens
		if total == 0 {
			return 0
		}
		return float64(total) / float64(budget)
	}

	total := 0
	tokenCount := 0
	for i := range conv.Messages {
		tokens := conv.Messages[i].Tokens
		if tokens <= 0 {
			tokens = cb.messageTokens(conv.Messages[i]) - contextMessageOverheadTokens
			if tokens < 0 {
				tokens = 0
			}
			conv.Messages[i].Tokens = tokens
		}
		tokenCount += tokens
		total += tokens + contextMessageOverheadTokens
	}
	conv.TokenCount = tokenCount

	if total == 0 {
		return 0
	}

	return float64(total) / float64(budget)
}

func (cb *ContextBuilder) messageTokens(msg Message) int {
	if msg.Tokens > 0 {
		return msg.Tokens + contextMessageOverheadTokens
	}

	content := GetContentAsString(msg.Content)
	if msg.Role == "assistant" && strings.TrimSpace(content) == "" && strings.TrimSpace(msg.Reasoning) != "" {
		content = msg.Reasoning
	}

	counter := cb.tokenCounter
	if counter == nil {
		counter = defaultTokenCounter{}
	}

	tokens := counter.Count(msg.Role) + counter.Count(content)
	return tokens + contextMessageOverheadTokens
}
