package review

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/tools"
)

// VerificationTools provides read-only tools for code review verification.
type VerificationTools struct {
	workDir string
}

// NewVerificationTools creates verification tools rooted at workDir.
func NewVerificationTools(workDir string) *VerificationTools {
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	return &VerificationTools{workDir: workDir}
}

// Definitions returns the tool definitions for verification.
func (v *VerificationTools) Definitions() []tools.Definition {
	return []tools.Definition{
		{
			Name:        "read_file",
			Description: "Read the contents of a file. Use this to verify claims about code structure, function definitions, or implementation details.",
			Parameters: tools.Schema{
				Type: "object",
				Properties: map[string]tools.Property{
					"path": {
						Type:        "string",
						Description: "Path to the file (relative to repo root or absolute)",
					},
					"start_line": {
						Type:        "integer",
						Description: "Optional: starting line number (1-indexed)",
					},
					"end_line": {
						Type:        "integer",
						Description: "Optional: ending line number (inclusive)",
					},
				},
				Required: []string{"path"},
			},
		},
		{
			Name:        "search_code",
			Description: "Search for a pattern in the codebase using ripgrep. Use this to find where functions, types, or patterns are defined or used.",
			Parameters: tools.Schema{
				Type: "object",
				Properties: map[string]tools.Property{
					"pattern": {
						Type:        "string",
						Description: "Regex pattern to search for",
					},
					"file_pattern": {
						Type:        "string",
						Description: "Optional: glob pattern for files to search (e.g., '*.go')",
					},
					"max_results": {
						Type:        "integer",
						Description: "Maximum number of results (default 20)",
					},
				},
				Required: []string{"pattern"},
			},
		},
		{
			Name:        "verify_build",
			Description: "Run 'go build' to verify the code compiles. Returns build errors if any.",
			Parameters: tools.Schema{
				Type: "object",
				Properties: map[string]tools.Property{
					"package": {
						Type:        "string",
						Description: "Package to build (default './...')",
					},
				},
			},
		},
		{
			Name:        "run_tests",
			Description: "Run 'go test' to verify tests pass. Returns test output including failures.",
			Parameters: tools.Schema{
				Type: "object",
				Properties: map[string]tools.Property{
					"package": {
						Type:        "string",
						Description: "Package to test (default './...')",
					},
					"run": {
						Type:        "string",
						Description: "Optional: regex pattern for specific tests to run",
					},
				},
			},
		},
		{
			Name:        "list_files",
			Description: "List files matching a glob pattern. Use to verify files exist or explore directory structure.",
			Parameters: tools.Schema{
				Type: "object",
				Properties: map[string]tools.Property{
					"pattern": {
						Type:        "string",
						Description: "Glob pattern (e.g., 'pkg/**/*.go', 'cmd/**/main.go')",
					},
				},
				Required: []string{"pattern"},
			},
		},
	}
}

// Execute runs a verification tool and returns its output.
func (v *VerificationTools) Execute(name string, args json.RawMessage) (string, error) {
	var params map[string]any
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	switch name {
	case "read_file":
		return v.readFile(params)
	case "search_code":
		return v.searchCode(params)
	case "verify_build":
		return v.verifyBuild(params)
	case "run_tests":
		return v.runTests(params)
	case "list_files":
		return v.listFiles(params)
	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

func (v *VerificationTools) readFile(params map[string]any) (string, error) {
	path, _ := params["path"].(string)
	if path == "" {
		return "", fmt.Errorf("path is required")
	}

	// Resolve path
	if !filepath.IsAbs(path) {
		path = filepath.Join(v.workDir, path)
	}

	// Security: ensure path is within workDir
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("invalid path: %w", err)
	}
	absWorkDir, _ := filepath.Abs(v.workDir)
	if !strings.HasPrefix(absPath, absWorkDir) {
		return "", fmt.Errorf("path outside repository: %s", path)
	}

	// Read file
	content, err := os.ReadFile(absPath)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	// Handle line ranges
	lines := strings.Split(string(content), "\n")
	startLine := 1
	endLine := len(lines)

	if sl, ok := params["start_line"].(float64); ok && sl > 0 {
		startLine = int(sl)
	}
	if el, ok := params["end_line"].(float64); ok && el > 0 {
		endLine = int(el)
	}

	// Clamp
	if startLine < 1 {
		startLine = 1
	}
	if endLine > len(lines) {
		endLine = len(lines)
	}
	if startLine > endLine {
		return "", fmt.Errorf("invalid line range: %d-%d", startLine, endLine)
	}

	// Format with line numbers
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("File: %s (lines %d-%d of %d)\n\n", path, startLine, endLine, len(lines)))

	for i := startLine - 1; i < endLine; i++ {
		sb.WriteString(fmt.Sprintf("%4d: %s\n", i+1, lines[i]))
	}

	return sb.String(), nil
}

func (v *VerificationTools) searchCode(params map[string]any) (string, error) {
	pattern, _ := params["pattern"].(string)
	if pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}

	maxResults := 20
	if mr, ok := params["max_results"].(float64); ok && mr > 0 {
		maxResults = int(mr)
	}

	// Build rg command
	args := []string{"-n", "--no-heading", "-m", fmt.Sprintf("%d", maxResults)}

	if fp, ok := params["file_pattern"].(string); ok && fp != "" {
		args = append(args, "-g", fp)
	}

	args = append(args, pattern, v.workDir)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "rg", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	_ = cmd.Run() // rg returns non-zero if no matches

	output := stdout.String()
	if output == "" {
		return fmt.Sprintf("No matches found for pattern: %s", pattern), nil
	}

	// Format output
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) > maxResults {
		lines = lines[:maxResults]
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d matches for '%s':\n\n", len(lines), pattern))
	for _, line := range lines {
		// Make paths relative
		line = strings.TrimPrefix(line, v.workDir+"/")
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	return sb.String(), nil
}

func (v *VerificationTools) verifyBuild(params map[string]any) (string, error) {
	pkg := "./..."
	if p, ok := params["package"].(string); ok && p != "" {
		pkg = p
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "build", "-o", "/dev/null", pkg)
	cmd.Dir = v.workDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return fmt.Sprintf("BUILD FAILED:\n%s\n%s", stderr.String(), stdout.String()), nil
	}

	return "BUILD SUCCESSFUL: No compilation errors", nil
}

func (v *VerificationTools) runTests(params map[string]any) (string, error) {
	pkg := "./..."
	if p, ok := params["package"].(string); ok && p != "" {
		pkg = p
	}

	args := []string{"test", "-v", "-count=1", pkg}

	if run, ok := params["run"].(string); ok && run != "" {
		args = append(args, "-run", run)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = v.workDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	output := stdout.String() + stderr.String()

	// Truncate if too long
	if len(output) > 10000 {
		output = output[:10000] + "\n... (truncated)"
	}

	if err != nil {
		return fmt.Sprintf("TESTS FAILED:\n%s", output), nil
	}

	return fmt.Sprintf("TESTS PASSED:\n%s", output), nil
}

func (v *VerificationTools) listFiles(params map[string]any) (string, error) {
	pattern, _ := params["pattern"].(string)
	if pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}

	// Use filepath.Glob for simple patterns, or find for complex ones
	var matches []string
	var err error

	if strings.Contains(pattern, "**") {
		// Use find for recursive patterns
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// Convert ** pattern to find -name pattern
		parts := strings.Split(pattern, "**")
		var findArgs []string

		if len(parts) == 2 && parts[0] == "" {
			// Pattern like "**/*.go"
			findArgs = []string{v.workDir, "-type", "f", "-name", strings.TrimPrefix(parts[1], "/")}
		} else {
			// Fallback: list all files
			findArgs = []string{v.workDir, "-type", "f"}
		}

		cmd := exec.CommandContext(ctx, "find", findArgs...)
		var stdout bytes.Buffer
		cmd.Stdout = &stdout

		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("find failed: %w", err)
		}

		for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
			if line != "" {
				// Make relative
				rel, _ := filepath.Rel(v.workDir, line)
				if rel != "" {
					matches = append(matches, rel)
				}
			}
		}
	} else {
		fullPattern := filepath.Join(v.workDir, pattern)
		matches, err = filepath.Glob(fullPattern)
		if err != nil {
			return "", fmt.Errorf("glob failed: %w", err)
		}

		// Make relative
		for i, m := range matches {
			rel, _ := filepath.Rel(v.workDir, m)
			matches[i] = rel
		}
	}

	if len(matches) == 0 {
		return fmt.Sprintf("No files match pattern: %s", pattern), nil
	}

	// Limit output
	if len(matches) > 50 {
		matches = matches[:50]
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d files matching '%s':\n\n", len(matches), pattern))
	for _, m := range matches {
		sb.WriteString(m)
		sb.WriteString("\n")
	}

	return sb.String(), nil
}
