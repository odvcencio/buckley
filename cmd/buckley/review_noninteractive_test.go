package main

import (
	"os"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/terminal"
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

// captureStdout swaps os.Stdout, but termOut keeps the writer it was built
// with. Bind termOut to the captured stdout inside the callback, so that a
// header written through termOut lands in the capture (review of #470, finding 2).
func TestPrintReview_OnlyTheReviewWhenOutputIsNotATerminal(t *testing.T) {
	previous, previousOut := reviewOutputIsTerminalFn, termOut
	t.Cleanup(func() { reviewOutputIsTerminalFn, termOut = previous, previousOut })
	reviewOutputIsTerminalFn = func() bool { return false }

	review := "## Grade: B\n\n## Findings\n\nNone."
	out := captureStdout(t, func() {
		termOut = terminal.NewWithOutput(os.Stdout)
		printReview(review)
	})
	if out != review+"\n" {
		t.Fatalf("stdout = %q, want the review and nothing else", out)
	}

	reviewOutputIsTerminalFn = func() bool { return true }
	out = captureStdout(t, func() {
		termOut = terminal.NewWithOutput(os.Stdout)
		printReview(review)
	})
	if !strings.Contains(out, "CODE REVIEW") || !strings.Contains(out, review) {
		t.Fatalf("at a terminal the review keeps its header: %q", out)
	}
}

// At the command level: with standard output redirected, the diagnostics of a
// review go to standard error and standard output stays empty of them.
func TestReviewDiagnosticsGoToStderrWhenOutputIsRedirected(t *testing.T) {
	previous, previousOut := reviewOutputIsTerminalFn, termOut
	t.Cleanup(func() { reviewOutputIsTerminalFn, termOut = previous, previousOut })
	reviewOutputIsTerminalFn = func() bool { return false }

	var stdout string
	stderr := captureStderr(t, func() {
		stdout = captureStdout(t, func() {
			termOut = terminal.NewWithOutput(os.Stdout) // as at start-up
			restore := routeDiagnosticsAwayFromRedirectedOutput()
			termOut.Dim("Using model: example")
			restore()
		})
	})
	if strings.Contains(stdout, "Using model") {
		t.Errorf("a diagnostic reached redirected stdout: %q", stdout)
	}
	if !strings.Contains(stderr, "Using model: example") {
		t.Errorf("stderr lacks the diagnostic: %q", stderr)
	}
}

func TestParseReviewCommandOptions_InteractiveIsStillTheDefault(t *testing.T) {
	opts, err := parseReviewCommandOptions(nil)
	if err != nil || !opts.interactive {
		t.Fatalf("interactive = %v, err = %v; a person at a terminal keeps the menu", opts.interactive, err)
	}
}
