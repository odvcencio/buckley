package conversation

import (
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
)

func TestCompactModelMessages_PreservesRecentAndCompactsOldExecution(t *testing.T) {
	large := strings.Repeat("execution output ", 500)
	messages := []model.Message{
		{Role: "user", Content: "keep my steering exactly"},
		{Role: "assistant", Reasoning: strings.Repeat("private reasoning ", 100), ToolCalls: []model.ToolCall{{ID: "one", Function: model.FunctionCall{Name: "run_shell", Arguments: `{}`}}}},
		{Role: "tool", Name: "run_shell", ToolCallID: "one", Content: large},
		{Role: "assistant", Content: strings.Repeat("old explanation ", 500), Reasoning: "old thought"},
		{Role: "user", Content: "latest"},
		{Role: "assistant", Reasoning: "recent thought", ToolCalls: []model.ToolCall{{ID: "two", Function: model.FunctionCall{Name: "read_file", Arguments: `{}`}}}},
		{Role: "tool", Name: "read_file", ToolCallID: "two", Content: large},
	}

	got := CompactModelMessages(messages, EfficientContextOptions{
		RecentMessages:      3,
		OldToolBytes:        240,
		OldAssistantBytes:   240,
		KeepReasoningRecent: 3,
		MaxBytes:            1 << 20,
	})
	if got[0].Content != messages[0].Content {
		t.Fatal("user steering was changed")
	}
	if got[1].Reasoning != "" || len(got[1].ToolCalls) != 1 {
		t.Fatal("old reasoning should be removed without removing its tool call")
	}
	if len(got[2].Content.(string)) > 240 || !strings.Contains(got[2].Content.(string), "compacted") {
		t.Fatalf("old tool output was not compacted: %q", got[2].Content)
	}
	if got[5].Reasoning != "recent thought" || got[6].Content != large {
		t.Fatal("recent execution context was modified")
	}
	if messages[1].Reasoning == "" || messages[2].Content != large {
		t.Fatal("input transcript was mutated")
	}
}

func TestCompactModelMessages_DeduplicatesOldToolResults(t *testing.T) {
	messages := []model.Message{
		{Role: "tool", Name: "read_file", ToolCallID: "one", Content: "same"},
		{Role: "tool", Name: "read_file", ToolCallID: "two", Content: "same"},
	}
	got := CompactModelMessages(messages, EfficientContextOptions{RecentMessages: 1, OldToolBytes: 100, KeepReasoningRecent: 1, MaxBytes: 1 << 20})
	if !strings.Contains(got[0].Content.(string), "duplicate") {
		t.Fatalf("duplicate result not replaced: %q", got[0].Content)
	}
}

func TestCompactModelMessages_EnforcesBurstBudget(t *testing.T) {
	messages := make([]model.Message, 20)
	for i := range messages {
		messages[i] = model.Message{Role: "tool", Name: "run_shell", Content: strings.Repeat("x", 10_000)}
	}
	got := CompactModelMessages(messages, EfficientContextOptions{
		RecentMessages: 20, OldToolBytes: 200, OldAssistantBytes: 200,
		KeepReasoningRecent: 2, MaxBytes: 70_000,
	})
	if size := modelMessagesBytes(got); size > 70_000 {
		t.Fatalf("compacted size = %d, want <= 70000", size)
	}
	if got[len(got)-1].Content != messages[len(messages)-1].Content {
		t.Fatal("immediate tail should remain exact")
	}
}
