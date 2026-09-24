package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/agentcoord"
)

func TestReadFileTool_OutsideWorkDirOptIn(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "instructions")
	if err := os.WriteFile(outside, []byte("outside evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	reader := &ReadFileTool{}
	reader.SetWorkDir(root)
	for _, allowed := range []bool{false, true, false} {
		reader.SetOutsideWorkDirReads(allowed, nil)
		result, err := reader.Execute(map[string]any{"path": outside})
		if err != nil || result.Success != allowed {
			t.Fatalf("allowed=%v result=%+v err=%v", allowed, result, err)
		}
		if allowed && result.Data["content"] != "outside evidence" {
			t.Fatalf("content=%v", result.Data)
		}
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	relativeDenied, err := filepath.Rel(root, filepath.Dir(outside))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Dir(outside))
	for _, denied := range []string{filepath.Dir(outside), relativeDenied, "~", "~/instructions"} {
		reader.SetOutsideWorkDirReads(true, []string{denied})
		for _, path := range []string{outside, link} {
			result, err := reader.Execute(map[string]any{"path": path})
			if err != nil || result.Success || !strings.Contains(result.Error, "denied") {
				t.Fatalf("denied=%q read=%+v err=%v", denied, result, err)
			}
		}
	}
	reader.SetOutsideWorkDirReads(true, nil)
	if err := reader.SetSourceScope(&agentcoord.SourceScope{Files: []agentcoord.SourceFile{{Path: "link"}}}); err != nil {
		t.Fatal(err)
	}
	result, err := reader.Execute(map[string]any{"path": "link"})
	if err != nil || result.Success || !strings.Contains(result.Error, "symlink") {
		t.Fatalf("scoped read=%+v err=%v", result, err)
	}
}
