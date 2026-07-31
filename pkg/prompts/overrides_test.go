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

func TestReviewSubKindsSupportEnvAndFileOverrides(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	now := time.Date(2025, 12, 13, 12, 0, 0, 0, time.UTC)

	for _, kind := range []string{"review-branch", "review-project", "review-pr"} {
		t.Run(kind, func(t *testing.T) {
			if !isSupportedPrompt(kind) {
				t.Fatalf("%s is not a supported prompt kind", kind)
			}
			if err := SaveOverride(kind, "{{DEFAULT_PROMPT}}\n\nExtra "+kind+" guidance."); err != nil {
				t.Fatalf("SaveOverride(%s): %v", kind, err)
			}
			t.Cleanup(func() { _ = DeleteOverride(kind) })

			override := resolveOverride(kind)
			if override == "" {
				t.Fatalf("resolveOverride(%s) returned no override after SaveOverride", kind)
			}
			effective := resolvePrompt(kind, "default body", now)
			if !strings.Contains(effective, "Extra "+kind+" guidance.") {
				t.Fatalf("resolvePrompt(%s) did not apply saved override; got: %q", kind, effective)
			}
		})
	}
}

func TestPromptEnvKeyReplacesHyphensWithUnderscores(t *testing.T) {
	if got, want := promptEnvKey("review-branch"), "BUCKLEY_PROMPT_REVIEW_BRANCH"; got != want {
		t.Fatalf("promptEnvKey(review-branch) = %q, want %q", got, want)
	}
	if got, want := promptEnvKey("review-project"), "BUCKLEY_PROMPT_REVIEW_PROJECT"; got != want {
		t.Fatalf("promptEnvKey(review-project) = %q, want %q", got, want)
	}
	if got, want := promptEnvKey("review-pr"), "BUCKLEY_PROMPT_REVIEW_PR"; got != want {
		t.Fatalf("promptEnvKey(review-pr) = %q, want %q", got, want)
	}
}

func TestReviewSubKindEnvOverrideApplied(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BUCKLEY_PROMPT_REVIEW_BRANCH", "{{DEFAULT_PROMPT}}\n\nEnv override line.")
	now := time.Date(2025, 12, 13, 12, 0, 0, 0, time.UTC)

	effective := resolvePrompt("review-branch", "default body", now)
	if !strings.Contains(effective, "Env override line.") {
		t.Fatalf("expected env override applied via hyphen-safe env key; got: %q", effective)
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
