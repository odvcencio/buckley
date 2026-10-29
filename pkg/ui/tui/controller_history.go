package tui

import (
	"encoding/json"
	"strings"

	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/model"
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
	renderConversationHistoryWith(app.chatView.AddMessage, messages)
	app.updateScrollStatus()
	app.dirty = true
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
			if strings.TrimSpace(content) != "" && len(msg.ToolCalls) > 0 {
				flushProgress()
				addMessage(content, "assistant")
			}
			if strings.TrimSpace(msg.Reasoning) != "" || len(msg.ToolCalls) > 0 {
				startProgress()
				if reasoning := strings.TrimSpace(msg.Reasoning); reasoning != "" {
					progress.WriteString("\n\nThinking\n\n")
					progress.WriteString(model.NormalizeReasoningText(reasoning))
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
			if guidance := storedCodeModeRecovery(content); guidance != "" {
				progress.WriteString("\n\n")
				progress.WriteString(codeModeRecoveryProgress(guidance))
			}
		default:
			flushProgress()
			if strings.TrimSpace(content) != "" {
				addMessage(content, msg.Role)
			}
		}
	}
	flushProgress()
}

func storedCodeModeRecovery(content string) string {
	index := recoverySuffixStart(content)
	if index < 0 {
		return ""
	}
	return strings.TrimSpace(content[index:])
}

func recoverySuffixStart(content string) int {
	const marker = "CODE MODE RECOVERY:"
	if strings.HasPrefix(content, marker) {
		return 0
	}
	if i := strings.Index(content, "\n\n"+marker); i >= 0 {
		return i + 2
	}
	if i := strings.Index(content, `\n\n`+marker); i >= 0 {
		return i + 4
	}
	return -1
}

func storedToolResultProgressSummary(name, content string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "tool"
	}
	// Recovery guidance is rendered separately below the tool result.
	if index := recoverySuffixStart(content); index >= 0 {
		content = strings.TrimSuffix(content[:index], `\n\n`)
	}
	detail := storedToolError(content)
	if detail != "" {
		return "✗ " + name + " — " + compactStatusText(detail, 200)
	}
	return "✓ " + name + " — completed"
}

func storedToolError(content string) string {
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "{") || content == "null" {
		var payload struct {
			Success bool   `json:"success"`
			Error   string `json:"error"`
		}
		if json.Unmarshal([]byte(content), &payload) == nil {
			if detail := strings.TrimSpace(payload.Error); detail != "" {
				return detail
			}
			if !payload.Success {
				return "failed"
			}
			return ""
		}
	}
	// TOON root scalars carry the envelope. Nested data may have its own
	// success/error fields and must not override the tool's result.
	var success, hasSuccess bool
	var detail string
	for line := range strings.Lines(content) {
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch key {
		case "success":
			if value == "true" || value == "false" {
				success, hasSuccess = value == "true", true
			}
		case "error":
			if json.Unmarshal([]byte(value), &detail) != nil {
				detail = value
			}
			detail = strings.TrimSpace(detail)
		}
	}
	if hasSuccess {
		if detail != "" {
			return detail
		}
		if !success {
			return "failed"
		}
		return ""
	}
	if strings.HasPrefix(strings.ToLower(content), "error:") {
		return strings.TrimSpace(content[len("error:"):])
	}
	if detail != "" {
		return detail
	}
	return ""
}
