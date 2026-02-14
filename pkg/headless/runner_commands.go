package headless

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func (r *Runner) processSlashCommand(content string) error {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "/") {
		return fmt.Errorf("not a slash command")
	}

	fields := strings.Fields(content)
	if len(fields) == 0 {
		return fmt.Errorf("empty slash command")
	}
	cmd := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	args := fields[1:]
	if strings.Contains(cmd, "/") || strings.Contains(cmd, "\\") {
		// Treat absolute/relative paths as regular input, not commands.
		return r.processUserInput(content)
	}

	switch cmd {
	case "clear":
		r.conv.Clear()
		return r.persistSystemMessage("Conversation cleared.")
	case "plan":
		return r.runPlanCommand(args)
	case "execute":
		return r.runExecuteCommand(args)
	case "status":
		return r.runStatusCommand()
	case "plans":
		return r.runPlansCommand()
	case "resume":
		return r.runResumePlanCommand(args)
	case "workflow":
		return r.runWorkflowCommand(args)
	default:
		return fmt.Errorf("unknown command: %s", cmd)
	}
}

func (r *Runner) processApproval(content string) error {
	var resp ApprovalResponse
	if err := json.Unmarshal([]byte(content), &resp); err != nil {
		// Try simple format: "approve" or "reject"
		content = strings.ToLower(strings.TrimSpace(content))
		r.mu.RLock()
		pending := r.pendingApproval
		r.mu.RUnlock()

		if pending == nil {
			return fmt.Errorf("no pending approval")
		}

		resp = ApprovalResponse{
			ID:       pending.ID,
			Approved: content == "approve" || content == "yes" || content == "y",
		}
	}

	select {
	case r.approvalChan <- resp:
		return nil
	default:
		return fmt.Errorf("no pending approval")
	}
}

func (r *Runner) pause() error {
	r.setState(StatePaused)
	return nil
}

func (r *Runner) resume() error {
	if r.State() != StatePaused {
		return fmt.Errorf("session not paused")
	}
	r.setState(StateIdle)
	return nil
}

func (r *Runner) setState(state RunnerState) {
	r.mu.Lock()
	oldState := r.state
	r.state = state
	r.lastActive = time.Now()
	r.mu.Unlock()

	if oldState != state {
		r.emit(RunnerEvent{
			Type:      EventStateChanged,
			SessionID: r.sessionID,
			Timestamp: time.Now(),
			Data: map[string]any{
				"state":     string(state),
				"prevState": string(oldState),
			},
		})
	}
}

func (r *Runner) emit(event RunnerEvent) {
	if r.emitter != nil {
		r.emitter.Emit(event)
	}
}

func (r *Runner) emitError(msg string, err error) {
	r.setState(StateError)
	r.emit(RunnerEvent{
		Type:      EventError,
		SessionID: r.sessionID,
		Timestamp: time.Now(),
		Data: map[string]any{
			"message": msg,
			"error":   err.Error(),
		},
	})
}

func (r *Runner) persistSystemMessage(content string) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	if r.conv == nil || r.store == nil {
		return nil
	}
	r.conv.AddSystemMessage(content)
	msg := r.conv.Messages[len(r.conv.Messages)-1]
	if err := r.conv.SaveMessage(r.store, msg); err != nil {
		r.emitError("failed to save system message", err)
		return fmt.Errorf("persisting system message: %w", err)
	}
	return nil
}
