package dream

import (
	"strings"
	"testing"
)

func TestNewGenerator(t *testing.T) {
	analysis := &CodebaseAnalysis{}
	gen := NewGenerator(analysis)

	if gen.analysis != analysis {
		t.Error("Generator should store analysis reference")
	}
}

func TestGenerateIdeas_CLI(t *testing.T) {
	analysis := &CodebaseAnalysis{
		TotalFiles: 20,
		Languages: map[string]int{
			"Go": 20,
		},
		Architecture: ArchitecturePattern{
			Type: "cli",
		},
		Gaps: []Gap{},
	}

	gen := NewGenerator(analysis)
	ideas := gen.GenerateIdeas()

	if len(ideas) == 0 {
		t.Error("Expected some ideas for CLI project")
	}

	// Should have CLI-specific ideas
	foundCLIIdea := false
	for _, idea := range ideas {
		if strings.Contains(idea.Title, "Interactive") || strings.Contains(idea.Title, "Plugin") {
			foundCLIIdea = true
		}
	}

	if !foundCLIIdea {
		t.Error("Expected CLI-specific ideas")
	}
}

func TestGenerateIdeas_Web(t *testing.T) {
	analysis := &CodebaseAnalysis{
		TotalFiles: 50,
		Languages: map[string]int{
			"TypeScript": 30,
			"Go":         20,
		},
		Architecture: ArchitecturePattern{
			Type: "web",
		},
		Gaps: []Gap{},
	}

	gen := NewGenerator(analysis)
	ideas := gen.GenerateIdeas()

	// Should have web-specific ideas
	foundWebIdea := false
	for _, idea := range ideas {
		if strings.Contains(idea.Title, "GraphQL") || strings.Contains(idea.Title, "WebSocket") {
			foundWebIdea = true
		}
	}

	if !foundWebIdea {
		t.Error("Expected web-specific ideas")
	}
}

func TestGenerateIdeas_Library(t *testing.T) {
	analysis := &CodebaseAnalysis{
		TotalFiles: 15,
		Languages: map[string]int{
			"Go": 15,
		},
		Architecture: ArchitecturePattern{
			Type: "library",
		},
		Gaps: []Gap{},
	}

	gen := NewGenerator(analysis)
	ideas := gen.GenerateIdeas()

	// Should have library-specific ideas
	foundLibraryIdea := false
	for _, idea := range ideas {
		if strings.Contains(idea.Title, "Fluent API") || strings.Contains(idea.Title, "Builder") {
			foundLibraryIdea = true
		}
	}

	if !foundLibraryIdea {
		t.Error("Expected library-specific ideas")
	}
}

func TestGenerateGapIdeas_Testing(t *testing.T) {
	analysis := &CodebaseAnalysis{
		Gaps: []Gap{
			{Category: "testing", Severity: "important"},
		},
	}

	gen := NewGenerator(analysis)
	ideas := gen.generateGapIdeas()

	foundTestingIdea := false
	for _, idea := range ideas {
		if idea.Category == "tooling" && strings.Contains(idea.Description, "testing") {
			foundTestingIdea = true
		}
	}

	if !foundTestingIdea {
		t.Error("Expected testing-related idea for testing gap")
	}
}

func TestGenerateGapIdeas_Security(t *testing.T) {
	analysis := &CodebaseAnalysis{
		Gaps: []Gap{
			{Category: "security", Severity: "critical"},
		},
	}

	gen := NewGenerator(analysis)
	ideas := gen.generateGapIdeas()

	foundSecurityIdea := false
	for _, idea := range ideas {
		if strings.Contains(strings.ToLower(idea.Title), "security") {
			foundSecurityIdea = true
			if idea.Effort != "large" {
				t.Error("Security hardening should be marked as large effort")
			}
		}
	}

	if !foundSecurityIdea {
		t.Error("Expected security idea for security gap")
	}
}

func TestGenerateModernPracticeIdeas_LargeProject(t *testing.T) {
	analysis := &CodebaseAnalysis{
		TotalFiles: 100,
		TotalLines: 15000,
		Languages:  map[string]int{"Go": 100},
	}

	gen := NewGenerator(analysis)
	ideas := gen.generateModernPracticeIdeas()

	// Large projects should get performance and monorepo ideas
	foundPerformanceIdea := false
	foundMonorepoIdea := false

	for _, idea := range ideas {
		if strings.Contains(idea.Title, "Performance") || strings.Contains(idea.Title, "Profiling") {
			foundPerformanceIdea = true
		}
		if strings.Contains(idea.Title, "Monorepo") {
			foundMonorepoIdea = true
		}
	}

	if !foundPerformanceIdea {
		t.Error("Expected performance idea for large project")
	}

	if !foundMonorepoIdea {
		t.Error("Expected monorepo idea for large project")
	}
}

func TestGenerateModernPracticeIdeas_Go(t *testing.T) {
	analysis := &CodebaseAnalysis{
		Languages: map[string]int{"Go": 50},
	}

	gen := NewGenerator(analysis)
	ideas := gen.generateModernPracticeIdeas()

	// Go projects should get Go-specific ideas
	foundGoIdea := false
	for _, idea := range ideas {
		if strings.Contains(idea.Title, "Generics") || strings.Contains(idea.Title, "Context") {
			foundGoIdea = true
		}
	}

	if !foundGoIdea {
		t.Error("Expected Go-specific ideas")
	}
}

func TestHasLanguage(t *testing.T) {
	analysis := &CodebaseAnalysis{
		Languages: map[string]int{
			"Go":         20,
			"TypeScript": 10,
		},
	}

	if !hasLanguage(analysis, "Go") {
		t.Error("Should find Go")
	}

	if !hasLanguage(analysis, "TypeScript") {
		t.Error("Should find TypeScript")
	}

	if hasLanguage(analysis, "Rust") {
		t.Error("Should not find Rust")
	}
}

func TestFormatIdea(t *testing.T) {
	idea := DreamIdea{
		Title:        "Test Idea",
		Category:     "feature",
		Description:  "A test description",
		Benefits:     []string{"Benefit 1", "Benefit 2"},
		Effort:       "medium",
		Dependencies: []string{"Dep 1"},
		Example:      "Example code",
	}

	formatted := FormatIdea(idea)

	// Check all sections are present
	if !strings.Contains(formatted, "Test Idea") {
		t.Error("Formatted output should contain title")
	}

	if !strings.Contains(formatted, "medium") {
		t.Error("Formatted output should contain effort")
	}

	if !strings.Contains(formatted, "A test description") {
		t.Error("Formatted output should contain description")
	}

	if !strings.Contains(formatted, "Benefit 1") {
		t.Error("Formatted output should contain benefits")
	}

	if !strings.Contains(formatted, "Dep 1") {
		t.Error("Formatted output should contain dependencies")
	}

	if !strings.Contains(formatted, "Example code") {
		t.Error("Formatted output should contain example")
	}
}

func TestCategoryToIcon(t *testing.T) {
	tests := []struct {
		category string
		want     string
	}{
		{"feature", "✨"},
		{"architecture", "🏗️"},
		{"refactoring", "♻️"},
		{"tooling", "🔧"},
		{"testing", "🧪"},
		{"unknown", "💡"},
	}

	for _, tt := range tests {
		got := categoryToIcon(tt.category)
		if got != tt.want {
			t.Errorf("categoryToIcon(%q) = %q, want %q", tt.category, got, tt.want)
		}
	}
}

func TestGenerateIdeas_AllCategories(t *testing.T) {
	analysis := &CodebaseAnalysis{
		TotalFiles: 50,
		TotalLines: 10000,
		Languages:  map[string]int{"Go": 50},
		Architecture: ArchitecturePattern{
			Type: "cli",
		},
		Gaps: []Gap{
			{Category: "testing"},
			{Category: "docs"},
		},
	}

	gen := NewGenerator(analysis)
	ideas := gen.GenerateIdeas()

	// Check we have diverse categories
	categories := make(map[string]bool)
	for _, idea := range ideas {
		categories[idea.Category] = true
	}

	if len(categories) < 2 {
		t.Error("Expected diverse idea categories")
	}

	// All ideas should have required fields
	for _, idea := range ideas {
		if idea.Title == "" {
			t.Error("Idea should have title")
		}
		if idea.Description == "" {
			t.Error("Idea should have description")
		}
		if idea.Effort == "" {
			t.Error("Idea should have effort estimate")
		}
	}
}
