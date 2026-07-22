package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

type toolLoopState struct {
	useTools   bool
	totalUsage model.Usage
	progress   toolLoopProgress
}

type toolLoopProgress struct {
	started       bool
	reasoningOpen bool
	reasoning     strings.Builder
}

type toolLoopIterationResult struct {
	done         bool
	message      model.Message
	finishReason string
}

func (c *Controller) runToolLoop(ctx context.Context, sess *SessionState, modelID string) (string, *model.Usage, string, error) {
	if err := c.validateToolLoopInputs(sess); err != nil {
		return "", nil, "", err
	}

	state := c.newToolLoopState(sess, modelID)
	for iter := 0; ; iter++ {
		result, err := c.runToolLoopIteration(ctx, sess, modelID, iter, &state)
		if err != nil {
			return "", nil, "", err
		}
		if result.done {
			return c.finishToolLoopResponse(sess, result.message, state.totalUsage, result.finishReason)
		}
	}
}

func (c *Controller) validateToolLoopInputs(sess *SessionState) error {
	if c.modelMgr == nil {
		return fmt.Errorf("model manager unavailable")
	}
	if sess == nil || sess.Conversation == nil {
		return fmt.Errorf("session unavailable")
	}
	return nil
}

func (c *Controller) newToolLoopState(sess *SessionState, modelID string) toolLoopState {
	return toolLoopState{
		useTools: !c.consumeDisableToolsNextTurn(sess) &&
			sess.ToolRegistry != nil &&
			c.modelMgr.SupportsTools(modelID),
	}
}

func (c *Controller) runToolLoopIteration(ctx context.Context, sess *SessionState, modelID string, iteration int, state *toolLoopState) (toolLoopIterationResult, error) {
	if ctx.Err() != nil {
		return toolLoopIterationResult{}, ctx.Err()
	}

	allowedTools := toolLoopAllowedTools(sess)
	req, nextUseTools := c.buildToolLoopRequest(sess, modelID, state.useTools, allowedTools)
	state.useTools = nextUseTools

	resp, err := c.callToolLoopModel(ctx, req, modelID, iteration, state)
	if err != nil {
		return toolLoopIterationResult{}, c.handleToolLoopModelError(err, state)
	}
	state.totalUsage = addUsage(state.totalUsage, resp.Usage)

	choice, err := firstToolLoopChoice(req, resp)
	if err != nil {
		return toolLoopIterationResult{}, err
	}
	return c.handleToolLoopChoice(sess, choice.Message, allowedTools, choice.FinishReason, state), nil
}

func (c *Controller) handleToolLoopModelError(err error, state *toolLoopState) error {
	if state != nil && state.useTools && isToolUnsupportedError(err) {
		c.app.SetStatus("Retrying without tools")
		state.useTools = false
		return nil
	}
	return err
}

func (c *Controller) handleToolLoopChoice(sess *SessionState, msg model.Message, allowedTools []string, finishReason string, state *toolLoopState) toolLoopIterationResult {
	if len(msg.ToolCalls) == 0 {
		return toolLoopIterationResult{
			done:         true,
			message:      msg,
			finishReason: finishReason,
		}
	}

	toolCalls := normalizeToolLoopCalls(sess.ToolRegistry, msg.ToolCalls, allowedTools)
	c.recordToolLoopCalls(sess, toolCalls, msg)
	c.executeToolLoopCalls(sess, toolCalls, allowedTools, state)
	return toolLoopIterationResult{}
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
			if c.modelMgr != nil && c.modelMgr.SupportsParameter(modelID, "parallel_tool_calls") {
				sequential := false
				req.ParallelToolCalls = &sequential
			}
		} else {
			useTools = false
		}
	}

	if c.modelMgr != nil && c.modelMgr.SupportsReasoning(modelID) {
		exclude := false
		req.Reasoning = &model.ReasoningConfig{Exclude: &exclude}
		if effort := model.ResolveReasoningEffort(c.cfg, c.modelMgr, c.rulesEngine, modelID, "execution"); effort != "" {
			req.Reasoning.Effort = effort
		} else {
			enabled := true
			req.Reasoning.Enabled = &enabled
		}
	}
	if c.modelMgr != nil && c.modelMgr.SupportsParameter(modelID, "include_reasoning") {
		include := true
		req.IncludeReasoning = &include
	}
	return req, useTools
}

func (c *Controller) callToolLoopModel(ctx context.Context, req model.ChatRequest, modelID string, iteration int, state *toolLoopState) (*model.ChatResponse, error) {
	c.app.StartProcessStatus(modelProcessStatus(modelID, iteration, len(req.Tools), req.Reasoning))
	defer c.app.StopProcessStatus()

	chunks, errs := c.modelMgr.ChatCompletionStream(ctx, req)
	accumulator := model.AcquireStreamAccumulator()
	defer model.ReleaseStreamAccumulator(accumulator)
	if state != nil {
		state.progress.reasoningOpen = false
		state.progress.reasoning.Reset()
	}

	var responseID string
	var responseModel string
	var finishReason string
	receivedChoice := false
	for chunks != nil || errs != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case chunk, ok := <-chunks:
			if !ok {
				chunks = nil
				continue
			}
			if chunk.ID != "" {
				responseID = chunk.ID
			}
			if chunk.Model != "" {
				responseModel = chunk.Model
			}
			accumulator.Add(chunk)
			for _, choice := range chunk.Choices {
				receivedChoice = true
				c.appendReasoningProgress(state, choice.Delta)
				if choice.FinishReason != nil {
					finishReason = *choice.FinishReason
				}
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				return nil, err
			}
		}
	}

	message := accumulator.FinalizeWithTokenParsing()
	if !receivedChoice {
		return nil, model.NoResponseChoicesError(req, &model.ChatResponse{ID: responseID, Model: responseModel})
	}
	if message.Role == "" {
		message.Role = "assistant"
	}
	usage := model.Usage{}
	if streamedUsage := accumulator.Usage(); streamedUsage != nil {
		usage = *streamedUsage
	}
	return &model.ChatResponse{
		ID:    responseID,
		Model: responseModel,
		Choices: []model.Choice{{
			Message:      message,
			FinishReason: finishReason,
		}},
		Usage: usage,
	}, nil
}

func (c *Controller) appendReasoningProgress(state *toolLoopState, delta model.MessageDelta) {
	if c == nil || c.app == nil || state == nil {
		return
	}
	text := delta.Reasoning
	if strings.TrimSpace(text) == "" {
		text = visibleReasoningDetails(delta.ReasoningDetails)
	}
	if text == "" {
		return
	}
	state.progress.reasoning.WriteString(text)
	display := "Thinking\n\n" + model.NormalizeReasoningText(state.progress.reasoning.String())
	if !state.progress.started {
		state.progress.started = true
	}
	if !state.progress.reasoningOpen {
		c.app.AddMessage(display, "thinking")
		state.progress.reasoningOpen = true
		return
	}
	c.app.ReplaceLastMessage(display)
}

func visibleReasoningDetails(details []model.ReasoningDetail) string {
	var b strings.Builder
	for _, detail := range details {
		if detail.Text != "" {
			b.WriteString(detail.Text)
		} else if detail.Summary != "" {
			b.WriteString(detail.Summary)
		}
	}
	return b.String()
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

func (c *Controller) executeToolLoopCalls(sess *SessionState, calls []model.ToolCall, allowedTools []string, state *toolLoopState) {
	for i, tc := range calls {
		c.executeToolLoopCall(sess, tc, i+1, len(calls), allowedTools, state)
	}
}

func (c *Controller) executeToolLoopCall(sess *SessionState, tc model.ToolCall, index, total int, allowedTools []string, state *toolLoopState) {
	c.appendToolCallProgress(state, tc)
	params, err := parseToolParams(tc.Function.Arguments)
	if err != nil {
		message := fmt.Sprintf("Error: invalid tool arguments: %v", err)
		c.appendToolResultProgress(state, tc.Function.Name, nil, fmt.Errorf("invalid arguments: %w", err))
		c.addToolLoopResponse(sess, tc, message)
		return
	}
	if sess.ToolRegistry == nil {
		c.appendToolResultProgress(state, tc.Function.Name, nil, fmt.Errorf("tool registry unavailable"))
		c.addToolLoopResponse(sess, tc, "Error: tool registry unavailable")
		return
	}
	if !tool.IsToolAllowed(tc.Function.Name, allowedTools) {
		err := fmt.Errorf("tool %s not allowed by active skills", tc.Function.Name)
		c.appendToolResultProgress(state, tc.Function.Name, nil, err)
		c.addToolLoopResponse(sess, tc, "Error: "+err.Error())
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
	c.appendToolResultProgress(state, tc.Function.Name, result, execErr)
	c.addToolLoopResponse(sess, tc, formatToolResultForModel(result, execErr))
}

func (c *Controller) addToolLoopResponse(sess *SessionState, tc model.ToolCall, text string) {
	sess.Conversation.AddToolResponseMessage(tc.ID, tc.Function.Name, text)
	c.saveLatestConversationMessage(sess)
}

func (c *Controller) appendToolCallProgress(state *toolLoopState, tc model.ToolCall) {
	if c == nil || c.app == nil || state == nil {
		return
	}
	if !state.progress.started {
		state.progress.started = true
	}
	state.progress.reasoningOpen = false
	c.app.AddMessage(toolCallProgressBlock(tc), "tool")
}

func (c *Controller) appendToolResultProgress(state *toolLoopState, name string, result *builtin.Result, execErr error) {
	if c == nil || c.app == nil || state == nil {
		return
	}
	if !state.progress.started {
		state.progress.started = true
	}
	c.app.AppendToLastMessage("\n\n" + toolResultProgressSummary(name, result, execErr))
}

func toolCallProgressBlock(tc model.ToolCall) string {
	name := strings.TrimSpace(tc.Function.Name)
	if name == "" {
		name = "tool"
	}
	detail := compactToolArguments(tc.Function.Arguments, 600)
	if detail == "" || detail == "{}" {
		return "→ " + name
	}
	return "→ " + name + "\n\n" + detail
}

func toolCallProgressSummary(tc model.ToolCall) string {
	name := strings.TrimSpace(tc.Function.Name)
	if name == "" {
		name = "tool"
	}
	arguments := compactToolArguments(tc.Function.Arguments, 180)
	if arguments == "" || arguments == "{}" {
		return name
	}
	return name + " — " + arguments
}

func compactToolArguments(raw string, maxLen int) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var params map[string]any
	if json.Unmarshal([]byte(raw), &params) == nil && len(params) > 0 {
		keys := make([]string, 0, len(params))
		for key := range params {
			if key != tool.ToolCallIDParam {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, "- "+key+": "+compactToolArgumentValue(params[key]))
		}
		raw = strings.Join(parts, "\n")
	}
	return compactMultilineText(raw, maxLen)
}

func compactToolArgumentValue(value any) string {
	if text, ok := value.(string); ok {
		text = strings.TrimSpace(text)
		text = strings.ReplaceAll(text, "\r\n", " ↵ ")
		text = strings.ReplaceAll(text, "\n", " ↵ ")
		return strings.Join(strings.Fields(text), " ")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "<unavailable>"
	}
	return string(encoded)
}

func compactMultilineText(text string, maxLen int) string {
	text = strings.TrimSpace(text)
	if maxLen <= 0 || len(text) <= maxLen {
		return text
	}
	if maxLen <= 3 {
		return text[:maxLen]
	}
	return strings.TrimSpace(text[:maxLen-3]) + "..."
}

func toolResultProgressSummary(name string, result *builtin.Result, execErr error) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "tool"
	}
	if execErr != nil {
		return "✗ " + name + " — " + compactStatusText(execErr.Error(), 200)
	}
	if result == nil {
		return "✗ " + name + " — no result returned"
	}
	if !result.Success {
		detail := strings.TrimSpace(result.Error)
		if stderr, ok := result.Data["stderr"].(string); ok && strings.TrimSpace(stderr) != "" {
			detail = strings.TrimSpace(detail + ": " + strings.TrimSpace(stderr))
		}
		if detail == "" {
			detail = "failed"
		}
		return "✗ " + name + " — " + compactStatusText(detail, 200)
	}
	if display := strings.TrimSpace(toolDisplayMessage(name, result, nil)); display != "" {
		return "✓ " + name + " — " + compactStatusText(display, 200)
	}
	return "✓ " + name + " — completed"
}

func modelProcessStatus(modelID string, iteration, toolCount int, reasoning *model.ReasoningConfig) string {
	phase := "Thinking with " + compactStatusText(modelID, 44)
	if iteration > 0 {
		phase = "Thinking after tools with " + compactStatusText(modelID, 34)
	}
	var details []string
	details = append(details, fmt.Sprintf("round %d", iteration+1))
	if toolCount > 0 {
		details = append(details, fmt.Sprintf("%d tools", toolCount))
	}
	if reasoning != nil && strings.TrimSpace(reasoning.Effort) != "" {
		details = append(details, "reasoning "+strings.TrimSpace(reasoning.Effort))
	}
	details = append(details, "type to steer")
	if len(details) > 0 {
		phase += " - " + strings.Join(details, ", ")
	}
	return phase
}

func modelFinishReasonNotice(reason string) string {
	trimmed := strings.TrimSpace(reason)
	switch strings.ToLower(trimmed) {
	case "", "stop", "tool_calls":
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
