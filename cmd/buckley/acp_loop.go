package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/odvcencio/buckley/pkg/acp"
	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/tool"
	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

func runACPLoop(
	ctx context.Context,
	cfg *config.Config,
	mgr *model.Manager,
	state *acpSessionState,
	modelOverride string,
	stream acp.StreamFunc,
) (string, error) {
	if state == nil {
		return "", fmt.Errorf("session state unavailable")
	}
	conv := state.conv
	registry := state.registry
	skillState := state.skillState
	modelID := strings.TrimSpace(modelOverride)
	if modelID == "" {
		modelID = cfg.Models.Execution
		if modelID == "" {
			modelID = cfg.Models.Planning
		}
	}

	useTools := registry != nil
	toolChoice := "auto"

	maxNudges := 2
	nudgeCount := 0
	lastPhase := ""
	for {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		lastPhase = sendACPPhaseUpdate(stream, lastPhase, "Thinking…")

		var tools []map[string]any
		allowedTools := []string{}
		if skillState != nil {
			allowedTools = skillState.ToolFilter()
		}
		if useTools && registry != nil {
			tools = registry.ToOpenAIFunctionsFiltered(allowedTools)
			if len(tools) == 0 {
				useTools = false
			}
		}
		toolsEnabled := len(tools) > 0

		req := model.ChatRequest{
			Model:    modelID,
			Messages: buildACPRequestMessages(cfg, mgr, state, modelID, allowedTools, useTools),
		}
		if useTools {
			req.Tools = tools
			req.ToolChoice = toolChoice
		}
		if reasoning := strings.TrimSpace(cfg.Models.Reasoning); reasoning != "" && mgr.SupportsReasoning(modelID) {
			req.Reasoning = &model.ReasoningConfig{Effort: reasoning}
		}

		resp, err := mgr.ChatCompletion(ctx, req)
		if err != nil {
			if useTools && isToolUnsupportedError(err) {
				useTools = false
				continue
			}
			return "", err
		}
		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("no response choices")
		}

		msg := resp.Choices[0].Message
		if len(msg.ToolCalls) == 0 {
			text, err := model.ExtractTextContent(msg.Content)
			if err != nil {
				return "", err
			}
			if useTools && toolsEnabled && nudgeCount < maxNudges && shouldNudgeForTools(text) {
				nudgeCount++
				conv.AddUserMessage("Use tools to take action now. Pick a tool and call it; do not answer with prose alone.")
				continue
			}
			sendACPPhaseUpdate(stream, lastPhase, "Finalizing response…")
			conv.AddAssistantMessageWithReasoning(text, msg.Reasoning)
			return text, nil
		}

		lastPhase = sendACPPhaseUpdate(stream, lastPhase, fmt.Sprintf("Executing %d tool call(s)…", len(msg.ToolCalls)))

		for i := range msg.ToolCalls {
			if msg.ToolCalls[i].ID == "" {
				msg.ToolCalls[i].ID = fmt.Sprintf("tool-%d", i+1)
			}
		}
		conv.AddToolCallMessage(msg.ToolCalls)

		for i, tc := range msg.ToolCalls {
			params, err := parseACPToolParams(tc.Function.Arguments)
			if err != nil {
				rawParams := map[string]any{"raw": tc.Function.Arguments}
				lastPhase = sendACPPhaseUpdate(stream, lastPhase, fmt.Sprintf("Running %s (%d/%d)…", toolCallTitle(tc.Function.Name, nil), i+1, len(msg.ToolCalls)))
				sendACPToolCallStart(stream, tc, rawParams)
				toolText := fmt.Sprintf("Error: invalid tool arguments: %v", err)
				conv.AddToolResponseMessage(tc.ID, tc.Function.Name, toolText)
				sendACPToolCallUpdate(stream, tc, rawParams, acp.ToolCallStatusFailed, toolText, map[string]any{
					"error": err.Error(),
				}, nil)
				continue
			}

			lastPhase = sendACPPhaseUpdate(stream, lastPhase, fmt.Sprintf("Running %s (%d/%d)…", toolCallTitle(tc.Function.Name, params), i+1, len(msg.ToolCalls)))
			sendACPToolCallStart(stream, tc, params)

			if !tool.IsToolAllowed(tc.Function.Name, allowedTools) {
				toolText := fmt.Sprintf("Error: tool %s not allowed by active skills", tc.Function.Name)
				conv.AddToolResponseMessage(tc.ID, tc.Function.Name, toolText)
				sendACPToolCallUpdate(stream, tc, params, acp.ToolCallStatusFailed, toolText, map[string]any{
					"error": toolText,
				}, nil)
				continue
			}

			result, execErr := executeACPToolCall(registry, tc.Function.Name, params, tc.ID)
			toolText := formatACPToolResult(result, execErr)
			displayText := formatACPToolDisplay(result, execErr)
			conv.AddToolResponseMessage(tc.ID, tc.Function.Name, toolText)

			status := acp.ToolCallStatusCompleted
			if execErr != nil || (result != nil && !result.Success) {
				status = acp.ToolCallStatusFailed
			}
			sendACPToolCallUpdate(stream, tc, params, status, displayText, toolCallRawOutput(result, execErr), result)
		}
	}
}

func parseACPToolParams(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}, nil
	}
	var params map[string]any
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		return nil, err
	}
	if params == nil {
		params = make(map[string]any)
	}
	return params, nil
}

func shouldNudgeForTools(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}
	intentPhrases := []string{
		"i'll", "i will", "let me", "i can", "i'm going to", "i am going to",
	}
	actionPhrases := []string{
		"search", "check", "look", "scan", "read", "open", "inspect", "review", "browse", "find",
		"run", "execute", "test",
	}
	intent := false
	for _, phrase := range intentPhrases {
		if strings.Contains(lower, phrase) {
			intent = true
			break
		}
	}
	if !intent {
		return false
	}
	for _, phrase := range actionPhrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

func isToolUnsupportedError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "tool") && strings.Contains(lower, "not support") {
		return true
	}
	if strings.Contains(lower, "tool") && strings.Contains(lower, "unsupported") {
		return true
	}
	if strings.Contains(lower, "does not support tool calling") {
		return true
	}
	if strings.Contains(lower, "does not support tool response") {
		return true
	}
	return false
}

func executeACPToolCall(registry *tool.Registry, name string, params map[string]any, callID string) (*builtin.Result, error) {
	if registry == nil {
		return nil, fmt.Errorf("tool registry unavailable")
	}
	if params == nil {
		params = make(map[string]any)
	}
	if callID != "" {
		params[tool.ToolCallIDParam] = callID
	}
	return registry.Execute(name, params)
}

func formatACPToolResult(result *builtin.Result, err error) string {
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	if result == nil {
		return "No result"
	}
	if !result.Success {
		return fmt.Sprintf("Error: %s", result.Error)
	}
	if msg, shows := result.DisplayData["message"].(string); shows && msg != "" {
		return msg
	}
	if len(result.Data) > 0 {
		data, err := json.MarshalIndent(result.Data, "", "  ")
		if err == nil {
			return string(data)
		}
	}
	return "Success"
}

func formatACPToolDisplay(result *builtin.Result, err error) string {
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	if result == nil {
		return "No result"
	}
	if !result.Success {
		if result.Error != "" {
			return fmt.Sprintf("Error: %s", result.Error)
		}
		return "Error"
	}
	if len(result.DisplayData) > 0 {
		if msg, ok := result.DisplayData["message"].(string); ok && msg != "" && len(result.DisplayData) == 1 {
			return msg
		}
		if data, err := json.MarshalIndent(result.DisplayData, "", "  "); err == nil {
			return string(data)
		}
	}
	if len(result.Data) > 0 {
		data, err := json.MarshalIndent(result.Data, "", "  ")
		if err == nil {
			return string(data)
		}
	}
	return "Success"
}
