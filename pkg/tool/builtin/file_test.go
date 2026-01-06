package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFileTool(t *testing.T) {
	tool := &ReadFileTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "read_file" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "read_file")
		}
		if tool.Description() == "" {
			t.Error("Description() returned empty string")
		}
		params := tool.Parameters()
		if params.Type != "object" {
			t.Errorf("Parameters().Type = %q, want %q", params.Type, "object")
		}
		if _, ok := params.Properties["path"]; !ok {
			t.Error("Parameters() missing 'path' property")
		}
	})

	t.Run("read existing file", func(t *testing.T) {
		// Create a temp file
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "test.txt")
		content := "hello world\nline 2\n"
		if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}

		result, err := tool.Execute(map[string]any{"path": testFile})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("expected success, got error: %s", result.Error)
		}
		if result.Data["content"] != content {
			t.Errorf("content mismatch: got %q, want %q", result.Data["content"], content)
		}
	})

	t.Run("read large file triggers abridging", func(t *testing.T) {
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "large.txt")
		// Create file with >100 lines
		var lines []string
		for i := 0; i < 150; i++ {
			lines = append(lines, "line content")
		}
		content := strings.Join(lines, "\n")
		if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}

		result, err := tool.Execute(map[string]any{"path": testFile})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("expected success, got error: %s", result.Error)
		}
		if !result.ShouldAbridge {
			t.Error("expected ShouldAbridge=true for large file")
		}
		if result.DisplayData == nil {
			t.Error("expected DisplayData to be set for large file")
		}
	})

	t.Run("read enforces max file size", func(t *testing.T) {
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "big.txt")
		if err := os.WriteFile(testFile, []byte("too-big"), 0644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}

		limited := &ReadFileTool{}
		limited.SetMaxFileSizeBytes(3)
		result, err := limited.Execute(map[string]any{"path": testFile})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Fatalf("expected failure for oversized file")
		}
	})
}

func TestWriteFileTool(t *testing.T) {
	tool := &WriteFileTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "write_file" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "write_file")
		}
		if tool.Description() == "" {
			t.Error("Description() returned empty string")
		}
	})

	t.Run("write new file", func(t *testing.T) {
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "new.txt")
		content := "new file content"

		result, err := tool.Execute(map[string]any{
			"path":    testFile,
			"content": content,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("expected success, got error: %s", result.Error)
		}

		// Verify file was created
		written, err := os.ReadFile(testFile)
		if err != nil {
			t.Fatalf("failed to read written file: %v", err)
		}
		if string(written) != content {
			t.Errorf("content mismatch: got %q, want %q", string(written), content)
		}
	})

	t.Run("write creates parent directories", func(t *testing.T) {
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "sub", "dir", "file.txt")
		content := "nested file"

		result, err := tool.Execute(map[string]any{
			"path":    testFile,
			"content": content,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("expected success, got error: %s", result.Error)
		}

		// Verify file exists
		if _, err := os.Stat(testFile); err != nil {
			t.Errorf("file should exist: %v", err)
		}
	})

	t.Run("overwrite existing file", func(t *testing.T) {
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "existing.txt")

		// Create existing file
		if err := os.WriteFile(testFile, []byte("old content"), 0644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}

		newContent := "new content"
		result, err := tool.Execute(map[string]any{
			"path":    testFile,
			"content": newContent,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("expected success, got error: %s", result.Error)
		}

		// DisplayData should indicate it's not new
		if result.DisplayData == nil {
			t.Error("expected DisplayData to be set")
		} else if isNew, ok := result.DisplayData["is_new"].(bool); ok && isNew {
			t.Error("expected is_new=false for overwritten file")
		}
	})

	t.Run("write enforces max file size", func(t *testing.T) {
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "new.txt")

		limited := &WriteFileTool{}
		limited.SetMaxFileSizeBytes(3)

		result, err := limited.Execute(map[string]any{
			"path":    testFile,
			"content": "too-big",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Fatalf("expected failure for oversized write")
		}
	})
}

func TestListDirectoryTool(t *testing.T) {
	tool := &ListDirectoryTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "list_directory" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "list_directory")
		}
	})

	t.Run("list directory with files", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create some files and dirs
		if err := os.WriteFile(filepath.Join(tmpDir, "file1.txt"), []byte("a"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmpDir, "file2.txt"), []byte("b"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(tmpDir, "subdir"), 0755); err != nil {
			t.Fatal(err)
		}

		result, err := tool.Execute(map[string]any{"path": tmpDir})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("expected success, got error: %s", result.Error)
		}

		count, ok := result.Data["count"].(int)
		if !ok || count != 3 {
			t.Errorf("expected count=3, got %v", result.Data["count"])
		}
	})

	t.Run("default path uses current directory", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("expected success, got error: %s", result.Error)
		}
	})
}

func TestFileExistsTool(t *testing.T) {
	tool := &FileExistsTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "file_exists" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "file_exists")
		}
	})

	t.Run("existing file returns true", func(t *testing.T) {
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "exists.txt")
		if err := os.WriteFile(testFile, []byte("content"), 0644); err != nil {
			t.Fatal(err)
		}

		result, err := tool.Execute(map[string]any{"path": testFile})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("expected success, got error: %s", result.Error)
		}
		if exists, ok := result.Data["exists"].(bool); !ok || !exists {
			t.Error("expected exists=true for existing file")
		}
	})

	t.Run("directory returns true with is_dir", func(t *testing.T) {
		tmpDir := t.TempDir()

		result, err := tool.Execute(map[string]any{"path": tmpDir})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("expected success, got error: %s", result.Error)
		}
		if exists, ok := result.Data["exists"].(bool); !ok || !exists {
			t.Error("expected exists=true for directory")
		}
		if isDir, ok := result.Data["is_dir"].(bool); !ok || !isDir {
			t.Error("expected is_dir=true for directory")
		}
	})
}

func TestGetFileInfoTool(t *testing.T) {
	tool := &GetFileInfoTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "get_file_info" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "get_file_info")
		}
	})

	t.Run("get info for existing file", func(t *testing.T) {
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "info.txt")
		content := "some content"
		if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		result, err := tool.Execute(map[string]any{"path": testFile})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("expected success, got error: %s", result.Error)
		}

		if size, ok := result.Data["size"].(int64); !ok || size != int64(len(content)) {
			t.Errorf("expected size=%d, got %v", len(content), result.Data["size"])
		}
	})

	t.Run("missing path parameter", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for missing path")
		}
	})

	t.Run("nonexistent file", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{"path": "/nonexistent/file.txt"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for nonexistent file")
		}
	})
}

func TestPatchFileTool(t *testing.T) {
	tool := &PatchFileTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "apply_patch" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "apply_patch")
		}
	})

	t.Run("missing patch parameter", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for missing patch")
		}
	})

	t.Run("empty patch", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{"patch": "   "})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for empty patch")
		}
	})

	t.Run("negative strip", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{
			"patch": "some patch",
			"strip": -1,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for negative strip")
		}
	})

	t.Run("invalid strip type", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{
			"patch": "some patch",
			"strip": []string{"invalid"},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for invalid strip type")
		}
	})

	t.Run("strip as string", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{
			"patch": "invalid patch content",
			"strip": "1",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Patch will fail because content is invalid, but strip parsing should work
		if result.Error == "strip parameter must be an integer" {
			t.Error("strip as string should be accepted")
		}
	})

	t.Run("strip as empty string defaults to 0", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{
			"patch": "invalid patch",
			"strip": "",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Should not fail on strip parsing
		if strings.Contains(result.Error, "strip") {
			t.Errorf("empty strip string should default to 0: %s", result.Error)
		}
	})
}

func TestFindFilesTool(t *testing.T) {
	tool := &FindFilesTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "find_files" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "find_files")
		}
	})

	t.Run("missing pattern parameter", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for missing pattern")
		}
	})

	t.Run("find files with glob pattern", func(t *testing.T) {
		tmpDir := t.TempDir()
		// Create test files
		if err := os.WriteFile(filepath.Join(tmpDir, "test.go"), []byte("package main"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte("text"), 0644); err != nil {
			t.Fatal(err)
		}

		result, err := tool.Execute(map[string]any{
			"pattern":   "*.go",
			"base_path": tmpDir,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("expected success, got error: %s", result.Error)
		}

		matches, ok := result.Data["matches"].([]string)
		if !ok {
			t.Fatal("expected matches to be []string")
		}
		if len(matches) != 1 {
			t.Errorf("expected 1 match, got %d", len(matches))
		}
	})
}

func TestSearchReplaceTool(t *testing.T) {
	tool := &SearchReplaceTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "search_replace" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "search_replace")
		}
	})

	t.Run("missing required parameters", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for missing parameters")
		}
	})

	t.Run("missing path", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{
			"search":  "old",
			"replace": "new",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for missing path")
		}
	})

	t.Run("simple replace", func(t *testing.T) {
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "replace.txt")
		if err := os.WriteFile(testFile, []byte("hello world"), 0644); err != nil {
			t.Fatal(err)
		}

		result, err := tool.Execute(map[string]any{
			"path":    testFile,
			"search":  "world",
			"replace": "universe",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("expected success, got error: %s", result.Error)
		}

		// Verify replacement
		content, _ := os.ReadFile(testFile)
		if string(content) != "hello universe" {
			t.Errorf("expected 'hello universe', got %q", string(content))
		}
	})

	t.Run("no matches", func(t *testing.T) {
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "nomatch.txt")
		if err := os.WriteFile(testFile, []byte("hello world"), 0644); err != nil {
			t.Fatal(err)
		}

		result, err := tool.Execute(map[string]any{
			"path":    testFile,
			"search":  "notfound",
			"replace": "something",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("expected success even with no matches, got error: %s", result.Error)
		}
		if count, ok := result.Data["replacements"].(int); !ok || count != 0 {
			t.Errorf("expected 0 replacements, got %v", result.Data["replacements"])
		}
	})
}
