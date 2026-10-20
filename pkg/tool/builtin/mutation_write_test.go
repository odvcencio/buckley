package builtin

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

func TestTextMutationTools_PreservePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not supported")
	}
	for _, tc := range []struct {
		name string
		tool interface {
			Execute(map[string]any) (*Result, error)
			SetWorkDir(string)
		}
		params map[string]any
		want   string
	}{
		{"write shorter", &WriteFileTool{}, map[string]any{"content": "new\n"}, "new\n"},
		{"write empty", &WriteFileTool{}, map[string]any{"content": ""}, ""},
		{"edit", &EditFileTool{}, map[string]any{"old_string": "alpha", "new_string": "a"}, "a\nbeta\n"},
		{"insert", &InsertTextTool{}, map[string]any{"line": 2, "text": "insert"}, "alpha\ninsert\nbeta\n"},
		{"delete", &DeleteLinesTool{}, map[string]any{"start_line": 2, "end_line": 2}, "alpha\n"},
		{"search replace", &SearchReplaceTool{}, map[string]any{"search": "alpha", "replace": "a"}, "a\nbeta\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "f")
			if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0640); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			tc.tool.SetWorkDir(root)
			tc.params["path"] = "f"
			result, err := tc.tool.Execute(tc.params)
			if err != nil || !result.Success {
				t.Fatalf("mutation failed: %+v %v", result, err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != tc.want {
				t.Fatalf("content = %q, want %q; error=%v", got, tc.want, err)
			}
			after, err := os.Stat(path)
			if err != nil || after.Mode().Perm() != before.Mode().Perm() {
				t.Fatalf("permissions changed: before=%v after=%v error=%v", before, after, err)
			}
		})
	}
}

func TestWriteMutationFile_WriteError(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires Linux /dev/full")
	}
	err := writeMutationFile("/dev/full", "content", 0600)
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("write error = %v, want ENOSPC", err)
	}
}
