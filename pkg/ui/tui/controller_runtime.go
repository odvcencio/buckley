package tui

import (
	"fmt"

	"m31labs.dev/buckley/pkg/storage"
)

func (s *SessionState) loadRuntime(store *storage.Store) error {
	if s.runtimeLoader == nil {
		return nil
	}
	if store == nil {
		return fmt.Errorf("session storage unavailable")
	}
	loaded, err := s.runtimeLoader(store)
	if err != nil {
		return err
	}
	s.Conversation = loaded.Conversation
	s.ToolRegistry = loaded.ToolRegistry
	s.HookCloser = loaded.HookCloser
	s.SkillRegistry = loaded.SkillRegistry
	s.SkillState = loaded.SkillState
	s.deferredHistory = false
	s.runtimeLoader = nil
	return nil
}

// loadSessionRuntimeLocked must be called with c.mu held.
func (c *Controller) loadSessionRuntimeLocked(sess *SessionState) error {
	if sess.runtimeLoader == nil {
		return nil
	}
	if err := sess.loadRuntime(c.store); err != nil {
		return err
	}
	configureSessionSubagentPolicy(sess, c.evaluator)
	c.configureSessionPermissions(sess)
	return nil
}
