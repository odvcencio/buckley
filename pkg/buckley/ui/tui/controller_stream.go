package tui

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/orchestrator"
	"github.com/odvcencio/buckley/pkg/telemetry"
)

// streamResponse handles the AI response streaming for a specific session.
func (c *Controller) streamResponse(ctx context.Context, prompt string, sess *SessionState, attachments []string) {
	handoff := false
	defer func() {
		if r := recover(); r != nil {
			// Log stack trace for debugging
			stack := make([]byte, 4096)
			n := runtime.Stack(stack, false)
			log.Printf("Panic in streamResponse: %v\n%s", r, stack[:n])

			if c.app != nil && c.isCurrentSession(sess) {
				c.app.AddMessage(fmt.Sprintf("Internal error: %v", r), "system")
				c.app.SetStatus("Error")
			}
			c.clearMessageQueue(sess)
		}
		if handoff {
			return
		}
		c.mu.Lock()
		sess.Streaming = false
		sess.Cancel = nil
		isCurrent := c.currentSession >= 0 && c.currentSession < len(c.sessions) && c.sessions[c.currentSession] == sess
		c.mu.Unlock()
		c.emitStreaming(sess.ID, false)
		if isCurrent {
			c.app.SetStreaming(false)
		}
	}()

	c.runIfCurrentSession(sess, func() {
		c.app.SetStatus("Thinking...")
		c.app.ShowThinkingIndicator()
	})

	inputText := strings.TrimSpace(prompt)
	if len(attachments) > 0 {
		if inputText != "" {
			inputText += "\n"
		}
		inputText += strings.Join(attachments, "\n")
	}
	raw := orchestrator.ParseInputText(inputText)
	processor := orchestrator.NewInputProcessor(nil)
	if c.cfg != nil {
		processor.EnableVideoProcessing(c.cfg.Input.Video.Enabled)
		processor.SetMaxFrames(c.cfg.Input.Video.MaxFrames)
	}
	processor.SetWorkDir(c.workDir)
	input, err := processor.Process(ctx, raw)
	if err != nil {
		c.clearMessageQueue(sess)
		c.runIfCurrentSession(sess, func() {
			c.app.AddMessage(fmt.Sprintf("Error processing input: %v", err), "system")
			c.app.SetStatus("Error")
		})
		return
	}

	var parts []model.ContentPart
	if strings.TrimSpace(input.Text) != "" {
		parts = append(parts, model.ContentPart{
			Type: "text",
			Text: input.Text,
		})
	}
	for _, img := range input.Images {
		parts = append(parts, model.ContentPart{
			Type: "image_url",
			ImageURL: &model.ImageURL{
				URL:    img.DataURL,
				Detail: "auto",
			},
		})
	}

	var warnings []string
	for _, att := range input.Attachments {
		if !isTextAttachment(att.MimeType) {
			warnings = append(warnings, fmt.Sprintf("Skipped non-text attachment: %s", att.Name))
			continue
		}
		content, truncated, err := readAttachmentText(att.Path, defaultTUIAttachmentMaxBytes)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("Attachment %s: %v", att.Name, err))
			continue
		}
		text := fmt.Sprintf("Attachment: %s\n```\n%s\n```", att.Name, content)
		if truncated {
			text += "\n...[truncated]"
		}
		parts = append(parts, model.ContentPart{
			Type: "text",
			Text: text,
		})
	}
	if len(warnings) > 0 {
		c.runIfCurrentSession(sess, func() {
			c.app.AddMessage(strings.Join(warnings, "\n"), "system")
		})
	}
	if len(input.Metadata.ProcessingErrors) > 0 {
		c.runIfCurrentSession(sess, func() {
			c.app.AddMessage(strings.Join(input.Metadata.ProcessingErrors, "\n"), "system")
		})
	}

	if len(parts) == 0 {
		if strings.TrimSpace(prompt) == "" {
			c.runIfCurrentSession(sess, func() {
				c.app.AddMessage("No input content.", "system")
				c.app.SetStatus("Error")
			})
			return
		}
		parts = append(parts, model.ContentPart{Type: "text", Text: prompt})
	}

	useParts := len(input.Images) > 0 || len(input.Attachments) > 0 || len(parts) > 1 || (len(parts) == 1 && parts[0].Type != "text")
	if useParts {
		sess.Conversation.AddUserMessageParts(parts)
	} else {
		sess.Conversation.AddUserMessage(parts[0].Text)
	}
	c.saveLastMessage(sess)

	modelID := c.executionModelID()
	contextPrompt := ""
	if c.execStrategy != nil {
		contextPrompt = strings.TrimSpace(input.Text)
		if contextPrompt == "" {
			contextPrompt = prompt
		}
	}
	c.updateContextIndicator(sess, modelID, contextPrompt, allowedToolsForSession(sess))

	outcome, err := c.runToolLoop(ctx, sess, modelID)
	c.runIfCurrentSession(sess, func() {
		c.app.RemoveThinkingIndicator()
	})
	if err != nil {
		if ctx.Err() == context.Canceled {
			c.clearMessageQueue(sess)
			c.runIfCurrentSession(sess, func() {
				c.app.SetStatus("Cancelled")
			})
			return
		}
		c.clearMessageQueue(sess)
		c.runIfCurrentSession(sess, func() {
			if !outcome.errorReported {
				c.app.AddMessage(fmt.Sprintf("Error: %v", err), "system")
			}
			c.app.SetStatus("Error")
		})
		return
	}

	if outcome.response != "" && !outcome.streamed {
		c.runIfCurrentSession(sess, func() {
			c.app.AddMessage(outcome.response, "assistant")
		})
	}

	// Update token count and cost
	var tokens int
	var costCents float64

	if outcome.usage != nil {
		// Use actual usage from API response
		tokens = outcome.usage.TotalTokens
		if c.modelMgr != nil {
			if cost, err := c.modelMgr.CalculateCost(modelID, *outcome.usage); err == nil {
				costCents = cost * 100 // Convert dollars to cents
			}
		}
		c.recordCost(sess.ID, modelID, outcome.usage)
	} else {
		// Fallback: estimate tokens from response length
		tokens = len(outcome.response) / 4
		// Estimate cost using model pricing if available
		if c.modelMgr != nil {
			if cost, err := c.modelMgr.CalculateCostFromTokens(modelID, 0, tokens); err == nil {
				costCents = cost * 100
			}
		}
	}
	c.runIfCurrentSession(sess, func() {
		c.app.SetTokenCount(tokens, costCents)
	})
	c.updateContextIndicator(sess, modelID, "", allowedToolsForSession(sess))

	// Handoff one queued message to a fresh stream invocation to avoid recursive stack growth.
	if queued, ok := c.dequeueMessage(sess); ok {
		c.mu.Lock()
		ctx, cancel := context.WithCancel(c.baseContext())
		sess.Cancel = cancel
		sess.Streaming = true
		sessionID := sess.ID
		c.mu.Unlock()
		c.emitStreaming(sessionID, true)
		c.runIfCurrentSession(sess, func() {
			c.app.SetStreaming(true)
		})

		handoff = true
		go c.streamResponse(ctx, queued.Content, sess, queued.Attachments)
		return
	}

	// Update status only if no more queued messages
	c.runIfCurrentSession(sess, func() {
		c.app.SetStatus("Ready")
	})
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

func (c *Controller) emitUICommandEvent(cmd string, args []string, phase string, duration time.Duration) {
	if c.telemetry == nil || strings.TrimSpace(cmd) == "" {
		return
	}
	data := map[string]any{
		"command": cmd,
		"phase":   phase,
	}
	if len(args) > 0 {
		data["args"] = args
	}
	if duration > 0 {
		data["duration_ms"] = duration.Milliseconds()
	}
	sessionID := ""
	if c.mu.TryLock() {
		if len(c.sessions) > 0 && c.currentSession >= 0 && c.currentSession < len(c.sessions) {
			sessionID = c.sessions[c.currentSession].ID
		}
		c.mu.Unlock()
	}
	c.telemetry.Publish(telemetry.Event{
		Type:      telemetry.EventUICommand,
		SessionID: sessionID,
		Data:      data,
	})
}
