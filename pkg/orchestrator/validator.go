package orchestrator

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/envdetect"
	"github.com/odvcencio/buckley/pkg/tool"
)

// Validator handles pre-execution validation of tasks
type Validator struct {
	toolRegistry    *tool.Registry
	projectDetector *ProjectDetector
	projectRoot     string
}

// NewValidator creates a new validator
func NewValidator(registry *tool.Registry, projectRoot string) *Validator {
	root := strings.TrimSpace(projectRoot)
	if root == "" {
		root = "."
	}
	return &Validator{
		toolRegistry:    registry,
		projectDetector: NewProjectDetector(root),
		projectRoot:     root,
	}
}

// ValidationResult contains validation results
type ValidationResult struct {
	Valid          bool
	Errors         []string
	Warnings       []string
	MissingTools   []string
	MissingEnvVars []string
}

// ValidatePreconditions validates that a task can be executed
func (v *Validator) ValidatePreconditions(task *Task) *ValidationResult {
	result := &ValidationResult{
		Valid: true,
	}

	// 1. Check tool availability
	if err := v.checkTools(task, result); err != nil {
		result.Valid = false
		result.Errors = append(result.Errors, fmt.Sprintf("Tool check failed: %v", err))
	}

	// 2. Check environment variables
	if err := v.checkEnvironment(task, result); err != nil {
		result.Valid = false
		result.Errors = append(result.Errors, fmt.Sprintf("Environment check failed: %v", err))
	}

	// 3. Check dependencies
	if err := v.checkDependencies(task, result); err != nil {
		result.Valid = false
		result.Errors = append(result.Errors, fmt.Sprintf("Dependency check failed: %v", err))
	}

	// 4. Check permissions
	if err := v.checkPermissions(task, result); err != nil {
		result.Valid = false
		result.Errors = append(result.Errors, fmt.Sprintf("Permission check failed: %v", err))
	}

	// 5. Verify model compatibility
	if err := v.checkModelCompatibility(task, result); err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("Model compatibility: %v", err))
	}

	return result
}

// checkTools verifies all required tools are available
func (v *Validator) checkTools(task *Task, result *ValidationResult) error {
	// Extract tool names from task description and verification steps
	tools := v.extractRequiredTools(task)

	for _, toolName := range tools {
		toolName = strings.TrimSpace(toolName)
		if toolName == "" {
			continue
		}

		// Check if tool exists in registry
		_, exists := v.toolRegistry.Get(toolName)
		if !exists {
			// Check if it's a native command
			if _, err := exec.LookPath(toolName); err != nil {
				result.MissingTools = append(result.MissingTools, toolName)
				result.Valid = false
			}
		}
	}

	if len(result.MissingTools) > 0 {
		return fmt.Errorf("missing tools: %v", result.MissingTools)
	}

	return nil
}

// extractRequiredTools extracts tool names from task
func (v *Validator) extractRequiredTools(task *Task) []string {
	var tools []string
	seen := make(map[string]bool)

	// Check task description for common tools
	patterns := []struct {
		pattern string
		tool    string
	}{
		{"npm ", "npm"},
		{"go ", "go"},
		{"cargo ", "cargo"},
		{"make ", "make"},
		{"docker ", "docker"},
		{"python ", "python"},
		{"ruby ", "ruby"},
		{"java ", "java"},
		{"javac ", "javac"},
		{"git ", "git"},
		{"gh ", "gh"},
		{"aws ", "aws"},
		{"gcloud ", "gcloud"},
		{"terraform ", "terraform"},
		{"kubectl ", "kubectl"},
		{"helm ", "helm"},
		{"ansible ", "ansible"},
	}

	lowerDesc := strings.ToLower(task.Description)
	for _, p := range patterns {
		if strings.Contains(lowerDesc, p.pattern) && !seen[p.tool] {
			tools = append(tools, p.tool)
			seen[p.tool] = true
		}
	}

	// Exact word matching for known tool names in the description.
	words := strings.Fields(lowerDesc)
	for _, word := range words {
		clean := strings.Trim(word, ".,:;")
		for _, p := range patterns {
			target := strings.TrimSpace(p.pattern)
			if clean == target && !seen[p.tool] {
				tools = append(tools, p.tool)
				seen[p.tool] = true
			}
		}
	}

	// Check verification steps
	for _, verification := range task.Verification {
		for _, p := range patterns {
			if strings.Contains(strings.ToLower(verification), p.pattern) && !seen[p.tool] {
				tools = append(tools, p.tool)
				seen[p.tool] = true
			}
		}
	}

	// Extract tools from backtick-enclosed commands in verification steps.
	// This avoids false positives from natural language descriptions.
	for _, verification := range task.Verification {
		for {
			startIdx := strings.Index(verification, "`")
			if startIdx == -1 {
				break
			}
			remaining := verification[startIdx+1:]
			endIdx := strings.Index(remaining, "`")
			if endIdx == -1 {
				break
			}

			command := remaining[:endIdx]
			fields := strings.Fields(command)
			if len(fields) > 0 {
				candidate := strings.TrimSpace(fields[0])
				if candidate != "" && !seen[candidate] {
					tools = append(tools, candidate)
					seen[candidate] = true
				}
			}

			// Move past this backtick pair for next iteration
			verification = remaining[endIdx+1:]
		}
	}

	return tools
}

// checkEnvironment verifies required environment variables
func (v *Validator) checkEnvironment(task *Task, result *ValidationResult) error {
	// Common environment variables by project type
	effectiveEnv := v.getEffectiveEnvironment()

	// Project-specific checks
	requiredEnv := v.getRequiredEnvironmentVars(task)

	for _, envVar := range requiredEnv {
		if os.Getenv(envVar) == "" && effectiveEnv[envVar] == "" {
			result.MissingEnvVars = append(result.MissingEnvVars, envVar)
			result.Warnings = append(result.Warnings, fmt.Sprintf("Missing environment variable: %s", envVar))
		}
	}

	return nil
}

// getEffectiveEnvironment gets environment from context and system
func (v *Validator) getEffectiveEnvironment() map[string]string {
	env := make(map[string]string)

	// Start with system environment
	for _, e := range os.Environ() {
		pair := strings.SplitN(e, "=", 2)
		if len(pair) == 2 {
			env[pair[0]] = pair[1]
		}
	}

	// Load project environment from detected profile
	if profile := v.detectProjectEnvironment(); profile != nil {
		// Add detected environment variables
		for _, envVar := range profile.EnvVars {
			// Parse VAR=value format
			parts := strings.SplitN(envVar, "=", 2)
			if len(parts) == 2 {
				// Only add if not already set (system env takes precedence)
				if _, exists := env[parts[0]]; !exists {
					env[parts[0]] = parts[1]
				}
			}
		}
	}

	return env
}

// detectProjectEnvironment uses envdetect to scan the project
func (v *Validator) detectProjectEnvironment() *envdetect.EnvironmentProfile {
	cwd := strings.TrimSpace(v.projectRoot)
	if cwd == "" {
		if wd, err := os.Getwd(); err == nil {
			cwd = wd
		}
	}
	if cwd == "" {
		return nil
	}

	// Create detector and scan
	detector := envdetect.NewDetector(cwd)
	profile, err := detector.Detect()
	if err != nil {
		return nil
	}

	return profile
}

// getRequiredEnvironmentVars determines required env vars based on task
func (v *Validator) getRequiredEnvironmentVars(task *Task) []string {
	var required []string

	desc := strings.ToLower(task.Description)

	// Model API keys
	if strings.Contains(desc, "model") || strings.Contains(desc, "openai") ||
		strings.Contains(desc, "anthropic") || strings.Contains(desc, "openrouter") {
		required = append(required, "OPENROUTER_API_KEY")
	}

	// Database credentials
	if strings.Contains(desc, "database") || strings.Contains(desc, "postgres") ||
		strings.Contains(desc, "mysql") || strings.Contains(desc, "mongodb") {
		required = append(required, "DB_HOST", "DB_USER", "DB_PASSWORD")
	}

	// Cloud providers
	if strings.Contains(desc, "aws") || strings.Contains(desc, "amazon") {
		required = append(required, "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY")
	}
	if strings.Contains(desc, "gcp") || strings.Contains(desc, "google") {
		required = append(required, "GOOGLE_APPLICATION_CREDENTIALS")
	}
	if strings.Contains(desc, "azure") || strings.Contains(desc, "microsoft") {
		required = append(required, "AZURE_SUBSCRIPTION_ID")
	}

	return required
}

// checkDependencies verifies project dependencies are installed
func (v *Validator) checkDependencies(task *Task, result *ValidationResult) error {
	// Check system-level dependencies
	systemDeps := []string{
		"go", "node", "npm", "cargo", "make", "cmake", "gcc", "git",
	}

	for _, dep := range systemDeps {
		if v.isLikelyNeeded(task, dep) {
			if _, err := exec.LookPath(dep); err != nil {
				// Only warn, don't fail - might be in container
				result.Warnings = append(result.Warnings,
					fmt.Sprintf("System dependency '%s' not found in PATH", dep))
			}
		}
	}

	// Check project-level dependencies
	if err := v.checkProjectDependencies(result); err != nil {
		result.Warnings = append(result.Warnings, err.Error())
	}

	return nil
}

// isLikelyNeeded determines if a system dep is likely needed
func (v *Validator) isLikelyNeeded(task *Task, dep string) bool {
	// Simple heuristic based on task description
	desc := strings.ToLower(task.Description)

	switch dep {
	case "go":
		return strings.Contains(desc, "go ") || strings.Contains(desc, "golang")
	case "node", "npm":
		return strings.Contains(desc, "node") || strings.Contains(desc, "javascript") ||
			strings.Contains(desc, "npm ") || strings.Contains(desc, "package.json")
	case "cargo":
		return strings.Contains(desc, "rust") || strings.Contains(desc, "cargo ")
	case "make":
		return strings.Contains(desc, "make ") || strings.Contains(desc, "makefile")
	case "git":
		return strings.Contains(desc, "git ") || strings.Contains(desc, "commit") ||
			strings.Contains(desc, "branch")
	}

	return false
}

// checkProjectDependencies checks project-specific dependencies based on detected project type
func (v *Validator) checkProjectDependencies(result *ValidationResult) error {
	projectType := v.projectDetector.DetectType()
	root := strings.TrimSpace(v.projectRoot)
	if root == "" {
		root = "."
	}

	switch projectType {
	case ProjectTypeGo:
		cmd := exec.Command("go", "mod", "verify")
		cmd.Dir = root
		if err := cmd.Run(); err != nil {
			result.Warnings = append(result.Warnings,
				"Go dependencies may not be fully downloaded - run go mod download")
		}

	case ProjectTypeNodeJS, ProjectTypeTypeScript:
		if _, err := os.Stat(filepath.Join(root, "node_modules")); err != nil {
			result.Warnings = append(result.Warnings,
				"node_modules missing - run npm install")
		}

	case ProjectTypeRust:
		if _, err := os.Stat(filepath.Join(root, "Cargo.lock")); err != nil {
			result.Warnings = append(result.Warnings,
				"Cargo.lock not found - run cargo check")
		}

	case ProjectTypePython:
		// Check for virtual environment
		if _, err := os.Stat(filepath.Join(root, "venv")); err != nil {
			if _, err := os.Stat(filepath.Join(root, ".venv")); err != nil {
				result.Warnings = append(result.Warnings,
					"Virtual environment not found - consider running python -m venv venv")
			}
		}
	}

	return nil
}

// checkPermissions verifies file system permissions
func (v *Validator) checkPermissions(task *Task, result *ValidationResult) error {
	// Skip permission checks for analysis and validation tasks (they don't create files)
	if task.Type == TaskTypeAnalysis || task.Type == TaskTypeValidation {
		return nil
	}

	// Check if write_file tool is available for creating missing files
	_, hasWriteTool := v.toolRegistry.Get("write_file")
	root := strings.TrimSpace(v.projectRoot)
	if root == "" {
		root = "."
	}
	absRoot, err := filepath.Abs(root)
	if err == nil {
		absRoot = filepath.Clean(absRoot)
	} else {
		absRoot = filepath.Clean(root)
	}

	// Check if we need to write to specific files
	for _, file := range task.Files {
		if file == "" {
			continue
		}

		// Skip glob patterns - these are placeholders, not actual paths
		if strings.Contains(file, "*") || strings.Contains(file, "**") {
			continue
		}

		// Skip files that look like artifacts (will be generated, not written directly)
		if isArtifactFile(file) {
			continue
		}

		// Check if path exists and is writable
		targetPath := file
		if !filepath.IsAbs(targetPath) {
			targetPath = filepath.Join(root, targetPath)
		}
		if absTarget, err := filepath.Abs(targetPath); err == nil {
			targetPath = filepath.Clean(absTarget)
		} else {
			targetPath = filepath.Clean(targetPath)
		}

		if absRoot != "" && !isWithinDir(absRoot, targetPath) {
			result.Errors = append(result.Errors, fmt.Sprintf("Path outside project root: %s", file))
			result.Valid = false
			continue
		}

		if _, err := os.Stat(targetPath); err == nil {
			// File exists - check writability
			if fi, err := os.Stat(targetPath); err == nil {
				// Simplified check - in reality we'd need to check actual permissions
				if fi.Mode()&0200 == 0 && os.Getuid() != 0 {
					result.Errors = append(result.Errors,
						fmt.Sprintf("File not writable: %s", file))
					result.Valid = false
				}
			}
		} else if os.IsNotExist(err) {
			// File doesn't exist - check if we can create it
			dir := filepath.Dir(targetPath)
			if strings.TrimSpace(dir) != "" && !strings.Contains(dir, "*") {
				// Check if parent directory exists
				if _, statErr := os.Stat(dir); statErr != nil {
					// Directory doesn't exist - check if we have tools to create it
					if !hasWriteTool {
						// Can't create files without write_file tool - hard error
						result.Errors = append(result.Errors,
							fmt.Sprintf("Directory does not exist: %s (and no write_file tool available)", dir))
						result.Valid = false
					} else if !hasValidParent(dir) {
						// Have write_file tool but no parent directory to create into
						result.Errors = append(result.Errors,
							fmt.Sprintf("No valid parent directory for: %s", dir))
						result.Valid = false
					} else {
						// Have write_file tool and valid parent - can create
						result.Warnings = append(result.Warnings,
							fmt.Sprintf("Directory does not exist (will be created if needed): %s", dir))
					}
				}
			}
		}
	}

	return nil
}

func isWithinDir(base, target string) bool {
	rel, err := filepath.Rel(filepath.Clean(base), filepath.Clean(target))
	if err != nil {
		return false
	}
	rel = filepath.Clean(rel)
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// isArtifactFile checks if a file path looks like a generated artifact
func isArtifactFile(path string) bool {
	artifactPatterns := []string{
		"coverage.out",
		"coverage.html",
		".html",
		".log",
		"-report.md",
		"-analysis.md",
	}
	for _, pattern := range artifactPatterns {
		if strings.Contains(path, pattern) {
			return true
		}
	}
	return false
}

// hasValidParent checks if any parent directory in the path exists
func hasValidParent(dir string) bool {
	current := filepath.Clean(strings.TrimSpace(dir))
	if current == "" {
		return false
	}
	for {
		parent := filepath.Dir(current)
		if parent == "" || parent == current {
			// Reached root without finding existing directory
			return false
		}
		if _, err := os.Stat(parent); err == nil {
			// Found existing parent directory
			return true
		}
		current = parent
	}
}

// checkModelCompatibility verifies model is compatible with task
func (v *Validator) checkModelCompatibility(task *Task, result *ValidationResult) error {
	// Special considerations for long tasks or large files
	if task.EstimatedTime != "" && strings.Contains(task.EstimatedTime, "hour") {
		result.Warnings = append(result.Warnings,
			"Long-running task - ensure adequate timeout configuration")
	}

	// High complexity tasks warning
	if len(task.Files) > 10 {
		result.Warnings = append(result.Warnings,
			"Many files to modify - consider breaking into smaller tasks")
	}

	return nil
}

// VerifyResult contains verification results
type VerifyResult struct {
	Version   string
	Passed    bool
	Errors    []string
	Warnings  []string
	Artifacts []Artifact
}

// Artifact represents a generated artifact
type Artifact struct {
	ID   string
	Type string // "file", "log", "report", "test_result"
	Path string
}

// Verifier handles post-execution verification
type Verifier struct {
	toolRegistry *tool.Registry
}

// NewVerifier creates a new verifier
func NewVerifier(registry *tool.Registry) *Verifier {
	return &Verifier{
		toolRegistry: registry,
	}
}

// VerifyOutcomes validates that execution produced expected results
func (verifier *Verifier) VerifyOutcomes(task *Task, result *VerifyResult) error {
	// 1. Verify files were created/modified (skip for analysis/validation tasks)
	if task.Type == TaskTypeImplementation {
		if err := verifier.verifyFiles(task, result); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("File verification failed: %v", err))
		}
	}

	// 2. Run verification steps
	if err := verifier.runVerificationSteps(task, result); err != nil {
		result.Passed = false
		result.Errors = append(result.Errors, fmt.Sprintf("Verification failed: %v", err))
		return err
	}

	// 3. Run automated tests (primarily for validation tasks)
	if task.Type == TaskTypeValidation || task.Type == "" {
		if err := verifier.runTests(task, result); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("Tests: %v", err))
		}
	}

	// 4. Check code quality thresholds (for implementation tasks)
	if task.Type == TaskTypeImplementation || task.Type == "" {
		if err := verifier.checkQuality(task, result); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("Quality: %v", err))
		}
	}

	// 5. Collect artifacts
	verifier.collectArtifacts(task, result)

	if len(result.Errors) == 0 {
		result.Passed = true
	}

	return nil
}

// verifyFiles checks that expected files were created/modified
func (verifier *Verifier) verifyFiles(task *Task, result *VerifyResult) error {
	readTool, ok := verifier.toolRegistry.Get("read_file")
	if !ok {
		return fmt.Errorf("read_file tool not available")
	}

	for _, filePath := range task.Files {
		params := map[string]any{
			"path": filePath,
		}

		execResult, err := readTool.Execute(params)
		if err != nil || !execResult.Success {
			result.Errors = append(result.Errors,
				fmt.Sprintf("Expected file not accessible: %s", filePath))
		} else {
			// File exists - check it's not empty (basic validation)
			if content, ok := execResult.Data["content"].(string); ok &&
				strings.TrimSpace(content) == "" {
				result.Warnings = append(result.Warnings,
					fmt.Sprintf("File is empty: %s", filePath))
			}
		}
	}

	return nil
}

// runVerificationSteps executes verification steps from task
func (verifier *Verifier) runVerificationSteps(task *Task, result *VerifyResult) error {
	for _, verification := range task.Verification {
		if err := verifier.runVerificationStep(verification, result); err != nil {
			return fmt.Errorf("verification step failed: %s: %v", verification, err)
		}
	}
	return nil
}

// runVerificationStep executes a single verification step
func (verifier *Verifier) runVerificationStep(verification string, result *VerifyResult) error {
	cmd := verifier.buildCommand(verification)
	if cmd == "" {
		return nil // Skip if no command can be inferred
	}

	// Run with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	parts := strings.Fields(cmd)
	var c *exec.Cmd
	if len(parts) > 1 {
		c = exec.CommandContext(ctx, parts[0], parts[1:]...)
	} else {
		c = exec.CommandContext(ctx, parts[0])
	}

	output, err := c.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("verification timed out")
		}
		return fmt.Errorf("command failed: %s\nOutput: %s", err, string(output))
	}

	// Record artifact
	result.Artifacts = append(result.Artifacts, Artifact{
		ID:   fmt.Sprintf("verification_%s", cmd),
		Type: "test_result",
		Path: cmd,
	})

	return nil
}

// buildCommand infers command from verification description based on project type
func (verifier *Verifier) buildCommand(verification string) string {
	src := strings.ToLower(verification)
	detector := NewProjectDetector(".")

	// Direct commands (pass through if they match the project type)
	if strings.HasPrefix(src, "go ") && detector.DetectType() == ProjectTypeGo {
		return verification
	}
	if strings.HasPrefix(src, "npm ") && (detector.DetectType() == ProjectTypeNodeJS || detector.DetectType() == ProjectTypeTypeScript) {
		return verification
	}
	if strings.HasPrefix(src, "cargo ") && detector.DetectType() == ProjectTypeRust {
		return verification
	}
	if strings.HasPrefix(src, "make ") {
		return verification
	}

	// Descriptions mapped to commands based on project type
	if strings.Contains(src, "test") {
		return detector.GetTestCommand()
	}

	if strings.Contains(src, "build") {
		return detector.GetBuildCommand()
	}

	return ""
}

// runTests automatically runs appropriate tests for the project
func (verifier *Verifier) runTests(task *Task, result *VerifyResult) error {
	// Don't run tests if task already includes test verification
	for _, v := range task.Verification {
		if strings.Contains(strings.ToLower(v), "test") {
			return nil
		}
	}

	testCommands := verifier.detectTestCommands()
	for _, cmd := range testCommands {
		if err := verifier.runVerificationStep(cmd, result); err != nil {
			result.Warnings = append(result.Warnings, err.Error())
		}
	}

	return nil
}

// detectTestCommands finds appropriate test commands based on project type
func (verifier *Verifier) detectTestCommands() []string {
	var commands []string
	detector := NewProjectDetector(".")

	cmd := detector.GetTestCommand()
	if cmd != "" {
		commands = append(commands, cmd)
	}

	return commands
}

// checkQuality runs code quality checks
func (verifier *Verifier) checkQuality(task *Task, result *VerifyResult) error {
	// Run linter if available
	linterCommands := verifier.detectLinterCommands()
	for _, cmd := range linterCommands {
		if err := verifier.runVerificationStep(cmd, result); err != nil {
			// Lint warnings are just warnings, not errors
			result.Warnings = append(result.Warnings, err.Error())
		}
	}

	return nil
}

// detectLinterCommands finds appropriate linter commands based on project type
func (verifier *Verifier) detectLinterCommands() []string {
	var commands []string
	detector := NewProjectDetector(".")

	cmd := detector.GetLinterCommand()
	if cmd != "" {
		// Verify the linter tool is available
		parts := strings.Fields(cmd)
		if len(parts) > 0 {
			if _, err := exec.LookPath(parts[0]); err == nil {
				commands = append(commands, cmd)
			}
		}
	}

	return commands
}

// collectArtifacts collects generated artifacts for tracking
func (verifier *Verifier) collectArtifacts(task *Task, result *VerifyResult) {
	// Find test result files
	if _, err := os.Stat("coverage.out"); err == nil {
		result.Artifacts = append(result.Artifacts, Artifact{
			ID:   "coverage",
			Type: "report",
			Path: "coverage.out",
		})
	}

	// Find build artifacts
	if _, err := os.Stat("buckley"); err == nil {
		result.Artifacts = append(result.Artifacts, Artifact{
			ID:   "binary",
			Type: "file",
			Path: "buckley",
		})
	}

	// Look for test output directories
	if _, err := os.Stat("test-results"); err == nil {
		result.Artifacts = append(result.Artifacts, Artifact{
			ID:   "test_results",
			Type: "report",
			Path: "test-results",
		})
	}
}
