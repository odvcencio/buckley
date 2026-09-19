package model

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"m31labs.dev/buckley/pkg/config"
)

func TestOpenAICompatibleProvider_ModelInfoNormalization(t *testing.T) {
	const prefix = "litellm/"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/model/info" {
			t.Errorf("unexpected route: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
			{"model_name": "fresh-model", "model_info": map[string]any{"mode": "chat", "max_input_tokens": 8192, "max_tokens": 4096, "max_output_tokens": 64, "input_cost_per_token": 0.000001, "output_cost_per_token": 0.000002, "supports_function_calling": true, "supports_vision": true}},
			{"model_name": "  " + prefix + "prefixed-model  ", "model_info": map[string]any{"max_input_tokens": 16384, "max_output_tokens": 128}},
			{"model_name": "legacy-context", "model_info": map[string]any{"max_tokens": 512}},
			{"model_name": "invalid-limits", "model_info": map[string]any{"max_input_tokens": -1, "max_tokens": -1, "max_output_tokens": -1}},
			{"model_name": "null-output", "model_info": map[string]any{"max_output_tokens": nil}},
			{"model_name": " ", "model_info": map[string]any{}},
			{"model_name": "embedding-only", "model_info": map[string]any{"mode": "embedding"}},
		}})
	}))
	defer server.Close()
	settings := config.OpenAICompatibleConfig{
		BaseURL:             server.URL,
		ContextLengths:      map[string]int{prefix + "prefixed-model": 32768},
		SupportedParameters: map[string][]string{prefix + "prefixed-model": {"reasoning_effort"}},
	}
	provider := NewLiteLLMProvider(config.LiteLLMConfig(settings), false)
	provider.httpClient = server.Client()
	catalog, err := provider.FetchCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Data) != 5 {
		t.Fatalf("catalog entries=%d, want5: %+v", len(catalog.Data), catalog.Data)
	}
	for i, want := range []struct {
		name            string
		context, output int
	}{
		{"fresh-model", 8192, 64}, {"prefixed-model", 32768, 128}, {"legacy-context", 512, 0}, {"invalid-limits", 8192, 0}, {"null-output", 8192, 0},
	} {
		got := catalog.Data[i]
		if got.ID != prefix+want.name || got.Name != want.name || got.ContextLength != want.context || got.MaxCompletionTokens != want.output {
			t.Errorf("entry %d: got=%+v want id=%s context=%d output=%d", i, got, prefix+want.name, want.context, want.output)
		}
	}
	fresh := catalog.Data[0]
	if fresh.Pricing.Prompt != 1 || fresh.Pricing.Completion != 2 || fresh.Architecture.Modality != "text+image" || !reflect.DeepEqual(fresh.SupportedParameters, []string{"tools", "functions"}) {
		t.Fatalf("lost existing pricing or capabilities: %+v", fresh)
	}
	prefixed := catalog.Data[1]
	if prefixed.Architecture.Modality != "text" || !reflect.DeepEqual(prefixed.SupportedParameters, []string{"reasoning_effort"}) {
		t.Fatalf("configured parameters or default modality lost: %+v", prefixed)
	}
	if prefixed.PricingKnown {
		t.Fatal("missing prices must not become known-free")
	}
}
