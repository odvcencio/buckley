package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/durability/modelstep"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/telemetry"
)

// streamResponse handles the AI response streaming for a specific session.
func (c *Controller) streamResponse(ctx context.Context, prompt string, sess *SessionState) {
	c.streamResponseWithIntent(ctx, prompt, sess, agentloop.UnknownIntent)
}

func (c *Controller) streamResponseWithIntent(ctx context.Context, prompt string, sess *SessionState, taskIntent agentloop.TaskIntent) {
	for {
		advance := c.streamResponseTurn(ctx, prompt, sess, taskIntent)
		nextCtx, queued, ok := c.finishStreamTurn(sess, advance)
		if !ok {
			return
		}
		ctx, prompt, taskIntent = nextCtx, queued.Content, queued.TaskIntent
	}
}

func (c *Controller) streamResponseTurn(ctx context.Context, prompt string, sess *SessionState, taskIntent agentloop.TaskIntent) bool {
	c.mu.Lock()
	err := c.loadSessionRuntimeLocked(sess)
	c.mu.Unlock()
	if err != nil {
		c.handleStreamError(ctx, err)
		return false
	}
	turnBoundary := c.beginTurnUndo(sess)
	modelID := c.prepareStreamRequest(prompt, sess)
	if strings.TrimSpace(modelID) == "" {
		c.app.RemoveThinkingIndicator()
		c.finishTurnUndo(sess, turnBoundary)
		err := sess.persistenceErr
		if err == nil {
			_, err = model.ResolvePhaseModelRequired(c.cfg, c.modelMgr, c.rulesEngine, "execution", c.modelOverride)
		}
		if err == nil {
			err = fmt.Errorf("no execution model resolved; configure a model before sending a message")
		}
		c.handleStreamError(ctx, err)
		return false
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
		return ctx.Err() == context.Canceled
	}
	if c.handleStreamError(ctx, err) {
		c.mu.Lock()
		saveFailed := sess.persistenceErr != nil
		c.mu.Unlock()
		if !saveFailed {
			c.app.retirePresentation(result.presentationTextID)
			c.app.retirePresentation(result.presentationReasonID)
		}
		return ctx.Err() == context.Canceled
	}

	c.renderStreamResponse(result)
	c.updateStreamUsage(modelID, result.Text, result.Usage)
	if result.HarnessStopReason != "" {
		c.app.SetStatus("Ready - harness stopped tools")
	} else {
		c.app.SetStatus(readyStatusForFinishReason(result.ProviderFinishReason))
	}
	return true
}

func retainedIncompleteStreamResult(result toolLoopResult, err error) bool {
	if err == nil {
		return false
	}
	return strings.TrimSpace(result.Text) != "" || result.Usage != nil
}

func (c *Controller) finishStreamTurn(sess *SessionState, advance bool) (context.Context, QueuedMessage, bool) {
	c.mu.Lock()
	if err := c.boundSessionWorkingSetLocked(sess); err != nil {
		sess.persistenceErr = err
		advance = false
		c.app.SetStatus("Paused - session memory budget")
	}
	if sess.Cancel != nil {
		sess.Cancel()
	}
	sess.Streaming = false
	sess.Cancel = nil
	c.emitStreaming(sess.ID, false)
	if !advance || len(sess.MessageQueue) == 0 {
		cacheErr := c.boundIdleHistoryLocked()
		c.mu.Unlock()
		if cacheErr != nil {
			c.app.AddMessage("Keeping inactive history in memory: "+cacheErr.Error(), "system")
		}
		c.refreshSessionNav()
		return nil, QueuedMessage{}, false
	}
	queued := sess.MessageQueue[0]
	sess.MessageQueue[0] = QueuedMessage{}
	sess.MessageQueue = sess.MessageQueue[1:]
	if len(sess.MessageQueue) == 0 {
		sess.MessageQueue = nil
	}
	sess.DisableToolsNextTurn = queued.DisableTools
	ctx, cancel := context.WithCancel(context.Background())
	sess.Cancel, sess.Streaming = cancel, true
	c.emitStreaming(sess.ID, true)
	remaining := len(sess.MessageQueue)
	c.mu.Unlock()
	ack := fmt.Sprintf("Processing queued message from %s", queued.Timestamp.Format("15:04:05"))
	if queued.Steering {
		ack = fmt.Sprintf("Applying steering from %s", queued.Timestamp.Format("15:04:05"))
	}
	if remaining > 0 {
		ack += fmt.Sprintf(" (%d more queued)", remaining)
	}
	c.app.AddMessage(ack, "system")
	c.refreshSessionNav()
	return ctx, queued, true
}

func (c *Controller) prepareStreamRequest(prompt string, sess *SessionState) string {
	c.app.SetStatus("Preparing request")
	sess.Conversation.AddUserMessage(prompt)
	if err := c.saveLatestConversationMessage(sess); err != nil {
		return ""
	}
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
	var incomplete *agentloop.IncompleteTurnError
	if errors.As(err, &incomplete) {
		cause := strings.Join(strings.Fields(modelstep.NormalizeErrorText(incomplete.ProviderError)), " ")
		if cause != "" && !strings.Contains(detail, cause) {
			detail += " Provider error: " + cause
		}
	}
	text := strings.TrimSpace(result.Text)
	c.mu.Lock()
	noticeErr := c.persistAndRenderIncompleteNotice(sess, detail)
	var draftErr error
	if text != "" {
		draftErr = c.persistAndRenderIncompleteDraft(sess, text)
	}
	if sess != nil && sess.Conversation != nil {
		c.app.live.accept(result.presentationTextID)
		c.app.live.accept(result.presentationReasonID)
		if noticeErr != nil {
			sess.persistenceErr = noticeErr
		}
	}
	c.mu.Unlock()
	if noticeErr == nil && draftErr == nil {
		c.app.retirePresentation(result.presentationTextID)
		c.app.retirePresentation(result.presentationReasonID)
	}
}

func (c *Controller) persistAndRenderIncompleteNotice(sess *SessionState, content string) error {
	if c.app != nil {
		if sess != nil {
			c.app.Post(AddMessageMsg{SessionID: sess.ID, Epoch: c.app.presentationEpoch.Load(), Content: content, Source: "system"})
		} else {
			c.app.AddMessage(content, "system")
		}
	}
	if sess == nil || sess.Conversation == nil {
		return nil
	}
	sess.Conversation.AddSystemMessage(content)
	return c.saveLatestConversationMessage(sess)
}

func (c *Controller) persistAndRenderIncompleteDraft(sess *SessionState, draft string) error {
	draft = strings.TrimSpace(draft)
	if draft == "" {
		return nil
	}
	content := "Preserved draft (incomplete):\n" + draft
	if sess != nil {
		c.app.Post(AddMessageMsg{SessionID: sess.ID, Epoch: c.app.presentationEpoch.Load(), Content: content, Source: "assistant"})
	} else {
		c.app.AddMessage(content, "assistant")
	}
	if sess == nil || sess.Conversation == nil {
		return nil
	}
	msg := conversation.Message{
		Role:        "assistant",
		Content:     content,
		Timestamp:   time.Now(),
		Tokens:      conversation.CountTokens(content),
		IsTruncated: true,
	}
	sess.Conversation.AppendMessage(msg)
	sess.Conversation.TokenCount += msg.Tokens
	return c.saveLatestConversationMessage(sess)
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
