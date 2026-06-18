package tui

import (
	"context"
	"fmt"
	"strings"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/telemetry"
)

// streamResponse handles the AI response streaming for a specific session.
func (c *Controller) streamResponse(ctx context.Context, prompt string, sess *SessionState) {
	defer c.finishStreamLifecycle(sess)

	modelID := c.prepareStreamRequest(prompt, sess)
	fullResponse, usage, finishReason, err := c.runToolLoop(ctx, sess, modelID)
	c.app.RemoveThinkingIndicator()
	if c.handleStreamError(ctx, err) {
		return
	}

	c.renderStreamResponse(fullResponse, finishReason)
	c.updateStreamUsage(modelID, fullResponse, usage)
	if c.processMessageQueue(sess) {
		return
	}
	c.app.SetStatus(readyStatusForFinishReason(finishReason))
}

func (c *Controller) finishStreamLifecycle(sess *SessionState) {
	c.mu.Lock()
	sess.Streaming = false
	sess.Cancel = nil
	c.mu.Unlock()
	c.emitStreaming(sess.ID, false)
}

func (c *Controller) prepareStreamRequest(prompt string, sess *SessionState) string {
	c.app.SetStatus("Preparing request")
	c.app.ShowThinkingIndicator()
	sess.Conversation.AddUserMessage(prompt)
	c.saveLatestConversationMessage(sess)
	return c.resolveExecutionModel()
}

func (c *Controller) resolveExecutionModel() string {
	modelID := model.ResolvePhaseModel(c.cfg, c.modelMgr, c.rulesEngine, "execution", "")
	if modelID == "" {
		return "openai/gpt-4o"
	}
	return modelID
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

func (c *Controller) renderStreamResponse(fullResponse, finishReason string) {
	if fullResponse != "" {
		c.app.AddMessage(fullResponse, "assistant")
	} else {
		c.app.AddMessage("(empty response from model)", "system")
	}
	if notice := modelFinishReasonNotice(finishReason); notice != "" {
		c.app.AddMessage(notice, "system")
	}
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
