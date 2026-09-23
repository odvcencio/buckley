package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
)

func newTestOpenAICompatibleProvider(baseURL string) *OpenAICompatibleProvider {
	return NewOpenAICompatibleProvider(config.OpenAICompatibleConfig{BaseURL: baseURL, APIKey: "test-key"}, false)
}

func TestOpenAICompatibleProvider_ConfiguredSupportedParametersApplyToStaticCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "catalog unavailable", http.StatusUnauthorized)
	}))
	defer server.Close()

	provider := NewOpenAICompatibleProvider(config.OpenAICompatibleConfig{
		BaseURL: server.URL,
		APIKey:  "test-key",
		Models:  []string{"glm-5.3-flash"},
		SupportedParameters: map[string][]string{
			"glm-5.3-flash": {"tools"},
		},
		ContextLengths: map[string]int{"glm-5.3-flash": 204800},
	}, false)
	provider.httpClient = server.Client()

	catalog, err := provider.FetchCatalog()
	if err != nil {
		t.Fatalf("FetchCatalog() error = %v", err)
	}
	if len(catalog.Data) != 1 {
		t.Fatalf("catalog = %+v, want one static model", catalog.Data)
	}
	if catalog.Data[0].ID != "openai_compatible/glm-5.3-flash" {
		t.Fatalf("model ID = %q, want canonical provider prefix", catalog.Data[0].ID)
	}
	if catalog.Data[0].ContextLength != 204800 {
		t.Fatalf("context length = %d, want 204800", catalog.Data[0].ContextLength)
	}
	if got := catalog.Data[0].SupportedParameters; len(got) != 1 || got[0] != "tools" {
		t.Fatalf("supported parameters = %v, want [tools]", got)
	}
}

func TestOpenAICompatibleProvider_FetchModelsPreservesAdvertisedMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("path = %q, want /models", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[
			{
				"id":"glm-5.3-flash",
				"name":"GLM Flash",
				"description":"fast thinking model",
				"context_length":1048576,
				"max_completion_tokens":32768,
				"created":1790000000,
				"architecture":{"modality":"text+image","tokenizer":"test-tokenizer"},
				"supported_parameters":["tools","reasoning_effort"]
			},
			{
				"id":"openai_compatible/prefixed-model",
				"name":"openai_compatible/Prefixed Model",
				"context_length":512,
				"supported_parameters":["tools"]
			}
		]}`)
	}))
	defer server.Close()

	provider := NewOpenAICompatibleProvider(config.OpenAICompatibleConfig{
		BaseURL: server.URL,
		APIKey:  "test-key",
		SupportedParameters: map[string][]string{
			"glm-5.3-flash":                    {"reasoning_content", "tools", " "},
			"openai_compatible/prefixed-model": {"parallel_tool_calls"},
		},
		ContextLengths: map[string]int{"openai_compatible/glm-5.3-flash": 2048000},
	}, false)
	provider.httpClient = server.Client()

	catalog, err := provider.FetchCatalog()
	if err != nil {
		t.Fatalf("FetchCatalog() error = %v", err)
	}
	if len(catalog.Data) != 2 {
		t.Fatalf("catalog = %+v, want two fetched models", catalog.Data)
	}

	got := catalog.Data[0]
	if got.ID != "openai_compatible/glm-5.3-flash" {
		t.Fatalf("model ID = %q, want canonical provider prefix", got.ID)
	}
	if got.Name != "GLM Flash" {
		t.Fatalf("model name = %q, want provider-advertised name", got.Name)
	}
	if got.Description != "fast thinking model" || got.Created != 1790000000 {
		t.Fatalf("model metadata = %+v, want description and created preserved", got)
	}
	if got.ContextLength != 2048000 {
		t.Fatalf("context length = %d, want configured override 2048000", got.ContextLength)
	}
	if got.MaxCompletionTokens != 32768 {
		t.Fatalf("max completion tokens = %d, want 32768", got.MaxCompletionTokens)
	}
	if got.Architecture.Modality != "text+image" || got.Architecture.Tokenizer != "test-tokenizer" {
		t.Fatalf("architecture = %+v, want provider-advertised architecture preserved", got.Architecture)
	}
	wantParams := []string{"tools", "reasoning_effort", "reasoning_content"}
	if len(got.SupportedParameters) != len(wantParams) {
		t.Fatalf("supported parameters = %v, want %v", got.SupportedParameters, wantParams)
	}
	for i := range wantParams {
		if got.SupportedParameters[i] != wantParams[i] {
			t.Fatalf("supported parameters = %v, want %v", got.SupportedParameters, wantParams)
		}
	}

	got = catalog.Data[1]
	if got.ID != "openai_compatible/prefixed-model" {
		t.Fatalf("prefixed model ID = %q, want no double prefix", got.ID)
	}
	if got.Name != "Prefixed Model" {
		t.Fatalf("prefixed model name = %q, want display name without provider prefix", got.Name)
	}
	wantParams = []string{"tools", "parallel_tool_calls"}
	if len(got.SupportedParameters) != len(wantParams) {
		t.Fatalf("prefixed supported parameters = %v, want %v", got.SupportedParameters, wantParams)
	}
	for i := range wantParams {
		if got.SupportedParameters[i] != wantParams[i] {
			t.Fatalf("prefixed supported parameters = %v, want %v", got.SupportedParameters, wantParams)
		}
	}
}

func TestOpenAICompatibleProvider_FetchModelsAcceptsMaxModelLenAndMinimalID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("path = %q, want /models", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[
			{"id":"minimal","architecture":{"tokenizer":"partial-tokenizer"}},
			{"id":"long-context","max_model_len":262144,"top_provider":{"max_completion_tokens":16384}}
		]}`)
	}))
	defer server.Close()

	provider := newTestOpenAICompatibleProvider(server.URL)
	provider.httpClient = server.Client()

	catalog, err := provider.FetchCatalog()
	if err != nil {
		t.Fatalf("FetchCatalog() error = %v", err)
	}
	if len(catalog.Data) != 2 {
		t.Fatalf("catalog = %+v, want two fetched models", catalog.Data)
	}

	minimal := catalog.Data[0]
	if minimal.ID != "openai_compatible/minimal" || minimal.Name != "minimal" {
		t.Fatalf("minimal model = %+v, want prefixed ID and raw name", minimal)
	}
	if minimal.ContextLength != 8192 {
		t.Fatalf("minimal context length = %d, want default 8192", minimal.ContextLength)
	}
	if minimal.Architecture.Modality != "text" {
		t.Fatalf("minimal architecture = %+v, want text default", minimal.Architecture)
	}
	if minimal.Architecture.Tokenizer != "partial-tokenizer" {
		t.Fatalf("minimal tokenizer = %q, want preserved provider metadata", minimal.Architecture.Tokenizer)
	}
	if len(minimal.SupportedParameters) != 0 {
		t.Fatalf("minimal supported parameters = %v, want none", minimal.SupportedParameters)
	}

	longContext := catalog.Data[1]
	if longContext.ID != "openai_compatible/long-context" || longContext.Name != "long-context" {
		t.Fatalf("long context model = %+v, want prefixed ID and raw name", longContext)
	}
	if longContext.ContextLength != 262144 {
		t.Fatalf("long context length = %d, want max_model_len 262144", longContext.ContextLength)
	}
	if longContext.MaxCompletionTokens != 16384 {
		t.Fatalf("long context max completion tokens = %d, want top_provider 16384", longContext.MaxCompletionTokens)
	}
}

func TestLiteLLMLegacyAlias_ConfiguredSupportedParametersAugmentModelInfo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/model/info" {
			t.Fatalf("path = %q, want /model/info", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"model_name":"glm-5.3-flash","model_info":{"mode":"chat","supports_function_calling":true}}]}`)
	}))
	defer server.Close()

	provider := NewLiteLLMProvider(config.LiteLLMConfig{
		BaseURL: server.URL,
		APIKey:  "test-key",
		SupportedParameters: map[string][]string{
			"litellm/glm-5.3-flash": {"reasoning", "tools", " "},
		},
	}, false)
	provider.httpClient = server.Client()

	catalog, err := provider.FetchCatalog()
	if err != nil {
		t.Fatalf("FetchCatalog() error = %v", err)
	}
	got := catalog.Data[0].SupportedParameters
	want := []string{"tools", "functions", "reasoning"}
	if len(got) != len(want) {
		t.Fatalf("supported parameters = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("supported parameters = %v, want %v", got, want)
		}
	}
}

func TestOpenAICompatibleProvider_ReasoningEffortUsesConfiguredTopLevelField(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %q, want /chat/completions", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-1",
			"model":"glm-5.3-flash",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]
		}`)
	}))
	defer server.Close()

	provider := NewOpenAICompatibleProvider(config.OpenAICompatibleConfig{
		BaseURL: server.URL,
		APIKey:  "test-key",
		SupportedParameters: map[string][]string{
			"openai_compatible/glm-5.3-flash": {"reasoning_effort"},
		},
	}, false)
	provider.httpClient = server.Client()

	_, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Model:     "openai_compatible/glm-5.3-flash",
		Messages:  []Message{{Role: "user", Content: "hi"}},
		Reasoning: &ReasoningConfig{Effort: "low"},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if got := captured["reasoning_effort"]; got != "low" {
		t.Fatalf("reasoning_effort = %v, want low", got)
	}
	if _, ok := captured["reasoning"]; ok {
		t.Fatalf("request should not include nested reasoning when reasoning_effort is used: %#v", captured["reasoning"])
	}
}

func TestOpenAICompatibleProvider_ReasoningEffortDropsTokenBudgetBeforeTopLevelConversion(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-1",
			"model":"glm-5.3-flash",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]
		}`)
	}))
	defer server.Close()

	provider := NewOpenAICompatibleProvider(config.OpenAICompatibleConfig{
		BaseURL: server.URL,
		APIKey:  "test-key",
		SupportedParameters: map[string][]string{
			"glm-5.3-flash": {"reasoning_effort"},
		},
	}, false)
	provider.httpClient = server.Client()

	_, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Model:     "openai_compatible/glm-5.3-flash",
		Messages:  []Message{{Role: "user", Content: "hi"}},
		Reasoning: &ReasoningConfig{Effort: "low", MaxTokens: 128},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if got := captured["reasoning_effort"]; got != "low" {
		t.Fatalf("reasoning_effort = %v, want low", got)
	}
	if _, ok := captured["reasoning"]; ok {
		t.Fatalf("request should not include nested reasoning when effort is converted: %#v", captured["reasoning"])
	}
}

func TestOpenAICompatibleProvider_ReasoningTokenBudgetOnlyPreservesNestedReasoning(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-1",
			"model":"glm-5.3-flash",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]
		}`)
	}))
	defer server.Close()

	provider := NewOpenAICompatibleProvider(config.OpenAICompatibleConfig{
		BaseURL: server.URL,
		APIKey:  "test-key",
		SupportedParameters: map[string][]string{
			"glm-5.3-flash": {"reasoning_effort"},
		},
	}, false)
	provider.httpClient = server.Client()

	_, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Model:     "openai_compatible/glm-5.3-flash",
		Messages:  []Message{{Role: "user", Content: "hi"}},
		Reasoning: &ReasoningConfig{MaxTokens: 128},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if _, ok := captured["reasoning_effort"]; ok {
		t.Fatalf("request should not synthesize reasoning_effort without an effort: %#v", captured["reasoning_effort"])
	}
	reasoning, ok := captured["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("request should keep nested reasoning, got %#v", captured["reasoning"])
	}
	if _, ok := reasoning["effort"]; ok {
		t.Fatalf("reasoning.effort should be omitted for token-budget-only requests: %#v", reasoning)
	}
	if reasoning["max_tokens"] != float64(128) {
		t.Fatalf("reasoning.max_tokens = %#v, want 128", reasoning["max_tokens"])
	}
}

func TestOpenAICompatibleProvider_StreamReasoningEffortDropsTokenBudget(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"test-id\",\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	provider := NewOpenAICompatibleProvider(config.OpenAICompatibleConfig{
		BaseURL: server.URL,
		APIKey:  "test-key",
		SupportedParameters: map[string][]string{
			"provider-neutral-model": {"reasoning_effort"},
		},
	}, false)
	provider.httpClient = server.Client()
	chunks, errs := provider.ChatCompletionStream(context.Background(), ChatRequest{
		Model:     "openai_compatible/provider-neutral-model",
		Messages:  []Message{{Role: "user", Content: "hi"}},
		Reasoning: &ReasoningConfig{Effort: "medium", MaxTokens: 2048},
	})
	drainTestChatStream(t, chunks, errs)

	if captured["reasoning_effort"] != "medium" {
		t.Fatalf("reasoning_effort = %#v, want medium", captured["reasoning_effort"])
	}
	if _, ok := captured["reasoning"]; ok {
		t.Fatalf("stream request retained nested reasoning after effort conversion: %#v", captured)
	}
}

func TestOpenAICompatibleProvider_ReasoningEffortOmittedWhenUnsupported(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-1",
			"model":"glm-5.3-flash",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]
		}`)
	}))
	defer server.Close()

	provider := newTestOpenAICompatibleProvider(server.URL)
	provider.httpClient = server.Client()

	_, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Model:     "openai_compatible/glm-5.3-flash",
		Messages:  []Message{{Role: "user", Content: "hi"}},
		Reasoning: &ReasoningConfig{Effort: "low"},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if _, ok := captured["reasoning_effort"]; ok {
		t.Fatalf("request should not include unsupported reasoning_effort: %#v", captured["reasoning_effort"])
	}
	if _, ok := captured["reasoning"]; !ok {
		t.Fatalf("request should keep nested reasoning for providers without reasoning_effort support")
	}
}

func TestOpenAICompatibleProvider_ReasoningContentWireMappingIsCapabilityGated(t *testing.T) {
	rawReasoning := "思\n\n\n考\n\n\n alpha\n\n\n beta\n\n\n γ\n\n\n delta\n\n\n epsilon\n\n\n zeta\n\n\n eta\n\n\n."
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-1",
			"model":"glm-5.3-flash",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]
		}`)
	}))
	defer server.Close()

	provider := NewOpenAICompatibleProvider(config.OpenAICompatibleConfig{
		BaseURL: server.URL,
		APIKey:  "test-key",
		SupportedParameters: map[string][]string{
			"glm-5.3-flash": {"tools", "reasoning_effort", "reasoning_content"},
		},
	}, false)
	provider.httpClient = server.Client()

	_, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Model:     "openai_compatible/glm-5.3-flash",
		Reasoning: &ReasoningConfig{Effort: "low"},
		Messages: []Message{
			{Role: "user", Content: "start"},
			{
				Role:      "assistant",
				Content:   "",
				Reasoning: rawReasoning,
				ToolCalls: []ToolCall{{
					ID:   "call_α",
					Type: "function",
					Function: FunctionCall{
						Name:      "read_fixture",
						Arguments: `{"path":"fixtures/未知.txt"}`,
					},
				}},
			},
			{Role: "tool", ToolCallID: "call_α", Name: "read_fixture", Content: `{"nonce":"値-123"}`},
		},
		Tools: []map[string]any{{
			"type": "function",
			"function": map[string]any{
				"name":        "read_fixture",
				"description": "read one fixture",
				"parameters": map[string]any{
					"type":       "object",
					"properties": map[string]any{"path": map[string]any{"type": "string"}},
					"required":   []any{"path"},
				},
			},
		}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if got := captured["reasoning_effort"]; got != "low" {
		t.Fatalf("reasoning_effort = %v, want low", got)
	}
	messages := captured["messages"].([]any)
	assistant := messages[1].(map[string]any)
	if got := assistant["reasoning_content"]; got != rawReasoning {
		t.Fatalf("reasoning_content changed:\n got %#v\nwant %q", got, rawReasoning)
	}
	if _, ok := assistant["reasoning"]; ok {
		t.Fatalf("assistant message should not emit generic reasoning field: %#v", assistant["reasoning"])
	}
	toolCalls := assistant["tool_calls"].([]any)
	call := toolCalls[0].(map[string]any)
	if call["id"] != "call_α" {
		t.Fatalf("tool call id = %v, want call_α", call["id"])
	}
	fn := call["function"].(map[string]any)
	if fn["name"] != "read_fixture" || fn["arguments"] != `{"path":"fixtures/未知.txt"}` {
		t.Fatalf("tool call function = %#v", fn)
	}
	tool := messages[2].(map[string]any)
	if tool["tool_call_id"] != "call_α" || tool["content"] != `{"nonce":"値-123"}` {
		t.Fatalf("tool result did not round-trip: %#v", tool)
	}
}

func TestOpenAICompatibleProvider_ReasoningContentNotSentWithoutCapability(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-1",
			"model":"glm-5.3-flash",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]
		}`)
	}))
	defer server.Close()

	provider := NewOpenAICompatibleProvider(config.OpenAICompatibleConfig{
		BaseURL: server.URL,
		APIKey:  "test-key",
		SupportedParameters: map[string][]string{
			"glm-5.3-flash": {"tools"},
		},
	}, false)
	provider.httpClient = server.Client()

	_, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Model: "openai_compatible/glm-5.3-flash",
		Messages: []Message{{
			Role:      "assistant",
			Content:   "answer",
			Reasoning: "private",
		}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	messages := captured["messages"].([]any)
	assistant := messages[0].(map[string]any)
	if _, ok := assistant["reasoning_content"]; ok {
		t.Fatalf("reasoning_content leaked without capability: %#v", assistant)
	}
	if got := assistant["reasoning"]; got != "private" {
		t.Fatalf("direct provider fallback reasoning = %v, want generic reasoning", got)
	}
}

func TestOpenAICompatibleProvider_ObservedReasoningContentEnablesScopedContinuity(t *testing.T) {
	var captured []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"sparse-a"},{"id":"sparse-b"}]}`)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		captured = append(captured, payload)
		if payload["model"] == "sparse-a" && len(captured) == 1 {
			_, _ = io.WriteString(w, `{"id":"first","model":"sparse-a","choices":[{"index":0,"message":{"role":"assistant","reasoning_content":"native-private","content":"inspect"},"finish_reason":"stop"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"next","model":"test","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	provider := newTestOpenAICompatibleProvider(server.URL)
	provider.httpClient = server.Client()
	if _, err := provider.FetchCatalog(); err != nil {
		t.Fatalf("FetchCatalog() error = %v", err)
	}
	first, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Model:    "openai_compatible/sparse-a",
		Messages: []Message{{Role: "user", Content: "inspect"}},
	})
	if err != nil {
		t.Fatalf("first ChatCompletion() error = %v", err)
	}
	if !first.Choices[0].Message.ReasoningContent {
		t.Fatal("first response lost reasoning_content provenance")
	}
	if provider.supportsParameter("sparse-a", "reasoning_effort") {
		t.Fatal("observing reasoning_content must not infer reasoning_effort input support")
	}
	info, err := provider.GetModelInfo("openai_compatible/sparse-a")
	if err != nil || !containsString(info.SupportedParameters, "reasoning_content") {
		t.Fatalf("observed catalog info = %+v, err=%v", info, err)
	}

	for _, modelID := range []string{"sparse-a", "sparse-b"} {
		_, err := provider.ChatCompletion(context.Background(), ChatRequest{
			Model: "openai_compatible/" + modelID,
			Messages: []Message{{
				Role:      "assistant",
				Content:   "inspect",
				Reasoning: "native-private",
			}},
		})
		if err != nil {
			t.Fatalf("continuation ChatCompletion(%q) error = %v", modelID, err)
		}
	}

	learned := captured[1]["messages"].([]any)[0].(map[string]any)
	if learned["reasoning_content"] != "native-private" {
		t.Fatalf("learned model message = %#v, want native reasoning_content", learned)
	}
	if _, ok := learned["reasoning"]; ok {
		t.Fatalf("learned model retained generic reasoning field: %#v", learned)
	}
	unobserved := captured[2]["messages"].([]any)[0].(map[string]any)
	if _, ok := unobserved["reasoning_content"]; ok {
		t.Fatalf("observed capability leaked across models: %#v", unobserved)
	}
	if unobserved["reasoning"] != "native-private" {
		t.Fatalf("unobserved model generic reasoning = %#v", unobserved)
	}
}

func TestOpenAICompatibleProvider_DiscoveredReasoningCapabilitiesAffectWireRequest(t *testing.T) {
	captured := make(map[string]map[string]any)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/models":
			_, _ = io.WriteString(w, `{"data":[
				{"id":"discovered-reasoning","supported_parameters":["reasoning_effort","reasoning_content"]},
				{"id":"tools-only","supported_parameters":["tools"]}
			]}`)
		case "/chat/completions":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			modelID, _ := payload["model"].(string)
			captured[modelID] = payload
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl-1",
				"model":"test-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]
			}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := newTestOpenAICompatibleProvider(server.URL)
	provider.httpClient = server.Client()
	if _, err := provider.FetchCatalog(); err != nil {
		t.Fatalf("FetchCatalog() error = %v", err)
	}

	for _, modelID := range []string{"discovered-reasoning", "tools-only"} {
		_, err := provider.ChatCompletion(context.Background(), ChatRequest{
			Model:     "openai_compatible/" + modelID,
			Reasoning: &ReasoningConfig{Effort: "medium"},
			Messages: []Message{{
				Role:      "assistant",
				Content:   "answer",
				Reasoning: "private",
			}},
		})
		if err != nil {
			t.Fatalf("ChatCompletion(%q) error = %v", modelID, err)
		}
	}

	discovered := captured["discovered-reasoning"]
	if got := discovered["reasoning_effort"]; got != "medium" {
		t.Fatalf("discovered reasoning_effort = %v, want medium", got)
	}
	if _, ok := discovered["reasoning"]; ok {
		t.Fatalf("discovered request should translate nested reasoning: %#v", discovered)
	}
	discoveredAssistant := discovered["messages"].([]any)[0].(map[string]any)
	if got := discoveredAssistant["reasoning_content"]; got != "private" {
		t.Fatalf("discovered reasoning_content = %v, want private", got)
	}
	if _, ok := discoveredAssistant["reasoning"]; ok {
		t.Fatalf("discovered assistant should omit generic reasoning: %#v", discoveredAssistant)
	}

	unsupported := captured["tools-only"]
	if _, ok := unsupported["reasoning_effort"]; ok {
		t.Fatalf("unsupported request should omit reasoning_effort: %#v", unsupported)
	}
	if _, ok := unsupported["reasoning"]; !ok {
		t.Fatalf("unsupported request should preserve nested reasoning: %#v", unsupported)
	}
	unsupportedAssistant := unsupported["messages"].([]any)[0].(map[string]any)
	if _, ok := unsupportedAssistant["reasoning_content"]; ok {
		t.Fatalf("unsupported assistant should omit reasoning_content: %#v", unsupportedAssistant)
	}
	if got := unsupportedAssistant["reasoning"]; got != "private" {
		t.Fatalf("unsupported assistant reasoning = %v, want private", got)
	}
}

// TestOpenAICompatibleProvider_ChatCompletionRetriesTransientError proves the
// migration onto the shared ProviderTransport: a transient 429 is retried
// and recovered inside ChatCompletion instead of surfacing to the caller.
func TestOpenAICompatibleProvider_ChatCompletionRetriesTransientError(t *testing.T) {
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("authorization = %q, want Bearer test-key", got)
		}
		if atomic.AddInt32(&requests, 1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"rate limited","type":"rate_limit_error"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-1",
			"model":"test-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"recovered"},"finish_reason":"stop"}]
		}`)
	}))
	defer server.Close()

	provider := newTestOpenAICompatibleProvider(server.URL)
	provider.httpClient = server.Client()
	provider.transport.SetRetryConfig(RetryConfig{
		MaxRetries:          3,
		MaxRateLimitRetries: 3,
		InitialInterval:     time.Millisecond,
		MaxInterval:         2 * time.Millisecond,
		Multiplier:          2,
	})

	resp, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Model:    "openai_compatible/test-model",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v, want the transient 429 retried transparently", err)
	}
	if len(resp.Choices) == 0 || resp.Choices[0].Message.Content != "recovered" {
		t.Fatalf("response = %#v", resp)
	}
	if atomic.LoadInt32(&requests) != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

// TestOpenAICompatibleProvider_ChatCompletionSurfacesStructuredError proves a
// non-retryable error status becomes a structured *APIError instead of the
// prior implementation's plain fmt.Errorf(status, body).
func TestOpenAICompatibleProvider_ChatCompletionSurfacesStructuredError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid model","type":"invalid_request_error"}}`)
	}))
	defer server.Close()

	provider := newTestOpenAICompatibleProvider(server.URL)
	provider.httpClient = server.Client()

	_, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Model:    "openai_compatible/test-model",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v (%T), want *APIError", err, err)
	}
	if apiErr.StatusCode != http.StatusBadRequest || apiErr.Message != "invalid model" || apiErr.Retryable {
		t.Fatalf("apiErr = %#v", apiErr)
	}
}

// TestOpenAICompatibleProvider_ChatCompletionStreamDetectsMalformedChunk proves the
// migration onto the shared SSE parser: a malformed chunk in the middle of a
// stream is now a hard error instead of being silently skipped by the prior
// hand-rolled parser's `continue` on decode failure.
func TestOpenAICompatibleProvider_ChatCompletionStreamDetectsMalformedChunk(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = io.WriteString(w, "data: not-json\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer server.Close()

	provider := newTestOpenAICompatibleProvider(server.URL)
	provider.httpClient = server.Client()

	chunkChan, errChan := provider.ChatCompletionStream(context.Background(), ChatRequest{
		Model:    "openai_compatible/test-model",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})

	var gotChunk bool
	var gotErr error
	timeout := time.After(2 * time.Second)
loop:
	for {
		select {
		case chunk, ok := <-chunkChan:
			if !ok {
				chunkChan = nil
				if errChan == nil {
					break loop
				}
				continue
			}
			if len(chunk.Choices) > 0 {
				gotChunk = true
			}
		case err, ok := <-errChan:
			if !ok {
				errChan = nil
				if chunkChan == nil {
					break loop
				}
				continue
			}
			gotErr = err
		case <-timeout:
			t.Fatal("timeout waiting for stream")
		}
		if chunkChan == nil && errChan == nil {
			break
		}
	}

	if !gotChunk {
		t.Fatal("expected at least one valid chunk before the malformed one")
	}
	if gotErr == nil {
		t.Fatal("expected the malformed chunk to surface as an error, not be silently skipped")
	}
}

func TestOpenAICompatibleProvider_ChatCompletionStreamIdleTimeoutAfterPartialChunk(t *testing.T) {
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(100 * time.Millisecond)
		_, _ = io.WriteString(w, "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"late\"},\"finish_reason\":null}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer server.Close()

	provider := NewOpenAICompatibleProvider(config.OpenAICompatibleConfig{
		BaseURL:           server.URL,
		APIKey:            "test-key",
		StreamIdleTimeout: 25 * time.Millisecond,
	}, false)
	provider.httpClient = server.Client()

	chunkChan, errChan := provider.ChatCompletionStream(context.Background(), ChatRequest{
		Model:    "openai_compatible/test-model",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})

	var gotFirst bool
	var gotErr error
	timeout := time.After(time.Second)
	for chunkChan != nil || errChan != nil {
		select {
		case chunk, ok := <-chunkChan:
			if !ok {
				chunkChan = nil
				continue
			}
			if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content == "hi" {
				gotFirst = true
			}
		case err, ok := <-errChan:
			if !ok {
				errChan = nil
				continue
			}
			gotErr = err
		case <-timeout:
			t.Fatal("timeout waiting for idle error")
		}
	}
	if !gotFirst {
		t.Fatal("expected first chunk before idle timeout")
	}
	if gotErr == nil || !strings.Contains(gotErr.Error(), "stream idle timeout") {
		t.Fatalf("error = %v, want stream idle timeout", gotErr)
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Fatalf("requests = %d, want no replay after partial stream", got)
	}
}

func TestOpenAICompatibleProvider_ChatCompletionStreamReasoningContentResetsIdleTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"think\"},\"finish_reason\":null}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(20 * time.Millisecond)
		_, _ = io.WriteString(w, "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"more\"},\"finish_reason\":null}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(20 * time.Millisecond)
		_, _ = io.WriteString(w, "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer server.Close()

	provider := NewOpenAICompatibleProvider(config.OpenAICompatibleConfig{
		BaseURL:           server.URL,
		APIKey:            "test-key",
		StreamIdleTimeout: 35 * time.Millisecond,
	}, false)
	provider.httpClient = server.Client()

	chunkChan, errChan := provider.ChatCompletionStream(context.Background(), ChatRequest{
		Model:    "openai_compatible/test-model",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})

	var gotReasoning bool
	var gotContent bool
	var gotErr error
	timeout := time.After(time.Second)
	for chunkChan != nil || errChan != nil {
		select {
		case chunk, ok := <-chunkChan:
			if !ok {
				chunkChan = nil
				continue
			}
			for _, choice := range chunk.Choices {
				if choice.Delta.Reasoning != "" {
					gotReasoning = true
				}
				if choice.Delta.Content == "done" {
					gotContent = true
				}
			}
		case err, ok := <-errChan:
			if !ok {
				errChan = nil
				continue
			}
			gotErr = err
		case <-timeout:
			t.Fatal("timeout waiting for completed stream")
		}
	}
	if gotErr != nil {
		t.Fatalf("stream error = %v, want nil", gotErr)
	}
	if !gotReasoning || !gotContent {
		t.Fatalf("gotReasoning=%v gotContent=%v, want both", gotReasoning, gotContent)
	}
}

func TestOpenAICompatibleProvider_StreamObservationEnablesReasoningContinuity(t *testing.T) {
	requests := 0
	var continuation map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"id\":\"1\",\"model\":\"stream-sparse\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"private\"},\"finish_reason\":null}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"id\":\"1\",\"model\":\"stream-sparse\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&continuation); err != nil {
			t.Fatalf("decode continuation: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"2","model":"stream-sparse","choices":[{"index":0,"message":{"role":"assistant","content":"complete"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	provider := NewOpenAICompatibleProvider(config.OpenAICompatibleConfig{BaseURL: server.URL, APIKey: "test-key"}, false)
	provider.httpClient = server.Client()
	chunks, errs := provider.ChatCompletionStream(context.Background(), ChatRequest{
		Model:    "openai_compatible/stream-sparse",
		Messages: []Message{{Role: "user", Content: "start"}},
	})
	for range chunks {
	}
	for err := range errs {
		if err != nil {
			t.Fatalf("stream error = %v", err)
		}
	}

	_, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Model: "openai_compatible/stream-sparse",
		Messages: []Message{{
			Role:      "assistant",
			Content:   "done",
			Reasoning: "private",
		}},
	})
	if err != nil {
		t.Fatalf("continuation ChatCompletion() error = %v", err)
	}
	assistant := continuation["messages"].([]any)[0].(map[string]any)
	if assistant["reasoning_content"] != "private" {
		t.Fatalf("continuation message = %#v, want observed reasoning_content", assistant)
	}
}

func TestOpenAICompatibleProvider_ChatCompletionStreamHeartbeatOnlyDoesNotResetIdleTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, ": heartbeat\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(80 * time.Millisecond)
	}))
	defer server.Close()

	provider := NewOpenAICompatibleProvider(config.OpenAICompatibleConfig{
		BaseURL:           server.URL,
		APIKey:            "test-key",
		StreamIdleTimeout: 25 * time.Millisecond,
	}, false)
	provider.httpClient = server.Client()

	_, errChan := provider.ChatCompletionStream(context.Background(), ChatRequest{
		Model:    "openai_compatible/test-model",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err := waitForStreamError(t, errChan); err == nil || !strings.Contains(err.Error(), "stream idle timeout") {
		t.Fatalf("error = %v, want stream idle timeout", err)
	}
}

func TestOpenAICompatibleProvider_ChatCompletionStreamIdleTimeoutBeforeHeadersCancelsRequest(t *testing.T) {
	cancelled := make(chan struct{})
	var requests int32
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		atomic.AddInt32(&requests, 1)
		select {
		case <-r.Context().Done():
			close(cancelled)
			return nil, r.Context().Err()
		case <-time.After(time.Second):
			t.Fatal("request context was not cancelled")
			return nil, nil
		}
	})

	provider := NewOpenAICompatibleProvider(config.OpenAICompatibleConfig{
		BaseURL:           "http://example.test",
		APIKey:            "test-key",
		StreamIdleTimeout: 25 * time.Millisecond,
	}, false)
	provider.httpClient = &http.Client{Transport: transport}

	_, errChan := provider.ChatCompletionStream(context.Background(), ChatRequest{
		Model:    "openai_compatible/test-model",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err := waitForStreamError(t, errChan); err == nil || !strings.Contains(err.Error(), "stream idle timeout") {
		t.Fatalf("error = %v, want stream idle timeout", err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("server did not observe request cancellation")
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Fatalf("requests = %d, want one cancelled request", got)
	}
}

func TestOpenAICompatibleProvider_ChatCompletionStreamFirstContentTimeoutReasoningOnlyCancelsRequest(t *testing.T) {
	cancelled := make(chan struct{}, 1)
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for {
			_, _ = io.WriteString(w, "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"thinking\"},\"finish_reason\":null}]}\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			select {
			case <-r.Context().Done():
				select {
				case cancelled <- struct{}{}:
				default:
				}
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}))
	defer server.Close()

	provider := NewOpenAICompatibleProvider(config.OpenAICompatibleConfig{
		BaseURL:                   server.URL,
		APIKey:                    "test-key",
		StreamIdleTimeout:         30 * time.Millisecond,
		StreamFirstContentTimeout: 75 * time.Millisecond,
	}, false)
	provider.httpClient = server.Client()

	chunkChan, errChan := provider.ChatCompletionStream(context.Background(), ChatRequest{
		Model:    "openai_compatible/test-model",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})

	var gotReasoning bool
	var gotErr error
	timeout := time.After(time.Second)
	for chunkChan != nil || errChan != nil {
		select {
		case chunk, ok := <-chunkChan:
			if !ok {
				chunkChan = nil
				continue
			}
			for _, choice := range chunk.Choices {
				gotReasoning = gotReasoning || choice.Delta.Reasoning != ""
			}
		case err, ok := <-errChan:
			if !ok {
				errChan = nil
				continue
			}
			gotErr = err
		case <-timeout:
			t.Fatal("timeout waiting for first content timeout")
		}
	}
	if !gotReasoning {
		t.Fatal("expected reasoning-only chunks before first content timeout")
	}
	if gotErr == nil || !strings.Contains(gotErr.Error(), "stream first content timeout") {
		t.Fatalf("error = %v, want stream first content timeout", gotErr)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("server did not observe request cancellation")
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Fatalf("requests = %d, want no replay after a reasoning stream event", got)
	}
}

func TestOpenAICompatibleProvider_ChatCompletionStreamFirstContentReasoningChunkLimitCancelsRequest(t *testing.T) {
	cancelled := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for {
			_, _ = io.WriteString(w, "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"thinking\"},\"finish_reason\":null}]}\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			select {
			case <-r.Context().Done():
				select {
				case cancelled <- struct{}{}:
				default:
				}
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}))
	defer server.Close()

	provider := NewOpenAICompatibleProvider(config.OpenAICompatibleConfig{
		BaseURL:                              server.URL,
		APIKey:                               "test-key",
		StreamFirstContentTimeout:            time.Second,
		StreamFirstContentMaxReasoningChunks: 2,
	}, false)
	provider.httpClient = server.Client()

	_, errChan := provider.ChatCompletionStream(context.Background(), ChatRequest{
		Model:    "openai_compatible/test-model",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err := waitForStreamError(t, errChan); err == nil || !strings.Contains(err.Error(), "stream first content reasoning chunk limit exceeded (3 > 2)") {
		t.Fatalf("error = %v, want first content reasoning chunk limit error", err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("server did not observe request cancellation")
	}
}

func TestOpenAICompatibleProvider_ChatCompletionStreamFirstContentTimeoutContentDisarms(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		time.Sleep(10 * time.Millisecond)
		_, _ = io.WriteString(w, "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"start\"},\"finish_reason\":null}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(90 * time.Millisecond)
		_, _ = io.WriteString(w, "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer server.Close()

	provider := NewOpenAICompatibleProvider(config.OpenAICompatibleConfig{
		BaseURL:                   server.URL,
		APIKey:                    "test-key",
		StreamFirstContentTimeout: 50 * time.Millisecond,
	}, false)
	provider.httpClient = server.Client()

	chunkChan, errChan := provider.ChatCompletionStream(context.Background(), ChatRequest{
		Model:    "openai_compatible/test-model",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})

	var gotStart, gotDone bool
	var gotErr error
	timeout := time.After(time.Second)
	for chunkChan != nil || errChan != nil {
		select {
		case chunk, ok := <-chunkChan:
			if !ok {
				chunkChan = nil
				continue
			}
			for _, choice := range chunk.Choices {
				gotStart = gotStart || choice.Delta.Content == "start"
				gotDone = gotDone || choice.Delta.Content == "done"
			}
		case err, ok := <-errChan:
			if !ok {
				errChan = nil
				continue
			}
			gotErr = err
		case <-timeout:
			t.Fatal("timeout waiting for content-disarmed stream")
		}
	}
	if gotErr != nil {
		t.Fatalf("stream error = %v, want nil", gotErr)
	}
	if !gotStart || !gotDone {
		t.Fatalf("gotStart=%v gotDone=%v, want both", gotStart, gotDone)
	}
}

func TestOpenAICompatibleProvider_ChatCompletionStreamFirstContentTimeoutToolCallDisarms(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		time.Sleep(10 * time.Millisecond)
		_, _ = io.WriteString(w, "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"read\",\"arguments\":\"\"}}]},\"finish_reason\":null}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(90 * time.Millisecond)
		_, _ = io.WriteString(w, "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer server.Close()

	provider := NewOpenAICompatibleProvider(config.OpenAICompatibleConfig{
		BaseURL:                   server.URL,
		APIKey:                    "test-key",
		StreamFirstContentTimeout: 50 * time.Millisecond,
	}, false)
	provider.httpClient = server.Client()

	chunkChan, errChan := provider.ChatCompletionStream(context.Background(), ChatRequest{
		Model:    "openai_compatible/test-model",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})

	var gotToolCall, gotDone bool
	var gotErr error
	timeout := time.After(time.Second)
	for chunkChan != nil || errChan != nil {
		select {
		case chunk, ok := <-chunkChan:
			if !ok {
				chunkChan = nil
				continue
			}
			for _, choice := range chunk.Choices {
				gotToolCall = gotToolCall || len(choice.Delta.ToolCalls) > 0
				gotDone = gotDone || choice.Delta.Content == "done"
			}
		case err, ok := <-errChan:
			if !ok {
				errChan = nil
				continue
			}
			gotErr = err
		case <-timeout:
			t.Fatal("timeout waiting for tool-call-disarmed stream")
		}
	}
	if gotErr != nil {
		t.Fatalf("stream error = %v, want nil", gotErr)
	}
	if !gotToolCall || !gotDone {
		t.Fatalf("gotToolCall=%v gotDone=%v, want both", gotToolCall, gotDone)
	}
}

func TestOpenAICompatibleProvider_ChatCompletionStreamFirstContentTimeoutBeforeHeadersCancelsRequest(t *testing.T) {
	cancelled := make(chan struct{})
	var requests int32
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		atomic.AddInt32(&requests, 1)
		select {
		case <-r.Context().Done():
			close(cancelled)
			return nil, r.Context().Err()
		case <-time.After(time.Second):
			t.Fatal("request context was not cancelled")
			return nil, nil
		}
	})

	provider := NewOpenAICompatibleProvider(config.OpenAICompatibleConfig{
		BaseURL:                   "http://example.test",
		APIKey:                    "test-key",
		StreamFirstContentTimeout: 25 * time.Millisecond,
	}, false)
	provider.httpClient = &http.Client{Transport: transport}

	_, errChan := provider.ChatCompletionStream(context.Background(), ChatRequest{
		Model:    "openai_compatible/test-model",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err := waitForStreamError(t, errChan); err == nil || !strings.Contains(err.Error(), "stream first content timeout") {
		t.Fatalf("error = %v, want stream first content timeout", err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("server did not observe request cancellation")
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Fatalf("requests = %d, want one cancelled request", got)
	}
}

func TestOpenAICompatibleProvider_ChatCompletionStreamDefaultZeroIdleAllowsSlowStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		time.Sleep(50 * time.Millisecond)
		_, _ = io.WriteString(w, "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"slow\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer server.Close()

	provider := newTestOpenAICompatibleProvider(server.URL)
	provider.httpClient = server.Client()

	chunkChan, errChan := provider.ChatCompletionStream(context.Background(), ChatRequest{
		Model:    "openai_compatible/test-model",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	var gotContent bool
	var gotErr error
	timeout := time.After(time.Second)
	for chunkChan != nil || errChan != nil {
		select {
		case chunk, ok := <-chunkChan:
			if !ok {
				chunkChan = nil
				continue
			}
			if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content == "slow" {
				gotContent = true
			}
		case err, ok := <-errChan:
			if !ok {
				errChan = nil
				continue
			}
			gotErr = err
		case <-timeout:
			t.Fatal("timeout waiting for slow stream")
		}
	}
	if gotErr != nil {
		t.Fatalf("stream error = %v, want nil", gotErr)
	}
	if !gotContent {
		t.Fatal("expected slow content with default zero idle timeout")
	}
}

func waitForStreamError(t *testing.T, errChan <-chan error) error {
	t.Helper()
	select {
	case err, ok := <-errChan:
		if !ok {
			return nil
		}
		return err
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for stream error")
		return nil
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// TestOpenAICompatibleProvider_ReasoningDroppedWhenModelDoesNotSupportEffort
// reproduces a real Particle production failure: deepseek-v4.1-flash's
// supported_parameters list "tools" and "reasoning_content" but not
// "reasoning_effort", yet compatiblePayload used to forward the raw
// OpenRouter-style nested `reasoning` object (e.g. reasoning.max_tokens) on
// that path. Particle's strict OpenAI-compatible gateway rejects it with
// HTTP 400 "Unsupported nested reasoning field 'max_tokens'" (buckbot pr on
// openai_compatible/deepseek-v4.1-flash, 2026-09-23).
func TestOpenAICompatibleProvider_ReasoningDroppedWhenModelDoesNotSupportEffort(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-1",
			"model":"deepseek-v4.1-flash",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]
		}`)
	}))
	defer server.Close()

	provider := NewOpenAICompatibleProvider(config.OpenAICompatibleConfig{
		BaseURL: server.URL,
		APIKey:  "test-key",
		SupportedParameters: map[string][]string{
			"deepseek-v4.1-flash": {"tools", "reasoning_content"},
		},
	}, false)
	provider.httpClient = server.Client()

	_, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Model:     "openai_compatible/deepseek-v4.1-flash",
		Messages:  []Message{{Role: "user", Content: "hi"}},
		Reasoning: &ReasoningConfig{MaxTokens: 8192},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if _, ok := captured["reasoning"]; ok {
		t.Fatalf("request should drop nested reasoning when reasoning_effort is unsupported: %#v", captured["reasoning"])
	}
	if _, ok := captured["reasoning_effort"]; ok {
		t.Fatalf("request should not synthesize reasoning_effort when unsupported: %#v", captured["reasoning_effort"])
	}
}

// TestOpenAICompatibleProvider_PricingOverrideMarksZeroPriceAuthoritative
// covers a design-partner free tier (Particle): the live catalog reports no
// pricing at all for a model, so ModelInfo.Pricing is the zero value and
// PricingKnown stays false. Cost-bounded requests and --budget repair logic
// then refuse to admit the model because a zero-value price is normally
// indistinguishable from "the provider never told us." An explicit
// providers.openai_compatible.pricing override must mark that zero price as
// authoritative (PricingKnown=true) instead.
func TestOpenAICompatibleProvider_PricingOverrideMarksZeroPriceAuthoritative(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"deepseek-v4.1-flash","supported_parameters":["tools","reasoning_content"]}]}`)
	}))
	defer server.Close()

	provider := NewOpenAICompatibleProvider(config.OpenAICompatibleConfig{
		BaseURL: server.URL,
		APIKey:  "test-key",
		Pricing: map[string]config.ModelPricingOverride{
			"deepseek-v4.1-flash": {InputPerMillion: 0, OutputPerMillion: 0},
		},
	}, false)
	provider.httpClient = server.Client()

	info, err := provider.GetModelInfo("openai_compatible/deepseek-v4.1-flash")
	if err != nil {
		t.Fatalf("GetModelInfo() error = %v", err)
	}
	if !info.PricingKnown {
		t.Fatal("PricingKnown = false, want true for a configured pricing override")
	}
	if info.Pricing.Prompt != 0 || info.Pricing.Completion != 0 {
		t.Fatalf("Pricing = %+v, want zero", info.Pricing)
	}
}

// TestOpenAICompatibleProvider_PricingOverrideAppliesToStaticCatalog covers
// the fallback path when the live /models endpoint is unreachable and
// Buckley falls back to the statically configured model list.
func TestOpenAICompatibleProvider_PricingOverrideAppliesToStaticCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "catalog unavailable", http.StatusUnauthorized)
	}))
	defer server.Close()

	provider := NewOpenAICompatibleProvider(config.OpenAICompatibleConfig{
		BaseURL: server.URL,
		APIKey:  "test-key",
		Models:  []string{"deepseek-v4.1-flash"},
		Pricing: map[string]config.ModelPricingOverride{
			"deepseek-v4.1-flash": {InputPerMillion: 0, OutputPerMillion: 0},
		},
	}, false)
	provider.httpClient = server.Client()

	info, err := provider.GetModelInfo("openai_compatible/deepseek-v4.1-flash")
	if err != nil {
		t.Fatalf("GetModelInfo() error = %v", err)
	}
	if !info.PricingKnown {
		t.Fatal("PricingKnown = false, want true for a configured pricing override on the static catalog")
	}
}

// TestOpenAICompatibleProvider_UnmatchedModelKeepsPricingUnknown ensures the
// override only applies to models it names, leaving other models' pricing
// state untouched.
func TestOpenAICompatibleProvider_UnmatchedModelKeepsPricingUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"glm5.3flash"}]}`)
	}))
	defer server.Close()

	provider := NewOpenAICompatibleProvider(config.OpenAICompatibleConfig{
		BaseURL: server.URL,
		APIKey:  "test-key",
		Pricing: map[string]config.ModelPricingOverride{
			"deepseek-v4.1-flash": {InputPerMillion: 0, OutputPerMillion: 0},
		},
	}, false)
	provider.httpClient = server.Client()

	info, err := provider.GetModelInfo("openai_compatible/glm5.3flash")
	if err != nil {
		t.Fatalf("GetModelInfo() error = %v", err)
	}
	if info.PricingKnown {
		t.Fatal("PricingKnown = true, want false for a model with no configured override")
	}
}
