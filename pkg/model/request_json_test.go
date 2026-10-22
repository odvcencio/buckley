package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"testing"
)

func TestMarshalChatRequest_MatchesJSON(t *testing.T) {
	enabled, seed, signature := true, 7, "signed evidence"
	for _, tc := range []struct {
		name string
		req  ChatRequest
	}{
		{name: "nil_messages"},
		{name: "empty_messages", req: ChatRequest{Messages: []Message{}}},
		{name: "content_shapes", req: ChatRequest{Messages: []Message{
			{Role: "user", Content: "<source> & \"quotes\"\n\u2028\u2029\xff"},
			{Role: "user", Content: ""},
			{Role: "assistant"},
			{Role: "user", Content: []ContentPart{}},
			{Role: "tool", Content: []any{map[string]any{"type": "text", "text": "observed"}}},
			{Role: "tool", Content: json.RawMessage(`{"value":1}`)},
		}}},
		{name: "all_controls", req: ChatRequest{
			Model: "test/model", Models: []string{"fallback/model"},
			Messages: []Message{
				{Role: "system", Content: []ContentPart{{Type: "text", Text: "Evidence", CacheControl: &CacheControl{Type: "ephemeral", TTL: "1h"}}}},
				{Role: "user", Content: []ContentPart{{Type: "image_url", ImageURL: &ImageURL{URL: "data:image/png;base64,fixture", Detail: "low"}}}},
				{Role: "assistant", Reasoning: "thought", ReasoningContent: true, ReasoningDetails: []ReasoningDetail{{Type: "reasoning.text", Index: 0, HasIndex: true, Text: "thought", Signature: &signature, Extra: map[string]json.RawMessage{"provider_field": json.RawMessage(`{"preserved":true}`)}}}, ToolCalls: []ToolCall{{ID: "call-1", Type: "function", Function: FunctionCall{Name: "read_file", Arguments: `{"path":"source.go"}`}}}},
				{Role: "tool", Name: "read_file", ToolCallID: "call-1", Content: "observed source"},
			},
			Temperature: 0.5, MaxTokens: 1024, MaxCompletionTokens: 2048, Stream: true,
			StreamOptions: &StreamOptions{IncludeUsage: true}, Tools: []map[string]any{{"type": "function", "function": map[string]any{"name": "read_file"}}},
			ToolChoice: "auto", ParallelToolCalls: &enabled, Reasoning: &ReasoningConfig{Effort: "low", Enabled: &enabled}, IncludeReasoning: &enabled,
			Transforms: []string{"middle-out"}, Provider: map[string]any{"only": []string{"example"}}, ResponseFormat: map[string]any{"type": "json_object"},
			Seed: &seed, ServiceTier: "default", SessionID: "session", Metadata: map[string]string{"task": "inspect"}, Trace: map[string]string{"id": "trace"},
			CacheControl: &CacheControl{Type: "ephemeral"}, PromptCacheKey: "cache-key", PromptCacheRetention: "24h", Usage: &UsageRequestOptions{Include: true},
			PromptCache: &PromptCache{Enabled: true}, Route: ModelRoute{RequestedModel: "internal-route"},
			ToolsCatalogConfirmedUnavailable: true, RetryMode: RequestRetrySingleAttempt, ReviewSnapshot: &ReviewSnapshot{},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want, err := json.Marshal(tc.req)
			if err != nil {
				t.Fatal(err)
			}
			got, err := MarshalChatRequest(tc.req)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("request JSON differs:\n got %s\nwant %s", got, want)
			}
			after, err := json.Marshal(tc.req)
			if err != nil || !bytes.Equal(after, want) {
				t.Fatalf("encoding mutated request: %v", err)
			}
		})
	}
}

func TestMarshalChatRequest_ContentErrors(t *testing.T) {
	for _, content := range []any{make(chan int), math.NaN(), json.RawMessage(`{`)} {
		req := ChatRequest{Messages: []Message{{Role: "user", Content: content}}}
		if _, err := json.Marshal(req); err == nil {
			t.Fatalf("fixture %T must fail ordinary JSON encoding", content)
		}
		if _, err := MarshalChatRequest(req); err == nil {
			t.Fatalf("encoding accepted invalid content %T", content)
		}
	}
	content := &retryBodyContent{err: errors.New("content encoding failed")}
	_, err := MarshalChatRequest(ChatRequest{Messages: []Message{{Role: "user", Content: content}}})
	if !errors.Is(err, content.err) || content.calls != 1 {
		t.Fatalf("content error changed: err=%v encodings=%d", err, content.calls)
	}
}
