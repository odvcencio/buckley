package dream

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CodebaseAnalysis represents the analysis of a codebase
type CodebaseAnalysis struct {
	RootPath         string
	Languages        map[string]int // Language -> file count
	TotalFiles       int
	TotalLines       int
	PackageStructure map[string][]string // Package -> files
	EntryPoints      []string
	Dependencies     []string
	Architecture     ArchitecturePattern
	Gaps             []Gap
}

// ArchitecturePattern represents detected architectural patterns
type ArchitecturePattern struct {
	Type       string // "monolith", "microservices", "library", "cli", "web"
	Confidence float64
	Indicators []string
}

// Gap represents a missing or underutilized area
type Gap struct {
	Category    string // "testing", "docs", "monitoring", "security", etc.
	Severity    string // "critical", "important", "nice-to-have"
	Description string
	Suggestion  string
}

// Analyzer analyzes codebases for dream mode suggestions
type Analyzer struct {
	rootPath string
}

// NewAnalyzer creates a new dream mode analyzer
func NewAnalyzer(rootPath string) *Analyzer {
	return &Analyzer{rootPath: rootPath}
}

// Analyze performs comprehensive codebase analysis
func (a *Analyzer) Analyze() (*CodebaseAnalysis, error) {
	analysis := &CodebaseAnalysis{
		RootPath:         a.rootPath,
		Languages:        make(map[string]int),
		PackageStructure: make(map[string][]string),
	}

	// Scan directory structure
	if err := a.scanDirectory(analysis); err != nil {
		return nil, fmt.Errorf("failed to scan directory: %w", err)
	}

	// Detect architecture
	analysis.Architecture = a.detectArchitecture(analysis)

	// Find gaps
	analysis.Gaps = a.findGaps(analysis)

	return analysis, nil
}

// scanDirectory scans the directory structure
func (a *Analyzer) scanDirectory(analysis *CodebaseAnalysis) error {
	return filepath.Walk(a.rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip errors
		}

		// Skip vendor and hidden directories
		if info.IsDir() {
			if strings.HasPrefix(info.Name(), ".") || info.Name() == "vendor" || info.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}

		// Count by language
		ext := filepath.Ext(path)
		lang := extensionToLanguage(ext)
		if lang != "" {
			analysis.Languages[lang]++
			analysis.TotalFiles++

			// Count lines
			lines, _ := countLines(path)
			analysis.TotalLines += lines

			// Track package structure for Go
			if lang == "Go" {
				pkg := filepath.Dir(path)
				relPkg, _ := filepath.Rel(a.rootPath, pkg)
				analysis.PackageStructure[relPkg] = append(analysis.PackageStructure[relPkg], filepath.Base(path))
			}

			// Detect entry points
			if isEntryPoint(path, info.Name()) {
				relPath, _ := filepath.Rel(a.rootPath, path)
				analysis.EntryPoints = append(analysis.EntryPoints, relPath)
			}
		}

		return nil
	})
}

// detectArchitecture detects the architectural pattern
func (a *Analyzer) detectArchitecture(analysis *CodebaseAnalysis) ArchitecturePattern {
	indicators := []string{}
	scores := make(map[string]float64)

	// Check for CLI indicators
	if hasPackage(analysis.PackageStructure, "cmd") {
		scores["cli"] += 0.3
		indicators = append(indicators, "cmd/ directory present")
	}
	if hasFiles(analysis.Languages, "Go") {
		scores["cli"] += 0.2
		scores["library"] += 0.2
	}

	// Check for web indicators
	if hasFiles(analysis.Languages, "TypeScript") || hasFiles(analysis.Languages, "JavaScript") {
		scores["web"] += 0.3
		indicators = append(indicators, "Frontend code detected")
	}
	if hasPackage(analysis.PackageStructure, "api") || hasPackage(analysis.PackageStructure, "http") {
		scores["web"] += 0.2
		indicators = append(indicators, "API layer detected")
	}

	// Check for microservices
	serviceCount := 0
	for pkg := range analysis.PackageStructure {
		if strings.Contains(pkg, "service") || strings.Contains(pkg, "svc") {
			serviceCount++
		}
	}
	if serviceCount > 2 {
		scores["microservices"] += 0.4
		indicators = append(indicators, fmt.Sprintf("%d service packages found", serviceCount))
	}

	// Check for library
	if len(analysis.EntryPoints) == 0 || len(analysis.EntryPoints) == 1 {
		scores["library"] += 0.2
	}

	// Find highest score
	maxScore := 0.0
	archType := "monolith"
	for t, score := range scores {
		if score > maxScore {
			maxScore = score
			archType = t
		}
	}

	return ArchitecturePattern{
		Type:       archType,
		Confidence: maxScore,
		Indicators: indicators,
	}
}

// findGaps identifies missing or underutilized areas
func (a *Analyzer) findGaps(analysis *CodebaseAnalysis) []Gap {
	gaps := []Gap{}

	// Check for tests
	testFiles := 0
	for _, files := range analysis.PackageStructure {
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") || strings.HasSuffix(file, ".test.js") {
				testFiles++
			}
		}
	}

	testRatio := float64(testFiles) / float64(analysis.TotalFiles)
	if testRatio < 0.3 {
		gaps = append(gaps, Gap{
			Category:    "testing",
			Severity:    "important",
			Description: fmt.Sprintf("Low test coverage: %d test files out of %d total files (%.0f%%)", testFiles, analysis.TotalFiles, testRatio*100),
			Suggestion:  "Add comprehensive test coverage, aiming for at least 30% test files",
		})
	}

	// Check for documentation
	hasReadme := false
	hasDocs := false
	for pkg := range analysis.PackageStructure {
		if strings.Contains(strings.ToLower(pkg), "doc") {
			hasDocs = true
		}
	}
	readmePath := filepath.Join(a.rootPath, "README.md")
	if _, err := os.Stat(readmePath); err == nil {
		hasReadme = true
	}

	if !hasReadme {
		gaps = append(gaps, Gap{
			Category:    "docs",
			Severity:    "important",
			Description: "No README.md found",
			Suggestion:  "Add README.md with project overview, setup instructions, and usage examples",
		})
	}

	if !hasDocs && analysis.TotalFiles > 50 {
		gaps = append(gaps, Gap{
			Category:    "docs",
			Severity:    "nice-to-have",
			Description: "No dedicated documentation directory for large project",
			Suggestion:  "Create docs/ directory with architecture, API, and development guides",
		})
	}

	// Check for CI/CD
	hasCI := false
	ciPath := filepath.Join(a.rootPath, ".github", "workflows")
	if _, err := os.Stat(ciPath); err == nil {
		hasCI = true
	}

	if !hasCI && analysis.TotalFiles > 20 {
		gaps = append(gaps, Gap{
			Category:    "automation",
			Severity:    "important",
			Description: "No CI/CD configuration found",
			Suggestion:  "Add GitHub Actions or similar CI/CD for automated testing and deployment",
		})
	}

	// Check for monitoring/observability
	hasMonitoring := false
	for pkg := range analysis.PackageStructure {
		if strings.Contains(pkg, "metrics") || strings.Contains(pkg, "telemetry") || strings.Contains(pkg, "observability") {
			hasMonitoring = true
		}
	}

	if !hasMonitoring && analysis.TotalFiles > 30 {
		gaps = append(gaps, Gap{
			Category:    "monitoring",
			Severity:    "nice-to-have",
			Description: "No observability layer detected",
			Suggestion:  "Add metrics, logging, and tracing for production monitoring",
		})
	}

	// Check for security
	hasSecurity := false
	for pkg := range analysis.PackageStructure {
		if strings.Contains(pkg, "security") || strings.Contains(pkg, "auth") {
			hasSecurity = true
		}
	}

	if !hasSecurity && analysis.Architecture.Type == "web" {
		gaps = append(gaps, Gap{
			Category:    "security",
			Severity:    "critical",
			Description: "Web application without dedicated security layer",
			Suggestion:  "Add authentication, authorization, input validation, and security headers",
		})
	}

	return gaps
}

// Helper functions

func extensionToLanguage(ext string) string {
	switch ext {
	case ".go":
		return "Go"
	case ".js":
		return "JavaScript"
	case ".ts":
		return "TypeScript"
	case ".py":
		return "Python"
	case ".rs":
		return "Rust"
	case ".java":
		return "Java"
	default:
		return ""
	}
}

func countLines(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}

	return strings.Count(string(data), "\n"), nil
}

func isEntryPoint(path, name string) bool {
	return name == "main.go" || name == "index.js" || name == "index.ts" || name == "app.py"
}

func hasFiles(m map[string]int, key string) bool {
	for k := range m {
		if strings.Contains(strings.ToLower(k), strings.ToLower(key)) {
			return true
		}
	}
	return false
}

func hasPackage(m map[string][]string, key string) bool {
	for k := range m {
		if strings.Contains(strings.ToLower(k), strings.ToLower(key)) {
			return true
		}
	}
	return false
}
