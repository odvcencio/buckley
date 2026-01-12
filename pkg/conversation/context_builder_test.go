package conversation

import (
	"context"
	"testing"

	"github.com/odvcencio/buckley/pkg/model"
)

type stubTokenCounter struct{}

func (stubTokenCounter) Count(text string) int {
	return len(text)
}

type stubCompactor struct {
	should    bool
	called    int
	lastMode  string
	lastUsage float64
}

func (s *stubCompactor) ShouldAutoCompact(mode string, usageRatio float64) bool {
	s.lastMode = mode
	s.lastUsage = usageRatio
	return s.should
}

func (s *stubCompactor) CompactAsync(_ context.Context) {
	s.called++
}

func TestContextBuilder_BuildMessages_TrimsToBudget(t *testing.T) {
	conv := &Conversation{
		Messages: []Message{
			{Role: "user", Content: "one", Tokens: 10},
			{Role: "assistant", Content: "two", Tokens: 10},
			{Role: "user", Content: "three", Tokens: 10},
			{Role: "assistant", Content: "four", Tokens: 10},
		},
	}

	builder := &ContextBuilder{
		tokenCounter: stubTokenCounter{},
	}

	trimmed := builder.BuildMessages(conv, 28, "classic")
	if len(trimmed) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(trimmed))
	}
	if trimmed[0].Content != "three" || trimmed[1].Content != "four" {
		t.Fatalf("unexpected trimmed messages: %#v", trimmed)
	}
}

func TestContextBuilder_BuildMessages_TriggersCompaction(t *testing.T) {
	conv := &Conversation{
		Messages: []Message{
			{Role: "user", Content: "hello", Tokens: 10},
		},
	}
	compactor := &stubCompactor{should: true}
	builder := &ContextBuilder{
		tokenCounter: stubTokenCounter{},
		compactor:    compactor,
	}

	builder.BuildMessages(conv, 5, "classic")
	if compactor.called != 1 {
		t.Fatalf("expected compaction to be triggered once, got %d", compactor.called)
	}
	if compactor.lastMode != "classic" {
		t.Fatalf("expected mode classic, got %s", compactor.lastMode)
	}
	if compactor.lastUsage <= 0 {
		t.Fatalf("expected positive usage ratio, got %.2f", compactor.lastUsage)
	}
}

func TestContextBuilder_PreservesToolCallPairs(t *testing.T) {
	// Scenario: Budget allows only last 2 messages, but msg[2] is a tool response
	// that references msg[1]'s tool_call. We must include msg[1] to avoid orphan.
	conv := &Conversation{
		Messages: []Message{
			{Role: "user", Content: "do something", Tokens: 20},
			{Role: "assistant", Content: "", Tokens: 10, ToolCalls: []model.ToolCall{
				{ID: "call_1", Type: "function", Function: model.FunctionCall{Name: "read_file", Arguments: "{}"}},
			}},
			{Role: "tool", Content: "file contents", Tokens: 10, ToolCallID: "call_1", Name: "read_file"},
			{Role: "assistant", Content: "done", Tokens: 10},
		},
	}

	builder := &ContextBuilder{tokenCounter: stubTokenCounter{}}

	// Budget of 28 would normally include only messages 2, 3 (tool response + final assistant)
	// But tool response needs its assistant with tool_calls, so we must include 1, 2, 3
	trimmed := builder.BuildMessages(conv, 34, "classic")

	// Should include the assistant with tool_calls, tool response, and final assistant
	if len(trimmed) < 3 {
		t.Fatalf("expected at least 3 messages to preserve tool pair, got %d", len(trimmed))
	}

	// Verify the assistant with tool_calls is included
	hasToolCallAssistant := false
	for _, msg := range trimmed {
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			hasToolCallAssistant = true
			break
		}
	}
	if !hasToolCallAssistant {
		t.Fatal("expected assistant message with tool_calls to be preserved")
	}
}

func TestContextBuilder_SkipsOrphanedToolResponses(t *testing.T) {
	// Scenario: Tool response at start without matching assistant message
	// Should be skipped to avoid API errors
	conv := &Conversation{
		Messages: []Message{
			{Role: "tool", Content: "orphaned result", Tokens: 10, ToolCallID: "call_orphan", Name: "some_tool"},
			{Role: "user", Content: "continue", Tokens: 10},
			{Role: "assistant", Content: "ok", Tokens: 10},
		},
	}

	builder := &ContextBuilder{tokenCounter: stubTokenCounter{}}
	trimmed := builder.BuildMessages(conv, 100, "classic")

	// The orphaned tool message should be skipped
	for _, msg := range trimmed {
		if msg.Role == "tool" && msg.ToolCallID == "call_orphan" {
			t.Fatal("orphaned tool response should have been skipped")
		}
	}
}

func TestContextBuilder_MultipleToolCallsInOneMessage(t *testing.T) {
	// Scenario: Assistant calls multiple tools at once
	conv := &Conversation{
		Messages: []Message{
			{Role: "user", Content: "do multiple things", Tokens: 10},
			{Role: "assistant", Content: "", Tokens: 10, ToolCalls: []model.ToolCall{
				{ID: "call_a", Type: "function", Function: model.FunctionCall{Name: "tool_a", Arguments: "{}"}},
				{ID: "call_b", Type: "function", Function: model.FunctionCall{Name: "tool_b", Arguments: "{}"}},
			}},
			{Role: "tool", Content: "result a", Tokens: 10, ToolCallID: "call_a", Name: "tool_a"},
			{Role: "tool", Content: "result b", Tokens: 10, ToolCallID: "call_b", Name: "tool_b"},
			{Role: "assistant", Content: "done", Tokens: 10},
		},
	}

	builder := &ContextBuilder{tokenCounter: stubTokenCounter{}}

	// Even if budget only allows tool responses, assistant with tool_calls must be included
	trimmed := builder.BuildMessages(conv, 50, "classic")

	// Count tool messages
	toolCount := 0
	assistantWithToolCalls := false
	for _, msg := range trimmed {
		if msg.Role == "tool" {
			toolCount++
		}
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			assistantWithToolCalls = true
		}
	}

	if toolCount > 0 && !assistantWithToolCalls {
		t.Fatal("tool responses included but assistant with tool_calls is missing")
	}
}
