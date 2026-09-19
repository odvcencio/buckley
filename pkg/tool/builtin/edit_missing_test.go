package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditFileTool_RejectionFeedback(t *testing.T) {
	const first = "edit 1: old_string not found in file. Reread the relevant range using read_file with line_numbers:false and copy its decoded content exactly, including whitespace; do not guess indentation or copy line-number prefixes."
	const staged = "edit 2: old_string not found in the staged text after preceding batch edits. The file remains unchanged; reread the original text using read_file with line_numbers:false and account for preceding replacements before retrying."
	const source = "package sample\nfunc run() {\n return 4\n}\n// PRIVATE_SOURCE_CONTENT\n"
	for _, tc := range []struct {
		name, old, want string
		batch           bool
	}{
		{"wrong-indentation", "func run() {\n\treturn 4\n}", first, false},
		{"retained-number-separator", "func run() {\n  return 4\n}", first, false},
		{"serialized-escapes", "func run() {\\n\\treturn 4\\n}", first, false},
		{"number-prefixes", "2: func run() {\n3:  return 4\n4: }", first, false},
		{"no-source-echo", "PRIVATE_SOURCE_CONTENT_NOT_PRESENT", first, false},
		{"removed-by-preceding-edit", "return 4", staged, true},
	} {
		for _, preview := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/preview=%t", tc.name, preview), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "sample.go")
				if err := os.WriteFile(path, []byte(source), 0600); err != nil {
					t.Fatal(err)
				}
				params := map[string]any{"path": path, "old_string": tc.old, "new_string": "replacement"}
				if tc.batch {
					params = map[string]any{"path": path, "edits": []any{
						map[string]any{"old_string": "return 4", "new_string": "return 8"},
						map[string]any{"old_string": tc.old, "new_string": "replacement"},
					}}
				}
				result, err := (&EditFileTool{ShowDiffPreview: preview}).Execute(params)
				if err != nil || result == nil || result.Success || result.NeedsApproval || result.Error != tc.want {
					t.Fatalf("result=%+v err=%v, want %q rejection", result, err, tc.want)
				}
				if strings.Contains(result.Error, "PRIVATE_SOURCE_CONTENT") {
					t.Fatal("error echoed source or supplied text")
				}
				actual, err := os.ReadFile(path)
				if err != nil || string(actual) != source {
					t.Fatalf("rejection changed bytes: %q %v", actual, err)
				}
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatalf("rejection changed mode: %v %v", info, err)
				}
			})
		}
	}
}
