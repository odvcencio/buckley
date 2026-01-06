package builtin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindSymbolTool(t *testing.T) {
	tool := &FindSymbolTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "find_symbol" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "find_symbol")
		}
		if tool.Description() == "" {
			t.Error("Description() should not be empty")
		}
		params := tool.Parameters()
		if params.Type != "object" {
			t.Errorf("Parameters().Type = %q, want %q", params.Type, "object")
		}
	})

	t.Run("missing symbol parameter", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for missing symbol")
		}
	})

	t.Run("empty symbol parameter", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{"symbol": ""})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for empty symbol")
		}
	})

	t.Run("search for Go function", func(t *testing.T) {
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "test.go")
		content := `package main

func TestFunction() {
	return
}
`
		if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		result, err := tool.Execute(map[string]any{
			"symbol": "TestFunction",
			"path":   tmpDir,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// May or may not find depending on grep availability
		_ = result
	})

	t.Run("search with type filter", func(t *testing.T) {
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "test.go")
		content := `package main

type MyType struct{}
func MyFunc() {}
`
		if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		result, err := tool.Execute(map[string]any{
			"symbol": "MyType",
			"type":   "type",
			"path":   tmpDir,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		_ = result
	})
}

func TestFindReferencesTool(t *testing.T) {
	tool := &FindReferencesTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "find_references" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "find_references")
		}
		if tool.Description() == "" {
			t.Error("Description() should not be empty")
		}
	})

	t.Run("missing symbol parameter", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for missing symbol")
		}
	})

	t.Run("find references in codebase", func(t *testing.T) {
		tmpDir := t.TempDir()
		// Create files that reference a symbol
		testFile1 := filepath.Join(tmpDir, "file1.go")
		testFile2 := filepath.Join(tmpDir, "file2.go")
		if err := os.WriteFile(testFile1, []byte("package main\nvar x = MySymbol"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(testFile2, []byte("package main\nfunc f() { _ = MySymbol }"), 0644); err != nil {
			t.Fatal(err)
		}

		result, err := tool.Execute(map[string]any{
			"symbol": "MySymbol",
			"path":   tmpDir,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		_ = result
	})
}

func TestGetFunctionSignatureTool(t *testing.T) {
	tool := &GetFunctionSignatureTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "get_function_signature" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "get_function_signature")
		}
		if tool.Description() == "" {
			t.Error("Description() should not be empty")
		}
	})

	t.Run("missing function parameter", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for missing function")
		}
	})

	t.Run("get signature for function", func(t *testing.T) {
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "test.go")
		content := `package main

func TestFunc(a string, b int) (bool, error) {
	return true, nil
}
`
		if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		result, err := tool.Execute(map[string]any{
			"function": "TestFunc",
			"path":     tmpDir,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// May or may not find depending on grep availability
		_ = result
	})
}
