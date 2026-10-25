package builtin

import (
	"reflect"
	"testing"

	"m31labs.dev/buckley/pkg/skill"
)

func TestSkillActivationTool_RecomputesRemainingRestrictions(t *testing.T) {
	registry := skill.NewRegistry()
	for _, s := range []*skill.Skill{
		{Name: "a", Description: "A", AllowedTools: []string{"read_file", "run_tests"}},
		{Name: "b", Description: "B", AllowedTools: []string{"read_file", "list_directory"}},
		{Name: "guidance", Description: "Guidance without a filter"},
	} {
		if err := registry.Register(s); err != nil {
			t.Fatal(err)
		}
	}
	state := skill.NewRuntimeState(nil)
	state.SetToolCeiling([]string{"read_file", "list_directory"})
	tool := &SkillActivationTool{Registry: registry, Conversation: state}
	for _, step := range []struct {
		action, name string
		want         []string
	}{
		{"activate", "a", []string{"read_file"}},
		{"activate", "b", []string{"read_file"}},
		{"activate", "guidance", []string{"read_file"}},
		{"deactivate", "a", []string{"list_directory", "read_file"}},
		{"deactivate", "b", []string{"read_file", "list_directory"}},
		{"deactivate", "guidance", []string{"read_file", "list_directory"}},
	} {
		result, err := tool.Execute(map[string]any{"action": step.action, "skill": step.name})
		if err != nil || !result.Success {
			t.Fatalf("%s %s: result=%+v err=%v", step.action, step.name, result, err)
		}
		if got := state.ToolFilter(); !reflect.DeepEqual(got, step.want) {
			t.Fatalf("after %s %s: tools=%v want=%v", step.action, step.name, got, step.want)
		}
	}
}
