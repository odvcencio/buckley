package hunt

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// TODOAnalyzer scans for TODO/FIXME/BUG/HACK comments
type TODOAnalyzer struct{}

func (a *TODOAnalyzer) Name() string {
	return "todo"
}

func (a *TODOAnalyzer) Analyze(rootPath string) ([]ImprovementSuggestion, error) {
	suggestions := []ImprovementSuggestion{}

	// Patterns to match
	patterns := map[string]int{
		"TODO":  5, // Medium severity
		"FIXME": 7, // Higher severity
		"BUG":   9, // Critical
		"HACK":  6, // Moderate severity
	}

	todoRegex := regexp.MustCompile(`(?i)//\s*(TODO|FIXME|BUG|HACK):?\s*(.*)`)

	err := filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip files we can't read
		}

		// Skip directories and non-Go files for now
		if info.IsDir() {
			if strings.HasPrefix(info.Name(), ".") || info.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}

		// Only analyze Go files
		if !strings.HasSuffix(path, ".go") {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return nil // Skip files we can't open
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()

			matches := todoRegex.FindStringSubmatch(line)
			if len(matches) >= 3 {
				keyword := strings.ToUpper(matches[1])
				message := strings.TrimSpace(matches[2])

				relPath, _ := filepath.Rel(rootPath, path)

				suggestions = append(suggestions, ImprovementSuggestion{
					ID:          fmt.Sprintf("todo-%s-%d", filepath.Base(path), lineNum),
					Category:    "tech-debt",
					Severity:    patterns[keyword],
					File:        relPath,
					LineStart:   lineNum,
					LineEnd:     lineNum,
					Snippet:     strings.TrimSpace(line),
					Rationale:   fmt.Sprintf("%s comment: %s", keyword, message),
					Effort:      estimateEffort(keyword, message),
					AutoFixable: false,
				})
			}
		}

		return nil
	})

	return suggestions, err
}

// estimateEffort guesses effort based on keyword and message
func estimateEffort(keyword, message string) string {
	msgLower := strings.ToLower(message)

	// Simple heuristics
	if keyword == "BUG" {
		return "medium"
	}

	if strings.Contains(msgLower, "refactor") || strings.Contains(msgLower, "rewrite") {
		return "large"
	}

	if strings.Contains(msgLower, "add") || strings.Contains(msgLower, "implement") {
		return "medium"
	}

	return "small"
}

// LintAnalyzer runs golangci-lint and parses output
type LintAnalyzer struct{}

func (a *LintAnalyzer) Name() string {
	return "lint"
}

func (a *LintAnalyzer) Analyze(rootPath string) ([]ImprovementSuggestion, error) {
	suggestions := []ImprovementSuggestion{}

	// Check if golangci-lint is available
	if _, err := exec.LookPath("golangci-lint"); err != nil {
		return nil, fmt.Errorf("golangci-lint not found in PATH: %w", err)
	}

	cmd := exec.Command("golangci-lint", "run", "--out-format", "json", "./...")
	cmd.Dir = rootPath

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// golangci-lint exits with code 1 when issues found, so we don't check error
	_ = cmd.Run()

	// Parse JSON output
	var result struct {
		Issues []struct {
			FromLinter string `json:"FromLinter"`
			Text       string `json:"Text"`
			Pos        struct {
				Filename string `json:"Filename"`
				Line     int    `json:"Line"`
				Column   int    `json:"Column"`
			} `json:"Pos"`
			Severity string `json:"Severity"`
		} `json:"Issues"`
	}

	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		// If JSON parsing fails, try to parse line-by-line format
		return parseLintText(stdout.String(), rootPath)
	}

	for _, issue := range result.Issues {
		relPath, _ := filepath.Rel(rootPath, issue.Pos.Filename)

		severity := 5 // default
		switch issue.Severity {
		case "error":
			severity = 8
		case "warning":
			severity = 5
		case "info":
			severity = 3
		}

		suggestions = append(suggestions, ImprovementSuggestion{
			ID:          fmt.Sprintf("lint-%s-%d", filepath.Base(issue.Pos.Filename), issue.Pos.Line),
			Category:    "readability",
			Severity:    severity,
			File:        relPath,
			LineStart:   issue.Pos.Line,
			LineEnd:     issue.Pos.Line,
			Snippet:     "",
			Rationale:   fmt.Sprintf("%s: %s", issue.FromLinter, issue.Text),
			Effort:      lintEffort(issue.FromLinter),
			AutoFixable: isAutoFixable(issue.FromLinter),
		})
	}

	return suggestions, nil
}

// parseLintText parses plain text lint output as fallback
func parseLintText(output, rootPath string) ([]ImprovementSuggestion, error) {
	suggestions := []ImprovementSuggestion{}

	// Pattern: file.go:123:45: message (linter)
	lineRegex := regexp.MustCompile(`^(.+):(\d+):(\d+):\s*(.+?)\s*\((\w+)\)`)

	for _, line := range strings.Split(output, "\n") {
		matches := lineRegex.FindStringSubmatch(line)
		if len(matches) >= 6 {
			lineNum, _ := strconv.Atoi(matches[2])
			relPath, _ := filepath.Rel(rootPath, matches[1])

			suggestions = append(suggestions, ImprovementSuggestion{
				ID:          fmt.Sprintf("lint-%s-%d", filepath.Base(matches[1]), lineNum),
				Category:    "readability",
				Severity:    5,
				File:        relPath,
				LineStart:   lineNum,
				LineEnd:     lineNum,
				Snippet:     "",
				Rationale:   matches[4],
				Effort:      "small",
				AutoFixable: false,
			})
		}
	}

	return suggestions, nil
}

func lintEffort(linter string) string {
	// Some linters indicate more complex issues
	complexLinters := map[string]bool{
		"gocyclo":  true,
		"gocognit": true,
		"maintidx": true,
		"funlen":   true,
		"nestif":   true,
	}

	if complexLinters[linter] {
		return "medium"
	}

	return "small"
}

func isAutoFixable(linter string) bool {
	autoFixableLinters := map[string]bool{
		"gofmt":      true,
		"goimports":  true,
		"gocritic":   true,
		"misspell":   true,
		"whitespace": true,
	}

	return autoFixableLinters[linter]
}

// DependencyAnalyzer checks for outdated Go modules
type DependencyAnalyzer struct{}

func (a *DependencyAnalyzer) Name() string {
	return "dependency"
}

func (a *DependencyAnalyzer) Analyze(rootPath string) ([]ImprovementSuggestion, error) {
	suggestions := []ImprovementSuggestion{}

	// Check if go.mod exists
	goModPath := filepath.Join(rootPath, "go.mod")
	if _, err := os.Stat(goModPath); os.IsNotExist(err) {
		return suggestions, nil // Not a Go project
	}

	// Run go list -u -m -json all
	cmd := exec.Command("go", "list", "-u", "-m", "-json", "all")
	cmd.Dir = rootPath

	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to run go list: %w", err)
	}

	// Parse JSON output (newline-delimited JSON objects)
	decoder := json.NewDecoder(&stdout)
	for decoder.More() {
		var module struct {
			Path    string `json:"Path"`
			Version string `json:"Version"`
			Update  *struct {
				Version string `json:"Version"`
			} `json:"Update"`
			Indirect bool `json:"Indirect"`
		}

		if err := decoder.Decode(&module); err != nil {
			continue
		}

		if module.Update != nil && module.Update.Version != "" {
			severity := 4
			if !module.Indirect {
				severity = 6 // Direct dependencies are more important
			}

			suggestions = append(suggestions, ImprovementSuggestion{
				ID:          fmt.Sprintf("dep-%s", module.Path),
				Category:    "dependency",
				Severity:    severity,
				File:        "go.mod",
				LineStart:   0,
				LineEnd:     0,
				Snippet:     fmt.Sprintf("%s %s", module.Path, module.Version),
				Rationale:   fmt.Sprintf("Update available: %s → %s", module.Version, module.Update.Version),
				Effort:      "trivial",
				AutoFixable: true,
			})
		}
	}

	return suggestions, nil
}
