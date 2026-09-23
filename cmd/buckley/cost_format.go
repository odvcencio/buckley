package main

import (
	"fmt"
	"strings"

	"m31labs.dev/buckley/pkg/transparency"
)

func formatTokenUsageLine(tokens transparency.TokenUsage) string {
	parts := []string{formatInputTokens(tokens), formatOutputTokens(tokens)}
	if tokens.Reasoning > 0 {
		parts = append(parts, fmt.Sprintf("%d reasoning", tokens.Reasoning))
	}
	if tokens.Unclassified > 0 {
		part := fmt.Sprintf("%d unclassified", tokens.Unclassified)
		if tokens.ReportedTotal == tokens.Unclassified && tokens.Input == 0 && tokens.Output == 0 && tokens.Reasoning == 0 {
			part += " (reported total only)"
		}
		parts = append(parts, part)
	}

	label := "Tokens"
	if tokens.Estimated {
		label = "Tokens (estimated)"
	}
	total := tokens.Total()
	line := fmt.Sprintf("%s: %s = %d total", label, strings.Join(parts, " · "), total)
	if annotations := formatTokenUsageAnnotations(tokens, total); len(annotations) > 0 {
		line += fmt.Sprintf(" (%s)", strings.Join(annotations, "; "))
	}
	return line
}

func formatInputTokens(tokens transparency.TokenUsage) string {
	part := fmt.Sprintf("%d in", tokens.Input)
	if tokens.ReportedCachedInput != nil {
		part += fmt.Sprintf(" (%d cached)", *tokens.ReportedCachedInput)
	}
	return part
}

func formatOutputTokens(tokens transparency.TokenUsage) string {
	part := fmt.Sprintf("%d out", tokens.Output)
	if tokens.ReportedReasoning != nil {
		part += fmt.Sprintf(" (%d reasoning)", *tokens.ReportedReasoning)
	}
	return part
}

func formatTokenUsageAnnotations(tokens transparency.TokenUsage, total int) []string {
	var annotations []string
	if tokens.ReportedUsageInconsistent {
		annotations = append(annotations, "reported usage inconsistent")
	}
	if tokens.ReportedTotal != 0 && (tokens.ReportedUsageInconsistent || tokens.ReportedTotal != total) {
		if tokens.ReportedUsageInconsistent {
			annotations = append(annotations, fmt.Sprintf("reported total: %d", tokens.ReportedTotal))
		} else {
			annotations = append(annotations, fmt.Sprintf("available reported total: %d", tokens.ReportedTotal))
		}
	}
	if tokens.ReportedCacheWrite != 0 {
		annotations = append(annotations, fmt.Sprintf("cache write reported: %d (not added)", tokens.ReportedCacheWrite))
	}
	return annotations
}

func formatTraceCostLine(trace *transparency.Trace, summary transparency.CostSummary) string {
	if trace == nil {
		return ""
	}
	return fmt.Sprintf("%s · %s", formatTraceCost(trace), formatSessionCost(summary))
}

// traceCostAmount formats trace's known cost figure with any provenance
// tag (currently just BYOK, C7), without the "Cost: " prefix or the
// unknown-cost framing formatTraceCost adds around it. Both the ordinary
// success-path cost line and the error-path "still charged" line build on
// this shared fragment so provenance shows up consistently everywhere a
// known cost is printed.
func traceCostAmount(trace *transparency.Trace) string {
	amount := fmt.Sprintf("$%.4f", trace.Cost)
	if trace.Tokens.ProviderCostKnown && trace.Tokens.ProviderCostIsBYOK {
		// The caller is billed directly by the upstream provider for part
		// of this figure, not only by OpenRouter -- worth naming, since it
		// can otherwise read as a suspiciously small charge for a real call.
		amount += " (BYOK)"
	}
	return amount
}

func formatTraceCost(trace *transparency.Trace) string {
	if trace.CostUnknown {
		if trace.Cost > 0 {
			return fmt.Sprintf("Cost: unknown ($%.4f known subtotal)", trace.Cost)
		}
		return "Cost: unknown"
	}
	return "Cost: " + traceCostAmount(trace)
}

func formatSessionCost(summary transparency.CostSummary) string {
	if summary.SessionCostUnknown {
		return fmt.Sprintf("Session known subtotal: $%.4f + unknown", summary.SessionCost)
	}
	return fmt.Sprintf("Session: $%.4f", summary.SessionCost)
}

func formatTraceErrorUsageLine(trace *transparency.Trace) string {
	if trace == nil {
		return ""
	}
	tokensLabel := fmt.Sprintf("Tokens used: %d", trace.Tokens.Total())
	if trace.Tokens.Estimated {
		tokensLabel += " (estimated)"
	}
	if annotations := formatTokenUsageAnnotations(trace.Tokens, trace.Tokens.Total()); len(annotations) > 0 {
		tokensLabel += fmt.Sprintf(" (%s)", strings.Join(annotations, "; "))
	}
	if trace.CostUnknown {
		return fmt.Sprintf("%s · %s", tokensLabel, formatTraceCost(trace))
	}
	if trace.Tokens.Estimated {
		return fmt.Sprintf("%s · Cost: %s", tokensLabel, traceCostAmount(trace))
	}
	return fmt.Sprintf("%s · Cost: %s (still charged)", tokensLabel, traceCostAmount(trace))
}
