package tui

import (
	"fmt"
	"sort"
)

// boundIdleHistoryLocked shares one working-window budget among inactive views.
// Selected, busy, queued and failed-persistence sessions keep their bodies.
// The session runtime, undo records and provider continuation are never evicted.
func (c *Controller) boundIdleHistoryLocked() error {
	limit := sessionWorkingMemoryBytes(c.cfg)
	if limit <= 0 || c.store == nil {
		return nil
	}
	type candidate struct {
		session *SessionState
		bytes   int
	}
	var candidates []candidate
	resident := 0
	for i, sess := range c.sessions {
		if i == c.currentSession || sess.Streaming || sess.Compacting || len(sess.MessageQueue) > 0 || sess.persistenceErr != nil || sess.historyReference != nil {
			continue
		}
		bytes := sess.Conversation.WorkingSetBytes()
		if bytes > 0 {
			candidates = append(candidates, candidate{sess, bytes})
			resident += bytes
		}
	}
	if resident <= limit {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].session.historyUse < candidates[j].session.historyUse
	})
	var firstErr error
	for _, entry := range candidates {
		if resident <= limit {
			break
		}
		reference, err := entry.session.Conversation.ReleaseWorkingSet(c.store)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("session %s: %w", entry.session.ID, err)
			}
			continue
		}
		if reference != nil {
			entry.session.historyReference = reference
		}
		// A nil reference pins unsaved data outside the recoverable cache.
		resident -= entry.bytes
	}
	return firstErr
}
