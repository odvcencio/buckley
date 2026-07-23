package conversation

import (
	"encoding/json"
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

func TestCompactModelMessages_CompactsOldToolArgumentsWithoutMutatingTranscript(t *testing.T) {
	arguments := `{"path":"pkg/example.go","content":"` + strings.Repeat("generated source ", 1000) + `"}`
	messages := []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{{
			ID: "write", Function: model.FunctionCall{Name: "write_file", Arguments: arguments},
		}}},
		{Role: "tool", Name: "write_file", ToolCallID: "write", Content: "ok"},
		{Role: "user", Content: "continue"},
	}

	got := CompactModelMessages(messages, EfficientContextOptions{
		RecentMessages: 1, OldToolBytes: 100, OldToolArgumentBytes: 300,
		OldAssistantBytes: 100, KeepReasoningRecent: 1, MaxBytes: 1 << 20,
	})
	compacted := got[0].ToolCalls[0].Function.Arguments
	if len(compacted) > 300 {
		t.Fatalf("compacted arguments = %d bytes, want <= 300", len(compacted))
	}
	var summary map[string]any
	if err := json.Unmarshal([]byte(compacted), &summary); err != nil {
		t.Fatalf("compacted arguments are not valid JSON: %v", err)
	}
	if summary["_buckley_compacted"] != true || summary["path"] != "pkg/example.go" {
		t.Fatalf("compacted arguments lost useful identity: %#v", summary)
	}
	if messages[0].ToolCalls[0].Function.Arguments != arguments {
		t.Fatal("input transcript was mutated")
	}
}

func TestCompactModelMessages_PreservesRecentToolArguments(t *testing.T) {
	arguments := `{"content":"` + strings.Repeat("x", 2000) + `"}`
	messages := []model.Message{{
		Role: "assistant", ToolCalls: []model.ToolCall{{
			ID: "write", Function: model.FunctionCall{Name: "write_file", Arguments: arguments},
		}},
	}}
	got := CompactModelMessages(messages, EfficientContextOptions{
		RecentMessages: 2, OldToolArgumentBytes: 100, KeepReasoningRecent: 1, MaxBytes: 1 << 20,
	})
	if got[0].ToolCalls[0].Function.Arguments != arguments {
		t.Fatal("recent tool arguments were compacted")
	}
}

func TestCompactModelMessagesForRequest_AccountsForSmallContextWindow(t *testing.T) {
	messages := make([]model.Message, 30)
	for i := range messages {
		messages[i] = model.Message{Role: "tool", Name: "run_shell", Content: strings.Repeat("x", 4000)}
	}
	req := model.ChatRequest{
		Model:     "small",
		MaxTokens: 2048,
		Tools: []map[string]any{{
			"type": "function",
			"function": map[string]any{
				"name":        "large_schema",
				"description": strings.Repeat("schema ", 500),
			},
		}},
	}
	got := CompactModelMessagesForRequest(messages, req, 8192)
	if size := modelMessagesBytes(got); size > 16_384 {
		t.Fatalf("projected messages = %d bytes, want <= 16384", size)
	}
	if got[len(got)-1].Content != messages[len(messages)-1].Content {
		t.Fatal("immediate result tail should remain exact")
	}
}

func TestCompactModelMessagesForRequest_CollapsesPrefixAndKeepsToolPair(t *testing.T) {
	messages := []model.Message{{Role: "system", Content: "protected instructions"}}
	for i := 0; i < 100; i++ {
		messages = append(messages, model.Message{Role: "user", Content: strings.Repeat("old request ", 100)})
	}
	messages = append(messages,
		model.Message{Role: "assistant", ToolCalls: []model.ToolCall{{
			ID: "tail-call", Function: model.FunctionCall{Name: "read_file", Arguments: `{"path":"large.go"}`},
		}}},
		model.Message{Role: "tool", Name: "read_file", ToolCallID: "tail-call", Content: strings.Repeat("result ", 1000)},
		model.Message{Role: "user", Content: "finish this"},
	)

	got := CompactModelMessagesForRequest(messages, model.ChatRequest{MaxTokens: 2048}, 8192)
	if len(got) >= len(messages) {
		t.Fatalf("historical prefix was not collapsed: %d messages", len(got))
	}
	if got[0].Content != "protected instructions" {
		t.Fatal("system instructions were not preserved")
	}
	var assistantFound bool
	for _, msg := range got {
		if assistantHasToolCall(msg, "tail-call") {
			assistantFound = true
		}
	}
	if !assistantFound {
		t.Fatal("assistant side of retained tool result pair was dropped")
	}
	if got[len(got)-1].Content != "finish this" {
		t.Fatal("latest user steering was not preserved")
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
		RecentMessages: 20, OldToolBytes: 200, OldToolArgumentBytes: 200, OldAssistantBytes: 200,
		KeepReasoningRecent: 2, MaxBytes: 70_000,
	})
	if size := modelMessagesBytes(got); size > 70_000 {
		t.Fatalf("compacted size = %d, want <= 70000", size)
	}
	if got[len(got)-1].Content != messages[len(messages)-1].Content {
		t.Fatal("immediate tail should remain exact")
	}
}
