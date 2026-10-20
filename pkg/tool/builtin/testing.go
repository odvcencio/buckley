package builtin

import (
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Pre-compiled regex patterns for test output parsing.
var cargoResultRe = regexp.MustCompile(`(?m)^test result: (?:ok|FAILED)\. (\d+)\s+passed;\s+(\d+)\s+failed;\s+(\d+)\s+ignored`)

// RunTestsTool runs tests with smart filtering and reporting
type RunTestsTool struct{ workDirAware }

type testReport struct {
	passed, failed, skipped int
	complete                bool
	verificationError       string
	goResult                *goTestReport
	outputBytes             int64
	outputTruncated         bool
	failureDetails          [20]string
	failureCount            int
}

// execCommandContext is overridden in tests to stub command execution.
var execCommandContext = exec.CommandContext

// errNoTestFramework reports that no test framework was detected at the path.
var errNoTestFramework = errors.New("unsupported test framework")

func (t *RunTestsTool) Name() string {
	return "run_tests"
}

func (t *RunTestsTool) TrustedVerification() bool {
	return true
}

func (t *RunTestsTool) Description() string {
	return "Run tests with optional path/pattern filtering and framework detection. Omit path to test every Go package (./...); path . tests one package. Streams Go/Cargo counts while retaining bounded output and failure details; truncation and original readable output bytes are reported. Jest/Pytest reports have an 8 MiB capture budget; Unix streams report payloads without disk storage. Complete results are capped at 24 KiB (or the configured smaller limit). Evidence-budget failures cannot verify a pass. A workspace with no framework returns unavailable."
}

func (t *RunTestsTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			// No default: a model copies a schema default into every call, and a
			// path of "." names the root package alone for Go. Omitting the path
			// tests the whole workspace.
			"path": {
				Type:        "string",
				Description: "Optional: directory or file to test. Omit it to test everything under the workspace root (for Go, every package). Name a path such as ./pkg/a to test one package.",
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
				Description: "Test timeout in seconds (default 300, range 1-3600); the configured tool execution limit may shorten it.",
				Default:     300,
			},
		},
		Required: []string{},
	}
}

func (t *RunTestsTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}

func (t *RunTestsTool) ExecuteWithContext(ctx context.Context, params map[string]any) (*Result, error) {
	testPath := "."
	explicitPath := false
	if p, ok := params["path"].(string); ok && p != "" {
		testPath = p
		explicitPath = true
	}
	if len(testPath) > 8192 {
		return &Result{Error: "test path exceeds metadata budget"}, nil
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
	if len(pattern) > 8192 {
		return &Result{Error: "test pattern exceeds metadata budget"}, nil
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
	if value := params["timeout_seconds"]; value != nil {
		var err error
		timeout, err = positiveIntParameter("timeout_seconds", value)
		if err != nil || timeout > 3600 {
			return &Result{Error: "timeout_seconds must be an integer between 1 and 3600"}, nil
		}
	}

	if ctx == nil {
		ctx = context.Background()
	}
	ctx, execCancel := t.execContextWithParent(ctx)
	defer execCancel()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		defer cancel()
	}

	// Detect test framework
	framework := t.detectTestFramework(absTestPath)
	// pytest, jest, and cargo test the whole directory tree by default, but
	// "go test ." tests only the package in the current directory, and fails
	// outright when that directory holds none. When the caller named no path,
	// test every package, as the other frameworks do. A path the caller names is
	// used exactly.
	if framework == "go" && !explicitPath && testPath == "." {
		testPath = "./..."
	}

	// Run tests
	output, exitCode, duration, structuredReport, err := t.runTestsForFramework(ctx, framework, testPath, pattern, coverage, verbose)
	if err != nil {
		failed := &Result{
			Success: false,
			Error:   fmt.Sprintf("test execution failed: %v", err),
		}
		// A workspace with no test framework, or a machine without the test
		// toolchain, gives no evidence about the code. Mark it so the
		// completion contract records an unavailable check, not a failed one.
		switch {
		case errors.Is(err, errNoTestFramework):
			failed.Error += ". Nothing ran and this is not a test failure; this workspace has no test framework at " + testPath + ", so there is nothing to verify with run_tests"
			failed.Data = verificationNotRun(VerificationStatusUnavailable, "no test framework was detected in the workspace")
		case errors.Is(err, exec.ErrNotFound):
			failed.Data = verificationNotRun(VerificationStatusUnavailable, "the test toolchain is not installed on this machine")
		}
		return failed, nil
	}

	// Parse results
	passed, failed, skipped := 0, 0, 0
	verificationError := ""
	if structuredReport != nil {
		passed, failed, skipped = structuredReport.passed, structuredReport.failed, structuredReport.skipped
		verificationError = structuredReport.verificationError
		if exitCode == 0 && !structuredReport.complete && verificationError == "" {
			verificationError = framework + " did not produce a complete structured test report"
		}
	}
	var goFailures []string
	buildOnly := false
	if framework == "go" {
		report := *structuredReport.goResult
		passed, failed, skipped = report.passed, report.failed, report.skipped
		buildOnly = report.buildOnly && pattern == ""
		output = report.output
		goFailures = report.failures
		if exitCode == 0 && !report.complete && verificationError == "" {
			verificationError = "go test did not produce a complete test report"
		}
	}
	ranNoTests := false
	if exitCode == 0 && verificationError == "" {
		switch {
		case failed > 0:
			verificationError = framework + " test reported failed tests"
		case passed+skipped == 0 && !buildOnly:
			verificationError = framework + " test ran no tests; check the path and pattern"
			ranNoTests = true
		case passed == 0 && !buildOnly:
			verificationError = framework + " test skipped every test; no passing tests verified"
		}
	}
	if exitCode != 0 {
		verificationError = fmt.Sprintf("test command exited with code %d", exitCode)
	}

	result := &Result{
		Success: exitCode == 0 && verificationError == "",
		Error:   verificationError,
		Data: map[string]any{
			"framework":        framework,
			"build_only":       buildOnly,
			"path":             strings.Clone(testPath),
			"pattern":          strings.Clone(pattern),
			"passed":           passed,
			"failed":           failed,
			"skipped":          skipped,
			"duration":         duration,
			"exit_code":        exitCode,
			"output":           output,
			"output_bytes":     structuredReport.outputBytes,
			"output_truncated": structuredReport.outputTruncated,
			"coverage":         coverage,
		},
	}
	// With no pattern to blame, a clean run that found no tests to run means the
	// workspace has nothing to verify, not that a test failed.
	if ranNoTests && pattern == "" {
		for key, value := range verificationNotRun(VerificationStatusUnavailable, framework+" found no tests to run at "+testPath) {
			result.Data[key] = value
		}
	}

	// Abridge long output
	if len(output) > 5000 {
		result.ShouldAbridge = true
		summary := fmt.Sprintf("✓ %d passed, ✗ %d failed, ⊘ %d skipped (%.2fs)", passed, failed, skipped, duration)
		if !result.Success {
			summary = "Tests FAILED: " + result.Error + "; " + summary
		}

		// Extract failure details
		var failureDetails []string
		if framework == "go" {
			failureDetails = goFailures
		} else {
			failureDetails = structuredReport.failureDetails[:structuredReport.failureCount]
		}

		result.DisplayData = map[string]any{
			"exit_code":        exitCode,
			"error":            result.Error,
			"framework":        framework,
			"passed":           passed,
			"failed":           failed,
			"skipped":          skipped,
			"duration":         duration,
			"summary":          summary,
			"failures":         failureDetails,
			"output_tail":      outputTail(output, verificationTailBytes),
			"output_bytes":     structuredReport.outputBytes,
			"output_truncated": true,
		}
	}

	budget := DefaultTestResultBytes
	if t.maxOutputBytes > 0 {
		budget = min(budget, t.maxOutputBytes)
	}
	return boundTestResult(result, budget)
}

func (t *RunTestsTool) detectTestFramework(path string) string {
	// Check for framework indicators
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		switch filepath.Ext(path) {
		case ".go":
			return "go"
		case ".js", ".jsx", ".ts", ".tsx", ".cjs", ".mjs":
			return "jest"
		case ".py":
			return "pytest"
		}
	}
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
	table := []struct {
		pattern   string
		framework string
	}{
		{"*_test.go", "go"},
		{"*.test.js", "jest"},
		{"test_*.py", "pytest"},
		{"*.go", "go"},
	}
	if dir, err := os.Open(path); err == nil {
		defer dir.Close()
		best := len(table)
		for {
			entries, err := dir.ReadDir(directoryReadBatch)
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				for i, candidate := range table[:best] {
					if matched, _ := filepath.Match(candidate.pattern, entry.Name()); matched {
						best = i
						break
					}
				}
			}
			if best == 0 {
				return table[best].framework
			}
			if err != nil {
				if best < len(table) {
					return table[best].framework
				}
				break
			}
		}
	}

	return "unknown"
}

func (t *RunTestsTool) runTestsForFramework(ctx context.Context, framework, path, pattern string, coverage, verbose bool) (string, int, float64, *testReport, error) {
	var (
		cmd *exec.Cmd
	)
	var reportPath string
	var reportCapture *testReportCapture
	var jestPath string
	commandDir := strings.TrimSpace(t.workDir)

	if framework == "pytest" || framework == "jest" {
		dir, err := os.MkdirTemp("", "buckley-test-report-")
		if err != nil {
			return "", 1, 0, nil, fmt.Errorf("create test report directory: %w", err)
		}
		defer os.RemoveAll(dir)
		reportPath = filepath.Join(dir, "results")
		reportCapture, err = newTestReportCapture(reportPath, min(int64(DefaultTestReportBytes), t.mutationFileLimit()))
		if err != nil {
			return "", 1, 0, nil, err
		}
		defer reportCapture.close()
	}

	switch framework {
	case "go":
		args := []string{"test", "-json"}
		if coverage {
			args = append(args, "-cover")
		}
		if verbose {
			args = append(args, "-v")
		}
		if pattern != "" {
			args = append(args, "-run", pattern)
		}
		args = append(args, localGoTestPath(path))
		cmd = execCommandContext(ctx, "go", args...)

	case "jest":
		resolved, err := resolvePath(t.workDir, path)
		if err != nil {
			return "", 1, 0, nil, fmt.Errorf("resolving jest test path: %w", err)
		}
		jestPath = resolved
		info, err := os.Stat(jestPath)
		if err != nil {
			return "", 1, 0, nil, fmt.Errorf("stat jest test path: %w", err)
		}
		if info.IsDir() {
			commandDir = jestPath
		} else {
			commandDir = filepath.Dir(jestPath)
		}

		args := []string{"test", "--", "--json", "--outputFile", reportPath}
		if coverage {
			args = append(args, "--coverage")
		}
		if verbose {
			args = append(args, "--verbose")
		}
		if pattern != "" {
			args = append(args, "-t", pattern)
		}
		if info.IsDir() {
			args = append(args, "^"+regexp.QuoteMeta(strings.TrimSuffix(filepath.ToSlash(jestPath), "/")+"/"))
		} else {
			args = append(args, "^"+regexp.QuoteMeta(filepath.ToSlash(jestPath))+"$")
		}
		cmd = execCommandContext(ctx, "npm", args...)

	case "pytest":
		args := []string{"--junitxml", reportPath}
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
		args := []string{"test", "--manifest-path", filepath.Join(path, "Cargo.toml")}
		if pattern != "" {
			args = append(args, pattern)
		}
		cmd = execCommandContext(ctx, "cargo", args...)

	default:
		return "", 1, 0, nil, fmt.Errorf("%w: %s", errNoTestFramework, framework)
	}

	stdout := testStream{framework: framework}
	if framework == "go" {
		stdout.goReport = &goTestStream{}
	}
	stderr := testStream{}
	if framework != "go" {
		stderr.framework = framework
	}
	cmd.Dir = commandDir
	cmd.Env = mergeEnv(cmd.Env, t.env)
	if framework == "pytest" {
		cmd.Env = mergeEnv(cmd.Env, map[string]string{
			"PYTHONDONTWRITEBYTECODE": "1",
			"PYTHONPYCACHEPREFIX":     filepath.Join(filepath.Dir(reportPath), "pycache"),
		})
	}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	configureCommandCancellation(cmd)

	start := time.Now()
	err := cmd.Run()
	stdout.finish()
	stderr.finish()
	duration := time.Since(start).Seconds()

	exitCode := 0
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return stdout.text.String() + stderr.text.String(), 1, duration, nil, fmt.Errorf("test run exceeded timeout: %w", context.DeadlineExceeded)
		}
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return stdout.text.String() + stderr.text.String(), 1, duration, nil, fmt.Errorf("test run canceled: %w", context.Canceled)
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return "", 1, duration, nil, err
		}
	}

	output := stdout.text.String() + stderr.text.String()
	report := &testReport{complete: true}
	if framework == "go" {
		parsed := stdout.goReport.finish(output)
		report.goResult = &parsed
		report.verificationError = stdout.goReport.problem
		if report.verificationError == "" && stdout.goReport.failedRun {
			report.verificationError = "go test reported a failed package or build"
		}
	}
	if framework == "cargo" {
		report.passed = stdout.cargo.passed + stderr.cargo.passed
		report.failed = stdout.cargo.failed + stderr.cargo.failed
		report.skipped = stdout.cargo.skipped + stderr.cargo.skipped
		report.verificationError = stdout.cargo.verificationError
		if report.verificationError == "" {
			report.verificationError = stderr.cargo.verificationError
		}
	}
	if reportPath != "" {
		parsed := testReport{}
		if raw, exceeded, err := reportCapture.bytes(ctx); err == nil && !exceeded {
			if framework == "jest" {
				parsed = parseJestReport(raw, jestPath)
			} else {
				parsed = parsePytestReport(raw)
			}
		} else if exceeded {
			parsed.verificationError = "structured test report exceeds the capture byte budget; narrow the path or pattern"
		} else if err != nil {
			parsed.verificationError = "structured test report capture failed: " + err.Error()
		}
		report = &parsed
	}
	report.outputBytes = stdout.text.total + stderr.text.total
	report.outputTruncated = stdout.text.total > DefaultTestCaptureBytes || stderr.text.total > DefaultTestCaptureBytes || stdout.oversized || stderr.oversized
	report.failureCount = copy(report.failureDetails[:], stdout.failures)
	report.failureCount += copy(report.failureDetails[report.failureCount:], stderr.failures)
	return output, exitCode, duration, report, nil
}

func localGoTestPath(path string) string {
	path = filepath.ToSlash(strings.TrimSpace(path))
	if path == "" || path == "." || strings.HasPrefix(path, ".") || filepath.IsAbs(path) {
		return path
	}
	return "./" + path
}

func (t *RunTestsTool) parseCargoResults(output string) (passed, failed, skipped int) {
	for _, matches := range cargoResultRe.FindAllStringSubmatch(output, -1) {
		if len(matches) != 4 {
			continue
		}
		var p, f, s int
		fmt.Sscanf(matches[1], "%d", &p)
		fmt.Sscanf(matches[2], "%d", &f)
		fmt.Sscanf(matches[3], "%d", &s)
		passed += p
		failed += f
		skipped += s
	}
	return
}

// GenerateTestTool generates test scaffolding for a function or file
type GenerateTestTool struct{ workDirAware }

func (t *GenerateTestTool) Name() string {
	return "generate_test"
}

func (t *GenerateTestTool) Description() string {
	return "Generate skipped test scaffolds for Go, JS/TS, or Python. Implement behavioral assertions and remove the skip before using run_tests as verification."
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
				Description: "Optional target function or method label. Omit for a file-level skipped scaffold; assertions are not inferred.",
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

	if strings.TrimSpace(t.workDir) != "" {
		abs, err := resolvePath(t.workDir, sourceFile)
		if err != nil {
			return &Result{Success: false, Error: err.Error()}, nil
		}
		sourceFile = abs
	}

	// Auto-generate test file path if not specified
	if testFile == "" {
		testFile = t.generateTestFilePath(sourceFile)
	}

	if strings.TrimSpace(t.workDir) != "" {
		resolved, err := resolvePath(t.workDir, testFile)
		if err != nil {
			return &Result{Error: err.Error()}, nil
		}
		testFile = resolved
	}

	// Detect language
	language := t.detectLanguage(sourceFile)

	if int64(len(functionName)) > t.mutationFileLimit() || int64(len(filepath.Base(sourceFile))) > t.mutationFileLimit() {
		return &Result{Error: mutationBudgetError(t.mutationFileLimit()).Error()}, nil
	}

	// Generate test content
	testContent, err := t.generateTestContent(sourceFile, functionName, language)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to generate test: %v", err),
		}, nil
	}

	if int64(len(testContent)) > t.mutationFileLimit() {
		return &Result{Error: mutationBudgetError(t.mutationFileLimit()).Error()}, nil
	}

	// Write test file
	if err := writeMutationFile(testFile, testContent, 0644); err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to write test file: %v", err),
		}, nil
	}

	result := &Result{
		Success: true,
		Data: map[string]any{
			"source_file":             sourceFile,
			"test_file":               testFile,
			"function_name":           functionName,
			"language":                language,
			"content":                 testContent,
			"status":                  "scaffold",
			"requires_implementation": true,
		},
		ShouldAbridge: true,
	}

	result.DisplayData = map[string]any{
		"source_file":             sourceFile,
		"test_file":               testFile,
		"language":                language,
		"summary":                 fmt.Sprintf("Created skipped test scaffold: %s; add assertions and remove the skip before verification", testFile),
		"status":                  "scaffold",
		"requires_implementation": true,
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
		return filepath.Join(filepath.Dir(sourceFile), "test_"+filepath.Base(sourceFile))
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
	if language == "javascript" || language == "python" {
		info, err := os.Stat(sourceFile)
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("%s is not a regular file", sourceFile)
		}
		if info.Size() > t.mutationFileLimit() {
			return "", mutationBudgetError(t.mutationFileLimit())
		}
	}

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

const scaffoldSkipReason = "TODO: implement behavioral assertions, then remove this skip"

func (t *GenerateTestTool) generateGoTest(sourceFile, functionName string) (string, error) {
	content, err := t.readMutationFile(sourceFile)
	if err != nil {
		return "", err
	}
	source, err := parser.ParseFile(token.NewFileSet(), sourceFile, content, parser.PackageClauseOnly|parser.SkipObjectResolution)
	if err != nil {
		return "", fmt.Errorf("read source package: %w", err)
	}
	text := generatedText{limit: t.mutationFileLimit()}
	text.append("package ", source.Name.Name, "\n\nimport \"testing\"\n\nfunc Test_")
	appendScaffoldIdentifier(&text, strings.TrimSuffix(filepath.Base(sourceFile), filepath.Ext(sourceFile)))
	if functionName == "" {
		text.append("_File")
	} else {
		text.append("_Target_")
		appendScaffoldIdentifier(&text, functionName)
	}
	text.append("(t *testing.T) {\n\tt.Skip(", strconv.Quote(scaffoldSkipReason), ")\n\t// Arrange inputs, call the subject, and assert its behavior.\n}\n")
	return text.result()
}

func (t *GenerateTestTool) generateJSTest(sourceFile, functionName string) (string, error) {
	label := functionName
	if label == "" {
		label = filepath.Base(sourceFile)
	}
	text := generatedText{limit: t.mutationFileLimit()}
	text.append("// TODO: import the subject from ", strconv.Quote("./"+filepath.Base(sourceFile)), ".\n\ndescribe(", strconv.Quote(label), ", () => {\n  it.skip(", strconv.Quote(scaffoldSkipReason), ", () => {\n    // Arrange inputs, call the subject, and assert its behavior.\n  });\n});\n")
	return text.result()
}

func (t *GenerateTestTool) generatePythonTest(sourceFile, functionName string) (string, error) {
	text := generatedText{limit: t.mutationFileLimit()}
	text.append("import pytest\n\n# TODO: import the subject from ", strconv.Quote(filepath.Base(sourceFile)), ".\n\ndef test_")
	if functionName == "" {
		text.append("file_scaffold")
	} else {
		appendScaffoldIdentifier(&text, functionName)
	}
	text.append("():\n    pytest.skip(", strconv.Quote(scaffoldSkipReason), ")\n    # Arrange inputs, call the subject, and assert its behavior.\n")
	return text.result()
}

func appendScaffoldIdentifier(text *generatedText, label string) {
	for _, r := range label {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if !text.append(string(r)) {
				return
			}
		} else {
			if !text.append("_", strconv.FormatInt(int64(r), 16), "_") {
				return
			}
		}
	}
}
