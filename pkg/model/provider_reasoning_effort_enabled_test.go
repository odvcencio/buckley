package model

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"m31labs.dev/buckley/pkg/config"
)

func TestOpenAICompatibleProvider_ReasoningEffortEnabled(t *testing.T) {
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

	enabled := true
	_, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Model:     "openai_compatible/glm-5.3-flash",
		Messages:  []Message{{Role: "user", Content: "hi"}},
		Reasoning: &ReasoningConfig{Enabled: &enabled, Effort: "high"},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if got := captured["reasoning_effort"]; got != "high" {
		t.Fatalf("reasoning_effort = %v, want high", got)
	}
	if _, ok := captured["reasoning"]; ok {
		t.Fatalf("request should not include nested reasoning when reasoning_effort is supported: %#v", captured["reasoning"])
	}
}
