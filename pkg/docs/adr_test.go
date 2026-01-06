package docs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewADRManager(t *testing.T) {
	mgr := NewADRManager("/tmp/docs")
	if mgr == nil {
		t.Fatal("NewADRManager returned nil")
	}
	expectedDecisionsDir := filepath.Join("/tmp/docs", "architecture", "decisions")
	if mgr.decisionsDir != expectedDecisionsDir {
		t.Errorf("expected decisionsDir %q, got %q", expectedDecisionsDir, mgr.decisionsDir)
	}
}

func TestADRManager_Create(t *testing.T) {
	tmpDir := t.TempDir()
	docsRoot := filepath.Join(tmpDir, "docs")
	mgr := NewADRManager(docsRoot)

	// Create initial README
	decisionsDir := filepath.Join(docsRoot, "architecture", "decisions")
	if err := os.MkdirAll(decisionsDir, 0755); err != nil {
		t.Fatal(err)
	}
	readmePath := filepath.Join(decisionsDir, "README.md")
	readmeContent := `# Architecture Decision Records

## Index

---
`
	if err := os.WriteFile(readmePath, []byte(readmeContent), 0644); err != nil {
		t.Fatal(err)
	}

	adr := &ADR{
		Title:        "Use PostgreSQL",
		Context:      "We need a database",
		Decision:     "Use PostgreSQL",
		Consequences: "Better performance",
	}

	filePath, err := mgr.Create(adr)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Check that file was created
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		t.Error("ADR file was not created")
	}

	// Check that number was assigned
	if adr.Number != 1 {
		t.Errorf("expected ADR number 1, got %d", adr.Number)
	}

	// Check that status was set
	if adr.Status != ADRStatusProposed {
		t.Errorf("expected status %s, got %s", ADRStatusProposed, adr.Status)
	}

	// Check that date was set
	if adr.Date.IsZero() {
		t.Error("date was not set")
	}
}

func TestADRManager_Create_WithNumber(t *testing.T) {
	tmpDir := t.TempDir()
	docsRoot := filepath.Join(tmpDir, "docs")
	mgr := NewADRManager(docsRoot)

	decisionsDir := filepath.Join(docsRoot, "architecture", "decisions")
	if err := os.MkdirAll(decisionsDir, 0755); err != nil {
		t.Fatal(err)
	}
	readmePath := filepath.Join(decisionsDir, "README.md")
	if err := os.WriteFile(readmePath, []byte("# ADRs\n\n## Index\n\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}

	adr := &ADR{
		Number:       5,
		Title:        "Test",
		Context:      "context",
		Decision:     "decision",
		Consequences: "consequences",
	}

	_, err := mgr.Create(adr)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Should use provided number
	if adr.Number != 5 {
		t.Errorf("expected ADR number 5, got %d", adr.Number)
	}
}

func TestADRManager_List(t *testing.T) {
	tmpDir := t.TempDir()
	docsRoot := filepath.Join(tmpDir, "docs")
	mgr := NewADRManager(docsRoot)

	decisionsDir := filepath.Join(docsRoot, "architecture", "decisions")
	if err := os.MkdirAll(decisionsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create test ADR files
	adr1 := `# 1. First Decision

**Status:** Accepted

**Date:** 2025-01-01

## Context

Some context

## Decision

Some decision

## Consequences

Some consequences
`
	if err := os.WriteFile(filepath.Join(decisionsDir, "0001-first.md"), []byte(adr1), 0644); err != nil {
		t.Fatal(err)
	}

	adr2 := `# 2. Second Decision

**Status:** Proposed

**Date:** 2025-01-02

## Context

Context 2

## Decision

Decision 2

## Consequences

Consequences 2
`
	if err := os.WriteFile(filepath.Join(decisionsDir, "0002-second.md"), []byte(adr2), 0644); err != nil {
		t.Fatal(err)
	}

	adrs, err := mgr.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	if len(adrs) != 2 {
		t.Errorf("expected 2 ADRs, got %d", len(adrs))
	}

	// Should be sorted by number
	if adrs[0].Number != 1 {
		t.Errorf("expected first ADR to be number 1, got %d", adrs[0].Number)
	}
	if adrs[1].Number != 2 {
		t.Errorf("expected second ADR to be number 2, got %d", adrs[1].Number)
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Use PostgreSQL", "use-postgresql"},
		{"API Design v2", "api-design-v2"},
		{"Test   Spaces", "test-spaces"},
		{"Special!@#Chars", "special-chars"},
	}

	for _, test := range tests {
		result := slugify(test.input)
		if result != test.expected {
			t.Errorf("slugify(%q) = %q, expected %q", test.input, result, test.expected)
		}
	}
}

func TestGenerateMarkdown(t *testing.T) {
	mgr := NewADRManager("/tmp")
	adr := &ADR{
		Number:       1,
		Title:        "Test Decision",
		Status:       ADRStatusAccepted,
		Date:         time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		Context:      "Some context",
		Decision:     "Some decision",
		Consequences: "Some consequences",
	}

	markdown := mgr.generateMarkdown(adr)

	if !strings.Contains(markdown, "# 1. Test Decision") {
		t.Error("expected title in markdown")
	}
	if !strings.Contains(markdown, "**Status:** Accepted") {
		t.Error("expected status in markdown")
	}
	if !strings.Contains(markdown, "## Context") {
		t.Error("expected Context section")
	}
	if !strings.Contains(markdown, "## Decision") {
		t.Error("expected Decision section")
	}
	if !strings.Contains(markdown, "## Consequences") {
		t.Error("expected Consequences section")
	}
}

func TestExtractSection(t *testing.T) {
	lines := []string{
		"# Title",
		"",
		"## Section1",
		"",
		"Content line 1",
		"Content line 2",
		"",
		"## Section2",
		"",
		"Other content",
	}

	result := extractSection(lines, 4, "## Section2")
	expected := "Content line 1\nContent line 2"

	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}
