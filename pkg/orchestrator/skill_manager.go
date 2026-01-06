package orchestrator

import (
	"fmt"

	"github.com/odvcencio/buckley/pkg/skill"
)

// SkillManager handles skill activation during workflow phases
type SkillManager struct {
	registry SkillRegistry
	conv     SkillConversation
}

// SkillRegistry interface for skill operations
type SkillRegistry interface {
	GetByPhase(phase string) []skill.PhaseSkill
	Activate(name, scope, activatedBy string) error
	Deactivate(name string) error
	IsActive(name string) bool
	Get(name string) any
	GetDescriptions() string
}

// SkillConversation interface for injecting skill content
type SkillConversation interface {
	AddSystemMessage(content string)
	SetToolFilter(allowedTools []string)
	ClearToolFilter()
	GetMetadata(key string) any
	SetMetadata(key string, value any)
}

// NewSkillManager creates a new skill manager
func NewSkillManager(registry SkillRegistry, conv SkillConversation) *SkillManager {
	return &SkillManager{
		registry: registry,
		conv:     conv,
	}
}

// ActivatePhaseSkills activates all skills tagged for a specific phase
func (sm *SkillManager) ActivatePhaseSkills(phase string) error {
	if sm.registry == nil {
		return nil // Skills not configured
	}

	skills := sm.registry.GetByPhase(phase)
	if len(skills) == 0 {
		return nil // No skills for this phase
	}

	var errors []string
	for _, skillIface := range skills {
		skill := skillIface
		if sm.registry.IsActive(skill.GetName()) {
			continue // Already active
		}

		// Activate the skill
		scope := fmt.Sprintf("%s phase", phase)
		if err := sm.registry.Activate(skill.GetName(), scope, "phase"); err != nil {
			errors = append(errors, fmt.Sprintf("failed to activate %s: %v", skill.GetName(), err))
			continue
		}

		// Inject skill content
		if err := sm.injectSkillContent(skill, scope); err != nil {
			errors = append(errors, fmt.Sprintf("failed to inject %s content: %v", skill.GetName(), err))
			continue
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("skill activation errors: %v", errors)
	}

	return nil
}

// injectSkillContent injects skill instructions into the conversation
func (sm *SkillManager) injectSkillContent(phaseSkill skill.PhaseSkill, scope string) error {
	if sm.conv == nil {
		return fmt.Errorf("conversation not configured")
	}

	// Build activation message
	msg := fmt.Sprintf("# Skill Activated: %s\n\n", phaseSkill.GetName())
	msg += fmt.Sprintf("**Scope:** %s\n\n", scope)

	// Add TODO requirement message if applicable
	msg += skill.FormatTodoRequirement(phaseSkill, sm.conv)

	// Add skill content
	msg += phaseSkill.GetContent()

	// Inject into conversation
	sm.conv.AddSystemMessage(msg)

	// Apply tool restrictions if specified
	if allowedTools := phaseSkill.GetAllowedTools(); len(allowedTools) > 0 {
		sm.conv.SetToolFilter(allowedTools)
	}

	return nil
}

// DeactivatePhaseSkills deactivates all skills for a phase
func (sm *SkillManager) DeactivatePhaseSkills(phase string) error {
	if sm.registry == nil {
		return nil
	}

	skills := sm.registry.GetByPhase(phase)
	var errors []string

	for _, skillIface := range skills {
		skill := skillIface
		if !sm.registry.IsActive(skill.GetName()) {
			continue // Not active
		}

		if err := sm.registry.Deactivate(skill.GetName()); err != nil {
			errors = append(errors, fmt.Sprintf("failed to deactivate %s: %v", skill.GetName(), err))
		}
	}

	// Clear tool filter only if no remaining active skills have tool restrictions
	if sm.conv != nil {
		hasActiveFilters := sm.hasActiveToolFilters()
		if !hasActiveFilters {
			sm.conv.ClearToolFilter()
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("skill deactivation errors: %v", errors)
	}

	return nil
}

// hasActiveToolFilters checks if any active skills have tool restrictions
func (sm *SkillManager) hasActiveToolFilters() bool {
	if sm.registry == nil {
		return false
	}

	// Check all known phases for active skills with tool restrictions
	phases := []string{"planning", "execute", "review", ""} // "" for phase-less skills
	skillsSeen := make(map[string]bool)

	for _, phase := range phases {
		skills := sm.registry.GetByPhase(phase)
		for _, skill := range skills {
			name := skill.GetName()
			if skillsSeen[name] {
				continue // Already checked this skill
			}
			skillsSeen[name] = true

			if sm.registry.IsActive(name) && len(skill.GetAllowedTools()) > 0 {
				return true
			}
		}
	}

	return false
}

// GetSkillsDescription returns formatted description of all skills for system prompt
func (sm *SkillManager) GetSkillsDescription() string {
	if sm.registry == nil {
		return ""
	}
	return sm.registry.GetDescriptions()
}
