Produce one incremental candidate patch for the MIT-licensed github.com/odvcencio/gotreesitter repository at exact clean commit 8f6f5c1d3c9d8d03c8704fbf6cd9d202f7400053 on draft branch codex/ox-alpha-supplement-20260823.

This is a reviewer-directed repair of your existing grep where-clause work. Change only grep/where.go and grep/where_test.go. Do not touch parser, performance, normalization, generated, docs, dependencies, or APIs outside the private helpers in grep/where.go.

Focused tests currently pass, but independent review issued NO-GO for these concrete regressions:

1. splitClauses treats any apostrophe or quote anywhere outside quote mode as opening a quoted region. Unquoted arguments remain supported. For example, `contains($NAME, don't); contains($NAME, stop)` must still split into two ANDed filters and match suitable text. The current code swallows the separator after the apostrophe in don't.
2. Unterminated quoted input containing a separator, such as `contains($NAME, "a;b)`, now compiles instead of returning an error. It must fail compilation.
3. The current test claiming to cover an even run of backslashes before a quote actually places the slashes before `b`. Replace it with a real case where two backslashes directly precede a closing quote/delimiter boundary.

Preserve all currently green behavior: separators inside genuinely quoted second arguments; single and double quote forms; actual newlines inside quoted arguments; odd/even backslash quote escaping; matching-quote unescaping; unrelated backslashes such as `\d+`; and ordinary semicolon/newline AND behavior.

Choose a small lexical state sufficient to distinguish a genuinely quoted second argument from apostrophes in an unquoted argument. It is acceptable for private splitClauses to return an error and for CompileWhere to wrap it. Add focused public CompileWhere regression tests.

Current production helpers at exact commit:

func splitClauses(s string) []string {
	var clauses []string
	var cur strings.Builder
	var quote byte
	var bs int
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			cur.WriteByte(c)
			if c == '\\' {
				bs++
				continue
			}
			if c == quote && bs%2 == 0 {
				quote = 0
			}
			bs = 0
		case c == '"' || c == '\'':
			quote = c
			bs = 0
			cur.WriteByte(c)
		case c == ';' || c == '\n':
			clauses = append(clauses, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	clauses = append(clauses, cur.String())
	return clauses
}

func stripQuotes(s string) string {
	if len(s) >= 2 {
		q := s[0]
		if (q == '"' || q == '\'') && s[len(s)-1] == q {
			s = s[1 : len(s)-1]
			var out strings.Builder
			for i := 0; i < len(s); i++ {
				if s[i] == '\\' && i+1 < len(s) && s[i+1] == q {
					out.WriteByte(q)
					i++
					continue
				}
				out.WriteByte(s[i])
			}
			return out.String()
		}
	}
	return s
}

Return only a git-compatible unified diff against exact commit 8f6f5c1d3c9d8d03c8704fbf6cd9d202f7400053. Use accurate hunk context and line counts. No Markdown fences or explanation.
