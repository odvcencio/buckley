package conversation

import "testing"

func TestExtractSignals_PendingWork(t *testing.T) {
	messages := []Message{
		{Role: "user", Content: "fix the bug in main.go"},
		{Role: "assistant", Content: "I'll look at main.go. TODO: also check utils.go"},
		{Role: "user", Content: "next we need to update the tests"},
	}
	signals := ExtractSignals(messages)
	if len(signals.PendingWorkItems) < 2 {
		t.Errorf("expected at least 2 pending work items (TODO + next), got %d", len(signals.PendingWorkItems))
	}
}

func TestExtractSignals_FilePaths(t *testing.T) {
	messages := []Message{
		{Role: "assistant", Content: "I read pkg/rules/engine.go and pkg/config/config.go"},
	}
	signals := ExtractSignals(messages)
	if len(signals.ReferencedFiles) < 2 {
		t.Errorf("expected at least 2 file paths, got %d", len(signals.ReferencedFiles))
	}
}

func TestExtractSignals_ToolTimeline(t *testing.T) {
	messages := []Message{
		{Role: "tool", Name: "read_file"},
		{Role: "tool", Name: "edit_file"},
		{Role: "tool", Name: "bash"},
	}
	signals := ExtractSignals(messages)
	if len(signals.ToolTimeline) != 3 {
		t.Errorf("expected 3 tools in timeline, got %d", len(signals.ToolTimeline))
	}
	if signals.ToolTimeline[0] != "read_file" {
		t.Errorf("first tool = %q, want read_file", signals.ToolTimeline[0])
	}
}

func TestExtractSignals_CurrentWork(t *testing.T) {
	messages := []Message{
		{Role: "user", Content: "fix the bug"},
		{Role: "assistant", Content: "looking at the code"},
		{Role: "tool", Name: "read_file"},
		{Role: "assistant", Content: "found the issue in handler.go"},
	}
	signals := ExtractSignals(messages)
	if signals.CurrentWork != "found the issue in handler.go" {
		t.Errorf("current work = %q, want last assistant message", signals.CurrentWork)
	}
}

func TestExtractSignals_MessageCount(t *testing.T) {
	messages := []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}
	signals := ExtractSignals(messages)
	if signals.MessageCount != 2 {
		t.Errorf("message count = %d, want 2", signals.MessageCount)
	}
	if signals.EstimatedTokens <= 0 {
		t.Error("expected non-zero estimated tokens")
	}
}

func TestExtractSignals_EmptyMessages(t *testing.T) {
	signals := ExtractSignals(nil)
	if signals.MessageCount != 0 {
		t.Errorf("message count = %d, want 0", signals.MessageCount)
	}
	if len(signals.PendingWorkItems) != 0 {
		t.Error("expected no pending work items")
	}
}
