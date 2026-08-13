package model

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCatalogCache_MissingFileReturnsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.json")
	catalog, err := LoadCatalogCache(path)
	if err != nil {
		t.Fatalf("LoadCatalogCache: %v", err)
	}
	if len(catalog) != 0 {
		t.Fatalf("catalog = %v, want empty", catalog)
	}
}

func TestSaveAndLoadCatalogCache_RoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "model_catalog.json")
	original := map[string]ModelInfo{
		"openai/gpt-4o": {
			ID:                  "openai/gpt-4o",
			Name:                "GPT-4o",
			ContextLength:       128000,
			MaxCompletionTokens: 16384,
			Pricing:             ModelPricing{Prompt: 2.5, Completion: 10},
			PricingKnown:        true,
		},
		"anthropic/claude-4.5": {
			ID:   "anthropic/claude-4.5",
			Name: "Claude 4.5",
		},
	}

	if err := SaveCatalogCache(path, original); err != nil {
		t.Fatalf("SaveCatalogCache: %v", err)
	}

	loaded, err := LoadCatalogCache(path)
	if err != nil {
		t.Fatalf("LoadCatalogCache: %v", err)
	}
	if len(loaded) != len(original) {
		t.Fatalf("loaded len = %d, want %d", len(loaded), len(original))
	}
	if loaded["openai/gpt-4o"].ContextLength != 128000 {
		t.Fatalf("loaded gpt-4o ContextLength = %d, want 128000", loaded["openai/gpt-4o"].ContextLength)
	}
	if loaded["openai/gpt-4o"].MaxCompletionTokens != 16384 {
		t.Fatalf("loaded gpt-4o MaxCompletionTokens = %d, want 16384", loaded["openai/gpt-4o"].MaxCompletionTokens)
	}
	if !loaded["openai/gpt-4o"].PricingKnown {
		t.Fatal("loaded gpt-4o lost authoritative pricing marker")
	}
	if loaded["anthropic/claude-4.5"].Name != "Claude 4.5" {
		t.Fatalf("loaded claude Name = %q, want Claude 4.5", loaded["anthropic/claude-4.5"].Name)
	}
}

func TestLoadCatalogCache_RejectsMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "model_catalog.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := LoadCatalogCache(path); err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}
