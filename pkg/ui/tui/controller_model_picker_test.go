package tui

import (
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
)

func TestModelGroupKey(t *testing.T) {
	tests := []struct {
		modelID string
		want    string
	}{
		{modelID: "openai/gpt-4o", want: "openai"},
		{modelID: "anthropic/claude-3.5", want: "anthropic"},
		{modelID: "gpt-4o", want: "other"},
	}

	for _, tt := range tests {
		if got := modelGroupKey(tt.modelID, nil); got != tt.want {
			t.Fatalf("modelGroupKey(%q) = %q, want %q", tt.modelID, got, tt.want)
		}
	}
}

func TestModelLabel(t *testing.T) {
	tests := []struct {
		modelID string
		group   string
		want    string
	}{
		{modelID: "openai/gpt-4o", group: "openai", want: "gpt-4o"},
		{modelID: "openrouter/auto", group: "openrouter", want: "auto"},
		{modelID: "gpt-4o", group: "other", want: "gpt-4o"},
	}

	for _, tt := range tests {
		if got := modelLabel(tt.modelID, tt.group); got != tt.want {
			t.Fatalf("modelLabel(%q, %q) = %q, want %q", tt.modelID, tt.group, got, tt.want)
		}
	}
}

func TestModelRoleTags(t *testing.T) {
	tags := modelRoleTags("openai/gpt-4o", "openai/gpt-4o", "openai/gpt-4o-mini", "openai/gpt-4o")
	if len(tags) != 2 || tags[0] != "exec" || tags[1] != "review" {
		t.Fatalf("modelRoleTags unexpected: %v", tags)
	}
}

func TestPreferredModelIDs(t *testing.T) {
	catalog := map[string]model.ModelInfo{
		"moonshotai/kimi-k2.7-code": {},
		"openai/gpt-4o":             {},
		"qwen/qwen3.7-max":          {},
		"z-ai/glm-5.2":              {},
	}
	ids := preferredModelIDs("openai/gpt-4o", "", "", catalog)
	if len(ids) != 4 {
		t.Fatalf("expected 4 preferred models, got %d", len(ids))
	}
	want := []string{"openai/gpt-4o", "z-ai/glm-5.2", "moonshotai/kimi-k2.7-code", "qwen/qwen3.7-max"}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("unexpected preferred model order: %v", ids)
		}
	}
}

func TestModelProcessStatus(t *testing.T) {
	got := modelProcessStatus("z-ai/glm-5.2", 0, 12, nil)
	if got != "Thinking with z-ai/glm-5.2 - 12 tools" {
		t.Fatalf("modelProcessStatus() = %q", got)
	}

	got = modelProcessStatus("very/long-model-name-that-will-not-fit-in-the-status-bar", 1, 0, nil)
	if !strings.HasPrefix(got, "Thinking after tools with very/long-model-name") {
		t.Fatalf("unexpected model process status: %q", got)
	}
}
