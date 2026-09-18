package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/terminal"
	"m31labs.dev/buckley/pkg/transparency"
)

func TestFormatTokenUsageLineShowsUnclassifiedTokens(t *testing.T) {
	got := formatTokenUsageLine(transparency.TokenUsage{Unclassified: 42})
	for _, want := range []string{"0 in", "0 out", "42 unclassified", "42 total"} {
		if !strings.Contains(got, want) {
			t.Fatalf("token line = %q, want %q", got, want)
		}
	}
}

func TestFormatTokenUsageLinePreservesLegacyOutput(t *testing.T) {
	got := formatTokenUsageLine(transparency.TokenUsage{Input: 100, Output: 50, Reasoning: 20})
	want := "Tokens: 100 in · 50 out · 20 reasoning = 170 total"
	if got != want {
		t.Fatalf("token line = %q, want legacy line %q", got, want)
	}
}

func TestFormatTokenUsageLineShowsProviderSubsetDetails(t *testing.T) {
	reasoning := 20
	cached := 30
	got := formatTokenUsageLine(transparency.TokenUsage{
		Input:               100,
		Output:              50,
		ReportedTotal:       150,
		ReportedReasoning:   &reasoning,
		ReportedCachedInput: &cached,
	})
	want := "Tokens: 100 in (30 cached) · 50 out (20 reasoning) = 150 total"
	if got != want {
		t.Fatalf("token line = %q, want provider subset details %q", got, want)
	}
}

func TestFormatTokenUsageLineShowsReportedUsageInconsistency(t *testing.T) {
	got := formatTokenUsageLine(transparency.TokenUsage{
		Input:                     100,
		Output:                    50,
		ReportedTotal:             140,
		ReportedUsageInconsistent: true,
	})
	for _, want := range []string{
		"100 in",
		"50 out",
		"150 total",
		"reported usage inconsistent",
		"reported total: 140",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("token line = %q, want %q", got, want)
		}
	}
	if strings.Contains(got, "available reported total") {
		t.Fatalf("token line = %q, must not describe inconsistent usage as merely partial reports", got)
	}
}

func TestFormatTokenUsageLineShowsPartialReportedTotalWithoutInconsistency(t *testing.T) {
	got := formatTokenUsageLine(transparency.TokenUsage{
		Input:         100,
		Output:        50,
		ReportedTotal: 140,
	})
	if !strings.Contains(got, "available reported total: 140") {
		t.Fatalf("token line = %q, want partial reported total label", got)
	}
	if strings.Contains(got, "inconsistent") {
		t.Fatalf("token line = %q, must not infer inconsistency from aggregate totals alone", got)
	}
}

func TestFormatTokenUsageLineShowsNegativeInconsistentReportedTotal(t *testing.T) {
	got := formatTokenUsageLine(transparency.TokenUsage{
		Input:                     100,
		Output:                    50,
		ReportedTotal:             -1,
		ReportedUsageInconsistent: true,
	})
	if !strings.Contains(got, "reported usage inconsistent; reported total: -1") {
		t.Fatalf("token line = %q, want inconsistent negative reported total", got)
	}
}

func TestFormatTokenUsageLineShowsTotalOnlyAndCacheWrite(t *testing.T) {
	got := formatTokenUsageLine(transparency.TokenUsage{
		Unclassified:       42,
		ReportedTotal:      42,
		ReportedCacheWrite: 9,
	})
	for _, want := range []string{
		"42 unclassified (reported total only)",
		"42 total",
		"cache write reported: 9 (not added)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("token line = %q, want %q", got, want)
		}
	}
}

func TestFormatTokenUsageLineLabelsEstimatedCounts(t *testing.T) {
	got := formatTokenUsageLine(transparency.TokenUsage{
		Input:         100,
		Output:        50,
		ReportedTotal: 150,
		Estimated:     true,
	})
	if !strings.HasPrefix(got, "Tokens (estimated):") {
		t.Fatalf("token line = %q, want estimated label", got)
	}
}

func TestFormatTraceCostLineLabelsUnknownCost(t *testing.T) {
	trace := &transparency.Trace{CostUnknown: true}
	summary := transparency.CostSummary{SessionCost: 0.25, SessionCostUnknown: true}
	got := formatTraceCostLine(trace, summary)
	if !strings.Contains(got, "Cost: unknown") || !strings.Contains(got, "$0.2500 + unknown") {
		t.Fatalf("cost line = %q, want unknown cost with known subtotal", got)
	}
	if strings.Contains(got, "$0.0000") {
		t.Fatalf("cost line = %q, must not render unknown trace as free", got)
	}
}

func TestFormatTraceCostLineLabelsMixedKnownAndUnknownCost(t *testing.T) {
	trace := &transparency.Trace{Cost: 0.1234, CostUnknown: true}
	summary := transparency.CostSummary{SessionCost: 0.5, SessionCostUnknown: true}
	got := formatTraceCostLine(trace, summary)
	for _, want := range []string{
		"Cost: unknown ($0.1234 known subtotal)",
		"Session known subtotal: $0.5000 + unknown",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("cost line = %q, want %q", got, want)
		}
	}
}

func TestFormatTraceCostLinePreservesKnownZeroFreeCost(t *testing.T) {
	got := formatTraceCostLine(&transparency.Trace{}, transparency.CostSummary{})
	if got != "Cost: $0.0000 · Session: $0.0000" {
		t.Fatalf("cost line = %q, want known zero/free formatting", got)
	}
}

func TestFormatTraceErrorUsageLineDoesNotAssertStillChargedWhenUnknown(t *testing.T) {
	got := formatTraceErrorUsageLine(&transparency.Trace{
		Tokens:      transparency.TokenUsage{Input: 1, Output: 2, Unclassified: 3},
		CostUnknown: true,
	})
	if got != "Tokens used: 6 · Cost: unknown" {
		t.Fatalf("error usage line = %q, want unknown cost", got)
	}
	if strings.Contains(got, "still charged") {
		t.Fatalf("error usage line = %q, must not assert still charged for unknown cost", got)
	}
}

func TestFormatTraceErrorUsageLineShowsMixedKnownSubtotal(t *testing.T) {
	got := formatTraceErrorUsageLine(&transparency.Trace{
		Tokens:      transparency.TokenUsage{Unclassified: 42},
		Cost:        0.1234,
		CostUnknown: true,
	})
	if got != "Tokens used: 42 · Cost: unknown ($0.1234 known subtotal)" {
		t.Fatalf("error usage line = %q, want mixed unknown cost", got)
	}
	if strings.Contains(got, "still charged") {
		t.Fatalf("error usage line = %q, must not assert still charged for unknown cost", got)
	}
}

func TestFormatTraceErrorUsageLineLabelsEstimatedAndReportedMismatch(t *testing.T) {
	got := formatTraceErrorUsageLine(&transparency.Trace{
		Tokens: transparency.TokenUsage{
			Input:                     100,
			Output:                    50,
			ReportedTotal:             140,
			Estimated:                 true,
			ReportedUsageInconsistent: true,
		},
		Cost: 0.1234,
	})
	for _, want := range []string{
		"Tokens used: 150 (estimated)",
		"reported usage inconsistent",
		"reported total: 140",
		"Cost: $0.1234",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("error usage line = %q, want %q", got, want)
		}
	}
	if strings.Contains(got, "still charged") {
		t.Fatalf("error usage line = %q, must not assert still charged for estimated usage", got)
	}
}

func TestPrintCostLabelsUnknownTraceAndUnclassifiedTokens(t *testing.T) {
	ledger := transparency.NewCostLedger()
	ledger.Record(transparency.CostEntry{
		Tokens:      transparency.TokenUsage{Unclassified: 42},
		Cost:        0.5,
		CostUnknown: true,
	})
	trace := &transparency.Trace{
		Tokens:      transparency.TokenUsage{Unclassified: 42},
		Cost:        0.1234,
		CostUnknown: true,
	}

	got := captureTermOut(t, func() { printCost(trace, ledger) })
	for _, want := range []string{
		"42 unclassified",
		"Cost: unknown ($0.1234 known subtotal)",
		"Session known subtotal: $0.5000 + unknown",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("printCost output = %q, want %q", got, want)
		}
	}
}

func TestPrintCostShowsProviderSubsetAndEstimatedDetails(t *testing.T) {
	reasoning := 20
	cached := 30
	ledger := transparency.NewCostLedger()
	ledger.Record(transparency.CostEntry{
		Tokens: transparency.TokenUsage{
			Input:               100,
			Output:              50,
			ReportedTotal:       150,
			ReportedReasoning:   &reasoning,
			ReportedCachedInput: &cached,
			Estimated:           true,
		},
		Cost: 0.5,
	})
	trace := &transparency.Trace{
		Tokens: transparency.TokenUsage{
			Input:               100,
			Output:              50,
			ReportedTotal:       150,
			ReportedReasoning:   &reasoning,
			ReportedCachedInput: &cached,
			Estimated:           true,
		},
		Cost: 0.1234,
	}

	got := captureTermOut(t, func() { printCost(trace, ledger) })
	for _, want := range []string{
		"Tokens (estimated): 100 in (30 cached) · 50 out (20 reasoning) = 150 total",
		"Cost: $0.1234",
		"Session: $0.5000",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("printCost output = %q, want %q", got, want)
		}
	}
}

func TestPrintReviewCostUsesSessionTokensAndUnknownSubtotal(t *testing.T) {
	ledger := transparency.NewCostLedger()
	ledger.Record(transparency.CostEntry{
		Tokens:      transparency.TokenUsage{Unclassified: 42},
		Cost:        0.5,
		CostUnknown: true,
	})
	trace := &transparency.Trace{Cost: 0.1234, CostUnknown: true}

	got := captureTermOut(t, func() { printReviewCost(trace, ledger) })
	for _, want := range []string{
		"42 unclassified",
		"Cost: unknown ($0.1234 known subtotal)",
		"Session known subtotal: $0.5000 + unknown",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("printReviewCost output = %q, want %q", got, want)
		}
	}
}

func TestPrintReviewCostShowsReportedUsageInconsistency(t *testing.T) {
	ledger := transparency.NewCostLedger()
	ledger.Record(transparency.CostEntry{
		Tokens: transparency.TokenUsage{
			Input:                     100,
			Output:                    50,
			ReportedTotal:             140,
			ReportedUsageInconsistent: true,
		},
		Cost: 0.5,
	})
	trace := &transparency.Trace{Cost: 0.1234}

	got := captureTermOut(t, func() { printReviewCost(trace, ledger) })
	for _, want := range []string{
		"Tokens: 100 in · 50 out = 150 total (reported usage inconsistent; reported total: 140)",
		"Cost: $0.1234",
		"Session: $0.5000",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("printReviewCost output = %q, want %q", got, want)
		}
	}
}

func TestPrintErrorUnknownCostDoesNotSayStillCharged(t *testing.T) {
	trace := &transparency.Trace{
		Tokens:      transparency.TokenUsage{Input: 1, Output: 2, Unclassified: 3},
		Cost:        0.1234,
		CostUnknown: true,
	}

	got := captureTermOut(t, func() { printError(errors.New("boom"), trace) })
	for _, want := range []string{
		"boom",
		"Tokens used: 6",
		"Cost: unknown ($0.1234 known subtotal)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("printError output = %q, want %q", got, want)
		}
	}
	if strings.Contains(got, "still charged") {
		t.Fatalf("printError output = %q, must not assert still charged for unknown cost", got)
	}
}

func captureTermOut(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	original := termOut
	termOut = terminal.NewWithOutput(&buf)
	t.Cleanup(func() { termOut = original })
	fn()
	return buf.String()
}
