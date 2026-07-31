package skill

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Registry manages skill discovery, loading, and activation
type Registry struct {
	mu          sync.RWMutex
	loadMu      sync.Mutex
	skills      map[string]*Skill       // All discovered skills by name
	active      map[string]*ActiveSkill // Currently active skills
	loader      *Loader
	diagnostics []string
}

// NewRegistry creates a new skill registry
func NewRegistry() *Registry {
	return &Registry{
		skills: make(map[string]*Skill),
		active: make(map[string]*ActiveSkill),
		loader: NewLoader(),
	}
}

// Register adds or replaces a skill in the registry.
func (r *Registry) Register(s *Skill) error {
	if s == nil {
		return fmt.Errorf("skill is required")
	}
	if err := s.Validate(); err != nil {
		return err
	}
	if s.LoadedAt.IsZero() {
		s.LoadedAt = time.Now()
	}

	r.mu.Lock()
	r.skills[s.Name] = s
	if active := r.active[s.Name]; active != nil {
		active.Skill = s
	}
	r.mu.Unlock()
	return nil
}

// LoadAll loads skills from all standard locations in precedence order
func (r *Registry) LoadAll() error {
	r.loadMu.Lock()
	defer r.loadMu.Unlock()

	next := make(map[string]*Skill)
	var loadErrs []error
	var diagnostics []string
	record := func(source string, err error) {
		if err == nil {
			return
		}
		loadErrs = append(loadErrs, fmt.Errorf("load %s skills: %w", source, err))
		for _, detail := range errorLeaves(err) {
			diagnostics = append(diagnostics, fmt.Sprintf("%s: %s", source, detail))
		}
	}

	// Load in precedence order (later overrides earlier)
	record("bundled", r.loader.LoadBundled(next))
	record("plugin", r.loader.LoadFromPlugins(next))
	record("personal", r.loader.LoadPersonal(next))
	record("project agent", r.loader.LoadProjectAgent(next))
	record("project", r.loader.LoadProject(next))

	r.mu.Lock()
	r.skills = next
	r.diagnostics = diagnostics
	for name, active := range r.active {
		loaded, ok := next[name]
		if !ok {
			delete(r.active, name)
			continue
		}
		active.Skill = loaded
	}
	r.mu.Unlock()

	return errors.Join(loadErrs...)
}

func errorLeaves(err error) []string {
	type multiError interface {
		Unwrap() []error
	}
	if joined, ok := err.(multiError); ok {
		var leaves []string
		for _, child := range joined.Unwrap() {
			leaves = append(leaves, errorLeaves(child)...)
		}
		return leaves
	}
	return []string{err.Error()}
}

// Diagnostics returns recoverable problems found during the latest load.
func (r *Registry) Diagnostics() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.diagnostics...)
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
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	return skills
}

// ListBySource returns skills grouped by source as any for compatibility
func (r *Registry) ListBySource() map[string][]any {
	r.mu.RLock()
	defer r.mu.RUnlock()

	bySource := make(map[string][]any)
	names := make([]string, 0, len(r.skills))
	for name := range r.skills {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		skill := r.skills[name]
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
	sort.Slice(phaseSkills, func(i, j int) bool {
		return phaseSkills[i].GetName() < phaseSkills[j].GetName()
	})
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

	names := make([]string, 0, len(r.active))
	for name := range r.active {
		names = append(names, name)
	}
	sort.Strings(names)
	active := make([]any, 0, len(names))
	for _, name := range names {
		active = append(active, any(r.active[name]))
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
	result += "These are lightweight triggers; full instructions load only after activation. "
	result += "Use `activate_skill` when relevant; `/skill <name>` is the user shortcut; phase tags auto-activate.\n\n"

	names := make([]string, 0, len(r.skills))
	for name := range r.skills {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		skill := r.skills[name]
		result += fmt.Sprintf("- %s: %s", skill.Name, promptSkillDescription(skill.Description))
		if skill.Phase != "" {
			result += fmt.Sprintf(" [auto-activates in %s phase]", skill.Phase)
		}
		result += "\n"
	}

	result += "\nWhen a skill is active, follow its instructions precisely.\n"
	return result
}

func promptSkillDescription(description string) string {
	const maxRunes = 400
	compact := strings.Join(strings.Fields(description), " ")
	runes := []rune(compact)
	if len(runes) <= maxRunes {
		return compact
	}
	return strings.TrimSpace(string(runes[:maxRunes-1])) + "…"
}
