package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFileTool_LargeFilePagination(t *testing.T) {
	root := t.TempDir()
	var lines []string
	for i := 1; i <= 250; i++ {
		lines = append(lines, fmt.Sprintf("line %03d", i))
	}
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	reader := &ReadFileTool{}
	reader.SetWorkDir(root)
	reader.SetMaxFileSizeBytes(1000)
	var got []string
	for start := 1; ; {
		result, err := reader.Execute(map[string]any{"path": "large.txt", "start_line": start, "end_line": start + 500})
		if err != nil || !result.Success {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		page := result.Data["page"].(map[string]any)
		got = append(got, strings.Split(result.Data["content"].(string), "\n")...)
		if page["has_more"] == false {
			break
		}
		next := page["next_start_line"].(int)
		if next <= start || next > start+100 {
			t.Fatalf("bad page: %+v", page)
		}
		start = next
	}
	if strings.Join(got, "\n") != strings.Join(lines, "\n") {
		t.Fatal("pagination lost or duplicated lines")
	}
}

func TestReadFileTool_ActionableFailures(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct{ name, content, kind, hint string }{
		{"missing", "", "file_not_found", "find_files"},
		{"binary", "abc\x00def", "binary_file", "hex dump"},
		{"huge-line", strings.Repeat("x", 2000), "line_too_large", "bounded byte read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name != "missing" {
				if err := os.WriteFile(filepath.Join(root, tc.name), []byte(tc.content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			reader := &ReadFileTool{}
			reader.SetWorkDir(root)
			reader.SetMaxFileSizeBytes(1000)
			result, err := reader.Execute(map[string]any{"path": tc.name})
			if err != nil || result.Success || result.Data["error_kind"] != tc.kind || !strings.Contains(result.Error, tc.hint) {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}
