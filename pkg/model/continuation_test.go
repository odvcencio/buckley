package model

import (
	"encoding/json"
	"testing"
)

func TestProviderContinuation_CompatibleRequiresProviderAndModel(t *testing.T) {
	continuation := &ProviderContinuation{
		ProviderID: "openai",
		ModelID:    "openai/gpt-5.4",
		State:      json.RawMessage(`{"items":[{"type":"reasoning","encrypted_content":"opaque"}]}`),
	}

	if !continuation.Compatible("openai", "openai/gpt-5.4") {
		t.Fatal("matching provider and model should be compatible")
	}
	if continuation.Compatible("openrouter", "openai/gpt-5.4") {
		t.Fatal("provider failover must invalidate continuation state")
	}
	if continuation.Compatible("openai", "openai/gpt-5.4-mini") {
		t.Fatal("model failover must invalidate continuation state")
	}
}

func TestProviderContinuation_CloneDoesNotAliasOpaqueState(t *testing.T) {
	continuation := &ProviderContinuation{
		ProviderID: "openai",
		ModelID:    "openai/gpt-5.4",
		State:      json.RawMessage(`{"response_id":"resp_123"}`),
	}

	cloned := continuation.Clone()
	cloned.State[0] = '['

	if string(continuation.State) != `{"response_id":"resp_123"}` {
		t.Fatalf("clone mutated original state: %s", continuation.State)
	}
}

func TestProviderContinuation_JSONRoundTripPreservesOpaqueState(t *testing.T) {
	original := ProviderContinuation{
		ProviderID: "openai",
		ModelID:    "openai/gpt-5.4",
		State:      json.RawMessage(`{"items":[{"type":"compaction","encrypted_content":"ciphertext","future_field":{"x":1}}]}`),
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal continuation: %v", err)
	}
	var decoded ProviderContinuation
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal continuation: %v", err)
	}

	if !decoded.Compatible(original.ProviderID, original.ModelID) {
		t.Fatalf("decoded scope = %s/%s", decoded.ProviderID, decoded.ModelID)
	}
	if string(decoded.State) != string(original.State) {
		t.Fatalf("opaque state changed:\n got: %s\nwant: %s", decoded.State, original.State)
	}
}

func TestContinuationCursor_SendsOnlyMessagesAfterCommittedAssistant(t *testing.T) {
	cursor := &ContinuationCursor{}
	initial := ChatRequest{
		Model: "openai/gpt-5.4",
		Messages: []Message{
			{Role: "system", Content: "system"},
			{Role: "user", Content: "inspect the repository"},
		},
	}
	first, err := cursor.Prepare(initial)
	if err != nil {
		t.Fatalf("Prepare(first) error = %v", err)
	}
	if first.Continuation != nil || len(first.Request.Messages) != 2 {
		t.Fatalf("first request = %#v", first)
	}

	assistantToolCall := Message{
		Role: "assistant",
		ToolCalls: []ToolCall{{
			ID:   "call_1",
			Type: "function",
			Function: FunctionCall{
				Name:      "read_file",
				Arguments: `{"path":"go.mod"}`,
			},
		}},
	}
	continuation := &ProviderContinuation{
		ProviderID: "openai",
		ModelID:    "openai/gpt-5.4",
		State:      json.RawMessage(`{"version":1,"items":[]}`),
	}
	if err := cursor.Commit(initial, &ContinuationResponse{
		Response:     &ChatResponse{Choices: []Choice{{Message: assistantToolCall}}},
		Continuation: continuation,
	}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	next := initial
	next.Messages = append(append([]Message(nil), initial.Messages...),
		assistantToolCall,
		Message{
			Role:       "tool",
			ToolCallID: "call_1",
			Name:       "read_file",
			Content:    "module m31labs.dev/buckley/v2",
		},
	)
	incremental, err := cursor.Prepare(next)
	if err != nil {
		t.Fatalf("Prepare(next) error = %v", err)
	}
	if incremental.Continuation == continuation {
		t.Fatal("Prepare() returned aliased continuation state")
	}
	if len(incremental.Request.Messages) != 1 ||
		incremental.Request.Messages[0].Role != "tool" {
		t.Fatalf("incremental messages = %#v", incremental.Request.Messages)
	}
}

func TestContinuationCursor_ResetsWhenCompactionChangesRepresentedPrefix(t *testing.T) {
	cursor := &ContinuationCursor{}
	initial := ChatRequest{
		Model: "openai/gpt-5.4",
		Messages: []Message{
			{Role: "system", Content: "system"},
			{Role: "user", Content: "large original input"},
		},
	}
	if err := cursor.Commit(initial, &ContinuationResponse{
		Response: &ChatResponse{Choices: []Choice{{Message: Message{
			Role:    "assistant",
			Content: "working",
		}}}},
		Continuation: &ProviderContinuation{
			ProviderID: "openai",
			ModelID:    "openai/gpt-5.4",
			State:      json.RawMessage(`{"version":1,"items":[]}`),
		},
	}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	compacted := ChatRequest{
		Model: "openai/gpt-5.4",
		Messages: []Message{
			{Role: "system", Content: "summary of prior work"},
			{Role: "user", Content: "continue"},
		},
	}
	prepared, err := cursor.Prepare(compacted)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if prepared.Continuation != nil {
		t.Fatal("changed prefix should invalidate continuation")
	}
	if len(prepared.Request.Messages) != len(compacted.Messages) {
		t.Fatalf("messages = %d, want full request of %d", len(prepared.Request.Messages), len(compacted.Messages))
	}
}

func TestContinuationCursor_ResetsBeforeSlicingWhenModelChanges(t *testing.T) {
	cursor := &ContinuationCursor{}
	initial := ChatRequest{
		Model:    "openai/gpt-5.4",
		Messages: []Message{{Role: "user", Content: "start"}},
	}
	if err := cursor.Commit(initial, &ContinuationResponse{
		Response: &ChatResponse{Choices: []Choice{{Message: Message{
			Role:    "assistant",
			Content: "first response",
		}}}},
		Continuation: &ProviderContinuation{
			ProviderID: "openai",
			ModelID:    "openai/gpt-5.4",
			State:      json.RawMessage(`{"version":1,"items":[]}`),
		},
	}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	changed := ChatRequest{
		Model: "openai/gpt-5.4-mini",
		Messages: []Message{
			{Role: "user", Content: "start"},
			{Role: "assistant", Content: "first response"},
			{Role: "user", Content: "continue"},
		},
	}
	prepared, err := cursor.Prepare(changed)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if prepared.Continuation != nil || len(prepared.Request.Messages) != len(changed.Messages) {
		t.Fatalf("model change should produce full request, got %#v", prepared)
	}
}

func TestContinuationCursor_MatchesReasoningOnlyPortableHistory(t *testing.T) {
	cursor := &ContinuationCursor{}
	initial := ChatRequest{
		Model:    "openai/gpt-5.4",
		Messages: []Message{{Role: "user", Content: "think"}},
	}
	response := &ContinuationResponse{
		Response: &ChatResponse{Choices: []Choice{{Message: Message{
			Role:      "assistant",
			Content:   "",
			Reasoning: "summary",
		}}}},
		Continuation: &ProviderContinuation{
			ProviderID: "openai",
			ModelID:    "openai/gpt-5.4",
			State:      json.RawMessage(`{"version":1,"items":[]}`),
		},
	}
	if err := cursor.Commit(initial, response); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	next := initial
	next.Messages = append(append([]Message(nil), initial.Messages...),
		Message{Role: "assistant", Content: "summary", Reasoning: "summary"},
		Message{Role: "user", Content: "continue"},
	)
	prepared, err := cursor.Prepare(next)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if prepared.Continuation == nil || len(prepared.Request.Messages) != 1 {
		t.Fatalf("reasoning-only history should retain continuation, got %#v", prepared)
	}
}
