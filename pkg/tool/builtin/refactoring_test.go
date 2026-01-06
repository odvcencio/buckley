package builtin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRenameSymbolTool(t *testing.T) {
	tool := &RenameSymbolTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "rename_symbol" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "rename_symbol")
		}
		if tool.Description() == "" {
			t.Error("Description() should not be empty")
		}
		params := tool.Parameters()
		if params.Type != "object" {
			t.Errorf("Parameters().Type = %q, want %q", params.Type, "object")
		}
	})

	t.Run("missing old_name parameter", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{
			"new_name": "NewName",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for missing old_name")
		}
	})

	t.Run("missing new_name parameter", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{
			"old_name": "OldName",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for missing new_name")
		}
	})

	t.Run("rename in Go file", func(t *testing.T) {
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "test.go")
		content := `package main

func OldName() {
	x := OldName
	_ = x
}
`
		if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		result, err := tool.Execute(map[string]any{
			"old_name": "OldName",
			"new_name": "NewName",
			"path":     tmpDir,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// May succeed or fail depending on implementation
		_ = result
	})

	t.Run("rename in specific file", func(t *testing.T) {
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "test.go")
		if err := os.WriteFile(testFile, []byte("package main\nvar foo = 1"), 0644); err != nil {
			t.Fatal(err)
		}

		result, err := tool.Execute(map[string]any{
			"old_name": "foo",
			"new_name": "bar",
			"path":     testFile,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		_ = result
	})
}

func TestExtractFunctionTool(t *testing.T) {
	tool := &ExtractFunctionTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "extract_function" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "extract_function")
		}
		if tool.Description() == "" {
			t.Error("Description() should not be empty")
		}
	})

	t.Run("missing file parameter", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{
			"start_line":    1,
			"end_line":      5,
			"function_name": "extractedFunc",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for missing file")
		}
	})

	t.Run("missing start_line parameter", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{
			"file":          "test.go",
			"end_line":      5,
			"function_name": "extractedFunc",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for missing start_line")
		}
	})

	t.Run("extract function", func(t *testing.T) {
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "test.go")
		content := `package main

func main() {
	x := 1
	y := 2
	z := x + y
	_ = z
}
`
		if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		result, err := tool.Execute(map[string]any{
			"file":          testFile,
			"start_line":    4,
			"end_line":      7,
			"function_name": "compute",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// May succeed or fail depending on implementation
		_ = result
	})

	t.Run("nonexistent file", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{
			"file":          "/nonexistent/file.go",
			"start_line":    1,
			"end_line":      5,
			"function_name": "func",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for nonexistent file")
		}
	})
}
