package skill

import (
	"testing"
	"time"
)

func TestNewRegistry(t *testing.T) {
	registry := NewRegistry()

	if registry == nil {
		t.Fatal("NewRegistry() returned nil")
	}

	if registry.Count() != 0 {
		t.Errorf("Count() = %d, want 0 for new registry", registry.Count())
	}

	if registry.CountActive() != 0 {
		t.Errorf("CountActive() = %d, want 0 for new registry", registry.CountActive())
	}
}

func TestRegistry_Get(t *testing.T) {
	registry := NewRegistry()

	// Add a skill
	skill := &Skill{
		Name:        "test-skill",
		Description: "Test skill",
	}
	registry.skills["test-skill"] = skill

	// Get existing skill
	got := registry.GetSkill("test-skill")
	if got == nil {
		t.Fatal("GetSkill() returned nil for existing skill")
	}
	if got.Name != "test-skill" {
		t.Errorf("GetSkill() returned skill with name %s, want test-skill", got.Name)
	}

	// Get non-existent skill
	got = registry.GetSkill("nonexistent")
	if got != nil {
		t.Errorf("GetSkill() returned %v for non-existent skill, want nil", got)
	}
}

func TestRegistry_List(t *testing.T) {
	registry := NewRegistry()

	// Empty registry
	skills := registry.List()
	if len(skills) != 0 {
		t.Errorf("List() returned %d skills, want 0 for empty registry", len(skills))
	}

	// Add skills
	skill1 := &Skill{Name: "skill1", Description: "Skill 1"}
	skill2 := &Skill{Name: "skill2", Description: "Skill 2"}
	registry.skills["skill1"] = skill1
	registry.skills["skill2"] = skill2

	skills = registry.List()
	if len(skills) != 2 {
		t.Errorf("List() returned %d skills, want 2", len(skills))
	}
}

func TestRegistry_GetByPhase(t *testing.T) {
	registry := NewRegistry()

	// Add skills with different phases
	skill1 := &Skill{Name: "planning-skill", Description: "Planning", Phase: "planning"}
	skill2 := &Skill{Name: "execute-skill", Description: "Execute", Phase: "execute"}
	skill3 := &Skill{Name: "no-phase-skill", Description: "No phase", Phase: ""}

	registry.skills["planning-skill"] = skill1
	registry.skills["execute-skill"] = skill2
	registry.skills["no-phase-skill"] = skill3

	// Get planning skills
	planningSkills := registry.GetByPhase("planning")
	if len(planningSkills) != 1 {
		t.Errorf("GetByPhase('planning') returned %d skills, want 1", len(planningSkills))
	}
	if planningSkills[0].GetName() != "planning-skill" {
		t.Errorf("GetByPhase('planning') returned %s, want planning-skill", planningSkills[0].GetName())
	}

	// Get execute skills
	executeSkills := registry.GetByPhase("execute")
	if len(executeSkills) != 1 {
		t.Errorf("GetByPhase('execute') returned %d skills, want 1", len(executeSkills))
	}

	// Get skills with empty phase
	emptyPhaseSkills := registry.GetByPhase("")
	if len(emptyPhaseSkills) != 1 {
		t.Errorf("GetByPhase('') returned %d skills, want 1", len(emptyPhaseSkills))
	}

	// Get non-existent phase
	nonexistent := registry.GetByPhase("nonexistent")
	if len(nonexistent) != 0 {
		t.Errorf("GetByPhase('nonexistent') returned %d skills, want 0", len(nonexistent))
	}
}

func TestRegistry_Activate(t *testing.T) {
	registry := NewRegistry()

	// Add a skill
	skill := &Skill{Name: "test-skill", Description: "Test"}
	registry.skills["test-skill"] = skill

	// Activate skill
	err := registry.Activate("test-skill", "test-scope", "user")
	if err != nil {
		t.Errorf("Activate() error = %v", err)
	}

	// Check if skill is active
	if !registry.IsActive("test-skill") {
		t.Error("IsActive() = false, want true after activation")
	}

	// Get active skill
	active := registry.GetActiveSkill("test-skill")
	if active == nil {
		t.Fatal("GetActiveSkill() returned nil for active skill")
	}
	if active.Scope != "test-scope" {
		t.Errorf("Scope = %s, want test-scope", active.Scope)
	}
	if active.ActivatedBy != "user" {
		t.Errorf("ActivatedBy = %s, want user", active.ActivatedBy)
	}
	if active.Skill != skill {
		t.Error("Active skill reference doesn't match original skill")
	}

	// Try to activate already active skill
	err = registry.Activate("test-skill", "new-scope", "model")
	if err == nil {
		t.Error("Activate() error = nil, want error for already active skill")
	}
	if _, ok := err.(ErrSkillAlreadyActive); !ok {
		t.Errorf("Activate() error type = %T, want ErrSkillAlreadyActive", err)
	}

	// Try to activate non-existent skill
	err = registry.Activate("nonexistent", "scope", "user")
	if err == nil {
		t.Error("Activate() error = nil, want error for non-existent skill")
	}
	if _, ok := err.(ErrSkillNotFound); !ok {
		t.Errorf("Activate() error type = %T, want ErrSkillNotFound", err)
	}
}

func TestRegistry_Deactivate(t *testing.T) {
	registry := NewRegistry()

	// Add and activate a skill
	skill := &Skill{Name: "test-skill", Description: "Test"}
	registry.skills["test-skill"] = skill
	registry.Activate("test-skill", "scope", "user")

	// Deactivate skill
	err := registry.Deactivate("test-skill")
	if err != nil {
		t.Errorf("Deactivate() error = %v", err)
	}

	// Check if skill is inactive
	if registry.IsActive("test-skill") {
		t.Error("IsActive() = true, want false after deactivation")
	}

	// Get active skill should return nil
	active := registry.GetActive("test-skill")
	if active != nil {
		t.Errorf("GetActive() = %v, want nil for deactivated skill", active)
	}

	// Try to deactivate already inactive skill
	err = registry.Deactivate("test-skill")
	if err == nil {
		t.Error("Deactivate() error = nil, want error for already inactive skill")
	}
	if _, ok := err.(ErrSkillNotActive); !ok {
		t.Errorf("Deactivate() error type = %T, want ErrSkillNotActive", err)
	}
}

func TestRegistry_IsActive(t *testing.T) {
	registry := NewRegistry()

	// Add a skill
	skill := &Skill{Name: "test-skill", Description: "Test"}
	registry.skills["test-skill"] = skill

	// Initially not active
	if registry.IsActive("test-skill") {
		t.Error("IsActive() = true, want false for non-activated skill")
	}

	// Activate
	registry.Activate("test-skill", "scope", "user")
	if !registry.IsActive("test-skill") {
		t.Error("IsActive() = false, want true for activated skill")
	}

	// Deactivate
	registry.Deactivate("test-skill")
	if registry.IsActive("test-skill") {
		t.Error("IsActive() = true, want false for deactivated skill")
	}

	// Non-existent skill
	if registry.IsActive("nonexistent") {
		t.Error("IsActive() = true, want false for non-existent skill")
	}
}

func TestRegistry_ListActive(t *testing.T) {
	registry := NewRegistry()

	// Add skills
	skill1 := &Skill{Name: "skill1", Description: "Skill 1"}
	skill2 := &Skill{Name: "skill2", Description: "Skill 2"}
	skill3 := &Skill{Name: "skill3", Description: "Skill 3"}
	registry.skills["skill1"] = skill1
	registry.skills["skill2"] = skill2
	registry.skills["skill3"] = skill3

	// Initially no active skills
	activeList := registry.ListActive()
	if len(activeList) != 0 {
		t.Errorf("ListActive() returned %d skills, want 0", len(activeList))
	}

	// Activate two skills
	registry.Activate("skill1", "scope1", "user")
	registry.Activate("skill2", "scope2", "model")

	activeList = registry.ListActive()
	if len(activeList) != 2 {
		t.Errorf("ListActive() returned %d skills, want 2", len(activeList))
	}

	// Count should match
	if registry.CountActive() != 2 {
		t.Errorf("CountActive() = %d, want 2", registry.CountActive())
	}

	// Deactivate one
	registry.Deactivate("skill1")

	activeList = registry.ListActive()
	if len(activeList) != 1 {
		t.Errorf("ListActive() returned %d skills, want 1 after deactivation", len(activeList))
	}
}

func TestRegistry_DeactivateAll(t *testing.T) {
	registry := NewRegistry()

	// Add and activate multiple skills
	skill1 := &Skill{Name: "skill1", Description: "Skill 1"}
	skill2 := &Skill{Name: "skill2", Description: "Skill 2"}
	registry.skills["skill1"] = skill1
	registry.skills["skill2"] = skill2

	registry.Activate("skill1", "scope1", "user")
	registry.Activate("skill2", "scope2", "model")

	// Verify they're active
	if registry.CountActive() != 2 {
		t.Errorf("CountActive() = %d, want 2 before DeactivateAll", registry.CountActive())
	}

	// Deactivate all
	registry.DeactivateAll()

	// Verify all are inactive
	if registry.CountActive() != 0 {
		t.Errorf("CountActive() = %d, want 0 after DeactivateAll", registry.CountActive())
	}
	if registry.IsActive("skill1") {
		t.Error("skill1 still active after DeactivateAll")
	}
	if registry.IsActive("skill2") {
		t.Error("skill2 still active after DeactivateAll")
	}
}

func TestRegistry_Count(t *testing.T) {
	registry := NewRegistry()

	if registry.Count() != 0 {
		t.Errorf("Count() = %d, want 0 for empty registry", registry.Count())
	}

	// Add skills
	registry.skills["skill1"] = &Skill{Name: "skill1", Description: "Skill 1"}
	registry.skills["skill2"] = &Skill{Name: "skill2", Description: "Skill 2"}

	if registry.Count() != 2 {
		t.Errorf("Count() = %d, want 2", registry.Count())
	}
}

func TestRegistry_ListBySource(t *testing.T) {
	registry := NewRegistry()

	// Add skills from different sources
	skill1 := &Skill{Name: "bundled1", Description: "Bundled 1", Source: "bundled"}
	skill2 := &Skill{Name: "bundled2", Description: "Bundled 2", Source: "bundled"}
	skill3 := &Skill{Name: "personal1", Description: "Personal 1", Source: "personal"}
	skill4 := &Skill{Name: "plugin1", Description: "Plugin 1", Source: "plugin"}

	registry.skills["bundled1"] = skill1
	registry.skills["bundled2"] = skill2
	registry.skills["personal1"] = skill3
	registry.skills["plugin1"] = skill4

	bySource := registry.ListBySource()

	// Check bundled skills
	bundled, ok := bySource["bundled"]
	if !ok {
		t.Fatal("ListBySource() missing 'bundled' key")
	}
	if len(bundled) != 2 {
		t.Errorf("bundled source has %d skills, want 2", len(bundled))
	}

	// Check personal skills
	personal, ok := bySource["personal"]
	if !ok {
		t.Fatal("ListBySource() missing 'personal' key")
	}
	if len(personal) != 1 {
		t.Errorf("personal source has %d skills, want 1", len(personal))
	}

	// Check plugin skills
	plugin, ok := bySource["plugin"]
	if !ok {
		t.Fatal("ListBySource() missing 'plugin' key")
	}
	if len(plugin) != 1 {
		t.Errorf("plugin source has %d skills, want 1", len(plugin))
	}
}

func TestRegistry_GetDescriptions(t *testing.T) {
	registry := NewRegistry()

	// Empty registry
	desc := registry.GetDescriptions()
	if desc != "" {
		t.Errorf("GetDescriptions() = %q, want empty string for empty registry", desc)
	}

	// Add skills
	skill1 := &Skill{
		Name:        "skill1",
		Description: "First skill",
		Phase:       "planning",
	}
	skill2 := &Skill{
		Name:        "skill2",
		Description: "Second skill",
		Phase:       "",
	}
	registry.skills["skill1"] = skill1
	registry.skills["skill2"] = skill2

	desc = registry.GetDescriptions()

	// Should contain header
	if !containsHelper(desc, "# Available Skills") {
		t.Error("GetDescriptions() missing '# Available Skills' header")
	}

	// Should contain skill names
	if !containsHelper(desc, "skill1") {
		t.Error("GetDescriptions() missing 'skill1'")
	}
	if !containsHelper(desc, "skill2") {
		t.Error("GetDescriptions() missing 'skill2'")
	}

	// Should mention auto-activation for phase skills
	if !containsHelper(desc, "planning phase") {
		t.Error("GetDescriptions() missing phase information for skill1")
	}
}

func TestRegistry_ConcurrentAccess(t *testing.T) {
	registry := NewRegistry()

	// Add a skill
	skill := &Skill{Name: "concurrent-skill", Description: "Test concurrent access"}
	registry.skills["concurrent-skill"] = skill

	// Test concurrent activations and deactivations
	done := make(chan bool)

	// Goroutine 1: Activate/Deactivate repeatedly
	go func() {
		for i := 0; i < 100; i++ {
			registry.Activate("concurrent-skill", "scope", "user")
			registry.Deactivate("concurrent-skill")
		}
		done <- true
	}()

	// Goroutine 2: Check if active repeatedly
	go func() {
		for i := 0; i < 100; i++ {
			registry.IsActive("concurrent-skill")
			registry.CountActive()
		}
		done <- true
	}()

	// Goroutine 3: List skills repeatedly
	go func() {
		for i := 0; i < 100; i++ {
			registry.List()
			registry.ListActive()
		}
		done <- true
	}()

	// Wait for all goroutines
	<-done
	<-done
	<-done

	// If we get here without deadlock or race conditions, test passes
}

func TestRegistry_ActivationTimestamp(t *testing.T) {
	registry := NewRegistry()

	skill := &Skill{Name: "time-test", Description: "Test timestamps"}
	registry.skills["time-test"] = skill

	before := time.Now()
	time.Sleep(1 * time.Millisecond) // Ensure time progresses

	registry.Activate("time-test", "scope", "user")

	time.Sleep(1 * time.Millisecond)
	after := time.Now()

	active := registry.GetActiveSkill("time-test")
	if active == nil {
		t.Fatal("GetActiveSkill() returned nil")
	}

	if active.ActivatedAt.Before(before) || active.ActivatedAt.After(after) {
		t.Errorf("ActivatedAt = %v, want between %v and %v", active.ActivatedAt, before, after)
	}
}
