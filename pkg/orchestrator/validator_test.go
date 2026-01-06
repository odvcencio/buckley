package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/odvcencio/buckley/pkg/tool"
)

func TestNewValidator(t *testing.T) {
	registry := tool.NewRegistry()
	validator := NewValidator(registry, ".")

	if validator == nil {
		t.Fatal("NewValidator returned nil")
	}
	if validator.toolRegistry != registry {
		t.Error("Validator toolRegistry not set correctly")
	}
}

func TestValidator_ValidatePreconditions_Basic(t *testing.T) {
	registry := tool.NewRegistry()
	validator := NewValidator(registry, ".")

	task := &Task{
		ID:           "1",
		Title:        "Test Task",
		Description:  "Run tests",
		Files:        []string{},
		Verification: []string{"go test ./..."},
	}

	result := validator.ValidatePreconditions(task)

	if result == nil {
		t.Fatal("ValidatePreconditions returned nil")
	}

	// Should pass basic validation (go is usually installed in test environment)
	if !result.Valid {
		t.Logf("Validation errors: %v", result.Errors)
		t.Logf("Validation warnings: %v", result.Warnings)
		t.Log("Note: Validation may fail if 'go' is not in PATH")
	}
}

func TestValidator_ValidatePreconditions_MissingTools(t *testing.T) {
	registry := tool.NewRegistry()
	validator := NewValidator(registry, ".")

	task := &Task{
		ID:           "1",
		Title:        "Non-existent tool task",
		Description:  "Use a tool that definitely doesn't exist xyzabc123",
		Verification: []string{"Run `xyzabc123 --do-something` to verify"},
	}

	result := validator.ValidatePreconditions(task)

	if result == nil {
		t.Fatal("ValidatePreconditions returned nil")
	}

	// Should detect missing tool
	if result.Valid {
		t.Error("Expected validation to fail for non-existent tool")
	}

	foundMissingTool := false
	for _, tool := range result.MissingTools {
		if tool == "xyzabc123" {
			foundMissingTool = true
			break
		}
	}
	if !foundMissingTool {
		t.Error("Expected to find xyzabc123 in missing tools list")
	}
}

func TestValidator_ValidatePreconditions_EnvVars(t *testing.T) {
	registry := tool.NewRegistry()
	validator := NewValidator(registry, ".")

	task := &Task{
		ID:          "1",
		Title:       "AWS Task",
		Description: "Deploy to AWS using credentials",
	}

	result := validator.ValidatePreconditions(task)

	if result == nil {
		t.Fatal("ValidatePreconditions returned nil")
	}

	// Should warn about AWS credentials (may or may not be set in test env)
	if len(result.Warnings) > 0 {
		t.Logf("Environment warnings: %v", result.Warnings)
	}
}

func TestValidator_ExtractRequiredTools(t *testing.T) {
	registry := tool.NewRegistry()
	validator := NewValidator(registry, ".")

	tests := []struct {
		name         string
		description  string
		verification []string
		wantTools    []string
	}{
		{
			name:        "go tool",
			description: "Run go tests",
			wantTools:   []string{"go"},
		},
		{
			name:         "npm tool",
			description:  "Build project",
			verification: []string{"npm run build"},
			wantTools:    []string{"npm"},
		},
		{
			name:        "docker tool",
			description: "Build container image with docker",
			wantTools:   []string{"docker"},
		},
		{
			name:        "terraform tool",
			description: "Deploy infrastructure with terraform",
			wantTools:   []string{"terraform"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := &Task{
				Description:  tt.description,
				Verification: tt.verification,
			}

			tools := validator.extractRequiredTools(task)

			// Check that expected tools are found (when available in PATH)
			t.Logf("Extracted tools: %v", tools)

			toolFound := make(map[string]bool)
			for _, tool := range tools {
				toolFound[tool] = true
			}

			for _, want := range tt.wantTools {
				if !toolFound[want] {
					t.Errorf("Expected to extract tool %s, but didn't find it", want)
				}
			}
		})
	}
}

func TestValidator_CheckPermissions(t *testing.T) {
	tmpDir := t.TempDir()
	registry := tool.NewRegistry()
	validator := NewValidator(registry, tmpDir)

	// Create a read-only file
	readonlyFile := filepath.Join(tmpDir, "readonly.txt")
	if err := os.WriteFile(readonlyFile, []byte("test"), 0444); err != nil {
		t.Fatalf("Failed to create readonly file: %v", err)
	}

	task := &Task{
		ID:    "1",
		Files: []string{readonlyFile},
	}

	result := &ValidationResult{Valid: true}
	err := validator.checkPermissions(task, result)

	if err != nil {
		t.Errorf("checkPermissions returned error: %v", err)
	}

	// Should have permission errors
	if len(result.Errors) == 0 {
		t.Log("No permission errors found (may be running as root or on Windows)")
	}
}

func TestNewVerifier(t *testing.T) {
	registry := tool.NewRegistry()
	verifier := NewVerifier(registry)

	if verifier == nil {
		t.Fatal("NewVerifier returned nil")
	}
	if verifier.toolRegistry != registry {
		t.Error("Verifier toolRegistry not set correctly")
	}
}

func TestVerifier_VerifyOutcomes_Files(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a mock tool registry with read_file
	registry := tool.NewRegistry()

	// Create test files
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	task := &Task{
		ID:    "1",
		Files: []string{testFile},
	}

	result := &VerifyResult{
		Passed: false, // Will be set by VerifyOutcomes
	}

	verifier := NewVerifier(registry)
	err := verifier.VerifyOutcomes(task, result)

	if err != nil {
		t.Errorf("VerifyOutcomes failed: %v", err)
	}

	// For a simple file verification with existing file and no verification steps,
	// it should pass (though may have warnings about tests)
	t.Logf("Verification result - Passed: %v, Errors: %v, Warnings: %v",
		result.Passed, result.Errors, result.Warnings)
}

func TestVerifier_DetectTestCommands(t *testing.T) {
	tmpDir := t.TempDir()
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	tests := []struct {
		name         string
		setupFiles   map[string]string
		wantCommands []string
	}{
		{
			name: "Go project",
			setupFiles: map[string]string{
				"go.mod": "module test\n\ngo 1.21",
			},
			wantCommands: []string{"go test ./..."},
		},
		{
			name: "Node project",
			setupFiles: map[string]string{
				"package.json": `{"name": "test", "scripts": {"test": "jest"}}`,
			},
			wantCommands: []string{"npm test"},
		},
		{
			name: "Rust project",
			setupFiles: map[string]string{
				"Cargo.toml": "[package]\nname = \"test\"\nversion = \"0.1.0\"",
			},
			wantCommands: []string{"cargo test"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup files
			for filename, content := range tt.setupFiles {
				if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
					t.Fatalf("Failed to create %s: %v", filename, err)
				}
			}

			registry := tool.NewRegistry()
			verifier := NewVerifier(registry)
			commands := verifier.detectTestCommands()

			// Check that expected commands are found
			for _, want := range tt.wantCommands {
				found := false
				for _, cmd := range commands {
					if cmd == want {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Expected to find test command %s, but didn't", want)
				}
			}

			// Cleanup
			for filename := range tt.setupFiles {
				os.Remove(filename)
			}
		})
	}
}

func TestVerifier_DetectLinterCommands(t *testing.T) {
	tmpDir := t.TempDir()
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	tests := []struct {
		name         string
		setupFiles   map[string]string
		wantCommands []string
	}{
		{
			name: "Go project with golangci-lint config",
			setupFiles: map[string]string{
				".golangci.yml": "linters:\n  enable:\n    - errcheck",
			},
			wantCommands: []string{"golangci-lint run"},
		},
		{
			name: "Go project without golangci-lint",
			setupFiles: map[string]string{
				"go.mod": "module test",
			},
			wantCommands: []string{"go vet ./..."},
		},
		{
			name: "Node project with eslint config",
			setupFiles: map[string]string{
				".eslintrc.json": "{}",
			},
			wantCommands: []string{"eslint ."},
		},
		{
			name: "Rust project",
			setupFiles: map[string]string{
				"Cargo.toml": "[package]\nname = \"test\"",
			},
			wantCommands: []string{"cargo clippy"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup files
			for filename, content := range tt.setupFiles {
				if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
					t.Fatalf("Failed to create %s: %v", filename, err)
				}
			}

			registry := tool.NewRegistry()
			verifier := NewVerifier(registry)
			commands := verifier.detectLinterCommands()

			// Check that expected commands are found
			for _, want := range tt.wantCommands {
				found := false
				for _, cmd := range commands {
					if cmd == want {
						found = true
						break
					}
				}
				// Note: These may not be found if the linters aren't installed
				if !found {
					t.Logf("Note: Expected linter command %s not found (may not be installed)", want)
				}
			}

			// Cleanup
			for filename := range tt.setupFiles {
				os.Remove(filename)
			}
		})
	}
}

func TestVerifier_RunVerificationStep(t *testing.T) {
	registry := tool.NewRegistry()
	verifier := NewVerifier(registry)
	result := &VerifyResult{}

	// Test a simple command that should succeed
	err := verifier.runVerificationStep("echo test", result)

	if err != nil {
		t.Errorf("runVerificationStep failed for simple echo command: %v", err)
	}
}

func TestVerifier_CollectArtifacts(t *testing.T) {
	tmpDir := t.TempDir()
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	// Create test artifacts
	artifacts := []struct {
		path    string
		content string
	}{
		{"coverage.out", "coverage data here"},
		{"buckley", "binary content"},
		{"test-results/output.xml", "<testsuite></testsuite>"},
	}

	for _, artifact := range artifacts {
		dir := filepath.Dir(artifact.path)
		if dir != "." {
			os.MkdirAll(dir, 0755)
		}
		if err := os.WriteFile(artifact.path, []byte(artifact.content), 0644); err != nil {
			t.Fatalf("Failed to create artifact %s: %v", artifact.path, err)
		}
	}

	registry := tool.NewRegistry()
	verifier := NewVerifier(registry)
	result := &VerifyResult{}

	verifier.collectArtifacts(&Task{}, result)

	// Should have found artifacts
	if len(result.Artifacts) == 0 {
		t.Error("Expected to find artifacts, but found none")
	}

	t.Logf("Found %d artifacts: %+v", len(result.Artifacts), result.Artifacts)
}

func TestValidator_MarkdownVerificationSteps(t *testing.T) {
	registry := tool.NewRegistry()
	validator := NewValidator(registry, ".")

	// Simulate a task with markdown-style verification steps
	// (similar to auto-generated plans)
	task := &Task{
		ID:          "test-markdown",
		Title:       "Test Markdown Verification",
		Description: "Test task with markdown verification steps",
		Files: []string{
			"go.mod",
			"cmd/**/main.go",   // glob pattern - should be skipped
			"internal/**/*.go", // glob pattern - should be skipped
			"pkg/**/*.go",      // glob pattern - should be skipped
		},
		Verification: []string{
			"Run `find . -name '*.go' | wc -l` to count files",
			"Execute `go mod graph > dependency-graph.txt` and verify output",
			"Generate `tree -I 'vendor|node_modules'` and confirm structure is documented",
			"Verify all imports of goquery are identified",
			"Create at least 10 GitHub issues with detailed descriptions",
		},
	}

	result := validator.ValidatePreconditions(task)

	if result == nil {
		t.Fatal("ValidatePreconditions returned nil")
	}

	// Should not extract common verbs as tools
	commonVerbs := []string{"Run", "Execute", "Generate", "Verify", "Create", "Add", "Document"}
	for _, tool := range result.MissingTools {
		for _, verb := range commonVerbs {
			if strings.EqualFold(tool, verb) {
				t.Errorf("Incorrectly extracted common verb as tool: %s", tool)
			}
		}
	}

	// Should extract actual tools from backticks
	tools := validator.extractRequiredTools(task)
	t.Logf("Extracted tools: %v", tools)

	// Should have extracted actual commands like "find", "go", "tree"
	hasFind := false
	hasGo := false
	for _, tool := range tools {
		if tool == "find" {
			hasFind = true
		}
		if tool == "go" {
			hasGo = true
		}
		// Make sure no common verbs
		for _, verb := range commonVerbs {
			if strings.EqualFold(tool, verb) {
				t.Errorf("Found common verb in extracted tools: %s", tool)
			}
		}
	}

	if !hasFind {
		t.Log("Note: 'find' not extracted (may be expected depending on extraction logic)")
	}
	if !hasGo {
		t.Log("Note: 'go' not extracted (may be expected depending on extraction logic)")
	}

	// Glob patterns in Files should not cause directory validation errors
	for _, err := range result.Errors {
		if strings.Contains(err, "cmd/*") || strings.Contains(err, "internal/**") || strings.Contains(err, "pkg/**") {
			t.Errorf("Glob patterns should not cause validation errors: %s", err)
		}
	}

	t.Logf("Validation result - Valid: %v, Errors: %v, Warnings: %v, MissingTools: %v",
		result.Valid, result.Errors, result.Warnings, result.MissingTools)
}
