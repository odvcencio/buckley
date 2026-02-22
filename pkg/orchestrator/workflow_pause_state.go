package orchestrator

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/storage"
)

// SetActiveAgent updates the current agent label.
func (w *WorkflowManager) SetActiveAgent(agent string) {
	if w == nil {
		return
	}
	agent = strings.TrimSpace(agent)
	if agent == "" {
		w.stateMu.RLock()
		agent = string(w.currentPhase)
		w.stateMu.RUnlock()
	}
	w.stateMu.Lock()
	w.currentAgent = agent
	w.stateMu.Unlock()
}

// GetActiveAgent returns the current agent label.
func (w *WorkflowManager) GetActiveAgent() string {
	if w == nil {
		return ""
	}
	w.stateMu.RLock()
	defer w.stateMu.RUnlock()
	if w.currentAgent == "" {
		return string(w.currentPhase)
	}
	return w.currentAgent
}

// ClearPause resets pause metadata in memory and database.
func (w *WorkflowManager) ClearPause() {
	if w == nil {
		return
	}
	w.pauseMu.Lock()
	w.paused = false
	w.pauseReason = ""
	w.pauseQuestion = ""
	w.pauseAt = time.Time{}
	w.pauseMu.Unlock()

	// Clear pause state from database (copy sessionID under lock before I/O)
	w.stateMu.RLock()
	sessionID := w.sessionID
	w.stateMu.RUnlock()
	if w.store != nil && sessionID != "" {
		if err := w.store.UpdateSessionPauseState(sessionID, "", "", nil); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to clear pause state: %v\n", err)
		}
	}
}

// GetPauseInfo reports current pause state.
func (w *WorkflowManager) GetPauseInfo() (bool, string, string, time.Time) {
	if w == nil {
		return false, "", "", time.Time{}
	}
	w.pauseMu.RLock()
	defer w.pauseMu.RUnlock()
	if !w.paused {
		return false, "", "", time.Time{}
	}
	return true, w.pauseReason, w.pauseQuestion, w.pauseAt
}

// RestorePauseStateFromSession loads pause state from the database session.
// Call this after SetSessionID to restore pause state across restarts.
func (w *WorkflowManager) RestorePauseStateFromSession() {
	if w == nil || w.store == nil {
		return
	}

	w.stateMu.RLock()
	sessionID := w.sessionID
	w.stateMu.RUnlock()
	if sessionID == "" {
		return
	}

	session, err := w.store.GetSession(sessionID)
	if err != nil || session == nil {
		return
	}

	if session.Status == storage.SessionStatusPaused && (session.PauseReason != "" || session.PauseQuestion != "") {
		w.pauseMu.Lock()
		w.paused = true
		w.pauseReason = session.PauseReason
		w.pauseQuestion = session.PauseQuestion
		if session.PausedAt != nil {
			w.pauseAt = *session.PausedAt
		}
		w.pauseMu.Unlock()

		fmt.Fprintf(os.Stderr, "▶️  Workflow paused: %s\n", session.PauseReason)
		if session.PauseQuestion != "" {
			fmt.Fprintf(os.Stderr, "   %s\n", session.PauseQuestion)
		}
	}
}

// GetCurrentPlan returns the active plan reference (if any).
func (w *WorkflowManager) GetCurrentPlan() *Plan {
	if w == nil {
		return nil
	}
	w.stateMu.RLock()
	defer w.stateMu.RUnlock()
	return w.planRef
}

// GetActivitySummaries returns recent tool activity summaries.
func (w *WorkflowManager) GetActivitySummaries() []string {
	if w == nil || w.activityTracker == nil {
		return nil
	}
	var summaries []string
	for _, group := range w.activityTracker.GetGroups() {
		if group.Summary != "" {
			summaries = append(summaries, group.Summary)
		}
	}
	return summaries
}
