package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditFileTool(t *testing.T) {
	tool := &EditFileTool{}

	if tool.Name() != "edit_file" {
		t.Errorf("Name() = %v, want edit_file", tool.Name())
	}

	if tool.Description() == "" {
		t.Error("Description() should not be empty")
	}

	params := tool.Parameters()
	if params.Type != "object" {
		t.Errorf("Parameters().Type = %v, want object", params.Type)
	}
}

func TestEditFileTool_Execute(t *testing.T) {
	tool := &EditFileTool{}

	// Create temp file
	tmpDir, err := os.MkdirTemp("", "buckley-edit-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	testFile := filepath.Join(tmpDir, "test.go")
	originalContent := `package main

func hello() {
	println("hello")
}

func world() {
	println("world")
}
`
	if err := os.WriteFile(testFile, []byte(originalContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Test single replacement
	result, err := tool.Execute(map[string]any{
		"path":       testFile,
		"old_string": `println("hello")`,
		"new_string": `fmt.Println("hello")`,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !result.Success {
		t.Errorf("Execute() Success = false, error = %v", result.Error)
	}

	content, _ := os.ReadFile(testFile)
	if !strings.Contains(string(content), `fmt.Println("hello")`) {
		t.Error("File should contain new string")
	}
	if strings.Contains(string(content), `println("hello")`) {
		t.Error("File should not contain old string")
	}

	// Test that diff preview is returned
	if result.DiffPreview == nil {
		t.Error("DiffPreview should not be nil")
	}
	if result.DiffPreview.LinesAdded == 0 && result.DiffPreview.LinesRemoved == 0 {
		t.Error("DiffPreview should show line changes")
	}
}

func TestEditFileTool_Execute_NotFound(t *testing.T) {
	tool := &EditFileTool{}

	tmpDir, err := os.MkdirTemp("", "buckley-edit-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("hello world"), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := tool.Execute(map[string]any{
		"path":       testFile,
		"old_string": "not found",
		"new_string": "replacement",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.Success {
		t.Error("Execute() should fail when old_string not found")
	}
	if !strings.Contains(result.Error, "not found in file") {
		t.Errorf("Error should indicate string not found, got: %v", result.Error)
	}
}

func TestEditFileTool_Execute_NotUnique(t *testing.T) {
	tool := &EditFileTool{}

	tmpDir, err := os.MkdirTemp("", "buckley-edit-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("hello hello hello"), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := tool.Execute(map[string]any{
		"path":       testFile,
		"old_string": "hello",
		"new_string": "hi",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.Success {
		t.Error("Execute() should fail when old_string appears multiple times")
	}
	if !strings.Contains(result.Error, "appears 3 times") {
		t.Errorf("Error should indicate multiple occurrences, got: %v", result.Error)
	}
}

func TestEditFileTool_Execute_ReplaceAll(t *testing.T) {
	tool := &EditFileTool{}

	tmpDir, err := os.MkdirTemp("", "buckley-edit-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("hello hello hello"), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := tool.Execute(map[string]any{
		"path":        testFile,
		"old_string":  "hello",
		"new_string":  "hi",
		"replace_all": true,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !result.Success {
		t.Errorf("Execute() Success = false, error = %v", result.Error)
	}

	content, _ := os.ReadFile(testFile)
	if string(content) != "hi hi hi" {
		t.Errorf("Content = %q, want %q", string(content), "hi hi hi")
	}
}

func TestInsertTextTool(t *testing.T) {
	tool := &InsertTextTool{}

	if tool.Name() != "insert_text" {
		t.Errorf("Name() = %v, want insert_text", tool.Name())
	}

	tmpDir, err := os.MkdirTemp("", "buckley-insert-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("line1\nline2\nline3"), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := tool.Execute(map[string]any{
		"path": testFile,
		"line": 2,
		"text": "inserted",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !result.Success {
		t.Errorf("Execute() Success = false, error = %v", result.Error)
	}

	content, _ := os.ReadFile(testFile)
	lines := strings.Split(string(content), "\n")
	if len(lines) != 4 {
		t.Errorf("Should have 4 lines, got %d", len(lines))
	}
	if lines[1] != "inserted" {
		t.Errorf("Line 2 = %q, want %q", lines[1], "inserted")
	}
}

func TestDeleteLinesTool(t *testing.T) {
	tool := &DeleteLinesTool{}

	if tool.Name() != "delete_lines" {
		t.Errorf("Name() = %v, want delete_lines", tool.Name())
	}

	tmpDir, err := os.MkdirTemp("", "buckley-delete-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("line1\nline2\nline3\nline4\nline5"), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := tool.Execute(map[string]any{
		"path":       testFile,
		"start_line": 2,
		"end_line":   4,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !result.Success {
		t.Errorf("Execute() Success = false, error = %v", result.Error)
	}

	content, _ := os.ReadFile(testFile)
	lines := strings.Split(string(content), "\n")
	if len(lines) != 2 {
		t.Errorf("Should have 2 lines, got %d", len(lines))
	}
	if lines[0] != "line1" || lines[1] != "line5" {
		t.Errorf("Content = %v, want [line1, line5]", lines)
	}
}

func TestGenerateDiff(t *testing.T) {
	oldContent := "line1\nline2\nline3\n"
	newContent := "line1\nmodified\nline3\n"

	diff := generateDiff("test.txt", oldContent, newContent)

	if diff == nil {
		t.Fatal("generateDiff returned nil")
	}

	if diff.FilePath != "test.txt" {
		t.Errorf("FilePath = %v, want test.txt", diff.FilePath)
	}

	if diff.LinesAdded == 0 || diff.LinesRemoved == 0 {
		t.Error("Should detect added and removed lines")
	}

	if diff.UnifiedDiff == "" {
		t.Error("UnifiedDiff should not be empty")
	}

	if !strings.Contains(diff.UnifiedDiff, "--- test.txt") {
		t.Error("UnifiedDiff should contain header")
	}
}

func TestGenerateDiff_NewFile(t *testing.T) {
	newContent := "new file content\n"

	diff := generateDiff("new.txt", "", newContent)

	if !diff.IsNew {
		t.Error("IsNew should be true for empty old content")
	}

	if diff.LinesAdded == 0 {
		t.Error("LinesAdded should be > 0 for new file")
	}
}

func TestContainsAt(t *testing.T) {
	tests := []struct {
		name   string
		lines  []string
		target string
		want   bool
	}{
		{
			name:   "empty lines",
			lines:  nil,
			target: "foo",
			want:   false,
		},
		{
			name:   "target found",
			lines:  []string{"foo", "bar", "baz"},
			target: "bar",
			want:   true,
		},
		{
			name:   "target not found",
			lines:  []string{"foo", "bar", "baz"},
			target: "qux",
			want:   false,
		},
		{
			name:   "exact match required",
			lines:  []string{"foobar"},
			target: "foo",
			want:   false,
		},
		{
			name:   "empty target",
			lines:  []string{"foo", "", "bar"},
			target: "",
			want:   true,
		},
		{
			name:   "first element",
			lines:  []string{"first", "second"},
			target: "first",
			want:   true,
		},
		{
			name:   "last element",
			lines:  []string{"first", "last"},
			target: "last",
			want:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := containsAt(tc.lines, tc.target)
			if got != tc.want {
				t.Errorf("containsAt(%v, %q) = %v, want %v", tc.lines, tc.target, got, tc.want)
			}
		})
	}
}

func TestPluralize(t *testing.T) {
	tests := []struct {
		count int
		want  string
	}{
		{count: 0, want: "s"},
		{count: 1, want: ""},
		{count: 2, want: "s"},
		{count: 5, want: "s"},
		{count: 100, want: "s"},
		{count: -1, want: "s"},
	}

	for _, tc := range tests {
		got := pluralize(tc.count)
		if got != tc.want {
			t.Errorf("pluralize(%d) = %q, want %q", tc.count, got, tc.want)
		}
	}
}
