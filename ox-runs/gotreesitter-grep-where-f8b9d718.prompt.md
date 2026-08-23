You are producing one bounded candidate patch for the MIT-licensed open-source repository github.com/odvcencio/gotreesitter.

Exact base commit: f8b9d718ee19f65598e274035f5481a899ab2b72

This work supplements an ongoing parser-performance campaign. Do not touch parser, compact-parser, normalization, cgo_harness, documentation, generated files, dependencies, or public APIs. Change only:

- grep/where.go
- grep/where_test.go

Confirmed defect: splitClauses currently converts every newline to a semicolon and then splits on every semicolon. As a result, valid quoted arguments such as contains($TEXT, "a;b") and matches($TEXT, "first;second") are broken into invalid clauses. Newlines inside quoted arguments have the same problem.

Acceptance criteria:

1. Semicolons and newlines delimit clauses only when outside single- or double-quoted arguments.
2. A backslash-escaped quote inside its matching quoted argument does not end the quote.
3. Existing multiple-clause AND behavior and the CompileWhere public API remain unchanged.
4. Add focused, table-driven tests covering quoted semicolons, quoted newlines, both quote styles, escaped quotes, and ordinary multi-clause splitting.
5. Keep the implementation small and idiomatic. Do not add a general parser or dependencies.
6. Return only a git-compatible unified diff against the exact base. Do not use Markdown fences and do not explain the patch.

Current grep/where.go:

package grep

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/odvcencio/gotreesitter"
)

// WhereFilter is a predicate that tests whether a match result satisfies a
// where-clause constraint. It returns true if the result passes the filter.
type WhereFilter func(result *Result, source []byte, lang *gotreesitter.Language) bool

// CompileWhere compiles a where-clause string into a WhereFilter function.
//
// Supported constraint forms:
//
//	contains($CAP, <text>)          — capture text contains literal text
//	not contains($CAP, <text>)      — capture text does NOT contain literal text
//	matches($CAP, "regex")          — capture text matches regex
//	not matches($CAP, "regex")      — capture text does NOT match regex
//
// Multiple constraints can be combined with semicolons or newlines; all must
// pass (logical AND).
func CompileWhere(where string) (WhereFilter, error) {
	where = strings.TrimSpace(where)
	if where == "" {
		return func(*Result, []byte, *gotreesitter.Language) bool { return true }, nil
	}

	clauses := splitClauses(where)

	var filters []WhereFilter
	for _, clause := range clauses {
		clause = strings.TrimSpace(clause)
		if clause == "" {
			continue
		}
		f, err := compileClause(clause)
		if err != nil {
			return nil, fmt.Errorf("where clause %q: %w", clause, err)
		}
		filters = append(filters, f)
	}

	if len(filters) == 0 {
		return func(*Result, []byte, *gotreesitter.Language) bool { return true }, nil
	}

	return func(result *Result, source []byte, lang *gotreesitter.Language) bool {
		for _, f := range filters {
			if !f(result, source, lang) {
				return false
			}
		}
		return true
	}, nil
}

// splitClauses splits a where string on semicolons and newlines.
func splitClauses(s string) []string {
	// Replace newlines with semicolons, then split.
	s = strings.ReplaceAll(s, "\n", ";")
	return strings.Split(s, ";")
}

// compileClause compiles a single where constraint.
func compileClause(clause string) (WhereFilter, error) {
	clause = strings.TrimSpace(clause)
	negated := false
	if strings.HasPrefix(clause, "not ") {
		negated = true
		clause = strings.TrimSpace(clause[4:])
	}
	if strings.HasPrefix(clause, "contains(") {
		return compileContains(clause, negated)
	}
	if strings.HasPrefix(clause, "matches(") {
		return compileMatches(clause, negated)
	}
	return nil, fmt.Errorf("unsupported constraint: %q", clause)
}

func compileContains(clause string, negated bool) (WhereFilter, error) {
	capName, arg, err := parseTwoArgFunc(clause, "contains")
	if err != nil {
		return nil, err
	}
	return func(result *Result, source []byte, lang *gotreesitter.Language) bool {
		text := captureText(result, capName, source)
		found := strings.Contains(text, arg)
		if negated {
			return !found
		}
		return found
	}, nil
}

func compileMatches(clause string, negated bool) (WhereFilter, error) {
	capName, pattern, err := parseTwoArgFunc(clause, "matches")
	if err != nil {
		return nil, err
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex %q: %w", pattern, err)
	}
	return func(result *Result, source []byte, lang *gotreesitter.Language) bool {
		text := captureText(result, capName, source)
		matched := re.MatchString(text)
		if negated {
			return !matched
		}
		return matched
	}, nil
}

func parseTwoArgFunc(clause, funcName string) (capName, arg string, err error) {
	inner := strings.TrimPrefix(clause, funcName+"(")
	if !strings.HasSuffix(inner, ")") {
		return "", "", fmt.Errorf("expected closing ')' in %s call", funcName)
	}
	inner = inner[:len(inner)-1]
	commaIdx := strings.Index(inner, ",")
	if commaIdx < 0 {
		return "", "", fmt.Errorf("%s requires two arguments: %s($CAP, value)", funcName, funcName)
	}
	capRef := strings.TrimSpace(inner[:commaIdx])
	arg = strings.TrimSpace(inner[commaIdx+1:])
	if strings.HasPrefix(capRef, "$") {
		capName = capRef[1:]
	} else {
		capName = capRef
	}
	if capName == "" {
		return "", "", fmt.Errorf("%s: empty capture name", funcName)
	}
	arg = stripQuotes(arg)
	return capName, arg, nil
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

func captureText(result *Result, capName string, source []byte) string {
	cap, ok := result.Captures[capName]
	if !ok {
		return ""
	}
	if len(cap.Text) > 0 {
		return string(cap.Text)
	}
	if int(cap.EndByte) <= len(source) {
		return string(source[cap.StartByte:cap.EndByte])
	}
	return ""
}

Relevant existing test conventions in grep/where_test.go:

func TestCompileWhere_MultipleClauses(t *testing.T) {
	filter, err := CompileWhere(`contains($NAME, "Func"); not contains($NAME, "Test")`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	r := &Result{Captures: map[string]Capture{"NAME": {Name: "NAME", Text: []byte("myFunc")}}}
	if !filter(r, nil, nil) {
		t.Error("expected filter to match myFunc")
	}
}

The test file already imports only testing and has TestCompileWhere_Contains, TestCompileWhere_Matches, TestCompileWhere_MultipleClauses, and error/integration tests. Add your focused test near TestCompileWhere_MultipleClauses without rewriting unrelated tests.
