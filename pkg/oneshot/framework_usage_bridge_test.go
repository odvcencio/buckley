package oneshot

import (
	"context"
	"testing"

	"m31labs.dev/buckley/pkg/transparency"
)

func TestMinimalAgentResultTracePrefersRichUsageOverLegacyCounters(t *testing.T) {
	rich := transparency.TokenUsage{
		Input:                10,
		Output:               5,
		ReportedTotal:        20,
		Unclassified:         5,
		UsageEvidencePresent: true,
	}
	trace := minimalAgentResultTrace(&AgentResult{
		Response:     "valid",
		TokensUsed:   999,
		InputTokens:  111,
		OutputTokens: 222,
		Usage:        rich,
	}, "primary", 1)
	if trace == nil {
		t.Fatal("trace = nil")
	}
	assertRichUsage(t, trace.Tokens, rich)
}

func TestRunAgentNilTraceRichUsageRetainedAndCloned(t *testing.T) {
	reportedReasoning := 4
	wantReportedReasoning := reportedReasoning
	rich := transparency.TokenUsage{
		ReportedTotal:        42,
		Unclassified:         42,
		UsageEvidencePresent: true,
		ReportedReasoning:    &reportedReasoning,
	}
	source := &AgentResult{Response: "valid", Usage: rich}
	framework := NewFramework(nil, nil).WithAgentRunner(&retainedIdentityAgentExecutor{results: []*AgentResult{source}})

	result, err := framework.RunAgent(context.Background(), validatingAgentDefinition{}, AgentRunOpts{
		UserPrompt: "review",
		MaxRetries: 1,
	})
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if result == nil || result.Trace == nil {
		t.Fatalf("result = %+v, want trace", result)
	}
	assertRichUsage(t, result.Trace.Tokens, rich)
	*source.Usage.ReportedReasoning = 0
	if result.Trace.Tokens.ReportedReasoning == nil || *result.Trace.Tokens.ReportedReasoning != wantReportedReasoning {
		t.Fatalf("trace reported reasoning = %v, want cloned value %d", result.Trace.Tokens.ReportedReasoning, wantReportedReasoning)
	}
}

func TestMinimalAgentResultTraceLegacyScalarFallbackStillWorks(t *testing.T) {
	trace := minimalAgentResultTrace(&AgentResult{TokensUsed: 42}, "primary", 1)
	if trace == nil {
		t.Fatal("trace = nil, want total-only usage evidence retained")
	}
	if trace.Tokens != (transparency.TokenUsage{Unclassified: 42}) {
		t.Fatalf("trace tokens = %+v, want legacy total-only as unclassified", trace.Tokens)
	}
}

func TestMinimalAgentResultTraceLegacySplitDoesNotInventRemainder(t *testing.T) {
	trace := minimalAgentResultTrace(&AgentResult{TokensUsed: 37, InputTokens: 10, OutputTokens: 20}, "primary", 1)
	if trace == nil {
		t.Fatal("trace = nil")
	}
	if trace.Tokens != (transparency.TokenUsage{Input: 10, Output: 20}) {
		t.Fatalf("trace tokens = %+v, want framework legacy split without TokensUsed remainder", trace.Tokens)
	}
}
