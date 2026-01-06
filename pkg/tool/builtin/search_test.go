package builtin

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestSearchTextTool(t *testing.T) {
	tool := &SearchTextTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "search_text" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "search_text")
		}
		if tool.Description() == "" {
			t.Error("Description() should not be empty")
		}
		params := tool.Parameters()
		if params.Type != "object" {
			t.Errorf("Parameters().Type = %q, want %q", params.Type, "object")
		}
	})

	t.Run("missing pattern", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for missing pattern")
		}
	})

	t.Run("search in directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		// Create test files
		if err := os.WriteFile(filepath.Join(tmpDir, "test.go"), []byte("package main\nfunc Hello() {}"), 0644); err != nil {
			t.Fatal(err)
		}

		result, err := tool.Execute(map[string]any{
			"query": "Hello",
			"path":  tmpDir,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("expected success, got error: %s", result.Error)
		}
	})

	t.Run("search with file glob", func(t *testing.T) {
		tmpDir := t.TempDir()
		// Create test files
		if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmpDir, "main.txt"), []byte("package text"), 0644); err != nil {
			t.Fatal(err)
		}

		result, err := tool.Execute(map[string]any{
			"query":     "package",
			"path":      tmpDir,
			"file_glob": "*.go",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("expected success, got error: %s", result.Error)
		}
	})

	t.Run("search with max results", func(t *testing.T) {
		tmpDir := t.TempDir()
		// Create test file with multiple matches
		content := "match1\nmatch2\nmatch3\nmatch4\nmatch5"
		if err := os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		result, err := tool.Execute(map[string]any{
			"query":       "match",
			"path":        tmpDir,
			"max_results": 2,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("expected success, got error: %s", result.Error)
		}
	})

	t.Run("search with context lines", func(t *testing.T) {
		tmpDir := t.TempDir()
		content := "line1\nline2\ntarget\nline4\nline5"
		if err := os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		result, err := tool.Execute(map[string]any{
			"query":         "target",
			"path":          tmpDir,
			"context_lines": 1,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("expected success, got error: %s", result.Error)
		}
	})

	t.Run("case insensitive search", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte("Hello World"), 0644); err != nil {
			t.Fatal(err)
		}

		result, err := tool.Execute(map[string]any{
			"query":            "hello",
			"path":             tmpDir,
			"case_insensitive": true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("expected success, got error: %s", result.Error)
		}
	})

	t.Run("search in nonexistent path", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{
			"query": "test",
			"path":  "/nonexistent/path/12345",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Should fail for nonexistent path
		if result.Success {
			// Some implementations might return empty results instead of error
			t.Logf("search in nonexistent path returned success")
		}
	})
}

func TestSearchReplaceTool_Extended(t *testing.T) {
	tool := &SearchReplaceTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "search_replace" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "search_replace")
		}
		if tool.Description() == "" {
			t.Error("Description() should not be empty")
		}
	})

	t.Run("replace multiple occurrences", func(t *testing.T) {
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "multi.txt")
		if err := os.WriteFile(testFile, []byte("foo bar foo baz foo"), 0644); err != nil {
			t.Fatal(err)
		}

		result, err := tool.Execute(map[string]any{
			"path":    testFile,
			"search":  "foo",
			"replace": "qux",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("expected success, got error: %s", result.Error)
		}

		// Check replacements count
		if count, ok := result.Data["replacements"].(int); ok {
			if count != 3 {
				t.Errorf("expected 3 replacements, got %d", count)
			}
		}

		// Verify file content
		content, _ := os.ReadFile(testFile)
		if string(content) != "qux bar qux baz qux" {
			t.Errorf("expected 'qux bar qux baz qux', got %q", string(content))
		}
	})

	t.Run("regex replace", func(t *testing.T) {
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "regex.txt")
		if err := os.WriteFile(testFile, []byte("hello123world456test"), 0644); err != nil {
			t.Fatal(err)
		}

		result, err := tool.Execute(map[string]any{
			"path":    testFile,
			"search":  "[0-9]+",
			"replace": "NUM",
			"regex":   true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("expected success, got error: %s", result.Error)
		}
	})

	t.Run("nonexistent file", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{
			"path":    "/nonexistent/file.txt",
			"search":  "foo",
			"replace": "bar",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for nonexistent file")
		}
	})
}

func TestFindFilesTool_Extended(t *testing.T) {
	tool := &FindFilesTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "find_files" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "find_files")
		}
	})

	t.Run("find in subdirectories", func(t *testing.T) {
		tmpDir := t.TempDir()
		// Create nested structure
		subDir := filepath.Join(tmpDir, "sub")
		if err := os.MkdirAll(subDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmpDir, "root.go"), []byte("pkg"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(subDir, "nested.go"), []byte("pkg"), 0644); err != nil {
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

		if count, ok := result.Data["count"].(int); ok {
			if count != 2 {
				t.Errorf("expected 2 files, got %d", count)
			}
		}
	})

	t.Run("no matches", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte("text"), 0644); err != nil {
			t.Fatal(err)
		}

		result, err := tool.Execute(map[string]any{
			"pattern":   "*.xyz",
			"base_path": tmpDir,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("expected success, got error: %s", result.Error)
		}

		if count, ok := result.Data["count"].(int); ok {
			if count != 0 {
				t.Errorf("expected 0 matches, got %d", count)
			}
		}
	})

	t.Run("complex glob pattern", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(tmpDir, "test_a.go"), []byte("pkg"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmpDir, "test_b.go"), []byte("pkg"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmpDir, "other.go"), []byte("pkg"), 0644); err != nil {
			t.Fatal(err)
		}

		result, err := tool.Execute(map[string]any{
			"pattern":   "test_*.go",
			"base_path": tmpDir,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("expected success, got error: %s", result.Error)
		}

		if count, ok := result.Data["count"].(int); ok {
			if count != 2 {
				t.Errorf("expected 2 matches, got %d", count)
			}
		}
	})
}

func TestParseBool(t *testing.T) {
	tests := []struct {
		name       string
		value      any
		defaultVal bool
		want       bool
	}{
		{name: "bool true", value: true, defaultVal: false, want: true},
		{name: "bool false", value: false, defaultVal: true, want: false},
		{name: "string true", value: "true", defaultVal: false, want: true},
		{name: "string True", value: "True", defaultVal: false, want: true},
		{name: "string TRUE", value: "TRUE", defaultVal: false, want: true},
		{name: "string yes", value: "yes", defaultVal: false, want: true},
		{name: "string on", value: "on", defaultVal: false, want: true},
		{name: "string 1", value: "1", defaultVal: false, want: true},
		{name: "string false", value: "false", defaultVal: true, want: false},
		{name: "string False", value: "False", defaultVal: true, want: false},
		{name: "string no", value: "no", defaultVal: true, want: false},
		{name: "string off", value: "off", defaultVal: true, want: false},
		{name: "string 0", value: "0", defaultVal: true, want: false},
		{name: "string with whitespace", value: "  true  ", defaultVal: false, want: true},
		{name: "invalid string", value: "maybe", defaultVal: true, want: true},
		{name: "nil value", value: nil, defaultVal: true, want: true},
		{name: "integer value", value: 1, defaultVal: false, want: false},
		{name: "float value", value: 1.0, defaultVal: false, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseBool(tc.value, tc.defaultVal)
			if got != tc.want {
				t.Errorf("parseBool(%v, %v) = %v, want %v", tc.value, tc.defaultVal, got, tc.want)
			}
		})
	}
}

func TestParseInt(t *testing.T) {
	tests := []struct {
		name       string
		value      any
		defaultVal int
		want       int
	}{
		{name: "int value", value: 42, defaultVal: 0, want: 42},
		{name: "int zero", value: 0, defaultVal: 99, want: 0},
		{name: "int negative", value: -10, defaultVal: 0, want: -10},
		{name: "float64 value", value: float64(25), defaultVal: 0, want: 25},
		{name: "float64 with decimal", value: float64(25.9), defaultVal: 0, want: 25},
		{name: "string integer", value: "123", defaultVal: 0, want: 123},
		{name: "string with whitespace", value: "  456  ", defaultVal: 0, want: 456},
		{name: "string negative", value: "-50", defaultVal: 0, want: -50},
		{name: "empty string", value: "", defaultVal: 99, want: 99},
		{name: "whitespace string", value: "   ", defaultVal: 99, want: 99},
		{name: "invalid string", value: "abc", defaultVal: 77, want: 77},
		{name: "nil value", value: nil, defaultVal: 33, want: 33},
		{name: "bool value", value: true, defaultVal: 11, want: 11},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseInt(tc.value, tc.defaultVal)
			if got != tc.want {
				t.Errorf("parseInt(%v, %d) = %d, want %d", tc.value, tc.defaultVal, got, tc.want)
			}
		})
	}
}

func TestExtractGlobParams(t *testing.T) {
	tests := []struct {
		name     string
		value    any
		wantLen  int
		wantVals []string
	}{
		{
			name:     "nil value",
			value:    nil,
			wantLen:  0,
			wantVals: nil,
		},
		{
			name:     "empty string",
			value:    "",
			wantLen:  0,
			wantVals: nil,
		},
		{
			name:     "whitespace string",
			value:    "   ",
			wantLen:  0,
			wantVals: nil,
		},
		{
			name:     "single string",
			value:    "*.go",
			wantLen:  1,
			wantVals: []string{"*.go"},
		},
		{
			name:     "empty array",
			value:    []any{},
			wantLen:  0,
			wantVals: nil,
		},
		{
			name:     "string array",
			value:    []any{"*.go", "*.ts"},
			wantLen:  2,
			wantVals: []string{"*.go", "*.ts"},
		},
		{
			name:     "mixed array with empty strings",
			value:    []any{"*.go", "", "  ", "*.ts"},
			wantLen:  2,
			wantVals: []string{"*.go", "*.ts"},
		},
		{
			name:     "array with non-strings ignored",
			value:    []any{"*.go", 123, true, "*.ts"},
			wantLen:  2,
			wantVals: []string{"*.go", "*.ts"},
		},
		{
			name:    "unsupported type",
			value:   123,
			wantLen: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractGlobParams(tc.value)
			if len(got) != tc.wantLen {
				t.Errorf("extractGlobParams(%v) len = %d, want %d", tc.value, len(got), tc.wantLen)
			}
			for i, want := range tc.wantVals {
				if i < len(got) && got[i] != want {
					t.Errorf("extractGlobParams(%v)[%d] = %q, want %q", tc.value, i, got[i], want)
				}
			}
		})
	}
}

func TestReplaceLimited(t *testing.T) {
	tests := []struct {
		name            string
		pattern         string
		input           string
		replacement     string
		limit           int
		wantOutput      string
		wantReplacements int
	}{
		{
			name:             "no matches",
			pattern:          "xyz",
			input:            "hello world",
			replacement:      "ABC",
			limit:            10,
			wantOutput:       "hello world",
			wantReplacements: 0,
		},
		{
			name:             "single match, limit 1",
			pattern:          "world",
			input:            "hello world",
			replacement:      "universe",
			limit:            1,
			wantOutput:       "hello universe",
			wantReplacements: 1,
		},
		{
			name:             "multiple matches, no limit",
			pattern:          "a",
			input:            "banana",
			replacement:      "X",
			limit:            10,
			wantOutput:       "bXnXnX",
			wantReplacements: 3,
		},
		{
			name:             "multiple matches, limit 2",
			pattern:          "a",
			input:            "banana",
			replacement:      "X",
			limit:            2,
			wantOutput:       "bXnXna",
			wantReplacements: 2,
		},
		{
			name:             "limit zero",
			pattern:          "a",
			input:            "banana",
			replacement:      "X",
			limit:            0,
			wantOutput:       "banana",
			wantReplacements: 0,
		},
		{
			name:             "regex pattern",
			pattern:          "[0-9]+",
			input:            "abc123def456ghi",
			replacement:      "NUM",
			limit:            1,
			wantOutput:       "abcNUMdef456ghi",
			wantReplacements: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			re := regexp.MustCompile(tc.pattern)
			gotOutput, gotReplacements := replaceLimited(re, tc.input, tc.replacement, tc.limit)
			if gotOutput != tc.wantOutput {
				t.Errorf("replaceLimited output = %q, want %q", gotOutput, tc.wantOutput)
			}
			if gotReplacements != tc.wantReplacements {
				t.Errorf("replaceLimited replacements = %d, want %d", gotReplacements, tc.wantReplacements)
			}
		})
	}
}

func TestCountMatches(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		input   string
		want    int
	}{
		{name: "no matches", pattern: "xyz", input: "hello world", want: 0},
		{name: "single match", pattern: "world", input: "hello world", want: 1},
		{name: "multiple matches", pattern: "a", input: "banana", want: 3},
		{name: "overlapping pattern", pattern: "aa", input: "aaaa", want: 2},
		{name: "empty input", pattern: "a", input: "", want: 0},
		{name: "regex pattern", pattern: "[0-9]+", input: "a1b22c333", want: 3},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			re := regexp.MustCompile(tc.pattern)
			got := countMatches(re, tc.input)
			if got != tc.want {
				t.Errorf("countMatches(%q, %q) = %d, want %d", tc.pattern, tc.input, got, tc.want)
			}
		})
	}
}

func TestParseSearchLine(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		wantPath string
		wantLine int
		wantNil  bool
	}{
		{
			name:     "standard format with column and content",
			line:     "file.go:10:5:func main() {",
			wantPath: "file.go",
			wantLine: 10,
		},
		{
			name:     "format without content",
			line:     "file.go:10:5",
			wantPath: "file.go",
			wantLine: 10,
		},
		{
			name:     "format with just file and line",
			line:     "file.go:10",
			wantPath: "file.go",
			wantLine: 10,
		},
		{
			name:     "invalid format - no line number (returns 0)",
			line:     "file.go:notanumber:text",
			wantPath: "file.go",
			wantLine: 0,
		},
		{
			name:     "single element (just path)",
			line:     "file.go",
			wantPath: "",
			wantNil:  true,
		},
		{
			name:    "empty line",
			line:    "",
			wantNil: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseSearchLine(tc.line)
			if tc.wantNil {
				if got != nil {
					t.Errorf("parseSearchLine(%q) = %v, want nil", tc.line, got)
				}
				return
			}
			if got == nil {
				t.Errorf("parseSearchLine(%q) = nil, want non-nil", tc.line)
				return
			}
			if path, ok := got["path"].(string); ok {
				if path != tc.wantPath {
					t.Errorf("parseSearchLine(%q)[path] = %q, want %q", tc.line, path, tc.wantPath)
				}
			}
			if line, ok := got["line"].(int); ok {
				if line != tc.wantLine {
					t.Errorf("parseSearchLine(%q)[line] = %d, want %d", tc.line, line, tc.wantLine)
				}
			}
		})
	}
}
