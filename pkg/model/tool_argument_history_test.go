package model

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"m31labs.dev/buckley/pkg/config"
)

func invalidArgumentHistory(raw string) []Message {
	return []Message{
		{Role: "user", Content: "Read the requested source."},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call-bad", Type: "function", Function: FunctionCall{Name: "read_file", Arguments: raw}}}},
		{Role: "tool", ToolCallID: "call-bad", Name: "read_file", Content: "Error: invalid tool arguments; no tool executed."},
	}
}

func TestToolArgumentHistoryWrapsInvalidObjectsWithoutChangingSource(t *testing.T) {
	for _, provider := range []string{"openai_compatible", "openai", "anthropic", "google", "openrouter", "ollama", "litellm", "mistral"} {
		for _, raw := range []string{`{"path":`, `{"path":"x",}`, `{} trailing`, `{} {}`, `[]`, `null`, `42`, `true`, `"quoted"`, "```json\n{}\n```", "\u00a0{}", "{}\u00a0"} {
			t.Run(provider+"/"+raw, func(t *testing.T) {
				messages := invalidArgumentHistory(raw)
				got := normalizeChatMessages(messages, provider, "modern-model")
				if len(got) != 3 || len(got[1].ToolCalls) != 1 {
					t.Fatalf("history structure changed: %+v", got)
				}
				var wrapper map[string]string
				if err := json.Unmarshal([]byte(got[1].ToolCalls[0].Function.Arguments), &wrapper); err != nil || len(wrapper) != 1 || wrapper["_buckley_invalid_tool_arguments"] != raw {
					t.Fatalf("missing lossless historical placeholder: args=%q err=%v", got[1].ToolCalls[0].Function.Arguments, err)
				}
				if messages[1].ToolCalls[0].Function.Arguments != raw || messages[2].Content != got[2].Content {
					t.Fatal("raw transcript or tool failure was changed")
				}
				if got[2].ToolCallID != got[1].ToolCalls[0].ID {
					t.Fatal("tool/result pairing lost")
				}
				if twice := normalizeChatMessages(got, provider, "modern-model"); !reflect.DeepEqual(got, twice) {
					t.Fatalf("history normalization is not idempotent: %+v", twice)
				}
			})
		}
	}
}

func TestToolArgumentHistoryPreservesValidObjectBytes(t *testing.T) {
	for _, raw := range []string{`{}`, " \n{\"path\": \"a\\nb\", \"n\": 123456789012345678901234567890, \"huge\": 1e1000}\t", `{"nested":{"array":[null,true,1]}}`, `{"_buckley_invalid_tool_arguments":"already wrapped"}`} {
		messages := invalidArgumentHistory(raw)
		got := normalizeChatMessages(messages, "openai_compatible", "modern-model")
		if got[1].ToolCalls[0].Function.Arguments != raw || messages[1].ToolCalls[0].Function.Arguments != raw {
			t.Fatalf("valid object bytes were rewritten: %q", raw)
		}
	}
}

func TestOpenAICompatibleHistoryContinuesAfterMalformedToolCall(t *testing.T) {
	for _, reasoningWire := range []bool{false, true} {
		t.Run(fmt.Sprint(reasoningWire), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				var req struct {
					Messages []Message `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				var args map[string]json.RawMessage
				if len(req.Messages) != 3 || len(req.Messages[1].ToolCalls) != 1 {
					t.Errorf("unexpected history: %+v", req.Messages)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if err := json.Unmarshal([]byte(req.Messages[1].ToolCalls[0].Function.Arguments), &args); err != nil || args == nil {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, `{"error":{"message":"GLM tool call arguments must be valid JSON objects","type":"invalid_request_error","code":"invalid_tokenization_tool_arguments"}}`)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"recovered","choices":[{"message":{"role":"assistant","content":"The call failed without execution; I can correct it."},"finish_reason":"stop"}]}`)
			}))
			defer server.Close()
			parameters := []string{"tools"}
			if reasoningWire {
				parameters = append(parameters, "reasoning_content")
			}
			provider := NewOpenAICompatibleProvider(config.OpenAICompatibleConfig{
				BaseURL: server.URL, APIKey: "test-key", Models: []string{"modern-model"},
				SupportedParameters: map[string][]string{"modern-model": parameters},
			}, false)
			provider.httpClient = server.Client()
			req := applyProviderTransformsWithOptions(ChatRequest{Model: "modern-model", Messages: invalidArgumentHistory(`{"path":`), ToolChoice: "none"}, "openai_compatible", providerTransformOptions{PreserveReasoningMessages: reasoningWire})
			response, err := provider.ChatCompletion(t.Context(), req)
			if err != nil || len(response.Choices) != 1 || requests.Load() != 1 {
				t.Fatalf("continuation failed or retried: response=%+v requests=%d err=%v", response, requests.Load(), err)
			}
		})
	}
}
