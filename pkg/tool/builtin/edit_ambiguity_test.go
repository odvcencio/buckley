package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditFileTool_AmbiguityLocations(t *testing.T) {
	cases := []struct {
		name, input, old, lines string
		count                   int
	}{
		{"lf", "a\nx\nb\nx\nc", "x", "[2 4]", 2},
		{"crlf", "a\r\nx\r\nb\r\nx\r\nc", "x", "[2 4]", 2},
		{"unicode-prefix", "é\nx\né\nx", "x", "[2 4]", 2},
		{"multiline", "a\nb\nc\na\nb\nc", "a\nb\nc", "[1 4]", 2},
		{"same-line", "go go go", "go", "[1 1 1]", 3},
		{"non-overlapping", "aaaaa", "aa", "[1 1]", 2},
		{"capped", strings.Repeat("PRIVATE_SOURCE_CONTENT\n", 12), "PRIVATE_SOURCE_CONTENT", "[1 2 3 4 5 6 7 8]; 4 more omitted", 12},
		{"empty-unicode", "é", "", "", 2},
	}
	for _, tc := range cases {
		for _, preview := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/preview=%t", tc.name, preview), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "input.txt")
				if err := os.WriteFile(path, []byte(tc.input), 0600); err != nil {
					t.Fatal(err)
				}
				result, err := (&EditFileTool{ShowDiffPreview: preview}).Execute(map[string]any{"path": path, "old_string": tc.old, "new_string": "replacement"})
				want := fmt.Sprintf("edit 1: old_string appears %d times in the file", tc.count)
				if tc.lines != "" {
					want += " (starting lines " + tc.lines + ")"
				}
				want += ". Either provide a more specific string or use replace_all=true"
				if err != nil || result.Success || result.NeedsApproval || result.Error != want {
					t.Fatalf("result=%+v err=%v, want %q rejection", result, err, want)
				}
				if strings.Contains(result.Error, "PRIVATE_SOURCE_CONTENT") {
					t.Fatal("error echoed source text")
				}
				after, err := os.ReadFile(path)
				if err != nil || string(after) != tc.input {
					t.Fatalf("rejection changed bytes: %q %v", after, err)
				}
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatalf("rejection changed mode: %v %v", info, err)
				}
			})
		}
	}
}

func TestEditFileTool_AmbiguityStagedLocations(t *testing.T) {
	for _, tc := range []struct{ name, input, old, new, match, lines string }{
		{"insert-lines", "a\nb\nb\n", "a", "a\nc", "b", "[3 4]"},
		{"remove-lines", "a\nz\nb\nb\n", "a\nz\n", "", "b", "[1 2]"},
		{"create-match", "a\nb\n", "a", "b", "b", "[1 2]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "input.txt")
			if err := os.WriteFile(path, []byte(tc.input), 0600); err != nil {
				t.Fatal(err)
			}
			result, err := (&EditFileTool{}).Execute(map[string]any{"path": path, "edits": []any{
				map[string]any{"old_string": tc.old, "new_string": tc.new},
				map[string]any{"old_string": tc.match, "new_string": "replacement"},
			}})
			want := "edit 2: old_string appears 2 times in the staged text after preceding batch edits (starting lines " + tc.lines + "). Either provide a more specific string or use replace_all=true. The file remains unchanged."
			if err != nil || result.Success || result.Error != want {
				t.Fatalf("result=%+v err=%v, want %q", result, err, want)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != tc.input {
				t.Fatalf("failed batch changed bytes: %q %v", after, err)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("failed batch changed mode: %v %v", info, err)
			}
		})
	}
}

func TestEditFileTool_EmptyAndReplaceAllLegacy(t *testing.T) {
	for _, tc := range []struct {
		name, input, old, new, want string
		all                         bool
		count                       int
	}{
		{"empty-file", "", "", "initial", "initial", false, 1},
		{"empty-unicode-all", "é", "", "x", "xéx", true, 2},
		{"replace-all", "hello hello hello", "hello", "hi", "hi hi hi", true, 3},
	} {
		for _, preview := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/preview=%t", tc.name, preview), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "input.txt")
				if err := os.WriteFile(path, []byte(tc.input), 0600); err != nil {
					t.Fatal(err)
				}
				result, err := (&EditFileTool{ShowDiffPreview: preview}).Execute(map[string]any{"path": path, "old_string": tc.old, "new_string": tc.new, "replace_all": tc.all})
				if err != nil || !result.Success || result.NeedsApproval != preview {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				if result.DiffPreview == nil || result.DiffPreview.NewContent != tc.want {
					t.Fatalf("wrong proposed edit: %+v", result)
				}
				wantDisk := tc.want
				if preview {
					wantDisk = tc.input
				} else if result.Data["replacements"] != tc.count {
					t.Fatalf("replacements=%v want=%d", result.Data["replacements"], tc.count)
				}
				after, err := os.ReadFile(path)
				if err != nil || string(after) != wantDisk {
					t.Fatalf("disk=%q err=%v want=%q", after, err, wantDisk)
				}
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatalf("mode changed: %v %v", info, err)
				}
			})
		}
	}
}
