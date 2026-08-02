package tool

import (
	"encoding/json"
	"testing"

	"m31labs.dev/buckley/pkg/tool/builtin"
	"m31labs.dev/buckley/pkg/types"
)

// TestCanopyToolSchemas_StayUnderCombinedBudget guards the Pillar C schema
// discipline requirement: code_callgraph, code_refs, and code_impact
// together must stay under a 1.5 KB combined OpenAI-function schema budget.
func TestCanopyToolSchemas_StayUnderCombinedBudget(t *testing.T) {
	const budgetBytes = 1536 // 1.5 KB

	canopyTools := []Tool{
		&builtin.CodeCallgraphTool{},
		&builtin.CodeRefsTool{},
		&builtin.CodeImpactTool{},
	}

	total := 0
	for _, current := range canopyTools {
		encoded, err := json.Marshal(ToOpenAIFunction(current))
		if err != nil {
			t.Fatalf("marshal schema for %s: %v", current.Name(), err)
		}
		total += len(encoded)
	}

	t.Logf("canopy tool schema bytes: %d (budget %d)", total, budgetBytes)
	if total > budgetBytes {
		t.Fatalf("canopy tool schemas = %d bytes, budget %d", total, budgetBytes)
	}
}

// TestCanopyTools_ClassifyAsFilesystemReadOnly mirrors
// TestGetMetadata_DynamicCodeMatchesShell: the canopy query tools are
// read-only and must carry explicit metadata rather than depend on
// inferMetadata's substring fallback silently matching.
func TestCanopyTools_ClassifyAsFilesystemReadOnly(t *testing.T) {
	canopyTools := []Tool{
		&builtin.CodeCallgraphTool{},
		&builtin.CodeRefsTool{},
		&builtin.CodeImpactTool{},
	}

	for _, current := range canopyTools {
		t.Run(current.Name(), func(t *testing.T) {
			meta := GetMetadata(current)
			if meta.Category != CategoryFilesystem {
				t.Errorf("category = %v, want %v", meta.Category, CategoryFilesystem)
			}
			if meta.Impact != ImpactReadOnly {
				t.Errorf("impact = %v, want %v", meta.Impact, ImpactReadOnly)
			}
			if RequiredTierForTool(current) != types.TierReadOnly {
				t.Errorf("required tier = %v, want %v", RequiredTierForTool(current), types.TierReadOnly)
			}
		})
	}
}
