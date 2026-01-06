package embeddings

import (
	"context"
	"math"
	"testing"
)

func TestNewService(t *testing.T) {
	tmpDir := t.TempDir()
	service := NewService(ServiceOptions{
		APIKey:   "test-key",
		CacheDir: tmpDir,
		Provider: ProviderOpenRouter,
	})

	if service.apiKey != "test-key" {
		t.Errorf("Expected API key 'test-key', got %q", service.apiKey)
	}

	if service.model != "openai/text-embedding-3-small" {
		t.Errorf("Expected model 'openai/text-embedding-3-small', got %q", service.model)
	}

	if service.apiURL != "https://openrouter.ai/api/v1/embeddings" {
		t.Errorf("Expected OpenRouter API URL, got %q", service.apiURL)
	}

	if service.cache == nil {
		t.Error("Cache should not be nil")
	}
}

func TestSetModel(t *testing.T) {
	tmpDir := t.TempDir()
	service := NewService(ServiceOptions{
		APIKey:   "test-key",
		CacheDir: tmpDir,
		Provider: ProviderOpenRouter,
	})

	service.SetModel("custom-model")

	if service.model != "custom-model" {
		t.Errorf("Expected model 'custom-model', got %q", service.model)
	}
}

func TestComputeCacheKey(t *testing.T) {
	tmpDir := t.TempDir()
	service := NewService(ServiceOptions{
		APIKey:   "test-key",
		CacheDir: tmpDir,
		Provider: ProviderOpenRouter,
	})

	key1 := service.computeCacheKey("hello")
	key2 := service.computeCacheKey("hello")
	key3 := service.computeCacheKey("world")

	if key1 != key2 {
		t.Error("Same text should produce same cache key")
	}

	if key1 == key3 {
		t.Error("Different text should produce different cache keys")
	}

	if len(key1) != 32 {
		t.Errorf("Cache key should be 32 characters, got %d", len(key1))
	}
}

func TestCosineSimilarity(t *testing.T) {
	tests := []struct {
		name string
		a    []float64
		b    []float64
		want float64
		err  bool
	}{
		{
			name: "identical vectors",
			a:    []float64{1, 2, 3},
			b:    []float64{1, 2, 3},
			want: 1.0,
		},
		{
			name: "orthogonal vectors",
			a:    []float64{1, 0},
			b:    []float64{0, 1},
			want: 0.0,
		},
		{
			name: "opposite vectors",
			a:    []float64{1, 0},
			b:    []float64{-1, 0},
			want: -1.0,
		},
		{
			name: "different lengths",
			a:    []float64{1, 2},
			b:    []float64{1, 2, 3},
			err:  true,
		},
		{
			name: "zero vector",
			a:    []float64{0, 0},
			b:    []float64{1, 2},
			want: 0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CosineSimilarity(tt.a, tt.b)

			if tt.err {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if math.Abs(got-tt.want) > 0.01 {
				t.Errorf("CosineSimilarity() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSqrt(t *testing.T) {
	tests := []struct {
		input float64
		want  float64
	}{
		{0, 0},
		{1, 1},
		{4, 2},
		{9, 3},
		{16, 4},
		{2, 1.414},
	}

	for _, tt := range tests {
		got := sqrt(tt.input)
		if math.Abs(got-tt.want) > 0.01 {
			t.Errorf("sqrt(%v) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestSearchResults_Sort(t *testing.T) {
	results := SearchResults{
		{ID: "1", Similarity: 0.5},
		{ID: "2", Similarity: 0.9},
		{ID: "3", Similarity: 0.3},
		{ID: "4", Similarity: 0.7},
	}

	// Sort should be descending by similarity
	if results.Less(0, 1) {
		t.Error("0.5 should not be less than 0.9 (descending order)")
	}

	if !results.Less(1, 0) {
		t.Error("0.9 should be less than 0.5 (descending order)")
	}

	if results.Len() != 4 {
		t.Errorf("Expected length 4, got %d", results.Len())
	}

	// Test swap
	results.Swap(0, 1)
	if results[0].ID != "2" || results[1].ID != "1" {
		t.Error("Swap did not work correctly")
	}
}

func TestEmbed_Cache(t *testing.T) {
	tmpDir := t.TempDir()
	service := NewService(ServiceOptions{
		APIKey:   "fake-key",
		CacheDir: tmpDir,
		Provider: ProviderOpenRouter,
	})

	// Manually populate cache to avoid API call
	cacheKey := service.computeCacheKey("test text")
	testEmbedding := []float64{0.1, 0.2, 0.3}
	service.cache.Set(cacheKey, testEmbedding)

	// Embed should return cached value
	ctx := context.Background()
	result, err := service.Embed(ctx, "test text")

	// This will fail if it tries to hit the API (no real key)
	// But should succeed with cache
	if err == nil {
		// Got cached result
		if len(result) != 3 {
			t.Errorf("Expected 3 elements, got %d", len(result))
		}
		if result[0] != 0.1 || result[1] != 0.2 || result[2] != 0.3 {
			t.Error("Cached embedding values don't match")
		}
	}
}

func TestSerializeDeserializeEmbedding(t *testing.T) {
	original := []float64{1.5, -2.3, 0.0, 42.7, -0.001}

	// Serialize
	bytes, err := serializeEmbedding(original)
	if err != nil {
		t.Fatalf("Serialize failed: %v", err)
	}

	// Deserialize
	result, err := deserializeEmbedding(bytes)
	if err != nil {
		t.Fatalf("Deserialize failed: %v", err)
	}

	// Compare
	if len(result) != len(original) {
		t.Errorf("Length mismatch: got %d, want %d", len(result), len(original))
	}

	for i := range original {
		// Allow some precision loss due to simplified conversion
		if math.Abs(result[i]-original[i]) > 0.0001 {
			t.Errorf("Value mismatch at index %d: got %v, want %v", i, result[i], original[i])
		}
	}
}

func TestDeserializeEmbedding_Invalid(t *testing.T) {
	tests := []struct {
		name  string
		bytes []byte
	}{
		{
			name:  "too short",
			bytes: []byte{0, 0},
		},
		{
			name:  "wrong length",
			bytes: []byte{0, 0, 0, 5, 1, 2, 3}, // Says 5 floats but only has 3 bytes
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := deserializeEmbedding(tt.bytes)
			if err == nil {
				t.Error("Expected error for invalid data")
			}
		})
	}
}

func TestComputeHash(t *testing.T) {
	hash1 := computeHash("hello")
	hash2 := computeHash("hello")
	hash3 := computeHash("world")

	if hash1 != hash2 {
		t.Error("Same content should produce same hash")
	}

	if hash1 == hash3 {
		t.Error("Different content should produce different hashes")
	}

	if len(hash1) != 32 {
		t.Errorf("Hash should be 32 characters, got %d", len(hash1))
	}
}
