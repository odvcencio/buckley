package docs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewHierarchyManager(t *testing.T) {
	hm := NewHierarchyManager("/tmp/docs")
	if hm == nil {
		t.Fatal("NewHierarchyManager returned nil")
	}
	if hm.rootDir != "/tmp/docs" {
		t.Errorf("expected rootDir '/tmp/docs', got %q", hm.rootDir)
	}
}

func TestHierarchyManager_Initialize(t *testing.T) {
	tmpDir := t.TempDir()
	hm := NewHierarchyManager(tmpDir)

	err := hm.Initialize()
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	// Check that directories were created
	requiredDirs := []string{
		tmpDir,
		filepath.Join(tmpDir, "architecture"),
		filepath.Join(tmpDir, "architecture", "decisions"),
	}

	for _, dir := range requiredDirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			t.Errorf("directory not created: %s", dir)
		}
	}

	// Check that README files were created
	requiredFiles := []string{
		filepath.Join(tmpDir, "README.md"),
		filepath.Join(tmpDir, "architecture", "overview.md"),
		filepath.Join(tmpDir, "architecture", "decisions", "README.md"),
	}

	for _, file := range requiredFiles {
		if _, err := os.Stat(file); os.IsNotExist(err) {
			t.Errorf("file not created: %s", file)
		}
	}
}

func TestHierarchyManager_Exists(t *testing.T) {
	tmpDir := t.TempDir()
	hm := NewHierarchyManager(tmpDir)

	// Should not exist initially
	if hm.Exists() {
		t.Error("Exists returned true for uninitialized hierarchy")
	}

	// Initialize
	if err := hm.Initialize(); err != nil {
		t.Fatal(err)
	}

	// Should exist now
	if !hm.Exists() {
		t.Error("Exists returned false after Initialize")
	}
}

func TestHierarchyManager_ValidateStructure(t *testing.T) {
	tmpDir := t.TempDir()
	hm := NewHierarchyManager(tmpDir)

	// Should fail before initialization
	err := hm.ValidateStructure()
	if err == nil {
		t.Error("expected validation to fail before Initialize")
	}

	// Initialize
	if err := hm.Initialize(); err != nil {
		t.Fatal(err)
	}

	// Should pass after initialization
	err = hm.ValidateStructure()
	if err != nil {
		t.Errorf("validation failed after Initialize: %v", err)
	}
}

func TestHierarchyManager_ValidateStructure_MissingFile(t *testing.T) {
	tmpDir := t.TempDir()
	hm := NewHierarchyManager(tmpDir)

	// Initialize
	if err := hm.Initialize(); err != nil {
		t.Fatal(err)
	}

	// Remove a required file
	os.Remove(filepath.Join(tmpDir, "README.md"))

	// Validation should fail
	err := hm.ValidateStructure()
	if err == nil {
		t.Error("expected validation to fail with missing file")
	}
}

func TestFileExists(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test.txt")

	// Should not exist
	if fileExists(filePath) {
		t.Error("fileExists returned true for nonexistent file")
	}

	// Create file
	if err := os.WriteFile(filePath, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	// Should exist
	if !fileExists(filePath) {
		t.Error("fileExists returned false for existing file")
	}
}

func TestFileExists_Directory(t *testing.T) {
	tmpDir := t.TempDir()

	// Directory should not pass fileExists
	if fileExists(tmpDir) {
		t.Error("fileExists returned true for directory")
	}
}

func TestDirExists(t *testing.T) {
	tmpDir := t.TempDir()

	// Should exist
	if !dirExists(tmpDir) {
		t.Error("dirExists returned false for existing directory")
	}

	// Should not exist
	if dirExists(filepath.Join(tmpDir, "nonexistent")) {
		t.Error("dirExists returned true for nonexistent directory")
	}
}

func TestDirExists_File(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test.txt")

	if err := os.WriteFile(filePath, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	// File should not pass dirExists
	if dirExists(filePath) {
		t.Error("dirExists returned true for file")
	}
}
