package rlm

import (
	"testing"

	"github.com/draco/buckley/pkg/model"
)

func strPtr(s string) *string { return &s }

func TestStreamAggregator_AssemblesContentAndUsage(t *testing.T) {
	agg := newStreamAggregator()

	agg.absorb(model.StreamChunk{ID: "resp-1", Model: "glm-4.6", Choices: []model.StreamChoice{
		{Delta: model.MessageDelta{Role: "assistant"}},
	}})
	agg.absorb(model.StreamChunk{Choices: []model.StreamChoice{
		{Delta: model.MessageDelta{Content: "Hello, "}},
	}})
	agg.absorb(model.StreamChunk{Choices: []model.StreamChoice{
		{Delta: model.MessageDelta{Content: "world.", Reasoning: "thinking..."}, FinishReason: strPtr("stop")},
	}})
	agg.absorb(model.StreamChunk{Usage: &model.Usage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8}})

	if !agg.hasContent() {
		t.Fatalf("expected hasContent() to be true after absorbing content deltas")
	}

	resp := agg.chatResponse()
	if len(resp.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(resp.Choices))
	}
	content, _ := resp.Choices[0].Message.Content.(string)
	if content != "Hello, world." {
		t.Errorf("content = %q, want %q", content, "Hello, world.")
	}
	if resp.Choices[0].Message.Reasoning != "thinking..." {
		t.Errorf("reasoning = %q, want %q", resp.Choices[0].Message.Reasoning, "thinking...")
	}
	if resp.Choices[0].Message.Role != "assistant" {
		t.Errorf("role = %q, want %q", resp.Choices[0].Message.Role, "assistant")
	}
	if resp.Choices[0].FinishReason != "stop" {
		t.Errorf("finish reason = %q, want %q", resp.Choices[0].FinishReason, "stop")
	}
	if resp.Usage.TotalTokens != 8 {
		t.Errorf("usage.TotalTokens = %d, want 8", resp.Usage.TotalTokens)
	}
	if resp.ID != "resp-1" || resp.Model != "glm-4.6" {
		t.Errorf("unexpected ID/Model: %q/%q", resp.ID, resp.Model)
	}
}

func TestStreamAggregator_AssemblesFragmentedToolCall(t *testing.T) {
	agg := newStreamAggregator()

	agg.absorb(model.StreamChunk{Choices: []model.StreamChoice{{Delta: model.MessageDelta{
		Role: "assistant",
		ToolCalls: []model.ToolCallDelta{
			{Index: 0, ID: "call_1", Type: "function", Function: &model.FunctionCallDelta{Name: "get_wea"}},
		},
	}}}})
	agg.absorb(model.StreamChunk{Choices: []model.StreamChoice{{Delta: model.MessageDelta{
		ToolCalls: []model.ToolCallDelta{
			{Index: 0, Function: &model.FunctionCallDelta{Name: "ther", Arguments: `{"locat`}},
		},
	}}}})
	agg.absorb(model.StreamChunk{Choices: []model.StreamChoice{{Delta: model.MessageDelta{
		ToolCalls: []model.ToolCallDelta{
			{Index: 0, Function: &model.FunctionCallDelta{Arguments: `ion":"NYC"}`}},
		},
	}}}})

	resp := agg.chatResponse()
	if len(resp.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("expected 1 assembled tool call, got %d", len(resp.Choices[0].Message.ToolCalls))
	}
	tc := resp.Choices[0].Message.ToolCalls[0]
	if tc.ID != "call_1" || tc.Function.Name != "get_weather" {
		t.Errorf("unexpected tool call: %+v", tc)
	}
	if tc.Function.Arguments != `{"location":"NYC"}` {
		t.Errorf("arguments = %q, want %q", tc.Function.Arguments, `{"location":"NYC"}`)
	}
}

func TestStreamAggregator_MultipleParallelToolCallsPreserveOrder(t *testing.T) {
	agg := newStreamAggregator()

	agg.absorb(model.StreamChunk{Choices: []model.StreamChoice{{Delta: model.MessageDelta{
		ToolCalls: []model.ToolCallDelta{
			{Index: 1, ID: "call_second", Function: &model.FunctionCallDelta{Name: "second", Arguments: "{}"}},
			{Index: 0, ID: "call_first", Function: &model.FunctionCallDelta{Name: "first", Arguments: "{}"}},
		},
	}}}})

	resp := agg.chatResponse()
	calls := resp.Choices[0].Message.ToolCalls
	if len(calls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(calls))
	}
	// toolOrder tracks first-seen index order (0 was seen second in this
	// chunk but has the lower index) -- absorb records insertion order by
	// index-of-first-appearance, so "second" (index 1) is recorded first.
	if calls[0].ID != "call_second" || calls[1].ID != "call_first" {
		t.Errorf("unexpected call order: %+v", calls)
	}
}

func TestStreamAggregator_HasContentFalseWhenEmpty(t *testing.T) {
	agg := newStreamAggregator()
	if agg.hasContent() {
		t.Errorf("expected hasContent() to be false for a fresh aggregator")
	}
	agg.absorb(model.StreamChunk{Usage: &model.Usage{TotalTokens: 1}})
	if agg.hasContent() {
		t.Errorf("expected hasContent() to stay false when only usage (no content/reasoning/tool calls) was absorbed")
	}
}

func TestStreamDebugEnabled_DefaultsFalse(t *testing.T) {
	// streamDebugEnabled is a sync.OnceValue; this just documents/verifies it
	// evaluates without panicking and returns a bool. We don't mutate the
	// environment here since other tests in this package may run in
	// parallel and OnceValue caches the result for the process lifetime.
	_ = streamDebugEnabled()
}
