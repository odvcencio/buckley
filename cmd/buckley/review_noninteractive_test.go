package main

import (
	"testing"
)

// The session of 2026-11-09 did not use `buckley review` because, without a
// terminal, it needed --no-interactive: the menu that offers to fix findings
// waits on standard input.
func TestReviewMenuWanted(t *testing.T) {
	previousIn, previousOut := stdinIsTerminalFn, reviewOutputIsTerminalFn
	t.Cleanup(func() { stdinIsTerminalFn, reviewOutputIsTerminalFn = previousIn, previousOut })

	cases := []struct {
		name          string
		opts          reviewCommandOptions
		stdin, stdout bool
		want          bool
	}{
		{"a person at a terminal", reviewCommandOptions{interactive: true}, true, true, true},
		{"standard input is a pipe (an agent or a script)", reviewCommandOptions{interactive: true}, false, true, false},
		{"standard output is redirected", reviewCommandOptions{interactive: true}, true, false, false},
		{"neither is a terminal", reviewCommandOptions{interactive: true}, false, false, false},
		{"--no-interactive at a terminal", reviewCommandOptions{interactive: false}, true, true, false},
		{"--output at a terminal", reviewCommandOptions{interactive: true, outputFile: "review.md"}, true, true, false},
	}
	for _, tc := range cases {
		stdinIsTerminalFn = func() bool { return tc.stdin }
		reviewOutputIsTerminalFn = func() bool { return tc.stdout }
		if got := reviewMenuWanted(tc.opts); got != tc.want {
			t.Errorf("%s: reviewMenuWanted() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestPrintReview_OnlyTheReviewWhenOutputIsNotATerminal(t *testing.T) {
	previous := reviewOutputIsTerminalFn
	t.Cleanup(func() { reviewOutputIsTerminalFn = previous })
	reviewOutputIsTerminalFn = func() bool { return false }

	review := "## Grade: B\n\n## Findings\n\nNone."
	if out := captureStdout(t, func() { printReview(review) }); out != review+"\n" {
		t.Fatalf("stdout = %q, want the review and nothing else", out)
	}
}

func TestParseReviewCommandOptions_InteractiveIsStillTheDefault(t *testing.T) {
	opts, err := parseReviewCommandOptions(nil)
	if err != nil || !opts.interactive {
		t.Fatalf("interactive = %v, err = %v; a person at a terminal keeps the menu", opts.interactive, err)
	}
}
