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

// InputValidationAnalyzer detects input validation vulnerabilities
type InputValidationAnalyzer struct {
	config   Config
	patterns []validationPattern
}

// validationPattern represents a pattern to detect
type validationPattern struct {
	pattern     string
	category    Category
	severity    Severity
	title       string
	description string
	remediation string
}

// NewInputValidationAnalyzer creates a new input validation analyzer
func NewInputValidationAnalyzer(config Config) *InputValidationAnalyzer {
	analyzer := &InputValidationAnalyzer{
		config: config,
		patterns: []validationPattern{
			// SQL Injection patterns
			{
				pattern:     `(?i)(fmt\.Sprintf|sprintf)\s*\(\s*"[^"]*%[^"]*"[^,]+,\s*(r\.\w+|req\.\w+|request\.\w+|params\[\w+\]|c\.Query\(|c\.Param\()\)`,
				category:    CategoryInjection,
				severity:    SeverityHigh,
				title:       "Potential SQL Injection",
				description: "SQL query built with user input using string formatting",
				remediation: "Use parameterized queries or prepared statements instead of string formatting",
			},
			{
				pattern:     `(?i)"[^"]*"\s*\+\s*(r\.\w+|req\.\w+|request\.\w+)`,
				category:    CategoryInjection,
				severity:    SeverityHigh,
				title:       "String concatenation in SQL query",
				description: "SQL query built with user input using string concatenation",
				remediation: "Use parameterized queries or an ORM",
			},
			{
				pattern:     `(?i)(SELECT|INSERT|UPDATE|DELETE)\s+.*\+.*(\$|\w+Param)`,
				category:    CategoryInjection,
				severity:    SeverityHigh,
				title:       "Dynamic SQL construction",
				description: "SQL query appears to concatenate user input",
				remediation: "Use parameterized queries with proper escaping",
			},

			// NoSQL Injection patterns
			{
				pattern:     `(?i)\.Find\(\s*map\[\]\w+\{\s*"\$\w+"\s*:\s*.*(r\.|req\.|request\.|params)\.`,
				category:    CategoryInjection,
				severity:    SeverityHigh,
				title:       "Potential NoSQL Injection",
				description: "NoSQL query built with unsanitized user input",
				remediation: "Validate and sanitize all user inputs before using in queries",
			},

			// XSS patterns
			{
				pattern:     `(?i)(c\.|context\.)?(HTML\(|RenderHTML\(|WriteString\(|Fprintf\(w,\s*"[^"]*%[^"]*"[^,]+,\s*(r\.\w+|req\.\w+|request\.\w+|params\[\w+\])\))`,
				category:    CategoryXSS,
				severity:    SeverityMedium,
				title:       "Potential XSS vulnerability",
				description: "HTML output with user input without proper escaping",
				remediation: "Use HTML escaping or a templating engine with auto-escaping",
			},
			{
				pattern:     `(?i)innerHTML\s*=\s*.*(r\.\w+|req\.\w+|request\.\w+)`,
				category:    CategoryXSS,
				severity:    SeverityHigh,
				title:       "DOM XSS vulnerability",
				description: "Direct assignment to innerHTML with user input",
				remediation: "Use textContent or properly sanitize HTML before assignment",
			},
			{
				pattern:     `(?i)document\.write\(\s*.*(\+\s*(r\.\w+|req\.\w+|request\.\w+)\s*\+\s*|\$\{[^}]+\})`,
				category:    CategoryXSS,
				severity:    SeverityHigh,
				title:       "document.write with user input",
				description: "document.write used with user input can lead to XSS",
				remediation: "Use DOM manipulation methods instead of document.write",
			},

			// Command Injection patterns
			{
				pattern:     `(?i)(exec\.Command|execCommand|shell_exec|system\()\s*\(\s*"[^"]*%[^"]*"[^,]+,\s*(r\.\w+|req\.\w+|request\.\w+)`,
				category:    CategoryInjection,
				severity:    SeverityCritical,
				title:       "Command Injection vulnerability",
				description: "Shell command built with user input",
				remediation: "Use hard-coded commands with allowlists or proper sanitization",
			},
			{
				pattern:     `(?i)(exec\.Command|execCommand)\(\s*(r\.\w+|req\.\w+|request\.\w+)\.`,
				category:    CategoryInjection,
				severity:    SeverityCritical,
				title:       "Direct command execution with user input",
				description: "Direct use of user input in command execution",
				remediation: "Validate and sanitize all user inputs used in commands",
			},

			// Path Traversal patterns
			{
				pattern:     `(?i)(ioutil\.ReadFile|os\.ReadFile|open\(|fopen\()\s*\(\s*(r\.\w+|req\.\w+|request\.\w+)\.`,
				category:    CategoryInjection,
				severity:    SeverityHigh,
				title:       "Path Traversal vulnerability",
				description: "File operation with user-controlled path",
				remediation: "Validate and sanitize file paths, use allowlists",
			},
			{
				pattern:     `(?i)\.\.\/`,
				category:    CategoryInjection,
				severity:    SeverityMedium,
				title:       "Path Traversal pattern detected",
				description: "Path contains parent directory traversal",
				remediation: "Validate file paths to prevent directory traversal",
			},

			// LDAP Injection patterns
			{
				pattern:     `(?i)LDAP|ldap.*\+.*(r\.\w+|req\.\w+|request\.\w+)`,
				category:    CategoryInjection,
				severity:    SeverityHigh,
				title:       "Potential LDAP Injection",
				description: "LDAP query built with user input",
				remediation: "Use proper LDAP encoding or parameterized queries",
			},

			// XPath Injection patterns
			{
				pattern:     `(?i)XPath|xpath.*\+.*(r\.\w+|req\.\w+|request\.\w+)`,
				category:    CategoryInjection,
				severity:    SeverityHigh,
				title:       "Potential XPath Injection",
				description: "XPath expression built with user input",
				remediation: "Use parameterized XPath or validate input thoroughly",
			},

			// Template Injection patterns
			{
				pattern:     `(?i)\.Parse\(\s*.*(\+.*(r\.\w+|req\.\w+|request\.\w+)|"[^"]*%[^"]*".*,)`,
				category:    CategoryInjection,
				severity:    SeverityHigh,
				title:       "Template Injection vulnerability",
				description: "Template parsed with user input",
				remediation: "Use context-aware templating with auto-escaping",
			},

			// Log Injection patterns
			{
				pattern:     `(?i)(log\.|logger\.|fmt\.Print|println|print\()\s*.*(\+.*(r\.\w+|req\.\w+|request\.\w+)|"[^"]*%[^"]*".*,)`,
				category:    CategoryInjection,
				severity:    SeverityLow,
				title:       "Log Injection vulnerability",
				description: "Log entries built with user input",
				remediation: "Sanitize user input before logging or use structured logging",
			},

			// CRLF Injection patterns
			{
				pattern:     `(?i)\.Set\("[^"]*"\s*,\s*"[^"]*"\+.*(r\.\w+|req\.\w+|request\.\w+)`,
				category:    CategoryInjection,
				severity:    SeverityMedium,
				title:       "HTTP Response Splitting (CRLF Injection)",
				description: "HTTP headers built with user input",
				remediation: "Validate or sanitize user input used in headers",
			},

			// Open Redirect patterns
			{
				pattern:     `(?i)(redirect|Redirect|location|Location)\s*=\s*(r\.\w+|req\.\w+|request\.\w+)`,
				category:    CategoryInjection,
				severity:    SeverityMedium,
				title:       "Open Redirect vulnerability",
				description: "Redirect URL controlled by user input",
				remediation: "Use allowlists or validate redirect URLs",
			},

			// SSRF patterns
			{
				pattern:     `(?i)(http\.Get|http\.Post|http\.Do|curl|wget)\(\s*(r\.\w+|req\.\w+|request\.\w+)`,
				category:    CategoryInjection,
				severity:    SeverityHigh,
				title:       "Server-Side Request Forgery (SSRF)",
				description: "HTTP request to user-controlled URL",
				remediation: "Validate and restrict URLs, use allowlists",
			},
		},
	}

	return analyzer
}

// Name returns the analyzer name
func (a *InputValidationAnalyzer) Name() string {
	return "input-validation"
}

// Description returns the analyzer description
func (a *InputValidationAnalyzer) Description() string {
	return "Detects input validation vulnerabilities including SQL injection, XSS, command injection, and other injection flaws"
}

// Analyze performs input validation analysis
func (a *InputValidationAnalyzer) Analyze(path string) (*Result, error) {
	result := NewResult()
	startTime := time.Now()

	// Walk through all files
	err := filepath.Walk(path, func(filePath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip directories and non-source files
		if info.IsDir() || !a.shouldAnalyzeFile(filePath) {
			return nil
		}

		// Scan file
		fileFindings, err := a.analyzeFile(filePath)
		if err != nil {
			fmt.Printf("Warning: Failed to analyze %s: %v\n", filePath, err)
			return nil // Continue with other files
		}

		result.AddFindings(fileFindings)
		return nil
	})

	if err != nil {
		return nil, err
	}

	result.ScanTime = time.Since(startTime).Milliseconds()

	return result, nil
}

// shouldAnalyzeFile determines if a file should be analyzed
func (a *InputValidationAnalyzer) shouldAnalyzeFile(path string) bool {
	for _, excludeDir := range a.config.ExcludeDirs {
		excludeDir = strings.TrimSpace(excludeDir)
		if excludeDir == "" {
			continue
		}
		if strings.Contains(path, string(filepath.Separator)+excludeDir+string(filepath.Separator)) {
			return false
		}
	}

	// Skip test files unless configured to include them
	if !a.config.IncludeTests {
		if strings.Contains(path, "_test.go") || strings.Contains(path, ".test.") {
			return false
		}
	}

	// Only analyze relevant file types
	ext := strings.ToLower(filepath.Ext(path))
	supportedExts := map[string]bool{
		".go":   true,
		".py":   true,
		".js":   true,
		".ts":   true,
		".php":  true,
		".java": true,
		".rb":   true,
		".cs":   true,
	}

	return supportedExts[ext]
}

// analyzeFile analyzes a single file for input validation vulnerabilities
func (a *InputValidationAnalyzer) analyzeFile(path string) ([]SecurityFinding, error) {
	var findings []SecurityFinding

	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// Read all lines
	var lines []string
	lineNum := 1
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		lineNum++
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// Check each line against patterns
	for i, line := range lines {
		for _, pattern := range a.patterns {
			matched, matches := a.matchPattern(line, pattern.pattern)
			if matched {
				finding := a.createFinding(pattern, path, i+1, line, matches)
				if finding != nil {
					findings = append(findings, *finding)
				}
			}
		}
	}

	return findings, nil
}

// matchPattern checks if a line matches a pattern
func (a *InputValidationAnalyzer) matchPattern(line, pattern string) (bool, []string) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false, nil
	}

	matches := re.FindStringSubmatch(line)
	return matches != nil, matches
}

// createFinding creates a security finding from a pattern match
func (a *InputValidationAnalyzer) createFinding(pattern validationPattern, filePath string, lineNum int, lineContent string, matches []string) *SecurityFinding {
	description := pattern.description
	remediation := pattern.remediation

	// Enhance description with match details if available
	if len(matches) > 0 {
		description = fmt.Sprintf("%s: Found pattern '%s'", pattern.description, matches[0])
	}

	finding := &SecurityFinding{
		Category:    pattern.category,
		Severity:    pattern.severity,
		Title:       pattern.title,
		Description: description,
		FilePath:    filePath,
		LineNumber:  lineNum,
		LineContent: strings.TrimSpace(lineContent),
		Remediation: remediation,
		Confidence:  0.8, // Pattern-based detection has high confidence
	}

	// Adjust confidence based on context
	if a.isSensibleContext(lineContent) {
		finding.Confidence = 0.9
	} else if a.isFalsePositivePattern(lineContent) {
		finding.Confidence = 0.3
		finding.Severity = finding.Severity * 0.5 // Reduce severity for likely false positives
	}

	return finding
}

// isSensibleContext checks if the line appears to be in a security-sensitive context
func (a *InputValidationAnalyzer) isSensibleContext(line string) bool {
	sensiblePatterns := []string{
		"func ", "function ", "def ", "public ", "private ", "protected ",
		"if ", "for ", "while ", "switch ", "case ", "try {", "catch ", "except ",
		"=", "+=", "-=", "*=", "/=", "%=", "<-", "=>",
	}

	for _, pattern := range sensiblePatterns {
		if strings.Contains(line, pattern) {
			return true
		}
	}

	return false
}

// isFalsePositivePattern checks if this looks like a false positive
func (a *InputValidationAnalyzer) isFalsePositivePattern(line string) bool {
	// Comment lines
	if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "//") || strings.HasPrefix(strings.TrimSpace(line), "#") || strings.HasPrefix(strings.TrimSpace(line), "/*") || strings.HasPrefix(strings.TrimSpace(line), "*") {
		return true
	}

	// String constants that look like code
	if strings.Count(line, `"`) >= 2 && !strings.Contains(line, "`+`") && !strings.Contains(line, "%") {
		// Likely a string literal containing code examples
		return true
	}

	// Import statements
	if strings.Contains(line, "import ") || strings.Contains(line, "require(") || strings.Contains(line, "using ") {
		return true
	}

	return false
}
