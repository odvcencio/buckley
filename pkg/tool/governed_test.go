package tool

import (
	"testing"

	"m31labs.dev/buckley/pkg/tool/builtin"
	"m31labs.dev/buckley/pkg/types"
)

type governedTestTool struct {
	name     string
	metadata ToolMetadata
}

func (t *governedTestTool) Name() string {
	return t.name
}

func (t *governedTestTool) Description() string {
	return t.name
}

func (t *governedTestTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{Type: "object"}
}

func (t *governedTestTool) Execute(params map[string]any) (*builtin.Result, error) {
	return &builtin.Result{Success: true}, nil
}

func (t *governedTestTool) Metadata() ToolMetadata {
	return t.metadata
}

func TestGovernedToolNames_AppliesPoolModeAndExclusions(t *testing.T) {
	registry := NewEmptyRegistry()
	registry.Register(&governedTestTool{
		name: "read_file",
		metadata: ToolMetadata{
			Category: CategoryFilesystem,
			Impact:   ImpactReadOnly,
		},
	})
	registry.Register(&governedTestTool{
		name: "write_file",
		metadata: ToolMetadata{
			Category: CategoryFilesystem,
			Impact:   ImpactModifying,
		},
	})
	registry.Register(&governedTestTool{
		name: "buckley",
		metadata: ToolMetadata{
			Category: CategoryDelegation,
			Impact:   ImpactReadOnly,
		},
	})

	evaluator := &mockEvaluator{
		results: map[[2]string]types.StrategyResult{
			{"runtime/concurrency", "pool_policy"}: {
				Params: map[string]any{
					"mode":          "standard",
					"exclude_tools": "read_file",
				},
			},
		},
	}

	got := GovernedToolNames(registry, evaluator, "interactive", "coding", nil, 0)
	if len(got) != 1 || got[0] != "write_file" {
		t.Fatalf("GovernedToolNames() = %v, want [write_file]", got)
	}
}

func TestRegistry_ToOpenAIFunctionsGoverned_RespectsAllowedTools(t *testing.T) {
	registry := NewEmptyRegistry()
	registry.Register(&governedTestTool{name: "read_file"})
	registry.Register(&governedTestTool{name: "write_file"})

	functions := registry.ToOpenAIFunctionsGoverned(nil, "interactive", "coding", []string{"write_file"}, 0)
	if len(functions) != 1 {
		t.Fatalf("expected 1 function, got %d", len(functions))
	}

	functionDef, _ := functions[0]["function"].(map[string]any)
	if functionDef["name"] != "write_file" {
		t.Fatalf("unexpected function name: %+v", functionDef)
	}
}

func TestRegistry_ToOpenAIFunctionsGoverned_EmptyAllowedMeansNoTools(t *testing.T) {
	registry := NewEmptyRegistry()
	registry.Register(&governedTestTool{name: "read_file"})
	registry.Register(&governedTestTool{name: "write_file"})

	functions := registry.ToOpenAIFunctionsGoverned(nil, "interactive", "coding", []string{}, 0)
	if len(functions) != 0 {
		t.Fatalf("expected no functions for empty allowed list, got %d", len(functions))
	}
}

func newPoolModeTestRegistry() *Registry {
	registry := NewEmptyRegistry()
	registry.Register(&governedTestTool{
		name: "read_file",
		metadata: ToolMetadata{
			Category: CategoryFilesystem,
			Impact:   ImpactReadOnly,
		},
	})
	registry.Register(&governedTestTool{
		name: "write_file",
		metadata: ToolMetadata{
			Category: CategoryFilesystem,
			Impact:   ImpactModifying,
		},
	})
	registry.Register(&governedTestTool{
		name: "buckley",
		metadata: ToolMetadata{
			Category: CategoryDelegation,
			Impact:   ImpactReadOnly,
		},
	})
	return registry
}

func TestGovernedToolNames_NoEvaluatorNoConfigKeepsFullPool(t *testing.T) {
	registry := newPoolModeTestRegistry()

	got := GovernedToolNames(registry, nil, "interactive", "coding", nil, 0)
	if len(got) != 3 {
		t.Fatalf("GovernedToolNames() = %v, want all 3 tools (default pool mode unset must not change behavior)", got)
	}
}

func TestGovernedToolNames_DefaultPoolModeAppliesWhenEvaluatorNil(t *testing.T) {
	registry := newPoolModeTestRegistry()
	registry.SetDefaultPoolMode("standard")

	got := GovernedToolNames(registry, nil, "interactive", "coding", nil, 0)
	want := []string{"read_file", "write_file"}
	if len(got) != len(want) {
		t.Fatalf("GovernedToolNames() = %v, want %v (standard mode excludes delegation tools)", got, want)
	}
	for i, name := range want {
		if got[i] != name {
			t.Fatalf("GovernedToolNames() = %v, want %v", got, want)
		}
	}
}

func TestGovernedToolNames_EvaluatorModeOverridesConfiguredDefault(t *testing.T) {
	registry := newPoolModeTestRegistry()
	registry.SetDefaultPoolMode("standard")

	evaluator := &mockEvaluator{
		results: map[[2]string]types.StrategyResult{
			{"runtime/concurrency", "pool_policy"}: {
				Params: map[string]any{"mode": "full"},
			},
		},
	}

	got := GovernedToolNames(registry, evaluator, "interactive", "coding", nil, 0)
	if len(got) != 3 {
		t.Fatalf("GovernedToolNames() = %v, want all 3 tools (evaluator mode must override configured default)", got)
	}
}
