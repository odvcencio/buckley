package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/odvcencio/buckley/pkg/conversation"
)

func TestLoaderParsesStructuredSections(t *testing.T) {
	root := t.TempDir()
	agents := `## Project Summary
Buckley overview line.

## Development Rules
- Rule one
- Rule two

## Agent Guidelines
- Guideline alpha

## Sub-Agents
### Builder
- **Description:** Builds features
- **Model:** gpt-4
- **Tools:** [write_file, run_shell]
- **Max Cost:** $12.50
- **Instructions:** Keep tests green`

	path := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(path, []byte(agents), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	loader := NewLoader(root)
	ctx, err := loader.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !ctx.Loaded {
		t.Fatalf("expected context to be marked as loaded")
	}
	if got := strings.TrimSpace(ctx.Summary); got != "Buckley overview line." {
		t.Fatalf("summary mismatch: %q", got)
	}
	if len(ctx.Rules) != 2 || ctx.Rules[0] != "Rule one" || ctx.Rules[1] != "Rule two" {
		t.Fatalf("rules not parsed correctly: %#v", ctx.Rules)
	}
	if len(ctx.Guidelines) != 1 || ctx.Guidelines[0] != "Guideline alpha" {
		t.Fatalf("guidelines not parsed correctly: %#v", ctx.Guidelines)
	}

	builder := ctx.SubAgents["Builder"]
	if builder == nil {
		t.Fatalf("expected Builder sub-agent")
	}
	if builder.Model != "gpt-4" {
		t.Fatalf("builder model mismatch: %q", builder.Model)
	}
	if builder.Description != "Builds features" {
		t.Fatalf("builder description mismatch: %q", builder.Description)
	}
	if builder.MaxCost != 12.50 {
		t.Fatalf("builder max cost mismatch: %v", builder.MaxCost)
	}
	if len(builder.Tools) != 2 || builder.Tools[0] != "write_file" || builder.Tools[1] != "run_shell" {
		t.Fatalf("builder tools mismatch: %#v", builder.Tools)
	}
	if builder.Instructions != "Keep tests green" {
		t.Fatalf("builder instructions mismatch: %q", builder.Instructions)
	}
}

func TestInjectFallsBackToRawContent(t *testing.T) {
	root := t.TempDir()
	raw := "Unstructured AGENTS content"
	path := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	loader := NewLoader(root)
	ctx, err := loader.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	conv := conversation.New("session-1")
	loader.InjectIntoConversation(conv, ctx)

	if len(conv.Messages) != 1 {
		t.Fatalf("expected a single injected message, got %d", len(conv.Messages))
	}
	msg := conv.Messages[0]
	if msg.Role != "system" {
		t.Fatalf("expected system role, got %s", msg.Role)
	}
	content := conversation.GetContentAsString(msg.Content)
	if !strings.Contains(content, "## From AGENTS.md") || !strings.Contains(content, raw) {
		t.Fatalf("fallback content missing raw AGENTS data: %q", content)
	}
}

func TestInjectUsesStructuredContentWhenAvailable(t *testing.T) {
	root := t.TempDir()
	agents := `## Project Summary
Buckley overview line.

## Development Rules
- Rule alpha
- Rule beta

## Agent Guidelines
- Guideline delta`

	path := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(path, []byte(agents), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	loader := NewLoader(root)
	ctx, err := loader.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	conv := conversation.New("session-structured")
	loader.InjectIntoConversation(conv, ctx)

	if len(conv.Messages) != 1 {
		t.Fatalf("expected one injected system message, got %d", len(conv.Messages))
	}
	content := conversation.GetContentAsString(conv.Messages[0].Content)
	if strings.Contains(content, "## From AGENTS.md") {
		t.Fatalf("should not use raw fallback when structured content exists: %q", content)
	}
	for _, want := range []string{"Project Context", "Summary:", "Buckley overview line.", "Rule alpha", "Guideline delta"} {
		if !strings.Contains(content, want) {
			t.Fatalf("context message missing %q: %q", want, content)
		}
	}
}

func TestSummaryTrimsWhitespace(t *testing.T) {
	root := t.TempDir()
	agents := `
## Project Summary
 Buckley overview line.
 Second line.
`
	path := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(path, []byte(agents), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	loader := NewLoader(root)
	ctx, err := loader.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := "Buckley overview line. Second line."
	if strings.TrimSpace(ctx.Summary) != want {
		t.Fatalf("unexpected summary: %q (want %q)", ctx.Summary, want)
	}
}
