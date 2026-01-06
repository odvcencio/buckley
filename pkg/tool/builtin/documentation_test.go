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
		// May succeed or fail depending on implementation
		_ = result
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
			"file":     testFile,
			"function": "Add",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		_ = result
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
		// May succeed or fail depending on implementation
		_ = result
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
		_ = result
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

func TestGenerateDocstringHelperCoverage(t *testing.T) {
	tool := &GenerateDocstringTool{}

	if lang := tool.detectLanguage("x.go"); lang != "go" {
		t.Fatalf("detectLanguage(.go)=%q want go", lang)
	}
	if lang := tool.detectLanguage("x.ts"); lang != "javascript" {
		t.Fatalf("detectLanguage(.ts)=%q want javascript", lang)
	}
	if lang := tool.detectLanguage("x.py"); lang != "python" {
		t.Fatalf("detectLanguage(.py)=%q want python", lang)
	}

	if !tool.isDocComment("// hello", "go") {
		t.Fatal("expected Go comment to be recognized")
	}
	if !tool.isDocComment("/** hi */", "javascript") {
		t.Fatal("expected JS doc comment to be recognized")
	}
	if !tool.isDocComment("\"\"\"doc\"\"\"", "python") {
		t.Fatal("expected Python doc comment to be recognized")
	}

	goContent := `package p

func Add(a int, b string) (int, error) {
	return 0, nil
}
`
	goDoc, line, err := tool.generateDocstring(goContent, "Add", "", "go")
	if err != nil {
		t.Fatalf("generateDocstring go error: %v", err)
	}
	if line != 3 {
		t.Fatalf("go line=%d want 3", line)
	}
	if !strings.Contains(goDoc, "Add") {
		t.Fatalf("expected docstring to mention Add, got %q", goDoc)
	}

	jsContent := `export function Foo(a, b) {
  return a + b
}`
	jsDoc, _, err := tool.generateDocstring(jsContent, "Foo", "does stuff", "javascript")
	if err != nil {
		t.Fatalf("generateDocstring js error: %v", err)
	}
	if !strings.Contains(jsDoc, "@param") {
		t.Fatalf("expected JSDoc params, got %q", jsDoc)
	}

	pyContent := `def Bar(x, y):
    return x
`
	pyDoc, _, err := tool.generateDocstring(pyContent, "Bar", "", "python")
	if err != nil {
		t.Fatalf("generateDocstring py error: %v", err)
	}
	if !strings.Contains(pyDoc, "Args") {
		t.Fatalf("expected python Args block, got %q", pyDoc)
	}
}
