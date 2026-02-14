package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	buckleywidgets "github.com/odvcencio/buckley/pkg/buckley/ui/widgets"
	"github.com/odvcencio/buckley/pkg/conversation"
)

func (c *Controller) baseContext() context.Context {
	if c == nil || c.ctx == nil {
		return context.Background()
	}
	return c.ctx
}

// SetContext updates the controller base context for downstream operations.
func (c *Controller) SetContext(ctx context.Context) {
	if c == nil || ctx == nil {
		return
	}
	if c.cancel != nil {
		c.cancel()
	}
	c.ctx, c.cancel = context.WithCancel(ctx)
}

func (c *Controller) buildChatMessages(sess *SessionState, intro string, includeContext bool) []buckleywidgets.ChatMessage {
	if c == nil || sess == nil {
		return nil
	}

	modelName := strings.TrimSpace(c.executionModelID())
	now := time.Now()
	nextID := 0
	lastUserAt := time.Time{}
	baseLen := 0
	if sess.Conversation != nil {
		baseLen = len(sess.Conversation.Messages)
	}
	messages := make([]buckleywidgets.ChatMessage, 0, baseLen+4)

	addMessage := func(content, source string, messageTime time.Time) {
		if messageTime.IsZero() {
			messageTime = now
		}
		nextID++
		entry := buckleywidgets.ChatMessage{
			ID:      nextID,
			Content: content,
			Source:  source,
			Time:    messageTime,
			Model:   modelName,
		}
		if source == "user" {
			lastUserAt = entry.Time
		} else if source == "assistant" {
			entry.UserAt = lastUserAt
		}
		messages = append(messages, entry)
	}

	addMessage("Welcome to Buckley", "system", now)
	addMessage("Type a message to get started, or use /help for commands.", "system", now)

	if includeContext && c.projectCtx != nil && c.projectCtx.Loaded {
		addMessage("Project context loaded from AGENTS.md", "system", now)
	}

	if strings.TrimSpace(intro) != "" {
		addMessage(intro, "system", now)
	}

	if sess.Conversation != nil {
		for _, msg := range sess.Conversation.Messages {
			content := conversation.GetContentAsString(msg.Content)
			addMessage(content, msg.Role, msg.Timestamp)
		}
	}

	return messages
}

// Run starts the TUI controller.
func (c *Controller) Run() error {
	// Start telemetry bridge for sidebar updates
	if c.telemetryBridge != nil {
		c.telemetryBridge.Start(c.baseContext())
	}

	sess := c.sessions[c.currentSession]
	intro := ""
	if sess != nil && sess.Conversation != nil && len(sess.Conversation.Messages) > 0 {
		intro = fmt.Sprintf("Resuming session: %s (%d messages)", sess.ID, len(sess.Conversation.Messages))
	}
	c.app.SetChatMessages(c.buildChatMessages(sess, intro, true))
	if sess != nil {
		c.showPendingApprovals(sess.ID)
	}
	c.startMissionApprovalPolling()

	c.updateContextIndicator(sess, c.executionModelID(), "", allowedToolsForSession(sess))

	// Run the app
	err := c.app.Run()
	if c.cancel != nil {
		c.cancel()
	}
	return err
}

// handleSubmit processes user input submission.
func (c *Controller) handleSubmit(text string) {
	// Handle commands immediately (no locking needed)
	if strings.HasPrefix(text, "/") {
		c.handleCommand(text)
		return
	}

	c.mu.Lock()

	// Get current session
	sess := c.sessions[c.currentSession]
	attachments := append([]string(nil), sess.PendingAttachments...)
	if text == "" && len(attachments) == 0 {
		c.mu.Unlock()
		return
	}

	// If session is streaming, queue the message instead of starting new stream
	if sess.Streaming {
		sess.MessageQueue = append(sess.MessageQueue, QueuedMessage{
			Content:     text,
			Timestamp:   time.Now(),
			Attachments: attachments,
		})
		sess.PendingAttachments = nil
		c.mu.Unlock()

		// Show queued message with indicator (outside lock)
		displayText := text
		if strings.TrimSpace(displayText) == "" && len(attachments) > 0 {
			displayText = fmt.Sprintf("[Queued %d attachment(s)]", len(attachments))
		}
		c.app.AddMessage(displayText+" (queued)", "user")
		c.updateQueueIndicator(sess)
		return
	}

	// Add user message to display (outside lock)
	displayText := text
	if strings.TrimSpace(displayText) == "" && len(attachments) > 0 {
		displayText = fmt.Sprintf("[Sent %d attachment(s)]", len(attachments))
	}

	// Create context with cancellation for this session
	ctx, cancel := context.WithCancel(c.baseContext())
	sess.Cancel = cancel
	sess.Streaming = true
	sess.PendingAttachments = nil
	c.mu.Unlock()

	// Update UI (outside lock)
	c.app.AddMessage(displayText, "user")
	c.app.SetStreaming(true)
	c.emitStreaming(sess.ID, true)

	// Start streaming response for this session
	go c.streamResponse(ctx, text, sess, attachments)
}

// handleCommand processes slash commands.
