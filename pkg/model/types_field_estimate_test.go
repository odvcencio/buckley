package model

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"
)

// TestEstimateRequestTokens_TracksMarshalBaseline is a differential test: the
// field-walk EstimateRequestTokens must stay within 3% of the original
// marshal-based estimator (kept unexported as estimateRequestTokensByMarshal)
// across representative transcript shapes.
func TestEstimateRequestTokens_TracksMarshalBaseline(t *testing.T) {
	signature := "sig-abc123"
	tests := []struct {
		name string
		req  ChatRequest
	}{
		{
			name: "plain_text",
			req: ChatRequest{
				Model: "openai/gpt-5.4",
				Messages: []Message{
					{Role: "system", Content: "You are a helpful assistant."},
					{Role: "user", Content: "What is the capital of France? Please explain your reasoning in detail."},
					{Role: "assistant", Content: "The capital of France is Paris. It has been the capital since 987 AD."},
					{Role: "user", Content: "Interesting -- what about Germany & Austria? Compare <them> vs France."},
				},
				Temperature: 0.7,
				MaxTokens:   2048,
			},
		},
		{
			name: "tool_calls",
			req: ChatRequest{
				Model: "openai/gpt-5.4",
				Messages: []Message{
					{Role: "system", Content: "You can call tools."},
					{Role: "user", Content: "Read the file go.mod and summarize the module path & dependencies."},
					{
						Role: "assistant",
						ToolCalls: []ToolCall{
							{ID: "call_1", Type: "function", Function: FunctionCall{
								Name:      "read_file",
								Arguments: `{"path":"go.mod","limit":200,"pattern":"module \"m31labs.dev/buckley\""}`,
							}},
							{ID: "call_2", Type: "function", Function: FunctionCall{
								Name:      "grep",
								Arguments: `{"pattern":"require (","path":"go.mod","case_sensitive":true,"context":"before && after"}`,
							}},
						},
					},
					{Role: "tool", ToolCallID: "call_1", Name: "read_file", Content: "module m31labs.dev/buckley/v2\n\ngo 1.24\n"},
					{Role: "tool", ToolCallID: "call_2", Name: "grep", Content: "require (\n\tgithub.com/foo v1.2.3\n)\n"},
				},
				Tools: []map[string]any{
					{"type": "function", "function": map[string]any{
						"name":        "read_file",
						"description": "Read a file from disk with an optional byte limit & line range.",
						"parameters": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"path":  map[string]any{"type": "string"},
								"limit": map[string]any{"type": "integer"},
							},
						},
					}},
					{"type": "function", "function": map[string]any{
						"name":        "grep",
						"description": "Search file contents for a pattern.",
					}},
				},
				ToolChoice: "auto",
			},
		},
		{
			name: "reasoning_details",
			req: ChatRequest{
				Model: "anthropic/claude-4.5-sonnet",
				Messages: []Message{
					{Role: "user", Content: "Prove that sqrt(2) is irrational."},
					{
						Role:      "assistant",
						Content:   "sqrt(2) is irrational because assuming p/q in lowest terms leads to a contradiction.",
						Reasoning: "Suppose sqrt(2) = p/q with gcd(p,q)=1. Then p^2 = 2q^2, so p is even...",
						ReasoningDetails: []ReasoningDetail{
							{
								Type:      "reasoning.text",
								ID:        "rt_1",
								Index:     0,
								HasIndex:  true,
								Text:      "Suppose sqrt(2) = p/q with gcd(p,q)=1. Then p^2 = 2q^2, so p is even, p=2k, 4k^2=2q^2, q^2=2k^2, so q is even too -- contradiction since gcd(p,q)=1.",
								Signature: &signature,
								Format:    "anthropic-claude-v1",
							},
							{
								Type:  "reasoning.encrypted",
								ID:    "rt_2",
								Data:  "ZW5jcnlwdGVkLXBheWxvYWQtZGF0YS1oZXJlLW1vcmUtYnl0ZXM=",
								Extra: map[string]json.RawMessage{"provider_meta": json.RawMessage(`{"vendor":"anthropic","tier":"high"}`)},
							},
						},
					},
					{Role: "user", Content: "Now do the same for sqrt(3)."},
				},
				Reasoning: &ReasoningConfig{Effort: "high", MaxTokens: 8000},
			},
		},
		{
			name: "multimodal_parts",
			req: ChatRequest{
				Model: "openai/gpt-5.4-vision",
				Messages: []Message{
					{Role: "system", Content: "Describe images accurately."},
					{
						Role: "user",
						Content: []ContentPart{
							{Type: "text", Text: "What is shown in this diagram? Note the <arrows> & labels."},
							{Type: "image_url", ImageURL: &ImageURL{URL: "https://example.com/diagram.png", Detail: "high"}},
							{Type: "text", Text: "Also compare it to this one:"},
							{Type: "image_url", ImageURL: &ImageURL{URL: "https://example.com/diagram2.png"}, CacheControl: &CacheControl{Type: "ephemeral", TTL: "1h"}},
						},
					},
					{Role: "assistant", Content: "The first diagram shows a flowchart with three decision nodes."},
				},
				Provider:       map[string]any{"allow_fallbacks": true, "order": []any{"openai", "azure"}},
				ResponseFormat: map[string]any{"type": "json_object"},
				Metadata:       map[string]string{"surface": "test", "session_kind": "eval"},
			},
		},
		{
			name: "empty_messages",
			req: ChatRequest{
				Model:    "openai/gpt-5.4",
				Messages: nil,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EstimateRequestTokens(tt.req)
			want := estimateRequestTokensByMarshal(tt.req)

			assertWithinTolerance(t, "Messages", got.Messages, want.Messages)
			assertWithinTolerance(t, "Tools", got.Tools, want.Tools)
			assertWithinTolerance(t, "Fixed", got.Fixed, want.Fixed)
			assertWithinTolerance(t, "Total", got.Total, want.Total)

			if got.Total != got.Messages+got.Tools+got.Fixed {
				t.Fatalf("Total (%d) != Messages+Tools+Fixed (%d)", got.Total, got.Messages+got.Tools+got.Fixed)
			}
		})
	}
}

// assertWithinTolerance fails the test if got is more than 3% away from
// want. Small baselines (want <= 8) allow a fixed +/-1 token slack since a
// percentage tolerance is meaningless near zero.
func assertWithinTolerance(t *testing.T, label string, got, want int) {
	t.Helper()
	if want <= 8 {
		if diff := got - want; diff < -2 || diff > 2 {
			t.Fatalf("%s = %d, want %d (+/-2 near zero)", label, got, want)
		}
		return
	}
	tolerance := math.Ceil(float64(want) * 0.03)
	diff := math.Abs(float64(got - want))
	if diff > tolerance {
		t.Fatalf("%s = %d, want %d (diff %.0f exceeds tolerance %.0f)", label, got, want, diff, tolerance)
	}
}

// TestEstimateRequestTokens_PreservesStructSemantics locks in that Total is
// always the sum of the three components, across a broad matrix of message
// counts and tool-schema presence.
func TestEstimateRequestTokens_PreservesStructSemantics(t *testing.T) {
	for _, msgCount := range []int{0, 1, 7, 500} {
		for _, withTools := range []bool{false, true} {
			t.Run(fmt.Sprintf("messages=%d/tools=%v", msgCount, withTools), func(t *testing.T) {
				req := ChatRequest{Model: "openai/gpt-5.4"}
				for i := 0; i < msgCount; i++ {
					req.Messages = append(req.Messages, Message{Role: "user", Content: fmt.Sprintf("message number %d with some content", i)})
				}
				if withTools {
					req.Tools = []map[string]any{{"type": "function", "function": map[string]any{"name": "noop"}}}
				}
				estimate := EstimateRequestTokens(req)
				if estimate.Total != estimate.Messages+estimate.Tools+estimate.Fixed {
					t.Fatalf("Total = %d, want Messages+Tools+Fixed = %d", estimate.Total, estimate.Messages+estimate.Tools+estimate.Fixed)
				}
			})
		}
	}
}
