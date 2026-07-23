package tool

import (
	"encoding/json"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/tool/builtin"
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
