package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/odvcencio/buckley/pkg/storage"
)

func (c *Controller) handleSessionsCommand(args []string) {
	if len(args) == 0 {
		c.listSessions()
		return
	}
	sub := strings.ToLower(strings.TrimSpace(args[0]))
	switch sub {
	case "complete", "close", "archive":
		c.completeSessionsCommand(args[1:])
	default:
		c.app.AddMessage("Usage: /sessions [complete <id|index|all|current>]", "system")
	}
}

func (c *Controller) completeSessionsCommand(args []string) {
	if len(args) == 0 {
		c.app.AddMessage("Usage: /sessions complete <id|index|all|current>", "system")
		return
	}
	if c.store == nil {
		c.app.AddMessage("Session store unavailable.", "system")
		return
	}
	target := strings.TrimSpace(args[0])
	switch strings.ToLower(target) {
	case "all":
		c.completeAllSessions()
		return
	case "current":
		c.completeCurrentSession()
		return
	}

	if idx, err := strconv.Atoi(target); err == nil {
		c.completeSessionByIndex(idx - 1)
		return
	}
	c.completeSessionByID(target)
}

func (c *Controller) completeAllSessions() {
	if !c.mu.TryLock() {
		c.app.AddMessage("Session list busy; try again.", "system")
		return
	}
	if len(c.sessions) <= 1 {
		c.mu.Unlock()
		c.app.AddMessage("No other sessions to complete.", "system")
		return
	}

	currentIdx := c.currentSession
	current := c.sessions[currentIdx]
	toComplete := make([]*SessionState, 0, len(c.sessions)-1)
	for i, sess := range c.sessions {
		if i == currentIdx {
			continue
		}
		toComplete = append(toComplete, sess)
	}
	c.sessions = []*SessionState{current}
	c.currentSession = 0
	c.conversation = current.Conversation
	c.registry = current.ToolRegistry
	c.mu.Unlock()

	failed := 0
	for _, sess := range toComplete {
		if err := c.store.SetSessionStatus(sess.ID, storage.SessionStatusCompleted); err != nil {
			failed++
		}
	}
	if failed > 0 {
		c.app.AddMessage(fmt.Sprintf("Completed %d sessions (%d failed).", len(toComplete)-failed, failed), "system")
	} else {
		c.app.AddMessage(fmt.Sprintf("Completed %d sessions.", len(toComplete)), "system")
	}
	c.updateContextIndicator(current, c.executionModelID(), "", allowedToolsForSession(current))
}

func (c *Controller) completeCurrentSession() {
	c.mu.Lock()
	idx := c.currentSession
	c.mu.Unlock()
	c.completeSessionByIndex(idx)
}

func (c *Controller) completeSessionByID(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		c.app.AddMessage("Session ID required.", "system")
		return
	}
	if !c.mu.TryLock() {
		c.app.AddMessage("Session list busy; try again.", "system")
		return
	}
	idx := -1
	for i, sess := range c.sessions {
		if sess.ID == sessionID {
			idx = i
			break
		}
	}
	c.mu.Unlock()
	if idx < 0 {
		c.app.AddMessage("Session not found: "+sessionID, "system")
		return
	}
	c.completeSessionByIndex(idx)
}

func (c *Controller) completeSessionByIndex(idx int) {
	if !c.mu.TryLock() {
		c.app.AddMessage("Session list busy; try again.", "system")
		return
	}
	if idx < 0 || idx >= len(c.sessions) {
		c.mu.Unlock()
		c.app.AddMessage("Session index out of range.", "system")
		return
	}

	sess := c.sessions[idx]
	wasCurrent := idx == c.currentSession
	onlySession := len(c.sessions) == 1

	// Cancel any active streaming on this session
	if sess.Cancel != nil {
		sess.Cancel()
	}

	if !onlySession {
		c.sessions = append(c.sessions[:idx], c.sessions[idx+1:]...)
		if idx < c.currentSession {
			c.currentSession--
		}
		if wasCurrent {
			if idx >= len(c.sessions) {
				idx = len(c.sessions) - 1
			}
			c.currentSession = idx
			c.switchToSessionLocked(idx)
		}
	}
	c.mu.Unlock()

	if err := c.store.SetSessionStatus(sess.ID, storage.SessionStatusCompleted); err != nil {
		c.app.AddMessage("Failed to complete session "+sess.ID+": "+err.Error(), "system")
	} else {
		c.app.AddMessage("Session completed: "+sess.ID, "system")
	}

	if wasCurrent {
		if onlySession {
			newSess, err := c.createNewSessionState()
			if err != nil {
				c.app.AddMessage("Error creating session: "+err.Error(), "system")
				return
			}
			c.mu.Lock()
			c.sessions = []*SessionState{newSess}
			c.currentSession = 0
			c.conversation = newSess.Conversation
			c.registry = newSess.ToolRegistry
			c.mu.Unlock()

			c.app.SetSessionID(newSess.ID)
			c.app.SetChatMessages(c.buildChatMessages(newSess, "New session started: "+newSess.ID, false))
			c.app.SetStatus("Ready")
			c.updateContextIndicator(newSess, c.executionModelID(), "", allowedToolsForSession(newSess))
			return
		}
		c.mu.Lock()
		if c.currentSession >= 0 && c.currentSession < len(c.sessions) {
			current := c.sessions[c.currentSession]
			c.mu.Unlock()
			c.updateContextIndicator(current, c.executionModelID(), "", allowedToolsForSession(current))
		} else {
			c.mu.Unlock()
		}
	}
}
