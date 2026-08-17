package model

import (
	"context"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
)

func TestParseOpenRouterPrivacyFallback(t *testing.T) {
	tests := []struct {
		value string
		want  OpenRouterPrivacyFallback
		bad   bool
	}{
		{value: "", want: OpenRouterPrivacyFallbackNone},
		{value: "off", want: OpenRouterPrivacyFallbackNone},
		{value: "zdr_then_data_collection_deny", want: OpenRouterPrivacyFallbackZDRThenDataCollection},
		{value: "unknown", bad: true},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			got, err := ParseOpenRouterPrivacyFallback(tt.value)
			if tt.bad {
				if err == nil {
					t.Fatalf("ParseOpenRouterPrivacyFallback(%q) error = nil", tt.value)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("ParseOpenRouterPrivacyFallback(%q) = %q, %v; want %q", tt.value, got, err, tt.want)
			}
		})
	}
}

func TestChatCompletionZDRFallsBackToDataCollectionDeny(t *testing.T) {
	modelID := "deepseek/deepseek-v4-pro-0813"
	provider := &stubProvider{
		id:      "openrouter",
		catalog: ModelCatalog{Data: []ModelInfo{{ID: modelID}}},
		errors: []error{&APIError{
			StatusCode: 404,
			Message:    "No endpoints found matching your data policy (Zero data retention).",
		}},
	}
	mgr := &Manager{
		config:         &config.Config{},
		providers:      map[string]Provider{"openrouter": provider},
		providerOrder:  []string{"openrouter"},
		catalog:        map[string]ModelInfo{modelID: provider.catalog.Data[0]},
		providerModels: map[string][]string{"openrouter": {modelID}},
		modelProviders: map[string]string{modelID: "openrouter"},
	}
	if err := mgr.SetOpenRouterPrivacyFallback(OpenRouterPrivacyFallbackZDRThenDataCollection); err != nil {
		t.Fatal(err)
	}

	resp, err := mgr.ChatCompletion(context.Background(), ChatRequest{
		Model:    modelID,
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil || resp == nil {
		t.Fatalf("ChatCompletion() = %#v, %v", resp, err)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("requests = %d, want ZDR attempt plus one fallback", len(provider.requests))
	}
	first, second := provider.requests[0], provider.requests[1]
	if first.Provider["zdr"] != true || first.Provider["data_collection"] != nil {
		t.Fatalf("first provider policy = %#v, want zdr=true only", first.Provider)
	}
	if second.Provider["zdr"] != nil || second.Provider["data_collection"] != "deny" {
		t.Fatalf("fallback provider policy = %#v, want data_collection=deny only", second.Provider)
	}
}

func TestChatCompletionPrivacyFallbackDoesNotOverrideExplicitPolicy(t *testing.T) {
	modelID := "deepseek/deepseek-v4-pro-0813"
	provider := &stubProvider{
		id:      "openrouter",
		catalog: ModelCatalog{Data: []ModelInfo{{ID: modelID}}},
		errors:  []error{&APIError{StatusCode: 404, Message: "No endpoints found matching your data policy (Zero data retention)."}},
	}
	mgr := &Manager{
		config:         &config.Config{},
		providers:      map[string]Provider{"openrouter": provider},
		providerOrder:  []string{"openrouter"},
		catalog:        map[string]ModelInfo{modelID: provider.catalog.Data[0]},
		providerModels: map[string][]string{"openrouter": {modelID}},
		modelProviders: map[string]string{modelID: "openrouter"},
	}
	if err := mgr.SetOpenRouterPrivacyFallback(OpenRouterPrivacyFallbackZDRThenDataCollection); err != nil {
		t.Fatal(err)
	}
	_, err := mgr.ChatCompletion(context.Background(), ChatRequest{
		Model:    modelID,
		Messages: []Message{{Role: "user", Content: "hello"}},
		Provider: map[string]any{"zdr": true},
	})
	if err == nil || !strings.Contains(err.Error(), "Zero data retention") {
		t.Fatalf("ChatCompletion() error = %v, want original explicit-policy failure", err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("requests = %d, explicit privacy policy must not be retried", len(provider.requests))
	}
}

func TestChatCompletionStreamZDRFallsBackBeforeAnyChunk(t *testing.T) {
	modelID := "deepseek/deepseek-v4-pro-0813"
	provider := &stubProvider{
		id:      "openrouter",
		catalog: ModelCatalog{Data: []ModelInfo{{ID: modelID}}},
		streamPlans: []stubStreamPlan{
			{err: &APIError{StatusCode: 404, Message: "No endpoints found matching your data policy (Zero data retention)."}},
			{chunks: []StreamChunk{{Choices: []StreamChoice{{Delta: MessageDelta{Content: "OK"}}}}}},
		},
	}
	mgr := &Manager{
		config:         &config.Config{},
		providers:      map[string]Provider{"openrouter": provider},
		providerOrder:  []string{"openrouter"},
		catalog:        map[string]ModelInfo{modelID: provider.catalog.Data[0]},
		providerModels: map[string][]string{"openrouter": {modelID}},
		modelProviders: map[string]string{modelID: "openrouter"},
	}
	if err := mgr.SetOpenRouterPrivacyFallback(OpenRouterPrivacyFallbackZDRThenDataCollection); err != nil {
		t.Fatal(err)
	}
	chunks, errs := mgr.ChatCompletionStream(context.Background(), ChatRequest{Model: modelID})
	for range chunks {
	}
	if err := <-errs; err != nil {
		t.Fatalf("stream error = %v", err)
	}
	if len(provider.streamRequests) != 2 {
		t.Fatalf("stream requests = %d, want ZDR attempt plus one fallback", len(provider.streamRequests))
	}
	if provider.streamRequests[0].Provider["zdr"] != true {
		t.Fatalf("initial stream policy = %#v, want zdr=true", provider.streamRequests[0].Provider)
	}
	if provider.streamRequests[1].Provider["data_collection"] != "deny" || provider.streamRequests[1].Provider["zdr"] != nil {
		t.Fatalf("fallback stream policy = %#v, want data_collection=deny", provider.streamRequests[1].Provider)
	}
}

func TestValidateBuckbotPrivacyFallback(t *testing.T) {
	valid := config.DefaultConfig()
	valid.Buckbot.OpenRouterPrivacyFallback = "zdr_then_data_collection_deny"
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid privacy fallback rejected: %v", err)
	}
	invalid := config.DefaultConfig()
	invalid.Buckbot.OpenRouterPrivacyFallback = "weaken-it"
	if err := invalid.Validate(); err == nil || !strings.Contains(err.Error(), "openrouter_privacy_fallback") {
		t.Fatalf("invalid privacy fallback error = %v", err)
	}
}
