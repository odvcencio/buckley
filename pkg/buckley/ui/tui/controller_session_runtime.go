package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/odvcencio/buckley/pkg/session"
	"github.com/odvcencio/buckley/pkg/storage"
)

func (c *Controller) newSession() {
	c.mu.Lock()
	var oldSess *SessionState
	if len(c.sessions) > 0 && c.currentSession >= 0 && c.currentSession < len(c.sessions) {
		oldSess = c.sessions[c.currentSession]
	}
	c.mu.Unlock()

	// Cancel any active streaming and mark old session as completed
	if oldSess != nil {
		if oldSess.Cancel != nil {
			oldSess.Cancel()
		}
		if oldSess.ID != "" {
			_ = c.store.SetSessionStatus(oldSess.ID, storage.SessionStatusCompleted)
		}
	}

	newSess, err := c.createNewSessionState()
	if err != nil {
		c.app.AddMessage("Error creating session: "+err.Error(), "system")
		return
	}
	c.mu.Lock()
	c.sessions = append([]*SessionState{newSess}, c.sessions...)
	c.currentSession = 0
	c.conversation = newSess.Conversation
	c.registry = newSess.ToolRegistry
	c.mu.Unlock()

	// Reset scrollback with a fresh welcome
	c.app.SetSessionID(newSess.ID)
	c.app.SetChatMessages(c.buildChatMessages(newSess, "New session started: "+newSess.ID, false))
	c.app.SetStatus("Ready")
	c.app.SetStreaming(false)
	c.updateContextIndicator(newSess, c.executionModelID(), "", allowedToolsForSession(newSess))
}

func (c *Controller) createNewSessionState() (*SessionState, error) {
	if c.store == nil {
		return nil, fmt.Errorf("session store unavailable")
	}
	baseID := session.DetermineSessionID(c.workDir)
	timestamp := time.Now().Format("0102-150405")
	sessionID := fmt.Sprintf("%s-%s", baseID, timestamp)

	now := time.Now()
	storageSess := &storage.Session{
		ID:          sessionID,
		ProjectPath: c.workDir,
		CreatedAt:   now,
		LastActive:  now,
		Status:      storage.SessionStatusActive,
	}
	if err := c.store.CreateSession(storageSess); err != nil {
		return nil, err
	}

	return newSessionState(c.baseContext(), c.cfg, c.store, c.workDir, c.telemetry, c.modelMgr, sessionID, false, c.progressMgr, c.toastMgr)
}

// handleFileSelect processes file selection from the picker.
func (c *Controller) handleFileSelect(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.sessions) == 0 {
		return
	}

	fullPath := filepath.Join(c.workDir, path)
	if _, err := os.Stat(fullPath); err != nil {
		c.app.AddMessage(fmt.Sprintf("Error reading file: %v", err), "system")
		return
	}

	sess := c.sessions[c.currentSession]
	sess.PendingAttachments = append(sess.PendingAttachments, fullPath)
	if c.app != nil {
		c.app.SetStatusOverride(fmt.Sprintf("Queued attachment: %s", path), 3*time.Second)
	}
}

// handleShellCmd executes a shell command.
func (c *Controller) handleShellCmd(cmd string) string {
	// For now, just indicate what would be executed
	// Full shell execution would need sandboxing considerations
	return fmt.Sprintf("Would execute: %s", cmd)
}

// Stop gracefully stops the controller.
func (c *Controller) Stop() {
	// Stop telemetry bridge
	if c.telemetryBridge != nil {
		c.telemetryBridge.Stop()
	}

	// Close diagnostics collector
	if c.diagnostics != nil {
		c.diagnostics.Close()
	}

	c.mu.Lock()
	// Cancel all streaming sessions
	for _, sess := range c.sessions {
		if sess.Cancel != nil {
			sess.Cancel()
		}
	}
	c.mu.Unlock()
	c.app.Quit()
}

// saveLastMessage persists the most recent conversation message to storage.
func (c *Controller) saveLastMessage(sess *SessionState) {
	if c.store == nil || sess == nil || sess.Conversation == nil {
		return
	}
	messages := sess.Conversation.Messages
	if len(messages) == 0 {
		return
	}
	_ = sess.Conversation.SaveMessage(c.store, messages[len(messages)-1])
}

// handleReview reviews the current git diff in conversation.
