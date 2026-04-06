package security

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Analyzer is the interface for security analyzers
type Analyzer interface {
	// Analyze performs security analysis on the given path
	Analyze(path string) (*Result, error)
	// Name returns the analyzer name
	Name() string
	// Description returns a description of what the analyzer checks
	Description() string
}

// AnalyzerRegistry manages available security analyzers
type AnalyzerRegistry struct {
	analyzers []Analyzer
}

// NewAnalyzerRegistry creates a new analyzer registry
func NewAnalyzerRegistry() *AnalyzerRegistry {
	return &AnalyzerRegistry{
		analyzers: []Analyzer{},
	}
}

// Register adds an analyzer to the registry
func (r *AnalyzerRegistry) Register(analyzer Analyzer) {
	r.analyzers = append(r.analyzers, analyzer)
}

// GetAnalyzers returns all registered analyzers
func (r *AnalyzerRegistry) GetAnalyzers() []Analyzer {
	return r.analyzers
}

// GetAnalyzer returns a specific analyzer by name
func (r *AnalyzerRegistry) GetAnalyzer(name string) (Analyzer, error) {
	for _, a := range r.analyzers {
		if strings.EqualFold(a.Name(), name) {
			return a, nil
		}
	}
	return nil, fmt.Errorf("analyzer %s not found", name)
}

// SecurityAnalyzer is the main security analysis coordinator
type SecurityAnalyzer struct {
	registry *AnalyzerRegistry
	config   Config
}

// Config configures the security analyzer
type Config struct {
	SeverityThreshold Severity // Minimum severity to report
	IncludeTests      bool     // Include test files in analysis
	ExcludeDirs       []string // Directories to exclude
	MaxFileSize       int64    // Max file size to scan (bytes)
	ContextLines      int      // Lines of context to include
}

// DefaultConfig returns default configuration
func DefaultConfig() Config {
	return Config{
		SeverityThreshold: SeverityLow,
		IncludeTests:      true,
		ExcludeDirs:       []string{"vendor", "node_modules", ".git", "dist", "build"},
		MaxFileSize:       1 * 1024 * 1024, // 1MB
		ContextLines:      3,
	}
}

// NewSecurityAnalyzer creates a new security analyzer
func NewSecurityAnalyzer(config Config) *SecurityAnalyzer {
	registry := NewAnalyzerRegistry()

	// Register built-in analyzers
	registry.Register(NewInputValidationAnalyzer(config))
	registry.Register(NewAuthPatternsAnalyzer(config))
	registry.Register(NewSecretsAnalyzer(config))

	return &SecurityAnalyzer{
		registry: registry,
		config:   config,
	}
}

// Analyze performs security analysis on a project
func (sa *SecurityAnalyzer) Analyze(projectPath string) (*Result, error) {
	startTime := time.Now()
	result := NewResult()

	// Ensure path is absolute
	absPath, err := filepath.Abs(projectPath)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path: %w", err)
	}

	// Check if directory exists
	info, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("cannot access path %s: %w", absPath, err)
	}

	if !info.IsDir() {
		return nil, fmt.Errorf("path %s is not a directory", absPath)
	}

	// Run all registered analyzers
	for _, analyzer := range sa.registry.GetAnalyzers() {
		analyzerResult, err := analyzer.Analyze(absPath)
		if err != nil {
			// Log error but continue with other analyzers
			fmt.Printf("Warning: Analyzer %s failed: %v\n", analyzer.Name(), err)
			continue
		}
		result.AddFindings(analyzerResult.Findings)
	}

	result.ScanTime = time.Since(startTime).Milliseconds()

	// Filter by severity threshold
	var filteredFindings []SecurityFinding
	for _, finding := range result.Findings {
		if finding.Severity >= sa.config.SeverityThreshold {
			filteredFindings = append(filteredFindings, finding)
		}
	}
	result.Findings = filteredFindings

	return result, nil
}

// GetAnalyzerRegistry returns the analyzer registry
func (sa *SecurityAnalyzer) GetAnalyzerRegistry() *AnalyzerRegistry {
	return sa.registry
}

// fileInfo represents information about a file being analyzed
type fileInfo struct {
	Path    string
	Content string
	Lines   []string
	LineNum int
	Ext     string
}

// shouldIncludePath checks if a file should be included in analysis
func shouldIncludePath(path string, config Config) bool {
	// Exclude directories
	for _, excludeDir := range config.ExcludeDirs {
		if strings.Contains(path, "/"+excludeDir+"/") {
			return false
		}
	}

	// Get extension
	ext := strings.ToLower(filepath.Ext(path))

	// Only analyze source code files
	supportedExts := map[string]bool{
		".go":    true,
		".py":    true,
		".js":    true,
		".ts":    true,
		".jsx":   true,
		".tsx":   true,
		".rs":    true,
		".java":  true,
		".php":   true,
		".rb":    true,
		".cs":    true,
		".cpp":   true,
		".c":     true,
		".h":     true,
		".swift": true,
		".kt":    true,
		".scala": true,
		".bash":  true,
		".sh":    true,
		".yaml":  true,
		".yml":   true,
		".json":  true,
		".xml":   true,
		".html":  true,
		".vue":   true,
	}

	// Check if test file
	isTestFile := strings.Contains(path, "_test") || strings.Contains(path, ".test.") ||
		strings.Contains(path, "test_") || strings.Contains(filepath.Base(path), "test.")

	if isTestFile && !config.IncludeTests {
		return false
	}

	return supportedExts[ext]
}

// scanFile scans a single file and invokes the provided callback for each line
func scanFile(path string, config Config, callback func(line string, lineNum int) error) error {
	// Check file size
	info, err := os.Stat(path)
	if err != nil {
		return err
	}

	if info.Size() > config.MaxFileSize {
		return fmt.Errorf("file too large: %d bytes", info.Size())
	}

	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNum := 1
	for scanner.Scan() {
		line := scanner.Text()
		if err := callback(line, lineNum); err != nil {
			return err
		}
		lineNum++
	}

	return scanner.Err()
}

// helper function to read file content
func readFileContent(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

// baseAnalyzer provides common functionality for all analyzers
type baseAnalyzer struct {
	config Config
}

// walkFiles walks through directory tree and calls visitor for each matching file
func (b *baseAnalyzer) walkFiles(root string, visitor func(string) error) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip directories
		if info.IsDir() {
			return nil
		}

		// Check if file should be included
		if !shouldIncludePath(path, b.config) {
			return nil
		}

		return visitor(path)
	})
}

// matchPattern checks if a pattern matches in a line
func (b *baseAnalyzer) matchPattern(line, pattern string) (bool, []string) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false, nil
	}

	matches := re.FindStringSubmatch(line)
	return matches != nil, matches
}

// getContextLines gets surrounding lines for context
func (b *baseAnalyzer) getContextLines(lines []string, lineNum, context int) []string {
	start := lineNum - context - 1
	if start < 0 {
		start = 0
	}

	end := lineNum + context
	if end > len(lines) {
		end = len(lines)
	}

	return lines[start:end]
}

// createFinding creates a security finding with common setup
func (b *baseAnalyzer) createFinding(category Category, severity Severity, title, description string) *SecurityFinding {
	return NewFinding(category, severity, title, description)
}

// ConfigFromYAML creates config from YAML/JSON
func ConfigFromYAML(data map[string]any) Config {
	config := DefaultConfig()

	if threshold, ok := data["severity_threshold"]; ok {
		switch v := threshold.(type) {
		case float64:
			config.SeverityThreshold = Severity(v)
		case int:
			config.SeverityThreshold = Severity(v)
		}
	}

	if includeTests, ok := data["include_tests"]; ok {
		if v, ok := includeTests.(bool); ok {
			config.IncludeTests = v
		}
	}

	if excludeDirs, ok := data["exclude_dirs"]; ok {
		if dirs, ok := excludeDirs.([]any); ok {
			for _, dir := range dirs {
				if s, ok := dir.(string); ok {
					config.ExcludeDirs = append(config.ExcludeDirs, s)
				}
			}
		}
	}

	return config
}
