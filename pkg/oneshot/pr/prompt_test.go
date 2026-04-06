package pr

import (
	"strings"
	"testing"
)

func TestSystemPrompt(t *testing.T) {
	prompt := SystemPrompt()

	// Should contain key instructions
	if !strings.Contains(prompt, "pull request generator") {
		t.Error("expected prompt to mention pull request generator")
	}
	if !strings.Contains(prompt, "generate_pull_request") {
		t.Error("expected prompt to mention generate_pull_request tool")
	}
	if !strings.Contains(prompt, "Title") {
		t.Error("expected prompt to mention Title")
	}
	if !strings.Contains(prompt, "Summary") {
		t.Error("expected prompt to mention Summary")
	}
	if !strings.Contains(prompt, "Changes") {
		t.Error("expected prompt to mention Changes")
	}
	if !strings.Contains(prompt, "Testing") {
		t.Error("expected prompt to mention Testing")
	}
	if !strings.Contains(prompt, "breaking changes") {
		t.Error("expected prompt to mention breaking changes")
	}
	if !strings.Contains(prompt, "MUST call") {
		t.Error("expected prompt to have MUST call instruction")
	}
}

func TestBuildPrompt(t *testing.T) {
	ctx := &Context{
		Branch:     "feature/new-api",
		BaseBranch: "main",
		Commits: []CommitInfo{
			{Hash: "abc1234567890", Subject: "Add new API endpoint"},
			{Hash: "def1234567890", Subject: "Add tests", Body: "Unit tests for the new endpoint."},
		},
		DiffSummary: " 2 files changed, 50 insertions(+), 10 deletions(-)",
		FullDiff:    "+new code\n-old code",
		AgentsMD:    "# Project Guidelines\nFollow TDD.",
	}

	prompt := BuildPrompt(ctx)

	// Check branch info
	if !strings.Contains(prompt, "feature/new-api → main") {
		t.Error("expected branch info with arrow")
	}
	if !strings.Contains(prompt, "Commits: 2") {
		t.Error("expected commit count")
	}

	// Check commits section
	if !strings.Contains(prompt, "## Commits") {
		t.Error("expected Commits section")
	}
	if !strings.Contains(prompt, "abc1234") {
		t.Error("expected shortened commit hash")
	}
	if !strings.Contains(prompt, "Add new API endpoint") {
		t.Error("expected commit subject")
	}
	if !strings.Contains(prompt, "Unit tests for the new endpoint") {
		t.Error("expected commit body content")
	}

	// Check diff summary section
	if !strings.Contains(prompt, "## Diff Summary") {
		t.Error("expected Diff Summary section")
	}
	if !strings.Contains(prompt, "50 insertions") {
		t.Error("expected diff summary content")
	}

	// Check full diff section
	if !strings.Contains(prompt, "## Full Diff") {
		t.Error("expected Full Diff section")
	}
	if !strings.Contains(prompt, "```diff") {
		t.Error("expected diff code block")
	}
	if !strings.Contains(prompt, "+new code") {
		t.Error("expected diff content")
	}

	// Check project context section
	if !strings.Contains(prompt, "## Project Context (AGENTS.md)") {
		t.Error("expected Project Context section")
	}
	if !strings.Contains(prompt, "Follow TDD") {
		t.Error("expected AGENTS.md content")
	}

	// Check tool call instruction
	if !strings.Contains(prompt, "Call the generate_pull_request tool") {
		t.Error("expected tool call instruction")
	}
}

func TestBuildPrompt_NoCommits(t *testing.T) {
	ctx := &Context{
		Branch:     "feature/empty",
		BaseBranch: "main",
		Commits:    nil,
		FullDiff:   "diff content",
	}

	prompt := BuildPrompt(ctx)

	// Should NOT have commits section
	if strings.Contains(prompt, "## Commits") {
		t.Error("should not have Commits section when no commits")
	}
	if !strings.Contains(prompt, "Commits: 0") {
		t.Error("expected Commits: 0")
	}
}

func TestBuildPrompt_NoDiffSummary(t *testing.T) {
	ctx := &Context{
		Branch:      "feature/test",
		BaseBranch:  "main",
		Commits:     []CommitInfo{{Hash: "abc1234567890", Subject: "Test"}},
		DiffSummary: "",
		FullDiff:    "diff content",
	}

	prompt := BuildPrompt(ctx)

	// Should NOT have diff summary section
	if strings.Contains(prompt, "## Diff Summary") {
		t.Error("should not have Diff Summary section when empty")
	}
}

func TestBuildPrompt_NoAgentsMD(t *testing.T) {
	ctx := &Context{
		Branch:     "feature/test",
		BaseBranch: "main",
		Commits:    []CommitInfo{{Hash: "abc1234567890", Subject: "Test"}},
		FullDiff:   "diff content",
		AgentsMD:   "",
	}

	prompt := BuildPrompt(ctx)

	// Should NOT have project context section
	if strings.Contains(prompt, "## Project Context") {
		t.Error("should not have Project Context section when AgentsMD is empty")
	}
}

func TestBuildPrompt_NoFullDiff(t *testing.T) {
	ctx := &Context{
		Branch:     "feature/test",
		BaseBranch: "main",
		Commits:    []CommitInfo{{Hash: "abc1234567890", Subject: "Test"}},
		FullDiff:   "",
	}

	prompt := BuildPrompt(ctx)

	// Should NOT have full diff section
	if strings.Contains(prompt, "## Full Diff") {
		t.Error("should not have Full Diff section when empty")
	}
}

func TestBuildPrompt_CommitBodyTruncation(t *testing.T) {
	ctx := &Context{
		Branch:     "feature/test",
		BaseBranch: "main",
		Commits: []CommitInfo{
			{
				Hash:    "abc1234567890",
				Subject: "Test commit",
				Body: `Line 1 of body
Line 2 of body
Line 3 of body
Line 4 of body - should be truncated
Line 5 of body - should be truncated`,
			},
		},
		FullDiff: "diff content",
	}

	prompt := BuildPrompt(ctx)

	// Should include first 3 lines
	if !strings.Contains(prompt, "Line 1 of body") {
		t.Error("expected Line 1")
	}
	if !strings.Contains(prompt, "Line 2 of body") {
		t.Error("expected Line 2")
	}
	if !strings.Contains(prompt, "Line 3 of body") {
		t.Error("expected Line 3")
	}
	// Should NOT include lines after 3
	if strings.Contains(prompt, "Line 4 of body") {
		t.Error("should not include Line 4 (after truncation limit)")
	}
}

func TestBuildPrompt_CommitBodySkipsEmptyLines(t *testing.T) {
	ctx := &Context{
		Branch:     "feature/test",
		BaseBranch: "main",
		Commits: []CommitInfo{
			{
				Hash:    "abc1234567890",
				Subject: "Test commit",
				Body: `Line 1

Line 2`,
			},
		},
		FullDiff: "diff content",
	}

	prompt := BuildPrompt(ctx)

	// Should include non-empty lines
	if !strings.Contains(prompt, "Line 1") {
		t.Error("expected Line 1")
	}
	if !strings.Contains(prompt, "Line 2") {
		t.Error("expected Line 2")
	}
}

func TestBuildPrompt_MultipleCommitsWithBodies(t *testing.T) {
	ctx := &Context{
		Branch:     "feature/test",
		BaseBranch: "main",
		Commits: []CommitInfo{
			{Hash: "aaa1234567890", Subject: "First", Body: "First body"},
			{Hash: "bbb1234567890", Subject: "Second", Body: "Second body"},
			{Hash: "ccc1234567890", Subject: "Third"}, // No body
		},
		FullDiff: "diff content",
	}

	prompt := BuildPrompt(ctx)

	// Should have all commits
	if !strings.Contains(prompt, "aaa1234") {
		t.Error("expected first commit hash")
	}
	if !strings.Contains(prompt, "bbb1234") {
		t.Error("expected second commit hash")
	}
	if !strings.Contains(prompt, "ccc1234") {
		t.Error("expected third commit hash")
	}
	if !strings.Contains(prompt, "First body") {
		t.Error("expected first commit body")
	}
	if !strings.Contains(prompt, "Second body") {
		t.Error("expected second commit body")
	}
}

func TestBuildPrompt_LongCommitHash(t *testing.T) {
	ctx := &Context{
		Branch:     "feature/test",
		BaseBranch: "main",
		Commits: []CommitInfo{
			{Hash: "abc123def456789012345678901234567890", Subject: "Test"},
		},
		FullDiff: "diff content",
	}

	prompt := BuildPrompt(ctx)

	// Should use only first 7 chars of hash
	if !strings.Contains(prompt, "abc123d") {
		t.Error("expected first 7 chars of hash")
	}
	// Should not have full hash
	if strings.Contains(prompt, "abc123def456789012345678901234567890") {
		t.Error("should not have full hash")
	}
}

func TestBuildPrompt_GeneratePRInstruction(t *testing.T) {
	ctx := &Context{
		Branch:     "feature/test",
		BaseBranch: "main",
		Commits:    []CommitInfo{{Hash: "abc1234567890", Subject: "Test"}},
		FullDiff:   "diff",
	}

	prompt := BuildPrompt(ctx)

	// Must end with tool call instruction
	if !strings.HasSuffix(prompt, "Call the generate_pull_request tool with the PR details.") {
		t.Error("expected prompt to end with tool call instruction")
	}
}

func TestSystemPrompt_RequiredElements(t *testing.T) {
	prompt := SystemPrompt()

	requiredPhrases := []string{
		"pull request generator",
		"generate_pull_request tool",
		"Title",
		"Summary",
		"Changes",
		"Testing",
		"breaking changes",
		"issues",
		"reviewers",
	}

	for _, phrase := range requiredPhrases {
		if !strings.Contains(strings.ToLower(prompt), strings.ToLower(phrase)) {
			t.Errorf("expected system prompt to contain %q", phrase)
		}
	}
}

func TestBuildPrompt_AllSections(t *testing.T) {
	// Full context with all fields populated
	ctx := &Context{
		Branch:     "feature/complete",
		BaseBranch: "main",
		Commits: []CommitInfo{
			{Hash: "abc1234567890", Subject: "Feature", Body: "Details"},
		},
		DiffSummary: "3 files changed",
		FullDiff:    "+addition\n-removal",
		AgentsMD:    "# Guidelines",
	}

	prompt := BuildPrompt(ctx)

	// All sections should be present
	sections := []string{
		"## Commits",
		"## Diff Summary",
		"## Full Diff",
		"## Project Context",
	}

	for _, section := range sections {
		if !strings.Contains(prompt, section) {
			t.Errorf("expected section %q in prompt", section)
		}
	}
}
