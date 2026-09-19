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
		AgentProfile:      "Agent: verifier\nAgent Instructions:\nRun tests.",
		ProjectContext:    "Project context block",
		KnowledgeContext:  "Hyphae Project Knowledge:\n- Available: hypha://m31labs/buckley",
		WorkDir:           workDir,
		RootDir:           root,
		SkillsDescription: "Skills:\n- Example skill",
		TaskType:          "coding",
	})

	for _, want := range []string{
		"Base prompt",
		"Agent Profile:\nAgent: verifier",
		"Agent Instructions:\nRun tests.",
		"Repository Instructions:",
		"Use focused edits.",
		"Project Context:\nProject context block",
		"Hyphae Project Knowledge:\n- Available: hypha://m31labs/buckley",
		"Working Directory: " + workDir,
		"Skills:\n- Example skill",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected prompt to contain %q\nfull prompt:\n%s", want, prompt)
		}
	}
}

func TestDefaultToolUseSystemPromptOutcomeFirst(t *testing.T) {
	t.Parallel()

	for _, want := range []string{
		"questions and read-only tasks",
		"observable scoped change",
		"cheapest relevant verification after the final change",
		"Never claim",
		"blocked or incomplete",
	} {
		if !strings.Contains(DefaultToolUseSystemPrompt, want) {
			t.Fatalf("default tool-use prompt missing %q:\n%s", want, DefaultToolUseSystemPrompt)
		}
	}
	for _, old := range []string{"Always take action with tools", "MUST use tools"} {
		if strings.Contains(DefaultToolUseSystemPrompt, old) {
			t.Fatalf("default tool-use prompt retained old pressure %q:\n%s", old, DefaultToolUseSystemPrompt)
		}
	}
}

func TestBuildRuntimeSystemPrompt_UsesSharedDefaultToolUsePrompt(t *testing.T) {
	t.Parallel()

	prompt := BuildRuntimeSystemPrompt(RuntimePromptInput{})
	if prompt != DefaultToolUseSystemPrompt {
		t.Fatalf("default runtime prompt = %q, want shared default %q", prompt, DefaultToolUseSystemPrompt)
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
