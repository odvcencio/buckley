package orchestrator

import (
	"strings"
	"testing"

	"go.uber.org/mock/gomock"

	"github.com/odvcencio/buckley/pkg/skill"
)

func setupSkillManagerTest(t *testing.T) (*gomock.Controller, *MockSkillRegistry, *MockSkillConversation, *SkillManager) {
	ctrl := gomock.NewController(t)
	registry := NewMockSkillRegistry(ctrl)
	conv := NewMockSkillConversation(ctrl)
	sm := NewSkillManager(registry, conv)
	return ctrl, registry, conv, sm
}

type phaseSkillConfig struct {
	name         string
	content      string
	description  string
	allowedTools []string
	requiresTodo bool
	todoTemplate string
}

func newPhaseSkillMock(ctrl *gomock.Controller, cfg phaseSkillConfig) *MockPhaseSkill {
	mockSkill := NewMockPhaseSkill(ctrl)
	mockSkill.EXPECT().GetName().Return(cfg.name).AnyTimes()
	mockSkill.EXPECT().GetDescription().Return(cfg.description).AnyTimes()
	mockSkill.EXPECT().GetContent().Return(cfg.content).AnyTimes()
	mockSkill.EXPECT().GetAllowedTools().Return(cfg.allowedTools).AnyTimes()
	mockSkill.EXPECT().GetRequiresTodo().Return(cfg.requiresTodo).AnyTimes()
	mockSkill.EXPECT().GetTodoTemplate().Return(cfg.todoTemplate).AnyTimes()
	return mockSkill
}

func expectMetadata(conv *MockSkillConversation, meta map[string]any) {
	conv.EXPECT().GetMetadata(gomock.Any()).AnyTimes().DoAndReturn(func(key string) any {
		return meta[key]
	})
	conv.EXPECT().SetMetadata(gomock.Any(), gomock.Any()).AnyTimes().Do(func(key string, value any) {
		meta[key] = value
	})
}

func TestNewSkillManager(t *testing.T) {
	ctrl, registry, conv, sm := setupSkillManagerTest(t)
	defer ctrl.Finish()

	if sm == nil {
		t.Fatal("NewSkillManager returned nil")
	}
	if sm.registry != registry || sm.conv != conv {
		t.Fatal("SkillManager did not retain dependencies")
	}
}

func TestActivatePhaseSkills_Success(t *testing.T) {
	ctrl, registry, conv, sm := setupSkillManagerTest(t)
	defer ctrl.Finish()

	skill1 := newPhaseSkillMock(ctrl, phaseSkillConfig{name: "planning-skill-1", content: "content"})
	skill2 := newPhaseSkillMock(ctrl, phaseSkillConfig{name: "planning-skill-2", content: "other"})

	registry.EXPECT().GetByPhase("planning").Return([]skill.PhaseSkill{skill1, skill2})

	registry.EXPECT().IsActive("planning-skill-1").Return(false)
	registry.EXPECT().IsActive("planning-skill-2").Return(false)

	registry.EXPECT().Activate("planning-skill-1", "planning phase", "phase").Return(nil)
	registry.EXPECT().Activate("planning-skill-2", "planning phase", "phase").Return(nil)

	meta := map[string]any{}
	expectMetadata(conv, meta)

	var messages []string
	conv.EXPECT().AddSystemMessage(gomock.Any()).Times(2).Do(func(msg string) {
		messages = append(messages, msg)
	})

	if err := sm.ActivatePhaseSkills("planning"); err != nil {
		t.Fatalf("ActivatePhaseSkills error = %v", err)
	}

	if len(messages) != 2 {
		t.Fatalf("expected 2 system messages, got %d", len(messages))
	}
	for _, msg := range messages {
		if !strings.Contains(msg, "Skill Activated") || !strings.Contains(msg, "planning phase") {
			t.Fatalf("missing activation context in message: %s", msg)
		}
	}
}

func TestActivatePhaseSkills_SkipAlreadyActive(t *testing.T) {
	ctrl, registry, _, sm := setupSkillManagerTest(t)
	defer ctrl.Finish()

	skillMock := newPhaseSkillMock(ctrl, phaseSkillConfig{name: "duplicate"})
	registry.EXPECT().GetByPhase("execute").Return([]skill.PhaseSkill{skillMock})
	registry.EXPECT().IsActive("duplicate").Return(true)

	if err := sm.ActivatePhaseSkills("execute"); err != nil {
		t.Fatalf("ActivatePhaseSkills returned error: %v", err)
	}
}

func TestActivatePhaseSkills_WithToolRestrictions(t *testing.T) {
	ctrl, registry, conv, sm := setupSkillManagerTest(t)
	defer ctrl.Finish()

	skillMock := newPhaseSkillMock(ctrl, phaseSkillConfig{
		name:         "restricted",
		content:      "content",
		allowedTools: []string{"read", "write"},
	})

	registry.EXPECT().GetByPhase("execute").Return([]skill.PhaseSkill{skillMock})
	registry.EXPECT().IsActive("restricted").Return(false)
	registry.EXPECT().Activate("restricted", "execute phase", "phase").Return(nil)

	meta := map[string]any{}
	expectMetadata(conv, meta)

	conv.EXPECT().AddSystemMessage(gomock.Any()).Times(1)
	conv.EXPECT().SetToolFilter([]string{"read", "write"}).Times(1)

	if err := sm.ActivatePhaseSkills("execute"); err != nil {
		t.Fatalf("ActivatePhaseSkills error = %v", err)
	}
}

func TestActivatePhaseSkills_TodoRequirement_NoTodos(t *testing.T) {
	ctrl, registry, conv, sm := setupSkillManagerTest(t)
	defer ctrl.Finish()

	skillMock := newPhaseSkillMock(ctrl, phaseSkillConfig{
		name:         "todo-skill",
		content:      "content",
		requiresTodo: true,
		todoTemplate: "- Item",
	})

	registry.EXPECT().GetByPhase("execute").Return([]skill.PhaseSkill{skillMock})
	registry.EXPECT().IsActive("todo-skill").Return(false)
	registry.EXPECT().Activate("todo-skill", "execute phase", "phase").Return(nil)

	meta := map[string]any{}
	expectMetadata(conv, meta)

	var msg string
	conv.EXPECT().AddSystemMessage(gomock.Any()).Do(func(s string) { msg = s })

	if err := sm.ActivatePhaseSkills("execute"); err != nil {
		t.Fatalf("ActivatePhaseSkills error = %v", err)
	}

	if !strings.Contains(msg, "⚠️") {
		t.Fatalf("expected warning for missing TODOs, got %s", msg)
	}
	if !strings.Contains(msg, "Item") {
		t.Fatalf("expected TODO template in message, got %s", msg)
	}
}

func TestActivatePhaseSkills_TodoRequirement_WithTodos(t *testing.T) {
	ctrl, registry, conv, sm := setupSkillManagerTest(t)
	defer ctrl.Finish()

	skillMock := newPhaseSkillMock(ctrl, phaseSkillConfig{
		name:         "todo-skill",
		content:      "content",
		requiresTodo: true,
		todoTemplate: "- Item",
	})

	registry.EXPECT().GetByPhase("execute").Return([]skill.PhaseSkill{skillMock})
	registry.EXPECT().IsActive("todo-skill").Return(false)
	registry.EXPECT().Activate("todo-skill", "execute phase", "phase").Return(nil)

	meta := map[string]any{skill.MetadataKeyHasTodos: true}
	expectMetadata(conv, meta)

	var msg string
	conv.EXPECT().AddSystemMessage(gomock.Any()).Do(func(s string) { msg = s })

	if err := sm.ActivatePhaseSkills("execute"); err != nil {
		t.Fatalf("ActivatePhaseSkills error = %v", err)
	}

	if strings.Contains(msg, "⚠️") {
		t.Fatalf("warning should not appear when TODOs exist: %s", msg)
	}
	if !strings.Contains(msg, "Item") {
		t.Fatalf("expected template when TODOs exist, got %s", msg)
	}
}

func TestActivatePhaseSkills_NoSkillsForPhase(t *testing.T) {
	ctrl, registry, _, sm := setupSkillManagerTest(t)
	defer ctrl.Finish()

	registry.EXPECT().GetByPhase("review").Return(nil)

	if err := sm.ActivatePhaseSkills("review"); err != nil {
		t.Fatalf("ActivatePhaseSkills returned error: %v", err)
	}
}

func TestDeactivatePhaseSkills_Success(t *testing.T) {
	ctrl, registry, conv, sm := setupSkillManagerTest(t)
	defer ctrl.Finish()

	execSkill1 := newPhaseSkillMock(ctrl, phaseSkillConfig{name: "skill-1", allowedTools: []string{"read"}})
	execSkill2 := newPhaseSkillMock(ctrl, phaseSkillConfig{name: "skill-2"})

	phaseMap := map[string][]skill.PhaseSkill{
		"execute": {execSkill1, execSkill2},
	}
	registry.EXPECT().GetByPhase(gomock.Any()).AnyTimes().DoAndReturn(func(phase string) []skill.PhaseSkill {
		return phaseMap[phase]
	})

	active := map[string]bool{"skill-1": true, "skill-2": true}
	registry.EXPECT().IsActive(gomock.Any()).AnyTimes().DoAndReturn(func(name string) bool {
		return active[name]
	})
	registry.EXPECT().Deactivate(gomock.Any()).Times(2).DoAndReturn(func(name string) error {
		active[name] = false
		return nil
	})

	conv.EXPECT().ClearToolFilter().Times(1)

	if err := sm.DeactivatePhaseSkills("execute"); err != nil {
		t.Fatalf("DeactivatePhaseSkills error = %v", err)
	}
}

func TestDeactivatePhaseSkills_KeepFilterWhenOtherPhaseActive(t *testing.T) {
	ctrl, registry, conv, sm := setupSkillManagerTest(t)
	defer ctrl.Finish()

	execSkill := newPhaseSkillMock(ctrl, phaseSkillConfig{name: "execute-skill", allowedTools: []string{"read"}})
	reviewSkill := newPhaseSkillMock(ctrl, phaseSkillConfig{name: "review-skill", allowedTools: []string{"write"}})

	phaseMap := map[string][]skill.PhaseSkill{
		"execute": {execSkill},
		"review":  {reviewSkill},
	}
	registry.EXPECT().GetByPhase(gomock.Any()).AnyTimes().DoAndReturn(func(phase string) []skill.PhaseSkill {
		return phaseMap[phase]
	})

	active := map[string]bool{
		"execute-skill": true,
		"review-skill":  true,
	}
	registry.EXPECT().IsActive(gomock.Any()).AnyTimes().DoAndReturn(func(name string) bool {
		return active[name]
	})
	registry.EXPECT().Deactivate("execute-skill").DoAndReturn(func(name string) error {
		active[name] = false
		return nil
	})

	conv.EXPECT().ClearToolFilter().Times(0)

	if err := sm.DeactivatePhaseSkills("execute"); err != nil {
		t.Fatalf("DeactivatePhaseSkills error = %v", err)
	}
}

func TestHasActiveToolFilters(t *testing.T) {
	ctrl, registry, _, sm := setupSkillManagerTest(t)
	defer ctrl.Finish()

	phaseMap := map[string][]skill.PhaseSkill{}
	registry.EXPECT().GetByPhase(gomock.Any()).AnyTimes().DoAndReturn(func(phase string) []skill.PhaseSkill {
		return phaseMap[phase]
	})
	activeMap := map[string]bool{}
	registry.EXPECT().IsActive(gomock.Any()).AnyTimes().DoAndReturn(func(name string) bool {
		return activeMap[name]
	})

	if sm.hasActiveToolFilters() {
		t.Fatal("expected no tool filters without active skills")
	}

	restricted := newPhaseSkillMock(ctrl, phaseSkillConfig{name: "restricted", allowedTools: []string{"read"}})
	phaseMap["execute"] = []skill.PhaseSkill{restricted}

	activeMap["restricted"] = true

	if !sm.hasActiveToolFilters() {
		t.Fatal("expected active tool filters when restricted skill is active")
	}

	activeMap["restricted"] = false
	if sm.hasActiveToolFilters() {
		t.Fatal("expected tool filters to clear after skill becomes inactive")
	}
}

func TestGetSkillsDescription(t *testing.T) {
	ctrl, registry, _, sm := setupSkillManagerTest(t)
	defer ctrl.Finish()

	registry.EXPECT().GetDescriptions().Return("skills info")

	if desc := sm.GetSkillsDescription(); desc != "skills info" {
		t.Fatalf("GetSkillsDescription = %q, want skills info", desc)
	}
}

func TestGetSkillsDescription_NilRegistry(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	conv := NewMockSkillConversation(ctrl)
	sm := NewSkillManager(nil, conv)
	if desc := sm.GetSkillsDescription(); desc != "" {
		t.Fatalf("expected empty description, got %q", desc)
	}
}

func TestInjectSkillContent_NilConversation(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	skillMock := newPhaseSkillMock(ctrl, phaseSkillConfig{name: "skill", content: "content"})
	sm := NewSkillManager(nil, nil)
	if err := sm.injectSkillContent(skillMock, "scope"); err == nil {
		t.Fatal("expected error when conversation is nil")
	}
}
