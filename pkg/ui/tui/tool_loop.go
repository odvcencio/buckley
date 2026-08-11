package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

type toolLoopState struct {
	useTools       bool
	totalUsage     model.Usage
	progress       toolLoopProgress
	governor       *agentloop.Governor
	guardReason    string
	contextScale   float64
	contextRetries int
	projection     conversation.ContextProjectionStats

	// lastProviderFinishReason is the raw finish_reason string the provider
	// reported on the most recent model call (e.g. "stop", "length",
	// "tool_calls"), captured by newToolLoopController's CallModel hook.
	// agentloop.Controller.Result.FinishReason is a different, Controller-
	// owned abstraction (empty on a normal completion) that never surfaces
	// the provider's own value, so runToolLoop reads this field instead
	// once Controller reports a normal completion -- modelFinishReasonNotice
	// and readyStatusForFinishReason both key off the provider's reason.
	lastProviderFinishReason string

	// continuation and useContinuation carry this turn's provider
	// continuation decision (decision 0001), behind the
	// models.provider_continuation flag. continuation is nil unless the
	// session is eligible and the flag is on.
	continuation     *model.ContinuationCoordinator
	useContinuation  bool
	codeModeRecovery *tool.CodeModeRecoveryState
}

// toolLoopResult keeps provider-owned completion metadata separate from a
// Buckley-owned harness stop. Conflating these two domains produced the
// misleading provider finish_reason="loop_guard" notice in the transcript.
type toolLoopResult struct {
	Text                 string
	Usage                *model.Usage
	ProviderFinishReason string
	HarnessStopReason    string
	Streamed             bool
}

type toolLoopProgress struct {
	started       bool
	reasoningOpen bool
	reasoning     strings.Builder

	// textOpen, textBuf, roundRendered, and roundRenderedText track this
	// round's live-streamed assistant text bubble. textOpen is true while a
	// seeded assistant message is open for StreamChunk appends; textBuf
	// mirrors the raw deltas sent so far. closeTextProgress sets
	// roundRendered once the bubble closes and roundRenderedText to
	// whatever text the transcript now shows for it -- textBuf's raw
	// stream, or the finalized message content if closeTextProgress had to
	// correct the bubble (inline tool-call-token models, where
	// FinalizeWithTokenParsing strips markup that already rendered live).
	// All four reset at the top of every callToolLoopModel call, so
	// finishToolLoopResponse can tell whether the message it is about to
	// persist already matches what's on screen (no double-render) or still
	// needs a discrete AddMessage (continuation turns, empty responses, and
	// the reasoning-channel fallback never stream).
	textOpen          bool
	textBuf           strings.Builder
	roundRendered     bool
	roundRenderedText string
}

// runToolLoop drives one interactive assistant turn through the shared turn
// engine (pkg/agentloop.Controller): it owns the round loop, the round
// ceiling, tool-call ID backfill, and Governor consultation that this
// package used to hand-roll, while the TUI-specific pieces --
// projection/continuation (buildToolLoopRequestWithState), the streaming
// model call (callToolLoopTurn), tool execution and progress rendering
// (toolLoopExecuteOne), and conversation persistence (recordToolLoopCalls,
// AddToolResponseMessage) -- stay exactly as they were, wired in as
// Controller hooks. See newToolLoopController.
func (c *Controller) runToolLoop(ctx context.Context, sess *SessionState, modelID string) (toolLoopResult, error) {
	if err := c.validateToolLoopInputs(sess); err != nil {
		return toolLoopResult{}, err
	}

	state := c.newToolLoopState(sess, modelID)
	allowedTools := toolLoopAllowedTools(sess)

	ctrl, err := c.newToolLoopController(sess, modelID, allowedTools, &state)
	if err != nil {
		return toolLoopResult{}, err
	}

	// accumulatedUsage sums every Controller.Run attempt's internally
	// accumulated usage, including attempts that later errored (a context-
	// length or tools-unsupported retry starts a fresh Result{} on the same
	// Controller/Governor, so usage from rounds that already succeeded
	// before the error would otherwise be lost). This mirrors the pre-engine
	// loop, where state.totalUsage accumulated across every outer-loop
	// iteration including retries.
	var accumulatedUsage model.Usage
	for {
		if ctx.Err() != nil {
			return toolLoopResult{}, ctx.Err()
		}
		result, err := ctrl.Run(ctx)
		if result != nil {
			accumulatedUsage = model.AddUsage(accumulatedUsage, result.Usage)
		}
		if err != nil {
			// The context/tools-unsupported fallbacks mutate state
			// (contextScale/contextRetries, useTools) and ask for a retry by
			// returning nil; Controller.Run is safe to call again on the
			// same instance since its per-round state lives in state and
			// state.governor, not in the Controller value itself. This
			// mirrors the pre-engine outer loop, where a recoverable error
			// simply continued to the next iteration on the same governor.
			if retryErr := c.handleToolLoopModelError(err, &state); retryErr == nil {
				continue
			}
			return toolLoopResult{}, err
		}

		switch result.FinishReason {
		case agentloop.FinishReasonLoopGuard:
			state.totalUsage = accumulatedUsage
			return c.finishGuardedToolLoop(ctx, sess, modelID, &state, result.GuardDecision.Reason)
		case agentloop.FinishReasonEmptyChoices:
			// callToolLoopModel already turns an empty stream response into
			// a hard error (model.NoResponseChoicesError) before it ever
			// reaches Controller, matching the pre-engine behavior where an
			// empty response failed the turn; this branch is defensive.
			return toolLoopResult{}, fmt.Errorf("model returned no response choices")
		}
		// result.FinishReason == "" here (Controller's own abstraction for a
		// normal completion); the provider's raw finish_reason -- what
		// modelFinishReasonNotice and readyStatusForFinishReason key off --
		// is state.lastProviderFinishReason, captured by the CallModel hook.
		return c.finishToolLoopResponse(sess, result.Message, accumulatedUsage, state.lastProviderFinishReason, &state)
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
		governor:         newInteractiveToolLoopGovernor(c.cfg),
		contextScale:     1,
		codeModeRecovery: &tool.CodeModeRecoveryState{},
	}
}

// continuationCoordinatorForSession lazily creates and caches this session's
// ContinuationCursor coordinator (decision 0001), restoring persisted state
// for the resolved provider/model on first use. It returns nil when the
// coordinator is unavailable (no model manager).
func (c *Controller) continuationCoordinatorForSession(sess *SessionState) *model.ContinuationCoordinator {
	if c == nil || sess == nil || c.modelMgr == nil {
		return nil
	}
	if sess.continuation == nil {
		var store model.ContinuationStore
		if c.store != nil {
			store = c.store
		}
		sess.continuation = model.NewContinuationCoordinator(c.modelMgr, store, sess.ID)
	}
	return sess.continuation
}

// continuationEligible reports whether this turn should attempt provider
// continuation: the opt-in flag is on, the resolved provider implements
// ContinuationClient, and it advertises support for modelID.
func (c *Controller) continuationEligible(modelID string) bool {
	if c == nil || c.cfg == nil || !c.cfg.Models.ProviderContinuation || c.modelMgr == nil {
		return false
	}
	return c.modelMgr.SupportsContinuation(modelID)
}

// newToolLoopController wires the shared turn engine (pkg/agentloop) for
// one interactive turn. Every hook delegates to the same TUI-specific logic
// this package always used, so the migration changes only who drives the
// round loop:
//
//   - BuildRequest re-derives and re-projects the request every round via
//     buildToolLoopRequestWithState (continuation pinning, the epoch rule,
//     and the context-scale retry all stay exactly as they were); it also
//     records the 1-based round number Controller assigns so CallModel can
//     reproduce the old 0-based "iteration" the status line expects.
//   - CallModel is callToolLoopTurn: the continuation-aware call for an
//     eligible round, or the streaming call otherwise. Repaired tool-call
//     names (normalizeToolLoopCalls) are applied here, before Controller
//     ever sees the response, so the corrected name is what gets persisted
//     to history and dispatched -- matching the pre-engine order exactly.
//   - DispatchTools executes each call via toolLoopExecuteOne, the same
//     execution/progress/formatting core executeToolLoopCall uses.
//   - History persists exactly what recordToolLoopCalls and
//     AddToolResponseMessage always did; Controller calls it once per
//     assistant tool-call message and once per tool result, never for the
//     turn's final answer (finishToolLoopResponse still owns that, so nothing
//     double-appends).
//   - Governor is state.governor directly, so Controller's round ceiling and
//     per-call Observe() consultation share the one instance this turn's
//     other guard-adjacent code (contextScale retries, finishGuardedToolLoop)
//     already references.
//
// The one behavior this intentionally does not preserve: a mid-batch guard
// stop no longer skips remaining calls in the same parallel tool-call
// batch, since Controller consults the Governor once per call only after
// Dispatch returns the whole batch's outcomes. Parallel tool calls are
// already suppressed via ParallelToolCalls=false whenever the provider
// advertises support for it, so this narrows to providers that both omit
// that parameter and have a model choose multiple calls in one turn.
func (c *Controller) newToolLoopController(sess *SessionState, modelID string, allowedTools []string, state *toolLoopState) (*agentloop.Controller, error) {
	currentRound := 0

	buildRequest := func(ctx context.Context, round int) (model.ChatRequest, error) {
		currentRound = round
		req, nextUseTools := c.buildToolLoopRequestWithState(sess, modelID, state.useTools, allowedTools, state)
		state.useTools = nextUseTools
		return req, nil
	}

	// useContinuation (Controller's parameter) is ignored: state.useContinuation,
	// set by buildToolLoopRequestWithState just above, already carries this
	// round's continuation decision, and callToolLoopTurn reads it directly.
	callModel := agentloop.ModelCallerFunc(func(ctx context.Context, req model.ChatRequest, _ bool) (*model.ChatResponse, error) {
		iteration := currentRound - 1
		if iteration < 0 {
			iteration = 0
		}
		resp, err := c.callToolLoopTurn(ctx, sess, modelID, iteration, req, state)
		if err != nil {
			return nil, err
		}
		if resp != nil && len(resp.Choices) > 0 {
			resp.Choices[0].Message.ToolCalls = normalizeToolLoopCalls(sess.ToolRegistry, resp.Choices[0].Message.ToolCalls, allowedTools)
			state.lastProviderFinishReason = resp.Choices[0].FinishReason
		}
		return resp, nil
	})

	dispatch := agentloop.ToolDispatcherFunc(func(ctx context.Context, calls []model.ToolCall) ([]agentloop.ToolOutcome, error) {
		outcomes := make([]agentloop.ToolOutcome, 0, len(calls))
		for i, tc := range calls {
			if ctx.Err() != nil {
				return outcomes, ctx.Err()
			}
			modelResult, result, execErr := c.toolLoopExecuteOne(ctx, sess, tc, i+1, len(calls), allowedTools, state)
			success := execErr == nil && result != nil && result.Success
			yield := tool.ResultYieldForTool(tc.Function.Name, result, execErr)
			outcomes = append(outcomes, agentloop.ToolOutcome{
				Content:       modelResult,
				Success:       success,
				YieldObserved: yield.Observed,
				YieldCount:    yield.Count,
				YieldUnit:     yield.Unit,
			})
		}
		return outcomes, nil
	})

	history := agentloop.HistorySinkFunc(func(msg model.Message) {
		switch {
		case msg.Role == "assistant" && len(msg.ToolCalls) > 0:
			c.recordToolLoopCalls(sess, msg.ToolCalls, msg)
		case msg.Role == "tool":
			sess.Conversation.AddToolResponseMessage(msg.ToolCallID, msg.Name, model.ExtractTextContentOrEmpty(msg.Content))
			c.saveLatestConversationMessage(sess)
		}
	})

	return agentloop.NewController(agentloop.ControllerConfig{
		Governor:      state.governor,
		Progress:      newInteractiveProgressController(c.cfg),
		BuildRequest:  buildRequest,
		CallModel:     callModel,
		DispatchTools: dispatch,
		History:       history,
		ContextWindow: func(mid string) int {
			if c.modelMgr == nil {
				return 0
			}
			window, _ := c.modelMgr.GetContextLength(mid)
			return window
		},
	})
}

// callToolLoopTurn executes one model turn, using the continuation-aware
// non-streaming path when this turn is eligible (decision 0001) and falling
// back to the normal streaming path otherwise. req already carries the full
// projected message list either way, so a continuation error can retry
// through the normal streaming path with the same request: a broken
// continuation never fails the turn.
func (c *Controller) callToolLoopTurn(ctx context.Context, sess *SessionState, modelID string, iteration int, req model.ChatRequest, state *toolLoopState) (*model.ChatResponse, error) {
	if !state.useContinuation || state.continuation == nil {
		return c.callToolLoopModel(ctx, req, modelID, iteration, sess, state)
	}

	// Predict hit/reset for the in-flight status line from whether the
	// cursor currently holds a represented prefix to reuse; Call below
	// records the actual outcome once it is known.
	state.projection.ContinuationHit = state.continuation.Active()
	status := modelProcessStatus(modelID, iteration, len(req.Tools), req.Reasoning)
	if projection := contextProjectionStatus(state.projection); projection != "" {
		status += ", " + projection
	}
	c.app.StartProcessStatus(status)
	resp, err := state.continuation.Call(ctx, req)
	c.app.StopProcessStatus()
	if err == nil {
		state.projection.ContinuationHit = state.continuation.Hit()
		return resp, nil
	}

	// Continuation broke; reset and retry once through the normal streaming
	// path rather than failing the turn.
	state.continuation.Reset()
	if c.app != nil {
		c.app.SetStatus("Continuation unavailable; retrying without it")
	}
	return c.callToolLoopModel(ctx, req, modelID, iteration, sess, state)
}

func (c *Controller) handleToolLoopModelError(err error, state *toolLoopState) error {
	if c.handleToolLoopContextError(err, state) {
		return nil
	}
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

func (c *Controller) buildToolLoopRequest(sess *SessionState, modelID string, useTools bool, allowedTools []string) (model.ChatRequest, bool) {
	return c.buildToolLoopRequestWithState(sess, modelID, useTools, allowedTools, nil)
}

func (c *Controller) buildToolLoopRequestWithState(sess *SessionState, modelID string, useTools bool, allowedTools []string, state *toolLoopState) (model.ChatRequest, bool) {
	useContinuation := state != nil && c.continuationEligible(modelID)
	var coordinator *model.ContinuationCoordinator
	if useContinuation {
		coordinator = c.continuationCoordinatorForSession(sess)
		useContinuation = coordinator != nil
	}

	req := model.ChatRequest{
		Model:     modelID,
		SessionID: sess.ID,
	}
	if useContinuation {
		req.Messages = c.buildContinuationMessagesForSession(sess)
	} else {
		req.Messages = c.buildMessagesForSession(sess)
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
	contextWindow := 0
	if c.modelMgr != nil {
		contextWindow, _ = c.modelMgr.GetContextLength(modelID)
	}
	scale := 1.0
	if state != nil && state.contextScale > 0 {
		scale = state.contextScale
	}

	pinnedFromIndex := 0
	if useContinuation {
		providerID := c.modelMgr.ProviderIDForModel(modelID)
		coordinator.Restore(providerID, modelID, req.Messages)
		pinnedFromIndex = coordinator.PinnedFromIndex()
	}

	rawMessages := req.Messages
	projected, projection := conversation.ProjectModelMessagesForRequestPinned(rawMessages, req, contextWindow, scale, pinnedFromIndex)
	if pinnedFromIndex > 0 && projection.ProjectedTokens > projection.BudgetTokens {
		// Epoch rule (decision 0001): projection cannot fit the request
		// without compacting inside the region the cursor represents. Reset
		// the cursor deliberately -- one full recompiled request -- so this
		// is an intentional epoch boundary rather than a fingerprint
		// mismatch discovered mid-turn.
		coordinator.Reset()
		pinnedFromIndex = 0
		projected, projection = conversation.ProjectModelMessagesForRequestPinned(rawMessages, req, contextWindow, scale, 0)
	}
	req.Messages = projected
	if state != nil {
		state.projection = projection
		state.useContinuation = useContinuation
		state.continuation = coordinator
	}
	return req, useTools
}

func (c *Controller) callToolLoopModel(ctx context.Context, req model.ChatRequest, modelID string, iteration int, sess *SessionState, state *toolLoopState) (*model.ChatResponse, error) {
	status := modelProcessStatus(modelID, iteration, len(req.Tools), req.Reasoning)
	if estimate := model.EstimateRequestTokens(req); estimate.Total >= 1000 {
		status += fmt.Sprintf(", ~%.1fk input", float64(estimate.Total)/1000)
	}
	if state != nil {
		if projection := contextProjectionStatus(state.projection); projection != "" {
			status += ", " + projection
		}
	}
	c.app.StartProcessStatus(status)
	defer c.app.StopProcessStatus()

	chunks, errs := c.modelMgr.ChatCompletionStream(ctx, req)
	accumulator := model.AcquireStreamAccumulator()
	defer model.ReleaseStreamAccumulator(accumulator)
	if state != nil {
		state.progress.reasoningOpen = false
		state.progress.reasoning.Reset()
		state.progress.textOpen = false
		state.progress.textBuf.Reset()
		state.progress.roundRendered = false
		state.progress.roundRenderedText = ""
	}

	var responseID string
	var responseModel string
	var finishReason string
	receivedChoice := false
	for chunks != nil || errs != nil {
		select {
		case <-ctx.Done():
			c.closeTextProgress(state, sess, "")
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
				c.appendTextProgress(state, sess, choice.Delta.Content)
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
				c.closeTextProgress(state, sess, "")
				return nil, err
			}
		}
	}

	message := accumulator.FinalizeWithTokenParsing()
	c.closeTextProgress(state, sess, model.ExtractTextContentOrEmpty(message.Content))
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

// appendTextProgress streams one assistant text delta into the transcript:
// it seeds an empty assistant bubble on the first non-empty delta this
// round (so StreamChunk has something to append into), then forwards the
// delta through the existing StreamChunk/coalescer machinery. text is
// buffered in state.progress.textBuf so closeTextProgress can detect a
// token-parsing correction (inline tool-call markup) once the round
// finishes.
func (c *Controller) appendTextProgress(state *toolLoopState, sess *SessionState, text string) {
	if c == nil || c.app == nil || state == nil || sess == nil || text == "" {
		return
	}
	if !state.progress.started {
		state.progress.started = true
	}
	// A new assistant bubble is about to become the last message; any
	// reasoning bubble that was open no longer is.
	state.progress.reasoningOpen = false
	if !state.progress.textOpen {
		c.app.AddMessage("", "assistant")
		state.progress.textOpen = true
	}
	state.progress.textBuf.WriteString(text)
	c.app.StreamChunk(sess.ID, text)
}

// closeTextProgress finalizes this round's streamed assistant bubble, if
// one is open. finalText is the round's finalized message content (post
// token-parsing); when it differs from what was actually streamed -- the
// inline tool-call-token models, where FinalizeWithTokenParsing strips
// markup that already rendered live -- the bubble is corrected in place
// before it closes, so no double-render still yields a transcript that
// matches persisted history exactly.
func (c *Controller) closeTextProgress(state *toolLoopState, sess *SessionState, finalText string) {
	if c == nil || c.app == nil || state == nil || !state.progress.textOpen {
		return
	}
	rendered := state.progress.textBuf.String()
	if finalText != "" && finalText != rendered {
		c.app.ReplaceLastMessage(finalText)
		rendered = finalText
	}
	if sess != nil {
		c.app.StreamEnd(sess.ID, "")
	}
	state.progress.textOpen = false
	state.progress.roundRendered = true
	state.progress.roundRenderedText = rendered
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

// finishToolLoopResponse persists the turn's final assistant message and
// reports whether it was already rendered live: state.progress reflects the
// most recent callToolLoopModel call, which streams and closes its bubble
// before returning, so a match here means the transcript already shows text
// identical to what's being persisted (no double-render needed). A
// continuation turn, an empty response, or the reasoning-channel fallback
// never streamed, so the caller still renders those with a discrete
// AddMessage.
func (c *Controller) finishToolLoopResponse(sess *SessionState, msg model.Message, totalUsage model.Usage, providerFinishReason string, state *toolLoopState) (toolLoopResult, error) {
	c.app.SetStatus("Finalizing response")
	text, err := model.ExtractTextContent(msg.Content)
	if err != nil {
		return toolLoopResult{}, err
	}
	if text == "" && strings.TrimSpace(msg.Reasoning) != "" {
		text = msg.Reasoning
	}
	sess.Conversation.AddAssistantMessageWithReasoningDetails(text, msg.Reasoning, msg.ReasoningDetails)
	c.saveLatestConversationMessage(sess)
	streamed := state != nil && state.progress.roundRendered && text != "" && state.progress.roundRenderedText == text
	return toolLoopResult{
		Text:                 text,
		Usage:                &totalUsage,
		ProviderFinishReason: providerFinishReason,
		Streamed:             streamed,
	}, nil
}

func normalizeToolLoopCalls(registry *tool.Registry, calls []model.ToolCall, allowedTools []string) []model.ToolCall {
	for i := range calls {
		if calls[i].ID == "" {
			calls[i].ID = fmt.Sprintf("tool-%d", i+1)
		}
		if repairedName, ok := resolveToolCallName(registry, calls[i].Function.Name, allowedTools); ok {
			calls[i].Function.Name = repairedName
		}
		repairToolCallByArguments(registry, &calls[i], allowedTools)
	}
	return calls
}

func repairToolCallByArguments(registry *tool.Registry, call *model.ToolCall, allowedTools []string) {
	if registry == nil || call == nil || call.Function.Name != "run_tests" {
		return
	}
	var params map[string]any
	if json.Unmarshal([]byte(call.Function.Arguments), &params) != nil {
		return
	}
	command, _ := params["command"].(string)
	if strings.TrimSpace(command) == "" || hasAnyToolParam(params, "path", "pattern", "coverage", "verbose") {
		return
	}
	if _, ok := registry.Get("run_shell"); ok && tool.IsToolAllowed("run_shell", allowedTools) {
		call.Function.Name = "run_shell"
	}
}

func hasAnyToolParam(params map[string]any, names ...string) bool {
	for _, name := range names {
		if _, ok := params[name]; ok {
			return true
		}
	}
	return false
}

// recordToolLoopCalls persists this round's tool-call turn, preserving any
// preamble text the model wrote before the calls (msg.Content) so it
// survives into wire history for subsequent requests -- see
// conversation.AddToolCallMessageWithContent.
func (c *Controller) recordToolLoopCalls(sess *SessionState, calls []model.ToolCall, msg model.Message) {
	c.app.SetStatus(fmt.Sprintf("Model requested %d tool call(s)", len(calls)))
	preamble := model.ExtractTextContentOrEmpty(msg.Content)
	sess.Conversation.AddToolCallMessageWithContent(preamble, calls, msg.Reasoning, msg.ReasoningDetails)
	c.saveLatestConversationMessage(sess)
}

// toolLoopExecuteOne runs one tool call -- argument parsing, registry and
// active-skill allow checks, shell-output-streaming context, execution, the
// per-call transcript progress messages (appendToolCallProgress/
// appendToolResultProgress), and model-facing result formatting -- and
// reports the formatted content plus the raw result/error so a caller can
// derive success and apply its own guard/history handling. This is the core
// both executeToolLoopCall (kept for direct callers and its own tests) and
// the agentloop.Controller-driven Dispatcher (newToolLoopController) share;
// neither the loop guard nor conversation persistence happens here.
func (c *Controller) toolLoopExecuteOne(ctx context.Context, sess *SessionState, tc model.ToolCall, index, total int, allowedTools []string, state *toolLoopState) (string, *builtin.Result, error) {
	c.appendToolCallProgress(state, tc)
	params, err := parseToolParams(tc.Function.Arguments)
	if err != nil {
		guardErr := fmt.Errorf("invalid arguments: %w", err)
		message := fmt.Sprintf("Error: invalid tool arguments: %v", err)
		c.appendToolResultProgress(state, tc.Function.Name, nil, guardErr)
		return message, nil, guardErr
	}
	if sess.ToolRegistry == nil {
		guardErr := fmt.Errorf("tool registry unavailable")
		message := "Error: tool registry unavailable"
		c.appendToolResultProgress(state, tc.Function.Name, nil, guardErr)
		return message, nil, guardErr
	}
	if !tool.IsToolAllowed(tc.Function.Name, allowedTools) {
		notAllowedErr := fmt.Errorf("tool %s not allowed by active skills", tc.Function.Name)
		message := "Error: " + notAllowedErr.Error()
		c.appendToolResultProgress(state, tc.Function.Name, nil, notAllowedErr)
		return message, nil, notAllowedErr
	}
	if params == nil {
		params = make(map[string]any)
	}
	if tc.ID != "" {
		params[tool.ToolCallIDParam] = tc.ID
	}

	toolCtx := c.withShellOutputStreaming(ctx, tc)

	c.app.StartProcessStatus(fmt.Sprintf("Running %s (%d/%d) · Ctrl+C to interrupt", compactStatusText(tc.Function.Name, 36), index, total))
	result, execErr := sess.ToolRegistry.ExecuteWithContext(toolCtx, tc.Function.Name, params)
	c.app.StopProcessStatus()
	c.appendToolResultProgress(state, tc.Function.Name, result, execErr)
	modelResult := formatToolResultForModel(result, execErr)
	if guidance := tool.CodeModeRecoveryGuidance(c.evaluator, sess.ToolRegistry, allowedTools, tc.Function.Name, result, execErr, state.codeModeRecovery); guidance != "" {
		if strings.TrimSpace(modelResult) == "" {
			modelResult = guidance
		} else {
			modelResult += "\n\n" + guidance
		}
		c.appendCodeModeRecoveryProgress(state, guidance)
	}
	return modelResult, result, execErr
}

// executeToolLoopCall runs one tool call and applies the loop guard and
// history persistence directly, matching the pre-engine behavior exactly.
// The agentloop.Controller-driven turn loop no longer calls this -- its
// Dispatcher hook calls toolLoopExecuteOne and lets Controller apply the
// Governor and History itself -- but this stays as the single-call entry
// point its own tests exercise.
func (c *Controller) executeToolLoopCall(ctx context.Context, sess *SessionState, tc model.ToolCall, index, total int, allowedTools []string, state *toolLoopState) {
	modelResult, result, execErr := c.toolLoopExecuteOne(ctx, sess, tc, index, total, allowedTools, state)
	modelResult += applyToolLoopGuard(state, tc, result, execErr, modelResult)
	c.addToolLoopResponse(sess, tc, modelResult)
}

// withShellOutputStreaming attaches a shell output sink for run_shell calls
// so a long-running command's stdout/stderr streams live into the
// inspector's activity Detail instead of only appearing once the command
// exits. The transcript keeps its compact single-line progress summary;
// only the inspector receives the incremental output. Non-shell tools get
// ctx unchanged.
func (c *Controller) withShellOutputStreaming(ctx context.Context, tc model.ToolCall) context.Context {
	if c == nil || c.telemetryBridge == nil || tc.Function.Name != "run_shell" || tc.ID == "" {
		return ctx
	}
	taskID := tc.ID
	return builtin.WithShellOutputSink(ctx, func(_ string, text string) {
		c.telemetryBridge.AppendActivityOutput(taskID, text)
	})
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

// appendCodeModeRecoveryProgress makes the governed recovery decision visible
// at the exact failed/low-yield operation, rather than hiding it only in the
// model-facing tool response. The same text is persisted to tool history by
// toolLoopExecuteOne for replay and resumed-session rendering.
func (c *Controller) appendCodeModeRecoveryProgress(state *toolLoopState, guidance string) {
	if c == nil || c.app == nil || state == nil {
		return
	}
	c.app.AppendToLastMessage("\n\n" + codeModeRecoveryProgress(guidance))
}

func codeModeRecoveryProgress(guidance string) string {
	guidance = strings.TrimSpace(guidance)
	guidance = strings.TrimSpace(strings.TrimPrefix(guidance, "CODE MODE RECOVERY:"))
	if guidance == "" {
		return "⚡ Code mode recommended"
	}
	return "⚡ Code mode recommended\n\n" + compactMultilineText(guidance, 640)
}

func toolCallProgressBlock(tc model.ToolCall) string {
	return "→ " + toolCallProgressSummary(tc)
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
	if yield := tool.ResultYieldForTool(name, result, nil); yield.Observed {
		return "✓ " + name + " — " + yield.Summary()
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
	case "loop_guard":
		return "Buckley's harness stopped further tool execution. This was not a provider finish reason."
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
	encoded, err := tool.ToModelOutput(result)
	if err != nil {
		return fmt.Sprintf("{\"success\":%t}", result.Success)
	}
	return truncateModelToolOutput(encoded, defaultTUIToolModelMaxBytes)
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
