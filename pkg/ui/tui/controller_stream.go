package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/telemetry"
)

// streamResponse handles the AI response streaming for a specific session.
func (c *Controller) streamResponse(ctx context.Context, prompt string, sess *SessionState) {
	c.streamResponseWithIntent(ctx, prompt, sess, agentloop.UnknownIntent)
}

func (c *Controller) streamResponseWithIntent(ctx context.Context, prompt string, sess *SessionState, taskIntent agentloop.TaskIntent) {
	defer c.finishStreamLifecycle(sess)

	turnBoundary := c.beginTurnUndo(sess)
	modelID := c.prepareStreamRequest(prompt, sess)
	if strings.TrimSpace(modelID) == "" {
		c.app.RemoveThinkingIndicator()
		c.finishTurnUndo(sess, turnBoundary)
		_, err := model.ResolvePhaseModelRequired(c.cfg, c.modelMgr, c.rulesEngine, "execution", c.modelOverride)
		if err == nil {
			err = fmt.Errorf("no execution model resolved; configure a model before sending a message")
		}
		c.handleStreamError(ctx, err)
		return
	}
	result, err := c.runToolLoopWithIntent(ctx, sess, modelID, taskIntent)
	c.app.RemoveThinkingIndicator()
	c.finishTurnUndo(sess, turnBoundary)
	var incomplete *agentloop.IncompleteTurnError
	if errors.As(err, &incomplete) || retainedIncompleteStreamResult(result, err) {
		c.renderIncompleteStreamResponse(sess, result, err)
		c.updateStreamUsage(modelID, result.Text, result.Usage)
		if ctx.Err() == context.Canceled {
			c.app.SetStatus("Cancelled")
		} else {
			c.app.SetStatus("Error")
		}
		if ctx.Err() == context.Canceled && c.processMessageQueue(sess) {
			return
		}
		return
	}
	if c.handleStreamError(ctx, err) {
		if ctx.Err() == context.Canceled && c.processMessageQueue(sess) {
			return
		}
		return
	}

	c.renderStreamResponse(result)
	c.updateStreamUsage(modelID, result.Text, result.Usage)
	if c.processMessageQueue(sess) {
		return
	}
	if result.HarnessStopReason != "" {
		c.app.SetStatus("Ready - harness stopped tools")
	} else {
		c.app.SetStatus(readyStatusForFinishReason(result.ProviderFinishReason))
	}
}

func retainedIncompleteStreamResult(result toolLoopResult, err error) bool {
	if err == nil {
		return false
	}
	return strings.TrimSpace(result.Text) != "" || result.Usage != nil
}

func (c *Controller) finishStreamLifecycle(sess *SessionState) {
	c.mu.Lock()
	sess.Streaming = false
	sess.Cancel = nil
	c.mu.Unlock()
	c.emitStreaming(sess.ID, false)
	c.refreshSessionNav()
}

func (c *Controller) prepareStreamRequest(prompt string, sess *SessionState) string {
	c.app.SetStatus("Preparing request")
	sess.Conversation.AddUserMessage(prompt)
	c.saveLatestConversationMessage(sess)
	return c.resolveExecutionModel()
}

func (c *Controller) resolveExecutionModel() string {
	return strings.TrimSpace(model.ResolvePhaseModel(c.cfg, c.modelMgr, c.rulesEngine, "execution", c.modelOverride))
}

func (c *Controller) handleStreamError(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx.Err() == context.Canceled {
		c.app.SetStatus("Cancelled")
		return true
	}
	c.app.AddMessage(fmt.Sprintf("Error: %v", err), "system")
	c.app.SetStatus("Error")
	return true
}

// renderStreamResponse renders the turn's closing system notices. The
// assistant answer itself is rendered here only when it was not already
// streamed live into the transcript (continuation turns, and the
// reasoning-channel fallback, never stream) -- streamed is true exactly
// when the transcript already shows result.Text verbatim, so skipping the
// AddMessage call here is what keeps streaming from double-rendering the
// final answer.
func (c *Controller) renderStreamResponse(result toolLoopResult) {
	if result.Text == "" {
		c.app.AddMessage("(empty response from model)", "system")
	} else if !result.Streamed {
		c.app.AddMessage(result.Text, "assistant")
	}
	if notice := harnessStopReasonNotice(result.HarnessStopReason); notice != "" {
		c.app.AddMessage(notice, "system")
	}
	if notice := modelFinishReasonNotice(result.ProviderFinishReason); notice != "" {
		c.app.AddMessage(notice, "system")
	}
}

func (c *Controller) renderIncompleteStreamResponse(sess *SessionState, result toolLoopResult, err error) {
	notice := agentloop.PresentIncompleteResult(err)
	detail := notice.Message
	text := strings.TrimSpace(result.Text)
	c.persistAndRenderIncompleteNotice(sess, detail)
	if text == "" {
		return
	}
	c.persistAndRenderIncompleteDraft(sess, text)
}

func (c *Controller) persistAndRenderIncompleteNotice(sess *SessionState, content string) {
	c.app.AddMessage(content, "system")
	if sess == nil || sess.Conversation == nil {
		return
	}
	sess.Conversation.AddSystemMessage(content)
	c.saveLatestConversationMessage(sess)
}

func (c *Controller) persistAndRenderIncompleteDraft(sess *SessionState, draft string) {
	draft = strings.TrimSpace(draft)
	if draft == "" {
		return
	}
	content := "Preserved draft (incomplete):\n" + draft
	c.app.AddMessage(content, "assistant")
	if sess == nil || sess.Conversation == nil {
		return
	}
	msg := conversation.Message{
		Role:        "assistant",
		Content:     content,
		Timestamp:   time.Now(),
		Tokens:      conversation.CountTokens(content),
		IsTruncated: true,
	}
	sess.Conversation.Messages = append(sess.Conversation.Messages, msg)
	sess.Conversation.TokenCount += msg.Tokens
	c.saveLatestConversationMessage(sess)
}

func harnessStopReasonNotice(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ""
	}
	return "Buckley's harness stopped further tool execution because " + strings.TrimSuffix(reason, ".") + ". This was a Buckley harness decision, not a provider finish reason; the response was synthesized from evidence already gathered."
}

func (c *Controller) updateStreamUsage(modelID, fullResponse string, usage *model.Usage) {
	stats := streamUsageStats(modelID, fullResponse, usage, c.modelMgr)
	c.app.SetTokenCount(stats.tokens, stats.costCents)
}

type streamUsage struct {
	tokens    int
	costCents float64
}

func streamUsageStats(modelID, fullResponse string, usage *model.Usage, mgr *model.Manager) streamUsage {
	if usage != nil {
		stats := streamUsage{tokens: usage.TotalTokens}
		if mgr != nil {
			if cost, err := mgr.CalculateCost(modelID, *usage); err == nil {
				stats.costCents = cost * 100
			}
		}
		return stats
	}

	stats := streamUsage{tokens: len(fullResponse) / 4}
	if mgr != nil {
		if cost, err := mgr.CalculateCostFromTokens(modelID, 0, stats.tokens); err == nil {
			stats.costCents = cost * 100
		}
	}
	return stats
}

func (c *Controller) emitStreaming(sessionID string, streaming bool) {
	if c.telemetry == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	eventType := telemetry.EventModelStreamEnded
	if streaming {
		eventType = telemetry.EventModelStreamStarted
	}
	c.telemetry.Publish(telemetry.Event{
		Type:      eventType,
		SessionID: sessionID,
	})
}
