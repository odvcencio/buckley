package main

import "strings"

// reviewStopKind names why a review ended before it produced a validated
// result. The kind appears in the salvage banner and selects the exit status.
type reviewStopKind string

const (
	reviewStopBudget     reviewStopKind = "budget"
	reviewStopTimeout    reviewStopKind = "timeout"
	reviewStopTurnLimit  reviewStopKind = "turn limit"
	reviewStopToolLimit  reviewStopKind = "tool-call limit"
	reviewStopProvider   reviewStopKind = "provider error"
	reviewStopValidation reviewStopKind = "validation"
	reviewStopOther      reviewStopKind = "unknown"
)

// Exit statuses for a salvaged (incomplete) review. 0 stays reserved for a
// completed review, 1 for an unclassified failure and 2 for usage errors.
const (
	exitReviewBudget     = 10
	exitReviewTimeout    = 11
	exitReviewTurnLimit  = 12
	exitReviewToolLimit  = 13
	exitReviewProvider   = 14
	exitReviewValidation = 15
)

// classifyReviewStop maps the text of the error that ended a review to a stop
// kind and exit status. The checks run from the most specific wording to the
// most general; text that matches nothing is "unknown" with status 1.
func classifyReviewStop(reason string) (reviewStopKind, int) {
	r := strings.ToLower(reason)
	has := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(r, s) {
				return true
			}
		}
		return false
	}
	switch {
	case has("cost budget", "budget exhausted", "spent,"):
		return reviewStopBudget, exitReviewBudget
	case has("deadline exceeded", "timed out", "timeout"):
		return reviewStopTimeout, exitReviewTimeout
	case has("maximum of", "iteration limit", "turn limit", "max iterations") && has("iteration", "turn"):
		return reviewStopTurnLimit, exitReviewTurnLimit
	case has("tool call limit", "tool-call limit", "tool calls limit", "too many tool calls"):
		return reviewStopToolLimit, exitReviewToolLimit
	case has("validation failed"):
		return reviewStopValidation, exitReviewValidation
	case has("provider", "rate limit", "service unavailable", "no response from model", "overloaded", " 429", " 502", " 503", " 504"):
		return reviewStopProvider, exitReviewProvider
	}
	return reviewStopOther, 1
}
