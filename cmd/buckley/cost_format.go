package main

import (
	"fmt"

	"m31labs.dev/buckley/pkg/transparency"
)

func formatTraceCostLine(trace *transparency.Trace, summary transparency.CostSummary) string {
	if trace == nil {
		return ""
	}
	return fmt.Sprintf("%s · %s", formatTraceCost(trace), formatSessionCost(summary))
}

func formatTraceCost(trace *transparency.Trace) string {
	if trace.CostUnknown {
		if trace.Cost > 0 {
			return fmt.Sprintf("Cost: unknown ($%.4f known subtotal)", trace.Cost)
		}
		return "Cost: unknown"
	}
	return fmt.Sprintf("Cost: $%.4f", trace.Cost)
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
	line := fmt.Sprintf("Tokens used: %d · %s", trace.Tokens.Total(), formatTraceCost(trace))
	if !trace.CostUnknown {
		line += " (still charged)"
	}
	return line
}
