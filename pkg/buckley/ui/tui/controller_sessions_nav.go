package tui

import (
	"fmt"
	"strings"
)

// listSessions shows all active sessions for this project.
func (c *Controller) listSessions() {
	if !c.mu.TryLock() {
		c.app.AddMessage("Session list busy; try again.", "system")
		return
	}
	current := c.currentSession
	snapshots := make([]struct {
		id        string
		streaming bool
	}, 0, len(c.sessions))
	for _, sess := range c.sessions {
		snapshots = append(snapshots, struct {
			id        string
			streaming bool
		}{
			id:        sess.ID,
			streaming: sess.Streaming,
		})
	}
	c.mu.Unlock()

	if len(snapshots) == 0 {
		c.app.AddMessage("No active sessions", "system")
		return
	}

	var sb strings.Builder
	sb.WriteString("Active sessions:\n")
	for i, sess := range snapshots {
		marker := "  "
		if i == current {
			marker = "→ "
		}
		status := ""
		if sess.streaming {
			status = " (streaming...)"
		}
		sb.WriteString(fmt.Sprintf("%s[%d] %s%s\n", marker, i+1, sess.id, status))
	}
	sb.WriteString("\nUse /next or /prev to switch (Alt+Right/Left)")
	sb.WriteString("\nUse /sessions complete <id|index|all> to archive sessions")
	c.app.AddMessage(sb.String(), "system")
}

// nextSession switches to the next session.
func (c *Controller) nextSession() {
	c.mu.Lock()
	if len(c.sessions) <= 1 {
		c.mu.Unlock()
		c.app.AddMessage("No other sessions to switch to", "system")
		return
	}

	c.currentSession = (c.currentSession + 1) % len(c.sessions)
	c.switchToSessionLocked(c.currentSession)
	sess := c.sessions[c.currentSession]
	c.mu.Unlock()

	c.updateContextIndicator(sess, c.executionModelID(), "", allowedToolsForSession(sess))
}

// prevSession switches to the previous session.
func (c *Controller) prevSession() {
	c.mu.Lock()
	if len(c.sessions) <= 1 {
		c.mu.Unlock()
		c.app.AddMessage("No other sessions to switch to", "system")
		return
	}

	c.currentSession = (c.currentSession - 1 + len(c.sessions)) % len(c.sessions)
	c.switchToSessionLocked(c.currentSession)
	sess := c.sessions[c.currentSession]
	c.mu.Unlock()

	c.updateContextIndicator(sess, c.executionModelID(), "", allowedToolsForSession(sess))
}

// switchToSessionLocked loads a session by index.
// Must be called with c.mu held.
func (c *Controller) switchToSessionLocked(idx int) {
	if idx < 0 || idx >= len(c.sessions) {
		return
	}

	sess := c.sessions[idx]
	c.conversation = sess.Conversation
	c.registry = sess.ToolRegistry
	c.app.SetSessionID(sess.ID)

	statusMsg := fmt.Sprintf("Switched to session: %s", sess.ID)
	if sess.Streaming {
		statusMsg += " (response in progress)"
	}
	c.app.SetChatMessages(c.buildChatMessages(sess, statusMsg, false))

	if sess.Streaming {
		c.app.SetStatus("Streaming...")
	} else {
		c.app.SetStatus("Ready")
	}
	c.app.SetStreaming(sess.Streaming)

	// Reset sidebar signals so stale data from previous session doesn't persist.
	// The telemetry bridge will repopulate as new events arrive for the active session.
	sigs := c.app.SidebarSignals()
	sigs.CurrentTask.Set("")
	sigs.TaskProgress.Set(0)
	sigs.RunningTools.Set(nil)
	sigs.ToolHistory.Set(nil)
	sigs.ActiveTouches.Set(nil)
	sigs.RecentFiles.Set(nil)
	sigs.RLMStatus.Set(nil)
	sigs.RLMScratchpad.Set(nil)
	sigs.CircuitStatus.Set(nil)

	c.showPendingApprovals(sess.ID)
}
