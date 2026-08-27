package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
		if _, ok := params.Properties["start_line"]; !ok {
			t.Error("Parameters() missing 'start_line' property")
		}
		if _, ok := params.Properties["end_line"]; !ok {
			t.Error("Parameters() missing 'end_line' property")
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

	t.Run("read page returns continuation metadata", func(t *testing.T) {
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "paged.txt")
		lines := make([]string, 250)
		for i := range lines {
			lines[i] = fmt.Sprintf("line %d", i+1)
		}
		content := strings.Join(lines, "\n") + "\n"
		if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
			t.Fatalf("failed to create paged file: %v", err)
		}

		result, err := tool.Execute(map[string]any{"path": testFile, "start_line": 101, "end_line": 200})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success || !result.ShouldAbridge {
			t.Fatalf("result = %+v, want successful abridged page", result)
		}
		if got := result.Data["content"]; got != content {
			t.Fatalf("Data content lost full file: got %T %v", got, got)
		}
		display := result.DisplayData["content"].(string)
		if !strings.HasPrefix(display, "line 101\n") || !strings.HasSuffix(display, "line 200") || strings.Contains(display, "line 201") {
			t.Fatalf("display page = %q, want lines 101 through 200 only", display)
		}
		page := result.DisplayData["page"].(map[string]any)
		if page["start_line"] != 101 || page["end_line"] != 200 || page["total_lines"] != 250 || page["next_start_line"] != 201 || page["has_more"] != true {
			t.Fatalf("page metadata = %#v", page)
		}
	})

	t.Run("rejects invalid page parameters", func(t *testing.T) {
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "small.txt")
		if err := os.WriteFile(testFile, []byte("one\ntwo\n"), 0644); err != nil {
			t.Fatalf("failed to create small file: %v", err)
		}

		for name, params := range map[string]map[string]any{
			"zero start":    {"path": testFile, "start_line": 0},
			"reverse range": {"path": testFile, "start_line": 2, "end_line": 1},
		} {
			t.Run(name, func(t *testing.T) {
				result, err := tool.Execute(params)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.Success {
					t.Fatalf("expected page validation error, got %+v", result)
				}
			})
		}
	})

	t.Run("clamps oversized explicit page with continuation", func(t *testing.T) {
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "oversized-page.txt")
		lines := make([]string, 700)
		for i := range lines {
			lines[i] = fmt.Sprintf("line %d", i+1)
		}
		if err := os.WriteFile(testFile, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}

		result, err := tool.Execute(map[string]any{"path": testFile, "start_line": 1, "end_line": 700})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Fatalf("expected clamped success, got %s", result.Error)
		}
		page := result.DisplayData["page"].(map[string]any)
		if page["end_line"] != 500 || page["next_start_line"] != 501 || page["has_more"] != true {
			t.Fatalf("page metadata = %#v", page)
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

	t.Run("write Go file attaches diagnostics", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module example.com/test\n\ngo 1.26\n"), 0644); err != nil {
			t.Fatalf("failed to create module: %v", err)
		}

		tool := &WriteFileTool{}
		tool.SetWorkDir(tmpDir)
		result, err := tool.Execute(map[string]any{
			"path":    "broken.go",
			"content": "package example\n\nfunc broken( {\n",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Fatalf("expected successful write, got error: %s", result.Error)
		}

		diagnostics, ok := result.Data["diagnostics"].(string)
		if !ok || diagnostics == "" {
			t.Fatalf("expected build diagnostics in result data, got %v", result.Data["diagnostics"])
		}
		if result.DisplayData["diagnostics"] != diagnostics {
			t.Errorf("display diagnostics = %v, want %q", result.DisplayData["diagnostics"], diagnostics)
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

	t.Run("overwrite existing empty file", func(t *testing.T) {
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "empty.txt")
		if err := os.WriteFile(testFile, []byte{}, 0644); err != nil {
			t.Fatalf("failed to create empty test file: %v", err)
		}

		result, err := tool.Execute(map[string]any{
			"path":    testFile,
			"content": "new content",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Fatalf("expected success, got error: %s", result.Error)
		}

		if result.DisplayData == nil {
			t.Fatal("expected DisplayData to be set")
		}
		if isNew, ok := result.DisplayData["is_new"].(bool); !ok || isNew {
			t.Fatalf("expected is_new=false for overwritten empty file, got %v", result.DisplayData["is_new"])
		}
		if summary, _ := result.DisplayData["summary"].(string); !strings.Contains(summary, "Wrote") {
			t.Fatalf("expected overwrite summary, got %q", summary)
		}
	})

	t.Run("overwrite existing unreadable but writable file", func(t *testing.T) {
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "write-only.txt")
		if err := os.WriteFile(testFile, []byte("old content"), 0222); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}
		if _, err := os.ReadFile(testFile); err == nil {
			_ = os.Chmod(testFile, 0644)
			t.Skip("platform permits reading a write-only file")
		}
		t.Cleanup(func() { _ = os.Chmod(testFile, 0644) })

		result, err := tool.Execute(map[string]any{
			"path":    testFile,
			"content": "new content",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Fatalf("expected success, got error: %s", result.Error)
		}
		if isNew, ok := result.DisplayData["is_new"].(bool); !ok || isNew {
			t.Fatalf("expected is_new=false for overwritten unreadable file, got %v", result.DisplayData["is_new"])
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

func TestFindFilesTool_RecursiveAndPaged(t *testing.T) {
	tool := &FindFilesTool{}

	t.Run("matches recursive path glob", func(t *testing.T) {
		tmpDir := t.TempDir()
		for _, name := range []string{"src/root.ts", "src/nested/view.ts", "other/view.ts", "src/nested/view.go"} {
			path := filepath.Join(tmpDir, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("source"), 0o644); err != nil {
				t.Fatal(err)
			}
		}

		result, err := tool.Execute(map[string]any{"pattern": "src/**/*.ts", "base_path": tmpDir})
		if err != nil || !result.Success {
			t.Fatalf("recursive find = %#v, %v", result, err)
		}
		matches := result.Data["matches"].([]string)
		want := []string{"src/nested/view.ts", "src/root.ts"}
		if !reflect.DeepEqual(matches, want) {
			t.Fatalf("matches = %#v, want %#v", matches, want)
		}
	})

	t.Run("expands extension alternatives", func(t *testing.T) {
		tmpDir := t.TempDir()
		for _, name := range []string{"main.go", "src/lib.rs", "src/readme.md"} {
			path := filepath.Join(tmpDir, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("source"), 0o644); err != nil {
				t.Fatal(err)
			}
		}

		result, err := tool.Execute(map[string]any{"pattern": "**/*.{go,rs}", "base_path": tmpDir})
		if err != nil || !result.Success {
			t.Fatalf("brace find = %#v, %v", result, err)
		}
		matches := result.Data["matches"].([]string)
		want := []string{"main.go", "src/lib.rs"}
		if !reflect.DeepEqual(matches, want) {
			t.Fatalf("matches = %#v, want %#v", matches, want)
		}
	})

	t.Run("reports invalid glob", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{"pattern": "src/[", "base_path": t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		if result.Success || !strings.Contains(result.Error, "invalid glob pattern") {
			t.Fatalf("result = %#v, want explicit invalid-pattern failure", result)
		}
	})

	t.Run("reports missing base directory", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "does-not-exist")
		result, err := tool.Execute(map[string]any{"pattern": "*.go", "base_path": missing})
		if err != nil {
			t.Fatal(err)
		}
		if result.Success || !strings.Contains(result.Error, "failed to inspect base path") {
			t.Fatalf("result = %#v, want explicit missing-base failure", result)
		}
	})

	t.Run("rejects a file as the base directory", func(t *testing.T) {
		root := t.TempDir()
		baseFile := filepath.Join(root, "main.go")
		if err := os.WriteFile(baseFile, []byte("package main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		result, err := tool.Execute(map[string]any{"pattern": "*.go", "base_path": baseFile})
		if err != nil {
			t.Fatal(err)
		}
		if result.Success || !strings.Contains(result.Error, "not a directory") {
			t.Fatalf("result = %#v, want explicit non-directory failure", result)
		}
	})

	t.Run("skips dependency directories", func(t *testing.T) {
		tmpDir := t.TempDir()
		srcDir := filepath.Join(tmpDir, "src")
		depDir := filepath.Join(tmpDir, "node_modules", "pkg")
		if err := os.MkdirAll(srcDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(depDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(srcDir, "package.json"), []byte("{}"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(depDir, "package.json"), []byte("{}"), 0644); err != nil {
			t.Fatal(err)
		}

		result, err := tool.Execute(map[string]any{
			"pattern":   "package.json",
			"base_path": tmpDir,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		matches := result.Data["matches"].([]string)
		if len(matches) != 1 || matches[0] != filepath.Join("src", "package.json") {
			t.Fatalf("matches = %#v, want only src/package.json", matches)
		}
	})

	t.Run("abridges large result sets", func(t *testing.T) {
		tmpDir := t.TempDir()
		for i := 0; i < 205; i++ {
			name := fmt.Sprintf("file-%03d.go", i)
			if err := os.WriteFile(filepath.Join(tmpDir, name), []byte("package main"), 0644); err != nil {
				t.Fatal(err)
			}
		}

		result, err := tool.Execute(map[string]any{
			"pattern":   "*.go",
			"base_path": tmpDir,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Data["count"].(int) != 205 {
			t.Fatalf("full result count = %v, want 205", result.Data["count"])
		}
		displayMatches, ok := result.Data["matches"].([]string)
		if !ok {
			t.Fatalf("page matches type = %T, want []string", result.Data["matches"])
		}
		if len(displayMatches) != 200 {
			t.Fatalf("display matches length = %d, want 200", len(displayMatches))
		}
		if result.Data["next_offset"] != 200 {
			t.Fatalf("next_offset = %v, want 200", result.Data["next_offset"])
		}

		second, err := tool.Execute(map[string]any{
			"pattern":   "*.go",
			"base_path": tmpDir,
			"offset":    200,
		})
		if err != nil || !second.Success {
			t.Fatalf("second page = %#v, %v", second, err)
		}
		secondMatches := second.Data["matches"].([]string)
		if len(secondMatches) != 5 || secondMatches[0] != "file-200.go" {
			t.Fatalf("second page matches = %#v", secondMatches)
		}

		pastEnd, err := tool.Execute(map[string]any{
			"pattern": "*.go", "base_path": tmpDir, "offset": 999,
		})
		if err != nil || !pastEnd.Success {
			t.Fatalf("past-end page = %#v, %v", pastEnd, err)
		}
		if got := pastEnd.Data["matches"].([]string); len(got) != 0 {
			t.Fatalf("past-end matches = %#v, want empty", got)
		}
		if summary, _ := pastEnd.Data["summary"].(string); !strings.Contains(summary, "no records at offset 999") {
			t.Fatalf("past-end summary = %q", summary)
		}
	})
}

func BenchmarkFindFilesRecursiveBraceGlob(b *testing.B) {
	root := b.TempDir()
	for dir := 0; dir < 20; dir++ {
		for file := 0; file < 50; file++ {
			path := filepath.Join(root, fmt.Sprintf("pkg-%02d", dir), fmt.Sprintf("file-%03d.go", file))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				b.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("package benchmark\n"), 0o644); err != nil {
				b.Fatal(err)
			}
		}
	}
	tool := &FindFilesTool{}
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		result, err := tool.Execute(map[string]any{
			"pattern":   "**/*.{go,rs,ts}",
			"base_path": root,
		})
		if err != nil || result == nil || !result.Success {
			b.Fatalf("Execute = %#v, %v", result, err)
		}
	}
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
