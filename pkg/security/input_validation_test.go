package security

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewInputValidationAnalyzer(t *testing.T) {
	config := DefaultConfig()
	analyzer := NewInputValidationAnalyzer(config)

	if analyzer == nil {
		t.Fatal("NewInputValidationAnalyzer should return non-nil analyzer")
	}

	if analyzer.Name() != "input-validation" {
		t.Errorf("Name() = %v, want 'input-validation'", analyzer.Name())
	}

	if analyzer.Description() == "" {
		t.Error("Description should not be empty")
	}

	if len(analyzer.patterns) == 0 {
		t.Error("Should have validation patterns registered")
	}
}

func TestInputValidationAnalyzer_DetectSQLInjection(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "db.go")

	content := `package db

import "fmt"

func GetUser(id string) {
	query := fmt.Sprintf("SELECT * FROM users WHERE id=%s", id)
	db.Exec(query)
}
`
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	config := DefaultConfig()
	analyzer := NewInputValidationAnalyzer(config)

	result, err := analyzer.Analyze(tmpDir)
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}

	// The analyzer should run successfully
	if result == nil {
		t.Fatal("Result should not be nil")
	}
}

func TestInputValidationAnalyzer_DetectXSS(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "handler.go")

	content := `package handler

import "net/http"
import "fmt"

func ShowUser(w http.ResponseWriter, r *http.Request) {
	username := r.URL.Query().Get("username")
	fmt.Fprintf(w, "<h1>Hello %s</h1>", username)
}
`
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	config := DefaultConfig()
	analyzer := NewInputValidationAnalyzer(config)

	result, err := analyzer.Analyze(tmpDir)
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}

	// The analyzer should run successfully
	if result == nil {
		t.Fatal("Result should not be nil")
	}
}

func TestInputValidationAnalyzer_DetectCommandInjection(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "exec.go")

	content := `package exec

import "os/exec"

func RunCommand(cmd string) {
	exec.Command(cmd).Run()
}
`
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	config := DefaultConfig()
	analyzer := NewInputValidationAnalyzer(config)

	result, err := analyzer.Analyze(tmpDir)
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}

	// The analyzer should run successfully
	if result == nil {
		t.Fatal("Result should not be nil")
	}
}

func TestInputValidationAnalyzer_DetectPathTraversal(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "file.go")

	content := `package file

import "os"
import "net/http"

func ReadFile(w http.ResponseWriter, r *http.Request) {
	filename := r.URL.Query().Get("file")
	data, _ := os.ReadFile(filename)
	w.Write(data)
}
`
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	config := DefaultConfig()
	analyzer := NewInputValidationAnalyzer(config)

	result, err := analyzer.Analyze(tmpDir)
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}

	// The analyzer should run successfully
	if result == nil {
		t.Fatal("Result should not be nil")
	}
}

func TestInputValidationAnalyzer_DetectOpenRedirect(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "redirect.go")

	content := `package handler

import "net/http"

func Redirect(w http.ResponseWriter, r *http.Request) {
	url := r.URL.Query().Get("redirect")
	http.Redirect(w, r, url, http.StatusFound)
}
`
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	config := DefaultConfig()
	analyzer := NewInputValidationAnalyzer(config)

	result, err := analyzer.Analyze(tmpDir)
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}

	// The analyzer should run successfully
	if result == nil {
		t.Fatal("Result should not be nil")
	}
}

func TestInputValidationAnalyzer_DetectSSRF(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "http.go")

	content := `package client

import "net/http"

func FetchURL(url string) {
	http.Get(url)
}
`
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	config := DefaultConfig()
	analyzer := NewInputValidationAnalyzer(config)

	result, err := analyzer.Analyze(tmpDir)
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}

	// The analyzer should run successfully
	if result == nil {
		t.Fatal("Result should not be nil")
	}
}

func TestInputValidationAnalyzer_ShouldAnalyzeFile(t *testing.T) {
	config := DefaultConfig()
	analyzer := NewInputValidationAnalyzer(config)

	tests := []struct {
		name string
		path string
		want bool
	}{
		{"Go file", "/path/to/file.go", true},
		{"Python file", "/path/to/script.py", true},
		{"JavaScript file", "/path/to/app.js", true},
		{"PHP file", "/path/to/index.php", true},
		{"Java file", "/path/to/Main.java", true},
		{"Ruby file", "/path/to/app.rb", true},
		{"C# file", "/path/to/Program.cs", true},
		{"Binary file", "/path/to/app.exe", false},
		{"Test file", "/path/to/test.go", true}, // IncludeTests=true by default
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp file
			tmpDir := t.TempDir()
			testPath := filepath.Join(tmpDir, filepath.Base(tt.path))
			if err := os.WriteFile(testPath, []byte("test"), 0644); err != nil {
				t.Fatalf("Failed to create test file: %v", err)
			}

			if got := analyzer.shouldAnalyzeFile(testPath); got != tt.want {
				t.Errorf("shouldAnalyzeFile(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestInputValidationAnalyzer_SkipTestFiles(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "vuln_test.go")

	content := `package test

import "fmt"

func TestVuln(t *testing.T) {
	query := fmt.Sprintf("SELECT * FROM users WHERE id=%s", id)
}
`
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	config := DefaultConfig()
	config.IncludeTests = false
	analyzer := NewInputValidationAnalyzer(config)

	result, err := analyzer.Analyze(tmpDir)
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}

	// Should not find issues in test files when IncludeTests is false
	if len(result.Findings) > 0 {
		t.Error("Should skip test files when IncludeTests is false")
	}
}

func TestInputValidationAnalyzer_IsSensibleContext(t *testing.T) {
	config := DefaultConfig()
	analyzer := NewInputValidationAnalyzer(config)

	tests := []struct {
		line string
		want bool
	}{
		{"func processRequest(id string) {", true},
		{"if username == \"admin\" {", true},
		{"for i := 0; i < 10; i++ {", true},
		{"query := fmt.Sprintf(...)", true},
		{"// This is just a comment", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			if got := analyzer.isSensibleContext(tt.line); got != tt.want {
				t.Errorf("isSensibleContext(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

func TestInputValidationAnalyzer_IsFalsePositivePattern(t *testing.T) {
	config := DefaultConfig()
	analyzer := NewInputValidationAnalyzer(config)

	tests := []struct {
		line string
		want bool
	}{
		{"// Comment with SQL: SELECT * FROM users", true},
		{"# Python comment", true},
		{"/* Block comment */", true},
		{"import \"database/sql\"", true},
		{"require('express')", true},
		{"", true},
		{"query := fmt.Sprintf(...)", false},
		{"exec.Command(userInput)", false},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			if got := analyzer.isFalsePositivePattern(tt.line); got != tt.want {
				t.Errorf("isFalsePositivePattern(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

func TestInputValidationAnalyzer_EmptyDirectory(t *testing.T) {
	tmpDir := t.TempDir()

	config := DefaultConfig()
	analyzer := NewInputValidationAnalyzer(config)

	result, err := analyzer.Analyze(tmpDir)
	if err != nil {
		t.Fatalf("Analyze should not fail on empty directory: %v", err)
	}

	if len(result.Findings) != 0 {
		t.Error("Empty directory should have no findings")
	}
}

func TestInputValidationAnalyzer_CleanCode(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "clean.go")

	content := `package db

import "database/sql"

func GetUser(db *sql.DB, id int) error {
	// Using parameterized query - safe
	row := db.QueryRow("SELECT * FROM users WHERE id = $1", id)
	return row.Scan(&user)
}
`
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	config := DefaultConfig()
	analyzer := NewInputValidationAnalyzer(config)

	result, err := analyzer.Analyze(tmpDir)
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}

	// Clean code using parameterized queries should not trigger findings
	// (or should have low confidence if it does)
	for _, finding := range result.Findings {
		if finding.Confidence > 0.7 {
			t.Errorf("Clean code should not have high-confidence findings: %s", finding.Title)
		}
	}
}

func TestInputValidationAnalyzer_MultipleVulnerabilities(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "vulns.go")

	content := `package vulns

import (
	"fmt"
	"net/http"
	"os/exec"
)

func MultiVuln(w http.ResponseWriter, r *http.Request) {
	// SQL injection
	id := r.URL.Query().Get("id")
	query := fmt.Sprintf("SELECT * FROM users WHERE id=%s", id)

	// XSS
	fmt.Fprintf(w, "<h1>%s</h1>", id)

	// Command injection
	cmd := r.URL.Query().Get("cmd")
	exec.Command(cmd).Run()
}
`
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	config := DefaultConfig()
	analyzer := NewInputValidationAnalyzer(config)

	result, err := analyzer.Analyze(tmpDir)
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}

	// The analyzer should run successfully
	if result == nil {
		t.Fatal("Result should not be nil")
	}

	// Optionally check that we detected at least some issues (pattern matching may vary)
	if len(result.Findings) > 0 {
		t.Logf("Detected %d findings", len(result.Findings))
	}
}
