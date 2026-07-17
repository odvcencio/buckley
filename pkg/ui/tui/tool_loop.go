package tui

import (
	"context"
	"fmt"
	"strings"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
	"m31labs.dev/buckley/pkg/toolrunner"
)

// toolLoopMaxToolsPhase1 is set high so the tool runner's optional LLM
// tool-selection pre-pass never fires for interactive turns: the tool set has
// already been reduced by Arbiter governance + active skills, and that decision
// must stay authoritative.
const toolLoopMaxToolsPhase1 = 4096

type toolLoopState struct {
	useTools bool
}

// runToolLoop drives one interactive assistant turn through the shared streaming
// tool loop in pkg/toolrunner. Converging the TUI onto that loop (rather than a
// separate non-streaming re-implementation) is what makes chat behave the same
// as the agent/one-shot surfaces: live token streaming, inline tool-call token
// parsing (Kimi/GLM), preserved preamble content, reasoning continuity, and the
// hardened SSE path all come for free. The handler renders live and persists the
// full turn; a TUI tool executor keeps governance, tool-name repair, per-tool
// display, and model-facing truncation.
func (c *Controller) runToolLoop(ctx context.Context, sess *SessionState, modelID string) (string, *model.Usage, string, error) {
	if err := c.validateToolLoopInputs(sess); err != nil {
		return "", nil, "", err
	}

	state := c.newToolLoopState(sess, modelID)
	allowedTools := toolLoopAllowedTools(sess)

	var governedNames []string
	if state.useTools && sess.ToolRegistry != nil {
		governedNames = tool.GovernedToolNames(sess.ToolRegistry, c.evaluator, "interactive", "coding", allowedTools, 0)
		if len(governedNames) == 0 {
			// A governance decision of "no tools" must not be sent as an empty
			// allow-list, which the runner reads as "all tools".
			state.useTools = false
		}
	}

	var reasoning *model.ReasoningConfig
	if effort := model.ResolveReasoningEffort(c.cfg, c.modelMgr, c.rulesEngine, modelID, "execution"); effort != "" {
		reasoning = &model.ReasoningConfig{Effort: effort}
	}

	handler := newTUIStreamHandler(c, sess)

	result, err := c.runToolRunner(ctx, sess, modelID, governedNames, reasoning, state.useTools, handler)
	if err != nil && state.useTools {
		// Reactive fallback: some models/providers reject tools outright. Retry
		// once with tools off, mirroring the pre-convergence behavior.
		if retryErr := c.handleToolLoopModelError(err, &state); retryErr == nil {
			handler.reset()
			result, err = c.runToolRunner(ctx, sess, modelID, nil, reasoning, false, handler)
		}
	}
	handler.finish()
	if err != nil {
		return "", nil, "", err
	}
	if result == nil {
		result = &toolrunner.Result{}
	}

	usage := result.Usage
	if result.FinishReason == toolrunner.MaxIterationsFinishReason {
		return c.checkpointToolLoop(sess, usage, interactiveMaxToolIterations)
	}

	text := result.Content
	if strings.TrimSpace(text) == "" {
		// Some reasoning models return their answer in the reasoning channel
		// with empty content; surface it rather than showing nothing.
		if strings.TrimSpace(result.Reasoning) != "" && !handler.streamedAny {
			text = result.Reasoning
			c.app.AddMessage(text, "assistant")
		} else if !handler.streamedAny {
			c.app.AddMessage("(empty response from model)", "system")
		}
	}

	c.setAwaitingToolLoopDecision(sess, false)
	return text, &usage, result.FinishReason, nil
}

// runToolRunner builds and runs a tool runner for a single interactive turn.
func (c *Controller) runToolRunner(ctx context.Context, sess *SessionState, modelID string, governedNames []string, reasoning *model.ReasoningConfig, useTools bool, handler *tuiStreamHandler) (*toolrunner.Result, error) {
	registry := sess.ToolRegistry
	if !useTools {
		// Run with no tools by handing the runner an empty registry, so it emits
		// zero tool definitions. (An empty AllowedTools list means "all tools".)
		registry = tool.NewRegistry(tool.WithBuiltinFilter(func(tool.Tool) bool { return false }))
		governedNames = nil
	}

	runner, err := toolrunner.New(toolrunner.Config{
		Models:               c.modelMgr,
		Registry:             registry,
		ToolExecutor:         c.newTUIToolExecutor(sess, governedNames),
		EnableReasoning:      true,
		DefaultMaxIterations: interactiveMaxToolIterations,
		MaxToolsPhase1:       toolLoopMaxToolsPhase1,
	})
	if err != nil {
		return nil, err
	}
	runner.SetStreamHandler(handler)

	return runner.Run(ctx, toolrunner.Request{
		Messages:      c.buildMessagesForSession(sess),
		AllowedTools:  governedNames,
		MaxIterations: interactiveMaxToolIterations,
		Model:         modelID,
		Reasoning:     reasoning,
	})
}

// newTUIToolExecutor returns a tool executor that keeps the interactive surface's
// tool-name repair, skill/governance allow-check, per-tool display messages, and
// model-facing output truncation while executing through the shared runner.
func (c *Controller) newTUIToolExecutor(sess *SessionState, allowed []string) toolrunner.ToolExecutor {
	return func(ctx context.Context, call model.ToolCall, args map[string]any, _ map[string]tool.Tool) (toolrunner.ToolExecutionResult, error) {
		if sess.ToolRegistry == nil {
			msg := "Error: tool registry unavailable"
			return toolrunner.ToolExecutionResult{Result: msg, Error: msg}, nil
		}

		name := call.Function.Name
		if repaired, ok := resolveToolCallName(sess.ToolRegistry, name, allowed); ok {
			name = repaired
		}
		if !tool.IsToolAllowed(name, allowed) {
			msg := fmt.Sprintf("Error: tool %s not allowed by active skills", name)
			return toolrunner.ToolExecutionResult{Result: msg, Error: msg}, nil
		}
		if args == nil {
			args = make(map[string]any)
		}

		result, execErr := sess.ToolRegistry.ExecuteWithContext(ctx, name, args)
		if display := toolDisplayMessage(name, result, execErr); display != "" {
			c.app.AddMessage(display, "system")
		}

		errStr := ""
		switch {
		case execErr != nil:
			errStr = execErr.Error()
		case result != nil && !result.Success:
			errStr = result.Error
		}
		return toolrunner.ToolExecutionResult{
			Result:  formatToolResultForModel(result, execErr),
			Error:   errStr,
			Success: execErr == nil && result != nil && result.Success,
		}, nil
	}
}

// tuiStreamHandler renders a streaming interactive turn and persists it. It
// implements toolrunner.StreamHandler and toolrunner.TurnObserver.
type tuiStreamHandler struct {
	c           *Controller
	sess        *SessionState
	bubbleOpen  bool
	streamedAny bool
	sawEvent    bool
}

func newTUIStreamHandler(c *Controller, sess *SessionState) *tuiStreamHandler {
	return &tuiStreamHandler{c: c, sess: sess}
}

func (h *tuiStreamHandler) firstEvent() {
	if !h.sawEvent {
		h.sawEvent = true
		h.c.app.RemoveThinkingIndicator()
	}
}

// openBubble seeds an empty assistant message so streamed deltas append into it
// (StreamChunk deltas append to the last message via the coalescer).
func (h *tuiStreamHandler) openBubble() {
	if !h.bubbleOpen {
		h.c.app.AddMessage("", "assistant")
		h.bubbleOpen = true
	}
}

func (h *tuiStreamHandler) closeBubble() {
	if h.bubbleOpen {
		h.c.app.StreamEnd(h.sess.ID, "")
		h.bubbleOpen = false
	}
}

func (h *tuiStreamHandler) OnText(text string) {
	if text == "" {
		return
	}
	h.firstEvent()
	h.openBubble()
	h.streamedAny = true
	h.c.app.StreamChunk(h.sess.ID, text)
}

func (h *tuiStreamHandler) OnReasoning(string) { h.firstEvent() }

func (h *tuiStreamHandler) OnReasoningEnd() {}

func (h *tuiStreamHandler) OnToolStart(name string, _ string) {
	h.firstEvent()
	// Finalize any preamble bubble before tool-activity messages appear.
	h.closeBubble()
	h.c.app.StartProcessStatus(fmt.Sprintf("Running %s", compactStatusText(name, 36)))
}

func (h *tuiStreamHandler) OnToolEnd(string, string, error) {
	h.c.app.StopProcessStatus()
}

func (h *tuiStreamHandler) OnError(error) {}

func (h *tuiStreamHandler) OnComplete(*toolrunner.Result) {
	h.closeBubble()
}

// OnTurnMessage persists each produced message into the session conversation,
// incrementally, so the transcript survives a crash mid-turn and feeds correct
// wire history back to the model on the next turn.
func (h *tuiStreamHandler) OnTurnMessage(msg model.Message) {
	if h.sess == nil || h.sess.Conversation == nil {
		return
	}
	switch {
	case msg.Role == "assistant" && len(msg.ToolCalls) > 0:
		h.sess.Conversation.AddToolCallMessageWithContent(model.ExtractTextContentOrEmpty(msg.Content), msg.ToolCalls, msg.Reasoning, msg.ReasoningDetails)
	case msg.Role == "tool":
		h.sess.Conversation.AddToolResponseMessage(msg.ToolCallID, msg.Name, model.ExtractTextContentOrEmpty(msg.Content))
	case msg.Role == "assistant":
		h.sess.Conversation.AddAssistantMessageWithReasoningDetails(model.ExtractTextContentOrEmpty(msg.Content), msg.Reasoning, msg.ReasoningDetails)
	default:
		return
	}
	h.c.saveLatestConversationMessage(h.sess)
}

// reset drops any partial bubble before a retry-without-tools re-run.
func (h *tuiStreamHandler) reset() {
	h.closeBubble()
	h.streamedAny = false
}

// finish flushes any open bubble once the turn is complete.
func (h *tuiStreamHandler) finish() {
	h.closeBubble()
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

func (c *Controller) handleToolLoopModelError(err error, state *toolLoopState) error {
	if state != nil && state.useTools && isToolUnsupportedError(err) {
		c.app.SetStatus("Retrying without tools")
		state.useTools = false
		return nil
	}
	return err
}

func toolLoopAllowedTools(sess *SessionState) []string {
	if sess == nil || sess.SkillState == nil {
		return nil
	}
	return sess.SkillState.ToolFilter()
}

func (c *Controller) checkpointToolLoop(sess *SessionState, totalUsage model.Usage, maxIterations int) (string, *model.Usage, string, error) {
	checkpoint := maxToolIterationsCheckpoint(maxIterations)
	sess.Conversation.AddAssistantMessage(checkpoint)
	c.saveLatestConversationMessage(sess)
	c.app.AddMessage(checkpoint, "assistant")
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
