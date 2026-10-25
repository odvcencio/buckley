package tui

import (
	"fmt"

	"m31labs.dev/buckley/pkg/skill"
	"m31labs.dev/buckley/pkg/storage"
)

func restoreSessionSkills(registry *skill.Registry, state *skill.RuntimeState, store *storage.Store, sessionID string) error {
	if store == nil {
		return nil
	}
	active, err := store.GetActiveSessionSkills(sessionID)
	if err != nil {
		return fmt.Errorf("load session %s skills: %w", sessionID, err)
	}
	for _, saved := range active {
		if err := registry.Activate(saved.SkillName, saved.Scope, saved.ActivatedBy); err != nil {
			return fmt.Errorf("restore session %s skill %q: %w", sessionID, saved.SkillName, err)
		}
		registry.GetActiveSkill(saved.SkillName).ActivatedAt = saved.ActivatedAt
	}
	if allowed := registry.ToolFilter(); allowed != nil {
		state.SetToolFilter(allowed)
	}
	registry.SetPersistence(sessionID, store)
	return nil
}
