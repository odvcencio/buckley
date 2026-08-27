package headless

import (
	"slices"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestRegistryCreateSession_ExplicitToolPolicyControlsProviderCatalog(t *testing.T) {
	tests := []struct {
		name    string
		policy  *ToolPolicy
		visible []string
	}{
		{
			name:    "non-core tool is visible",
			policy:  &ToolPolicy{AllowedTools: []string{"git_status"}},
			visible: []string{"git_status"},
		},
		{
			name:    "empty allowlist exposes no tools",
			policy:  &ToolPolicy{AllowedTools: []string{}},
			visible: []string{},
		},
		{
			name: "denied tool is removed from allowlist",
			policy: &ToolPolicy{
				AllowedTools: []string{"git_status", "read_file"},
				DeniedTools:  []string{"git_status"},
			},
			visible: []string{"read_file"},
		},
		{
			name:    "discovery requires explicit opt in",
			policy:  &ToolPolicy{AllowedTools: []string{"discover_tools", "git_status"}},
			visible: []string{"discover_tools", "git_status"},
		},
		{
			name:    "structured delegation is visible without legacy delegation",
			policy:  &ToolPolicy{AllowedTools: []string{"run_shell", "spawn_subagent"}},
			visible: []string{"run_shell", "spawn_subagent"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := newToolPolicyTestRunner(t, tt.policy)
			request := runner.buildRawChatRequest(runner.resolveExecutionModel())
			got := schemaFunctionNames(request.Tools)
			if !slices.Equal(got, tt.visible) {
				t.Fatalf("provider-visible tools = %v, want %v", got, tt.visible)
			}
			if slices.Contains(got, "invoke_buckley") {
				t.Fatal("legacy invoke_buckley must not be provider-visible")
			}
		})
	}
}

func TestRegistryCreateSession_DefaultPromptDoesNotAdvertiseFilteredTools(t *testing.T) {
	runner := newToolPolicyTestRunner(t, &ToolPolicy{AllowedTools: []string{"git_status"}})
	for _, leaked := range []string{"search_text", "read_file", "edit_file", "run_shell"} {
		if strings.Contains(runner.systemPrompt, leaked) {
			t.Fatalf("system prompt advertises filtered tool %q", leaked)
		}
	}
}

func TestRegistryCreateSession_StructuredDelegationKeepsGovernedChildControls(t *testing.T) {
	runner := newToolPolicyTestRunner(t, &ToolPolicy{AllowedTools: []string{"spawn_subagent"}})
	request := runner.buildRawChatRequest(runner.resolveExecutionModel())
	if len(request.Tools) != 1 {
		t.Fatalf("provider-visible tools = %v", schemaFunctionNames(request.Tools))
	}
	definition, _ := request.Tools[0]["function"].(map[string]any)
	parameters, _ := definition["parameters"].(builtin.ParameterSchema)
	for _, required := range []string{"model", "allowed_tools", "step_cap", "max_tool_calls", "max_model_requests", "max_elapsed_seconds", "max_cost_usd", "approval_posture"} {
		if _, ok := parameters.Properties[required]; !ok {
			t.Errorf("spawn_subagent schema is missing %q", required)
		}
	}
}

func newToolPolicyTestRunner(t *testing.T, policy *ToolPolicy) *Runner {
	t.Helper()
	root := t.TempDir()
	store := newTestStore(t)
	mgr := newTestModelManager(t)
	registry := NewRegistry(RegistryConfig{
		Store:        store,
		ModelManager: mgr,
		Config:       config.DefaultConfig(),
		ProjectRoot:  root,
	})
	t.Cleanup(registry.Stop)

	info, err := registry.CreateSession(CreateSessionRequest{Project: root, ToolPolicy: policy})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	runner, ok := registry.GetSession(info.ID)
	if !ok || runner == nil {
		t.Fatalf("expected runner for %s", info.ID)
	}
	return runner
}

func schemaFunctionNames(schemas []map[string]any) []string {
	names := make([]string, 0, len(schemas))
	for _, schema := range schemas {
		definition, _ := schema["function"].(map[string]any)
		name, _ := definition["name"].(string)
		names = append(names, name)
	}
	return names
}
