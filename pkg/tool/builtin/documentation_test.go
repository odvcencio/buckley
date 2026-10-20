package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateDocstringTool(t *testing.T) {
	tool := &GenerateDocstringTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "generate_docstring" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "generate_docstring")
		}
		if tool.Description() == "" {
			t.Error("Description() should not be empty")
		}
		params := tool.Parameters()
		if params.Type != "object" {
			t.Errorf("Parameters().Type = %q, want %q", params.Type, "object")
		}
	})

	t.Run("missing file parameter", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for missing file")
		}
	})

	t.Run("generate docstring for Go file", func(t *testing.T) {
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "calc.go")
		content := `package calc

func Add(a, b int) int {
	return a + b
}
`
		if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		result, err := tool.Execute(map[string]any{
			"file": testFile,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success || !strings.Contains(result.Error, "function_name") {
			t.Fatalf("missing target accepted: %+v", result)
		}
	})

	t.Run("generate docstring for specific function", func(t *testing.T) {
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "calc.go")
		content := `package calc

func Add(a, b int) int {
	return a + b
}
`
		if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		result, err := tool.Execute(map[string]any{
			"file":          testFile,
			"function_name": "Add",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Fatalf("generate: %+v", result)
		}
		got, err := os.ReadFile(testFile)
		if err != nil || !strings.Contains(string(got), "// Add TODO: add description\n") {
			t.Fatalf("missing godoc: %s %v", got, err)
		}
	})

	t.Run("nonexistent file", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{
			"file": "/nonexistent/file.go",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for nonexistent file")
		}
	})
}

func TestExplainCodeTool(t *testing.T) {
	tool := &ExplainCodeTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "explain_code" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "explain_code")
		}
		if tool.Description() == "" {
			t.Error("Description() should not be empty")
		}
	})

	t.Run("missing file parameter", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for missing file")
		}
	})

	t.Run("explain Go file", func(t *testing.T) {
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "algo.go")
		content := `package algo

func BinarySearch(arr []int, target int) int {
	left, right := 0, len(arr)-1
	for left <= right {
		mid := left + (right-left)/2
		if arr[mid] == target {
			return mid
		}
		if arr[mid] < target {
			left = mid + 1
		} else {
			right = mid - 1
		}
	}
	return -1
}
`
		if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		result, err := tool.Execute(map[string]any{
			"file": testFile,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Fatalf("explain: %+v", result)
		}
		analysis := result.Data["explanation"].(map[string]any)
		if analysis["has_loops"] != true || analysis["has_conditions"] != true {
			t.Fatalf("missing control-flow indicators: %+v", analysis)
		}
	})

	t.Run("explain specific lines", func(t *testing.T) {
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "code.go")
		content := "line1\nline2\nline3\nline4\nline5"
		if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		result, err := tool.Execute(map[string]any{
			"file":       testFile,
			"start_line": 2,
			"end_line":   4,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success || result.Data["code"] != "line2\nline3\nline4" {
			t.Fatalf("wrong line selection: %+v", result)
		}
	})

	t.Run("nonexistent file", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{
			"file": "/nonexistent/file.go",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for nonexistent file")
		}
	})
}
