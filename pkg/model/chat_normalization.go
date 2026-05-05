package model

import (
	"fmt"
	"strings"
)

const (
	missingToolResultContent = "Tool call did not return a result."
	noResultContent          = "No result"
)

type pendingToolCall struct {
	id   string
	name string
}

func normalizeProviderChatRequest(req ChatRequest, providerID string) ChatRequest {
	req.Messages = normalizeChatMessages(req.Messages, providerID, req.Model)
	if needsNoopToolForHistory(req, providerID) {
		req.Tools = []map[string]any{noopToolDefinition()}
		if strings.TrimSpace(req.ToolChoice) == "" {
			req.ToolChoice = "auto"
		}
	}
	return req
}

func normalizeChatMessages(messages []Message, providerID, modelID string) []Message {
	if len(messages) == 0 {
		return nil
	}

	normalized := make([]Message, 0, len(messages))
	idMap := map[string]string{}
	seenIDs := map[string]int{}
	toolSeq := 0

	for _, msg := range messages {
		msg = cloneMessage(msg)
		msg.Role = strings.TrimSpace(msg.Role)

		switch msg.Role {
		case "assistant":
			if len(msg.ToolCalls) > 0 {
				calls := make([]ToolCall, 0, len(msg.ToolCalls))
				for _, call := range msg.ToolCalls {
					call.Function.Name = strings.TrimSpace(call.Function.Name)
					if call.Function.Name == "" {
						continue
					}
					if strings.TrimSpace(call.Type) == "" {
						call.Type = "function"
					}
					if strings.TrimSpace(call.Function.Arguments) == "" {
						call.Function.Arguments = "{}"
					}

					toolSeq++
					originalID := strings.TrimSpace(call.ID)
					if originalID == "" {
						originalID = fmt.Sprintf("tool-%d", toolSeq)
					}
					call.ID = uniqueToolCallID(sanitizeToolCallID(originalID, providerID, modelID), seenIDs, providerID, modelID)
					idMap[originalID] = call.ID
					if strings.TrimSpace(msg.ToolCallID) != "" {
						idMap[msg.ToolCallID] = call.ID
					}
					calls = append(calls, call)
				}
				msg.ToolCalls = calls
			}
			if len(msg.ToolCalls) == 0 && messageContentEmpty(msg.Content) {
				if strings.TrimSpace(msg.Reasoning) == "" {
					continue
				}
				msg.Content = msg.Reasoning
			}
		case "tool":
			msg.ToolCallID = strings.TrimSpace(msg.ToolCallID)
			if mapped, ok := idMap[msg.ToolCallID]; ok {
				msg.ToolCallID = mapped
			} else if msg.ToolCallID != "" {
				msg.ToolCallID = sanitizeToolCallID(msg.ToolCallID, providerID, modelID)
			}
			if messageContentEmpty(msg.Content) {
				msg.Content = noResultContent
			}
		case "system", "user":
			if messageContentEmpty(msg.Content) {
				continue
			}
		default:
			if messageContentEmpty(msg.Content) && len(msg.ToolCalls) == 0 {
				continue
			}
		}

		normalized = append(normalized, msg)
	}

	return repairToolMessageSequence(normalized)
}

func cloneMessage(msg Message) Message {
	if len(msg.ToolCalls) > 0 {
		msg.ToolCalls = append([]ToolCall(nil), msg.ToolCalls...)
	}
	return msg
}

func messageContentEmpty(content any) bool {
	switch v := content.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(v) == ""
	case []ContentPart:
		if len(v) == 0 {
			return true
		}
		for _, part := range v {
			if strings.TrimSpace(part.Type) != "text" {
				return false
			}
			if strings.TrimSpace(part.Text) != "" {
				return false
			}
		}
		return true
	case []any:
		return len(v) == 0
	default:
		return false
	}
}

func sanitizeToolCallID(id, providerID, modelID string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		id = "tool"
	}

	switch {
	case isMistralLike(providerID, modelID):
		var b strings.Builder
		for _, r := range id {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
				b.WriteRune(r)
			}
		}
		id = b.String()
		if id == "" {
			id = "toolcall"
		}
		if len(id) > 9 {
			id = id[:9]
		}
		for len(id) < 9 {
			id += "0"
		}
		return id
	case isAnthropicLike(providerID, modelID):
		var b strings.Builder
		for _, r := range id {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
				b.WriteRune(r)
				continue
			}
			b.WriteByte('_')
		}
		id = b.String()
		if strings.Trim(id, "_-") == "" {
			return "tool"
		}
		return id
	default:
		return id
	}
}

func uniqueToolCallID(id string, seen map[string]int, providerID, modelID string) string {
	if id == "" {
		id = sanitizeToolCallID("tool", providerID, modelID)
	}
	if seen[id] == 0 {
		seen[id] = 1
		return id
	}

	base := id
	if isMistralLike(providerID, modelID) {
		stem := base
		if len(stem) > 6 {
			stem = stem[:6]
		}
		for index := seen[base] + 1; ; index++ {
			candidate := sanitizeToolCallID(fmt.Sprintf("%s%03d", stem, index), providerID, modelID)
			if seen[candidate] == 0 {
				seen[base] = index
				seen[candidate] = 1
				return candidate
			}
		}
	}

	for {
		seen[base]++
		candidate := sanitizeToolCallID(fmt.Sprintf("%s_%d", base, seen[base]), providerID, modelID)
		if seen[candidate] == 0 {
			seen[candidate] = 1
			return candidate
		}
	}
}

func isAnthropicLike(providerID, modelID string) bool {
	providerID = strings.ToLower(providerID)
	modelID = strings.ToLower(modelID)
	return strings.Contains(providerID, "anthropic") ||
		strings.Contains(modelID, "anthropic") ||
		strings.Contains(modelID, "claude")
}

func isMistralLike(providerID, modelID string) bool {
	providerID = strings.ToLower(providerID)
	modelID = strings.ToLower(modelID)
	return strings.Contains(providerID, "mistral") ||
		strings.Contains(modelID, "mistral") ||
		strings.Contains(modelID, "devstral")
}

func repairToolMessageSequence(messages []Message) []Message {
	if len(messages) == 0 {
		return nil
	}

	out := make([]Message, 0, len(messages))
	var pending []pendingToolCall

	for _, msg := range messages {
		if msg.Role == "tool" {
			var matched pendingToolCall
			var ok bool
			pending, matched, ok = consumePendingToolCall(pending, msg.ToolCallID)
			if !ok {
				out = appendMissingToolResults(out, pending)
				pending = nil
				if orphan := orphanToolResultMessage(msg); !messageContentEmpty(orphan.Content) {
					out = append(out, orphan)
				}
				continue
			}
			if msg.Name == "" {
				msg.Name = matched.name
			}
			out = append(out, msg)
			continue
		}

		out = appendMissingToolResults(out, pending)
		pending = nil

		out = append(out, msg)
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			pending = make([]pendingToolCall, 0, len(msg.ToolCalls))
			for _, call := range msg.ToolCalls {
				pending = append(pending, pendingToolCall{
					id:   call.ID,
					name: call.Function.Name,
				})
			}
		}
	}

	out = appendMissingToolResults(out, pending)
	return out
}

func consumePendingToolCall(pending []pendingToolCall, id string) ([]pendingToolCall, pendingToolCall, bool) {
	for i, call := range pending {
		if call.id == id {
			next := append(pending[:i:i], pending[i+1:]...)
			return next, call, true
		}
	}
	return pending, pendingToolCall{}, false
}

func appendMissingToolResults(messages []Message, pending []pendingToolCall) []Message {
	for _, call := range pending {
		if strings.TrimSpace(call.id) == "" {
			continue
		}
		messages = append(messages, Message{
			Role:       "tool",
			Content:    missingToolResultContent,
			ToolCallID: call.id,
			Name:       call.name,
		})
	}
	return messages
}

func orphanToolResultMessage(msg Message) Message {
	name := strings.TrimSpace(msg.Name)
	if name == "" {
		name = "unknown"
	}
	content := strings.TrimSpace(messageContentToText(msg.Content))
	if content == "" {
		content = noResultContent
	}
	if msg.ToolCallID != "" {
		content = fmt.Sprintf("Tool result from %s (%s) without a matching tool call: %s", name, msg.ToolCallID, content)
	} else {
		content = fmt.Sprintf("Tool result from %s without a matching tool call: %s", name, content)
	}
	return Message{Role: "user", Content: content}
}

func needsNoopToolForHistory(req ChatRequest, providerID string) bool {
	if len(req.Tools) > 0 {
		return false
	}
	if !strings.Contains(strings.ToLower(providerID), "litellm") {
		return false
	}
	return hasToolCallHistory(req.Messages)
}

func hasToolCallHistory(messages []Message) bool {
	for _, msg := range messages {
		if len(msg.ToolCalls) > 0 || msg.Role == "tool" {
			return true
		}
	}
	return false
}

func noopToolDefinition() map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "_noop",
			"description": "Do not call this tool. It exists only for provider compatibility.",
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
	}
}
