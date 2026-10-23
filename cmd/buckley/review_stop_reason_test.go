package main

import (
	"errors"
	"strings"
	"testing"
)

func TestClassifyReviewStop(t *testing.T) {
	cases := []struct {
		reason   string
		wantKind reviewStopKind
		wantExit int
	}{
		{"review cost budget exhausted before model call: $0.7501 spent, $0.7500 limit", reviewStopBudget, 10},
		{"review cost budget exhausted before approval critic", reviewStopBudget, 10},
		{"execute task: context deadline exceeded", reviewStopTimeout, 11},
		{"batched verification timed out after 15m0s", reviewStopTimeout, 11},
		{"agent reached the maximum of 25 iterations", reviewStopTurnLimit, 12},
		{"tool call limit of 40 reached", reviewStopToolLimit, 13},
		{"execute task: provider returned 503 service unavailable", reviewStopProvider, 14},
		{"no response from model", reviewStopProvider, 14},
		{`primary review validation failed after 2 attempts for "review-pr": missing evidence`, reviewStopValidation, 15},
		{"something else entirely", reviewStopOther, 1},
		{"", reviewStopOther, 1},
	}
	for _, tc := range cases {
		kind, exit := classifyReviewStop(tc.reason)
		if kind != tc.wantKind || exit != tc.wantExit {
			t.Errorf("classifyReviewStop(%q) = %q, %d; want %q, %d", tc.reason, kind, exit, tc.wantKind, tc.wantExit)
		}
	}
}

func TestMarkIncompleteReviewNamesStopReason(t *testing.T) {
	out := markIncompleteReview("body", "review cost budget exhausted before model call: $0.80 spent, $0.75 limit")
	for _, want := range []string{"Salvaged", "Stop reason: budget", "$0.80 spent, $0.75 limit"} {
		if !strings.Contains(strings.ToLower(out), strings.ToLower(want)) {
			t.Errorf("banner missing %q:\n%s", want, out)
		}
	}
	empty := markIncompleteReview("body", "")
	if !strings.Contains(empty, "Stop reason: unknown") {
		t.Errorf("empty reason must say unknown:\n%s", empty)
	}
}

func TestSalvageIncompleteReviewExitStatusFollowsStopReason(t *testing.T) {
	result := &reviewCommandResult{
		incomplete:    true,
		reviewText:    "partial",
		incompleteWhy: "review cost budget exhausted before model call: $0.80 spent, $0.75 limit",
	}
	err := salvageIncompleteReview("", errors.New("agent primary execution failed"), result)
	if err == nil {
		t.Fatal("salvage must still return an error")
	}
	if got := exitCodeForError(err); got != 10 {
		t.Fatalf("exit code = %d, want 10 for a budget stop", got)
	}
	if !strings.Contains(err.Error(), "budget") {
		t.Fatalf("error should name the stop reason: %v", err)
	}
}
