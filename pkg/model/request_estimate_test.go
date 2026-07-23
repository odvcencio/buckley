package model

import "testing"

func TestEstimateRequestTokens_IncludesToolSchemas(t *testing.T) {
	without := EstimateRequestTokens(ChatRequest{Model: "test", Messages: []Message{{Role: "user", Content: "hello"}}})
	with := EstimateRequestTokens(ChatRequest{
		Model:    "test",
		Messages: []Message{{Role: "user", Content: "hello"}},
		Tools: []map[string]any{{"type": "function", "function": map[string]any{
			"name": "large", "description": "a deliberately verbose tool definition",
		}}},
	})
	if with.Tools <= without.Tools || with.Total <= without.Total {
		t.Fatalf("schema was not counted: without=%+v with=%+v", without, with)
	}
}
