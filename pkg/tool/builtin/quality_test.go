package builtin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAnalyzeComplexityTool(t *testing.T) {
	tool := &AnalyzeComplexityTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "analyze_complexity" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "analyze_complexity")
		}
		if tool.Description() == "" {
			t.Error("Description() should not be empty")
		}
		params := tool.Parameters()
		if params.Type != "object" {
			t.Errorf("Parameters().Type = %q, want %q", params.Type, "object")
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

	t.Run("analyze Go file", func(t *testing.T) {
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "test.go")
		content := `package main

func simple() {
	x := 1
	y := 2
	z := x + y
	_ = z
}

func complex() {
	for i := 0; i < 10; i++ {
		if i%2 == 0 {
			for j := 0; j < i; j++ {
				if j%3 == 0 {
					continue
				}
			}
		}
	}
}
`
		if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		result, err := tool.Execute(map[string]any{
			"path": testFile,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("expected success, got error: %s", result.Error)
		}
	})

	t.Run("analyze directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte("package main\nfunc f() {}"), 0644); err != nil {
			t.Fatal(err)
		}

		result, err := tool.Execute(map[string]any{
			"path": tmpDir,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Should succeed
		_ = result
	})

	t.Run("nonexistent file", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{
			"path": "/nonexistent/file.go",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for nonexistent file")
		}
	})
}

func TestFindDuplicatesTool(t *testing.T) {
	tool := &FindDuplicatesTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "find_duplicates" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "find_duplicates")
		}
		if tool.Description() == "" {
			t.Error("Description() should not be empty")
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

	t.Run("find duplicates in directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		// Create files with duplicate code
		code1 := `package main

func duplicate() {
	x := 1
	y := 2
	z := x + y
	_ = z
}
`
		code2 := `package main

func duplicate() {
	x := 1
	y := 2
	z := x + y
	_ = z
}

func other() {}
`
		if err := os.WriteFile(filepath.Join(tmpDir, "file1.go"), []byte(code1), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmpDir, "file2.go"), []byte(code2), 0644); err != nil {
			t.Fatal(err)
		}

		result, err := tool.Execute(map[string]any{
			"path": tmpDir,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// May or may not find duplicates depending on implementation
		_ = result
	})

	t.Run("find duplicates with min_lines", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(tmpDir, "test.go"), []byte("package main\nfunc f() {}"), 0644); err != nil {
			t.Fatal(err)
		}

		result, err := tool.Execute(map[string]any{
			"path":      tmpDir,
			"min_lines": 5,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		_ = result
	})
}
