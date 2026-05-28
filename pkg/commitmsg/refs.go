// Package commitmsg holds small, dependency-free helpers shared by every
// commit-message rendering path (the oneshot commit tool, the oneshot
// command definition, and the orchestrator commit generator).
//
// Its reason for existing is a single class of bug: an LLM that sees a
// parenthesized issue reference in a diff (for example "(roadmap: #14)")
// populates the model's "related issues" field with that number, and the
// renderers historically emitted it as "Closes #14" — a GitHub
// auto-close directive. On merge to the default branch, GitHub silently
// closed the referenced issue or PR. Related is not the same as closed:
// these helpers render issue links in a reference-only form that GitHub
// still backlinks but never auto-closes, and neutralize any close
// directive that slips into free-text bullets.
package commitmsg

import "regexp"

// closeDirective matches a GitHub issue-closing keyword that is immediately
// followed by an issue reference (optional colon, then required whitespace,
// then "#<number>"). The keyword set mirrors the keywords GitHub honors:
// close, closes, closed, fix, fixes, fixed, resolve, resolves, resolved.
//
// The trailing "#<number>" is REQUIRED so ordinary prose is left alone:
// "fixes a memory leak" has no "#123" after it and does not match, while
// "fixes #123" does.
var closeDirective = regexp.MustCompile(`(?i)\b(?:clos(?:e|es|ed)|fix(?:es|ed)?|resolve(?:s|d)?)\b:?\s+#(\d+)`)

// NeutralizeCloseDirectives rewrites any GitHub close directive embedded in
// s into a reference-only form. "Closes #14" becomes "Refs #14",
// "fixes #9" becomes "Refs #9". The issue linkage is preserved (GitHub
// still records the cross-reference) but the auto-close semantics are
// removed. Text without a "keyword + #number" pair is returned unchanged.
func NeutralizeCloseDirectives(s string) string {
	return closeDirective.ReplaceAllString(s, "Refs #$1")
}

// IssueRefLine renders a single related-issue footer line in the
// reference-only form. The issue argument is the bare number, with or
// without a leading "#". It never emits a close directive.
func IssueRefLine(issue string) string {
	n := issue
	for len(n) > 0 && n[0] == '#' {
		n = n[1:]
	}
	return "Refs #" + n
}
