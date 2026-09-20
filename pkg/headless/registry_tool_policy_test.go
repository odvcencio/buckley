package headless

import (
	"slices"
	"testing"

	"m31labs.dev/buckley/pkg/tool"
)

func TestApplyToolPolicy_ModelVisibleAllowlist(t *testing.T) {
	tests := []struct {
		name   string
		policy ToolPolicy
		pool   string
		want   []string
	}{
		{
			name:   "specialized tools are visible without discovery",
			policy: ToolPolicy{AllowedTools: []string{"git_status", "spawn_subagent", "apply_patch"}},
			want:   []string{"apply_patch", "git_status", "spawn_subagent"},
		},
		{
			name:   "denied tools stay excluded",
			policy: ToolPolicy{AllowedTools: []string{"git_status", "spawn_subagent"}, DeniedTools: []string{"spawn_subagent"}},
			want:   []string{"git_status"},
		},
		{
			name:   "names are normalized and unknown tools stay absent",
			policy: ToolPolicy{AllowedTools: []string{" git_status ", "git_status", "missing_tool", " "}},
			want:   []string{"git_status"},
		},
		{
			name:   "empty allowlist exposes nothing",
			policy: ToolPolicy{AllowedTools: []string{}},
		},
		{
			name:   "pool restrictions still apply",
			policy: ToolPolicy{AllowedTools: []string{"git_status", "spawn_subagent"}},
			pool:   "standard",
			want:   []string{"git_status"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := tool.NewRegistry()
			registry.SetDefaultPoolMode(tt.pool)
			registry.EnableDynamicDiscovery(nil)
			applyToolPolicy(registry, &tt.policy)

			functions := registry.ToOpenAIFunctionsGoverned(nil, "interactive", "coding", nil, 0)
			var names []string
			for _, fn := range functions {
				definition := fn["function"].(map[string]any)
				names = append(names, definition["name"].(string))
			}
			if !slices.Equal(names, tt.want) {
				t.Fatalf("model-visible tools = %v, want %v", names, tt.want)
			}
		})
	}
}

func TestApplyToolPolicy_DenyOnlyPreservesDynamicDiscovery(t *testing.T) {
	registry := tool.NewRegistry()
	registry.EnableDynamicDiscovery(nil)
	applyToolPolicy(registry, &ToolPolicy{DeniedTools: []string{"read_file"}})

	functions := registry.ToOpenAIFunctionsGoverned(nil, "interactive", "coding", nil, 0)
	var names []string
	for _, fn := range functions {
		definition := fn["function"].(map[string]any)
		names = append(names, definition["name"].(string))
	}
	if !slices.Contains(names, "discover_tools") || slices.Contains(names, "read_file") || slices.Contains(names, "git_status") {
		t.Fatalf("deny-only policy changed discovery behavior: %v", names)
	}
	if _, ok := registry.Get("git_status"); !ok {
		t.Fatal("specialized tool is no longer available for discovery")
	}
}
