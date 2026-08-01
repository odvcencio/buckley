package model

import (
	"encoding/json"
	"fmt"
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

func TestContinuationCursor_ResetsBeforeSlicingWhenProviderChanges(t *testing.T) {
	cursor := &ContinuationCursor{}
	initial := ChatRequest{
		Model:    "gpt-5.4",
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
		Model: "gpt-5.4",
		Messages: []Message{
			{Role: "user", Content: "start"},
			{Role: "assistant", Content: "first response"},
			{Role: "user", Content: "continue"},
		},
	}
	prepared, err := cursor.PrepareForProvider(changed, "openrouter")
	if err != nil {
		t.Fatalf("PrepareForProvider() error = %v", err)
	}
	if prepared.Continuation != nil || len(prepared.Request.Messages) != len(changed.Messages) {
		t.Fatalf("provider change should produce full request, got %#v", prepared)
	}
	if cursor.Active() {
		t.Fatal("provider change should reset the cursor")
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

func TestContinuationCursor_ThirdTurnRemainsHitWithFullRequestCommit(t *testing.T) {
	cursor := &ContinuationCursor{}
	history := []Message{{Role: "user", Content: "turn one"}}

	for turn := 1; turn <= 3; turn++ {
		req := ChatRequest{Model: "openai/gpt-5.4", Messages: append([]Message(nil), history...)}
		prepared, err := cursor.Prepare(req)
		if err != nil {
			t.Fatalf("turn %d prepare: %v", turn, err)
		}
		if turn == 1 && prepared.Continuation != nil {
			t.Fatal("first turn must not be a continuation hit")
		}
		if turn > 1 {
			if prepared.Continuation == nil {
				t.Fatalf("turn %d lost the continuation window", turn)
			}
			if len(prepared.Request.Messages) != 1 {
				t.Fatalf("turn %d sent %d messages, want 1 (incremental suffix)", turn, len(prepared.Request.Messages))
			}
		}
		assistant := Message{Role: "assistant", Content: fmt.Sprintf("answer %d", turn)}
		resp := &ContinuationResponse{
			Response: &ChatResponse{Choices: []Choice{{Message: assistant}}},
			Continuation: &ProviderContinuation{
				ProviderID: "openai", ModelID: "openai/gpt-5.4", State: json.RawMessage(`{"v":1}`),
			},
		}
		if err := cursor.Commit(req, resp); err != nil {
			t.Fatalf("turn %d commit: %v", turn, err)
		}
		history = append(history, assistant, Message{Role: "user", Content: fmt.Sprintf("turn %d follow-up", turn+1)})
	}
}

// TestContinuationCursor_IncrementalDigestsMatchFromScratch proves the
// incremental digest cache Commit uses (carrying forward the previously
// verified prefix and hashing only the new suffix + assistant reply) produces
// byte-identical fingerprints to hashing the whole message list from scratch,
// across several turns.
func TestContinuationCursor_IncrementalDigestsMatchFromScratch(t *testing.T) {
	cursor := &ContinuationCursor{}
	history := []Message{{Role: "user", Content: "turn one"}}

	for turn := 1; turn <= 3; turn++ {
		req := ChatRequest{Model: "openai/gpt-5.4", Messages: append([]Message(nil), history...)}
		if _, err := cursor.Prepare(req); err != nil {
			t.Fatalf("turn %d prepare: %v", turn, err)
		}

		assistant := Message{Role: "assistant", Content: fmt.Sprintf("answer %d", turn)}
		resp := &ContinuationResponse{
			Response: &ChatResponse{Choices: []Choice{{Message: assistant}}},
			Continuation: &ProviderContinuation{
				ProviderID: "openai", ModelID: "openai/gpt-5.4", State: json.RawMessage(`{"v":1}`),
			},
		}
		if err := cursor.Commit(req, resp); err != nil {
			t.Fatalf("turn %d commit: %v", turn, err)
		}

		fullMessages := append(append([]Message(nil), req.Messages...), assistant)
		want, err := fingerprintMessages(fullMessages)
		if err != nil {
			t.Fatalf("turn %d from-scratch fingerprint: %v", turn, err)
		}
		if len(cursor.represented) != len(want) {
			t.Fatalf("turn %d represented length = %d, want %d", turn, len(cursor.represented), len(want))
		}
		for i := range want {
			if cursor.represented[i] != want[i] {
				t.Fatalf("turn %d fingerprint %d mismatch: incremental=%x want=%x", turn, i, cursor.represented[i], want[i])
			}
		}

		history = append(history, assistant, Message{Role: "user", Content: fmt.Sprintf("turn %d follow-up", turn+1)})
	}
}

func TestProviderContinuation_CompatibleAcceptsUnprefixedCallerModel(t *testing.T) {
	c := &ProviderContinuation{ProviderID: "openai", ModelID: "openai/gpt-5.4"}
	if !c.Compatible("openai", "gpt-5.4") {
		t.Fatal("canonical persisted model must accept the caller's unprefixed model ID")
	}
	if c.Compatible("openai", "gpt-4o") {
		t.Fatal("different model must stay incompatible")
	}
	if c.Compatible("openrouter", "gpt-5.4") {
		t.Fatal("different provider must stay incompatible")
	}
}
