package builtin

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPatchFileToolMissingTerminalNewline(t *testing.T) {
	if _, err := exec.LookPath("patch"); err != nil {
		t.Skip("patch is required")
	}
	for _, tc := range []struct{ name, patch, want string }{
		{"replacement", "--- sample.txt\n+++ sample.txt\n@@ -1 +1 @@\n-old\n+new", "new\n"},
		{"context", "--- sample.txt\n+++ sample.txt\n@@ -1,2 +1,2 @@\n-old\n+new\n tail", "new\ntail\n"},
		{"explicit source without newline", "--- sample.txt\n+++ sample.txt\n@@ -1 +1 @@\n-old\n+new\n\\ No newline at end of file", "new"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			original := "old\n"
			if tc.name == "context" {
				original += "tail\n"
			}
			path := filepath.Join(root, "sample.txt")
			if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
				t.Fatal(err)
			}
			tool := &PatchFileTool{}
			tool.SetWorkDir(root)
			result, err := tool.Execute(map[string]any{"patch": tc.patch})
			if err != nil || result == nil || !result.Success {
				t.Fatalf("Execute = %+v, %v", result, err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != tc.want {
				t.Fatalf("content = %q, %v; want %q", got, err, tc.want)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 1 {
				t.Fatalf("patch artifacts: %v, %v", entries, err)
			}
		})
	}
}

func TestPatchFileToolStillRejectsTruncatedHunk(t *testing.T) {
	if _, err := exec.LookPath("patch"); err != nil {
		t.Skip("patch is required")
	}
	root := t.TempDir()
	path := filepath.Join(root, "sample.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &PatchFileTool{}
	tool.SetWorkDir(root)
	result, err := tool.Execute(map[string]any{"patch": "--- sample.txt\n+++ sample.txt\n@@ -1,2 +1,2 @@\n-old\n+new"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success {
		t.Fatal("truncated patch succeeded")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "old\n" {
		t.Fatalf("truncated patch changed file: %q, %v", got, err)
	}
	if result.Error == "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
}
