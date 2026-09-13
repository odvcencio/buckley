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
			SupportedParameters: []string{},
		},
		"anthropic/claude-4.5": {
			ID:                  "anthropic/claude-4.5",
			Name:                "Claude 4.5",
			SupportedParameters: []string{"temperature"},
		},
	}
	info := original["openai/gpt-4o"]
	info.markSupportedParametersComplete()
	original["openai/gpt-4o"] = info
	info = original["anthropic/claude-4.5"]
	info.markSupportedParameterEvidence("tools", "functions", "temperature")
	original["anthropic/claude-4.5"] = info

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
	if got := loaded["openai/gpt-4o"].resolveParameterCapability("reasoning"); got != CapabilityNotAdvertised {
		t.Fatalf("explicit empty cache reasoning state = %s, want not_advertised", got)
	}
	if got := loaded["anthropic/claude-4.5"].resolveParameterCapability("temperature"); got != CapabilitySupported {
		t.Fatalf("partial cache temperature state = %s, want supported", got)
	}
	if got := loaded["anthropic/claude-4.5"].resolveParameterCapability("tools"); got != CapabilityNotAdvertised {
		t.Fatalf("partial cache tools state = %s, want not_advertised", got)
	}
	if got := loaded["anthropic/claude-4.5"].resolveParameterCapability("reasoning"); got != CapabilityUnknown {
		t.Fatalf("partial cache reasoning state = %s, want unknown", got)
	}
}

func TestLoadCatalogCache_PreservesExplicitCompletenessMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model_catalog.json")
	body := `{
	  "data": [
	    {"id":"explicit-empty","supported_parameters":[],"supported_parameters_complete":true},
	    {"id":"legacy-list","supported_parameters":["temperature"]},
	    {"id":"omitted"},
	    {"id":"null-params","supported_parameters":null}
	  ]
	}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	loaded, err := LoadCatalogCache(path)
	if err != nil {
		t.Fatalf("LoadCatalogCache: %v", err)
	}
	if got := loaded["explicit-empty"].resolveParameterCapability("reasoning"); got != CapabilityNotAdvertised {
		t.Fatalf("explicit empty state = %s, want not_advertised", got)
	}
	if got := loaded["legacy-list"].resolveParameterCapability("reasoning"); got != CapabilityUnknown {
		t.Fatalf("legacy list missing-parameter state = %s, want unknown", got)
	}
	if got := loaded["omitted"].resolveParameterCapability("reasoning"); got != CapabilityUnknown {
		t.Fatalf("omitted state = %s, want unknown", got)
	}
	if got := loaded["null-params"].resolveParameterCapability("reasoning"); got != CapabilityUnknown {
		t.Fatalf("null state = %s, want unknown", got)
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
