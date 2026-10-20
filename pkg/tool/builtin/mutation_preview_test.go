package builtin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileTool_PreviewNewFiles(t *testing.T) {
	for _, content := range []string{"", "created\n"} {
		t.Run(content, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "new", "nested", "note.txt")
			tool := &WriteFileTool{}
			tool.SetWorkDir(root)
			params := map[string]any{"path": "new/nested/note.txt", "content": content}
			preview, err := tool.Preview(params)
			if err != nil || preview == nil || !preview.Success || !preview.DiffPreview.IsNew {
				t.Fatalf("preview=%+v err=%v", preview, err)
			}
			if _, err := os.Stat(filepath.Join(root, "new")); !os.IsNotExist(err) {
				t.Fatalf("preview created directories: %v", err)
			}
			if err := preview.ValidateApproval(); err != nil {
				t.Fatal(err)
			}
			result, err := tool.Execute(params)
			if err != nil || !result.Success {
				t.Fatalf("execute=%+v err=%v", result, err)
			}
			if got, err := os.ReadFile(path); err != nil || string(got) != content {
				t.Fatalf("bytes=%q want=%q err=%v", got, content, err)
			}
			if err := preview.ValidateApproval(); err == nil {
				t.Fatal("creation approval remained valid after target appeared")
			}
		})
	}
}
