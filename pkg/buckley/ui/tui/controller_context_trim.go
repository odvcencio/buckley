package tui

import "github.com/odvcencio/buckley/pkg/conversation"

func trimConversationToBudget(conv *conversation.Conversation, budgetTokens int) *conversation.Conversation {
	if conv == nil {
		return nil
	}
	if budgetTokens <= 0 || len(conv.Messages) == 0 {
		return &conversation.Conversation{SessionID: conv.SessionID}
	}

	used := 0
	start := len(conv.Messages)
	lastIdx := len(conv.Messages) - 1

	for i := lastIdx; i >= 0; i-- {
		msgTokens := estimateConversationMessageTokens(conv.Messages[i])
		if i == lastIdx && msgTokens > budgetTokens {
			start = i
			used = msgTokens
			break
		}
		if used+msgTokens > budgetTokens {
			break
		}
		used += msgTokens
		start = i
	}

	if start == len(conv.Messages) {
		return &conversation.Conversation{SessionID: conv.SessionID}
	}

	// Ensure we don't orphan tool response messages.
	// Tool responses must have their corresponding assistant message with tool_calls.
	start, used = adjustStartForToolCallPairs(conv.Messages, start, used)

	trimmed := &conversation.Conversation{
		SessionID:  conv.SessionID,
		Messages:   append([]conversation.Message{}, conv.Messages[start:]...),
		TokenCount: used,
	}
	return trimmed
}

// adjustStartForToolCallPairs moves start backwards to include assistant messages
// that contain tool_calls for any tool responses we're keeping.
// This prevents orphaned tool responses which break API contracts.
func adjustStartForToolCallPairs(messages []conversation.Message, start, used int) (int, int) {
	if start >= len(messages) {
		return start, used
	}

	// Collect tool_call_ids from tool responses we're keeping.
	neededToolCallIDs := make(map[string]bool)
	for i := start; i < len(messages); i++ {
		if messages[i].Role == "tool" && messages[i].ToolCallID != "" {
			neededToolCallIDs[messages[i].ToolCallID] = true
		}
	}

	if len(neededToolCallIDs) == 0 {
		return start, used
	}

	// Find assistant messages with matching tool_calls that we need to include.
	for i := start - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != "assistant" || len(msg.ToolCalls) == 0 {
			continue
		}

		// Check if this assistant message has tool_calls we need.
		hasNeeded := false
		for _, tc := range msg.ToolCalls {
			if neededToolCallIDs[tc.ID] {
				hasNeeded = true
				delete(neededToolCallIDs, tc.ID)
			}
		}

		if hasNeeded {
			// Include this message and all between it and current start.
			for j := i; j < start; j++ {
				used += estimateConversationMessageTokens(messages[j])
			}
			start = i

			// Also track tool_calls from this assistant message.
			for _, tc := range msg.ToolCalls {
				neededToolCallIDs[tc.ID] = true
			}
		}

		if len(neededToolCallIDs) == 0 {
			break
		}
	}

	// If we still have orphaned tool responses (assistant message not found),
	// skip those tool responses entirely.
	if len(neededToolCallIDs) > 0 {
		for start < len(messages) {
			if messages[start].Role == "tool" && neededToolCallIDs[messages[start].ToolCallID] {
				used -= estimateConversationMessageTokens(messages[start])
				start++
			} else {
				break
			}
		}
	}

	return start, used
}
