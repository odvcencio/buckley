package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"m31labs.dev/buckley/pkg/model"
)

const modelsRefreshFixture = `{
  "openai": {
    "id": "openai",
    "name": "OpenAI",
    "models": {
      "gpt-4o": {
        "id": "gpt-4o",
        "name": "GPT-4o",
        "tool_call": true,
        "limit": {"context": 128000, "output": 16384},
        "cost": {"input": 2.5, "output": 10},
        "modalities": {"input": ["text", "image"], "output": ["text"]}
      }
    }
  }
}`

func newModelsDevFixtureServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestRunModelsRefreshCommand_WritesMergedCache(t *testing.T) {
	server := newModelsDevFixtureServer(t, modelsRefreshFixture)
	cachePath := filepath.Join(t.TempDir(), "model_catalog.json")

	err := runModelsRefreshCommand([]string{"--url", server.URL, "--cache", cachePath})
	if err != nil {
		t.Fatalf("runModelsRefreshCommand: %v", err)
	}

	catalog, err := model.LoadCatalogCache(cachePath)
	if err != nil {
		t.Fatalf("LoadCatalogCache: %v", err)
	}
	if len(catalog) != 1 {
		t.Fatalf("catalog entries = %d, want 1", len(catalog))
	}
	info, ok := catalog["openai/gpt-4o"]
	if !ok {
		t.Fatal("expected an openai/gpt-4o entry")
	}
	if info.Pricing.Prompt != 2.5 {
		t.Fatalf("Pricing.Prompt = %v, want 2.5", info.Pricing.Prompt)
	}
	if info.Pricing.Completion != 10 {
		t.Fatalf("Pricing.Completion = %v, want 10", info.Pricing.Completion)
	}
}

func TestRunModelsRefreshCommand_MergesWithExistingCache(t *testing.T) {
	server := newModelsDevFixtureServer(t, modelsRefreshFixture)
	cachePath := filepath.Join(t.TempDir(), "model_catalog.json")

	existing := map[string]model.ModelInfo{
		"custom/curated-only": {ID: "custom/curated-only", Name: "Curated"},
	}
	if err := model.SaveCatalogCache(cachePath, existing); err != nil {
		t.Fatalf("SaveCatalogCache: %v", err)
	}

	if err := runModelsRefreshCommand([]string{"--url", server.URL, "--cache", cachePath}); err != nil {
		t.Fatalf("runModelsRefreshCommand: %v", err)
	}

	loaded, err := model.LoadCatalogCache(cachePath)
	if err != nil {
		t.Fatalf("LoadCatalogCache: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("loaded len = %d, want 2 (merge, not replace)", len(loaded))
	}
	if _, ok := loaded["custom/curated-only"]; !ok {
		t.Fatal("expected the pre-existing curated entry to survive the merge")
	}
	if _, ok := loaded["openai/gpt-4o"]; !ok {
		t.Fatal("expected the new models.dev entry to be added")
	}
}

func TestRunModelsRefreshCommand_DryRunDoesNotWrite(t *testing.T) {
	server := newModelsDevFixtureServer(t, modelsRefreshFixture)
	cachePath := filepath.Join(t.TempDir(), "model_catalog.json")

	if err := runModelsRefreshCommand([]string{"--url", server.URL, "--cache", cachePath, "--dry-run"}); err != nil {
		t.Fatalf("runModelsRefreshCommand: %v", err)
	}

	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Fatalf("expected no cache file to be written in dry-run mode, stat err = %v", err)
	}
}

func TestRunModelsRefreshCommand_OfflineIsClearErrorNotCrash(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "model_catalog.json")

	err := runModelsRefreshCommand([]string{
		"--url", "http://127.0.0.1:0/unreachable",
		"--cache", cachePath,
		"--timeout", "500ms",
	})
	if err == nil {
		t.Fatal("expected an error when the models.dev endpoint is unreachable")
	}

	// The cache file must not exist or be corrupted by a failed refresh.
	if _, statErr := os.Stat(cachePath); !os.IsNotExist(statErr) {
		t.Fatalf("expected no cache file after an offline refresh, stat err = %v", statErr)
	}
}

func TestRunModelsCommand_UnknownSubcommand(t *testing.T) {
	if err := runModelsCommand([]string{"bogus"}); err == nil {
		t.Fatal("expected an error for an unknown models subcommand")
	}
	if err := runModelsCommand(nil); err == nil {
		t.Fatal("expected an error with no subcommand")
	}
}

func TestRunModelsRefreshCommand_RoutesFromModelsCommand(t *testing.T) {
	server := newModelsDevFixtureServer(t, modelsRefreshFixture)
	cachePath := filepath.Join(t.TempDir(), "model_catalog.json")

	err := runModelsCommand([]string{"refresh", "--url", server.URL, "--cache", cachePath})
	if err != nil {
		t.Fatalf("runModelsCommand: %v", err)
	}
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("expected cache file to exist: %v", err)
	}
}
