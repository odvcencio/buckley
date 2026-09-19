package transparency

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/tools"
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
	builder.WithResponse(&ResponseTrace{FinishReason: "stop"})
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
	if trace.Response == nil || trace.Response.FinishReason != "stop" {
		t.Errorf("unexpected response metadata: %#v", trace.Response)
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

func TestAggregateTraceAttemptsPreservesAttributionAndTotals(t *testing.T) {
	reportedReasoning := 2
	reportedCached := 3
	first := &Trace{
		ID:        "primary-1",
		Timestamp: time.Unix(100, 0),
		Model:     "model",
		Provider:  "provider",
		ModelExecutions: []ExecutionIdentityTrace{{
			RequestedModel: "alias/primary",
			SelectedModel:  "provider/primary",
			ProviderID:     "provider",
			ResponseModel:  "primary-real",
			ResponseID:     "resp-primary",
		}},
		Duration: 2 * time.Second,
		Tokens: TokenUsage{
			Input:               100,
			Output:              20,
			CachedInput:         10,
			ReportedTotal:       120,
			ReportedReasoning:   &reportedReasoning,
			ReportedCachedInput: &reportedCached,
		},
		Cost:    0.10,
		Content: "invalid primary",
	}
	last := &Trace{
		ID:        "critic-1",
		Timestamp: time.Unix(200, 0),
		Model:     "model",
		Provider:  "provider",
		ModelExecutions: []ExecutionIdentityTrace{{
			RequestedModel: "alias/critic",
			SelectedModel:  "provider/critic",
			ProviderID:     "provider",
			ResponseModel:  "critic-real",
			ResponseID:     "resp-critic",
			Conflicted:     true,
		}},
		Duration:    3 * time.Second,
		Tokens:      TokenUsage{Input: 70, Output: 30, Reasoning: 5, Unclassified: 9, ReportedTotal: 109, ReportedCacheWrite: 4, ReportedUsageInconsistent: true, Estimated: true},
		Cost:        0.20,
		CostUnknown: true,
		Content:     "final critic",
	}

	aggregate := AggregateTraceAttempts([]TraceAttempt{
		{Phase: "primary", Attempt: 1, ValidationError: "missing evidence", Trace: first},
		{Phase: "approval critic", Attempt: 1, Trace: last},
	})
	if aggregate == nil {
		t.Fatal("AggregateTraceAttempts() = nil")
	}
	if aggregate.ID != "aggregate:primary-1" {
		t.Fatalf("aggregate.ID = %q", aggregate.ID)
	}
	if aggregate.Timestamp != first.Timestamp || aggregate.Duration != 5*time.Second {
		t.Fatalf("aggregate timing = %v/%v", aggregate.Timestamp, aggregate.Duration)
	}
	if aggregate.Tokens.Input != 170 ||
		aggregate.Tokens.Output != 50 ||
		aggregate.Tokens.Reasoning != 5 ||
		aggregate.Tokens.Unclassified != 9 ||
		aggregate.Tokens.CachedInput != 10 ||
		aggregate.Tokens.ReportedTotal != 229 ||
		aggregate.Tokens.ReportedReasoning == nil ||
		*aggregate.Tokens.ReportedReasoning != 2 ||
		aggregate.Tokens.ReportedCachedInput == nil ||
		*aggregate.Tokens.ReportedCachedInput != 3 ||
		aggregate.Tokens.ReportedCacheWrite != 4 ||
		!aggregate.Tokens.ReportedUsageInconsistent ||
		!aggregate.Tokens.Estimated {
		t.Fatalf("aggregate.Tokens = %#v", aggregate.Tokens)
	}
	if aggregate.Tokens.Total() != 234 {
		t.Fatalf("aggregate.Tokens.Total() = %d, want 234", aggregate.Tokens.Total())
	}
	if math.Abs(aggregate.Cost-0.30) > 1e-12 {
		t.Fatalf("aggregate.Cost = %v", aggregate.Cost)
	}
	if !aggregate.CostUnknown {
		t.Fatalf("aggregate.CostUnknown = false, want unknown propagated")
	}
	if !aggregate.Attempts[1].Trace.CostUnknown {
		t.Fatalf("copied attempt CostUnknown = false, want child marker preserved")
	}
	reportedReasoning = 99
	reportedCached = 99
	if *aggregate.Attempts[0].Trace.Tokens.ReportedReasoning != 2 || *aggregate.Attempts[0].Trace.Tokens.ReportedCachedInput != 3 {
		t.Fatalf("copied attempt tokens aliased source reported details: %+v", aggregate.Attempts[0].Trace.Tokens)
	}
	if aggregate.Content != "final critic" {
		t.Fatalf("aggregate.Content = %q", aggregate.Content)
	}
	if len(aggregate.Attempts) != 2 || aggregate.Attempts[0].Phase != "primary" || aggregate.Attempts[1].Phase != "approval critic" {
		t.Fatalf("aggregate.Attempts = %#v", aggregate.Attempts)
	}
	if aggregate.Attempts[0].ValidationError != "missing evidence" {
		t.Fatalf("validation error = %q, want missing evidence", aggregate.Attempts[0].ValidationError)
	}
	if aggregate.Attempts[0].Trace == first || aggregate.Attempts[1].Trace == last {
		t.Fatal("aggregate retained mutable caller trace pointers")
	}
	if len(aggregate.Attempts[0].Trace.Attempts) != 0 {
		t.Fatal("nested aggregate attempts were not removed")
	}
	if got := aggregate.ModelExecutions; len(got) != 2 || got[0].ResponseID != "resp-primary" || got[1].ResponseID != "resp-critic" || !got[1].Conflicted {
		t.Fatalf("aggregate.ModelExecutions = %#v, want primary then conflicted critic", got)
	}
	first.ModelExecutions[0].ResponseID = "mutated"
	if aggregate.Attempts[0].Trace.ModelExecutions[0].ResponseID != "resp-primary" || aggregate.ModelExecutions[0].ResponseID != "resp-primary" {
		t.Fatalf("aggregate retained mutable model execution slices: %#v / %#v", aggregate.Attempts[0].Trace.ModelExecutions, aggregate.ModelExecutions)
	}
}

func TestTraceModelExecutionsSerializeProviderNeutralIdentity(t *testing.T) {
	trace := NewTraceBuilder("trace-id", "requested/alias", "provider-a").
		WithModelExecutions([]ExecutionIdentityTrace{{
			RequestedModel: "requested/alias",
			SelectedModel:  "provider/model",
			ProviderID:     "provider-a",
			ResponseModel:  "provider-model-2026-09-05",
			ResponseID:     "resp-123",
			Conflicted:     true,
		}}).
		Complete(TokenUsage{Input: 1, Output: 2}, 0)
	encoded, err := json.Marshal(trace)
	if err != nil {
		t.Fatalf("Marshal trace: %v", err)
	}
	raw := string(encoded)
	for _, want := range []string{`"model_executions"`, `"requested_model":"requested/alias"`, `"selected_model":"provider/model"`, `"response_model":"provider-model-2026-09-05"`, `"response_id":"resp-123"`, `"conflicted":true`} {
		if !strings.Contains(raw, want) {
			t.Fatalf("trace JSON missing %s: %s", want, raw)
		}
	}
}

type errTestError struct{}

func (e errTestError) Error() string { return "test error" }
