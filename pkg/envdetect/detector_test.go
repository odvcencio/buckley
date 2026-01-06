package envdetect

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDetector_DetectGo(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()

	// Create go.mod
	goMod := `module github.com/test/project

go 1.22

require (
	github.com/lib/pq v1.10.9
)`
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(goMod), 0644); err != nil {
		t.Fatal(err)
	}

	// Create go.sum
	if err := os.WriteFile(filepath.Join(tmpDir, "go.sum"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	// Create detector
	detector := NewDetector(tmpDir)

	// Run detection
	profile, err := detector.Detect()
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}

	// Verify Go was detected
	if len(profile.Languages) == 0 {
		t.Fatal("Expected Go to be detected, but no languages found")
	}

	goLang := profile.Languages[0]
	if goLang.Name != "go" {
		t.Errorf("Expected language name 'go', got '%s'", goLang.Name)
	}

	if goLang.Version != "1.22" {
		t.Errorf("Expected version '1.22', got '%s'", goLang.Version)
	}

	if len(goLang.Lockfiles) != 2 {
		t.Errorf("Expected 2 lockfiles, got %d", len(goLang.Lockfiles))
	}
}

func TestDetector_DetectNode(t *testing.T) {
	tmpDir := t.TempDir()

	// Create package.json
	packageJSON := `{
  "name": "test-project",
  "version": "1.0.0",
  "dependencies": {
    "express": "^4.18.0"
  }
}`
	if err := os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte(packageJSON), 0644); err != nil {
		t.Fatal(err)
	}

	// Create .nvmrc
	if err := os.WriteFile(filepath.Join(tmpDir, ".nvmrc"), []byte("20.0.0"), 0644); err != nil {
		t.Fatal(err)
	}

	detector := NewDetector(tmpDir)
	profile, err := detector.Detect()
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}

	if len(profile.Languages) == 0 {
		t.Fatal("Expected Node to be detected, but no languages found")
	}

	nodeLang := profile.Languages[0]
	if nodeLang.Name != "node" {
		t.Errorf("Expected language name 'node', got '%s'", nodeLang.Name)
	}

	if nodeLang.Version != "20.0.0" {
		t.Errorf("Expected version '20.0.0', got '%s'", nodeLang.Version)
	}
}

func TestDetector_DetectPostgres(t *testing.T) {
	tmpDir := t.TempDir()

	// Create go.mod
	goMod := `module github.com/test/project

go 1.22
`
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(goMod), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a Go file with postgres import
	mainGo := `package main

import (
	"database/sql"
	_ "github.com/lib/pq"
)

func main() {
	db, _ := sql.Open("postgres", "connection-string")
	defer db.Close()
}
`
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(mainGo), 0644); err != nil {
		t.Fatal(err)
	}

	detector := NewDetector(tmpDir)
	profile, err := detector.Detect()
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}

	// Verify Postgres was detected
	found := false
	for _, svc := range profile.Services {
		if svc.Type == "postgres" {
			found = true
			if svc.Version != "16" {
				t.Errorf("Expected Postgres version '16', got '%s'", svc.Version)
			}
			if len(svc.Ports) != 1 || svc.Ports[0].Host != 5432 {
				t.Errorf("Expected port 5432, got %+v", svc.Ports)
			}
		}
	}

	if !found {
		t.Error("Expected Postgres service to be detected")
	}
}

func TestDetector_DetectRedis(t *testing.T) {
	tmpDir := t.TempDir()

	// Create go.mod
	goMod := `module github.com/test/project

go 1.22
`
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(goMod), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a Go file with redis import
	mainGo := `package main

import (
	"github.com/redis/go-redis/v9"
)

func main() {
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})
	_ = client
}
`
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(mainGo), 0644); err != nil {
		t.Fatal(err)
	}

	detector := NewDetector(tmpDir)
	profile, err := detector.Detect()
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}

	// Verify Redis was detected
	found := false
	for _, svc := range profile.Services {
		if svc.Type == "redis" {
			found = true
			if svc.Version != "7" {
				t.Errorf("Expected Redis version '7', got '%s'", svc.Version)
			}
		}
	}

	if !found {
		t.Error("Expected Redis service to be detected")
	}
}

func TestDetector_Caching(t *testing.T) {
	tmpDir := t.TempDir()

	// Create go.mod
	goMod := `module github.com/test/project

go 1.22
`
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(goMod), 0644); err != nil {
		t.Fatal(err)
	}

	detector := NewDetector(tmpDir)

	// First detection
	profile1, err := detector.Detect()
	if err != nil {
		t.Fatalf("First Detect() error = %v", err)
	}

	// Second detection (should use cache)
	profile2, err := detector.Detect()
	if err != nil {
		t.Fatalf("Second Detect() error = %v", err)
	}

	// Verify cache keys match
	if profile1.CacheKey != profile2.CacheKey {
		t.Errorf("Cache keys don't match: %s != %s", profile1.CacheKey, profile2.CacheKey)
	}

	// Verify detection times are same (proving cache was used)
	// Allow for small time differences due to JSON serialization
	timeDiff := profile2.DetectedAt.Sub(profile1.DetectedAt)
	if timeDiff < 0 {
		timeDiff = -timeDiff
	}
	if timeDiff > 1*time.Millisecond {
		t.Error("Detection times differ significantly, cache was not used")
	}

	// Also verify cache file exists
	cacheFile := filepath.Join(tmpDir, ".buckley", "cache", "envdetect-"+profile1.CacheKey+".json")
	if _, err := os.Stat(cacheFile); os.IsNotExist(err) {
		t.Error("Cache file does not exist")
	}
}

func TestDetector_MultiLanguage(t *testing.T) {
	tmpDir := t.TempDir()

	// Create go.mod
	goMod := `module github.com/test/project

go 1.22
`
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(goMod), 0644); err != nil {
		t.Fatal(err)
	}

	// Create package.json
	packageJSON := `{"name": "test"}`
	if err := os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte(packageJSON), 0644); err != nil {
		t.Fatal(err)
	}

	detector := NewDetector(tmpDir)
	profile, err := detector.Detect()
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}

	if len(profile.Languages) != 2 {
		t.Errorf("Expected 2 languages, got %d", len(profile.Languages))
	}

	// Verify both Go and Node were detected
	langs := make(map[string]bool)
	for _, lang := range profile.Languages {
		langs[lang.Name] = true
	}

	if !langs["go"] {
		t.Error("Expected Go to be detected")
	}

	if !langs["node"] {
		t.Error("Expected Node to be detected")
	}
}

func TestDetector_EmptyProject(t *testing.T) {
	tmpDir := t.TempDir()

	detector := NewDetector(tmpDir)
	profile, err := detector.Detect()
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}

	if len(profile.Languages) != 0 {
		t.Errorf("Expected no languages, got %d", len(profile.Languages))
	}

	if len(profile.Services) != 0 {
		t.Errorf("Expected no services, got %d", len(profile.Services))
	}
}

func TestFileExists(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a test file
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	if !fileExists(testFile) {
		t.Error("fileExists() returned false for existing file")
	}

	if fileExists(filepath.Join(tmpDir, "nonexistent.txt")) {
		t.Error("fileExists() returned true for non-existent file")
	}
}
