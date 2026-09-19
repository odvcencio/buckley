package oneshot

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/transparency"
)

type retainedIdentityAgentExecutor struct {
	results []*AgentResult
	errs    []error
}

func (e *retainedIdentityAgentExecutor) Run(context.Context, string, string, []string, AgentExecutionOpts) (*AgentResult, error) {
	if len(e.results) == 0 {
		return nil, errors.New("unexpected agent execution")
	}
	result := e.results[0]
	e.results = e.results[1:]
	var err error
	if len(e.errs) > 0 {
		err = e.errs[0]
		e.errs = e.errs[1:]
	}
	return result, err
}

func TestRunAgentSynthesizesTraceFromNilTraceAgentResult(t *testing.T) {
	identity := model.ExecutionIdentity{
		RequestedModel: "requested/model",
		SelectedModel:  "selected/model",
		ProviderID:     "provider-a",
		ResponseModel:  "response/model",
		ResponseID:     "resp-success",
		Conflicted:     true,
	}
	source := &AgentResult{
		Response:        "valid",
		FinishReason:    "stop",
		InputTokens:     11,
		OutputTokens:    7,
		Duration:        23 * time.Millisecond,
		ProviderID:      "provider-a",
		ModelExecutions: []model.ExecutionIdentity{identity},
	}
	framework := NewFramework(nil, nil).WithAgentRunner(&retainedIdentityAgentExecutor{results: []*AgentResult{source}})

	result, err := framework.RunAgent(context.Background(), validatingAgentDefinition{}, AgentRunOpts{
		UserPrompt: "review",
		MaxRetries: 1,
	})
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if result == nil || result.Value != "valid" || result.Incomplete {
		t.Fatalf("unexpected result: %+v", result)
	}
	requireSyntheticAgentTrace(t, result.Trace, "valid", transparency.TokenUsage{Input: 11, Output: 7}, identity)
	if result.Trace.Cost != 0 {
		t.Fatalf("synthetic trace cost = %v, want unknown zero value", result.Trace.Cost)
	}
	source.ModelExecutions[0].ResponseID = "mutated"
	if result.Trace.ModelExecutions[0].ResponseID != "resp-success" {
		t.Fatalf("trace model execution aliased caller result: %+v", result.Trace.ModelExecutions)
	}
}

func TestRunAgentSynthesizesTraceForErroredPartialAgentResult(t *testing.T) {
	identity := model.ExecutionIdentity{ResponseID: "resp-partial", ResponseModel: "partial/model"}
	runErr := errors.New("provider partial failure")
	framework := NewFramework(nil, nil).WithAgentRunner(&retainedIdentityAgentExecutor{
		results: []*AgentResult{{
			Response:        "valid",
			FinishReason:    "length",
			InputTokens:     13,
			OutputTokens:    5,
			Duration:        17 * time.Millisecond,
			ProviderID:      "provider-b",
			ModelExecutions: []model.ExecutionIdentity{identity},
		}},
		errs: []error{runErr},
	})

	result, err := framework.RunAgent(context.Background(), validatingAgentDefinition{}, AgentRunOpts{
		UserPrompt: "review",
		MaxRetries: 1,
	})
	if !errors.Is(err, runErr) {
		t.Fatalf("RunAgent() error = %v, want wrapped run error", err)
	}
	if result == nil || !result.Incomplete || result.Value != "valid" {
		t.Fatalf("result = %+v, want salvaged incomplete valid value", result)
	}
	requireSyntheticAgentTrace(t, result.Trace, "valid", transparency.TokenUsage{Input: 13, Output: 5}, identity)
	if result.Trace.Error == "" || !strings.Contains(result.Trace.Error, "provider partial failure") {
		t.Fatalf("trace error = %q, want run error projected", result.Trace.Error)
	}
}

func TestRunAgentNilTraceValidationRetryAndCriticAggregateIdentityInOrder(t *testing.T) {
	ids := []model.ExecutionIdentity{
		{ResponseID: "resp-primary-1", ResponseModel: "model-a"},
		{ResponseID: "resp-primary-2", ResponseModel: "model-a"},
		{ResponseID: "resp-critic-1", ResponseModel: "model-b"},
		{ResponseID: "resp-critic-2", ResponseModel: "model-b"},
	}
	runner := &retainedIdentityAgentExecutor{results: []*AgentResult{
		agentResultWithNilTrace("malformed", 1, 2, 0, "provider-a", ids[0]),
		agentResultWithNilTrace("approve", 0, 0, 7, "provider-a", ids[1]),
		agentResultWithNilTrace("malformed", 5, 6, 0, "provider-b", ids[2]),
		agentResultWithNilTrace("request-changes", 7, 8, 99, "provider-b", ids[3]),
	}}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)

	result, err := framework.RunAgent(context.Background(), criticAgentDefinition{}, AgentRunOpts{
		UserPrompt: "review",
		MaxRetries: 2,
	})
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if result == nil || result.Value != "request-changes" || result.Incomplete {
		t.Fatalf("unexpected result: %+v", result)
	}
	assertReviewAttemptCounts(t, result, 4, 2, 2)
	if result.Trace == nil {
		t.Fatal("result.Trace = nil")
	}
	if got := result.Trace.Tokens; !reflect.DeepEqual(got, transparency.TokenUsage{Input: 13, Output: 16, Unclassified: 7}) {
		t.Fatalf("aggregate tokens = %+v, want split usage plus total-only without double-counting split+total", got)
	}
	if len(result.Trace.Attempts) != 4 || len(result.Trace.ModelExecutions) != 4 {
		t.Fatalf("trace attempts/model executions = %d/%d, want 4/4", len(result.Trace.Attempts), len(result.Trace.ModelExecutions))
	}
	for i, identity := range ids {
		if got := result.Trace.ModelExecutions[i].ResponseID; got != identity.ResponseID {
			t.Fatalf("aggregate identity %d = %q, want %q", i, got, identity.ResponseID)
		}
	}
	if result.Trace.Attempts[0].ValidationError == "" || result.Trace.Attempts[2].ValidationError == "" {
		t.Fatalf("validation retries were not attributed to rejected attempts: %+v", result.Trace.Attempts)
	}
}

func TestRunAgentNilTraceEmptyResultDoesNotFabricateTrace(t *testing.T) {
	framework := NewFramework(nil, nil).WithAgentRunner(&retainedIdentityAgentExecutor{results: []*AgentResult{{}}})

	result, err := framework.RunAgent(context.Background(), validatingAgentDefinition{}, AgentRunOpts{
		UserPrompt: "review",
		MaxRetries: 1,
	})
	if err == nil {
		t.Fatal("RunAgent() succeeded, want validation failure")
	}
	if result == nil || !result.Incomplete || result.Trace != nil {
		t.Fatalf("result = %+v, want incomplete result without fabricated trace", result)
	}
}

func TestRunAgentNilTraceTotalOnlyUsageUsesUnclassified(t *testing.T) {
	identity := model.ExecutionIdentity{ResponseID: "resp-total-only", ResponseModel: "total/model"}
	framework := NewFramework(nil, nil).WithAgentRunner(&retainedIdentityAgentExecutor{results: []*AgentResult{{
		Response:        "valid",
		TokensUsed:      42,
		ModelExecutions: []model.ExecutionIdentity{identity},
	}}})

	result, err := framework.RunAgent(context.Background(), validatingAgentDefinition{}, AgentRunOpts{
		UserPrompt: "review",
		MaxRetries: 1,
	})
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if result == nil || result.Trace == nil {
		t.Fatalf("result/trace = %+v, want retained trace from response and identity", result)
	}
	if result.Trace.Tokens != (transparency.TokenUsage{Unclassified: 42}) {
		t.Fatalf("trace tokens = %+v, want total-only TokensUsed as unclassified", result.Trace.Tokens)
	}
	if result.Trace.Tokens.Total() != 42 {
		t.Fatalf("trace total = %d, want 42", result.Trace.Tokens.Total())
	}
	if got := result.Trace.ModelExecutions; len(got) != 1 || got[0].ResponseID != "resp-total-only" {
		t.Fatalf("trace identity = %+v, want retained total-only execution identity", got)
	}
}

func TestMinimalAgentResultTraceTotalOnlyUsageCreatesTrace(t *testing.T) {
	trace := minimalAgentResultTrace(&AgentResult{TokensUsed: 42}, "primary", 1)
	if trace == nil {
		t.Fatal("trace = nil, want total-only usage evidence retained")
	}
	if trace.Tokens != (transparency.TokenUsage{Unclassified: 42}) {
		t.Fatalf("trace tokens = %+v, want total-only usage as unclassified", trace.Tokens)
	}
	if trace.Tokens.Total() != 42 {
		t.Fatalf("trace total = %d, want 42", trace.Tokens.Total())
	}
	if trace.Content != "" || trace.Provider != "" || len(trace.ModelExecutions) != 0 {
		t.Fatalf("total-only trace invented non-usage evidence: %+v", trace)
	}
}

func TestRunAgentNilTraceProviderOnlyEvidenceCreatesTrace(t *testing.T) {
	trace := minimalAgentResultTrace(&AgentResult{ProviderID: "provider-only"}, "primary", 1)
	if trace == nil {
		t.Fatal("trace = nil, want provider-only evidence retained")
	}
	if trace.Provider != "provider-only" {
		t.Fatalf("trace provider = %q, want provider-only", trace.Provider)
	}
	if trace.Content != "" || trace.Tokens != (transparency.TokenUsage{}) || len(trace.ModelExecutions) != 0 {
		t.Fatalf("provider-only trace invented content/tokens/identity: %+v", trace)
	}
}

func TestRunAgentKeepsExistingTraceSemanticsWhenTraceProvided(t *testing.T) {
	existing := newTestAgentTrace("existing", 21, 9, 0.42)
	existing.ModelExecutions = nil
	framework := NewFramework(nil, nil).WithAgentRunner(&retainedIdentityAgentExecutor{results: []*AgentResult{{
		Response:        "valid",
		Trace:           existing,
		InputTokens:     99,
		OutputTokens:    88,
		ModelExecutions: []model.ExecutionIdentity{{ResponseID: "resp-supplied-but-not-merged"}},
	}}})

	result, err := framework.RunAgent(context.Background(), validatingAgentDefinition{}, AgentRunOpts{
		UserPrompt: "review",
		MaxRetries: 1,
	})
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if result.Trace == nil || result.Trace.Cost != 0.42 || result.Trace.Tokens.Input != 21 || result.Trace.Tokens.Output != 9 {
		t.Fatalf("existing trace semantics changed: %+v", result.Trace)
	}
	if len(result.Trace.ModelExecutions) != 0 {
		t.Fatalf("existing trace unexpectedly merged supplied identity: %+v", result.Trace.ModelExecutions)
	}
}

func requireSyntheticAgentTrace(t *testing.T, trace *transparency.Trace, response string, usage transparency.TokenUsage, identity model.ExecutionIdentity) {
	t.Helper()
	if trace == nil {
		t.Fatal("trace = nil")
	}
	if trace.Content != response {
		t.Fatalf("trace content = %q, want %q", trace.Content, response)
	}
	if !reflect.DeepEqual(trace.Tokens, usage) {
		t.Fatalf("trace tokens = %+v, want %+v", trace.Tokens, usage)
	}
	if trace.Duration <= 0 {
		t.Fatalf("trace duration = %s, want retained positive duration", trace.Duration)
	}
	if len(trace.ModelExecutions) != 1 {
		t.Fatalf("trace model executions = %+v, want one", trace.ModelExecutions)
	}
	got := trace.ModelExecutions[0]
	if got.ResponseID != identity.ResponseID ||
		got.ResponseModel != identity.ResponseModel ||
		got.RequestedModel != identity.RequestedModel ||
		got.SelectedModel != identity.SelectedModel ||
		got.ProviderID != identity.ProviderID ||
		got.Conflicted != identity.Conflicted {
		t.Fatalf("trace identity = %+v, want %+v", got, identity)
	}
}

func agentResultWithNilTrace(response string, input, output, tokensUsed int, provider string, identity model.ExecutionIdentity) *AgentResult {
	return &AgentResult{
		Response:        response,
		FinishReason:    "stop",
		TokensUsed:      tokensUsed,
		InputTokens:     input,
		OutputTokens:    output,
		Duration:        time.Duration(input+output) * time.Millisecond,
		ProviderID:      provider,
		ModelExecutions: []model.ExecutionIdentity{identity},
	}
}
