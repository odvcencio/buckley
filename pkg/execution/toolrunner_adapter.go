package execution

import (
	"strings"

	"github.com/odvcencio/buckley/pkg/encoding/toon"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/toolrunner"
)

type toolrunnerStreamAdapter struct {
	handler StreamHandler
}

func (a *toolrunnerStreamAdapter) OnText(text string) {
	if a.handler != nil {
		a.handler.OnText(text)
	}
}

func (a *toolrunnerStreamAdapter) OnReasoning(reasoning string) {
	if a.handler != nil {
		a.handler.OnReasoning(reasoning)
	}
}

func (a *toolrunnerStreamAdapter) OnToolStart(name string, arguments string) {
	if a.handler != nil {
		a.handler.OnToolStart(name, arguments)
	}
}

func (a *toolrunnerStreamAdapter) OnToolEnd(name string, result string, err error) {
	if a.handler != nil {
		a.handler.OnToolEnd(name, result, err)
	}
}

func (a *toolrunnerStreamAdapter) OnComplete(result *toolrunner.Result) {
	if a.handler != nil {
		a.handler.OnComplete(toExecutionResult(result))
	}
}

func buildMessages(req ExecutionRequest) []model.Message {
	var messages []model.Message

	if req.SystemPrompt != "" {
		messages = append(messages, model.Message{
			Role:    "system",
			Content: req.SystemPrompt,
		})
	}

	// Use ToModelMessages to preserve ToolCalls, ToolCallID, Name fields
	if req.Conversation != nil {
		messages = append(messages, req.Conversation.ToModelMessages()...)
	}

	if req.Conversation != nil && len(req.Conversation.Messages) > 0 {
		last := req.Conversation.Messages[len(req.Conversation.Messages)-1]
		if last.Role == "user" {
			if strings.TrimSpace(req.Prompt) == "" {
				return messages
			}
			if parts, ok := last.Content.([]model.ContentPart); ok && len(parts) > 0 {
				if strings.TrimSpace(parts[0].Text) == strings.TrimSpace(req.Prompt) {
					return messages
				}
			} else if text, err := model.ExtractTextContent(last.Content); err == nil {
				if strings.TrimSpace(text) == strings.TrimSpace(req.Prompt) {
					return messages
				}
			}
		}
	}

	messages = append(messages, model.Message{
		Role:    "user",
		Content: req.Prompt,
	})

	return messages
}

func toExecutionResult(result *toolrunner.Result) *ExecutionResult {
	if result == nil {
		return &ExecutionResult{}
	}
	// Sanitize any TOON fragments that may have leaked into the output.
	// TOON is great for wire efficiency but should never appear in user-facing content.
	content := strings.TrimSpace(toon.SanitizeOutput(result.Content))
	return &ExecutionResult{
		Content:      content,
		Reasoning:    result.Reasoning,
		ToolCalls:    result.ToolCalls,
		Usage:        result.Usage,
		Iterations:   result.Iterations,
		FinishReason: result.FinishReason,
	}
}
