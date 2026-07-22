package tui

import (
	"encoding/json"
	"strings"

	"m31labs.dev/buckley/pkg/conversation"
)

func renderConversationHistory(app *WidgetApp, messages []conversation.Message) {
	if app == nil {
		return
	}
	renderConversationHistoryWith(app.AddMessage, messages)
}

func renderConversationHistoryImmediately(app *WidgetApp, messages []conversation.Message) {
	if app == nil {
		return
	}
	renderConversationHistoryWith(app.addMessageImmediately, messages)
}

func renderConversationHistoryWith(addMessage func(content, source string), messages []conversation.Message) {
	var progress strings.Builder
	flushProgress := func() {
		if progress.Len() == 0 {
			return
		}
		addMessage(progress.String(), "system")
		progress.Reset()
	}
	startProgress := func() {
		if progress.Len() == 0 {
			progress.WriteString("Working")
		}
	}

	for _, msg := range messages {
		content := conversation.GetContentAsString(msg.Content)
		switch msg.Role {
		case "assistant":
			if strings.TrimSpace(msg.Reasoning) != "" || len(msg.ToolCalls) > 0 {
				startProgress()
				if reasoning := strings.TrimSpace(msg.Reasoning); reasoning != "" {
					progress.WriteString("\n\nThinking\n\n")
					progress.WriteString(reasoning)
				}
				for _, call := range msg.ToolCalls {
					progress.WriteString("\n\n→ ")
					progress.WriteString(toolCallProgressSummary(call))
				}
			}
			if strings.TrimSpace(content) != "" && len(msg.ToolCalls) == 0 {
				flushProgress()
				addMessage(content, "assistant")
			}
		case "tool":
			startProgress()
			progress.WriteString("\n  ")
			progress.WriteString(storedToolResultProgressSummary(msg.Name, content))
		default:
			flushProgress()
			if strings.TrimSpace(content) != "" {
				addMessage(content, msg.Role)
			}
		}
	}
	flushProgress()
}

func storedToolResultProgressSummary(name, content string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "tool"
	}
	detail := storedToolError(content)
	if detail != "" {
		return "✗ " + name + " — " + compactStatusText(detail, 200)
	}
	return "✓ " + name + " — completed"
}

func storedToolError(content string) string {
	content = strings.TrimSpace(content)
	if strings.HasPrefix(strings.ToLower(content), "error:") {
		return strings.TrimSpace(content[len("error:"):])
	}
	var payload struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	if json.Unmarshal([]byte(content), &payload) == nil && (!payload.Success || strings.TrimSpace(payload.Error) != "") {
		if strings.TrimSpace(payload.Error) != "" {
			return strings.TrimSpace(payload.Error)
		}
		return "failed"
	}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToLower(line), "error:") {
			detail := strings.Trim(strings.TrimSpace(line[len("error:"):]), "\"")
			if detail != "" && detail != "null" {
				return detail
			}
		}
	}
	return ""
}
