package main

import (
	"os"

	"golang.org/x/term"
)

func stdinIsTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

var stdinIsTerminalFn = stdinIsTerminal
