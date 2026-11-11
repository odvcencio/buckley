package main

import (
	"strings"
	"testing"
)

func TestPrintCommitMessage_FollowsStandardOutput(t *testing.T) {
	previousIn, previousOut := stdinIsTerminalFn, stdoutIsTerminalFn
	t.Cleanup(func() { stdinIsTerminalFn, stdoutIsTerminalFn = previousIn, previousOut })
	message := "fix(parser): guard the nil map\n\n- Return early when the map is nil.\n"

	// A person at a terminal redirects the message into a file.
	stdinIsTerminalFn = func() bool { return true }
	stdoutIsTerminalFn = func() bool { return false }
	if out := captureStdout(t, func() { printCommitMessage(message) }); out != message+"\n" {
		t.Errorf("redirected output = %q, want the raw message", out)
	}

	// An agent with a pipe on standard input and a terminal on standard output.
	stdinIsTerminalFn = func() bool { return false }
	stdoutIsTerminalFn = func() bool { return true }
	var raw string
	styled := captureTermOut(t, func() { raw = captureStdout(t, func() { printCommitMessage(message) }) })
	if !strings.Contains(styled, "COMMIT") || !strings.Contains(styled, "guard the nil map") {
		t.Errorf("terminal output = %q, want the styled block", styled)
	}
	if raw != "" {
		t.Errorf("terminal run also wrote the raw message: %q", raw)
	}
}
