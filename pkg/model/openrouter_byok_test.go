package model

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
)

func TestOpenRouterBYOKRequest(t *testing.T) {
	pins := normalizeOpenRouterBYOKPins(map[string]string{
		" OpenAI/ ":                     " OpenAI ",
		"openai/gpt-6-luna-pro-special": "azure",
		"":                              "ignored",
	})
	tests := []struct {
		name string
		req  ChatRequest
		want any // expected provider["only"]; nil means no pin
	}{
		{name: "vendor prefix", req: ChatRequest{Model: "openai/gpt-6-luna-pro"}, want: []string{"openai"}},
		{name: "unqualified openai model", req: ChatRequest{Model: "gpt-6-luna-pro"}, want: []string{"openai"}},
		{name: "longest prefix wins", req: ChatRequest{Model: "openai/gpt-6-luna-pro-special"}, want: []string{"azure"}},
		{name: "keeps privacy fields", req: ChatRequest{Model: "openai/gpt-6-luna-pro", Provider: map[string]any{"zdr": true}}, want: []string{"openai"}},
		{name: "no matching pin", req: ChatRequest{Model: "anthropic/claude-sonnet-4.5"}},
		{name: "caller only wins", req: ChatRequest{Model: "openai/gpt-6-luna-pro", Provider: map[string]any{"only": []string{"azure"}}}, want: []string{"azure"}},
		{name: "caller order wins", req: ChatRequest{Model: "openai/gpt-6-luna-pro", Provider: map[string]any{"order": []string{"azure"}}}},
		{name: "mixed fallback chain", req: ChatRequest{Model: "openai/gpt-6-luna-pro", Models: []string{"openai/gpt-6-luna-pro", "anthropic/claude-sonnet-4.5"}}},
		{name: "same-pin fallback chain", req: ChatRequest{Model: "openai/gpt-6-luna-pro", Models: []string{"openai/gpt-6-luna-pro", "openai/gpt-6-luna"}}, want: []string{"openai"}},
		{name: "launch request stays exact", req: ChatRequest{Model: "openai/gpt-6-luna-pro", RetryMode: RequestRetrySingleAttempt}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := cloneAnyMap(tt.req.Provider)
			got := openRouterBYOKRequest(tt.req, pins)
			if !reflect.DeepEqual(got.Provider["only"], tt.want) && !(tt.want == nil && got.Provider["only"] == nil) {
				t.Fatalf("provider.only = %#v, want %#v (provider %#v)", got.Provider["only"], tt.want, got.Provider)
			}
			if tt.req.Provider != nil && !reflect.DeepEqual(tt.req.Provider, before) {
				t.Fatalf("caller provider map mutated: %#v", tt.req.Provider)
			}
			if tt.name == "keeps privacy fields" && got.Provider["zdr"] != true {
				t.Fatalf("pin dropped zdr: %#v", got.Provider)
			}
		})
	}
	if got := openRouterBYOKRequest(ChatRequest{Model: "openai/gpt-6-luna-pro"}, nil); got.Provider != nil {
		t.Fatalf("no pins configured: provider = %#v, want nil", got.Provider)
	}
}

func newBYOKTestManager(provider *stubProvider, modelID string) *Manager {
	return &Manager{
		config:         &config.Config{},
		providers:      map[string]Provider{"openrouter": provider},
		providerOrder:  []string{"openrouter"},
		catalog:        map[string]ModelInfo{modelID: {ID: modelID}},
		providerModels: map[string][]string{"openrouter": {modelID}},
		modelProviders: map[string]string{modelID: "openrouter"},
		openRouterBYOK: normalizeOpenRouterBYOKPins(map[string]string{"openai/": "openai"}),
	}
}

// A pinned BYOK provider with no ZDR endpoint makes OpenRouter answer 404; the
// opt-in fallback must retry with data_collection=deny and keep the pin, so the
// request never lands on a credit-billed endpoint.
func TestChatCompletionBYOKPinSurvivesZDRFallback(t *testing.T) {
	modelID := "openai/gpt-6-luna-pro"
	provider := &stubProvider{
		id:      "openrouter",
		catalog: ModelCatalog{Data: []ModelInfo{{ID: modelID}}},
		errors: []error{&APIError{
			StatusCode: 404,
			Message:    "No endpoints found matching your data policy (Zero data retention).",
		}},
	}
	mgr := newBYOKTestManager(provider, modelID)
	if err := mgr.SetOpenRouterPrivacyFallback(OpenRouterPrivacyFallbackZDRThenDataCollection); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.ChatCompletion(context.Background(), ChatRequest{Model: modelID}); err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("requests = %d, want ZDR attempt plus one fallback", len(provider.requests))
	}
	for i, req := range provider.requests {
		if !reflect.DeepEqual(req.Provider["only"], []string{"openai"}) {
			t.Fatalf("request %d provider.only = %#v, want [openai]", i, req.Provider["only"])
		}
	}
	if provider.requests[0].Provider["zdr"] != true {
		t.Fatalf("first request = %#v, want zdr=true", provider.requests[0].Provider)
	}
	if provider.requests[1].Provider["data_collection"] != "deny" || provider.requests[1].Provider["zdr"] != nil {
		t.Fatalf("fallback request = %#v, want data_collection=deny", provider.requests[1].Provider)
	}
}

func TestChatCompletionStreamAppliesBYOKPin(t *testing.T) {
	modelID := "openai/gpt-6-luna-pro"
	provider := &stubProvider{
		id:          "openrouter",
		catalog:     ModelCatalog{Data: []ModelInfo{{ID: modelID}}},
		streamPlans: []stubStreamPlan{{chunks: []StreamChunk{{Choices: []StreamChoice{{Delta: MessageDelta{Content: "OK"}}}}}}},
	}
	mgr := newBYOKTestManager(provider, modelID)
	chunks, errs := mgr.ChatCompletionStream(context.Background(), ChatRequest{Model: modelID})
	for range chunks {
	}
	if err := <-errs; err != nil {
		t.Fatalf("stream error = %v", err)
	}
	if len(provider.streamRequests) != 1 || !reflect.DeepEqual(provider.streamRequests[0].Provider["only"], []string{"openai"}) {
		t.Fatalf("stream requests = %#v, want one request pinned to openai", provider.streamRequests)
	}
}

func TestAPIErrorNamesCreditBilledRoute(t *testing.T) {
	nonBYOK := &APIError{
		StatusCode: 402,
		Message:    "Prompt tokens limit exceeded: 70193 > 2255.",
		Details:    `openrouter_metadata: {"requested":"openai/gpt-6-luna-pro","is_byok":false}`,
	}
	if got := nonBYOK.Error(); !strings.Contains(got, "is_byok=false") || !strings.Contains(got, "byok_providers") {
		t.Fatalf("402 on a non-BYOK route = %q, want the route cause and the byok_providers remedy", got)
	}
	plain := &APIError{StatusCode: 402, Message: "Insufficient credits."}
	if got := plain.Error(); strings.Contains(got, "is_byok") {
		t.Fatalf("402 without routing metadata = %q, want no BYOK hint", got)
	}
	byok := &APIError{StatusCode: 402, Message: "Insufficient credits.", Details: `openrouter_metadata: {"is_byok": true}`}
	if got := byok.Error(); strings.Contains(got, "is_byok=false") {
		t.Fatalf("402 on a BYOK route = %q, want no hint", got)
	}
}

func TestValidateOpenRouterBYOKProviders(t *testing.T) {
	valid := config.DefaultConfig()
	valid.Providers.OpenRouter.BYOKProviders = map[string]string{"openai/": "openai"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid pin rejected: %v", err)
	}
	for name, mutate := range map[string]func(*config.Config){
		"empty slug":     func(c *config.Config) { c.Providers.OpenRouter.BYOKProviders = map[string]string{"openai/": " "} },
		"bad slug":       func(c *config.Config) { c.Providers.OpenRouter.BYOKProviders = map[string]string{"openai/": "Open AI"} },
		"empty prefix":   func(c *config.Config) { c.Providers.OpenRouter.BYOKProviders = map[string]string{" ": "openai"} },
		"wrong provider": func(c *config.Config) { c.Providers.OpenAI.BYOKProviders = map[string]string{"openai/": "openai"} },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			mutate(cfg)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "byok_providers") {
				t.Fatalf("Validate() error = %v, want byok_providers rejection", err)
			}
		})
	}
}
