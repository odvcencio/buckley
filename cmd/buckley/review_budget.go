package main

import "strings"

// countChangedLines counts added and removed lines in a unified diff, not the
// file headers. Review diffs are already prioritized, so generated, minified
// and binary files appear as summary lines and add nothing here.
func countChangedLines(diff string) int {
	n := 0
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++ "), strings.HasPrefix(line, "--- "):
		case strings.HasPrefix(line, "+"), strings.HasPrefix(line, "-"):
			n++
		}
	}
	return n
}

// scaledReviewBudgetUSD is base + perKloc * (changedLines / 1000), clamped to
// ceiling when ceiling > 0. With no base cap the review stays uncapped, and a
// base above the ceiling is kept: scaling never lowers a cap the user set.
func scaledReviewBudgetUSD(base, perKloc, ceiling float64, changedLines int) float64 {
	if base <= 0 || perKloc <= 0 || changedLines <= 0 {
		return base
	}
	scaled := base + perKloc*float64(changedLines)/1000
	if ceiling > 0 && scaled > ceiling {
		scaled = max(ceiling, base)
	}
	return scaled
}

// withSizeScaledBudget raises the configured per-review cap with the size of
// the prioritized diff. --budget and --no-budget are explicit choices and are
// never rescaled.
func (opts automatedReviewOptions) withSizeScaledBudget(changedLines int) automatedReviewOptions {
	if opts.costOverridden {
		return opts
	}
	scaled := scaledReviewBudgetUSD(opts.maxCostUSD, opts.budgetPerKlocUSD, opts.maxBudgetUSD, changedLines)
	if scaled == opts.maxCostUSD {
		return opts
	}
	opts.maxCostUSD = scaled
	if opts.approvalCritic {
		opts.criticReserveUSD = scaled * 0.12
	}
	return opts
}
