package agentloop

import (
	"encoding/json"
	"strings"
	"testing"
)

func FuzzCanonicalToolValue(f *testing.F) {
	for _, raw := range []string{
		"", " \t\r\n", "word", "one  two\tthree\n", "success: true\ndata: source",
		"{}", "[]", `{"b":2,"a":1}`, `"a  b"`, `"literal \\n"`,
		"true", "false", "null", "0", "1e2", "1e10000", "-0.01", "9.5", "01", "+1", ".5", "NaN",
		"{broken", "[1] trailing", "tbad", "fbad", "nbad", "-bad",
		"界\u00a0a\u0085b\u2003c\u3000d", "a\u200bb\ufeffc", "a\xff\t\x85b", "\ufeff{}",
		strings.Repeat("x", 16<<10), "a" + strings.Repeat(" ", 16<<10) + "b",
	} {
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		text := strings.TrimSpace(raw)
		for _, empty := range []string{"{}", "<empty>"} {
			want := empty
			if text != "" {
				want = strings.Join(strings.Fields(text), " ")
				if json.Valid([]byte(text)) {
					var value any
					if json.Unmarshal([]byte(text), &value) == nil {
						if encoded, err := json.Marshal(value); err == nil {
							want = string(encoded)
						}
					}
				}
			}
			if got := canonicalToolValue(raw, empty); got != want {
				t.Fatalf("canonicalToolValue(%q, %q) = %q, want %q", raw, empty, got, want)
			}
		}
	})
}
