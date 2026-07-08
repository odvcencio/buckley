package tools

import (
	"encoding/json"
	"strings"
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

// TestToolCallUnmarshal_RepairsGLMNumericLiteralQuirk reproduces the live
// `buckley pr`/`buckley commit` bug under z-ai/glm-5.2: a tool call whose
// Arguments JSON has a stray space injected right after a leading '-' in a
// numeric literal (GLM's tool-call JSON quirk observed via
// OpenRouter/vLLM). Unrepaired, this fails encoding/json with exactly
// "invalid character ' ' in numeric literal" -- the literal error text from
// the bug report -- surfacing to callers as "unmarshal tool call: invalid
// character ' ' in numeric literal". Unmarshal must recover via
// jsonrepair.TryUnmarshal instead of propagating that failure.
func TestToolCallUnmarshal_RepairsGLMNumericLiteralQuirk(t *testing.T) {
	broken := ToolCall{
		ID:        "call_glm",
		Name:      "generate_pull_request",
		Arguments: []byte(`{"title": "fix bug", "confidence": - 5}`),
	}

	// Sanity check: confirm the unrepaired payload reproduces the exact
	// live error text from the bug report.
	var probe map[string]any
	if err := json.Unmarshal(broken.Arguments, &probe); err == nil {
		t.Fatalf("sanity check failed: expected the unrepaired payload to fail unmarshal")
	} else if !strings.Contains(err.Error(), "invalid character ' ' in numeric literal") {
		t.Fatalf("sanity check failed: expected exact reported error text, got: %v", err)
	}

	var result struct {
		Title      string  `json:"title"`
		Confidence float64 `json:"confidence"`
	}
	if err := broken.Unmarshal(&result); err != nil {
		t.Fatalf("unmarshal tool call: %v", err)
	}
	if result.Title != "fix bug" {
		t.Errorf("Title = %q, want %q", result.Title, "fix bug")
	}
	if result.Confidence != -5 {
		t.Errorf("Confidence = %v, want -5", result.Confidence)
	}
}

// TestToolCallUnmarshal_TrulyInvalidStillErrors proves the repair
// defense-in-depth doesn't mask genuinely malformed argument JSON -- it must
// still return an error, not silently succeed with zero values.
func TestToolCallUnmarshal_TrulyInvalidStillErrors(t *testing.T) {
	tc := ToolCall{
		ID:        "call_bad",
		Name:      "test_tool",
		Arguments: []byte(`{not json at all`),
	}

	var result struct {
		Name string `json:"name"`
	}
	if err := tc.Unmarshal(&result); err == nil {
		t.Fatalf("expected an error for genuinely malformed JSON, got nil")
	}
}
