package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/odvcencio/buckley/pkg/conversation"
	"github.com/odvcencio/buckley/pkg/execution"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/tool"
)

type toolLoopOutcome struct {
	response      string
	usage         *model.Usage
	streamed      bool
	errorReported bool
}

func (c *Controller) runToolLoop(ctx context.Context, sess *SessionState, modelID string) (toolLoopOutcome, error) {
	if c.modelMgr == nil {
		return toolLoopOutcome{}, fmt.Errorf("model manager unavailable")
	}
	if sess == nil || sess.Conversation == nil {
		return toolLoopOutcome{}, fmt.Errorf("session unavailable")
	}

	// Use execution strategy if available (the one true path)
	if c.execStrategy != nil {
		return c.runWithStrategy(ctx, sess)
	}

	// Legacy fallback (should not reach here in normal operation)
	useTools := sess.ToolRegistry != nil
	toolChoice := "auto"
	maxIterations := 10
	totalUsage := model.Usage{}

	for range maxIterations {
		if ctx.Err() != nil {
			return toolLoopOutcome{}, ctx.Err()
		}

		allowedTools := []string{}
		if sess.SkillState != nil {
			allowedTools = sess.SkillState.ToolFilter()
		}

		req := model.ChatRequest{
			Model:    modelID,
			Messages: c.buildMessagesForSession(sess, modelID, allowedTools),
		}
		if useTools && sess.ToolRegistry != nil {
			tools := sess.ToolRegistry.ToOpenAIFunctionsFiltered(allowedTools)
			if len(tools) > 0 {
				req.Tools = tools
				req.ToolChoice = toolChoice
			} else {
				useTools = false
			}
		}
		if reasoning := strings.TrimSpace(c.cfg.Models.Reasoning); reasoning != "" && c.modelMgr.SupportsReasoning(modelID) {
			req.Reasoning = &model.ReasoningConfig{Effort: reasoning}
		}

		resp, err := c.modelMgr.ChatCompletion(ctx, req)
		if err != nil {
			if useTools && isToolUnsupportedError(err) {
				useTools = false
				continue
			}
			return toolLoopOutcome{}, err
		}
		totalUsage = addUsage(totalUsage, resp.Usage)

		if len(resp.Choices) == 0 {
			return toolLoopOutcome{}, fmt.Errorf("no response choices")
		}

		msg := resp.Choices[0].Message
		if len(msg.ToolCalls) == 0 {
			text, err := model.ExtractTextContent(msg.Content)
			if err != nil {
				return toolLoopOutcome{}, err
			}
			sess.Conversation.AddAssistantMessageWithReasoning(text, msg.Reasoning)
			c.saveLastMessage(sess)
			c.warnIfTruncatedResponse(sess, resp.Choices[0].FinishReason)
			return toolLoopOutcome{response: text, usage: &totalUsage}, nil
		}

		for i := range msg.ToolCalls {
			if msg.ToolCalls[i].ID == "" {
				msg.ToolCalls[i].ID = fmt.Sprintf("tool-%d", i+1)
			}
		}
		sess.Conversation.AddToolCallMessage(msg.ToolCalls)

		for _, tc := range msg.ToolCalls {
			params, err := parseToolParams(tc.Function.Arguments)
			if err != nil {
				toolText := fmt.Sprintf("Error: invalid tool arguments: %v", err)
				sess.Conversation.AddToolResponseMessage(tc.ID, tc.Function.Name, toolText)
				continue
			}
			if sess.ToolRegistry == nil {
				toolText := "Error: tool registry unavailable"
				sess.Conversation.AddToolResponseMessage(tc.ID, tc.Function.Name, toolText)
				continue
			}
			if !tool.IsToolAllowed(tc.Function.Name, allowedTools) {
				toolText := fmt.Sprintf("Error: tool %s not allowed by active skills", tc.Function.Name)
				sess.Conversation.AddToolResponseMessage(tc.ID, tc.Function.Name, toolText)
				continue
			}
			if params == nil {
				params = make(map[string]any)
			}
			if tc.ID != "" {
				params[tool.ToolCallIDParam] = tc.ID
			}

			result, execErr := sess.ToolRegistry.ExecuteWithContext(ctx, tc.Function.Name, params)
			toolText := formatToolResultForModel(result, execErr)
			sess.Conversation.AddToolResponseMessage(tc.ID, tc.Function.Name, toolText)

			if display := toolDisplayMessage(tc.Function.Name, result, execErr); display != "" {
				c.runIfCurrentSession(sess, func() {
					c.app.AddMessage(display, "system")
				})
			}
		}
	}

	return toolLoopOutcome{usage: &totalUsage}, fmt.Errorf("max tool calling iterations (%d) exceeded", maxIterations)
}

// runWithStrategy executes using the configured execution strategy.
// This is the one true path for tool execution.
func (c *Controller) runWithStrategy(ctx context.Context, sess *SessionState) (toolLoopOutcome, error) {
	// Snapshot the execution strategy under lock to avoid racing with /mode command
	c.mu.Lock()
	strategy := c.execStrategy
	c.mu.Unlock()
	if strategy == nil {
		return toolLoopOutcome{}, fmt.Errorf("execution strategy unavailable")
	}

	// Get the last user message as the prompt
	prompt := ""
	if sess.Conversation != nil {
		messages := sess.Conversation.Messages
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == "user" {
				prompt = conversation.GetContentAsString(messages[i].Content)
				break
			}
		}
	}

	// Build allowed tools from skill state
	var allowedTools []string
	if sess.SkillState != nil {
		allowedTools = sess.SkillState.ToolFilter()
	}

	modelID := c.executionModelID()

	// Build system prompt + trimmed context
	systemPrompt, trimmedConv, budget := c.buildRequestContext(sess, modelID, prompt, allowedTools)

	// Create execution request
	req := execution.ExecutionRequest{
		Prompt:         prompt,
		Conversation:   trimmedConv,
		SessionID:      sess.ID,
		SystemPrompt:   systemPrompt,
		AllowedTools:   allowedTools,
		MaxIterations:  25,
		ContextBuilder: conversation.NewContextBuilder(sess.Compactor),
		ContextBudget:  budget,
	}

	// Set up stream handler for TUI updates
	handler := &tuiStreamHandler{
		app:  c.app,
		sess: sess,
		ctrl: c,
	}
	if runner, ok := strategy.(interface{ SetStreamHandler(execution.StreamHandler) }); ok {
		runner.SetStreamHandler(handler)
	}

	// Execute
	result, err := strategy.Execute(ctx, req)
	if err != nil {
		return toolLoopOutcome{
			streamed:      handler.hasStreamed(),
			errorReported: handler.errorWasReported(),
		}, err
	}

	// Update conversation with result
	if result.Content != "" || result.Reasoning != "" {
		sess.Conversation.AddAssistantMessageWithReasoning(result.Content, result.Reasoning)
		c.saveLastMessage(sess)
	}
	c.warnIfTruncatedResponse(sess, result.FinishReason)

	usage := &model.Usage{
		PromptTokens:     result.Usage.PromptTokens,
		CompletionTokens: result.Usage.CompletionTokens,
		TotalTokens:      result.Usage.TotalTokens,
	}

	return toolLoopOutcome{
		response:      result.Content,
		usage:         usage,
		streamed:      handler.hasStreamed(),
		errorReported: handler.errorWasReported(),
	}, nil
}

// tuiStreamHandler bridges execution events to the TUI display.
