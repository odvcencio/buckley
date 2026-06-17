package tui

import (
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestFormatToolResultForModel_UsesAbridgedDisplayData(t *testing.T) {
	result := &builtin.Result{
		Success: true,
		Data: map[string]any{
			"content": strings.Repeat("full-output ", 10_000),
		},
		DisplayData: map[string]any{
			"summary": "read large file preview",
			"content": "first 100 lines",
		},
		ShouldAbridge: true,
	}

	got := formatToolResultForModel(result, nil)
	if strings.Contains(got, "full-output") {
		t.Fatalf("model-facing tool result included full payload")
	}
	if !strings.Contains(got, "read large file preview") {
		t.Fatalf("model-facing tool result omitted display summary: %s", got)
	}
}

func TestTruncateModelToolMessages_TruncatesPersistedToolOutput(t *testing.T) {
	huge := strings.Repeat("0123456789", 10_000)
	messages := []model.Message{
		{Role: "system", Content: strings.Repeat("system", 10_000)},
		{Role: "tool", Name: "find_files", ToolCallID: "call_1", Content: huge},
	}

	got := truncateModelToolMessages(messages, 1024)
	content, ok := got[1].Content.(string)
	if !ok {
		t.Fatalf("tool content type = %T, want string", got[1].Content)
	}
	if len(content) > 1024 {
		t.Fatalf("tool content length = %d, want <= 1024", len(content))
	}
	if !strings.Contains(content, "tool output truncated for chat context") {
		t.Fatalf("truncation marker missing: %s", content)
	}
	if got[0].Content != messages[0].Content {
		t.Fatalf("non-tool message was modified")
	}
}

func TestTruncateModelToolOutput_PreservesHeadAndTail(t *testing.T) {
	input := "HEAD-" + strings.Repeat("middle", 1000) + "-TAIL"

	got := truncateModelToolOutput(input, 512)
	if len(got) > 512 {
		t.Fatalf("truncated length = %d, want <= 512", len(got))
	}
	if !strings.HasPrefix(got, "HEAD-") {
		t.Fatalf("head was not preserved: %s", got[:min(len(got), 32)])
	}
	if !strings.HasSuffix(got, "-TAIL") {
		t.Fatalf("tail was not preserved: %s", got[max(0, len(got)-32):])
	}
}
