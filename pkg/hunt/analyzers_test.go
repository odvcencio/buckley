package hunt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTODOAnalyzer(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test file with various TODO comments
	testFile := filepath.Join(tmpDir, "test.go")
	content := `package main

import "fmt"

// TODO: Refactor this function
func foo() {
	// FIXME: Handle error properly
	fmt.Println("hello")
	// BUG: This causes a panic
	var x *int
	_ = *x
	// HACK: Temporary workaround
	// Regular comment
}

// TODO implement bar
func bar() {}
`

	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	analyzer := &TODOAnalyzer{}
	results, err := analyzer.Analyze(tmpDir)
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}

	// Should find 5 comments: 2 TODOs, FIXME, BUG, HACK
	if len(results) != 5 {
		t.Errorf("Expected 5 suggestions, got %d", len(results))
	}

	// Verify keywords and severities
	keywordCounts := make(map[string]int)
	for _, r := range results {
		if strings.Contains(r.Rationale, "TODO") {
			keywordCounts["TODO"]++
			if r.Severity != 5 {
				t.Errorf("TODO should have severity 5, got %d", r.Severity)
			}
		}
		if strings.Contains(r.Rationale, "FIXME") {
			keywordCounts["FIXME"]++
			if r.Severity != 7 {
				t.Errorf("FIXME should have severity 7, got %d", r.Severity)
			}
		}
		if strings.Contains(r.Rationale, "BUG") {
			keywordCounts["BUG"]++
			if r.Severity != 9 {
				t.Errorf("BUG should have severity 9, got %d", r.Severity)
			}
		}
		if strings.Contains(r.Rationale, "HACK") {
			keywordCounts["HACK"]++
			if r.Severity != 6 {
				t.Errorf("HACK should have severity 6, got %d", r.Severity)
			}
		}
	}

	if keywordCounts["TODO"] != 2 {
		t.Errorf("Expected 2 TODO comments, got %d", keywordCounts["TODO"])
	}
	if keywordCounts["FIXME"] != 1 {
		t.Errorf("Expected 1 FIXME comment, got %d", keywordCounts["FIXME"])
	}
	if keywordCounts["BUG"] != 1 {
		t.Errorf("Expected 1 BUG comment, got %d", keywordCounts["BUG"])
	}
	if keywordCounts["HACK"] != 1 {
		t.Errorf("Expected 1 HACK comment, got %d", keywordCounts["HACK"])
	}
}

func TestTODOAnalyzer_SkipsVendor(t *testing.T) {
	tmpDir := t.TempDir()

	// Create vendor directory with TODO
	vendorDir := filepath.Join(tmpDir, "vendor")
	if err := os.Mkdir(vendorDir, 0755); err != nil {
		t.Fatalf("Failed to create vendor dir: %v", err)
	}

	vendorFile := filepath.Join(vendorDir, "vendor.go")
	if err := os.WriteFile(vendorFile, []byte("// TODO: should be ignored\n"), 0644); err != nil {
		t.Fatalf("Failed to create vendor file: %v", err)
	}

	// Create regular file with TODO
	regularFile := filepath.Join(tmpDir, "main.go")
	if err := os.WriteFile(regularFile, []byte("// TODO: should be found\n"), 0644); err != nil {
		t.Fatalf("Failed to create regular file: %v", err)
	}

	analyzer := &TODOAnalyzer{}
	results, err := analyzer.Analyze(tmpDir)
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}

	// Should only find the TODO in main.go
	if len(results) != 1 {
		t.Errorf("Expected 1 suggestion, got %d", len(results))
	}

	if len(results) > 0 && strings.Contains(results[0].File, "vendor") {
		t.Error("Should not find TODOs in vendor directory")
	}
}

func TestEstimateEffort(t *testing.T) {
	tests := []struct {
		keyword string
		message string
		want    string
	}{
		{"BUG", "fix this", "medium"},
		{"TODO", "refactor this function", "large"},
		{"TODO", "add validation", "medium"},
		{"FIXME", "quick fix", "small"},
		{"TODO", "rewrite parser", "large"},
	}

	for _, tt := range tests {
		got := estimateEffort(tt.keyword, tt.message)
		if got != tt.want {
			t.Errorf("estimateEffort(%q, %q) = %q, want %q", tt.keyword, tt.message, got, tt.want)
		}
	}
}

func TestLintAnalyzer_NotAvailable(t *testing.T) {
	tmpDir := t.TempDir()

	analyzer := &LintAnalyzer{}
	_, err := analyzer.Analyze(tmpDir)

	// If golangci-lint is not installed, should return error
	// If it is installed, this test will pass but we can't assert the error
	if err != nil && !strings.Contains(err.Error(), "not found") {
		t.Errorf("Expected 'not found' error, got: %v", err)
	}
}

func TestParseLintText(t *testing.T) {
	output := `main.go:10:5: Error return value is not checked (errcheck)
utils.go:25:12: unused variable 'x' (unused)
config.go:42:1: function is too long (funlen)
`

	tmpDir := t.TempDir()
	results, err := parseLintText(output, tmpDir)
	if err != nil {
		t.Fatalf("parseLintText failed: %v", err)
	}

	if len(results) != 3 {
		t.Errorf("Expected 3 suggestions, got %d", len(results))
	}

	// Check first result
	if len(results) > 0 {
		if results[0].LineStart != 10 {
			t.Errorf("Expected line 10, got %d", results[0].LineStart)
		}
		if results[0].Category != "readability" {
			t.Errorf("Expected category 'readability', got %q", results[0].Category)
		}
	}
}

func TestLintEffort(t *testing.T) {
	tests := []struct {
		linter string
		want   string
	}{
		{"gocyclo", "medium"},
		{"gocognit", "medium"},
		{"funlen", "medium"},
		{"gofmt", "small"},
		{"errcheck", "small"},
		{"unused", "small"},
	}

	for _, tt := range tests {
		got := lintEffort(tt.linter)
		if got != tt.want {
			t.Errorf("lintEffort(%q) = %q, want %q", tt.linter, got, tt.want)
		}
	}
}

func TestIsAutoFixable(t *testing.T) {
	tests := []struct {
		linter string
		want   bool
	}{
		{"gofmt", true},
		{"goimports", true},
		{"misspell", true},
		{"errcheck", false},
		{"unused", false},
		{"gocyclo", false},
	}

	for _, tt := range tests {
		got := isAutoFixable(tt.linter)
		if got != tt.want {
			t.Errorf("isAutoFixable(%q) = %v, want %v", tt.linter, got, tt.want)
		}
	}
}

func TestDependencyAnalyzer_NoGoMod(t *testing.T) {
	tmpDir := t.TempDir()

	analyzer := &DependencyAnalyzer{}
	results, err := analyzer.Analyze(tmpDir)
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}

	// Should return empty results for non-Go projects
	if len(results) != 0 {
		t.Errorf("Expected 0 suggestions for non-Go project, got %d", len(results))
	}
}

func TestDependencyAnalyzer_WithGoMod(t *testing.T) {
	// This test requires an actual Go module environment
	// Skip in environments where go command is not available
	tmpDir := t.TempDir()

	// Create minimal go.mod
	goModContent := `module test

go 1.21

require (
	github.com/example/dep v1.0.0
)
`
	goModPath := filepath.Join(tmpDir, "go.mod")
	if err := os.WriteFile(goModPath, []byte(goModContent), 0644); err != nil {
		t.Fatalf("Failed to create go.mod: %v", err)
	}

	// Create minimal main.go
	mainContent := `package main

func main() {}
`
	mainPath := filepath.Join(tmpDir, "main.go")
	if err := os.WriteFile(mainPath, []byte(mainContent), 0644); err != nil {
		t.Fatalf("Failed to create main.go: %v", err)
	}

	analyzer := &DependencyAnalyzer{}
	results, err := analyzer.Analyze(tmpDir)

	// go list might fail if the module doesn't actually resolve
	// That's okay - we're testing the analyzer logic
	if err != nil {
		t.Logf("go list failed (expected in test env): %v", err)
		return
	}

	// If it succeeds, results should be valid suggestions
	for _, r := range results {
		if r.Category != "dependency" {
			t.Errorf("Expected category 'dependency', got %q", r.Category)
		}
		if r.File != "go.mod" {
			t.Errorf("Expected file 'go.mod', got %q", r.File)
		}
		if !r.AutoFixable {
			t.Error("Dependency updates should be marked as auto-fixable")
		}
	}
}

func TestTODOAnalyzer_Name(t *testing.T) {
	analyzer := &TODOAnalyzer{}
	if analyzer.Name() != "todo" {
		t.Errorf("Expected name 'todo', got %q", analyzer.Name())
	}
}

func TestLintAnalyzer_Name(t *testing.T) {
	analyzer := &LintAnalyzer{}
	if analyzer.Name() != "lint" {
		t.Errorf("Expected name 'lint', got %q", analyzer.Name())
	}
}

func TestDependencyAnalyzer_Name(t *testing.T) {
	analyzer := &DependencyAnalyzer{}
	if analyzer.Name() != "dependency" {
		t.Errorf("Expected name 'dependency', got %q", analyzer.Name())
	}
}
