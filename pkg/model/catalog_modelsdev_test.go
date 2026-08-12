package model

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const modelsDevFixture = `{
  "openai": {
    "id": "openai",
    "name": "OpenAI",
    "models": {
      "gpt-4o": {
        "id": "gpt-4o",
        "name": "GPT-4o",
        "description": "Omni-era GPT for multimodal chat",
        "reasoning": false,
        "tool_call": true,
        "attachment": true,
        "temperature": true,
        "structured_output": true,
        "limit": {"context": 128000, "output": 16384},
        "cost": {"input": 2.5, "output": 10},
        "modalities": {"input": ["text", "image"], "output": ["text"]}
      }
    }
  },
  "anthropic": {
    "id": "anthropic",
    "name": "Anthropic",
    "models": {
      "claude-4.5": {
        "id": "claude-4.5",
        "name": "Claude 4.5",
        "reasoning": true,
        "tool_call": true,
        "limit": {"context": 200000, "output": 64000},
        "cost": {"input": 3, "output": 15},
        "modalities": {"input": ["text"], "output": ["text"]}
      }
    }
  }
}`

func TestFetchModelsDevCatalog_ParsesFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(modelsDevFixture))
	}))
	defer server.Close()

	catalog, err := FetchModelsDevCatalog(context.Background(), server.Client(), server.URL)
	if err != nil {
		t.Fatalf("FetchModelsDevCatalog: %v", err)
	}
	if len(catalog) != 2 {
		t.Fatalf("catalog providers = %d, want 2", len(catalog))
	}
	openai, ok := catalog["openai"]
	if !ok {
		t.Fatal("expected an openai provider entry")
	}
	model, ok := openai.Models["gpt-4o"]
	if !ok {
		t.Fatal("expected a gpt-4o model entry")
	}
	if model.Cost.Input != 2.5 || model.Cost.Output != 10 {
		t.Fatalf("gpt-4o cost = %+v, want input=2.5 output=10", model.Cost)
	}
}

func TestFetchModelsDevCatalog_NetworkErrorIsWrapped(t *testing.T) {
	_, err := FetchModelsDevCatalog(context.Background(), http.DefaultClient, "http://127.0.0.1:0/does-not-exist")
	if err == nil {
		t.Fatal("expected an error for an unreachable URL")
	}
}

func TestFetchModelsDevCatalog_NonOKStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	_, err := FetchModelsDevCatalog(context.Background(), server.Client(), server.URL)
	if err == nil {
		t.Fatal("expected an error for a non-200 response")
	}
}

func TestFetchModelsDevCatalog_BadJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer server.Close()

	_, err := FetchModelsDevCatalog(context.Background(), server.Client(), server.URL)
	if err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

func TestMergeModelsDevCatalog_AddsNewAndUpdatesExisting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(modelsDevFixture))
	}))
	defer server.Close()
	catalog, err := FetchModelsDevCatalog(context.Background(), server.Client(), server.URL)
	if err != nil {
		t.Fatalf("FetchModelsDevCatalog: %v", err)
	}

	base := map[string]ModelInfo{
		"openai/gpt-4o": {
			ID:            "openai/gpt-4o",
			Name:          "stale name",
			ContextLength: 1,
			Pricing:       ModelPricing{Prompt: 0.01, Completion: 0.02},
		},
		"custom/curated-only": {
			ID:   "custom/curated-only",
			Name: "Curated model models.dev does not know about",
		},
	}

	merged := MergeModelsDevCatalog(base, catalog)

	if len(merged) != 3 {
		t.Fatalf("merged len = %d, want 3 (2 base-overlap/updated + 1 new anthropic model, custom kept)", len(merged))
	}

	gpt4o, ok := merged["openai/gpt-4o"]
	if !ok {
		t.Fatal("expected openai/gpt-4o to survive the merge")
	}
	if gpt4o.Name != "GPT-4o" {
		t.Fatalf("gpt-4o Name = %q, want refreshed 'GPT-4o'", gpt4o.Name)
	}
	if gpt4o.ContextLength != 128000 {
		t.Fatalf("gpt-4o ContextLength = %d, want 128000", gpt4o.ContextLength)
	}
	if gpt4o.MaxCompletionTokens != 16384 {
		t.Fatalf("gpt-4o MaxCompletionTokens = %d, want 16384", gpt4o.MaxCompletionTokens)
	}
	if gpt4o.Pricing.Prompt != 2.5 || gpt4o.Pricing.Completion != 10 {
		t.Fatalf("gpt-4o Pricing = %+v, want {2.5 10}", gpt4o.Pricing)
	}
	if gpt4o.Architecture.Modality != "text+image" {
		t.Fatalf("gpt-4o Modality = %q, want text+image", gpt4o.Architecture.Modality)
	}
	wantParams := map[string]bool{"tools": true, "attachment": true, "temperature": true, "structured_output": true}
	if len(gpt4o.SupportedParameters) != len(wantParams) {
		t.Fatalf("gpt-4o SupportedParameters = %v, want %v", gpt4o.SupportedParameters, wantParams)
	}

	claude, ok := merged["anthropic/claude-4.5"]
	if !ok {
		t.Fatal("expected the new anthropic/claude-4.5 entry to be added")
	}
	if claude.Pricing.Prompt != 3 {
		t.Fatalf("claude Pricing.Prompt = %v, want 3", claude.Pricing.Prompt)
	}

	if _, ok := merged["custom/curated-only"]; !ok {
		t.Fatal("expected a curated model models.dev does not know about to survive the merge (merge, not replace)")
	}
}

func TestMergeModelsDevCatalog_EmptyBaseAddsEverything(t *testing.T) {
	catalog := ModelsDevCatalog{
		"openai": ModelsDevProvider{
			ID: "openai",
			Models: map[string]ModelsDevModel{
				"gpt-4o": {ID: "gpt-4o", Name: "GPT-4o", Cost: ModelsDevCost{Input: 2.5, Output: 10}},
			},
		},
	}
	merged := MergeModelsDevCatalog(nil, catalog)
	if len(merged) != 1 {
		t.Fatalf("merged len = %d, want 1", len(merged))
	}
	if _, ok := merged["openai/gpt-4o"]; !ok {
		t.Fatal("expected openai/gpt-4o in the merged catalog")
	}
}

func TestModelsDevSupportedParameters_NoCapabilitiesIsEmpty(t *testing.T) {
	params := modelsDevSupportedParameters(ModelsDevModel{})
	if len(params) != 0 {
		t.Fatalf("params = %v, want empty", params)
	}
}

func TestModelsDevModality_TextOnly(t *testing.T) {
	if got := modelsDevModality(ModelsDevModalities{Input: []string{"text"}, Output: []string{"text"}}); got != "text" {
		t.Fatalf("modality = %q, want text", got)
	}
}
