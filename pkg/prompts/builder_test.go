package prompts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/types"
)

func TestDiscoverInstructions_Dedup(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "subdir")
	os.MkdirAll(sub, 0o755)

	content := []byte("# Instructions\nDo the thing.\n")
	os.WriteFile(filepath.Join(root, "CLAUDE.md"), content, 0o644)
	os.WriteFile(filepath.Join(sub, "CLAUDE.md"), content, 0o644)

	files := DiscoverInstructions(root, sub)
	if len(files) != 1 {
		t.Errorf("expected 1 unique file (deduped), got %d", len(files))
	}
}

func TestDiscoverInstructions_MultipleFiles(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("global"), 0o644)
	os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("agents"), 0o644)

	files := DiscoverInstructions(root, root)
	if len(files) != 2 {
		t.Errorf("expected 2 files, got %d", len(files))
	}
}

func TestDiscoverInstructions_WalksUp(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b")
	os.MkdirAll(sub, 0o755)

	os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("root instructions"), 0o644)
	os.WriteFile(filepath.Join(sub, "CLAUDE.md"), []byte("sub instructions"), 0o644)

	files := DiscoverInstructions(root, sub)
	if len(files) != 2 {
		t.Errorf("expected 2 files (different content), got %d", len(files))
	}
}

func TestPromptBuilder_BudgetEnforcement(t *testing.T) {
	builder := NewPromptBuilder(nil)
	bigContent := strings.Repeat("x", 15000)
	builder.AddSection("instructions", bigContent, true)

	sections := builder.Build(PromptContext{InstructionChars: 15000})
	total := 0
	for _, s := range sections {
		total += len(s)
	}
	if total > MaxTotalInstructionChars+20 { // +20 for "[truncated]" + newline
		t.Errorf("total chars = %d, exceeds max %d", total, MaxTotalInstructionChars)
	}
}

func TestPromptBuilder_WithEvaluator(t *testing.T) {
	eval := &mockAssemblyEvaluator{omitInstructions: true}
	builder := NewPromptBuilder(eval)
	builder.AddSection("system", "system prompt", false)
	builder.AddSection("instructions", "user instructions", true)
	builder.AddSection("working_directory", "Working Directory: /tmp/project", true)

	sections := builder.Build(PromptContext{})
	for _, s := range sections {
		if strings.Contains(s, "user instructions") {
			t.Error("expected instructions section to be omitted")
		}
	}
	if len(sections) != 2 {
		t.Errorf("expected 2 sections (system + working directory), got %d", len(sections))
	}
}

type mockAssemblyEvaluator struct {
	omitInstructions bool
}

func (m *mockAssemblyEvaluator) EvalStrategy(domain, name string, facts map[string]any) (types.StrategyResult, error) {
	return types.StrategyResult{Params: map[string]any{
		"omit_instructions":   m.omitInstructions,
		"include_gts_context": false,
		"max_chars":           float64(MaxTotalInstructionChars),
	}}, nil
}
