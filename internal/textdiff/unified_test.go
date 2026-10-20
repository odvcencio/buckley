package textdiff

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pmezard/go-difflib/difflib"
)

func TestUnified_ChangeWindowPreservesRanges(t *testing.T) {
	var source strings.Builder
	for i := range 24 {
		fmt.Fprintf(&source, "line-%02d\n", i)
	}
	old := source.String()
	for _, tc := range []struct{ name, path, old, next string }{
		{"first", "f", old, strings.Replace(old, "line-00", "changed", 1)},
		{"middle", "f", old, strings.Replace(old, "line-12", "changed", 1)},
		{"last", "f", old, strings.Replace(old, "line-23", "changed", 1)},
		{"prepend", "f", old, "inserted\n" + old},
		{"append", "f", old, old + "inserted\n"},
		{"delete prefix", "f", old, strings.TrimPrefix(old, "line-00\nline-01\n")},
		{"delete suffix", "f", old, strings.TrimSuffix(old, "line-22\nline-23\n")},
		{"insert middle", "f", old, strings.Replace(old, "line-12\n", "line-12\ninserted\n", 1)},
		{"nearby edits", "f", old, strings.NewReplacer("line-10", "changed-a", "line-15", "changed-b").Replace(old)},
		{"separate hunks", "f", old, strings.NewReplacer("line-10", "changed-a", "line-20", "changed-b").Replace(old)},
		{"final newline", "f", old, strings.TrimSuffix(old, "\n")},
		{"unterminated context", "f", old + "tail", strings.Replace(old, "line-23", "changed", 1) + "tail"},
		{"no path", "", old, strings.Replace(old, "line-12", "changed", 1)},
		{"create", "f", "", "new"},
		{"delete", "f", "old", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
				A: unifiedDiffLines(tc.old), B: unifiedDiffLines(tc.next), FromFile: tc.path, ToFile: tc.path, Context: 3,
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := Unified(tc.path, tc.old, tc.next); got != want {
				t.Fatalf("diff changed ranges or context:\ngot:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

func TestUnified_RepeatedContextStaysSmall(t *testing.T) {
	for _, eol := range []string{"\n", "\r\n"} {
		old := strings.Repeat("same"+eol, 500)
		next := strings.Repeat("same"+eol, 250) + "changed" + eol + strings.Repeat("same"+eol, 249)
		diff := Unified("f", old, next)
		if len(diff) > 256 || !strings.Contains(diff, "@@ -248,7 +248,7 @@\n") || !strings.Contains(diff, "+changed"+eol) {
			t.Fatalf("small repeated-context edit produced an unexpected diff (%d bytes):\n%s", len(diff), diff)
		}
	}
}
