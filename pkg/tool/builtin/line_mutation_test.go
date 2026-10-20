package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInsertTextTool_LineBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, before, text, want string
		line, actualLine         int
	}{
		{"empty first", "", "new", "new\n", 1, 1},
		{"empty append", "", "new", "\nnew", 99, 2},
		{"unterminated append", "old", "new", "old\nnew", 2, 2},
		{"terminated append", "old\n", "new", "old\n\nnew", 99, 3},
		{"before trailing row", "old\n", "new", "old\nnew\n", 2, 2},
		{"clamp first", "old", "new", "new\nold", -10, 1},
		{"empty text", "a\nb", "", "a\n\nb", 2, 2},
		{"trailing separator", "a\nb", "new\n", "a\nnew\n\nb", 2, 2},
		{"crlf", "a\r\nb\r\n", "new", "a\r\nnew\nb\r\n", 2, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "f")
			if err := os.WriteFile(path, []byte(tc.before), 0600); err != nil {
				t.Fatal(err)
			}
			tool := &InsertTextTool{}
			tool.SetWorkDir(root)
			result, err := tool.Execute(map[string]any{"path": "f", "line": tc.line, "text": tc.text})
			if err != nil || !result.Success {
				t.Fatalf("insert failed: %+v %v", result, err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != tc.want || result.Data["line"] != tc.actualLine {
				t.Fatalf("content=%q line=%v, want %q line=%d; error=%v", got, result.Data["line"], tc.want, tc.actualLine, err)
			}
		})
	}
}

func TestDeleteLinesTool_LineBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, before, want string
		start, end         int
	}{
		{"empty", "", "", 1, 1},
		{"all", "a\nb\n", "", 1, 99},
		{"first", "a\nb\nc", "b\nc", -10, 1},
		{"middle", "a\nb\nc", "a\nc", 2, 2},
		{"last unterminated", "a\nb", "a", 2, 2},
		{"last terminated", "a\nb\n", "a\n", 2, 2},
		{"trailing empty row", "a\nb\n", "a\nb", 3, 3},
		{"through eof", "a\nb\n", "a", 2, 99},
		{"crlf trailing row", "a\r\nb\r\n", "a\r\nb\r", 3, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "f")
			if err := os.WriteFile(path, []byte(tc.before), 0600); err != nil {
				t.Fatal(err)
			}
			tool := &DeleteLinesTool{}
			tool.SetWorkDir(root)
			result, err := tool.Execute(map[string]any{"path": "f", "start_line": tc.start, "end_line": tc.end})
			if err != nil || !result.Success {
				t.Fatalf("delete failed: %+v %v", result, err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != tc.want {
				t.Fatalf("content=%q, want %q; error=%v", got, tc.want, err)
			}
		})
	}
}

func FuzzLineMutationTools_PreserveContents(f *testing.F) {
	for _, seed := range []struct {
		content, text string
		start, end    int
	}{{"", "new", 1, 1}, {"a\nb\n", "new\n", 2, 3}, {"a\r\nb", "", -2, 20}, {"\x00\xff\nlast", "界", 1, 2}} {
		f.Add(seed.content, seed.text, seed.start, seed.end)
	}
	f.Fuzz(func(t *testing.T, content, text string, start, end int) {
		if len(content)+len(text) > 1024 {
			t.Skip()
		}
		root := t.TempDir()
		path := filepath.Join(root, "f")
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(content, "\n")
		line := min(max(start, 1), len(lines)+1)
		inserted := append([]string{}, lines[:line-1]...)
		inserted = append(inserted, strings.Split(text, "\n")...)
		inserted = append(inserted, lines[line-1:]...)
		insert := &InsertTextTool{}
		insert.SetWorkDir(root)
		result, err := insert.Execute(map[string]any{"path": "f", "line": start, "text": text})
		if err != nil || !result.Success {
			t.Fatalf("insert failed: %+v %v", result, err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != strings.Join(inserted, "\n") || result.Data["line"] != line {
			t.Fatalf("insert content=%q, want=%q; error=%v", got, strings.Join(inserted, "\n"), err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		first, last := max(start, 1), min(end, len(lines))
		want := content
		if first <= last {
			remaining := append([]string{}, lines[:first-1]...)
			remaining = append(remaining, lines[last:]...)
			want = strings.Join(remaining, "\n")
		}
		delete := &DeleteLinesTool{}
		delete.SetWorkDir(root)
		result, err = delete.Execute(map[string]any{"path": "f", "start_line": start, "end_line": end})
		if err != nil || result.Success != (first <= last) {
			t.Fatalf("delete result differs: %+v %v", result, err)
		}
		got, err = os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("delete content=%q, want=%q; error=%v", got, want, err)
		}
	})
}
