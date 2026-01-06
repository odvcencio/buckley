package envdetect

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCache_SetAndGet(t *testing.T) {
	tmpDir := t.TempDir()
	cache := NewCache(tmpDir)

	profile := &EnvironmentProfile{
		Languages: []Language{
			{Name: "go", Version: "1.22"},
		},
		DetectedAt: time.Now(),
		CacheKey:   "test-key",
	}

	// Set profile
	if err := cache.Set("test-key", profile); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	// Get profile
	retrieved, found := cache.Get("test-key")
	if !found {
		t.Fatal("Get() returned false for existing key")
	}

	if retrieved.CacheKey != profile.CacheKey {
		t.Errorf("CacheKey mismatch: got %s, want %s", retrieved.CacheKey, profile.CacheKey)
	}

	if len(retrieved.Languages) != 1 {
		t.Errorf("Expected 1 language, got %d", len(retrieved.Languages))
	}

	if retrieved.Languages[0].Name != "go" {
		t.Errorf("Language name mismatch: got %s, want go", retrieved.Languages[0].Name)
	}
}

func TestCache_GetNonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	cache := NewCache(tmpDir)

	_, found := cache.Get("nonexistent-key")
	if found {
		t.Error("Get() returned true for non-existent key")
	}
}

func TestCache_OverwriteExisting(t *testing.T) {
	tmpDir := t.TempDir()
	cache := NewCache(tmpDir)

	profile1 := &EnvironmentProfile{
		Languages:  []Language{{Name: "go", Version: "1.21"}},
		CacheKey:   "test-key",
		DetectedAt: time.Now(),
	}

	profile2 := &EnvironmentProfile{
		Languages:  []Language{{Name: "go", Version: "1.22"}},
		CacheKey:   "test-key",
		DetectedAt: time.Now(),
	}

	// Set first profile
	if err := cache.Set("test-key", profile1); err != nil {
		t.Fatal(err)
	}

	// Overwrite with second profile
	if err := cache.Set("test-key", profile2); err != nil {
		t.Fatal(err)
	}

	// Verify second profile is retrieved
	retrieved, found := cache.Get("test-key")
	if !found {
		t.Fatal("Get() returned false")
	}

	if retrieved.Languages[0].Version != "1.22" {
		t.Errorf("Expected version 1.22, got %s", retrieved.Languages[0].Version)
	}
}

func TestComputeCacheKey(t *testing.T) {
	tmpDir := t.TempDir()

	// Create lockfiles
	goMod := `module test
go 1.22`
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(goMod), 0644); err != nil {
		t.Fatal(err)
	}

	// Compute cache key
	key1 := computeCacheKey(tmpDir, []string{"go.mod"})

	if key1 == "" {
		t.Error("computeCacheKey() returned empty string")
	}

	if len(key1) != 16 {
		t.Errorf("Expected key length 16, got %d", len(key1))
	}

	// Compute again with same file
	key2 := computeCacheKey(tmpDir, []string{"go.mod"})

	if key1 != key2 {
		t.Error("Cache keys differ for same file content")
	}

	// Modify file and verify key changes
	goModModified := `module test
go 1.23`
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(goModModified), 0644); err != nil {
		t.Fatal(err)
	}

	key3 := computeCacheKey(tmpDir, []string{"go.mod"})

	if key1 == key3 {
		t.Error("Cache key unchanged after file modification")
	}
}

func TestComputeCacheKey_NonExistentFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Compute cache key with non-existent file
	key := computeCacheKey(tmpDir, []string{"nonexistent.txt"})

	// Should still return a key (based on empty data)
	if key == "" {
		t.Error("computeCacheKey() returned empty string for non-existent file")
	}
}

func TestNewCache_CreatesDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	cacheDir := filepath.Join(tmpDir, "cache", "subdir")

	cache := NewCache(cacheDir)

	// Verify directory was created
	if _, err := os.Stat(cacheDir); os.IsNotExist(err) {
		t.Error("NewCache() did not create cache directory")
	}

	// Verify we can use the cache
	profile := &EnvironmentProfile{CacheKey: "test"}
	if err := cache.Set("test", profile); err != nil {
		t.Errorf("Set() failed after directory creation: %v", err)
	}
}
