package tui

import (
	"m31labs.dev/buckley/pkg/diffsignal"
)

// shapeDiff runs a captured git diff through diffsignal.Prioritize with the given
// budget and appends a truncation marker if any content was cut.  This is the
// single path used by /commit and /review so budget policy is kept in one
// place and is unit-testable independently of the TUI controller.
func shapeDiff(diff *diffsignal.Diff, budget int) string {
	const marker = "\n... (truncated)"
	res := diff.Prioritize(max(1, budget-len(marker)), diffsignal.Options{})
	out := res.Context
	if res.Truncated {
		out += marker
		if budget > 0 && len(out) > budget {
			out = out[:budget]
		}
	}
	return out
}
