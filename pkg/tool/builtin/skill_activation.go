package builtin

import (
	"fmt"
	"strings"

	"github.com/odvcencio/buckley/pkg/skill"
)

// SkillActivationTool allows the AI to activate and deactivate workflow skills
type SkillActivationTool struct {
	Registry       SkillRegistry
	Conversation   SkillConversation
	CurrentSession string
}

// SkillRegistry interface for skill management operations
type SkillRegistry interface {
	Get(name string) any // Returns *skill.Skill
	Activate(name, scope, activatedBy string) error
	Deactivate(name string) error
	IsActive(name string) bool
	GetActive(name string) any // Returns *skill.ActiveSkill
	ListActive() []any         // Returns []*skill.ActiveSkill
}

// SkillConversation interface for injecting skill content into conversation
type SkillConversation interface {
	AddSystemMessage(content string)
	SetToolFilter(allowedTools []string)
	ClearToolFilter()
	GetMetadata(key string) any
	SetMetadata(key string, value any)
}

func (t *SkillActivationTool) Name() string {
	return "activate_skill"
}

func (t *SkillActivationTool) Description() string {
	return "Activate or deactivate workflow skills to receive structured guidance. Use this when you recognize a pattern that matches an available skill (TDD, debugging, planning, etc.). Skills can also be manually activated by the user via /skill command or automatically during phase transitions."
}

func (t *SkillActivationTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"action": {
				Type:        "string",
				Description: "Action to perform: 'activate' or 'deactivate'",
			},
			"skill": {
				Type:        "string",
				Description: "Name of the skill to activate/deactivate (e.g., 'test-driven-development', 'systematic-debugging')",
			},
			"scope": {
				Type:        "string",
				Description: "Description of the activation context (e.g., 'implementing user auth', 'debugging login flow'). Optional for deactivate.",
			},
		},
		Required: []string{"action", "skill"},
	}
}

func (t *SkillActivationTool) Execute(params map[string]any) (*Result, error) {
	action, ok := params["action"].(string)
	if !ok {
		return nil, fmt.Errorf("action parameter is required and must be a string")
	}

	skillName, ok := params["skill"].(string)
	if !ok {
		return nil, fmt.Errorf("skill parameter is required and must be a string")
	}

	switch action {
	case "activate":
		return t.activate(skillName, params)
	case "deactivate":
		return t.deactivate(skillName)
	default:
		return nil, fmt.Errorf("invalid action: %s (must be 'activate' or 'deactivate')", action)
	}
}

func (t *SkillActivationTool) activate(skillName string, params map[string]any) (*Result, error) {
	// Check if already active
	if t.Registry.IsActive(skillName) {
		return &Result{
			Success: false,
			Data: map[string]any{
				"message": fmt.Sprintf("Skill '%s' is already active", skillName),
			},
		}, nil
	}

	// Get skill from registry
	skillIface := t.Registry.Get(skillName)
	if skillIface == nil {
		return nil, fmt.Errorf("skill not found: %s", skillName)
	}

	// Extract skill struct (we'll type assert in the actual integration)
	// For now, we'll use reflection-style access through the interface
	skillObj := skillIface.(interface {
		GetName() string
		GetDescription() string
		GetContent() string
		GetAllowedTools() []string
		GetRequiresTodo() bool
		GetTodoTemplate() string
	})

	// Get scope from params
	scope, _ := params["scope"].(string)
	if scope == "" {
		scope = "model-requested"
	}

	// Activate in registry
	if err := t.Registry.Activate(skillName, scope, "model"); err != nil {
		return nil, fmt.Errorf("failed to activate skill: %w", err)
	}

	// Build activation message
	var msg strings.Builder
	msg.WriteString(fmt.Sprintf("# Skill Activated: %s\n\n", skillObj.GetName()))
	msg.WriteString(fmt.Sprintf("**Scope:** %s\n\n", scope))

	// Add TODO requirement message if applicable
	msg.WriteString(skill.FormatTodoRequirement(skillObj, t.Conversation))

	// Add skill content
	msg.WriteString(skillObj.GetContent())

	// Inject into conversation as system message
	if t.Conversation != nil {
		t.Conversation.AddSystemMessage(msg.String())
	}

	// Apply tool restrictions if specified
	if allowedTools := skillObj.GetAllowedTools(); len(allowedTools) > 0 {
		if t.Conversation != nil {
			t.Conversation.SetToolFilter(allowedTools)
		}
	}

	return &Result{
		Success: true,
		Data: map[string]any{
			"skill":         skillName,
			"scope":         scope,
			"message":       fmt.Sprintf("Skill '%s' activated. Follow its instructions precisely.", skillName),
			"content":       msg.String(),
			"allowed_tools": skillObj.GetAllowedTools(),
		},
	}, nil
}

func (t *SkillActivationTool) deactivate(skillName string) (*Result, error) {
	// Check if active
	if !t.Registry.IsActive(skillName) {
		return &Result{
			Success: false,
			Data: map[string]any{
				"message": fmt.Sprintf("Skill '%s' is not currently active", skillName),
			},
		}, nil
	}

	// Deactivate in registry
	if err := t.Registry.Deactivate(skillName); err != nil {
		return nil, fmt.Errorf("failed to deactivate skill: %w", err)
	}

	// Clear tool filter (will need to check if other skills have restrictions)
	activeSkills := t.Registry.ListActive()
	if len(activeSkills) == 0 {
		t.Conversation.ClearToolFilter()
	}

	return &Result{
		Success: true,
		Data: map[string]any{
			"skill":   skillName,
			"message": fmt.Sprintf("Skill '%s' deactivated", skillName),
		},
	}, nil
}
