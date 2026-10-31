package main

import "flag"

// unreadFlag returns the first argument that fs.Parse(args) left unread but that
// looks like a flag, and the operand before it.
//
// Go's flag package stops reading flags at the first argument that is not a
// flag and hands back everything after it, flags included. A command that takes
// the rest as plain operands drops those flags without a word:
// `buckley pr --yes feature -C /other` would run in the directory it started in.
// Everything after a "--" is an operand by design, so it is not checked.
func unreadFlag(fs *flag.FlagSet, args []string) (flagArg, before string, found bool) {
	operands := fs.Args()
	if used := len(args) - len(operands); used > 0 && args[used-1] == "--" {
		return "", "", false
	}
	for i, arg := range operands {
		if i > 0 && len(arg) > 1 && arg[0] == '-' && arg != "--" {
			return arg, operands[i-1], true
		}
	}
	return "", "", false
}
