package tui

import (
	"context"
	"fmt"
	"strings"

	"m31labs.dev/buckley/v2/pkg/agentloop"
	"m31labs.dev/buckley/v2/pkg/conversation"
	"m31labs.dev/buckley/v2/pkg/model"
	"m31labs.dev/buckley/v2/pkg/tool/builtin"
)

type toolLoopGovernor interface {
	BeginRound() agentloop.Decision
	Observe(name, arguments, result string, success bool) agentloop.Decision
}

func newInteractiveToolLoopGovernor() toolLoopGovernor {
	return agentloop.New(agentloop.DefaultConfig())
}

func applyToolLoopGuard(state *toolLoopState, call model.ToolCall, result *builtin.Result, execErr error, modelResult string) string {
	if state == nil || state.governor == nil {
		return ""
	}
	success := execErr == nil && result != nil && result.Success
	decision := state.governor.Observe(call.Function.Name, call.Function.Arguments, modelResult, success)
	if decision.Stop && state.guardReason == "" {
		state.guardReason = decision.Reason
	}
	if strings.TrimSpace(decision.Nudge) == "" {
		return ""
	}
	return "\n\n" + decision.Nudge
}

func (c *Controller) handleToolLoopContextError(err error, state *toolLoopState) bool {
	if c == nil || state == nil || !model.IsContextLengthError(err) || state.contextRetries >= 2 {
		return false
	}
	state.contextRetries++
	state.contextScale = nextContextProjectionScale(state.contextScale)
	if c.app != nil {
		c.app.SetStatus(fmt.Sprintf("Context limit reached; retrying with %.0f%% projection", state.contextScale*100))
	}
	return true
}

func nextContextProjectionScale(current float64) float64 {
	if current <= 0 || current > 1 {
		current = 1
	}
	next := current * 0.55
	if next < 0.25 {
		next = 0.25
	}
	return next
}

func contextProjectionStatus(stats conversation.ContextProjectionStats) string {
	if !stats.Compacted || stats.OriginalTokens <= 0 || stats.ProjectedTokens <= 0 {
		return ""
	}
	label := fmt.Sprintf("context ~%.1fk→%.1fk", float64(stats.OriginalTokens)/1000, float64(stats.ProjectedTokens)/1000)
	if stats.Emergency {
		label += " emergency"
	} else {
		label += " compacted"
	}
	return label
}

func (c *Controller) finishGuardedToolLoop(ctx context.Context, sess *SessionState, modelID string, state *toolLoopState, reason string) (string, *model.Usage, string, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "tool execution stopped because the harness detected no forward progress"
	}
	if c.app != nil {
		c.app.SetStatus("Loop guard stopped tools; synthesizing from existing evidence")
	}

	req, _ := c.buildToolLoopRequestWithState(sess, modelID, false, state)
	req.Tools = nil
	req.ToolChoice = ""
	req.ParallelToolCalls = nil
	req.Messages = append(req.Messages, model.Message{
		Role: "system",
		Content: "Buckley's harness stopped further tool execution because " + reason + ". " +
			"Do not call another tool. Use only evidence already present in the conversation, state any remaining uncertainty, and give the user the most useful concise final response you can.",
	})

	contextWindow := 0
	if c.modelMgr != nil {
		contextWindow, _ = c.modelMgr.GetContextLength(modelID)
	}
	scale := 0.75
	if state != nil && state.contextScale > 0 && state.contextScale < scale {
		scale = state.contextScale
	}
	projected, projection := conversation.ProjectModelMessagesForRequest(req.Messages, req, contextWindow, scale)
	req.Messages = projected
	if state != nil {
		state.projection = projection
	}

	resp, err := c.callToolLoopModel(ctx, req, modelID, 0, state)
	if err != nil {
		return c.finishGuardFallback(sess, state, reason, err)
	}
	if state != nil {
		state.totalUsage = model.AddUsage(state.totalUsage, resp.Usage)
	}
	choice, err := firstToolLoopChoice(req, resp)
	if err != nil {
		return c.finishGuardFallback(sess, state, reason, err)
	}
	text, extractErr := model.ExtractTextContent(choice.Message.Content)
	if extractErr != nil || strings.TrimSpace(text) == "" || len(choice.Message.ToolCalls) > 0 {
		return c.finishGuardFallback(sess, state, reason, extractErr)
	}
	usage := model.Usage{}
	if state != nil {
		usage = state.totalUsage
	}
	return c.finishToolLoopResponse(sess, choice.Message, usage, "loop_guard")
}

func (c *Controller) finishGuardFallback(sess *SessionState, state *toolLoopState, reason string, cause error) (string, *model.Usage, string, error) {
	text := "I stopped the tool loop because " + strings.TrimSuffix(reason, ".") + ". " +
		"The evidence gathered so far remains available in the activity inspector. A different strategy or a narrower follow-up is needed before more tool execution would be useful."
	if cause != nil {
		text += " Final synthesis also failed: " + compactStatusText(cause.Error(), 240) + "."
	}
	usage := model.Usage{}
	if state != nil {
		usage = state.totalUsage
	}
	return c.finishToolLoopResponse(sess, model.Message{Role: "assistant", Content: text}, usage, "loop_guard")
}
