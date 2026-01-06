package docs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewArchiveManager(t *testing.T) {
	mgr := NewArchiveManager("/tmp/docs")
	if mgr == nil {
		t.Fatal("NewArchiveManager returned nil")
	}
	expectedArchiveRoot := filepath.Join("/tmp/docs", "archive")
	if mgr.archiveRoot != expectedArchiveRoot {
		t.Errorf("expected archiveRoot %q, got %q", expectedArchiveRoot, mgr.archiveRoot)
	}
}

func TestArchiveManager_Archive(t *testing.T) {
	tmpDir := t.TempDir()
	docsRoot := filepath.Join(tmpDir, "docs")
	mgr := NewArchiveManager(docsRoot)

	// Create initial archive README
	archiveDir := filepath.Join(docsRoot, "archive")
	if err := os.MkdirAll(archiveDir, 0755); err != nil {
		t.Fatal(err)
	}
	readmePath := filepath.Join(archiveDir, "README.md")
	readmeContent := `# Documentation Archive

## Index

`
	if err := os.WriteFile(readmePath, []byte(readmeContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create test artifacts
	plansDir := filepath.Join(docsRoot, "plans")
	if err := os.MkdirAll(plansDir, 0755); err != nil {
		t.Fatal(err)
	}
	planningPath := filepath.Join(plansDir, "2025-01-test-feature-planning.md")
	if err := os.WriteFile(planningPath, []byte("# Planning"), 0644); err != nil {
		t.Fatal(err)
	}

	feature := &ArchivedFeature{
		Feature:      "test-feature",
		PRNumber:     42,
		Summary:      "Test feature",
		PlanningPath: planningPath,
	}

	err := mgr.Archive(feature)
	if err != nil {
		t.Fatalf("Archive failed: %v", err)
	}

	// Check that month directory was created
	month := feature.MergedDate.Format("2006-01")
	monthDir := filepath.Join(archiveDir, month)
	if _, err := os.Stat(monthDir); os.IsNotExist(err) {
		t.Error("month directory was not created")
	}

	// Check that feature directory was created
	featureDir := filepath.Join(monthDir, "test-feature")
	if _, err := os.Stat(featureDir); os.IsNotExist(err) {
		t.Error("feature directory was not created")
	}
}

func TestArchiveManager_Archive_GenerateMonth(t *testing.T) {
	tmpDir := t.TempDir()
	mgr := NewArchiveManager(tmpDir)

	if err := os.MkdirAll(filepath.Join(tmpDir, "archive"), 0755); err != nil {
		t.Fatal(err)
	}
	readmePath := filepath.Join(tmpDir, "archive", "README.md")
	if err := os.WriteFile(readmePath, []byte("# Archive\n\n## Index\n"), 0644); err != nil {
		t.Fatal(err)
	}

	feature := &ArchivedFeature{
		Feature:    "test",
		MergedDate: time.Date(2025, 11, 15, 0, 0, 0, 0, time.UTC),
	}

	if err := mgr.Archive(feature); err != nil {
		t.Fatal(err)
	}

	if feature.Month != "2025-11" {
		t.Errorf("expected month '2025-11', got %q", feature.Month)
	}
}

func TestFormatFeatureName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"test-feature", "Test Feature"},
		{"api-v2", "Api V2"},
		{"single", "Single"},
	}

	for _, test := range tests {
		result := formatFeatureName(test.input)
		if result != test.expected {
			t.Errorf("formatFeatureName(%q) = %q, expected %q", test.input, result, test.expected)
		}
	}
}

func TestArchiveManager_FormatArchiveEntry(t *testing.T) {
	mgr := NewArchiveManager("/tmp")
	feature := &ArchivedFeature{
		Feature:    "test-feature",
		Month:      "2025-01",
		PRNumber:   42,
		MergedDate: time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC),
		Summary:    "Test summary",
	}

	entry := mgr.formatArchiveEntry(feature)

	if !strings.Contains(entry, "Test Feature") {
		t.Error("expected formatted feature name")
	}
	if !strings.Contains(entry, "2025-01") {
		t.Error("expected month")
	}
	if !strings.Contains(entry, "#42") {
		t.Error("expected PR number")
	}
	if !strings.Contains(entry, "Test summary") {
		t.Error("expected summary")
	}
}

func TestArchiveManager_MoveFile(t *testing.T) {
	tmpDir := t.TempDir()
	mgr := NewArchiveManager(tmpDir)

	srcPath := filepath.Join(tmpDir, "source.txt")
	destPath := filepath.Join(tmpDir, "dest.txt")

	// Create source file
	if err := os.WriteFile(srcPath, []byte("test content"), 0644); err != nil {
		t.Fatal(err)
	}

	// Move file
	err := mgr.moveFile(srcPath, destPath)
	if err != nil {
		t.Fatalf("moveFile failed: %v", err)
	}

	// Source should not exist
	if _, err := os.Stat(srcPath); !os.IsNotExist(err) {
		t.Error("source file still exists after move")
	}

	// Destination should exist with same content
	content, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "test content" {
		t.Errorf("expected content 'test content', got %q", string(content))
	}
}

func TestArchiveManager_CreatePRDocument(t *testing.T) {
	tmpDir := t.TempDir()
	mgr := NewArchiveManager(tmpDir)

	prPath := filepath.Join(tmpDir, "pr-42.md")
	feature := &ArchivedFeature{
		Feature:    "test-feature",
		PRNumber:   42,
		MergedDate: time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC),
		Summary:    "Test summary",
	}

	err := mgr.createPRDocument(prPath, feature)
	if err != nil {
		t.Fatalf("createPRDocument failed: %v", err)
	}

	// Check that file was created
	content, err := os.ReadFile(prPath)
	if err != nil {
		t.Fatal(err)
	}

	contentStr := string(content)
	if !strings.Contains(contentStr, "PR #42") {
		t.Error("expected PR number in document")
	}
	if !strings.Contains(contentStr, "Test summary") {
		t.Error("expected summary in document")
	}
}
