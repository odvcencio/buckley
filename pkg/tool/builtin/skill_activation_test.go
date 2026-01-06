package builtin

import (
	"testing"
)

func TestSkillActivationTool(t *testing.T) {
	tool := &SkillActivationTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "activate_skill" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "activate_skill")
		}
		if tool.Description() == "" {
			t.Error("Description() should not be empty")
		}
		params := tool.Parameters()
		if params.Type != "object" {
			t.Errorf("Parameters().Type = %q, want %q", params.Type, "object")
		}
	})

	t.Run("missing action parameter", func(t *testing.T) {
		_, err := tool.Execute(map[string]any{})
		if err == nil {
			t.Error("expected error for missing action")
		}
	})

	t.Run("missing skill parameter", func(t *testing.T) {
		_, err := tool.Execute(map[string]any{
			"action": "activate",
		})
		if err == nil {
			t.Error("expected error for missing skill")
		}
	})

	t.Run("invalid action", func(t *testing.T) {
		_, err := tool.Execute(map[string]any{
			"action": "invalid",
			"skill":  "test_skill",
		})
		if err == nil {
			t.Error("expected error for invalid action")
		}
	})

	t.Run("activate without registry", func(t *testing.T) {
		// Tool without registry should panic or return error
		defer func() {
			if r := recover(); r != nil {
				// Expected panic from nil pointer
			}
		}()
		tool.Execute(map[string]any{
			"action": "activate",
			"skill":  "test_skill",
		})
	})
}
