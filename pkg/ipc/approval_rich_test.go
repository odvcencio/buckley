package ipc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractApprovalRichFieldsShell(t *testing.T) {
	raw, err := json.Marshal(map[string]any{"command": "ls -la"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	rich := extractApprovalRichFields("run_shell", string(raw))
	if rich.operationType != "shell:read" {
		t.Fatalf("operationType=%q want shell:read", rich.operationType)
	}
	if rich.command != "ls -la" {
		t.Fatalf("command=%q want ls -la", rich.command)
	}
	if rich.description == "" {
		t.Fatalf("expected description to be set")
	}
}

func TestExtractApprovalRichFieldsApplyPatch(t *testing.T) {
	patch := "diff --git a/foo.txt b/foo.txt\n" +
		"index 0000000..1111111 100644\n" +
		"--- a/foo.txt\n" +
		"+++ b/foo.txt\n" +
		"@@ -1 +1 @@\n" +
		"-old\n" +
		"+new\n"
	raw, err := json.Marshal(map[string]any{"patch": patch})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	rich := extractApprovalRichFields("apply_patch", string(raw))
	if rich.filePath != "foo.txt" {
		t.Fatalf("filePath=%q want foo.txt", rich.filePath)
	}
	if rich.addedLines != 1 || rich.removedLines != 1 {
		t.Fatalf("added=%d removed=%d want 1/1", rich.addedLines, rich.removedLines)
	}
	if len(rich.diffLines) == 0 {
		t.Fatalf("expected diffLines to be populated")
	}
}

func TestExtractApprovalRichFieldsWriteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("hello\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	raw, err := json.Marshal(map[string]any{
		"path":    path,
		"content": "hello\nworld\n",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	rich := extractApprovalRichFields("write_file", string(raw))
	if rich.filePath != path {
		t.Fatalf("filePath=%q want %q", rich.filePath, path)
	}
	if rich.addedLines != 1 {
		t.Fatalf("addedLines=%d want 1", rich.addedLines)
	}
}

func TestExtractApprovalRichFieldsEditFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "edit.txt")
	if err := os.WriteFile(path, []byte("hello world\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	raw, err := json.Marshal(map[string]any{
		"path":        path,
		"old_string":  "world",
		"new_string":  "buckley",
		"replace_all": false,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	rich := extractApprovalRichFields("edit_file", string(raw))
	if rich.filePath != path {
		t.Fatalf("filePath=%q want %q", rich.filePath, path)
	}
	if rich.addedLines != 1 || rich.removedLines != 1 {
		t.Fatalf("added=%d removed=%d want 1/1", rich.addedLines, rich.removedLines)
	}
}

func TestEnrichToolEventPayload(t *testing.T) {
	payload := map[string]any{
		"toolName":  "run_shell",
		"arguments": `{"command":"ls"}`,
	}
	enrichToolEventPayload(payload)
	if payload["operationType"] != "shell:read" {
		t.Fatalf("operationType=%v want shell:read", payload["operationType"])
	}
	if payload["command"] != "ls" {
		t.Fatalf("command=%v want ls", payload["command"])
	}
}
