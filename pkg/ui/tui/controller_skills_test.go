package tui

import (
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/skill"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestFormatSkillList(t *testing.T) {
	registry := skill.NewRegistry()
	if got := formatSkillList(registry); got != "No skills available." {
		t.Fatalf("empty skill list = %q", got)
	}

	for _, s := range []*skill.Skill{
		{Name: "zeta", Description: "Zeta skill"},
		{Name: "alpha", Description: "Alpha skill"},
	} {
		if err := registry.Register(s); err != nil {
			t.Fatalf("Register(%s): %v", s.Name, err)
		}
	}

	want := "Available skills:\n- alpha\n- zeta"
	if got := formatSkillList(registry); got != want {
		t.Fatalf("skill list = %q, want %q", got, want)
	}
}

func TestFormatSkillActivationResult(t *testing.T) {
	tests := []struct {
		name   string
		result *builtin.Result
		want   string
		wantOK bool
	}{
		{name: "nil result", wantOK: false},
		{
			name: "tool error",
			result: &builtin.Result{
				Success: false,
				Error:   "not found",
			},
			want:   `Error activating skill "test": not found`,
			wantOK: true,
		},
		{
			name: "message and content",
			result: &builtin.Result{
				Success: true,
				Data: map[string]any{
					"message": "activated",
					"content": "instructions",
				},
			},
			want:   "activated\n\ninstructions",
			wantOK: true,
		},
		{
			name: "content only",
			result: &builtin.Result{
				Success: true,
				Data:    map[string]any{"content": "instructions"},
			},
			want:   "instructions",
			wantOK: true,
		},
		{
			name: "message only",
			result: &builtin.Result{
				Success: true,
				Data:    map[string]any{"message": "activated"},
			},
			want:   "activated",
			wantOK: true,
		},
		{
			name: "fallback",
			result: &builtin.Result{
				Success: true,
			},
			want:   `Skill "test" activated.`,
			wantOK: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := formatSkillActivationResult("test", tt.result)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if got != tt.want {
				t.Fatalf("content = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestActivateSessionSkill_ActivatesAndInjectsContent(t *testing.T) {
	registry := skill.NewRegistry()
	if err := registry.Register(&skill.Skill{
		Name:        "daily-driver",
		Description: "Daily driver guidance",
		Content:     "Use focused, test-backed changes.",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	var injected []string
	state := skill.NewRuntimeState(func(content string) {
		injected = append(injected, content)
	})
	toolRegistry := tool.NewEmptyRegistry()
	toolRegistry.Register(&builtin.SkillActivationTool{Registry: registry, Conversation: state})
	sess := &SessionState{
		SkillRegistry: registry,
		SkillState:    state,
		ToolRegistry:  toolRegistry,
	}

	content, err := activateSessionSkill(sess, "daily-driver")
	if err != nil {
		t.Fatalf("activateSessionSkill: %v", err)
	}
	if !registry.IsActive("daily-driver") {
		t.Fatal("skill should be active after activation")
	}
	if len(injected) != 1 {
		t.Fatalf("injected system message count = %d, want 1", len(injected))
	}
	for _, text := range []string{
		"Skill 'daily-driver' activated",
		"# Skill Activated: daily-driver",
		"Use focused, test-backed changes.",
	} {
		if !strings.Contains(content, text) {
			t.Fatalf("activation output missing %q:\n%s", text, content)
		}
	}
	if !strings.Contains(injected[0], "# Skill Activated: daily-driver") {
		t.Fatalf("injected message missing skill header:\n%s", injected[0])
	}
}

func TestActivateSessionSkill_UsesGovernedRegistry(t *testing.T) {
	skills := skill.NewRegistry()
	if err := skills.Register(&skill.Skill{Name: "guarded", Description: "Guarded skill", Content: "must not activate"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	state := skill.NewRuntimeState(func(string) {})
	registry := tool.NewEmptyRegistry()
	registry.Register(&builtin.SkillActivationTool{Registry: skills, Conversation: state})
	hookCalled := false
	registry.Hooks().RegisterPreHook("activate_skill", func(*tool.ExecutionContext) tool.HookResult {
		hookCalled = true
		return tool.HookResult{Abort: true, AbortReason: "governed denial"}
	})

	_, err := activateSessionSkill(&SessionState{ToolRegistry: registry, SkillRegistry: skills, SkillState: state}, "guarded")
	if err == nil || !strings.Contains(err.Error(), "governed denial") {
		t.Fatalf("activation error = %v, want governed denial", err)
	}
	if !hookCalled || skills.IsActive("guarded") {
		t.Fatalf("registry governance hook=%v active=%v", hookCalled, skills.IsActive("guarded"))
	}
}
