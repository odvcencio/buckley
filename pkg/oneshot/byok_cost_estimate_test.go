package oneshot

import (
	"context"
	"math"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/transparency"
)

// tieredCatalog mirrors the OpenRouter shape that strict pricing admission
// rejects: base prices plus a tiered "overrides" array.
const tieredCatalog = `{"data":[
 {"id":"vendor/tiered","pricing":{"prompt":"0.000002","completion":"0.00001","input_cache_read":"0.0000002","overrides":[{"min_prompt_tokens":272000,"prompt":"0.000004","completion":"0.000015","input_cache_read":"0.0000004"}]}},
 {"id":"vendor/noprice","pricing":{"prompt":null,"completion":null}}
]}`

func approxEqual(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestAgentRunnerInvocationCostEstimatesFromTieredCatalog(t *testing.T) {
	mgr := newAgentRunnerPricingCatalogManager(t, tieredCatalog)
	if info, err := mgr.GetModelInfo("vendor/tiered"); err != nil || info == nil || info.PricingKnown {
		t.Fatalf("precondition: tiered model must have strict PricingKnown=false, got %+v, %v", info, err)
	}

	// Base tier: 100k input (20k cached) + 10k output.
	cached := 20000
	tokens := transparency.TokenUsage{Input: 100000, Output: 10000, ReportedCachedInput: &cached, UsageEvidencePresent: true,
		ProviderCostKnown: true, ProviderCostIsBYOK: true}
	cost, unknown, estimated := agentRunnerInvocationCostDetail(mgr, "openrouter", "vendor/tiered", tokens)
	want := (80000*2.0 + 20000*0.2 + 10000*10.0) / 1e6
	if unknown || !estimated || !approxEqual(cost, want) {
		t.Fatalf("base tier = %v unknown=%v estimated=%v, want %v estimated", cost, unknown, estimated, want)
	}

	// Override tier applies once the prompt reaches min_prompt_tokens.
	cachedBig := 100000
	big := transparency.TokenUsage{Input: 300000, Output: 10000, ReportedCachedInput: &cachedBig, UsageEvidencePresent: true}
	cost, unknown, estimated = agentRunnerInvocationCostDetail(mgr, "openrouter", "vendor/tiered", big)
	want = (200000*4.0 + 100000*0.4 + 10000*15.0) / 1e6
	if unknown || !estimated || !approxEqual(cost, want) {
		t.Fatalf("override tier = %v unknown=%v estimated=%v, want %v estimated", cost, unknown, estimated, want)
	}
}

func TestAgentRunnerInvocationCostPrefersProviderBYOKCost(t *testing.T) {
	mgr := newAgentRunnerPricingCatalogManager(t, tieredCatalog)
	tokens := transparency.TokenUsage{Input: 1000, Output: 1000, UsageEvidencePresent: true,
		ProviderCostKnown: true, ProviderCostIsBYOK: true, ProviderCostUSD: 0.5}
	cost, unknown, estimated := agentRunnerInvocationCostDetail(mgr, "openrouter", "vendor/tiered", tokens)
	if cost != 0.5 || unknown || estimated {
		t.Fatalf("got %v unknown=%v estimated=%v, want provider cost 0.5 known", cost, unknown, estimated)
	}
}

func TestAgentRunnerInvocationCostNoCatalogPriceStaysUnknown(t *testing.T) {
	mgr := newAgentRunnerPricingCatalogManager(t, tieredCatalog)
	tokens := transparency.TokenUsage{Input: 1000, Output: 1000, UsageEvidencePresent: true}
	cost, unknown, estimated := agentRunnerInvocationCostDetail(mgr, "openrouter", "vendor/noprice", tokens)
	if cost != 0 || !unknown || estimated {
		t.Fatalf("got %v unknown=%v estimated=%v, want unknown", cost, unknown, estimated)
	}
	// Unreliable usage is never estimated even with a catalog price.
	bad := transparency.TokenUsage{Input: 1000, Output: 1000, Unclassified: 5}
	if _, unknown, estimated = agentRunnerInvocationCostDetail(mgr, "openrouter", "vendor/tiered", bad); !unknown || estimated {
		t.Fatalf("unclassified usage: unknown=%v estimated=%v, want unknown", unknown, estimated)
	}
}

// pricedAgentExecutor returns scripted results whose traces are priced by the
// real agent-runner cost path, so the framework budget sees estimated costs.
type pricedAgentExecutor struct {
	mgr     *model.Manager
	modelID string
	results []*AgentResult
	calls   int
}

func (e *pricedAgentExecutor) Run(_ context.Context, _ string, _ string, _ []string, _ AgentExecutionOpts) (*AgentResult, error) {
	e.calls++
	result := e.results[0]
	e.results = e.results[1:]
	tokens := agentResultTokenUsage(result)
	cost, unknown, estimated := agentRunnerInvocationCostDetail(e.mgr, "openrouter", e.modelID, tokens)
	trace := transparency.NewTraceBuilder("t", e.modelID, "openrouter").Complete(tokens, cost)
	trace.CostUnknown = unknown
	trace.CostEstimated = estimated
	result.Trace = trace
	return result, nil
}

func TestRunAgentBYOKEstimatedCostAllowsBudgetedRetry(t *testing.T) {
	mgr := newAgentRunnerPricingCatalogManager(t, tieredCatalog)
	// Each attempt: 100k in + 10k out = $0.30 estimated; two attempts stay under $2.
	runner := &pricedAgentExecutor{mgr: mgr, modelID: "vendor/tiered", results: []*AgentResult{
		{Response: "malformed", InputTokens: 100000, OutputTokens: 10000},
		{Response: "valid", InputTokens: 100000, OutputTokens: 10000},
	}}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)
	result, err := framework.RunAgent(context.Background(), validatingAgentDefinition{}, AgentRunOpts{MaxRetries: 2, MaxCostUSD: 2})
	if err != nil {
		t.Fatalf("RunAgent() error = %v, want retry to proceed on estimated cost", err)
	}
	if runner.calls != 2 || result == nil || result.Trace == nil {
		t.Fatalf("calls=%d result=%+v, want two attempts and a trace", runner.calls, result)
	}
	if result.Trace.CostUnknown || !result.Trace.CostEstimated || !approxEqual(result.Trace.Cost, 0.6) {
		t.Fatalf("trace cost=%v unknown=%v estimated=%v, want ~0.60 estimated", result.Trace.Cost, result.Trace.CostUnknown, result.Trace.CostEstimated)
	}
}

func TestRunAgentBYOKEstimatedCostStopsWhenCapExceeded(t *testing.T) {
	mgr := newAgentRunnerPricingCatalogManager(t, tieredCatalog)
	// 1M input at $2/M = $2.00 estimated, at the $1 cap before any retry.
	runner := &pricedAgentExecutor{mgr: mgr, modelID: "vendor/tiered", results: []*AgentResult{
		{Response: "malformed", InputTokens: 250000, OutputTokens: 400000},
		{Response: "valid", InputTokens: 1, OutputTokens: 1},
	}}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)
	_, err := framework.RunAgent(context.Background(), validatingAgentDefinition{}, AgentRunOpts{MaxRetries: 2, MaxCostUSD: 1})
	if err == nil || !strings.Contains(err.Error(), "cost budget exhausted") || strings.Contains(err.Error(), "cost is unknown") {
		t.Fatalf("RunAgent() error = %v, want budget-exhausted error", err)
	}
	if runner.calls != 1 {
		t.Fatalf("calls = %d, want stop after the first attempt", runner.calls)
	}
}

func TestRunAgentBYOKUnknownPriceStillStopsRetry(t *testing.T) {
	mgr := newAgentRunnerPricingCatalogManager(t, tieredCatalog)
	runner := &pricedAgentExecutor{mgr: mgr, modelID: "vendor/noprice", results: []*AgentResult{
		{Response: "malformed", InputTokens: 10, OutputTokens: 10},
		{Response: "valid", InputTokens: 10, OutputTokens: 10},
	}}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)
	_, err := framework.RunAgent(context.Background(), validatingAgentDefinition{}, AgentRunOpts{MaxRetries: 2, MaxCostUSD: 2})
	if err == nil || !strings.Contains(err.Error(), "prior agent attempt cost is unknown") {
		t.Fatalf("RunAgent() error = %v, want unknown-cost stop", err)
	}
}

func TestAgentRunnerInvocationCostBYOKReportedZeroFallsBackToEstimate(t *testing.T) {
	mgr := newAgentRunnerPricingCatalogManager(t, tieredCatalog)
	tokens := transparency.TokenUsage{Input: 100000, Output: 0, UsageEvidencePresent: true,
		ProviderCostKnown: true, ProviderCostIsBYOK: true, ProviderCostUSD: 0}
	cost, unknown, estimated := agentRunnerInvocationCostDetail(mgr, "openrouter", "vendor/tiered", tokens)
	if unknown || !estimated || !approxEqual(cost, 0.2) {
		t.Fatalf("got %v unknown=%v estimated=%v, want $0.20 estimated", cost, unknown, estimated)
	}
}
