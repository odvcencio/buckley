package conversation

import (
	"strings"
	"testing"

	"m31labs.dev/buckley/v2/pkg/model"
)

func TestProjectModelMessagesForRequest_PreservesHistoryInLongWindow(t *testing.T) {
	messages := make([]model.Message, 80)
	for i := range messages {
		messages[i] = model.Message{
			Role:    "assistant",
			Content: strings.Repeat("long-context evidence ", 220),
		}
	}

	projected, stats := ProjectModelMessagesForRequest(messages, model.ChatRequest{Model: "long"}, 1_000_000, 1)
	if stats.Compacted {
		t.Fatalf("unexpected compaction: %+v", stats)
	}
	if len(projected) != len(messages) {
		t.Fatalf("projected messages = %d, want %d", len(projected), len(messages))
	}
	if projected[0].Content != messages[0].Content || projected[len(projected)-1].Content != messages[len(messages)-1].Content {
		t.Fatal("long-window projection changed exact history")
	}
	projected[0].Content = "mutated"
	if messages[0].Content == "mutated" {
		t.Fatal("projection aliases durable message structs")
	}
}

func TestProjectModelMessagesForRequest_CompactsNearWindowLimit(t *testing.T) {
	messages := make([]model.Message, 60)
	for i := range messages {
		messages[i] = model.Message{
			Role:    "tool",
			Name:    "read_file",
			Content: strings.Repeat("large tool evidence ", 500),
		}
	}

	projected, stats := ProjectModelMessagesForRequest(messages, model.ChatRequest{MaxTokens: 2048}, 16_384, 1)
	if !stats.Compacted {
		t.Fatalf("expected compaction: %+v", stats)
	}
	if stats.ProjectedTokens >= stats.OriginalTokens {
		t.Fatalf("projected tokens = %d, original = %d", stats.ProjectedTokens, stats.OriginalTokens)
	}
	if projected[len(projected)-1].Content != messages[len(messages)-1].Content {
		t.Fatal("immediate evidence tail should stay exact")
	}
}

func TestProjectModelMessagesForRequest_EmergencyScaleTightensProjection(t *testing.T) {
	messages := make([]model.Message, 70)
	for i := range messages {
		messages[i] = model.Message{
			Role:    "assistant",
			Content: strings.Repeat("working history ", 300),
		}
	}
	req := model.ChatRequest{Model: "provider"}

	_, normal := ProjectModelMessagesForRequest(messages, req, 256*1024, 1)
	_, emergency := ProjectModelMessagesForRequest(messages, req, 256*1024, 0.4)
	if !emergency.Emergency {
		t.Fatal("emergency projection was not marked")
	}
	if emergency.BudgetTokens >= normal.BudgetTokens {
		t.Fatalf("emergency budget = %d, normal = %d", emergency.BudgetTokens, normal.BudgetTokens)
	}
	if emergency.ProjectedTokens > normal.ProjectedTokens {
		t.Fatalf("emergency tokens = %d, normal = %d", emergency.ProjectedTokens, normal.ProjectedTokens)
	}
}

func TestProjectModelMessagesForRequest_UnknownWindowKeepsStableFallback(t *testing.T) {
	messages := make([]model.Message, 40)
	for i := range messages {
		messages[i] = model.Message{Role: "tool", Content: strings.Repeat("x", 10_000)}
	}

	_, stats := ProjectModelMessagesForRequest(messages, model.ChatRequest{}, 0, 1)
	if stats.ContextWindow != 0 || stats.BudgetTokens != DefaultEfficientContextOptions().MaxBytes/4 {
		t.Fatalf("unexpected fallback stats: %+v", stats)
	}
	if !stats.Compacted {
		t.Fatal("unknown-window fallback should retain bounded behavior")
	}
}
