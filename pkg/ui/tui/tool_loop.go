package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func (c *Controller) runToolLoop(ctx context.Context, sess *SessionState, modelID string) (string, *model.Usage, string, error) {
	if c.modelMgr == nil {
		return "", nil, "", fmt.Errorf("model manager unavailable")
	}
	if sess == nil || sess.Conversation == nil {
		return "", nil, "", fmt.Errorf("session unavailable")
	}

	useTools := !c.consumeDisableToolsNextTurn(sess) && sess.ToolRegistry != nil && c.modelMgr.SupportsTools(modelID)
	maxIterations := interactiveMaxToolIterations
	totalUsage := model.Usage{}

	for iter := 0; iter < maxIterations; iter++ {
		if ctx.Err() != nil {
			return "", nil, "", ctx.Err()
		}

		allowedTools := toolLoopAllowedTools(sess)
		req, nextUseTools := c.buildToolLoopRequest(sess, modelID, useTools, allowedTools)
		useTools = nextUseTools

		resp, err := c.callToolLoopModel(ctx, req, modelID, iter, maxIterations)
		if err != nil {
			if useTools && isToolUnsupportedError(err) {
				c.app.SetStatus("Retrying without tools")
				useTools = false
				continue
			}
			return "", nil, "", err
		}
		totalUsage = addUsage(totalUsage, resp.Usage)

		choice, err := firstToolLoopChoice(req, resp)
		if err != nil {
			return "", nil, "", err
		}
		msg := choice.Message
		if len(msg.ToolCalls) == 0 {
			return c.finishToolLoopResponse(sess, msg, totalUsage, choice.FinishReason)
		}

		toolCalls := normalizeToolLoopCalls(sess.ToolRegistry, msg.ToolCalls, allowedTools)
		c.recordToolLoopCalls(sess, toolCalls, msg)
		c.executeToolLoopCalls(sess, toolCalls, allowedTools)
	}

	return c.checkpointToolLoop(sess, totalUsage, maxIterations)
}

func toolLoopAllowedTools(sess *SessionState) []string {
	if sess == nil || sess.SkillState == nil {
		return nil
	}
	return sess.SkillState.ToolFilter()
}

func (c *Controller) buildToolLoopRequest(sess *SessionState, modelID string, useTools bool, allowedTools []string) (model.ChatRequest, bool) {
	req := model.ChatRequest{
		Model:     modelID,
		Messages:  c.buildMessagesForSession(sess),
		SessionID: sess.ID,
	}

	if useTools && sess.ToolRegistry != nil {
		tools := sess.ToolRegistry.ToOpenAIFunctionsGoverned(c.evaluator, "interactive", "coding", allowedTools, 0)
		if len(tools) > 0 {
			req.Tools = tools
			req.ToolChoice = "auto"
		} else {
			useTools = false
		}
	}

	if effort := model.ResolveReasoningEffort(c.cfg, c.modelMgr, c.rulesEngine, modelID, "execution"); effort != "" {
		req.Reasoning = &model.ReasoningConfig{Effort: effort}
	}
	return req, useTools
}

func (c *Controller) callToolLoopModel(ctx context.Context, req model.ChatRequest, modelID string, iteration, maxIterations int) (*model.ChatResponse, error) {
	c.app.StartProcessStatus(modelProcessStatus(modelID, iteration, maxIterations, len(req.Tools), req.Reasoning))
	resp, err := c.modelMgr.ChatCompletion(ctx, req)
	c.app.StopProcessStatus()
	return resp, err
}

func firstToolLoopChoice(req model.ChatRequest, resp *model.ChatResponse) (model.Choice, error) {
	if resp == nil || len(resp.Choices) == 0 {
		return model.Choice{}, model.NoResponseChoicesError(req, resp)
	}
	return resp.Choices[0], nil
}

func (c *Controller) finishToolLoopResponse(sess *SessionState, msg model.Message, totalUsage model.Usage, finishReason string) (string, *model.Usage, string, error) {
	c.app.SetStatus("Finalizing response")
	text, err := model.ExtractTextContent(msg.Content)
	if err != nil {
		return "", nil, "", err
	}
	if text == "" && strings.TrimSpace(msg.Reasoning) != "" {
		text = msg.Reasoning
	}
	sess.Conversation.AddAssistantMessageWithReasoningDetails(text, msg.Reasoning, msg.ReasoningDetails)
	c.saveLatestConversationMessage(sess)
	c.setAwaitingToolLoopDecision(sess, false)
	return text, &totalUsage, finishReason, nil
}

func normalizeToolLoopCalls(registry *tool.Registry, calls []model.ToolCall, allowedTools []string) []model.ToolCall {
	for i := range calls {
		if calls[i].ID == "" {
			calls[i].ID = fmt.Sprintf("tool-%d", i+1)
		}
		if repairedName, ok := resolveToolCallName(registry, calls[i].Function.Name, allowedTools); ok {
			calls[i].Function.Name = repairedName
		}
	}
	return calls
}

func (c *Controller) recordToolLoopCalls(sess *SessionState, calls []model.ToolCall, msg model.Message) {
	c.app.SetStatus(fmt.Sprintf("Model requested %d tool call(s)", len(calls)))
	sess.Conversation.AddToolCallMessageWithReasoning(calls, msg.Reasoning, msg.ReasoningDetails)
	c.saveLatestConversationMessage(sess)
}

func (c *Controller) executeToolLoopCalls(sess *SessionState, calls []model.ToolCall, allowedTools []string) {
	for i, tc := range calls {
		c.executeToolLoopCall(sess, tc, i+1, len(calls), allowedTools)
	}
}

func (c *Controller) executeToolLoopCall(sess *SessionState, tc model.ToolCall, index, total int, allowedTools []string) {
	params, err := parseToolParams(tc.Function.Arguments)
	if err != nil {
		c.addToolLoopResponse(sess, tc, fmt.Sprintf("Error: invalid tool arguments: %v", err))
		return
	}
	if sess.ToolRegistry == nil {
		c.addToolLoopResponse(sess, tc, "Error: tool registry unavailable")
		return
	}
	if !tool.IsToolAllowed(tc.Function.Name, allowedTools) {
		c.addToolLoopResponse(sess, tc, fmt.Sprintf("Error: tool %s not allowed by active skills", tc.Function.Name))
		return
	}
	if params == nil {
		params = make(map[string]any)
	}
	if tc.ID != "" {
		params[tool.ToolCallIDParam] = tc.ID
	}

	c.app.StartProcessStatus(fmt.Sprintf("Running %s (%d/%d)", compactStatusText(tc.Function.Name, 36), index, total))
	result, execErr := sess.ToolRegistry.Execute(tc.Function.Name, params)
	c.app.StopProcessStatus()
	c.addToolLoopResponse(sess, tc, formatToolResultForModel(result, execErr))

	if display := toolDisplayMessage(tc.Function.Name, result, execErr); display != "" {
		c.app.AddMessage(display, "system")
	}
}

func (c *Controller) addToolLoopResponse(sess *SessionState, tc model.ToolCall, text string) {
	sess.Conversation.AddToolResponseMessage(tc.ID, tc.Function.Name, text)
	c.saveLatestConversationMessage(sess)
}

func (c *Controller) checkpointToolLoop(sess *SessionState, totalUsage model.Usage, maxIterations int) (string, *model.Usage, string, error) {
	checkpoint := maxToolIterationsCheckpoint(maxIterations)
	sess.Conversation.AddAssistantMessage(checkpoint)
	c.saveLatestConversationMessage(sess)
	c.setAwaitingToolLoopDecision(sess, true)
	return checkpoint, &totalUsage, toolLoopCheckpointFinishReason, nil
}

func modelProcessStatus(modelID string, iteration, maxIterations, toolCount int, reasoning *model.ReasoningConfig) string {
	phase := "Thinking with " + compactStatusText(modelID, 44)
	if iteration > 0 {
		phase = "Thinking after tools with " + compactStatusText(modelID, 34)
	}
	var details []string
	if maxIterations > 0 {
		details = append(details, fmt.Sprintf("round %d/%d", iteration+1, maxIterations))
	}
	if toolCount > 0 {
		details = append(details, fmt.Sprintf("%d tools", toolCount))
	}
	if reasoning != nil && strings.TrimSpace(reasoning.Effort) != "" {
		details = append(details, "reasoning "+strings.TrimSpace(reasoning.Effort))
	}
	if len(details) > 0 {
		phase += " - " + strings.Join(details, ", ")
	}
	return phase
}

func maxToolIterationsCheckpoint(maxIterations int) string {
	return fmt.Sprintf("I reached Buckley's interactive checkpoint after %d model/tool rounds without a final answer.\n\nReply with one of:\n- continue: keep going with tools\n- continue without tools: synthesize from the current context only\n- stop: leave this session here", maxIterations)
}

func modelFinishReasonNotice(reason string) string {
	trimmed := strings.TrimSpace(reason)
	switch strings.ToLower(trimmed) {
	case "", "stop", "tool_calls", toolLoopCheckpointFinishReason:
		return ""
	case "length", "max_tokens", "max_output_tokens", "token_limit":
		return "Response stopped because the provider reported finish_reason=" + trimmed + ", which usually means the output token limit was reached. Ask Buckley to continue, reduce context, or raise the chat max_tokens setting."
	case "content_filter", "safety":
		return "Response stopped because the provider reported finish_reason=" + trimmed + "."
	default:
		return fmt.Sprintf("Response stopped with provider finish_reason=%q.", trimmed)
	}
}

func readyStatusForFinishReason(reason string) string {
	if strings.EqualFold(strings.TrimSpace(reason), toolLoopCheckpointFinishReason) {
		return "Ready - needs direction"
	}
	if isTokenLimitFinishReason(reason) {
		return "Ready - output token limit reached"
	}
	return "Ready"
}

func shouldDisableToolsForPrompt(prompt string) bool {
	lower := strings.ToLower(strings.TrimSpace(prompt))
	if lower == "" {
		return false
	}
	normalized := strings.Trim(lower, " \t\r\n.!?")
	switch normalized {
	case "no tools", "without tools", "with no tools", "tools off", "tool-free":
		return true
	}
	if strings.Contains(lower, "tools off") {
		return true
	}

	toolFreePhrases := []string{
		"without tools",
		"with no tools",
		"no tools",
		"tool-free",
	}
	for _, phrase := range toolFreePhrases {
		if strings.Contains(lower, phrase) && containsToolFreeDirective(lower) {
			return true
		}
	}
	return false
}

func containsToolFreeDirective(prompt string) bool {
	directives := []string{
		"continue",
		"proceed",
		"answer",
		"respond",
		"synthesize",
		"summarize",
		"finish",
		"follow-up",
		"follow up",
		"this one",
	}
	for _, directive := range directives {
		if strings.Contains(prompt, directive) {
			return true
		}
	}
	return false
}

func isStopToolLoopDecision(prompt string) bool {
	lower := strings.ToLower(strings.TrimSpace(prompt))
	return lower == "stop" || lower == "no" || lower == "leave it" || lower == "leave it here"
}

func (c *Controller) consumeDisableToolsNextTurn(sess *SessionState) bool {
	if c == nil || sess == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	disable := sess.DisableToolsNextTurn
	sess.DisableToolsNextTurn = false
	return disable
}

func (c *Controller) setAwaitingToolLoopDecision(sess *SessionState, awaiting bool) {
	if c == nil || sess == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	sess.AwaitingToolLoopDecision = awaiting
}

func isTokenLimitFinishReason(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "length", "max_tokens", "max_output_tokens", "token_limit":
		return true
	default:
		return false
	}
}

func compactStatusText(text string, maxLen int) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "model"
	}
	if maxLen <= 0 || len(text) <= maxLen {
		return text
	}
	if maxLen <= 3 {
		return text[:maxLen]
	}
	return text[:maxLen-3] + "..."
}

func parseToolParams(raw string) (map[string]any, error) {
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

func formatToolResultForModel(result *builtin.Result, execErr error) string {
	if execErr != nil {
		return fmt.Sprintf("Error: %v", execErr)
	}
	if result == nil {
		return "No result"
	}
	encoded, err := tool.ToJSON(modelFacingToolResult(result))
	if err != nil {
		return fmt.Sprintf("{\"success\":%t}", result.Success)
	}
	return truncateModelToolOutput(encoded, defaultTUIToolModelMaxBytes)
}

func modelFacingToolResult(result *builtin.Result) *builtin.Result {
	if result == nil {
		return nil
	}
	if !result.ShouldAbridge || len(result.DisplayData) == 0 {
		return result
	}
	abridged := *result
	abridged.Data = cloneAnyMap(result.DisplayData)
	abridged.DisplayData = nil
	abridged.ShouldAbridge = false
	return &abridged
}

func cloneAnyMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]any, len(input))
	for k, v := range input {
		output[k] = v
	}
	return output
}

func truncateModelToolOutput(content string, maxBytes int) string {
	if maxBytes <= 0 || len(content) <= maxBytes {
		return content
	}
	marker := fmt.Sprintf("\n\n... tool output truncated for chat context (%d bytes omitted). Ask for a narrower query/path or inspect a specific file range. ...\n\n", len(content)-maxBytes)
	if len(marker) >= maxBytes {
		return takePrefixBytes(marker, maxBytes)
	}
	available := maxBytes - len(marker)
	headBytes := available * 2 / 3
	tailBytes := available - headBytes
	return takePrefixBytes(content, headBytes) + marker + takeSuffixBytes(content, tailBytes)
}

func toolDisplayMessage(name string, result *builtin.Result, execErr error) string {
	if execErr != nil {
		return fmt.Sprintf("Error running %s: %v", name, execErr)
	}
	if result == nil {
		return ""
	}
	if !result.Success {
		if result.Error != "" {
			return fmt.Sprintf("Error: %s", result.Error)
		}
		return "Error"
	}
	if name == "activate_skill" {
		if msg, ok := result.Data["message"].(string); ok && msg != "" {
			return msg
		}
	}
	if msg, ok := result.DisplayData["message"].(string); ok && msg != "" {
		return msg
	}
	if summary, ok := result.DisplayData["summary"].(string); ok && summary != "" {
		return summary
	}
	return ""
}

func addUsage(total model.Usage, next model.Usage) model.Usage {
	total.PromptTokens += next.PromptTokens
	total.CompletionTokens += next.CompletionTokens
	total.TotalTokens += next.TotalTokens
	return total
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

func resolveToolCallName(registry *tool.Registry, name string, allowed []string) (string, bool) {
	name = strings.TrimSpace(name)
	if registry == nil || name == "" {
		return name, false
	}
	if _, ok := registry.Get(name); ok && tool.IsToolAllowed(name, allowed) {
		return name, true
	}
	for _, candidate := range registry.List() {
		if candidate == nil {
			continue
		}
		candidateName := candidate.Name()
		if strings.EqualFold(candidateName, name) && tool.IsToolAllowed(candidateName, allowed) {
			return candidateName, true
		}
	}
	return name, false
}
