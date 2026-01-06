package prompts

import (
	"strings"
	"testing"
	"time"
)

func TestPromptInfoForCommitAndPRKinds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Date(2025, 12, 13, 12, 0, 0, 0, time.UTC)

	commit, err := PromptInfoFor("commit", now)
	if err != nil {
		t.Fatalf("PromptInfoFor(commit): %v", err)
	}
	if commit.Kind != "commit" || strings.TrimSpace(commit.Effective) == "" {
		t.Fatalf("unexpected commit prompt info: %#v", commit)
	}
	if !strings.Contains(commit.Effective, "SECURITY / SAFETY") {
		t.Fatalf("expected commit prompt to include safety guidance")
	}
	if !strings.Contains(commit.Effective, "action header") {
		t.Fatalf("expected commit prompt to mention action header")
	}
	if !strings.Contains(commit.Effective, "update(changes): staged changes") {
		t.Fatalf("expected commit prompt to include fallback guidance")
	}

	pr, err := PromptInfoFor("pr", now)
	if err != nil {
		t.Fatalf("PromptInfoFor(pr): %v", err)
	}
	if pr.Kind != "pr" || strings.TrimSpace(pr.Effective) == "" {
		t.Fatalf("unexpected pr prompt info: %#v", pr)
	}
	if !strings.Contains(pr.Effective, "EXACTLY ONE JSON object") {
		t.Fatalf("expected pr prompt to require a single JSON object")
	}
	if !strings.Contains(pr.Effective, "Escape newlines") || !strings.Contains(pr.Effective, `\\n`) {
		t.Fatalf("expected pr prompt to describe JSON newline escaping")
	}
	if !strings.Contains(pr.Effective, `"title"`) || !strings.Contains(pr.Effective, `"body"`) {
		t.Fatalf("expected pr prompt to describe required JSON keys")
	}
}

func TestPromptEnvOverrideApplied(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BUCKLEY_PROMPT_COMMIT", "{{DEFAULT_PROMPT}}\n\nExtra guidance line.")
	now := time.Date(2025, 12, 13, 12, 0, 0, 0, time.UTC)

	info, err := PromptInfoFor("commit", now)
	if err != nil {
		t.Fatalf("PromptInfoFor(commit): %v", err)
	}
	if !info.Overridden {
		t.Fatalf("expected overridden=true when BUCKLEY_PROMPT_COMMIT is set")
	}
	if !strings.Contains(info.Effective, "Extra guidance line.") {
		t.Fatalf("expected effective prompt to include env override content; got: %q", info.Effective)
	}
}

func TestListPromptInfoIncludesCommitAndPR(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Date(2025, 12, 13, 12, 0, 0, 0, time.UTC)

	infos, err := ListPromptInfo(now)
	if err != nil {
		t.Fatalf("ListPromptInfo: %v", err)
	}

	kinds := make(map[string]struct{}, len(infos))
	for _, info := range infos {
		kinds[info.Kind] = struct{}{}
	}
	if _, ok := kinds["commit"]; !ok {
		t.Fatalf("expected commit prompt to be listed")
	}
	if _, ok := kinds["pr"]; !ok {
		t.Fatalf("expected pr prompt to be listed")
	}
}
