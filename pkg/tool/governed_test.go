package tool

import (
	"testing"

	"github.com/odvcencio/buckley/pkg/tool/builtin"
	"github.com/odvcencio/buckley/pkg/types"
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
