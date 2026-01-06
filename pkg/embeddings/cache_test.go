package embeddings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewCache(t *testing.T) {
	tmpDir := t.TempDir()
	cacheDir := filepath.Join(tmpDir, "cache")

	cache := NewCache(cacheDir)

	if cache.dir != cacheDir {
		t.Errorf("Expected dir %q, got %q", cacheDir, cache.dir)
	}

	// Check that directory was created
	if _, err := os.Stat(cacheDir); os.IsNotExist(err) {
		t.Error("Cache directory was not created")
	}
}

func TestCache_SetAndGet(t *testing.T) {
	tmpDir := t.TempDir()
	cache := NewCache(tmpDir)

	embedding := []float64{1.0, 2.0, 3.0, 4.0}
	key := "test-key"

	// Set
	if err := cache.Set(key, embedding); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Get
	result, ok := cache.Get(key)
	if !ok {
		t.Fatal("Get returned false for existing key")
	}

	if len(result) != len(embedding) {
		t.Errorf("Length mismatch: got %d, want %d", len(result), len(embedding))
	}

	for i := range embedding {
		if result[i] != embedding[i] {
			t.Errorf("Value mismatch at index %d: got %v, want %v", i, result[i], embedding[i])
		}
	}
}

func TestCache_GetNonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	cache := NewCache(tmpDir)

	result, ok := cache.Get("nonexistent")
	if ok {
		t.Error("Get should return false for non-existent key")
	}

	if result != nil {
		t.Error("Get should return nil for non-existent key")
	}
}

func TestCache_Overwrite(t *testing.T) {
	tmpDir := t.TempDir()
	cache := NewCache(tmpDir)

	key := "test-key"

	// First write
	embedding1 := []float64{1.0, 2.0}
	if err := cache.Set(key, embedding1); err != nil {
		t.Fatalf("First Set failed: %v", err)
	}

	// Overwrite
	embedding2 := []float64{3.0, 4.0, 5.0}
	if err := cache.Set(key, embedding2); err != nil {
		t.Fatalf("Second Set failed: %v", err)
	}

	// Get should return latest value
	result, ok := cache.Get(key)
	if !ok {
		t.Fatal("Get returned false")
	}

	if len(result) != 3 {
		t.Errorf("Expected length 3, got %d", len(result))
	}

	if result[0] != 3.0 || result[1] != 4.0 || result[2] != 5.0 {
		t.Error("Get returned old value instead of new value")
	}
}

func TestCache_Clear(t *testing.T) {
	tmpDir := t.TempDir()
	cache := NewCache(tmpDir)

	// Add some entries
	cache.Set("key1", []float64{1.0})
	cache.Set("key2", []float64{2.0})
	cache.Set("key3", []float64{3.0})

	// Verify they exist
	if _, ok := cache.Get("key1"); !ok {
		t.Fatal("key1 should exist before clear")
	}

	// Clear
	if err := cache.Clear(); err != nil {
		t.Fatalf("Clear failed: %v", err)
	}

	// Verify they're gone
	if _, ok := cache.Get("key1"); ok {
		t.Error("key1 should not exist after clear")
	}
	if _, ok := cache.Get("key2"); ok {
		t.Error("key2 should not exist after clear")
	}
	if _, ok := cache.Get("key3"); ok {
		t.Error("key3 should not exist after clear")
	}
}

func TestCache_ConcurrentAccess(t *testing.T) {
	tmpDir := t.TempDir()
	cache := NewCache(tmpDir)

	// Spawn multiple goroutines to test thread safety
	done := make(chan bool)

	for i := 0; i < 10; i++ {
		go func(id int) {
			key := "concurrent-test"
			embedding := []float64{float64(id)}

			cache.Set(key, embedding)
			cache.Get(key)

			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// If we get here without deadlock or panic, test passes
}
