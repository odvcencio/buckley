package oneshot

import (
	"context"
	"errors"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/transparency"
)

type costUnknownAgentExecutor struct {
	results  []*AgentResult
	errs     []error
	maxCosts []float64
}

func (e *costUnknownAgentExecutor) Run(_ context.Context, _ string, _ string, _ []string, opts AgentExecutionOpts) (*AgentResult, error) {
	e.maxCosts = append(e.maxCosts, opts.MaxCostUSD)
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

func TestRunAgentUnknownCostValidationFailureStopsBudgetedRetry(t *testing.T) {
	identity := model.ExecutionIdentity{ResponseID: "resp-unknown-validation", ResponseModel: "model-a"}
	runner := &costUnknownAgentExecutor{results: []*AgentResult{{
		Response:        "malformed",
		InputTokens:     3,
		OutputTokens:    4,
		ModelExecutions: []model.ExecutionIdentity{identity},
	}}}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)

	result, err := framework.RunAgent(context.Background(), validatingAgentDefinition{}, AgentRunOpts{
		MaxRetries: 2,
		MaxCostUSD: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "prior agent attempt cost is unknown") {
		t.Fatalf("RunAgent() error = %v, want unknown-cost budget stop", err)
	}
	if result == nil || !result.Incomplete || result.Attempts != 1 || result.PrimaryAttempts != 1 {
		t.Fatalf("result = %+v, want one incomplete primary attempt", result)
	}
	if len(runner.maxCosts) != 1 {
		t.Fatalf("agent calls = %d, want 1", len(runner.maxCosts))
	}
	if result.Trace == nil || !result.Trace.CostUnknown {
		t.Fatalf("trace = %+v, want unknown-cost trace retained", result.Trace)
	}
	if got := result.Trace.Tokens; got != (transparency.TokenUsage{Input: 3, Output: 4}) {
		t.Fatalf("trace tokens = %+v, want split usage retained", got)
	}
	if got := result.Trace.ModelExecutions; len(got) != 1 || got[0].ResponseID != identity.ResponseID {
		t.Fatalf("trace identity = %+v, want retained execution identity", got)
	}
	if got := result.Trace.Attempts; len(got) != 1 || !strings.Contains(got[0].ValidationError, "missing coverage evidence") {
		t.Fatalf("trace attempts = %+v, want validation error retained", got)
	}
}

func TestRunAgentUnknownCostApprovalStopsBudgetedCritic(t *testing.T) {
	runner := &costUnknownAgentExecutor{results: []*AgentResult{
		{Response: "approve", InputTokens: 5, OutputTokens: 6},
		{Response: "request-changes", InputTokens: 7, OutputTokens: 8},
	}}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)

	result, err := framework.RunAgent(context.Background(), criticAgentDefinition{}, AgentRunOpts{
		MaxRetries: 1,
		MaxCostUSD: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "before approval critic") || !strings.Contains(err.Error(), "cost is unknown") {
		t.Fatalf("RunAgent() error = %v, want unknown-cost critic stop", err)
	}
	if result == nil || !result.Incomplete || result.Value != "approve" || result.PrimaryAttempts != 1 || result.CriticAttempts != 0 {
		t.Fatalf("result = %+v, want retained primary approval and no critic attempt", result)
	}
	if len(runner.maxCosts) != 1 {
		t.Fatalf("agent calls = %d, want 1", len(runner.maxCosts))
	}
	if result.Trace == nil || !result.Trace.CostUnknown {
		t.Fatalf("trace = %+v, want unknown-cost primary trace retained", result.Trace)
	}
}

func TestRunAgentUnknownCostEmptyEvidenceStopsBudgetedRetryWithoutTrace(t *testing.T) {
	for _, tt := range []struct {
		name  string
		first *AgentResult
	}{
		{name: "nil result", first: nil},
		{name: "empty result", first: &AgentResult{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := &costUnknownAgentExecutor{results: []*AgentResult{
				tt.first,
				{Response: "valid", InputTokens: 1, OutputTokens: 1},
			}}
			framework := NewFramework(nil, nil).WithAgentRunner(runner)

			result, err := framework.RunAgent(context.Background(), validatingAgentDefinition{}, AgentRunOpts{
				MaxRetries: 2,
				MaxCostUSD: 1,
			})
			if err == nil || !strings.Contains(err.Error(), "prior agent attempt cost is unknown") {
				t.Fatalf("RunAgent() error = %v, want unknown-cost budget stop", err)
			}
			if result == nil || !result.Incomplete || result.Attempts != 1 || result.Trace != nil {
				t.Fatalf("result = %+v, want one incomplete attempt without fabricated trace", result)
			}
			if len(runner.maxCosts) != 1 {
				t.Fatalf("agent calls = %d, want 1", len(runner.maxCosts))
			}
		})
	}
}

func TestRunAgentUnknownCostSuccessfulSingleAttemptAccepted(t *testing.T) {
	runner := &costUnknownAgentExecutor{results: []*AgentResult{{
		Response:    "valid",
		InputTokens: 9,
	}}}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)

	result, err := framework.RunAgent(context.Background(), validatingAgentDefinition{}, AgentRunOpts{
		MaxRetries: 2,
		MaxCostUSD: 1,
	})
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if result == nil || result.Incomplete || result.Value != "valid" || result.Attempts != 1 {
		t.Fatalf("result = %+v, want accepted first attempt", result)
	}
	if result.Trace == nil || !result.Trace.CostUnknown {
		t.Fatalf("trace = %+v, want unknown-cost evidence retained", result.Trace)
	}
}

func TestRunAgentNoBudgetStillRetriesAfterUnknownCostAndMarksAggregate(t *testing.T) {
	runner := &costUnknownAgentExecutor{results: []*AgentResult{
		{},
		{Response: "valid", Trace: newTestAgentTrace("known-after-unknown", 10, 2, 0.03)},
	}}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)

	result, err := framework.RunAgent(context.Background(), validatingAgentDefinition{}, AgentRunOpts{
		MaxRetries: 2,
	})
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if result == nil || result.Incomplete || result.Attempts != 2 || result.Value != "valid" {
		t.Fatalf("result = %+v, want successful retry without cost budget", result)
	}
	if result.Trace == nil || !result.Trace.CostUnknown {
		t.Fatalf("trace = %+v, want aggregate marked unknown from no-trace attempt", result.Trace)
	}
	if got := result.Trace.Attempts; len(got) != 1 || got[0].Trace.ID != "known-after-unknown" {
		t.Fatalf("trace attempts = %+v, want only real trace attempt retained", got)
	}
}

func TestRunAgentKnownCostRetryBudgetUnchanged(t *testing.T) {
	runner := &costUnknownAgentExecutor{results: []*AgentResult{
		{Response: "malformed", Trace: newTestAgentTrace("known-1", 10, 1, 0.10)},
		{Response: "valid", Trace: newTestAgentTrace("known-2", 20, 2, 0.02)},
	}}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)

	result, err := framework.RunAgent(context.Background(), validatingAgentDefinition{}, AgentRunOpts{
		MaxRetries: 2,
		MaxCostUSD: 0.50,
	})
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if result == nil || result.Incomplete || result.Attempts != 2 || result.Trace == nil {
		t.Fatalf("result = %+v, want successful known-cost retry", result)
	}
	assertFloatSliceNear(t, runner.maxCosts, []float64{0.50, 0.40})
	if result.Trace.CostUnknown {
		t.Fatalf("trace CostUnknown = true, want false for known-cost traces")
	}
	if result.Trace.Cost < 0.119999 || result.Trace.Cost > 0.120001 {
		t.Fatalf("trace cost = %v, want 0.12", result.Trace.Cost)
	}
}

func TestRunAgentExecutionErrorDoesNotValidateAndRetainsUnknownCostCause(t *testing.T) {
	runErr := context.DeadlineExceeded
	def := &countingValidationAgentDefinition{}
	runner := &costUnknownAgentExecutor{
		results: []*AgentResult{{Response: "valid", InputTokens: 1, OutputTokens: 2}},
		errs:    []error{runErr},
	}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)

	result, err := framework.RunAgent(context.Background(), def, AgentRunOpts{
		MaxRetries: 2,
		MaxCostUSD: 1,
	})
	if !errors.Is(err, runErr) {
		t.Fatalf("RunAgent() error = %v, want deadline cause", err)
	}
	if def.validationCalls != 0 {
		t.Fatalf("ValidateResult calls = %d, want 0 on runErr", def.validationCalls)
	}
	if result == nil || !result.Incomplete || result.Value != "valid" {
		t.Fatalf("result = %+v, want retained partial value", result)
	}
	if result.Trace == nil || !result.Trace.CostUnknown || result.Trace.Error == "" {
		t.Fatalf("trace = %+v, want unknown-cost error trace", result.Trace)
	}
}

type countingValidationAgentDefinition struct {
	validatingAgentDefinition
	validationCalls int
}

func (d *countingValidationAgentDefinition) ValidateResult(result any) error {
	d.validationCalls++
	return d.validatingAgentDefinition.ValidateResult(result)
}
