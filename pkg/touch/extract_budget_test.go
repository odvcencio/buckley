package touch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestExtractFromArgs_PreviewFileBudgets(t *testing.T) {
	for _, tc := range []struct {
		name, tool, source string
		args               map[string]any
		options            ExtractOptions
	}{
		{"oversized-source", "write_file", strings.Repeat("a", DefaultPreviewFileBytes+1), map[string]any{"content": "new"}, ExtractOptions{}},
		{"oversized-output", "write_file", "old", map[string]any{"content": strings.Repeat("b", DefaultPreviewFileBytes+1)}, ExtractOptions{}},
		{"oversized-edit", "edit_file", strings.Repeat("a", DefaultPreviewFileBytes), map[string]any{"old_string": "a", "new_string": "bb", "replace_all": true}, ExtractOptions{}},
		{"configured-source-cap", "write_file", "12345", map[string]any{"content": "new"}, ExtractOptions{MaxFileBytes: 4}},
		{"configured-output-cap", "write_file", "old", map[string]any{"content": "12345"}, ExtractOptions{MaxFileBytes: 4}},
		{"skip-file-diff", "write_file", "old", map[string]any{"content": "new"}, ExtractOptions{SkipFileDiff: true}},
		{"batch-does-not-show-partial-top-level-edit", "edit_file", "old", map[string]any{"old_string": "old", "new_string": "wrong", "edits": []any{map[string]any{"old_string": "old", "new_string": "actual"}}}, ExtractOptions{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "note.txt")
			if err := os.WriteFile(path, []byte(tc.source), 0600); err != nil {
				t.Fatal(err)
			}
			tc.args["path"] = path
			rich := ExtractFromArgsWithOptions(tc.tool, tc.args, tc.options)
			if rich.FilePath != path || rich.Description == "" {
				t.Fatalf("lost metadata: %+v", rich)
			}
			if len(rich.DiffLines) != 0 || len(rich.Ranges) != 0 || rich.AddedLines != 0 || rich.RemovedLines != 0 {
				t.Fatalf("unexpected optional preview: %+v", rich)
			}
			if got, err := os.ReadFile(path); err != nil || string(got) != tc.source {
				t.Fatalf("preview changed source: %v", err)
			}
		})
	}
}

func TestExtractFromArgs_PreservesReplacementWhitespace(t *testing.T) {
	for _, name := range []string{"write_file", "edit_file"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "note.txt")
			if err := os.WriteFile(path, []byte("  old\n"), 0600); err != nil {
				t.Fatal(err)
			}
			rich := ExtractFromArgs(name, map[string]any{"path": path, "content": "  new\n", "old_string": "  old\n", "new_string": "  new\n"})
			if rich.AddedLines != 1 || rich.RemovedLines != 1 {
				t.Fatalf("unexpected counts: %+v", rich)
			}
			sawOld, sawNew := false, false
			for _, line := range rich.DiffLines {
				if line.Type == "remove" && line.Content == "  old" {
					sawOld = true
				}
				if line.Type == "add" && line.Content == "  new" {
					sawNew = true
				}
				if strings.Contains(line.Content, "No newline") {
					t.Fatalf("invented missing newline: %+v", rich)
				}
			}
			if !sawOld || !sawNew {
				t.Fatalf("lost whitespace: %+v", rich)
			}
		})
	}
}

func TestWithDiff_BoundsRetainedTextAndRanges(t *testing.T) {
	const count = 1000
	patch := strings.Repeat("@@ -1 +1 @@\n-"+strings.Repeat("界", 2000)+"\n+new\n", count)
	rich := WithDiff(RichFields{FilePath: "owned.txt"}, patch)
	if rich.AddedLines != count || rich.RemovedLines != count {
		t.Fatalf("lost full counts: +%d/-%d", rich.AddedLines, rich.RemovedLines)
	}
	if len(rich.DiffLines) > diffLineLimit || len(rich.Ranges) > diffLineLimit {
		t.Fatal("unbounded rows or ranges")
	}
	bytes := 0
	sawTruncated := false
	for _, line := range rich.DiffLines {
		bytes += len(line.Content)
		if len(line.Content) > previewLineBytes || !utf8.ValidString(line.Content) {
			t.Fatalf("invalid bounded line: %d bytes", len(line.Content))
		}
		sawTruncated = sawTruncated || strings.HasSuffix(line.Content, "... [truncated]")
	}
	if bytes > DefaultPreviewBytes || !sawTruncated {
		t.Fatalf("retained bytes=%d truncated=%v", bytes, sawTruncated)
	}
	if rich.FilePath != "owned.txt" {
		t.Fatal("lost metadata")
	}
}
