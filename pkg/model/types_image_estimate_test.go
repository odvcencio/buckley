package model

import (
	"strings"
	"testing"
)

// Review of #471, finding 4: the base64 text of an inline image is transport
// bytes, not input tokens. A 3 MiB screenshot looked like about a million.
func TestEstimateRequestTokens_InlineImageIsNotChargedByBase64Length(t *testing.T) {
	data := "data:image/png;base64," + strings.Repeat("A", 3<<20)
	req := ChatRequest{Messages: []Message{{Role: "user", Content: []ContentPart{
		{Type: "text", Text: "review this"},
		{Type: "image_url", ImageURL: &ImageURL{URL: data, Detail: "high"}},
	}}}}
	got := EstimateRequestTokens(req).Total
	if got < estimatedImageTokens || got > estimatedImageTokens+200 {
		t.Fatalf("estimate = %d tokens, want about %d for one image", got, estimatedImageTokens)
	}

	// A remote URL is short text and keeps its text cost.
	remote := ChatRequest{Messages: []Message{{Role: "user", Content: []ContentPart{{Type: "image_url", ImageURL: &ImageURL{URL: "https://example.test/a.png"}}}}}}
	if got := EstimateRequestTokens(remote).Total; got > 100 {
		t.Fatalf("remote image estimate = %d", got)
	}
}
