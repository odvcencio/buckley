package tui

import (
	"context"
	"fmt"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/storage"
)

func sessionWorkingMemoryBytes(cfg *config.Config) int {
	if cfg == nil {
		return config.DefaultSessionMemoryBytes / 2
	}
	return cfg.Resources.SessionMemoryBytes / 2
}

func (c *Controller) boundSessionWorkingSetLocked(sess *SessionState) error {
	changed, err := sess.Conversation.BoundWorkingSet(sessionWorkingMemoryBytes(c.cfg))
	if changed {
		if sess.continuation != nil {
			sess.continuation.Reset()
		}
		if err != nil {
			sess.historyVersion++
			clear(sess.undoStack)
			clear(sess.redoStack)
			sess.undoStack, sess.redoStack = nil, nil
		} else {
			c.pruneUndoHistoryLocked(sess)
		}
	}
	return err
}

func (c *Controller) checkStorageBudget(ctx context.Context) error {
	if c.store == nil {
		return nil
	}
	return storage.CheckStorageHeadroom(ctx, c.store.DB(), 0)
}

func queuedMessageBytes(messages []QueuedMessage) int {
	bytes := 0
	for _, msg := range messages {
		bytes += 512 + len(msg.Content)
	}
	return bytes
}

func (c *Controller) checkQueueMemory(sess *SessionState, text string) error {
	limit := sessionWorkingMemoryBytes(c.cfg) / 4
	if limit > 0 && (len(sess.MessageQueue) >= 64 || queuedMessageBytes(sess.MessageQueue)+512+len(text) > limit) {
		return fmt.Errorf("%w: queued input is full", storage.ErrSessionMemoryBudget)
	}
	return nil
}

func undoMemoryBytes(records []turnUndoRecord) int {
	bytes := 0
	for _, record := range records {
		bytes += 256 + len(record.beforeTree) + len(record.afterTree)
		for _, msg := range record.messages {
			bytes += conversation.MessageMemoryBytes(msg)
		}
	}
	return bytes
}
