package conversation

import (
	"fmt"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
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

func TestProjectModelMessagesForRequest_PacksCloseToAvailableBudget(t *testing.T) {
	messages := make([]model.Message, 72)
	for i := range messages {
		messages[i] = model.Message{
			Role:    "assistant",
			Content: strings.Repeat("durable implementation evidence ", 180),
		}
	}
	req := model.ChatRequest{Model: "provider", MaxTokens: 2048}
	originalEstimate := model.EstimateRequestTokens(model.ChatRequest{Model: req.Model, MaxTokens: req.MaxTokens, Messages: messages})
	_, requestBudget := projectionTokenBudget(req, originalEstimate, 16_384, 1)

	_, stats := ProjectModelMessagesForRequest(messages, req, 16_384, 1)
	if stats.ProjectedTokens > requestBudget {
		t.Fatalf("projected tokens = %d, request budget = %d", stats.ProjectedTokens, requestBudget)
	}
	if stats.ProjectedTokens < requestBudget*9/10 {
		t.Fatalf("projected tokens = %d, want at least 90%% of request budget %d", stats.ProjectedTokens, requestBudget)
	}
}

func TestProjectModelMessagesForRequestPinned_RepresentedPrefixPassesThroughUntouched(t *testing.T) {
	messages := make([]model.Message, 60)
	for i := range messages {
		messages[i] = model.Message{
			Role:      "assistant",
			Content:   strings.Repeat("large tool evidence ", 500),
			Reasoning: "reasoning that must not be stripped once pinned",
		}
	}
	pinnedFromIndex := 40

	projected, stats := ProjectModelMessagesForRequestPinned(messages, model.ChatRequest{MaxTokens: 2048}, 16_384, 1, pinnedFromIndex)
	if !stats.ContinuationActive {
		t.Fatal("expected ContinuationActive to be reported")
	}
	// The represented prefix must survive byte-for-byte and by absolute
	// index: the continuation cursor fingerprints messages[0:pin], so no
	// message in that range may move, shrink, or lose reasoning.
	if len(projected) < pinnedFromIndex {
		t.Fatalf("projected messages = %d, want at least the represented prefix of %d", len(projected), pinnedFromIndex)
	}
	for i := 0; i < pinnedFromIndex; i++ {
		if projected[i].Content != messages[i].Content || projected[i].Reasoning != messages[i].Reasoning {
			t.Fatalf("represented message %d was modified: got %#v, want %#v", i, projected[i], messages[i])
		}
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

// TestProjectionTokenBudget_OverheadMatchesNilMessagesProbe proves the
// formula projectionTokenBudget uses -- 1 + originalEstimate.Tools +
// originalEstimate.Fixed -- is equivalent to the overhead a Messages=nil
// probe through EstimateRequestTokens would produce, across representative
// message counts. A nil Messages slice always estimates to 1 token (the
// "messages":null JSON envelope), and Tools/Fixed never depend on Messages,
// so the two must agree for any message count.
func TestProjectionTokenBudget_OverheadMatchesNilMessagesProbe(t *testing.T) {
	for _, count := range []int{0, 1, 7, 500} {
		t.Run(fmt.Sprintf("messages=%d", count), func(t *testing.T) {
			messages := make([]model.Message, count)
			for i := range messages {
				messages[i] = model.Message{Role: "user", Content: strings.Repeat("word ", 20)}
			}
			req := model.ChatRequest{Model: "openai/gpt-5.4", Messages: messages, MaxTokens: 4096}

			originalEstimate := model.EstimateRequestTokens(req)
			formulaOverhead := 1 + originalEstimate.Tools + originalEstimate.Fixed

			probe := req
			probe.Messages = nil
			probeOverhead := model.EstimateRequestTokens(probe).Total

			if formulaOverhead != probeOverhead {
				t.Fatalf("formula overhead = %d, nil-messages probe = %d", formulaOverhead, probeOverhead)
			}
		})
	}
}
