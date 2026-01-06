package security

import (
	"fmt"
	"strings"
)

// Severity levels (CVSS-inspired)
type Severity float64

const (
	SeverityLow      Severity = 0.1 // 0.1-3.9
	SeverityMedium   Severity = 4.0 // 4.0-6.9
	SeverityHigh     Severity = 7.0 // 7.0-8.9
	SeverityCritical Severity = 9.0 // 9.0-10.0
)

// Category represents security vulnerability categories (OWASP alignment)
type Category string

const (
	CategoryInjection               Category = "injection"
	CategoryBrokenAuth              Category = "broken-auth"
	CategorySensitiveData           Category = "sensitive-data"
	CategoryXXE                     Category = "xxe"
	CategoryBrokenAccess            Category = "broken-access-control"
	CategorySecurityMisconfig       Category = "security-misconfig"
	CategoryXSS                     Category = "xss"
	CategoryInsecureDeserialization Category = "insecure-deserialization"
	CategoryComponents              Category = "vulnerable-components"
	CategorySecrets                 Category = "secrets-exposure"
	CategoryOthers                  Category = "other"
)

// SecurityFinding represents a discovered security issue
type SecurityFinding struct {
	ID           string   // Unique finding ID
	Category     Category // OWASP category
	Severity     Severity // CVSS score (0.1-10.0)
	Title        string   // Brief description
	Description  string   // Detailed explanation
	FilePath     string   // Affected file
	LineNumber   int      // Line number (if applicable)
	LineContent  string   // Source code line
	Confidence   float64  // 0.0-1.0 confidence in detection
	Remediation  string   // How to fix
	Context      string   // Additional context
	SuggestedFix string   // Specific fix suggestion
}

// NewFinding creates a new security finding
func NewFinding(category Category, severity Severity, title, description string) *SecurityFinding {
	return &SecurityFinding{
		ID:          fmt.Sprintf("%s-%d", category, hashCode(title+description)),
		Category:    category,
		Severity:    severity,
		Title:       title,
		Description: description,
		Confidence:  0.8, // Default confidence
	}
}

// String returns a formatted string representation
func (f *SecurityFinding) String() string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("[%s] %s (Severity: %.1f)\n", f.Category, f.Title, f.Severity))
	if f.FilePath != "" {
		b.WriteString(fmt.Sprintf("  File: %s", f.FilePath))
		if f.LineNumber > 0 {
			b.WriteString(fmt.Sprintf(":%d", f.LineNumber))
		}
		b.WriteString("\n")
	}
	if f.LineContent != "" {
		b.WriteString(fmt.Sprintf("  Code: %s\n", strings.TrimSpace(f.LineContent)))
	}
	b.WriteString(fmt.Sprintf("  Description: %s\n", f.Description))
	if f.Remediation != "" {
		b.WriteString(fmt.Sprintf("  Remediation: %s\n", f.Remediation))
	}
	if f.SuggestedFix != "" {
		b.WriteString(fmt.Sprintf("  Suggested Fix: %s\n", f.SuggestedFix))
	}

	return b.String()
}

// IsHighSeverity returns true if finding is high or critical severity
func (f *SecurityFinding) IsHighSeverity() bool {
	return f.Severity >= SeverityHigh
}

// GetSeverityLabel returns a human-readable severity label
func (f *SecurityFinding) GetSeverityLabel() string {
	switch {
	case f.Severity >= SeverityCritical:
		return "CRITICAL"
	case f.Severity >= SeverityHigh:
		return "HIGH"
	case f.Severity >= SeverityMedium:
		return "MEDIUM"
	default:
		return "LOW"
	}
}

// hashCode generates a simple hash code for ID generation
func hashCode(s string) int {
	h := 0
	for _, c := range s {
		h = 31*h + int(c)
	}
	if h < 0 {
		h = -h
	}
	return h
}

// Result contains findings from an analyzer
type Result struct {
	Findings  []SecurityFinding
	ScanTime  int64 // milliseconds
	FileCount int
}

// NewResult creates a new result
func NewResult() *Result {
	return &Result{
		Findings: []SecurityFinding{},
	}
}

// AddFinding adds a finding to the result
func (r *Result) AddFinding(finding *SecurityFinding) {
	r.Findings = append(r.Findings, *finding)
}

// AddFindings adds multiple findings
func (r *Result) AddFindings(findings []SecurityFinding) {
	r.Findings = append(r.Findings, findings...)
}

// GetHighSeverityFindings returns only high/critical findings
func (r *Result) GetHighSeverityFindings() []SecurityFinding {
	var high []SecurityFinding
	for _, f := range r.Findings {
		if f.IsHighSeverity() {
			high = append(high, f)
		}
	}
	return high
}

// GetFindingsByCategory returns findings for a specific category
func (r *Result) GetFindingsByCategory(category Category) []SecurityFinding {
	var findings []SecurityFinding
	for _, f := range r.Findings {
		if f.Category == category {
			findings = append(findings, f)
		}
	}
	return findings
}

// GetCountBySeverity returns count of findings by severity level
func (r *Result) GetCountBySeverity() map[string]int {
	counts := map[string]int{
		"CRITICAL": 0,
		"HIGH":     0,
		"MEDIUM":   0,
		"LOW":      0,
	}

	for _, f := range r.Findings {
		counts[f.GetSeverityLabel()]++
	}

	return counts
}

// Summary returns a human-readable summary
func (r *Result) Summary() string {
	counts := r.GetCountBySeverity()
	return fmt.Sprintf("Found %d issues: %d critical, %d high, %d medium, %d low\nScanned %d files in %dms",
		len(r.Findings), counts["CRITICAL"], counts["HIGH"], counts["MEDIUM"], counts["LOW"],
		r.FileCount, r.ScanTime)
}

// Critical indicates if any critical findings exist
func (r *Result) Critical() bool {
	for _, f := range r.Findings {
		if f.Severity >= SeverityCritical {
			return true
		}
	}
	return false
}
