package execution

import (
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

	if req.Conversation != nil {
		for _, msg := range req.Conversation.Messages {
			messages = append(messages, model.Message{
				Role:    msg.Role,
				Content: msg.Content,
			})
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
	return &ExecutionResult{
		Content:    result.Content,
		Reasoning:  result.Reasoning,
		ToolCalls:  result.ToolCalls,
		Usage:      result.Usage,
		Iterations: result.Iterations,
		FinishReason: result.FinishReason,
	}
}
