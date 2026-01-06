package builtin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// RunTestsTool runs tests with smart filtering and reporting
type RunTestsTool struct{ workDirAware }

// execCommandContext is overridden in tests to stub command execution.
var execCommandContext = exec.CommandContext

func (t *RunTestsTool) Name() string {
	return "run_tests"
}

func (t *RunTestsTool) Description() string {
	return "Run tests with smart filtering by path, pattern, or name. Auto-detects test framework (Go, Jest, pytest, etc.) and provides formatted results with pass/fail counts, timing, and coverage info when available. Use this to verify code changes or run specific test suites."
}

func (t *RunTestsTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"path": {
				Type:        "string",
				Description: "Optional: directory or file to test (default: current directory)",
				Default:     ".",
			},
			"pattern": {
				Type:        "string",
				Description: "Optional: test name pattern to filter (e.g., 'TestUser*', 'test_auth')",
			},
			"coverage": {
				Type:        "boolean",
				Description: "Generate coverage report (default: false)",
				Default:     false,
			},
			"verbose": {
				Type:        "boolean",
				Description: "Verbose test output (default: false)",
				Default:     false,
			},
			"timeout_seconds": {
				Type:        "integer",
				Description: "Test timeout in seconds (default: 300)",
				Default:     300,
			},
		},
		Required: []string{},
	}
}

func (t *RunTestsTool) Execute(params map[string]any) (*Result, error) {
	testPath := "."
	if p, ok := params["path"].(string); ok && p != "" {
		testPath = p
	}
	absTestPath := testPath
	if strings.TrimSpace(t.workDir) != "" {
		abs, rel, err := resolveRelPath(t.workDir, testPath)
		if err != nil {
			return &Result{Success: false, Error: err.Error()}, nil
		}
		absTestPath = abs
		testPath = rel
	}

	pattern := ""
	if pat, ok := params["pattern"].(string); ok {
		pattern = pat
	}

	coverage := false
	if cov, ok := params["coverage"].(bool); ok {
		coverage = cov
	}

	verbose := false
	if v, ok := params["verbose"].(bool); ok {
		verbose = v
	}

	timeout := 300
	if to, ok := params["timeout_seconds"].(float64); ok {
		timeout = int(to)
	}

	ctx := context.Background()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
		defer cancel()
	}

	// Detect test framework
	framework := t.detectTestFramework(absTestPath)

	// Run tests
	output, exitCode, duration, err := t.runTestsForFramework(ctx, framework, testPath, pattern, coverage, verbose)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("test execution failed: %v", err),
		}, nil
	}

	// Parse results
	passed, failed, skipped := t.parseTestResults(framework, output)

	result := &Result{
		Success: exitCode == 0,
		Data: map[string]any{
			"framework": framework,
			"path":      testPath,
			"pattern":   pattern,
			"passed":    passed,
			"failed":    failed,
			"skipped":   skipped,
			"duration":  duration,
			"exit_code": exitCode,
			"output":    output,
			"coverage":  coverage,
		},
	}

	// Abridge long output
	if len(output) > 5000 {
		result.ShouldAbridge = true
		summary := fmt.Sprintf("✓ %d passed, ✗ %d failed, ⊘ %d skipped (%.2fs)", passed, failed, skipped, duration)
		if failed > 0 {
			summary = "Tests FAILED: " + summary
		}

		// Extract failure details
		failureDetails := t.extractFailures(framework, output)

		result.DisplayData = map[string]any{
			"framework": framework,
			"passed":    passed,
			"failed":    failed,
			"skipped":   skipped,
			"duration":  duration,
			"summary":   summary,
			"failures":  failureDetails,
		}
	}

	return result, nil
}

func (t *RunTestsTool) detectTestFramework(path string) string {
	// Check for framework indicators
	if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
		return "go"
	}
	if _, err := os.Stat(filepath.Join(path, "package.json")); err == nil {
		// Could be jest, mocha, etc. - check package.json
		return "jest" // Default to jest for JS/TS
	}
	if _, err := os.Stat(filepath.Join(path, "pytest.ini")); err == nil {
		return "pytest"
	}
	if _, err := os.Stat(filepath.Join(path, "setup.py")); err == nil {
		return "pytest"
	}
	if _, err := os.Stat(filepath.Join(path, "Cargo.toml")); err == nil {
		return "cargo"
	}

	// Fallback: check for test files
	if files, _ := filepath.Glob(filepath.Join(path, "*_test.go")); len(files) > 0 {
		return "go"
	}
	if files, _ := filepath.Glob(filepath.Join(path, "*.test.js")); len(files) > 0 {
		return "jest"
	}
	if files, _ := filepath.Glob(filepath.Join(path, "test_*.py")); len(files) > 0 {
		return "pytest"
	}

	return "unknown"
}

func (t *RunTestsTool) runTestsForFramework(ctx context.Context, framework, path, pattern string, coverage, verbose bool) (string, int, float64, error) {
	var (
		cmd *exec.Cmd
	)

	switch framework {
	case "go":
		args := []string{"test"}
		if coverage {
			args = append(args, "-cover")
		}
		if verbose {
			args = append(args, "-v")
		}
		if pattern != "" {
			args = append(args, "-run", pattern)
		}
		args = append(args, path)
		cmd = execCommandContext(ctx, "go", args...)

	case "jest":
		args := []string{"test"}
		if coverage {
			args = append(args, "--coverage")
		}
		if verbose {
			args = append(args, "--verbose")
		}
		if pattern != "" {
			args = append(args, "-t", pattern)
		}
		cmd = execCommandContext(ctx, "npm", args...)

	case "pytest":
		args := []string{}
		if coverage {
			args = append(args, "--cov")
		}
		if verbose {
			args = append(args, "-v")
		}
		if pattern != "" {
			args = append(args, "-k", pattern)
		}
		args = append(args, path)
		cmd = execCommandContext(ctx, "pytest", args...)

	case "cargo":
		args := []string{"test"}
		if pattern != "" {
			args = append(args, pattern)
		}
		cmd = execCommandContext(ctx, "cargo", args...)

	default:
		return "", 1, 0, fmt.Errorf("unsupported test framework: %s", framework)
	}

	var stdout, stderr bytes.Buffer
	if strings.TrimSpace(t.workDir) != "" && cmd != nil {
		cmd.Dir = strings.TrimSpace(t.workDir)
	}
	if cmd != nil {
		cmd.Env = mergeEnv(cmd.Env, t.env)
	}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err := cmd.Run()
	duration := time.Since(start).Seconds()

	exitCode := 0
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return stdout.String() + stderr.String(), 1, duration, fmt.Errorf("test run exceeded timeout")
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return "", 1, duration, err
		}
	}

	output := stdout.String() + stderr.String()
	return output, exitCode, duration, nil
}

func (t *RunTestsTool) parseTestResults(framework, output string) (passed, failed, skipped int) {
	switch framework {
	case "go":
		return t.parseGoTestResults(output)
	case "jest":
		return t.parseJestResults(output)
	case "pytest":
		return t.parsePytestResults(output)
	case "cargo":
		return t.parseCargoResults(output)
	default:
		return 0, 0, 0
	}
}

func (t *RunTestsTool) parseGoTestResults(output string) (passed, failed, skipped int) {
	// Go test output patterns
	passedRe := regexp.MustCompile(`--- PASS:`)
	failedRe := regexp.MustCompile(`--- FAIL:`)
	skippedRe := regexp.MustCompile(`--- SKIP:`)

	passed = len(passedRe.FindAllString(output, -1))
	failed = len(failedRe.FindAllString(output, -1))
	skipped = len(skippedRe.FindAllString(output, -1))

	return
}

func (t *RunTestsTool) parseJestResults(output string) (passed, failed, skipped int) {
	// Jest summary line: "Tests: 5 failed, 10 passed, 15 total"
	re := regexp.MustCompile(`Tests:\s+(?:(\d+)\s+failed,\s*)?(?:(\d+)\s+passed,\s*)?(?:(\d+)\s+skipped)?`)
	matches := re.FindStringSubmatch(output)
	if len(matches) > 1 {
		fmt.Sscanf(matches[1], "%d", &failed)
		fmt.Sscanf(matches[2], "%d", &passed)
		fmt.Sscanf(matches[3], "%d", &skipped)
	}
	return
}

func (t *RunTestsTool) parsePytestResults(output string) (passed, failed, skipped int) {
	// Pytest summary: "5 failed, 10 passed, 2 skipped"
	re := regexp.MustCompile(`(\d+)\s+(\w+)`)
	matches := re.FindAllStringSubmatch(output, -1)
	for _, match := range matches {
		if len(match) == 3 {
			count := 0
			fmt.Sscanf(match[1], "%d", &count)
			switch match[2] {
			case "passed":
				passed += count
			case "failed":
				failed += count
			case "skipped":
				skipped += count
			}
		}
	}
	return
}

func (t *RunTestsTool) parseCargoResults(output string) (passed, failed, skipped int) {
	// Cargo test output: "test result: ok. 5 passed; 0 failed; 0 ignored"
	re := regexp.MustCompile(`(\d+)\s+passed;\s+(\d+)\s+failed;\s+(\d+)\s+ignored`)
	matches := re.FindStringSubmatch(output)
	if len(matches) == 4 {
		fmt.Sscanf(matches[1], "%d", &passed)
		fmt.Sscanf(matches[2], "%d", &failed)
		fmt.Sscanf(matches[3], "%d", &skipped)
	}
	return
}

func (t *RunTestsTool) extractFailures(framework, output string) []string {
	failures := []string{}
	lines := strings.Split(output, "\n")

	switch framework {
	case "go":
		for _, line := range lines {
			if strings.Contains(line, "--- FAIL:") {
				failures = append(failures, strings.TrimSpace(line))
			}
		}
	case "jest", "pytest":
		inFailure := false
		for _, line := range lines {
			if strings.Contains(line, "FAIL") || strings.Contains(line, "ERROR") {
				inFailure = true
			}
			if inFailure {
				failures = append(failures, strings.TrimSpace(line))
				if len(failures) >= 10 {
					break
				}
			}
		}
	}

	return failures
}

// GenerateTestTool generates test scaffolding for a function or file
type GenerateTestTool struct{}

func (t *GenerateTestTool) Name() string {
	return "generate_test"
}

func (t *GenerateTestTool) Description() string {
	return "Generate test scaffolding for a function, method, or file. Auto-detects language and creates appropriate test structure with basic test cases. Supports Go, JavaScript/TypeScript, and Python. Use this to quickly create test skeletons that you can fill in."
}

func (t *GenerateTestTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"source_file": {
				Type:        "string",
				Description: "Source file containing the code to test",
			},
			"function_name": {
				Type:        "string",
				Description: "Optional: specific function to generate test for. If omitted, generates tests for all public functions.",
			},
			"test_file": {
				Type:        "string",
				Description: "Optional: output test file path. Auto-generated if not specified.",
			},
		},
		Required: []string{"source_file"},
	}
}

func (t *GenerateTestTool) Execute(params map[string]any) (*Result, error) {
	sourceFile, ok := params["source_file"].(string)
	if !ok || sourceFile == "" {
		return &Result{
			Success: false,
			Error:   "source_file parameter must be a non-empty string",
		}, nil
	}

	functionName := ""
	if fn, ok := params["function_name"].(string); ok {
		functionName = fn
	}

	testFile := ""
	if tf, ok := params["test_file"].(string); ok {
		testFile = tf
	}

	// Auto-generate test file path if not specified
	if testFile == "" {
		testFile = t.generateTestFilePath(sourceFile)
	}

	// Detect language
	language := t.detectLanguage(sourceFile)

	// Generate test content
	testContent, err := t.generateTestContent(sourceFile, functionName, language)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to generate test: %v", err),
		}, nil
	}

	// Write test file
	if err := os.WriteFile(testFile, []byte(testContent), 0644); err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to write test file: %v", err),
		}, nil
	}

	result := &Result{
		Success: true,
		Data: map[string]any{
			"source_file":   sourceFile,
			"test_file":     testFile,
			"function_name": functionName,
			"language":      language,
			"content":       testContent,
		},
		ShouldAbridge: true,
	}

	result.DisplayData = map[string]any{
		"source_file": sourceFile,
		"test_file":   testFile,
		"language":    language,
		"summary":     fmt.Sprintf("✓ Generated test file: %s", testFile),
	}

	return result, nil
}

func (t *GenerateTestTool) generateTestFilePath(sourceFile string) string {
	ext := filepath.Ext(sourceFile)
	base := strings.TrimSuffix(sourceFile, ext)

	switch ext {
	case ".go":
		return base + "_test.go"
	case ".js":
		return base + ".test.js"
	case ".ts":
		return base + ".test.ts"
	case ".py":
		return "test_" + filepath.Base(sourceFile)
	default:
		return base + "_test" + ext
	}
}

func (t *GenerateTestTool) detectLanguage(file string) string {
	ext := filepath.Ext(file)
	switch ext {
	case ".go":
		return "go"
	case ".js", ".ts":
		return "javascript"
	case ".py":
		return "python"
	default:
		return "unknown"
	}
}

func (t *GenerateTestTool) generateTestContent(sourceFile, functionName, language string) (string, error) {
	switch language {
	case "go":
		return t.generateGoTest(sourceFile, functionName)
	case "javascript":
		return t.generateJSTest(sourceFile, functionName)
	case "python":
		return t.generatePythonTest(sourceFile, functionName)
	default:
		return "", fmt.Errorf("unsupported language: %s", language)
	}
}

func (t *GenerateTestTool) generateGoTest(sourceFile, functionName string) (string, error) {
	// Read source to get package name
	content, err := os.ReadFile(sourceFile)
	if err != nil {
		return "", err
	}

	packageRe := regexp.MustCompile(`package\s+(\w+)`)
	matches := packageRe.FindStringSubmatch(string(content))
	packageName := "main"
	if len(matches) > 1 {
		packageName = matches[1]
	}

	var template string
	if functionName != "" {
		template = fmt.Sprintf(`package %s

import "testing"

func Test%s(t *testing.T) {
	tests := []struct {
		name string
		// Add test fields here
	}{
		{
			name: "basic case",
			// Add test values
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Call %s and verify results
			// got := %s(...)
			// if got != tt.want {
			//     t.Errorf("got %%v, want %%v", got, tt.want)
			// }
		})
	}
}
`, packageName, functionName, functionName, functionName)
	} else {
		template = fmt.Sprintf(`package %s

import "testing"

// TODO: Add test functions here
`, packageName)
	}

	return template, nil
}

func (t *GenerateTestTool) generateJSTest(sourceFile, functionName string) (string, error) {
	var template string
	if functionName != "" {
		template = fmt.Sprintf(`import { %s } from './%s';

describe('%s', () => {
	it('should handle basic case', () => {
		// Arrange
		const input = /* TODO: add input */;
		const expected = /* TODO: add expected output */;

		// Act
		const result = %s(input);

		// Assert
		expect(result).toBe(expected);
	});

	it('should handle edge case', () => {
		// TODO: Add edge case test
	});
});
`, functionName, filepath.Base(sourceFile), functionName, functionName)
	} else {
		template = fmt.Sprintf(`import { } from './%s';

describe('TODO: Module name', () => {
	// TODO: Add test cases
});
`, filepath.Base(sourceFile))
	}

	return template, nil
}

func (t *GenerateTestTool) generatePythonTest(sourceFile, functionName string) (string, error) {
	moduleName := strings.TrimSuffix(filepath.Base(sourceFile), ".py")

	var template string
	if functionName != "" {
		template = fmt.Sprintf(`import pytest
from %s import %s


class Test%s:
	def test_basic_case(self):
		"""Test basic functionality"""
		# Arrange
		input_value = None  # TODO: add input
		expected = None  # TODO: add expected output

		# Act
		result = %s(input_value)

		# Assert
		assert result == expected

	def test_edge_case(self):
		"""Test edge case"""
		# TODO: Add edge case test
		pass
`, moduleName, functionName, functionName, functionName)
	} else {
		template = fmt.Sprintf(`import pytest
from %s import *


# TODO: Add test classes and functions
`, moduleName)
	}

	return template, nil
}
