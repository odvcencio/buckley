package skill

import (
	"fmt"
	"sync"
	"time"
)

// Registry manages skill discovery, loading, and activation
type Registry struct {
	mu     sync.RWMutex
	skills map[string]*Skill       // All discovered skills by name
	active map[string]*ActiveSkill // Currently active skills
	loader *Loader
}

// NewRegistry creates a new skill registry
func NewRegistry() *Registry {
	return &Registry{
		skills: make(map[string]*Skill),
		active: make(map[string]*ActiveSkill),
		loader: NewLoader(),
	}
}

// LoadAll loads skills from all standard locations in precedence order
func (r *Registry) LoadAll() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Load in precedence order (later overrides earlier)
	if err := r.loader.LoadBundled(r.skills); err != nil {
		return fmt.Errorf("failed to load bundled skills: %w", err)
	}

	if err := r.loader.LoadFromPlugins(r.skills); err != nil {
		return fmt.Errorf("failed to load plugin skills: %w", err)
	}

	if err := r.loader.LoadPersonal(r.skills); err != nil {
		return fmt.Errorf("failed to load personal skills: %w", err)
	}

	if err := r.loader.LoadProject(r.skills); err != nil {
		return fmt.Errorf("failed to load project skills: %w", err)
	}

	return nil
}

// Get retrieves a skill by name (returns any for interface compatibility)
func (r *Registry) Get(name string) any {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.skills[name]
}

// GetSkill retrieves a skill by name with concrete type
func (r *Registry) GetSkill(name string) *Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.skills[name]
}

// List returns all registered skills
func (r *Registry) List() []*Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()

	skills := make([]*Skill, 0, len(r.skills))
	for _, skill := range r.skills {
		skills = append(skills, skill)
	}
	return skills
}

// ListBySource returns skills grouped by source as any for compatibility
func (r *Registry) ListBySource() map[string][]any {
	r.mu.RLock()
	defer r.mu.RUnlock()

	bySource := make(map[string][]any)
	for _, skill := range r.skills {
		bySource[skill.Source] = append(bySource[skill.Source], any(skill))
	}
	return bySource
}

// GetByPhase returns all skills tagged for a specific phase
func (r *Registry) GetByPhase(phase string) []PhaseSkill {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var phaseSkills []PhaseSkill
	for _, skill := range r.skills {
		if skill.Phase == phase {
			phaseSkills = append(phaseSkills, skill)
		}
	}
	return phaseSkills
}

// Activate activates a skill with the given scope
func (r *Registry) Activate(name, scope, activatedBy string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	skill, ok := r.skills[name]
	if !ok {
		return ErrSkillNotFound{Name: name}
	}

	if _, active := r.active[name]; active {
		return ErrSkillAlreadyActive{Name: name}
	}

	r.active[name] = &ActiveSkill{
		Skill:       skill,
		Scope:       scope,
		ActivatedAt: time.Now(),
		ActivatedBy: activatedBy,
	}

	return nil
}

// Deactivate deactivates a skill
func (r *Registry) Deactivate(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, active := r.active[name]; !active {
		return ErrSkillNotActive{Name: name}
	}

	delete(r.active, name)
	return nil
}

// IsActive checks if a skill is currently active
func (r *Registry) IsActive(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, active := r.active[name]
	return active
}

// GetActive returns a specific active skill or nil if not active.
// Note: Returns untyped nil when not found to avoid Go interface nil gotcha.
func (r *Registry) GetActive(name string) any {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if v, ok := r.active[name]; ok {
		return v
	}
	return nil
}

// GetActiveSkill retrieves an active skill with concrete type
func (r *Registry) GetActiveSkill(name string) *ActiveSkill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.active[name]
}

// ListActive returns all currently active skills as any for compatibility
func (r *Registry) ListActive() []any {
	r.mu.RLock()
	defer r.mu.RUnlock()

	active := make([]any, 0, len(r.active))
	for _, as := range r.active {
		active = append(active, any(as))
	}
	return active
}

// DeactivateAll deactivates all active skills
func (r *Registry) DeactivateAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.active = make(map[string]*ActiveSkill)
}

// Count returns the total number of registered skills
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.skills)
}

// CountActive returns the number of currently active skills
func (r *Registry) CountActive() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.active)
}

// GetDescriptions returns a formatted string of all skill descriptions
// for inclusion in the system prompt
func (r *Registry) GetDescriptions() string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.skills) == 0 {
		return ""
	}

	result := "# Available Skills\n\n"
	result += "You have access to workflow skills that provide structured guidance. Skills can be activated in three ways:\n\n"
	result += "1. **Automatic** - Some skills auto-activate during specific phases (planning/execute/review)\n"
	result += "2. **Model-driven** - Use the `activate_skill` tool when you recognize a relevant pattern\n"
	result += "3. **User-triggered** - User explicitly requests via `/skill <name>`\n\n"
	result += "Available skills:\n"

	for _, skill := range r.skills {
		result += fmt.Sprintf("- %s: %s", skill.Name, skill.Description)
		if skill.Phase != "" {
			result += fmt.Sprintf(" [auto-activates in %s phase]", skill.Phase)
		}
		result += "\n"
	}

	result += "\nWhen a skill is active, follow its instructions precisely.\n"
	return result
}
