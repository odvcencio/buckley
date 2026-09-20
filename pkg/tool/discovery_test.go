package tool

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/tool/builtin"
	"m31labs.dev/buckley/pkg/types"
)

func TestDynamicDiscovery_ExposesSmallStableWorkingSet(t *testing.T) {
	registry := NewRegistry()
	full, _ := json.Marshal(registry.ToOpenAIFunctions())
	registry.EnableDynamicDiscovery([]string{"read_file"})

	visible := registry.ToOpenAIFunctionsGoverned(nil, "interactive", "coding", nil, 0)
	compact, _ := json.Marshal(visible)
	if len(compact)*2 >= len(full) {
		t.Fatalf("dynamic catalog did not materially reduce schemas: full=%d compact=%d", len(full), len(compact))
	}
	t.Logf("tool schema bytes: full=%d compact=%d", len(full), len(compact))
	names := functionNames(visible)
	if strings.Join(names, ",") != "discover_tools,read_file" {
		t.Fatalf("visible tools = %v", names)
	}

	discovery, ok := registry.Get("discover_tools")
	if !ok {
		t.Fatal("discover_tools was not registered")
	}
	result, err := discovery.Execute(map[string]any{"names": []any{"git_diff"}})
	if err != nil || !result.Success {
		t.Fatalf("discover exact tool: result=%+v err=%v", result, err)
	}
	visible = registry.ToOpenAIFunctionsGoverned(nil, "interactive", "coding", nil, 0)
	names = functionNames(visible)
	if strings.Join(names, ",") != "discover_tools,git_diff,read_file" {
		t.Fatalf("visible tools after discovery = %v", names)
	}
}

func TestDynamicDiscovery_DefaultCatalogStaysUnderSchemaBudget(t *testing.T) {
	registry := NewRegistry()
	registry.EnableDynamicDiscovery(nil)
	visible, _ := json.Marshal(registry.ToOpenAIFunctionsGoverned(nil, "interactive", "coding", nil, 0))
	if len(visible) > 10_000 {
		t.Fatalf("default dynamic schema catalog = %d bytes, budget 10000", len(visible))
	}
	t.Logf("default dynamic tool schema bytes: %d", len(visible))
}

func TestDynamicDiscovery_ExposesExecProgramWhenOptInToolIsRegistered(t *testing.T) {
	registry := NewEmptyRegistry()
	registry.EnableDynamicDiscovery(nil)
	registry.Register(&governedTestTool{name: "exec_program"})

	visible := registry.ToOpenAIFunctionsGoverned(nil, "interactive", "coding", nil, 0)
	names := functionNames(visible)
	if strings.Join(names, ",") != "exec_program,discover_tools" {
		t.Fatalf("visible code-mode tools = %v", names)
	}
}

func TestDynamicDiscovery_DefaultCatalogIncludesRegisteredApplyPatch(t *testing.T) {
	registry := NewRegistry()
	registry.EnableDynamicDiscovery(nil)

	visible := registry.ToOpenAIFunctionsGoverned(nil, "interactive", "coding", nil, 0)
	names := functionNames(visible)
	if !slices.Contains(names, "apply_patch") {
		t.Fatalf("default visible tools = %v, want registered apply_patch", names)
	}
	if slices.Contains(names, "patch_file") {
		t.Fatalf("default visible tools contain stale patch_file name: %v", names)
	}

	var patchDefinition map[string]any
	for _, function := range visible {
		definition, _ := function["function"].(map[string]any)
		if definition["name"] == "apply_patch" {
			patchDefinition = definition
			break
		}
	}
	if patchDefinition == nil {
		t.Fatal("apply_patch schema missing from governed catalog")
	}
	parameters, ok := patchDefinition["parameters"].(builtin.ParameterSchema)
	if !ok || !slices.Contains(parameters.Required, "patch") {
		t.Fatalf("apply_patch parameters = %#v, want required patch field", patchDefinition["parameters"])
	}
}

func TestDynamicDiscovery_GovernanceStillRestrictsApplyPatch(t *testing.T) {
	tests := []struct {
		name      string
		allowed   []string
		poolMode  string
		evaluator types.RuleEvaluator
	}{
		{
			name:    "allowed tool filter",
			allowed: []string{"read_file"},
		},
		{
			name:     "read only pool",
			poolMode: "read_only",
		},
		{
			name: "arbiter exclusion",
			evaluator: &mockEvaluator{results: map[[2]string]types.StrategyResult{
				{"runtime/concurrency", "pool_policy"}: {
					Params: map[string]any{"exclude_tools": "apply_patch"},
				},
			}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			registry := NewRegistry()
			if tc.poolMode != "" {
				registry.SetDefaultPoolMode(tc.poolMode)
			}
			registry.EnableDynamicDiscovery(nil)

			visible := registry.ToOpenAIFunctionsGoverned(tc.evaluator, "interactive", "coding", tc.allowed, 0)
			if names := functionNames(visible); slices.Contains(names, "apply_patch") {
				t.Fatalf("restricted visible tools = %v, apply_patch must remain unavailable", names)
			}
		})
	}
}

func TestToModelOutput_OmitsHarnessOnlyFields(t *testing.T) {
	result := &builtin.Result{
		Success:       true,
		Data:          map[string]any{"full": "large"},
		DisplayData:   map[string]any{"summary": "small"},
		ShouldAbridge: true,
		NeedsApproval: true,
		ApprovalFunc:  func(bool) {},
	}
	encoded, err := ToModelOutput(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, unwanted := range []string{"ApprovalFunc", "display_data", "should_abridge", "needs_approval", "large", "omitempty"} {
		if strings.Contains(encoded, unwanted) {
			t.Fatalf("model output contains %q: %s", unwanted, encoded)
		}
	}
	if !strings.Contains(encoded, "small") {
		t.Fatalf("model output omitted abridged data: %s", encoded)
	}
}

func functionNames(functions []map[string]any) []string {
	result := make([]string, 0, len(functions))
	for _, function := range functions {
		definition, _ := function["function"].(map[string]any)
		name, _ := definition["name"].(string)
		result = append(result, name)
	}
	return result
}
