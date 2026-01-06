package security

import (
	"strings"
	"testing"
)

func TestNewFinding(t *testing.T) {
	finding := NewFinding(CategorySecrets, SeverityCritical, "Test Title", "Test Description")

	if finding == nil {
		t.Fatal("NewFinding should return non-nil finding")
	}

	if finding.Category != CategorySecrets {
		t.Errorf("Category = %v, want %v", finding.Category, CategorySecrets)
	}

	if finding.Severity != SeverityCritical {
		t.Errorf("Severity = %v, want %v", finding.Severity, SeverityCritical)
	}

	if finding.Title != "Test Title" {
		t.Errorf("Title = %v, want 'Test Title'", finding.Title)
	}

	if finding.Description != "Test Description" {
		t.Errorf("Description = %v, want 'Test Description'", finding.Description)
	}

	if finding.Confidence != 0.8 {
		t.Errorf("Default confidence = %v, want 0.8", finding.Confidence)
	}

	if finding.ID == "" {
		t.Error("ID should be generated")
	}
}

func TestSecurityFinding_String(t *testing.T) {
	finding := &SecurityFinding{
		Category:    CategoryInjection,
		Severity:    SeverityHigh,
		Title:       "SQL Injection",
		Description: "Potential SQL injection vulnerability",
		FilePath:    "/test/file.go",
		LineNumber:  42,
		LineContent: "query := fmt.Sprintf(\"SELECT * FROM users WHERE id=%s\", userId)",
		Remediation: "Use parameterized queries",
	}

	str := finding.String()

	if !strings.Contains(str, "SQL Injection") {
		t.Error("String should contain title")
	}

	if !strings.Contains(str, string(CategoryInjection)) {
		t.Error("String should contain category")
	}

	if !strings.Contains(str, "/test/file.go") {
		t.Error("String should contain file path")
	}

	if !strings.Contains(str, "42") {
		t.Error("String should contain line number")
	}

	if !strings.Contains(str, "Use parameterized queries") {
		t.Error("String should contain remediation")
	}
}

func TestSecurityFinding_IsHighSeverity(t *testing.T) {
	tests := []struct {
		name     string
		severity Severity
		want     bool
	}{
		{"Critical", SeverityCritical, true},
		{"High", SeverityHigh, true},
		{"Medium", SeverityMedium, false},
		{"Low", SeverityLow, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			finding := &SecurityFinding{Severity: tt.severity}
			if got := finding.IsHighSeverity(); got != tt.want {
				t.Errorf("IsHighSeverity() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSecurityFinding_GetSeverityLabel(t *testing.T) {
	tests := []struct {
		severity Severity
		want     string
	}{
		{SeverityCritical, "CRITICAL"},
		{9.5, "CRITICAL"},
		{SeverityHigh, "HIGH"},
		{8.0, "HIGH"},
		{SeverityMedium, "MEDIUM"},
		{5.0, "MEDIUM"},
		{SeverityLow, "LOW"},
		{2.0, "LOW"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			finding := &SecurityFinding{Severity: tt.severity}
			if got := finding.GetSeverityLabel(); got != tt.want {
				t.Errorf("GetSeverityLabel() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewResult(t *testing.T) {
	result := NewResult()

	if result == nil {
		t.Fatal("NewResult should return non-nil result")
	}

	if result.Findings == nil {
		t.Error("Findings should be initialized")
	}

	if len(result.Findings) != 0 {
		t.Error("Findings should be empty initially")
	}

	if result.FileCount != 0 {
		t.Error("FileCount should be 0 initially")
	}

	if result.ScanTime != 0 {
		t.Error("ScanTime should be 0 initially")
	}
}

func TestResult_AddFinding(t *testing.T) {
	result := NewResult()
	finding := NewFinding(CategoryXSS, SeverityMedium, "XSS", "XSS vulnerability")

	result.AddFinding(finding)

	if len(result.Findings) != 1 {
		t.Errorf("Findings count = %d, want 1", len(result.Findings))
	}

	if result.Findings[0].Title != "XSS" {
		t.Error("Added finding should be in results")
	}
}

func TestResult_AddFindings(t *testing.T) {
	result := NewResult()
	findings := []SecurityFinding{
		*NewFinding(CategorySecrets, SeverityCritical, "Secret 1", "Description 1"),
		*NewFinding(CategoryInjection, SeverityHigh, "Injection", "Description 2"),
		*NewFinding(CategoryXSS, SeverityMedium, "XSS", "Description 3"),
	}

	result.AddFindings(findings)

	if len(result.Findings) != 3 {
		t.Errorf("Findings count = %d, want 3", len(result.Findings))
	}
}

func TestResult_GetHighSeverityFindings(t *testing.T) {
	result := NewResult()
	result.AddFindings([]SecurityFinding{
		*NewFinding(CategorySecrets, SeverityCritical, "Critical", "Critical issue"),
		*NewFinding(CategoryInjection, SeverityHigh, "High", "High issue"),
		*NewFinding(CategoryXSS, SeverityMedium, "Medium", "Medium issue"),
		*NewFinding(CategoryOthers, SeverityLow, "Low", "Low issue"),
	})

	highSeverity := result.GetHighSeverityFindings()

	if len(highSeverity) != 2 {
		t.Errorf("High severity count = %d, want 2", len(highSeverity))
	}

	for _, finding := range highSeverity {
		if !finding.IsHighSeverity() {
			t.Errorf("Non-high severity finding in results: %v", finding.Severity)
		}
	}
}

func TestResult_GetFindingsByCategory(t *testing.T) {
	result := NewResult()
	result.AddFindings([]SecurityFinding{
		*NewFinding(CategorySecrets, SeverityCritical, "Secret 1", "Description"),
		*NewFinding(CategorySecrets, SeverityHigh, "Secret 2", "Description"),
		*NewFinding(CategoryInjection, SeverityHigh, "Injection", "Description"),
		*NewFinding(CategoryXSS, SeverityMedium, "XSS", "Description"),
	})

	secretFindings := result.GetFindingsByCategory(CategorySecrets)

	if len(secretFindings) != 2 {
		t.Errorf("Secret findings count = %d, want 2", len(secretFindings))
	}

	for _, finding := range secretFindings {
		if finding.Category != CategorySecrets {
			t.Errorf("Wrong category in filtered results: %v", finding.Category)
		}
	}

	injectionFindings := result.GetFindingsByCategory(CategoryInjection)
	if len(injectionFindings) != 1 {
		t.Errorf("Injection findings count = %d, want 1", len(injectionFindings))
	}
}

func TestResult_GetCountBySeverity(t *testing.T) {
	result := NewResult()
	result.AddFindings([]SecurityFinding{
		{Severity: SeverityCritical},
		{Severity: SeverityCritical},
		{Severity: SeverityHigh},
		{Severity: SeverityMedium},
		{Severity: SeverityMedium},
		{Severity: SeverityMedium},
		{Severity: SeverityLow},
	})

	counts := result.GetCountBySeverity()

	if counts["CRITICAL"] != 2 {
		t.Errorf("Critical count = %d, want 2", counts["CRITICAL"])
	}

	if counts["HIGH"] != 1 {
		t.Errorf("High count = %d, want 1", counts["HIGH"])
	}

	if counts["MEDIUM"] != 3 {
		t.Errorf("Medium count = %d, want 3", counts["MEDIUM"])
	}

	if counts["LOW"] != 1 {
		t.Errorf("Low count = %d, want 1", counts["LOW"])
	}
}

func TestResult_Summary(t *testing.T) {
	result := NewResult()
	result.AddFindings([]SecurityFinding{
		{Severity: SeverityCritical},
		{Severity: SeverityHigh},
		{Severity: SeverityMedium},
		{Severity: SeverityLow},
	})
	result.FileCount = 10
	result.ScanTime = 1500

	summary := result.Summary()

	if !strings.Contains(summary, "4 issues") {
		t.Error("Summary should contain total issue count")
	}

	if !strings.Contains(summary, "1 critical") {
		t.Error("Summary should contain critical count")
	}

	if !strings.Contains(summary, "10 files") {
		t.Error("Summary should contain file count")
	}

	if !strings.Contains(summary, "1500ms") {
		t.Error("Summary should contain scan time")
	}
}

func TestResult_Critical(t *testing.T) {
	tests := []struct {
		name     string
		findings []SecurityFinding
		wantCrit bool
	}{
		{
			name:     "Has critical",
			findings: []SecurityFinding{{Severity: SeverityCritical}},
			wantCrit: true,
		},
		{
			name:     "No critical",
			findings: []SecurityFinding{{Severity: SeverityHigh}, {Severity: SeverityMedium}},
			wantCrit: false,
		},
		{
			name:     "Empty",
			findings: []SecurityFinding{},
			wantCrit: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NewResult()
			result.AddFindings(tt.findings)

			if got := result.Critical(); got != tt.wantCrit {
				t.Errorf("Critical() = %v, want %v", got, tt.wantCrit)
			}
		})
	}
}

func TestHashCode(t *testing.T) {
	// Same input should produce same hash
	hash1 := hashCode("test string")
	hash2 := hashCode("test string")

	if hash1 != hash2 {
		t.Error("Same input should produce same hash")
	}

	// Different input should produce different hash (usually)
	hash3 := hashCode("different string")
	if hash1 == hash3 {
		t.Error("Different input produced same hash (collision)")
	}

	// Hash should always be positive
	if hash1 < 0 {
		t.Error("Hash should be positive")
	}
}
