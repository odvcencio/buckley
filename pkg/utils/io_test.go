package utils

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadFileLines_Success(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test.txt")

	content := "line1\nline2\nline3"
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	lines, err := ReadFileLines(filePath)
	if err != nil {
		t.Fatalf("ReadFileLines failed: %v", err)
	}

	if len(lines) != 3 {
		t.Errorf("expected 3 lines, got %d", len(lines))
	}
	if lines[0] != "line1" {
		t.Errorf("expected line1, got %q", lines[0])
	}
	if lines[1] != "line2" {
		t.Errorf("expected line2, got %q", lines[1])
	}
	if lines[2] != "line3" {
		t.Errorf("expected line3, got %q", lines[2])
	}
}

func TestReadFileLines_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "empty.txt")

	if err := os.WriteFile(filePath, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	lines, err := ReadFileLines(filePath)
	if err != nil {
		t.Fatalf("ReadFileLines failed: %v", err)
	}

	if len(lines) != 0 {
		t.Errorf("expected empty lines, got %d lines", len(lines))
	}
}

func TestReadFileLines_NonexistentFile(t *testing.T) {
	_, err := ReadFileLines("/nonexistent/file.txt")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestReadFile_Success(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test.txt")

	content := []byte("test content")
	if err := os.WriteFile(filePath, content, 0644); err != nil {
		t.Fatal(err)
	}

	data, err := ReadFile(filePath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	if string(data) != "test content" {
		t.Errorf("expected 'test content', got %q", string(data))
	}
}

func TestReadFile_NonexistentFile(t *testing.T) {
	_, err := ReadFile("/nonexistent/file.txt")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestFileExists_Exists(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test.txt")

	if err := os.WriteFile(filePath, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	if !FileExists(filePath) {
		t.Error("FileExists returned false for existing file")
	}
}

func TestFileExists_NotExists(t *testing.T) {
	if FileExists("/nonexistent/file.txt") {
		t.Error("FileExists returned true for nonexistent file")
	}
}

func TestFileExists_Directory(t *testing.T) {
	tmpDir := t.TempDir()

	if !FileExists(tmpDir) {
		t.Error("FileExists returned false for existing directory")
	}
}

func TestWalkGoFiles_Success(t *testing.T) {
	tmpDir := t.TempDir()

	// Create Go files
	if err := os.WriteFile(filepath.Join(tmpDir, "file1.go"), []byte("package main"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "file2.go"), []byte("package main"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create non-Go file
	if err := os.WriteFile(filepath.Join(tmpDir, "readme.md"), []byte("# README"), 0644); err != nil {
		t.Fatal(err)
	}

	var visited []string
	err := WalkGoFiles(tmpDir, func(path string, info os.FileInfo) error {
		visited = append(visited, filepath.Base(path))
		return nil
	})

	if err != nil {
		t.Fatalf("WalkGoFiles failed: %v", err)
	}

	if len(visited) != 2 {
		t.Errorf("expected 2 Go files, got %d", len(visited))
	}
}

func TestWalkGoFiles_SkipHiddenDirs(t *testing.T) {
	tmpDir := t.TempDir()

	// Create hidden directory with Go file
	hiddenDir := filepath.Join(tmpDir, ".hidden")
	if err := os.Mkdir(hiddenDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hiddenDir, "hidden.go"), []byte("package main"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create normal Go file
	if err := os.WriteFile(filepath.Join(tmpDir, "normal.go"), []byte("package main"), 0644); err != nil {
		t.Fatal(err)
	}

	var visited []string
	err := WalkGoFiles(tmpDir, func(path string, info os.FileInfo) error {
		visited = append(visited, filepath.Base(path))
		return nil
	})

	if err != nil {
		t.Fatalf("WalkGoFiles failed: %v", err)
	}

	// Should only visit normal.go, not hidden.go
	if len(visited) != 1 {
		t.Errorf("expected 1 Go file, got %d", len(visited))
	}
	if visited[0] != "normal.go" {
		t.Errorf("expected 'normal.go', got %q", visited[0])
	}
}

func TestWalkGoFiles_SkipVendor(t *testing.T) {
	tmpDir := t.TempDir()

	// Create vendor directory with Go file
	vendorDir := filepath.Join(tmpDir, "vendor")
	if err := os.Mkdir(vendorDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vendorDir, "vendor.go"), []byte("package main"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create normal Go file
	if err := os.WriteFile(filepath.Join(tmpDir, "normal.go"), []byte("package main"), 0644); err != nil {
		t.Fatal(err)
	}

	var visited []string
	err := WalkGoFiles(tmpDir, func(path string, info os.FileInfo) error {
		visited = append(visited, filepath.Base(path))
		return nil
	})

	if err != nil {
		t.Fatalf("WalkGoFiles failed: %v", err)
	}

	// Should only visit normal.go, not vendor.go
	if len(visited) != 1 {
		t.Errorf("expected 1 Go file, got %d", len(visited))
	}
	if visited[0] != "normal.go" {
		t.Errorf("expected 'normal.go', got %q", visited[0])
	}
}

func TestWalkGoFiles_Nested(t *testing.T) {
	tmpDir := t.TempDir()

	// Create nested directory structure
	subDir := filepath.Join(tmpDir, "pkg", "sub")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(tmpDir, "root.go"), []byte("package main"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "pkg", "pkg.go"), []byte("package pkg"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "sub.go"), []byte("package sub"), 0644); err != nil {
		t.Fatal(err)
	}

	var visited []string
	err := WalkGoFiles(tmpDir, func(path string, info os.FileInfo) error {
		visited = append(visited, filepath.Base(path))
		return nil
	})

	if err != nil {
		t.Fatalf("WalkGoFiles failed: %v", err)
	}

	if len(visited) != 3 {
		t.Errorf("expected 3 Go files, got %d", len(visited))
	}
}

func TestWalkGoFiles_EmptyDir(t *testing.T) {
	tmpDir := t.TempDir()

	var visited []string
	err := WalkGoFiles(tmpDir, func(path string, info os.FileInfo) error {
		visited = append(visited, filepath.Base(path))
		return nil
	})

	if err != nil {
		t.Fatalf("WalkGoFiles failed: %v", err)
	}

	if len(visited) != 0 {
		t.Errorf("expected 0 Go files, got %d", len(visited))
	}
}

func TestWalkGoFiles_NonexistentDir(t *testing.T) {
	// WalkGoFiles skips inaccessible directories without error
	var visited []string
	err := WalkGoFiles("/nonexistent/directory", func(path string, info os.FileInfo) error {
		visited = append(visited, filepath.Base(path))
		return nil
	})

	// Should not error, just skip
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(visited) != 0 {
		t.Errorf("expected 0 files visited, got %d", len(visited))
	}
}

func TestWalkGoFiles_CallbackError(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "test.go"), []byte("package main"), 0644); err != nil {
		t.Fatal(err)
	}

	testErr := os.ErrNotExist
	err := WalkGoFiles(tmpDir, func(path string, info os.FileInfo) error {
		return testErr
	})

	if err != testErr {
		t.Errorf("expected callback error to be returned, got: %v", err)
	}
}
