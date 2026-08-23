package skill

import (
	"fmt"
	"time"
)

// Skill represents a workflow guidance document that can be activated to provide
// structured instructions to the AI agent.
type Skill struct {
	// Core fields (Claude Code compatible)
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description"`
	AllowedTools []string `yaml:"allowed_tools,omitempty"`

	// Buckley extensions (optional)
	Phase        string `yaml:"phase,omitempty"`         // planning|execute|review
	RequiresTodo bool   `yaml:"requires_todo,omitempty"` // Enforce TODO creation
	Priority     int    `yaml:"priority,omitempty"`      // For conflict resolution
	Model        string `yaml:"model,omitempty"`         // Override model when active

	// TODO template for guidance
	TodoTemplate string `yaml:"todo_template,omitempty"`

	// Full markdown content (after frontmatter)
	Content string `yaml:"-"`

	// Metadata
	Source   string    `yaml:"-"` // Where loaded from (bundled|plugin|personal|project)
	FilePath string    `yaml:"-"` // Full path to SKILL.md
	LoadedAt time.Time `yaml:"-"`
}

// ActiveSkill represents a skill that is currently active in a conversation
type ActiveSkill struct {
	Skill       *Skill
	Scope       string // Description of activation context
	ActivatedAt time.Time
	ActivatedBy string // "model" | "user" | "phase"
}

// Getter methods for ActiveSkill

func (as *ActiveSkill) GetSkill() any {
	return as.Skill
}

func (as *ActiveSkill) GetScope() string {
	return as.Scope
}

func (as *ActiveSkill) GetActivatedAt() time.Time {
	return as.ActivatedAt
}

func (as *ActiveSkill) GetActivatedBy() string {
	return as.ActivatedBy
}

// Validate checks if a skill has required fields
func (s *Skill) Validate() error {
	if s.Name == "" {
		return ErrInvalidSkill{Field: "name", Reason: "name is required"}
	}
	if s.Description == "" {
		return ErrInvalidSkill{Field: "description", Reason: "description is required"}
	}
	if len(s.Name) > 64 {
		return ErrInvalidSkill{Field: "name", Reason: "name must be 64 characters or less"}
	}
	if len(s.Description) > 1024 {
		return ErrInvalidSkill{Field: "description", Reason: "description must be 1024 characters or less"}
	}
	return nil
}

// IsPhaseSkill returns true if this skill should auto-activate for a specific phase
func (s *Skill) IsPhaseSkill() bool {
	return s.Phase != ""
}

// HasToolRestrictions returns true if this skill restricts available tools
func (s *Skill) HasToolRestrictions() bool {
	return len(s.AllowedTools) > 0
}

// PhaseSkill interface represents a skill with phase information.
// This interface is implemented by *Skill and allows for abstraction
// in the orchestrator's skill management.
type PhaseSkill interface {
	GetName() string
	GetDescription() string
	GetContent() string
	GetAllowedTools() []string
	GetRequiresTodo() bool
	GetTodoTemplate() string
}

// Ensure *Skill implements PhaseSkill
var _ PhaseSkill = (*Skill)(nil)

// Getter methods for interface compatibility

func (s *Skill) GetName() string {
	return s.Name
}

func (s *Skill) GetDescription() string {
	return s.Description
}

func (s *Skill) GetContent() string {
	return s.Content
}

func (s *Skill) GetAllowedTools() []string {
	return s.AllowedTools
}

func (s *Skill) GetRequiresTodo() bool {
	return s.RequiresTodo
}

func (s *Skill) GetTodoTemplate() string {
	return s.TodoTemplate
}

func (s *Skill) GetPhase() string {
	return s.Phase
}

// Metadata keys used for skill tracking
const (
	MetadataKeyHasTodos = "has_todos"
)

// SkillConversationMetadata provides access to conversation metadata
type SkillConversationMetadata interface {
	GetMetadata(key string) any
}

// SkillTodoInfo provides the subset of skill info needed for TODO requirement checking
type SkillTodoInfo interface {
	GetRequiresTodo() bool
	GetTodoTemplate() string
}

// FormatTodoRequirement generates TODO requirement message for a skill
func FormatTodoRequirement(skill SkillTodoInfo, metadata SkillConversationMetadata) string {
	if !skill.GetRequiresTodo() {
		return ""
	}

	hasTodos := false
	if metadata != nil {
		if todosIface := metadata.GetMetadata(MetadataKeyHasTodos); todosIface != nil {
			hasTodos, _ = todosIface.(bool)
		}
	}

	var msg string
	if !hasTodos {
		msg = "⚠️ **This skill REQUIRES TODO tracking.** You must create a TODO list before proceeding.\n\n"
		if skill.GetTodoTemplate() != "" {
			msg += fmt.Sprintf("**Recommended TODO structure:**\n```\n%s\n```\n\n", skill.GetTodoTemplate())
		}
	} else if skill.GetTodoTemplate() != "" {
		msg = fmt.Sprintf("**Recommended TODO structure:**\n```\n%s\n```\n\n", skill.GetTodoTemplate())
	}

	return msg
}
