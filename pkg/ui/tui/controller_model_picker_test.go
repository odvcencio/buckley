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
	got := modelProcessStatus("z-ai/glm-5.2", 0, 50, 12, nil)
	if got != "Thinking with z-ai/glm-5.2 - round 1/50, 12 tools" {
		t.Fatalf("modelProcessStatus() = %q", got)
	}

	got = modelProcessStatus("very/long-model-name-that-will-not-fit-in-the-status-bar", 1, 50, 0, nil)
	if !strings.HasPrefix(got, "Thinking after tools with very/long-model-name") {
		t.Fatalf("unexpected model process status: %q", got)
	}
	if !strings.Contains(got, "round 2/50") {
		t.Fatalf("expected round indicator in status: %q", got)
	}
}

func TestMaxToolIterationsError(t *testing.T) {
	err := maxToolIterationsError(50)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "50 model/tool rounds") {
		t.Fatalf("expected model/tool round count, got %q", err.Error())
	}
	if strings.Contains(err.Error(), "max tool calling iterations") {
		t.Fatalf("error should be user-facing, got %q", err.Error())
	}
}

func TestModelFinishReasonNotice(t *testing.T) {
	if got := modelFinishReasonNotice("stop"); got != "" {
		t.Fatalf("stop should not produce a notice, got %q", got)
	}

	got := modelFinishReasonNotice("length")
	if !strings.Contains(got, "output token limit") {
		t.Fatalf("length notice should mention output token limit, got %q", got)
	}
	if status := readyStatusForFinishReason("length"); status != "Ready - output token limit reached" {
		t.Fatalf("ready status for length = %q", status)
	}

	got = modelFinishReasonNotice("content_filter")
	if !strings.Contains(got, "content_filter") {
		t.Fatalf("content_filter notice should include reason, got %q", got)
	}
}
