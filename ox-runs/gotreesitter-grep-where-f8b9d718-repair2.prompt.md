Produce a complete corrected unified diff against exact MIT-licensed github.com/odvcencio/gotreesitter commit f8b9d718ee19f65598e274035f5481a899ab2b72.

Scope remains exactly grep/where.go and grep/where_test.go. This is isolated supplemental work; touch nothing else.

Your latest candidate now applied after mechanical hunk-header normalization, but focused validation failed:

--- FAIL: TestCompileWhere_QuotedSeparators
    where_test.go:214: escaped double quote should not close the argument
    where_test.go:217: escaped single quote should not close the argument
FAIL github.com/odvcencio/gotreesitter/grep

Root cause visible in the exact base: splitClauses needs escape-aware quote tracking, while stripQuotes currently only removes the surrounding delimiters and leaves the backslash before an escaped matching quote in the returned argument. The intended public DSL behavior is that a backslash-escaped matching quote inside a quoted argument represents the literal quote. Preserve unrelated backslashes, especially regex escapes such as `\d`, and handle odd/even preceding backslash runs correctly.

Keep the good parts of your candidate: delimit semicolon/newline only outside matching quotes, cover single and double quotes, cover actual quoted newlines, preserve normal multi-clause AND behavior, and use focused public CompileWhere tests. Fix the semantic failure without introducing a general parser or dependency.

Important patch-format requirement: both files already exist. Use real context from the exact base and accurate hunk line counts. Return only a git-compatible unified diff that passes `git apply --check`; no Markdown or explanation.

Relevant exact-base functions:

func splitClauses(s string) []string {
	s = strings.ReplaceAll(s, "\n", ";")
	return strings.Split(s, ";")
}

func stripQuotes(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') ||
			(s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

The accepted public behaviors must include:

- contains($NAME, "a;b") matches text a;b
- contains($NAME, 'a;b') matches text a;b
- a literal newline inside either quoted argument remains part of the argument
- contains($NAME, "a\";b") matches text a";b
- contains($NAME, 'a\';b') matches text a';b
- regex escapes that do not escape the active quote remain unchanged
- ordinary semicolon and newline separators still create ANDed clauses
