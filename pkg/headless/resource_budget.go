package headless

import (
	"errors"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/ipc/command"
	"m31labs.dev/buckley/pkg/storage"
)

func runnerWorkingMemoryBytes(cfg *config.Config, durable bool) int {
	bytes := config.DefaultSessionMemoryBytes
	if cfg != nil {
		bytes = cfg.Resources.SessionMemoryBytes
	}
	if !durable {
		bytes /= 2
	}
	return bytes
}

func legacyQueuedCommandBytes(cmd command.SessionCommand) int {
	return 512 + len(cmd.SessionID) + len(cmd.ID) + len(cmd.Type) + len(cmd.Content) + len(cmd.TaskIntent) + len(cmd.AcceptedBy)
}

func (r *Runner) boundWorkingSet() error {
	changed, err := r.conv.BoundWorkingSet(runnerWorkingMemoryBytes(r.config, r.durable))
	if changed && r.continuation != nil {
		r.continuation.Reset()
	}
	return err
}

func (r *Runner) pauseForResourceBudget(err error) {
	if !storage.IsStorageBudgetError(err) && !errors.Is(err, storage.ErrSessionMemoryBudget) {
		return
	}
	if r.State() == StatePaused || r.State() == StateStopped {
		return
	}
	r.setState(StatePaused)
	if saveErr := r.store.SetSessionStatus(r.sessionID, storage.SessionStatusPaused); saveErr != nil {
		r.emitDurableJournalError("persist resource budget pause", saveErr)
	}
	r.mu.Lock()
	r.session.Status = storage.SessionStatusPaused
	r.mu.Unlock()
	r.emitDurableJournalError("resource budget paused session", err)
}
