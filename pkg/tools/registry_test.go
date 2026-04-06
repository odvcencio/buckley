package tools

import (
	"testing"
)

func TestRegistry(t *testing.T) {
	r := NewRegistry()

	// Test registration
	def := Definition{
		Name:        "test_tool",
		Description: "A test tool",
		Parameters:  ObjectSchema(map[string]Property{}, ""),
	}

	if err := r.Register(def); err != nil {
		t.Fatalf("failed to register tool: %v", err)
	}

	// Test duplicate registration
	if err := r.Register(def); err == nil {
		t.Error("expected error on duplicate registration")
	}

	// Test Get
	got, ok := r.Get("test_tool")
	if !ok {
		t.Error("expected to find registered tool")
	}
	if got.Name != "test_tool" {
		t.Errorf("expected name 'test_tool', got %q", got.Name)
	}

	// Test Get missing
	_, ok = r.Get("nonexistent")
	if ok {
		t.Error("expected not to find nonexistent tool")
	}

	// Test List
	list := r.List()
	if len(list) != 1 {
		t.Errorf("expected 1 tool, got %d", len(list))
	}

	// Test Names
	names := r.Names()
	if len(names) != 1 || names[0] != "test_tool" {
		t.Errorf("expected ['test_tool'], got %v", names)
	}
}

func TestRegistryEmptyName(t *testing.T) {
	r := NewRegistry()
	def := Definition{
		Name: "",
	}
	if err := r.Register(def); err == nil {
		t.Error("expected error on empty name")
	}
}

func TestRegistrySubset(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Definition{Name: "tool_a", Description: "A"}); err != nil {
		t.Fatalf("failed to register tool_a: %v", err)
	}
	if err := r.Register(Definition{Name: "tool_b", Description: "B"}); err != nil {
		t.Fatalf("failed to register tool_b: %v", err)
	}
	if err := r.Register(Definition{Name: "tool_c", Description: "C"}); err != nil {
		t.Fatalf("failed to register tool_c: %v", err)
	}

	subset := r.Subset("tool_a", "tool_c", "nonexistent")

	if len(subset.Names()) != 2 {
		t.Errorf("expected 2 tools in subset, got %d", len(subset.Names()))
	}

	if _, ok := subset.Get("tool_a"); !ok {
		t.Error("expected tool_a in subset")
	}
	if _, ok := subset.Get("tool_c"); !ok {
		t.Error("expected tool_c in subset")
	}
	if _, ok := subset.Get("tool_b"); ok {
		t.Error("expected tool_b NOT in subset")
	}
}

func TestDefinitionFormats(t *testing.T) {
	def := Definition{
		Name:        "my_tool",
		Description: "Does something",
		Parameters: ObjectSchema(
			map[string]Property{
				"input": StringProperty("The input"),
			},
			"input",
		),
	}

	// Test OpenAI format
	openai := def.ToOpenAIFormat()
	if openai["type"] != "function" {
		t.Errorf("expected type 'function', got %v", openai["type"])
	}
	fn := openai["function"].(map[string]any)
	if fn["name"] != "my_tool" {
		t.Errorf("expected name 'my_tool', got %v", fn["name"])
	}

	// Test Anthropic format
	anthropic := def.ToAnthropicFormat()
	if anthropic["name"] != "my_tool" {
		t.Errorf("expected name 'my_tool', got %v", anthropic["name"])
	}
	if anthropic["input_schema"] == nil {
		t.Error("expected input_schema to be set")
	}
}

func TestToolCallUnmarshal(t *testing.T) {
	tc := ToolCall{
		ID:        "call_123",
		Name:      "test_tool",
		Arguments: []byte(`{"name": "test", "value": 42}`),
	}

	var result struct {
		Name  string `json:"name"`
		Value int    `json:"value"`
	}

	if err := tc.Unmarshal(&result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if result.Name != "test" {
		t.Errorf("expected name 'test', got %q", result.Name)
	}
	if result.Value != 42 {
		t.Errorf("expected value 42, got %d", result.Value)
	}
}
