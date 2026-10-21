package tool

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestCodeOutline_DiscoveryKeepsDefaultSchemasSmall(t *testing.T) {
	registry := NewRegistry()
	registry.EnableDynamicDiscovery(nil)
	visible := registry.ToOpenAIFunctionsGoverned(nil, "interactive", "coding", nil, 0)
	before, _ := json.Marshal(visible)
	if slices.Contains(functionNames(visible), "code_outline") {
		t.Fatal("outline schema bloated default catalog")
	}
	discovery, _ := registry.Get("discover_tools")
	result, err := discovery.Execute(map[string]any{"query": "file symbol outline", "limit": 1})
	if err != nil || !result.Success {
		t.Fatalf("discover outline: %+v %v", result, err)
	}
	after := registry.ToOpenAIFunctionsGoverned(nil, "interactive", "coding", nil, 0)
	if !slices.Contains(functionNames(after), "code_outline") {
		t.Fatalf("outline not discoverable: %v", functionNames(after))
	}
	encoded, _ := json.Marshal(after)
	t.Logf("default_schema_bytes=%d discovered_schema_bytes=%d default_outline_schemas=0 discovered_outline_schemas=1", len(before), len(encoded))
}
