package tui

import (
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
		"moonshotai/kimi-k3":        {},
		"openai/gpt-4o":             {},
		"qwen/qwen3.7-max":          {},
		"z-ai/glm-5.2":              {},
	}
	ids := preferredModelIDs("openai/gpt-4o", "", "", catalog)
	if len(ids) != 5 {
		t.Fatalf("expected 5 preferred models, got %d", len(ids))
	}
	want := []string{"openai/gpt-4o", "moonshotai/kimi-k3", "z-ai/glm-5.2", "moonshotai/kimi-k2.7-code", "qwen/qwen3.7-max"}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("unexpected preferred model order: %v", ids)
		}
	}
}

func TestBuildModelPickerItems_PinsConfiguredAndHotModels(t *testing.T) {
	catalog := []model.ModelInfo{
		{ID: "moonshotai/kimi-k3"},
		{ID: "z-ai/glm-5.2"},
		{ID: "openai/gpt-4o"},
		{ID: "anthropic/claude-3.5"},
		{ID: "qwen/qwen3.7-max"},
		{ID: "moonshotai/kimi-k2.7-code"},
	}

	items, index := buildModelPickerItems(
		catalog,
		nil,
		"openai/gpt-4o",
		"z-ai/glm-5.2",
		"moonshotai/kimi-k2.7-code",
		map[string]struct{}{"qwen/qwen3.7-max": {}},
	)

	if len(index) != len(catalog) {
		t.Fatalf("index size = %d, want %d", len(index), len(catalog))
	}
	wantIDs := []string{
		"openai/gpt-4o",
		"z-ai/glm-5.2",
		"moonshotai/kimi-k2.7-code",
		"moonshotai/kimi-k3",
		"qwen/qwen3.7-max",
		"anthropic/claude-3.5",
	}
	if len(items) != len(wantIDs) {
		t.Fatalf("items = %d, want %d: %+v", len(items), len(wantIDs), items)
	}
	for i, want := range wantIDs {
		if items[i].ID != want {
			t.Fatalf("item %d ID = %q, want %q; items=%+v", i, items[i].ID, want, items)
		}
	}
	for i := 0; i < 5; i++ {
		if items[i].Category != "Pinned" {
			t.Fatalf("item %d category = %q, want Pinned", i, items[i].Category)
		}
	}
	if items[4].Shortcut != "curated" {
		t.Fatalf("qwen shortcut = %q, want curated", items[4].Shortcut)
	}
	if items[5].Label != "  claude-3.5" {
		t.Fatalf("anthropic label = %q, want trimmed provider label", items[5].Label)
	}
}

func TestBuildModelPickerItems_SortsGroupsAndModels(t *testing.T) {
	catalog := []model.ModelInfo{
		{ID: "zeta/model-b"},
		{ID: "alpha/model-c"},
		{ID: "alpha/model-a"},
	}

	items, _ := buildModelPickerItems(catalog, nil, "", "", "", nil)
	want := []struct {
		category string
		id       string
		label    string
	}{
		{category: "alpha", id: "alpha/model-a", label: "  model-a"},
		{category: "alpha", id: "alpha/model-c", label: "  model-c"},
		{category: "zeta", id: "zeta/model-b", label: "  model-b"},
	}

	if len(items) != len(want) {
		t.Fatalf("items = %d, want %d", len(items), len(want))
	}
	for i := range want {
		if items[i].Category != want[i].category || items[i].ID != want[i].id || items[i].Label != want[i].label {
			t.Fatalf("item %d = %+v, want %+v", i, items[i], want[i])
		}
	}
}
