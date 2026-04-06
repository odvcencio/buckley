package prompts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildRuntimeSystemPrompt_IncludesRuntimeContext(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "subdir")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Use focused edits."), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	prompt := BuildRuntimeSystemPrompt(RuntimePromptInput{
		BasePrompt:        "Base prompt",
		ProjectContext:    "Project context block",
		WorkDir:           workDir,
		RootDir:           root,
		SkillsDescription: "Skills:\n- Example skill",
		TaskType:          "coding",
		ModelTier:         "premium",
	})

	for _, want := range []string{
		"Base prompt",
		"Repository Instructions:",
		"Use focused edits.",
		"Project Context:\nProject context block",
		"Working Directory: " + workDir,
		"Skills:\n- Example skill",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected prompt to contain %q\nfull prompt:\n%s", want, prompt)
		}
	}
}

func TestBuildRuntimeSystemPrompt_DedupesProjectContextFromInstructionFile(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "nested")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	const shared = "Same context content"
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte(shared), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	prompt := BuildRuntimeSystemPrompt(RuntimePromptInput{
		BasePrompt:     "Base prompt",
		ProjectContext: shared,
		WorkDir:        workDir,
		RootDir:        root,
	})

	if strings.Count(prompt, shared) != 1 {
		t.Fatalf("expected shared content to appear once, got %d occurrences\nfull prompt:\n%s", strings.Count(prompt, shared), prompt)
	}
	if strings.Contains(prompt, "Project Context:\n"+shared) {
		t.Fatalf("expected duplicate project context to be omitted\nfull prompt:\n%s", prompt)
	}
}
