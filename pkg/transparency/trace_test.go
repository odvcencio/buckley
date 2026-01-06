package transparency

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/tools"
)

func TestTrace(t *testing.T) {
	trace := &Trace{
		ID:       "inv-123",
		Model:    "test-model",
		Provider: "openrouter",
		Tokens: TokenUsage{
			Input:  100,
			Output: 50,
		},
		Cost: 0.001,
	}

	if trace.HasToolCalls() {
		t.Error("expected no tool calls")
	}

	_, ok := trace.FirstToolCall()
	if ok {
		t.Error("expected FirstToolCall to return false")
	}
}

func TestTraceWithToolCalls(t *testing.T) {
	trace := &Trace{
		ID:    "inv-456",
		Model: "test-model",
		ToolCalls: []tools.ToolCall{
			{
				ID:        "call_1",
				Name:      "my_tool",
				Arguments: json.RawMessage(`{"value": 42}`),
			},
		},
	}

	if !trace.HasToolCalls() {
		t.Error("expected to have tool calls")
	}

	tc, ok := trace.FirstToolCall()
	if !ok {
		t.Fatal("expected FirstToolCall to return true")
	}
	if tc.Name != "my_tool" {
		t.Errorf("expected tool name 'my_tool', got %q", tc.Name)
	}

	var result struct {
		Value int `json:"value"`
	}
	if err := trace.UnmarshalToolCall(&result); err != nil {
		t.Fatalf("failed to unmarshal tool call: %v", err)
	}
	if result.Value != 42 {
		t.Errorf("expected value 42, got %d", result.Value)
	}
}

func TestTraceNoToolCallError(t *testing.T) {
	trace := &Trace{ID: "inv-789"}

	var result struct{}
	err := trace.UnmarshalToolCall(&result)
	if err == nil {
		t.Error("expected error when no tool calls")
	}

	noTCErr, ok := err.(*NoToolCallError)
	if !ok {
		t.Errorf("expected NoToolCallError, got %T", err)
	}
	if noTCErr.Expected != "any" {
		t.Errorf("expected 'any', got %q", noTCErr.Expected)
	}
}

func TestTraceBuilder(t *testing.T) {
	audit := NewContextAudit()
	audit.Add("test", 100)

	builder := NewTraceBuilder("inv-abc", "model-x", "provider-y")
	builder.WithContext(audit)
	builder.WithReasoning("I thought about it...")
	builder.WithContent("Here's the result")
	builder.WithToolCalls([]tools.ToolCall{
		{ID: "call_1", Name: "tool_a"},
	})

	trace := builder.Complete(TokenUsage{Input: 100, Output: 50}, 0.005)

	if trace.ID != "inv-abc" {
		t.Errorf("expected ID 'inv-abc', got %q", trace.ID)
	}
	if trace.Model != "model-x" {
		t.Errorf("expected model 'model-x', got %q", trace.Model)
	}
	if trace.Provider != "provider-y" {
		t.Errorf("expected provider 'provider-y', got %q", trace.Provider)
	}
	if trace.Context == nil {
		t.Error("expected context to be set")
	}
	if trace.Reasoning != "I thought about it..." {
		t.Errorf("unexpected reasoning: %q", trace.Reasoning)
	}
	if trace.Content != "Here's the result" {
		t.Errorf("unexpected content: %q", trace.Content)
	}
	if len(trace.ToolCalls) != 1 {
		t.Errorf("expected 1 tool call, got %d", len(trace.ToolCalls))
	}
	if trace.Tokens.Input != 100 {
		t.Errorf("expected 100 input tokens, got %d", trace.Tokens.Input)
	}
	if trace.Cost != 0.005 {
		t.Errorf("expected cost 0.005, got %f", trace.Cost)
	}
	if trace.Duration <= 0 {
		t.Error("expected positive duration")
	}
}

func TestTraceBuilderWithError(t *testing.T) {
	builder := NewTraceBuilder("inv-err", "model", "provider")

	// Simulate some work
	time.Sleep(1 * time.Millisecond)

	builder.WithError(errTestError{})
	trace := builder.Build()

	if trace.Error != "test error" {
		t.Errorf("expected error 'test error', got %q", trace.Error)
	}
	if trace.Duration <= 0 {
		t.Error("expected positive duration even on error")
	}
}

type errTestError struct{}

func (e errTestError) Error() string { return "test error" }
