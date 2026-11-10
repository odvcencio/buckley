package main

import (
	"os"

	"golang.org/x/term"
)

func stdinIsTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

var stdinIsTerminalFn = stdinIsTerminal

// stdoutIsTerminalFn reports whether standard output is a terminal. When it
// is a file or a pipe, a command writes its result there and nothing else.
// Tests replace it.
var stdoutIsTerminalFn = func() bool { return term.IsTerminal(int(os.Stdout.Fd())) }
