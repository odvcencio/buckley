package tui

import (
	"m31labs.dev/buckley/pkg/diffsignal"
)

// shapeDiff runs a raw git diff through diffsignal.Prioritize with the given
// budget and appends a truncation marker if any content was cut.  This is the
// single path used by /commit and /review so budget policy is kept in one
// place and is unit-testable independently of the TUI controller.
func shapeDiff(raw string, budget int) string {
	if raw == "" {
		return ""
	}
	res := diffsignal.Prioritize(raw, budget)
	out := res.Context
	if res.Truncated {
		out += "\n... (truncated)"
	}
	return out
}
