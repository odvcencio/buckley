package main

import (
	"fmt"
	"strings"

	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/oneshot/commands"
)

// mostCompleteIncompleteReview reuses retained attempt traces to keep a failed
// repair from replacing a more complete draft. It selects within the failed
// phase so a primary approval cannot displace an independent critic's report.
func mostCompleteIncompleteReview(current string, run *oneshot.RunResult) string {
	if run.Trace == nil {
		return current
	}
	phase := "primary"
	if run.CriticAttempts > 0 {
		phase = "approval critic"
	}
	bestSections, bestFindings := -1, -1
	bestReview, bestAttempt := "", 0
	for _, attempt := range run.Trace.Attempts {
		if attempt.Trace == nil || !strings.EqualFold(strings.TrimSpace(attempt.Phase), phase) {
			continue
		}
		value, err := (commands.ReviewBranchDef{}).ParseResult(attempt.Trace.Content)
		if err != nil {
			continue
		}
		review := value.(*commands.ReviewAgentResult)
		parsed := review.Parsed
		sections := 0
		for _, section := range []string{string(parsed.Grade), parsed.Summary, parsed.BuildStatus, parsed.TestStatus, parsed.Coverage, parsed.InvariantAudit, parsed.Falsification, parsed.Verdict} {
			if strings.TrimSpace(section) != "" {
				sections++
			}
		}
		findings := len(parsed.Findings)
		if sections < bestSections || (sections == bestSections && findings < bestFindings) {
			continue
		}
		// Equal candidates prefer the later attempt, including corrections that
		// keep the same amount of review material.
		bestSections, bestFindings = sections, findings
		bestReview, bestAttempt = review.Review, attempt.Attempt
	}
	if bestReview == "" || bestReview == strings.TrimSpace(current) {
		return current
	}
	return fmt.Sprintf(
		"> Preserved %s attempt %d from the failed review. This draft remains unvalidated.\n\n%s",
		reviewAttemptPhase(phase), bestAttempt, bestReview,
	)
}
