package orchestrator

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/artifact"
	"github.com/odvcencio/buckley/pkg/tool"
	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

// RecordIntent records an intent statement
func (w *WorkflowManager) RecordIntent(intent tool.Intent) {
	w.intentHistory.Add(intent)
}

// GetLatestIntent returns the most recent intent
func (w *WorkflowManager) GetLatestIntent() *tool.Intent {
	return w.intentHistory.GetLatest()
}

// GetActivityGroups returns current activity groups
func (w *WorkflowManager) GetActivityGroups() []tool.ActivityGroup {
	return w.activityTracker.GetGroups()
}

// RecordToolCall records a tool call for activity tracking
// AuthorizeToolCall inspects tool usage for risky operations.
func (w *WorkflowManager) AuthorizeToolCall(t tool.Tool, params map[string]any) error {
	if w == nil || t == nil {
		return nil
	}

	if strings.EqualFold(t.Name(), "run_shell") {
		if cmd, ok := params["command"].(string); ok {
			// Skip authorization for safe commands (tests, builds, etc.)
			if isSafeCommand(cmd) {
				return nil
			}
			// Check for elevation requirements
			if requiresElevation(cmd) {
				return w.pauseWorkflow("Permission Escalation", fmt.Sprintf("Shell command requires elevated privileges: %s", summarizeCommand(cmd)))
			}
		}
	}
	return nil
}

// RecordToolCall records a tool call for activity tracking.
func (w *WorkflowManager) RecordToolCall(t tool.Tool, params map[string]any, result any, startTime, endTime time.Time) {
	if w == nil || w.activityTracker == nil || t == nil {
		return
	}

	var converted *builtin.Result
	switch v := result.(type) {
	case *builtin.Result:
		converted = v
	case builtin.Result:
		converted = &v
	}

	w.activityTracker.RecordCall(t, params, converted, startTime, endTime)
}

func (w *WorkflowManager) pauseWorkflow(reason, question string) error {
	if w == nil {
		return &WorkflowPauseError{Reason: reason, Question: question}
	}

	fmt.Fprintf(os.Stderr, "⚠️  %s: %s\n", reason, question)

	now := time.Now()
	w.pauseMu.Lock()
	w.paused = true
	w.pauseReason = reason
	w.pauseQuestion = question
	w.pauseAt = now
	w.pauseMu.Unlock()

	// Persist pause state to database for recovery across restarts
	if w.store != nil && w.sessionID != "" {
		if err := w.store.UpdateSessionPauseState(w.sessionID, reason, question, &now); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to persist pause state: %v\n", err)
		}
	}

	if w.executionTracker != nil {
		pause := artifact.ExecutionPause{
			Reason:    reason,
			Question:  question,
			Timestamp: now,
		}
		if err := w.executionTracker.AddPause(pause); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to record execution pause: %v\n", err)
		}
	}

	return &WorkflowPauseError{Reason: reason, Question: question}
}

func isSafeCommand(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return true
	}

	// Safe command prefixes that don't require authorization
	safeCommands := []string{
		"go test",
		"go build",
		"go run",
		"go mod",
		"go get",
		"npm test",
		"npm run",
		"npm install",
		"cargo test",
		"cargo build",
		"make test",
		"pytest",
		"python -m pytest",
		"jest",
		"mocha",
		"ls",
		"cat",
		"grep",
		"find",
		"echo",
		"pwd",
		"which",
		"git ",
		"gh ",
	}

	for _, safe := range safeCommands {
		if strings.HasPrefix(cmd, safe) {
			return true
		}
	}

	return false
}

func requiresElevation(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return false
	}

	segments := splitShellSegments(cmd)
	for _, segment := range segments {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		if strings.HasPrefix(segment, "sudo ") || segment == "sudo" {
			return true
		}
		if strings.HasPrefix(segment, "sudo\t") {
			return true
		}
	}
	return false
}

func splitShellSegments(cmd string) []string {
	separators := []string{"&&", "||", ";", "\n"}
	segments := []string{cmd}
	for _, sep := range separators {
		var next []string
		for _, segment := range segments {
			next = append(next, strings.Split(segment, sep)...)
		}
		segments = next
	}
	return segments
}

func summarizeCommand(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	const maxLen = 80
	if len(cmd) <= maxLen {
		return cmd
	}
	return cmd[:maxLen] + "…"
}
