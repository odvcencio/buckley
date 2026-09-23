package main

import (
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/transparency"
)

func TestCostProvenance_Display(t *testing.T) {
	for _, tc := range []struct {
		name    string
		trace   transparency.Trace
		summary transparency.CostSummary
		want    string
		charged bool
	}{
		{"unknown", transparency.Trace{CostUnknown: true}, transparency.CostSummary{SessionCostUnknown: true}, "Cost: unknown · Session known subtotal: $0.0000 + unknown", false},
		{"mixed", transparency.Trace{Cost: 2, CostUnknown: true}, transparency.CostSummary{SessionCost: 5, SessionCostUnknown: true}, "Cost: unknown ($2.0000 known subtotal) · Session known subtotal: $5.0000 + unknown", false},
		{"known-zero", transparency.Trace{}, transparency.CostSummary{}, "Cost: $0.0000 · Session: $0.0000", true},
		{"known", transparency.Trace{Cost: 2}, transparency.CostSummary{SessionCost: 5}, "Cost: $2.0000 · Session: $5.0000", true},
		{
			"known-byok",
			transparency.Trace{Cost: 0.0001601, Tokens: transparency.TokenUsage{ProviderCostKnown: true, ProviderCostIsBYOK: true}},
			transparency.CostSummary{SessionCost: 0.0001601},
			"Cost: $0.0002 (BYOK) · Session: $0.0002",
			true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatTraceCostLine(&tc.trace, tc.summary); got != tc.want {
				t.Fatalf("cost line = %q, want %q", got, tc.want)
			}
			line := formatTraceErrorUsageLine(&tc.trace)
			if !strings.Contains(line, formatTraceCost(&tc.trace)) || strings.Contains(line, "still charged") != tc.charged {
				t.Fatalf("error line = %q", line)
			}
		})
	}
	if formatTraceCostLine(nil, transparency.CostSummary{}) != "" || formatTraceErrorUsageLine(nil) != "" {
		t.Fatal("nil trace should be empty")
	}
}
