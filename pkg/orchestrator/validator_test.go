package orchestrator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/tool"
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
		ID:            "1",
		Title:         "Non-existent tool task",
		Description:   "Use a tool that definitely doesn't exist xyzabc123",
		RequiredTools: []string{"xyzabc123"},
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

// TestValidator_ProseBacktickIdentifiersAreNotTools locks in the fix for the
// bug where the validator read the first word of every backtick span in the
// task prose and demanded a tool of that name. Plans quote code identifiers
// and file names that way, so execution never started.
func TestValidator_ProseBacktickIdentifiersAreNotTools(t *testing.T) {
	tmpDir := t.TempDir()
	registry := tool.NewRegistry()
	validator := NewValidator(registry, tmpDir)

	for _, name := range []string{"store.go", "service.go"} {
		path := filepath.Join(tmpDir, name)
		if err := os.WriteFile(path, []byte("package main\n"), 0644); err != nil {
			t.Fatalf("Failed to create %s: %v", name, err)
		}
	}

	task := &Task{
		ID:    "1",
		Title: "Harden `persistLocked` in `store.go`",
		Type:  TaskTypeImplementation,
		Description: "Review `persistLocked` in `store.go` and mirror the change " +
			"into `service.go`, `page.server.go`, and `gridiron.js`.",
		Files: []string{"store.go", "service.go"},
		Verification: []string{
			"Verify existing signature and logic of `persistLocked` in `store.go`",
			"Confirm `page.server.go` still imports `service.go` helpers",
			"Check that `gridiron.js` keeps the same export surface",
		},
	}

	tools := validator.requiredTools(task)
	if len(tools) != 0 {
		t.Errorf("Expected no required tools from prose, got %v", tools)
	}

	result := validator.ValidatePreconditions(task)
	if result == nil {
		t.Fatal("ValidatePreconditions returned nil")
	}

	if len(result.MissingTools) != 0 {
		t.Errorf("Prose identifiers were reported as missing tools: %v", result.MissingTools)
	}
	if !result.Valid {
		t.Errorf("Expected validation to pass, got errors: %v", result.Errors)
	}
}

// TestValidator_DeclaredToolsStillChecked proves the structured field keeps
// its power: a declared tool that is absent must still fail the task.
func TestValidator_DeclaredToolsStillChecked(t *testing.T) {
	tmpDir := t.TempDir()
	registry := tool.NewRegistry()
	validator := NewValidator(registry, tmpDir)

	task := &Task{
		ID:            "1",
		Title:         "Deploy with a missing binary",
		Type:          TaskTypeAnalysis,
		Description:   "Roll out the release.",
		Verification:  []string{"Confirm `deployStack` ran"},
		RequiredTools: []string{"totally-not-installed-xyzabc123"},
	}

	result := validator.ValidatePreconditions(task)
	if result == nil {
		t.Fatal("ValidatePreconditions returned nil")
	}

	if result.Valid {
		t.Error("Expected validation to fail for a declared missing tool")
	}

	if len(result.MissingTools) != 1 || result.MissingTools[0] != "totally-not-installed-xyzabc123" {
		t.Errorf("Expected only the declared tool to be missing, got %v", result.MissingTools)
	}
}

// TestValidator_DeclaredToolsResolveFromRegistryOrPath confirms the validator
// keeps both resolution paths: the tool registry and the system PATH.
func TestValidator_DeclaredToolsResolveFromRegistryOrPath(t *testing.T) {
	tmpDir := t.TempDir()
	registry := tool.NewRegistry()
	validator := NewValidator(registry, tmpDir)

	task := &Task{
		ID:            "1",
		Title:         "Run the build",
		Type:          TaskTypeAnalysis,
		Description:   "Build the module.",
		RequiredTools: []string{"go", "  ", "go"},
	}

	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not in PATH")
	}

	tools := validator.requiredTools(task)
	if len(tools) != 1 || tools[0] != "go" {
		t.Errorf("Expected deduplicated [go], got %v", tools)
	}

	result := validator.ValidatePreconditions(task)
	if len(result.MissingTools) != 0 {
		t.Errorf("Expected no missing tools, got %v", result.MissingTools)
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

func TestValidator_RequiredTools(t *testing.T) {
	registry := tool.NewRegistry()
	validator := NewValidator(registry, ".")

	tests := []struct {
		name          string
		description   string
		verification  []string
		requiredTools []string
		wantTools     []string
	}{
		{
			name:          "declared go tool",
			description:   "Run go tests",
			requiredTools: []string{"go"},
			wantTools:     []string{"go"},
		},
		{
			name:          "declared npm tool",
			description:   "Build project",
			verification:  []string{"npm run build"},
			requiredTools: []string{"npm"},
			wantTools:     []string{"npm"},
		},
		{
			name:          "blank entries dropped",
			description:   "Deploy infrastructure",
			requiredTools: []string{"terraform", "", "   "},
			wantTools:     []string{"terraform"},
		},
		{
			name:         "prose alone declares nothing",
			description:  "Build container image with docker and run `docker compose up`",
			verification: []string{"Run `make build` and check `main.go`"},
			wantTools:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := &Task{
				Description:   tt.description,
				Verification:  tt.verification,
				RequiredTools: tt.requiredTools,
			}

			tools := validator.requiredTools(task)

			if len(tools) != len(tt.wantTools) {
				t.Fatalf("Expected tools %v, got %v", tt.wantTools, tools)
			}
			for i, want := range tt.wantTools {
				if tools[i] != want {
					t.Errorf("Expected tool %s at index %d, got %s", want, i, tools[i])
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

	// Markdown verification prose must never produce a required tool.
	if len(result.MissingTools) != 0 {
		t.Errorf("Verification prose produced missing tools: %v", result.MissingTools)
	}

	tools := validator.requiredTools(task)
	if len(tools) != 0 {
		t.Errorf("Expected no required tools from verification prose, got %v", tools)
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
