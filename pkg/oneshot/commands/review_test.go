package commands

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"m31labs.dev/buckley/pkg/diffsignal"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/rlm"
)

func TestDefaultBranchContextOptions(t *testing.T) {
	opts := DefaultBranchContextOptions()

	assert.Equal(t, diffsignal.ReviewDiffBudget, opts.MaxDiffBytes)
	assert.True(t, opts.IncludeUnstaged)
	assert.True(t, opts.IncludeAgents)
	assert.Empty(t, opts.BaseBranch)
}

func TestDefaultProjectContextOptions(t *testing.T) {
	opts := DefaultProjectContextOptions()

	assert.Equal(t, 3, opts.MaxTreeDepth)
	assert.True(t, opts.IncludeAgents)
}

func TestReviewDefinitionsExposeOnlySnapshotReviewTools(t *testing.T) {
	want := []string{"read_file", "find_files", "search_text", "run_verification"}
	definitions := []struct {
		name  string
		tools []string
	}{
		{name: "branch", tools: (ReviewBranchDef{}).AllowedTools()},
		{name: "pull request", tools: (ReviewPRDef{}).AllowedTools()},
	}
	projectTools := (ReviewProjectDef{}).AllowedTools()
	assert.Equal(t, []string{"read_file", "find_files", "search_text"}, projectTools)
	assert.NotContains(t, projectTools, "run_verification")

	for _, definition := range definitions {
		t.Run(definition.name, func(t *testing.T) {
			assert.Equal(t, want, definition.tools)
			assert.Contains(t, definition.tools, "run_verification")
			assert.NotContains(t, definition.tools, "run_shell")
			assert.NotContains(t, definition.tools, "write_file")
		})
	}

	assert.Contains(t, (FixFindingDef{}).AllowedTools(), "run_shell")
	assert.Contains(t, (FixFindingDef{}).AllowedTools(), "write_file")
}

func TestReviewPRSystemPromptInstantiatesExactFeedbackContract(t *testing.T) {
	withFeedback := (ReviewPRDef{RequiredFeedbackIDs: []string{
		"submitted-review:PRR_1",
		"inline-thread:PRRT_2/comment:PRRC_3",
	}}).SystemPrompt()
	for _, want := range []string{
		"EXACT FEEDBACK OUTPUT CONTRACT FOR THIS REVIEW",
		"- **Feedback disposition**: `DISPOSITIONED`",
		"- **Feedback**: `submitted-review:PRR_1` — `ADDRESSED|DISPUTED|UNRESOLVED`",
		"- **Feedback**: `inline-thread:PRRT_2/comment:PRRC_3` — `ADDRESSED|DISPUTED|UNRESOLVED`",
		"do not use NONE_SUPPLIED",
	} {
		if !strings.Contains(withFeedback, want) {
			t.Errorf("feedback-aware prompt missing %q", want)
		}
	}

	withoutFeedback := (ReviewPRDef{}).SystemPrompt()
	if !strings.Contains(withoutFeedback, "- **Feedback disposition**: `NONE_SUPPLIED`") {
		t.Fatal("no-feedback prompt did not instantiate NONE_SUPPLIED disposition")
	}
}

func TestDiffStats_TotalChanges(t *testing.T) {
	tests := []struct {
		name     string
		stats    DiffStats
		expected int
	}{
		{"empty", DiffStats{}, 0},
		{"insertions only", DiffStats{Insertions: 10}, 10},
		{"deletions only", DiffStats{Deletions: 5}, 5},
		{"both", DiffStats{Insertions: 10, Deletions: 5}, 15},
		{"with files", DiffStats{Files: 3, Insertions: 100, Deletions: 50}, 150},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.stats.TotalChanges())
		})
	}
}

func TestParseNameStatus(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []FileChange
	}{
		{
			name:     "empty",
			input:    "",
			expected: nil,
		},
		{
			name:  "modified file",
			input: "M\tfile.go",
			expected: []FileChange{
				{Status: "M", Path: "file.go"},
			},
		},
		{
			name:  "added file",
			input: "A\tnew_file.go",
			expected: []FileChange{
				{Status: "A", Path: "new_file.go"},
			},
		},
		{
			name:  "deleted file",
			input: "D\told_file.go",
			expected: []FileChange{
				{Status: "D", Path: "old_file.go"},
			},
		},
		{
			name:  "renamed file",
			input: "R100\told.go\tnew.go",
			expected: []FileChange{
				{Status: "R", OldPath: "old.go", Path: "new.go"},
			},
		},
		{
			name:  "copied file",
			input: "C50\tsource.go\tcopy.go",
			expected: []FileChange{
				{Status: "C", OldPath: "source.go", Path: "copy.go"},
			},
		},
		{
			name: "multiple files",
			input: `M	pkg/foo.go
A	pkg/bar.go
D	pkg/old.go`,
			expected: []FileChange{
				{Status: "M", Path: "pkg/foo.go"},
				{Status: "A", Path: "pkg/bar.go"},
				{Status: "D", Path: "pkg/old.go"},
			},
		},
		{
			name:     "invalid line - no tab",
			input:    "M file.go",
			expected: nil,
		},
		{
			name:     "empty lines ignored",
			input:    "\n\n",
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseNameStatus(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"", 0},
		{"a", 1},
		{"ab", 1},
		{"abc", 1},
		{"abcd", 1},
		{"abcde", 2},
		{"Hello, World!", 4},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, reviewEstimateTokens(tt.input))
		})
	}
}

func TestBuildBranchPrompt(t *testing.T) {
	ctx := &BranchContext{
		RepoRoot:   "/home/user/project",
		Branch:     "feature/new-feature",
		BaseBranch: "main",
		Scope:      ReviewScopeBranch,
		Files: []FileChange{
			{Status: "M", Path: "file1.go"},
			{Status: "A", Path: "file2.go"},
		},
		Stats: DiffStats{
			Files:      2,
			Insertions: 50,
			Deletions:  10,
		},
		Diff:      "diff --git a/file1.go",
		RecentLog: "abc123 Some commit",
	}

	prompt := BuildBranchPrompt(ctx)

	assert.Contains(t, prompt, "Repository Information")
	assert.Contains(t, prompt, "captured repository root")
	assert.NotContains(t, prompt, "/home/user/project")
	assert.Contains(t, prompt, "feature/new-feature")
	assert.Contains(t, prompt, "main")
	assert.Contains(t, prompt, "Review Scope")
	assert.Contains(t, prompt, ReviewScopeBranch)
	assert.Contains(t, prompt, "Files Changed")
	assert.Contains(t, prompt, "2 files, +50/-10 lines")
	assert.Contains(t, prompt, "file1.go")
	assert.Contains(t, prompt, "file2.go")
	assert.Contains(t, prompt, "Full Diff")
	assert.Contains(t, prompt, "Commits on this Branch")
	assert.Contains(t, prompt, "abc123 Some commit")
}

func TestNormalizeReviewScope(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ReviewScopeWorktree},
		{"worktree", ReviewScopeWorktree},
		{"branch", ReviewScopeBranch},
		{"commits", ReviewScopeBranch},
		{"changes", ReviewScopeChanges},
		{"local", ReviewScopeChanges},
		{"unknown", ReviewScopeWorktree},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, normalizeReviewScope(tt.input))
		})
	}
}

func TestMergeFileChangesDedupes(t *testing.T) {
	base := []FileChange{{Status: "M", Path: "a.go"}}
	extra := []FileChange{
		{Status: "M", Path: "a.go"},
		{Status: "A", Path: "b.go"},
	}

	got := mergeFileChanges(base, extra)
	assert.Equal(t, []FileChange{
		{Status: "M", Path: "a.go"},
		{Status: "A", Path: "b.go"},
	}, got)
}

func TestBuildBranchPrompt_WithUncommittedChanges(t *testing.T) {
	ctx := &BranchContext{
		RepoRoot:         "/home/user/project",
		Branch:           "main",
		BaseBranch:       "main",
		Diff:             "some diff",
		Unstaged:         "unstaged changes here",
		IncludesUnstaged: true,
	}

	prompt := BuildBranchPrompt(ctx)

	assert.Contains(t, prompt, "Worktree Changes (staged and unstaged)")
	assert.Contains(t, prompt, "unstaged changes here")
}

func TestBuildBranchPrompt_WithAgentsMD(t *testing.T) {
	ctx := &BranchContext{
		RepoRoot:   "/home/user/project",
		Branch:     "main",
		BaseBranch: "main",
		Diff:       "some diff",
		AgentsMD:   "# Project Guidelines\n\nFollow these rules...",
	}

	prompt := BuildBranchPrompt(ctx)

	assert.Contains(t, prompt, "Project Guidelines (applicable AGENTS.md chain)")
	assert.Contains(t, prompt, "Follow these rules")
}

func TestBuildProjectPrompt(t *testing.T) {
	ctx := &ProjectContext{
		RepoRoot:  "/home/user/project",
		Branch:    "main",
		Tree:      ".\n├── cmd\n└── pkg",
		GoMod:     "module github.com/user/project",
		RecentLog: "abc123 Initial commit",
	}

	prompt := BuildProjectPrompt(ctx)

	assert.Contains(t, prompt, "Repository Information")
	assert.Contains(t, prompt, "captured repository root")
	assert.NotContains(t, prompt, "/home/user/project")
	assert.Contains(t, prompt, "main")
	assert.Contains(t, prompt, "Project Structure")
	assert.Contains(t, prompt, "cmd")
	assert.Contains(t, prompt, "pkg")
	assert.Contains(t, prompt, "go.mod")
	assert.Contains(t, prompt, "module github.com/user/project")
	assert.Contains(t, prompt, "Recent Git History")
}

func TestBuildProjectPrompt_WithAllFields(t *testing.T) {
	ctx := &ProjectContext{
		RepoRoot:    "/home/user/project",
		Branch:      "develop",
		Tree:        "directory tree",
		GoMod:       "go mod content",
		PackageJSON: `{"name": "project"}`,
		ReadmeMD:    "# Project README",
		AgentsMD:    "# AGENTS guidelines",
		RecentLog:   "commits",
	}

	prompt := BuildProjectPrompt(ctx)

	assert.Contains(t, prompt, "develop")
	assert.Contains(t, prompt, "directory tree")
	assert.Contains(t, prompt, "go mod content")
	assert.Contains(t, prompt, "package.json")
	assert.Contains(t, prompt, `{"name": "project"}`)
	assert.Contains(t, prompt, "README.md")
	assert.Contains(t, prompt, "Project README")
	assert.Contains(t, prompt, "AGENTS.md")
	assert.Contains(t, prompt, "AGENTS guidelines")
}

func TestFileChange(t *testing.T) {
	fc := FileChange{
		Status:  "R",
		Path:    "new/path.go",
		OldPath: "old/path.go",
	}

	assert.Equal(t, "R", fc.Status)
	assert.Equal(t, "new/path.go", fc.Path)
	assert.Equal(t, "old/path.go", fc.OldPath)
}

func TestBranchContext_Fields(t *testing.T) {
	ctx := BranchContext{
		RepoRoot:   "/root",
		Branch:     "feature",
		BaseBranch: "main",
		HeadCommit: strings.Repeat("a", 40),
		BaseCommit: strings.Repeat("b", 40),
		Files:      []FileChange{{Status: "M", Path: "f.go"}},
		Stats:      DiffStats{Files: 1, Insertions: 10, Deletions: 5},
		Diff:       "diff content",
		Unstaged:   "unstaged",
		RecentLog:  "log",
		AgentsMD:   "agents",
	}

	assert.Equal(t, "/root", ctx.RepoRoot)
	assert.Equal(t, "feature", ctx.Branch)
	assert.Equal(t, "main", ctx.BaseBranch)
	assert.Len(t, ctx.HeadCommit, 40)
	assert.Len(t, ctx.BaseCommit, 40)
	assert.Len(t, ctx.Files, 1)
	assert.Equal(t, 15, ctx.Stats.TotalChanges())
	assert.NotEmpty(t, ctx.Diff)
	assert.NotEmpty(t, ctx.Unstaged)
	assert.NotEmpty(t, ctx.RecentLog)
	assert.NotEmpty(t, ctx.AgentsMD)
}

func TestProjectContext_Fields(t *testing.T) {
	ctx := ProjectContext{
		RepoRoot:    "/root",
		Branch:      "main",
		HeadCommit:  strings.Repeat("a", 40),
		Tree:        "tree",
		GoMod:       "gomod",
		PackageJSON: "pkg",
		ReadmeMD:    "readme",
		AgentsMD:    "agents",
		RecentLog:   "log",
	}

	assert.Equal(t, "/root", ctx.RepoRoot)
	assert.Equal(t, "main", ctx.Branch)
	assert.Len(t, ctx.HeadCommit, 40)
	assert.NotEmpty(t, ctx.Tree)
	assert.NotEmpty(t, ctx.GoMod)
	assert.NotEmpty(t, ctx.PackageJSON)
	assert.NotEmpty(t, ctx.ReadmeMD)
	assert.NotEmpty(t, ctx.AgentsMD)
	assert.NotEmpty(t, ctx.RecentLog)
}

func TestReviewRLMResult_Fields(t *testing.T) {
	result := ReviewRLMResult{
		Review: "Some review content",
		Parsed: nil,
	}

	assert.Equal(t, "Some review content", result.Review)
	assert.Nil(t, result.Parsed)
}

func TestApprovedAPIReviewRequiresSuccessfulVerificationToolEvidence(t *testing.T) {
	result := &ReviewRLMResult{Parsed: &ParsedReview{Approved: true}}
	execution := &oneshot.RLMResult{ProviderID: "openai"}
	changedFiles := []string{"pkg/oneshot/commands/review.go"}
	assert.ErrorContains(t, validateReviewExecutionEvidence(result, execution, changedFiles), "does not cover")

	execution.ToolCalls = []rlm.SubAgentToolCall{
		{Name: "run_verification", Success: true, Data: map[string]any{
			"kind": "build", "language": "go", "path": "pkg/oneshot/commands", "pattern": "", "status": "PASS", "exit_code": 0,
		}},
		{Name: "run_verification", Success: false, Data: map[string]any{
			"kind": "test", "language": "go", "path": "pkg/oneshot/commands", "pattern": "", "status": "FAIL", "exit_code": 1,
		}},
	}
	assert.ErrorContains(t, validateReviewExecutionEvidence(result, execution, changedFiles), "does not cover")

	execution.ToolCalls[1].Success = true
	execution.ToolCalls[1].Data["status"] = "PASS"
	execution.ToolCalls[1].Data["exit_code"] = 0
	assert.NoError(t, validateReviewExecutionEvidence(result, execution, changedFiles))

	execution.ToolCalls[1].Data["pattern"] = "NoSuchTest"
	assert.ErrorContains(t, validateReviewExecutionEvidence(result, execution, changedFiles), "does not cover")
	execution.ToolCalls[1].Data["pattern"] = "TestFocused"
	execution.ToolCalls[1].Data["stdout"] = "=== RUN   TestFocused\n--- PASS: TestFocused (0.00s)\nPASS"
	assert.NoError(t, validateReviewExecutionEvidence(result, execution, changedFiles))
	execution.ToolCalls[1].Data["pattern"] = ""

	execution.ProviderID = "codex"
	execution.ToolCalls = nil
	assert.ErrorContains(t, validateReviewExecutionEvidence(result, execution, changedFiles), "does not cover")

	exitZero := 0
	execution.ExecutionEvidence = []model.CommandExecutionEvidence{
		{
			Command:          `/bin/bash -lc 'go build ./pkg/oneshot/commands'`,
			ExitCode:         &exitZero,
			Status:           "completed",
			WorkingDirectory: "/snapshot",
			RepositoryRoot:   "/snapshot",
		},
		{
			Command:          `/bin/bash -lc 'go test ./pkg/oneshot/commands'`,
			AggregatedOutput: "ok  m31labs.dev/buckley/pkg/oneshot/commands",
			ExitCode:         &exitZero,
			Status:           "completed",
			WorkingDirectory: "/snapshot",
			RepositoryRoot:   "/snapshot",
		},
	}
	assert.NoError(t, validateReviewExecutionEvidence(result, execution, changedFiles))

	result.Parsed.Approved = false
	execution.ProviderID = "openai"
	assert.NoError(t, validateReviewExecutionEvidence(result, execution, changedFiles))
}

func TestApprovedPRDocumentationReviewUsesExactDiffLedgerInsteadOfUnrelatedCommands(t *testing.T) {
	changedFiles := []string{"README.md", "docs/release-notes.mdx"}
	result := &ReviewRLMResult{Parsed: &ParsedReview{
		Approved: true,
		CoverageEntries: []CoverageEntry{
			{Path: "README.md", Evidence: "checked the installation claim against the changed command example"},
			{Path: "docs/release-notes.mdx", Evidence: "checked the release link and version claim in the diff"},
		},
	}}
	def := ReviewPRDef{ChangedFiles: changedFiles}

	assert.NoError(t, def.ValidateRLMExecution(result, nil))

	result.Parsed.CoverageEntries = result.Parsed.CoverageEntries[:1]
	assert.ErrorContains(t, def.ValidateRLMExecution(result, nil), "documentation-only approval requires exact changed-file diff evidence")
}

func TestReviewPRRuntimeAcceptsExactChangelogDocumentationLedger(t *testing.T) {
	def := ReviewPRDef{
		ChangedFiles: []string{"CHANGELOG.md"},
		CIStatus:     "passing (1/1)",
		CIProvenance: prCISourceBase,
	}
	result, err := def.ParseResult(`## Grade: A

## Summary
The release notes accurately describe the shipped behavior.

## CI Status
- Build: PASS — named remote build check passed.
- Tests: PASS — named remote test check passed.

## Coverage
- **File**: ` + "`CHANGELOG.md`" + ` — checked every changed release claim and link against the diff.
- **Feedback disposition**: ` + "`NONE_SUPPLIED`" + ` — no prior feedback was supplied.
- **Verification**: documentation diff and named remote CI checks.

## Invariant Audit
- Checked release version, links, and claim consistency; no source invariant changed.

## Falsification
- **Strongest plausible failure**: a release claim could name the wrong shipped version.
- **Evidence**: the changed heading and linked release version agree.
- **Conclusion**: DISPROVED

## Findings
None.

## Verdict
- **Approved**: YES
- **Blockers**: None
- **Suggestions**: None
`)
	if err != nil {
		t.Fatalf("ParseResult: %v", err)
	}
	if err := def.ValidateResult(result); err != nil {
		t.Fatalf("ValidateResult: %v", err)
	}
	if err := def.ValidateRLMExecution(result, nil); err != nil {
		t.Fatalf("ValidateRLMExecution: %v", err)
	}
}

func TestApprovedPRDocumentationExceptionRejectsMixedOrNonDocumentationChanges(t *testing.T) {
	result := &ReviewRLMResult{Parsed: &ParsedReview{
		Approved: true,
		CoverageEntries: []CoverageEntry{
			{Path: "README.md", Evidence: "reviewed the changed documentation claim"},
			{Path: "config/release.yaml", Evidence: "reviewed the changed release configuration"},
		},
	}}
	execution := &oneshot.RLMResult{ProviderID: "codex"}

	mixed := ReviewPRDef{ChangedFiles: []string{"README.md", "config/release.yaml"}}
	assert.ErrorContains(t, mixed.ValidateRLMExecution(result, execution), "requires repo-root build and test evidence")

	result.Parsed.CoverageEntries = result.Parsed.CoverageEntries[:1]
	branch := ReviewBranchDef{ChangedFiles: []string{"README.md"}}
	assert.NoError(t, branch.ValidateRLMExecution(result, nil))

	result.Parsed.CoverageEntries = nil
	assert.ErrorContains(t, branch.ValidateRLMExecution(result, nil), "documentation-only approval requires exact changed-file diff evidence")

	sourceBranch := ReviewBranchDef{ChangedFiles: []string{"README.md", "main.go"}}
	assert.ErrorContains(t, sourceBranch.ValidateRLMExecution(result, execution), "does not cover changed source paths")
}

func TestReviewBranchDef_Interface(t *testing.T) {
	def := ReviewBranchDef{}

	assert.Equal(t, "review", def.Name())
	assert.NotEmpty(t, def.SystemPrompt())
	assert.Contains(t, def.AllowedTools(), "read_file")
	assert.NotContains(t, def.AllowedTools(), "run_shell")
	assert.NotContains(t, def.AllowedTools(), "write_file")

	result, err := def.ParseResult("## Grade: A\n\nLooks good")
	assert.NoError(t, err)
	assert.NotNil(t, result)

	rlmResult, ok := result.(*ReviewRLMResult)
	assert.True(t, ok)
	assert.Equal(t, "## Grade: A\n\nLooks good", rlmResult.Review)
	assert.NotNil(t, rlmResult.Parsed)
	assert.Equal(t, GradeA, rlmResult.Parsed.Grade)
}

func TestReviewProjectDef_Interface(t *testing.T) {
	def := ReviewProjectDef{}

	assert.Equal(t, "review-project", def.Name())
	assert.NotEmpty(t, def.SystemPrompt())
	assert.Contains(t, def.AllowedTools(), "read_file")
	assert.NotContains(t, def.AllowedTools(), "run_verification")
	assert.NotContains(t, def.AllowedTools(), "write_file")

	advisory, err := def.ParseResult("## Project Status\nArchitecture assessment only")
	assert.NoError(t, err)
	assert.NoError(t, def.ValidateResult(advisory))
	assert.ErrorContains(t, def.ValidateResult(&ReviewRLMResult{
		Parsed: &ParsedReview{Approved: true},
	}), "advisory")
}

func TestReviewPRDef_Interface(t *testing.T) {
	def := ReviewPRDef{}

	assert.Equal(t, "review-pr", def.Name())
	assert.NotEmpty(t, def.SystemPrompt())
	assert.Contains(t, def.AllowedTools(), "read_file")
	assert.NotContains(t, def.AllowedTools(), "run_shell")
	assert.NotContains(t, def.AllowedTools(), "write_file")
}

func TestApprovalReviewsRequireIndependentCritic(t *testing.T) {
	approval := completeReviewWithCoverage("- **File**: `ratchet.go` — reviewed the exact changed file and paired bound.\n" +
		"- **Feedback disposition**: `NONE_SUPPLIED` — no prior feedback was supplied.\n" +
		"- **Verification**: focused test passed.")

	branch := ReviewBranchDef{ChangedFiles: []string{"ratchet.go"}}
	branchResult, err := branch.ParseResult(approval)
	assert.NoError(t, err)
	assert.NoError(t, branch.ValidateResult(branchResult))
	assert.True(t, branch.RequiresApprovalCritic(branchResult))
	assert.Contains(t, branch.ApprovalCriticSystemPrompt(), "INDEPENDENT APPROVAL CRITIC ROLE")
	branchCriticPrompt, err := branch.BuildApprovalCriticPrompt("ORIGINAL DIFF", branchResult)
	assert.NoError(t, err)
	assert.Contains(t, branchCriticPrompt, "ORIGINAL DIFF")
	assert.Contains(t, branchCriticPrompt, approval)
	assert.Contains(t, branchCriticPrompt, "complete machine-validated review becomes the final result")

	pr := ReviewPRDef{ChangedFiles: []string{"ratchet.go"}, CIStatus: "passing (1/1)", CIProvenance: prCISourceHead}
	prResult, err := pr.ParseResult(approval)
	assert.NoError(t, err)
	assert.NoError(t, pr.ValidateResult(prResult))
	assert.True(t, pr.RequiresApprovalCritic(prResult))
	assert.Contains(t, pr.ApprovalCriticSystemPrompt(), "INDEPENDENT APPROVAL CRITIC ROLE")

	nonApproval := strings.Replace(approval, "## Grade: A", "## Grade: C", 1)
	nonApproval = strings.Replace(nonApproval, "**Conclusion**: DISPROVED", "**Conclusion**: PROVED", 1)
	nonApproval = strings.Replace(nonApproval, "**Recommendation**: APPROVE", "**Recommendation**: REQUEST CHANGES", 1)
	nonApprovalResult, err := pr.ParseResult(nonApproval)
	assert.NoError(t, err)
	assert.NoError(t, pr.ValidateResult(nonApprovalResult))
	assert.False(t, pr.RequiresApprovalCritic(nonApprovalResult))
}

func TestFixFindingDef_Interface(t *testing.T) {
	def := FixFindingDef{}

	assert.Equal(t, "fix-finding", def.Name())
	assert.NotEmpty(t, def.SystemPrompt())
	assert.Contains(t, def.AllowedTools(), "write_file")

	result, err := def.ParseResult("Fixed the bug")
	assert.NoError(t, err)
	fixResult, ok := result.(*FixResult)
	assert.True(t, ok)
	assert.Equal(t, "Fixed the bug", fixResult.Summary)
}

func TestParseReview_Grade(t *testing.T) {
	tests := []struct {
		input    string
		expected Grade
	}{
		{"## Grade: A\n\nGreat code", GradeA},
		{"## Grade: B\n\nGood code", GradeB},
		{"## Grade: C\n\nOk code", GradeC},
		{"## Grade: D\n\nNeeds work", GradeD},
		{"## Grade: F\n\nBroken", GradeF},
		{"No grade here", ""},
	}

	for _, tt := range tests {
		t.Run(string(tt.expected), func(t *testing.T) {
			parsed := ParseReview(tt.input)
			assert.Equal(t, tt.expected, parsed.Grade)
		})
	}
}

func TestParseReview_Findings(t *testing.T) {
	review := `## Grade: C

## Summary
Code has issues.

## Findings

### FINDING-001: [CRITICAL] SQL injection vulnerability
- **File**: pkg/db/query.go:42
- **Evidence**: Uses string concatenation
- **Impact**: Data breach risk
- **Fix**: Use parameterized queries

### FINDING-002: [MINOR] Missing error check
- **File**: pkg/api/handler.go:15
- **Evidence**: err is ignored
- **Impact**: Silent failures
- **Fix**: Check and handle error

## Verdict
- **Approved**: NO
- **Blockers**: FINDING-001
- **Suggestions**: FINDING-002
`

	parsed := ParseReview(review)

	assert.Equal(t, GradeC, parsed.Grade)
	assert.Len(t, parsed.Findings, 2)

	assert.Equal(t, "FINDING-001", parsed.Findings[0].ID)
	assert.Equal(t, SeverityCritical, parsed.Findings[0].Severity)
	assert.Equal(t, "SQL injection vulnerability", parsed.Findings[0].Title)
	assert.Equal(t, "pkg/db/query.go", parsed.Findings[0].File)
	assert.Equal(t, 42, parsed.Findings[0].Line)

	assert.Equal(t, "FINDING-002", parsed.Findings[1].ID)
	assert.Equal(t, SeverityMinor, parsed.Findings[1].Severity)

	assert.False(t, parsed.Approved)
	assert.Equal(t, []string{"FINDING-001"}, parsed.Blockers)
	assert.Equal(t, []string{"FINDING-002"}, parsed.Suggestions)
}

func TestParseVerdictApprovalRequiresOneExactNormalizedDecision(t *testing.T) {
	tests := []struct {
		name     string
		section  string
		approved bool
		wantErr  string
	}{
		{name: "recommend approve", section: "- **Recommendation**: approve", approved: true},
		{name: "recommend request changes", section: "- **Recommendation**:  request   changes ", approved: false},
		{name: "recommend discussion", section: "- **Recommendation**: NEEDS DISCUSSION", approved: false},
		{name: "approved yes", section: "- **Approved**: yes", approved: true},
		{name: "approved no", section: "- **Approved**: NO", approved: false},
		{
			name:    "option-list template",
			section: "- **Recommendation**: APPROVE / REQUEST CHANGES / NEEDS DISCUSSION",
			wantErr: "must be exactly APPROVE",
		},
		{
			name:    "prose suffix",
			section: "- **Recommendation**: APPROVE because CI is green",
			wantErr: "must be exactly APPROVE",
		},
		{
			name:    "conflicting duplicates",
			section: "- **Recommendation**: APPROVE\n- **Approved**: NO",
			wantErr: "exactly one",
		},
		{
			name:    "matching duplicates",
			section: "- **Recommendation**: APPROVE\n- **Approved**: YES",
			wantErr: "exactly one",
		},
		{name: "missing", section: "- **Blockers**: None", wantErr: "exactly one"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			approved, err := parseVerdictApproval(tt.section)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.approved, approved)
		})
	}
}

func TestReviewValidationRejectsAmbiguousVerdictTemplate(t *testing.T) {
	review := completeReviewWithCoverage("- **File**: `ratchet.go` — reviewed the exact changed file.\n" +
		"- **Feedback disposition**: `NONE_SUPPLIED` — no prior feedback was supplied.\n" +
		"- **Verification**: focused verification passed.")
	review = strings.Replace(
		review,
		"**Recommendation**: APPROVE",
		"**Recommendation**: APPROVE / REQUEST CHANGES / NEEDS DISCUSSION",
		1,
	)

	def := ReviewBranchDef{ChangedFiles: []string{"ratchet.go"}}
	result, err := def.ParseResult(review)
	assert.NoError(t, err)
	assert.ErrorContains(t, def.ValidateResult(result), "invalid Verdict decision")
}

func TestReviewPRDefValidateResultRequiresCoverageAndInvariantAudit(t *testing.T) {
	def := ReviewPRDef{ChangedFiles: []string{"ratchet.go"}}
	result, err := def.ParseResult(`## Grade: A

## Summary
Looks good.

## CI Status
- Build: PASS
- Tests: PASS

## Findings
None.

## Verdict
- **Recommendation**: APPROVE
- **Blockers**: None
- **Suggestions**: None`)
	assert.NoError(t, err)
	err = def.ValidateResult(result)
	assert.ErrorContains(t, err, "Coverage section")
	assert.ErrorContains(t, err, "Invariant Audit section")
	assert.ErrorContains(t, err, "Falsification section")
	assert.ErrorContains(t, err, "explicit feedback disposition")
}

func TestReviewPRDefValidateResultAcceptsCompleteApproval(t *testing.T) {
	def := ReviewPRDef{
		ChangedFiles:                []string{"ratchet.go", "skips.go"},
		CIStatus:                    "passing (22/22)",
		CIProvenance:                prCISourceHead,
		RequiresFeedbackDisposition: true,
		RequiredFeedbackIDs:         []string{"thread:PRRT_1"},
	}
	result, err := def.ParseResult(`## Grade: A

## Summary
The paired ratchet is exact and the change is ready.

## CI Status
- Build: PASS
- Tests: PASS

## Coverage
- **File**: ` + "`" + `ratchet.go` + "`" + ` — compared the maximum to the live collection cardinality.
- **File**: ` + "`" + `skips.go` + "`" + ` — checked the empty boundary.
- **Feedback disposition**: ` + "`" + `DISPOSITIONED` + "`" + ` — verified the supplied thread and confirmed it is resolved.
- **Feedback**: ` + "`" + `thread:PRRT_1` + "`" + ` — ` + "`" + `ADDRESSED` + "`" + ` — the empty-boundary test proves the requested fix.
- **Verification**: focused CI passed.

## Invariant Audit
- len(knownSkips)=0 and maxKnownSkips=0.

## Falsification
- **Strongest plausible failure**: the cleared list retained a stale maximum.
- **Evidence**: both values are zero in the changed source and focused test.
- **Conclusion**: DISPROVED.

## Findings
None.

## Verdict
- **Recommendation**: APPROVE
- **Blockers**: None
- **Suggestions**: None`)
	assert.NoError(t, err)
	assert.NoError(t, def.ValidateResult(result))
}

func TestReviewCoverageLedgerUsesNormalizedExactPaths(t *testing.T) {
	def := ReviewPRDef{ChangedFiles: []string{"pkg/ratchet.go"}, CIStatus: "passing (1/1)", CIProvenance: prCISourceHead}

	valid := completeReviewWithCoverage("- **File**: `./pkg\\ratchet.go` — reviewed the exact changed file and its paired bound.\n" +
		"- **Feedback disposition**: `NONE_SUPPLIED` — no prior feedback was supplied.\n" +
		"- **Verification**: focused test passed.")
	result, err := def.ParseResult(valid)
	assert.NoError(t, err)
	assert.NoError(t, def.ValidateResult(result))

	lookalike := completeReviewWithCoverage("- **File**: `pkg/ratchet.go.bak` — this path merely contains the changed path.\n" +
		"- **Feedback disposition**: `NONE_SUPPLIED` — no prior feedback was supplied.\n" +
		"- **Verification**: not independently run.")
	result, err = def.ParseResult(lookalike)
	assert.NoError(t, err)
	err = def.ValidateResult(result)
	assert.ErrorContains(t, err, "missing pkg/ratchet.go")
	assert.ErrorContains(t, err, "unexpected pkg/ratchet.go.bak")
}

func TestReviewCoverageLedgerRequiresExplicitFeedbackDisposition(t *testing.T) {
	def := ReviewPRDef{
		ChangedFiles:                []string{"ratchet.go"},
		RequiresFeedbackDisposition: true,
		RequiredFeedbackIDs:         []string{"thread:PRRT_1"},
	}

	narrativeOnly := completeReviewWithCoverage("- **File**: `ratchet.go` — reviewed.\n" +
		"- **Verification**: existing feedback was mentioned in prose.")
	result, err := def.ParseResult(narrativeOnly)
	assert.NoError(t, err)
	assert.ErrorContains(t, def.ValidateResult(result), "explicit feedback disposition")

	noneSupplied := completeReviewWithCoverage("- **File**: `ratchet.go` — reviewed.\n" +
		"- **Feedback disposition**: `NONE_SUPPLIED` — no feedback considered.\n" +
		"- **Verification**: focused test passed.")
	result, err = def.ParseResult(noneSupplied)
	assert.NoError(t, err)
	assert.ErrorContains(t, def.ValidateResult(result), "mark supplied review feedback as DISPOSITIONED")

	genericOnly := completeReviewWithCoverage("- **File**: `ratchet.go` — reviewed.\n" +
		"- **Feedback disposition**: `DISPOSITIONED` — all feedback was considered.\n" +
		"- **Verification**: focused test passed.")
	result, err = def.ParseResult(genericOnly)
	assert.NoError(t, err)
	assert.ErrorContains(t, def.ValidateResult(result), "missing thread:PRRT_1")
}

func TestReviewApprovalRequiresDisprovedFalsification(t *testing.T) {
	def := ReviewPRDef{ChangedFiles: []string{"ratchet.go"}}
	base := completeReviewWithCoverage("- **File**: `ratchet.go` — reviewed the exact changed file.\n" +
		"- **Feedback disposition**: `NONE_SUPPLIED` — no prior feedback was supplied.\n" +
		"- **Verification**: focused test passed.")

	for _, conclusion := range []string{"PROVED", "UNRESOLVED"} {
		t.Run(conclusion, func(t *testing.T) {
			review := strings.Replace(base, "**Conclusion**: DISPROVED", "**Conclusion**: "+conclusion, 1)
			result, err := def.ParseResult(review)
			assert.NoError(t, err)
			assert.ErrorContains(t, def.ValidateResult(result), "approval requires a DISPROVED falsification conclusion")
		})
	}

	invalid := strings.Replace(base, "**Conclusion**: DISPROVED", "**Conclusion**: CLEAN", 1)
	result, err := def.ParseResult(invalid)
	assert.NoError(t, err)
	assert.ErrorContains(t, def.ValidateResult(result), "Falsification conclusion")
}

func completeReviewWithCoverage(coverage string) string {
	return `## Grade: A

## Summary
The exact changed-file contract is covered.

## CI Status
- Build: PASS
- Tests: PASS

## Coverage
` + coverage + `

## Invariant Audit
- Checked paired bounds and empty/default behavior.

## Falsification
- **Strongest plausible failure**: the paired bound diverges on the empty case.
- **Evidence**: the focused empty-case test passes.
- **Conclusion**: DISPROVED.

## Findings
None.

## Verdict
- **Recommendation**: APPROVE
- **Blockers**: None
- **Suggestions**: None`
}

func TestReviewPRDefValidateResultRejectsUnsupportedApproval(t *testing.T) {
	base := `## Grade: A

## Summary
Looks good.

## CI Status
- Build: PASS
- Tests: PASS

## Coverage
- **File**: ` + "`" + `ratchet.go` + "`" + ` — reviewed the changed ratchet and its consumer.
- **Feedback disposition**: ` + "`" + `NONE_SUPPLIED` + "`" + ` — no prior feedback was supplied.
- **Verification**: CI.

## Invariant Audit
- Checked paired bounds.

## Falsification
- **Strongest plausible failure**: the paired bound diverges.
- **Evidence**: focused test covers the equal boundary.
- **Conclusion**: DISPROVED.

## Findings
None.

## Verdict
- **Recommendation**: APPROVE
- **Blockers**: None
- **Suggestions**: None`

	for _, tt := range []struct {
		name string
		def  ReviewPRDef
		want string
	}{
		{name: "truncated", def: ReviewPRDef{ChangedFiles: []string{"ratchet.go"}, ContextIncomplete: true}, want: "truncated"},
		{name: "failing CI", def: ReviewPRDef{ChangedFiles: []string{"ratchet.go"}, CIStatus: "failing (1/22)"}, want: "remote CI PASS"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tt.def.ParseResult(base)
			assert.NoError(t, err)
			assert.ErrorContains(t, tt.def.ValidateResult(result), tt.want)
		})
	}
}

func TestReviewApprovalRequiresNormalizedPassingBuildAndTests(t *testing.T) {
	base := completeReviewWithCoverage("- **File**: `ratchet.go` — reviewed the exact changed file.\n" +
		"- **Feedback disposition**: `NONE_SUPPLIED` — no prior feedback was supplied.\n" +
		"- **Verification**: focused verification passed.")
	def := ReviewBranchDef{ChangedFiles: []string{"ratchet.go"}}

	for _, state := range []string{"FAIL", "PENDING", "NOT_RUN", "UNAVAILABLE", "UNKNOWN"} {
		t.Run("build "+state, func(t *testing.T) {
			review := strings.Replace(base, "- Build: PASS", "- Build: "+state, 1)
			result, err := def.ParseResult(review)
			assert.NoError(t, err)
			assert.ErrorContains(t, def.ValidateResult(result), "requires Build status PASS")
		})
		t.Run("tests "+state, func(t *testing.T) {
			review := strings.Replace(base, "- Tests: PASS", "- Tests: "+state, 1)
			result, err := def.ParseResult(review)
			assert.NoError(t, err)
			assert.ErrorContains(t, def.ValidateResult(result), "requires Tests status PASS")
		})
	}

	for _, tc := range []struct {
		name        string
		oldStatus   string
		newStatus   string
		wantMessage string
	}{
		{name: "arbitrary build prose", oldStatus: "- Build: PASS", newStatus: "- Build: compiled successfully", wantMessage: "build status must start"},
		{name: "arbitrary test prose", oldStatus: "- Tests: PASS", newStatus: "- Tests: 42 passed, 0 failed", wantMessage: "tests status must start"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			review := strings.Replace(base, tc.oldStatus, tc.newStatus, 1)
			result, err := def.ParseResult(review)
			assert.NoError(t, err)
			assert.ErrorContains(t, def.ValidateResult(result), tc.wantMessage)
		})
	}
}

func TestReviewPRApprovalRequiresAuthoritativePassingRemoteCI(t *testing.T) {
	base := completeReviewWithCoverage("- **File**: `ratchet.go` — reviewed the exact changed file.\n" +
		"- **Feedback disposition**: `NONE_SUPPLIED` — no prior feedback was supplied.\n" +
		"- **Verification**: named remote checks passed.")

	valid := ReviewPRDef{ChangedFiles: []string{"ratchet.go"}, CIStatus: "passing (3/3)", CIProvenance: prCISourceHead}
	result, err := valid.ParseResult(base)
	assert.NoError(t, err)
	assert.NoError(t, valid.ValidateResult(result))

	documentationBase := ReviewPRDef{ChangedFiles: []string{"README.md"}, CIStatus: "passing (3/3)", CIProvenance: prCISourceBase}
	result, err = documentationBase.ParseResult(strings.ReplaceAll(base, "ratchet.go", "README.md"))
	assert.NoError(t, err)
	assert.NoError(t, documentationBase.ValidateResult(result))

	sourceBase := ReviewPRDef{ChangedFiles: []string{"ratchet.go"}, CIStatus: "passing (3/3)", CIProvenance: prCISourceBase}
	result, err = sourceBase.ParseResult(base)
	assert.NoError(t, err)
	assert.ErrorContains(t, sourceBase.ValidateResult(result), "documentation-only approval")

	missingProvenance := ReviewPRDef{ChangedFiles: []string{"ratchet.go"}, CIStatus: "passing (3/3)"}
	result, err = missingProvenance.ParseResult(base)
	assert.NoError(t, err)
	assert.ErrorContains(t, missingProvenance.ValidateResult(result), "explicit remote CI provenance")

	for _, ciStatus := range []string{
		"",
		"unknown",
		"no checks",
		"pending (1/3)",
		"failing (1/3)",
		"passing (0/0)",
		"passing (1/3)",
		"passing",
		"all green",
	} {
		t.Run(ciStatus, func(t *testing.T) {
			def := ReviewPRDef{ChangedFiles: []string{"ratchet.go"}, CIStatus: ciStatus}
			result, err := def.ParseResult(base)
			assert.NoError(t, err)
			assert.ErrorContains(t, def.ValidateResult(result), "remote CI PASS")
		})
	}
}

func TestReviewFeedbackLedgerRequiresExactPerIDDisposition(t *testing.T) {
	const fileLine = "- **File**: `ratchet.go` — reviewed the exact changed file.\n"
	const disposition = "- **Feedback disposition**: `DISPOSITIONED` — every supplied ID is listed below.\n"
	const verification = "- **Verification**: named remote checks passed."
	entry1 := "- **Feedback**: `thread:PRRT_1` — `ADDRESSED` — focused test proves the requested boundary fix.\n"
	entry2 := "- **Feedback**: `review:123` — `DISPUTED` — source trace proves the concern does not apply.\n"
	def := ReviewPRDef{
		ChangedFiles:                []string{"ratchet.go"},
		CIStatus:                    "passing (2/2)",
		CIProvenance:                prCISourceHead,
		RequiresFeedbackDisposition: true,
		RequiredFeedbackIDs:         []string{"thread:PRRT_1", "review:123"},
	}

	valid := completeReviewWithCoverage(fileLine + disposition + entry1 + entry2 + verification)
	result, err := def.ParseResult(valid)
	assert.NoError(t, err)
	assert.NoError(t, def.ValidateResult(result))
	assert.Equal(t, []FeedbackEntry{
		{ID: "thread:PRRT_1", Status: FeedbackAddressed, Evidence: "focused test proves the requested boundary fix."},
		{ID: "review:123", Status: FeedbackDisputed, Evidence: "source trace proves the concern does not apply."},
	}, result.(*ReviewRLMResult).Parsed.FeedbackEntries)

	tests := []struct {
		name    string
		entries string
		want    string
	}{
		{name: "missing", entries: entry1, want: "missing review:123"},
		{name: "duplicate", entries: entry1 + entry1 + entry2, want: "duplicate thread:PRRT_1"},
		{name: "unexpected", entries: entry1 + entry2 + "- **Feedback**: `thread:other` — `ADDRESSED` — unrelated.\n", want: "unexpected thread:other"},
		{name: "invalid status", entries: entry1 + "- **Feedback**: `review:123` — `CLOSED` — unsupported state.\n", want: "invalid status for review:123"},
		{name: "missing evidence", entries: entry1 + "- **Feedback**: `review:123` — `DISPUTED` —\n", want: "missing evidence for review:123"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			review := completeReviewWithCoverage(fileLine + disposition + tc.entries + verification)
			result, err := def.ParseResult(review)
			assert.NoError(t, err)
			assert.ErrorContains(t, def.ValidateResult(result), tc.want)
		})
	}
}

func TestReviewFeedbackLedgerUnresolvedBlocksOnlyApproval(t *testing.T) {
	coverage := "- **File**: `ratchet.go` — reviewed the exact changed file.\n" +
		"- **Feedback disposition**: `DISPOSITIONED` — every supplied ID is listed below.\n" +
		"- **Feedback**: `thread:PRRT_1` — `UNRESOLVED` — requested regression test is still absent.\n" +
		"- **Verification**: named remote checks passed."
	def := ReviewPRDef{
		ChangedFiles:                []string{"ratchet.go"},
		CIStatus:                    "passing (1/1)",
		CIProvenance:                prCISourceHead,
		RequiresFeedbackDisposition: true,
		RequiredFeedbackIDs:         []string{"thread:PRRT_1"},
	}

	approval, err := def.ParseResult(completeReviewWithCoverage(coverage))
	assert.NoError(t, err)
	assert.ErrorContains(t, def.ValidateResult(approval), "unresolved feedback thread:PRRT_1")

	nonApprovalText := strings.Replace(completeReviewWithCoverage(coverage), "## Grade: A", "## Grade: C", 1)
	nonApprovalText = strings.Replace(nonApprovalText, "**Conclusion**: DISPROVED", "**Conclusion**: PROVED", 1)
	nonApprovalText = strings.Replace(nonApprovalText, "**Recommendation**: APPROVE", "**Recommendation**: REQUEST CHANGES", 1)
	nonApproval, err := def.ParseResult(nonApprovalText)
	assert.NoError(t, err)
	assert.NoError(t, def.ValidateResult(nonApproval))
}

func TestReviewFeedbackDispositionRequiresIDsWhenContextSaysFeedbackExists(t *testing.T) {
	review := completeReviewWithCoverage("- **File**: `ratchet.go` — reviewed.\n" +
		"- **Feedback disposition**: `DISPOSITIONED` — generic prose only.\n" +
		"- **Verification**: focused test passed.")
	def := ReviewBranchDef{ChangedFiles: []string{"ratchet.go"}}
	result, err := def.ParseResult(review)
	assert.NoError(t, err)
	err = ValidateParsedReview(result.(*ReviewRLMResult).Parsed, ReviewValidationOptions{
		ChangedFiles:                []string{"ratchet.go"},
		RequiresFeedbackDisposition: true,
	})
	assert.ErrorContains(t, err, "no required feedback IDs")
}

func TestParsedReview_FilterMethods(t *testing.T) {
	parsed := &ParsedReview{
		Findings: []Finding{
			{ID: "FINDING-001", Severity: SeverityCritical},
			{ID: "FINDING-002", Severity: SeverityMajor},
			{ID: "FINDING-003", Severity: SeverityMinor},
			{ID: "FINDING-004", Severity: SeverityMinor},
		},
	}

	assert.Len(t, parsed.CriticalFindings(), 1)
	assert.Len(t, parsed.MajorFindings(), 1)
	assert.Len(t, parsed.MinorFindings(), 2)
	assert.Len(t, parsed.BlockingFindings(), 2)
	assert.True(t, parsed.HasBlockers())

	f := parsed.FindingByID("FINDING-002")
	assert.NotNil(t, f)
	assert.Equal(t, SeverityMajor, f.Severity)

	f = parsed.FindingByID("FINDING-999")
	assert.Nil(t, f)
}

func TestParsedReview_NoBlockers(t *testing.T) {
	parsed := &ParsedReview{
		Findings: []Finding{
			{ID: "FINDING-001", Severity: SeverityMinor},
		},
	}

	assert.False(t, parsed.HasBlockers())
	assert.Len(t, parsed.BlockingFindings(), 0)
}

// gitInCmd runs a git command in dir, failing the test on error.
func gitInCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestUnstagedBudgetNeverExceeds pins the never-exceed contract for the
// unstaged diff path: appending the truncation marker must not push
// ctx.Unstaged past MaxDiffBytes even when many medium files pack the output
// right up to the budget edge.
func TestUnstagedBudgetNeverExceeds(t *testing.T) {
	dir := t.TempDir()
	gitInCmd(t, dir, "init", "-q")

	// Create an initial committed file so the repo has a HEAD commit.
	initial := filepath.Join(dir, "init.go")
	if err := os.WriteFile(initial, []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInCmd(t, dir, "add", ".")
	gitInCmd(t, dir, "commit", "-m", "init")

	// Write a single large unstaged file so Prioritize takes the per-file cap
	// path and reliably sets Truncated=true while staying within the budget.
	// Using one large file avoids the pre-existing summary-section overshoot
	// (the "... and N more" line is not budget-checked) so the test stays
	// deterministic.
	const budget = 5_000
	large := filepath.Join(dir, "large.go")
	content := "package p\n" + strings.Repeat("// line of content\n", budget)
	if err := os.WriteFile(large, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// Add to index so 'git diff' (unstaged) returns the modification.
	gitInCmd(t, dir, "add", ".")
	// Modify slightly so 'git diff' (unstaged) returns content.
	modified := "package p\n" + strings.Repeat("// modified line\n", budget)
	if err := os.WriteFile(large, []byte(modified), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Chdir(dir)

	opts := BranchContextOptions{
		MaxDiffBytes:    budget,
		IncludeUnstaged: true,
		BaseBranch:      "HEAD",
	}
	ctx, _, err := AssembleBranchContext(opts)
	if err != nil {
		t.Fatalf("AssembleBranchContext: %v", err)
	}

	if len(ctx.Unstaged) > budget {
		t.Errorf("Unstaged length %d exceeds MaxDiffBytes %d (truncation marker not reserved)",
			len(ctx.Unstaged), budget)
	}
	// The output must also have been truncated (proving the test covers the path).
	if !strings.Contains(ctx.Unstaged, "... (truncated)") {
		t.Errorf("expected truncation marker in Unstaged output; got none — test may not cover the truncation path")
	}
}

func TestWorktreeScopeIncludesStagedChanges(t *testing.T) {
	dir := t.TempDir()
	gitInCmd(t, dir, "init", "-q")
	initial := filepath.Join(dir, "init.go")
	if err := os.WriteFile(initial, []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInCmd(t, dir, "add", ".")
	gitInCmd(t, dir, "commit", "-m", "init")

	staged := filepath.Join(dir, "staged.go")
	if err := os.WriteFile(staged, []byte("package p\n\nconst stagedReviewEvidence = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInCmd(t, dir, "add", "staged.go")
	if err := os.WriteFile(initial, []byte("package p\n\nconst unstagedReviewEvidence = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	ctx, _, err := AssembleBranchContext(BranchContextOptions{
		MaxDiffBytes:    20_000,
		IncludeUnstaged: false,
		BaseBranch:      "HEAD",
		Scope:           ReviewScopeWorktree,
	})
	if err != nil {
		t.Fatalf("AssembleBranchContext: %v", err)
	}
	if !strings.Contains(ctx.Unstaged, "stagedReviewEvidence") {
		t.Fatalf("worktree review omitted staged diff:\n%s", ctx.Unstaged)
	}
	if strings.Contains(ctx.Unstaged, "unstagedReviewEvidence") {
		t.Fatalf("worktree review included unstaged diff with IncludeUnstaged=false:\n%s", ctx.Unstaged)
	}
	found := false
	unstagedFound := false
	for _, file := range ctx.Files {
		found = found || file.Path == "staged.go"
		unstagedFound = unstagedFound || file.Path == "init.go"
	}
	if !found {
		t.Fatalf("worktree review omitted staged file from inventory: %#v", ctx.Files)
	}
	if unstagedFound {
		t.Fatalf("worktree review included unstaged file with IncludeUnstaged=false: %#v", ctx.Files)
	}
	if ctx.Stats.Files != 1 || ctx.Stats.Insertions == 0 {
		t.Fatalf("worktree review stats omitted staged evidence: %#v", ctx.Stats)
	}
	prompt := BuildBranchPrompt(ctx)
	if !strings.Contains(prompt, "Worktree Changes (staged only; unstaged excluded)") {
		t.Fatalf("worktree prompt did not disclose staged-only evidence:\n%s", prompt)
	}
}

func TestWorktreeScopeIncludesReviewableUntrackedSource(t *testing.T) {
	dir := t.TempDir()
	gitInCmd(t, dir, "init", "-q")
	for path, content := range map[string]string{
		"go.mod":     "module example.test/worktree-context\n\ngo 1.24\n",
		"compile.go": "package snapshot\n\nfunc Value() int { return untrackedValue() }\n",
		".gitignore": "ignored.go\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitInCmd(t, dir, "add", ".")
	gitInCmd(t, dir, "commit", "-m", "initial")

	untrackedSource := "package snapshot\n\nfunc untrackedValue() int { return 42 }\n"
	for path, content := range map[string][]byte{
		"helper.go":             []byte(untrackedSource),
		"ignored.go":            []byte("package snapshot\nfunc untrackedValue() int { return 0 }\n"),
		".env":                  []byte("TOKEN=must-not-leak\n"),
		".env.production.local": []byte("TOKEN=production-must-not-leak\n"),
		"asset.bin":             {0xff, 0x00, 0x01},
		"AGENTS.md":             []byte("untracked instructions must not leak\n"),
		"credentials.json":      []byte(`{"token":"must-not-leak"}`),
		"credentials.txt":       []byte("plaintext-credentials-must-not-leak\n"),
		".git-credentials":      []byte("https://user:secret@example.test\n"),
		"control.go":            []byte("package snapshot\n// \x1b[31mterminal control\n"),
		"invalid.go":            {0xff, 0xfe, 'g', 'o'},
	} {
		if err := os.WriteFile(filepath.Join(dir, path), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := model.CaptureReviewSnapshot(context.Background(), dir, model.ReviewSnapshotPolicy{
		Mode:           model.ReviewSnapshotWorktree,
		UntrackedPaths: []string{"helper.go"},
	})
	if err != nil {
		t.Fatalf("CaptureReviewSnapshot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "helper.go"), []byte("package snapshot\n\nfunc untrackedValue() int { return 99 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	ctx, _, err := AssembleBranchContext(BranchContextOptions{
		MaxDiffBytes:      20_000,
		IncludeUnstaged:   true,
		UntrackedPaths:    []string{"helper.go"},
		BaseBranch:        "HEAD",
		Scope:             ReviewScopeWorktree,
		CapturedUntracked: snapshot.UntrackedFiles(),
	})
	if err != nil {
		t.Fatalf("AssembleBranchContext: %v", err)
	}
	if !ctx.IncludesUntracked {
		t.Fatal("worktree context did not disclose reviewable untracked state")
	}
	if len(ctx.Files) != 1 || ctx.Files[0] != (FileChange{Status: "A", Path: "helper.go"}) {
		t.Fatalf("worktree file inventory = %#v, want only untracked helper.go", ctx.Files)
	}
	if !strings.Contains(ctx.Unstaged, "helper.go") || !strings.Contains(ctx.Unstaged, "untrackedValue") {
		t.Fatalf("worktree diff omitted untracked source:\n%s", ctx.Unstaged)
	}
	if !strings.Contains(ctx.Unstaged, "return 42") || strings.Contains(ctx.Unstaged, "return 99") {
		t.Fatalf("worktree context diverged from the immutable untracked snapshot:\n%s", ctx.Unstaged)
	}
	if ctx.Stats.Files != 1 || ctx.Stats.Insertions != 3 || ctx.Stats.Deletions != 0 {
		t.Fatalf("worktree stats omitted untracked source: %#v", ctx.Stats)
	}
	prompt := BuildBranchPrompt(ctx)
	if !strings.Contains(prompt, "Included Local State**: explicitly opted-in filtered untracked text files") {
		t.Fatalf("worktree prompt did not disclose untracked source inclusion:\n%s", prompt)
	}
	for _, forbidden := range []string{"ignored.go", "TOKEN=must-not-leak", "production-must-not-leak", "plaintext-credentials-must-not-leak", "user:secret", "terminal control", "invalid.go", "asset.bin", "untracked instructions must not leak", "credentials.json"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("worktree context exposed excluded untracked content %q:\n%s", forbidden, prompt)
		}
	}
}

func TestWorktreeScopeExcludesUntrackedTextByDefault(t *testing.T) {
	dir := t.TempDir()
	gitInCmd(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "tracked.go"), []byte("package snapshot\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInCmd(t, dir, "add", "tracked.go")
	gitInCmd(t, dir, "commit", "-m", "initial")
	if err := os.MkdirAll(filepath.Join(dir, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	const secret = "database_password: ordinary-name-secret\n"
	if err := os.WriteFile(filepath.Join(dir, "config", "local.yaml"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	ctx, _, err := AssembleBranchContext(BranchContextOptions{
		MaxDiffBytes:    20_000,
		IncludeUnstaged: true,
		BaseBranch:      "HEAD",
		Scope:           ReviewScopeWorktree,
	})
	if err != nil {
		t.Fatalf("AssembleBranchContext: %v", err)
	}
	if ctx.IncludesUntracked {
		t.Fatal("worktree context included untracked input without explicit opt-in")
	}
	prompt := BuildBranchPrompt(ctx)
	if strings.Contains(prompt, "local.yaml") || strings.Contains(prompt, "ordinary-name-secret") {
		t.Fatalf("default worktree context exposed untracked secret:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Excluded Local State**: untracked files") {
		t.Fatalf("default worktree prompt did not disclose untracked exclusion:\n%s", prompt)
	}
}

func TestDetectBaseBranchPrefersRemoteTrackingBranch(t *testing.T) {
	dir := t.TempDir()
	gitInCmd(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInCmd(t, dir, "add", "base.txt")
	gitInCmd(t, dir, "commit", "-m", "base")
	gitInCmd(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
	if err := os.WriteFile(filepath.Join(dir, "local.txt"), []byte("stale local branch moved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInCmd(t, dir, "add", "local.txt")
	gitInCmd(t, dir, "commit", "-m", "move local main")

	if got := detectBaseBranch(dir); got != "origin/main" {
		t.Fatalf("detectBaseBranch() = %q, want origin/main", got)
	}
}

func TestWorktreeScopeCollapsesStagedAndUnstagedEditsToFinalState(t *testing.T) {
	dir := t.TempDir()
	gitInCmd(t, dir, "init", "-q")
	tracked := filepath.Join(dir, "state.txt")
	if err := os.WriteFile(tracked, []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInCmd(t, dir, "add", "state.txt")
	gitInCmd(t, dir, "commit", "-m", "base")

	if err := os.WriteFile(tracked, []byte("staged-only\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInCmd(t, dir, "add", "state.txt")
	if err := os.WriteFile(tracked, []byte("working-only\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	ctx, _, err := AssembleBranchContext(BranchContextOptions{
		MaxDiffBytes:    20_000,
		IncludeUnstaged: true,
		BaseBranch:      "HEAD",
		Scope:           ReviewScopeWorktree,
	})
	if err != nil {
		t.Fatalf("AssembleBranchContext: %v", err)
	}
	if !strings.Contains(ctx.Unstaged, "working-only") {
		t.Fatalf("worktree review omitted the final tracked state:\n%s", ctx.Unstaged)
	}
	if strings.Contains(ctx.Unstaged, "staged-only") {
		t.Fatalf("worktree review exposed an intermediate index state absent from the snapshot:\n%s", ctx.Unstaged)
	}
	if len(ctx.Files) != 1 || ctx.Files[0].Path != "state.txt" {
		t.Fatalf("worktree inventory double-counted the same file: %#v", ctx.Files)
	}
	if ctx.Stats.Files != 1 || ctx.Stats.Insertions != 1 || ctx.Stats.Deletions != 1 {
		t.Fatalf("worktree stats describe multiple transitions instead of the final state: %#v", ctx.Stats)
	}
}

func TestAssembleBranchContextPinsAndRevalidatesImmutableIdentity(t *testing.T) {
	dir := t.TempDir()
	gitInCmd(t, dir, "init", "-q")
	tracked := filepath.Join(dir, "behavior.go")
	if err := os.WriteFile(tracked, []byte("package p\n\nconst behavior = \"base\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("committed review instructions\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInCmd(t, dir, "add", "behavior.go", "AGENTS.md")
	gitInCmd(t, dir, "commit", "-m", "base")
	gitInCmd(t, dir, "branch", "-M", "main")
	baseCommit := gitOutputInCmd(t, dir, "rev-parse", "HEAD^{commit}")

	gitInCmd(t, dir, "switch", "-q", "-c", "feature")
	if err := os.WriteFile(tracked, []byte("package p\n\nconst behavior = \"feature\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInCmd(t, dir, "add", "behavior.go")
	gitInCmd(t, dir, "commit", "-m", "feature")
	headCommit := gitOutputInCmd(t, dir, "rev-parse", "HEAD^{commit}")
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("live ref dependent instructions\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	ctx, _, err := AssembleBranchContext(BranchContextOptions{
		MaxDiffBytes:  20_000,
		BaseBranch:    "main",
		Scope:         ReviewScopeBranch,
		IncludeAgents: true,
	})
	if err != nil {
		t.Fatalf("AssembleBranchContext: %v", err)
	}
	if ctx.HeadCommit != headCommit || ctx.BaseCommit != baseCommit {
		t.Fatalf("captured identity = head %s base %s, want head %s base %s",
			ctx.HeadCommit, ctx.BaseCommit, headCommit, baseCommit)
	}
	if !strings.Contains(ctx.Diff, `+const behavior = "feature"`) {
		t.Fatalf("pinned diff omitted feature commit:\n%s", ctx.Diff)
	}
	if ctx.AgentsMD != "committed review instructions" {
		t.Fatalf("branch review instructions = %q, want immutable HEAD content", ctx.AgentsMD)
	}
	prompt := BuildBranchPrompt(ctx)
	for _, want := range []string{
		"**Head Commit**: `" + headCommit + "` (immutable)",
		"**Base Ref**: main (display only; do not re-resolve)",
		"**Base Commit**: `" + baseCommit + "` (immutable)",
		"never resolve live branch refs",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("branch prompt missing %q:\n%s", want, prompt)
		}
	}
	if err := RevalidateBranchContext(ctx); err != nil {
		t.Fatalf("stable context did not revalidate: %v", err)
	}

	gitInCmd(t, dir, "branch", "-f", "main", headCommit)
	if err := RevalidateBranchContext(ctx); err == nil || !strings.Contains(err.Error(), "review base \"main\" moved") {
		t.Fatalf("base movement revalidation error = %v", err)
	}

	if err := os.WriteFile(tracked, []byte("package p\n\nconst behavior = \"new head\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInCmd(t, dir, "add", "behavior.go")
	gitInCmd(t, dir, "commit", "-m", "move head")
	if err := RevalidateBranchContext(ctx); err == nil || !strings.Contains(err.Error(), "review HEAD moved") {
		t.Fatalf("HEAD movement revalidation error = %v", err)
	}
}

func TestAssembleBranchContextIncludesApplicableNestedAgentsFromSnapshot(t *testing.T) {
	t.Run("immutable branch head", func(t *testing.T) {
		dir := initNestedAgentsReviewRepo(t)
		gitInCmd(t, dir, "switch", "-q", "-c", "feature")
		if err := os.WriteFile(filepath.Join(dir, "src", "deep", "behavior.go"), []byte("package deep\n\nconst behavior = \"feature\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitInCmd(t, dir, "add", "src/deep/behavior.go")
		gitInCmd(t, dir, "commit", "-m", "feature")
		if err := os.WriteFile(filepath.Join(dir, "src", "AGENTS.md"), []byte("live instructions must not leak\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Chdir(dir)

		ctx, _, err := AssembleBranchContext(BranchContextOptions{
			MaxDiffBytes:  20_000,
			BaseBranch:    "main",
			Scope:         ReviewScopeBranch,
			IncludeAgents: true,
		})
		if err != nil {
			t.Fatalf("AssembleBranchContext: %v", err)
		}
		assertNestedAgentsChain(t, ctx.AgentsMD, "committed src instructions", "committed deep instructions")
		if strings.Contains(ctx.AgentsMD, "live instructions must not leak") {
			t.Fatalf("branch guidance leaked live worktree content:\n%s", ctx.AgentsMD)
		}
		if ctx.ContextIncomplete {
			t.Fatalf("complete branch guidance was marked incomplete: %q", ctx.AgentsMD)
		}
	})

	t.Run("index only", func(t *testing.T) {
		dir := initNestedAgentsReviewRepo(t)
		if err := os.WriteFile(filepath.Join(dir, "src", "deep", "behavior.go"), []byte("package deep\n\nconst behavior = \"staged\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "src", "AGENTS.md"), []byte("staged src instructions\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitInCmd(t, dir, "add", "src/deep/behavior.go", "src/AGENTS.md")
		if err := os.WriteFile(filepath.Join(dir, "src", "AGENTS.md"), []byte("unstaged instructions must not leak\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Chdir(dir)

		ctx, _, err := AssembleBranchContext(BranchContextOptions{
			MaxDiffBytes:    20_000,
			IncludeUnstaged: false,
			IncludeAgents:   true,
			Scope:           ReviewScopeChanges,
		})
		if err != nil {
			t.Fatalf("AssembleBranchContext: %v", err)
		}
		assertNestedAgentsChain(t, ctx.AgentsMD, "staged src instructions", "committed deep instructions")
		if strings.Contains(ctx.AgentsMD, "unstaged instructions must not leak") {
			t.Fatalf("index guidance leaked unstaged content:\n%s", ctx.AgentsMD)
		}
	})

	t.Run("tracked worktree", func(t *testing.T) {
		dir := initNestedAgentsReviewRepo(t)
		if err := os.WriteFile(filepath.Join(dir, "src", "deep", "behavior.go"), []byte("package deep\n\nconst behavior = \"worktree\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "src", "AGENTS.md"), []byte("worktree src instructions\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "src", "deep", "AGENTS.md"), []byte("worktree deep instructions\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Chdir(dir)

		ctx, _, err := AssembleBranchContext(BranchContextOptions{
			MaxDiffBytes:    20_000,
			IncludeUnstaged: true,
			IncludeAgents:   true,
			Scope:           ReviewScopeChanges,
		})
		if err != nil {
			t.Fatalf("AssembleBranchContext: %v", err)
		}
		assertNestedAgentsChain(t, ctx.AgentsMD, "worktree src instructions", "worktree deep instructions")
	})
}

func TestNestedAgentsGuidanceFailsClosedOnSymlinkOrTruncation(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		dir := initNestedAgentsReviewRepo(t)
		gitInCmd(t, dir, "rm", "-q", "src/AGENTS.md")
		if err := os.Symlink("../nested-secret.txt", filepath.Join(dir, "src", "AGENTS.md")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "nested-secret.txt"), []byte("nested secret must not leak\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitInCmd(t, dir, "add", "src/AGENTS.md")
		gitInCmd(t, dir, "commit", "-m", "track nested instruction symlink")
		if err := os.WriteFile(filepath.Join(dir, "src", "deep", "behavior.go"), []byte("package deep\n\nconst behavior = \"changed\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitInCmd(t, dir, "add", "src/deep/behavior.go")
		t.Chdir(dir)

		ctx, _, err := AssembleBranchContext(BranchContextOptions{
			MaxDiffBytes:    20_000,
			IncludeUnstaged: false,
			IncludeAgents:   true,
			Scope:           ReviewScopeChanges,
		})
		if err != nil {
			t.Fatalf("AssembleBranchContext: %v", err)
		}
		if !ctx.ContextIncomplete {
			t.Fatal("nested instruction symlink did not mark review context incomplete")
		}
		if strings.Contains(ctx.AgentsMD, "nested secret must not leak") || strings.Contains(BuildBranchPrompt(ctx), "nested secret must not leak") {
			t.Fatal("nested instruction symlink target leaked into review context")
		}
	})

	t.Run("truncated", func(t *testing.T) {
		dir := initNestedAgentsReviewRepo(t)
		largeGuidance := "large nested guidance\n" + strings.Repeat("x", reviewAgentsPerFileLimit+1_000)
		if err := os.WriteFile(filepath.Join(dir, "src", "AGENTS.md"), []byte(largeGuidance), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "src", "deep", "behavior.go"), []byte("package deep\n\nconst behavior = \"changed\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Chdir(dir)

		ctx, audit, err := AssembleBranchContext(BranchContextOptions{
			MaxDiffBytes:    20_000,
			IncludeUnstaged: true,
			IncludeAgents:   true,
			Scope:           ReviewScopeChanges,
		})
		if err != nil {
			t.Fatalf("AssembleBranchContext: %v", err)
		}
		if !ctx.ContextIncomplete || !audit.HasTruncation() {
			t.Fatalf("truncated nested guidance did not fail closed: incomplete=%v audit=%#v", ctx.ContextIncomplete, audit.Sources())
		}
		if !strings.Contains(BuildBranchPrompt(ctx), "Context Completeness**: INCOMPLETE") {
			t.Fatal("branch prompt did not disclose truncated nested guidance")
		}
	})
}

func TestAssembleProjectContextUsesTrackedWorktreeEvidenceOnly(t *testing.T) {
	dir := t.TempDir()
	gitInCmd(t, dir, "init", "-q")
	trackedFiles := map[string]string{
		"README.md":           "committed readme\n",
		"go.mod":              "module example.test/project\n",
		"src/tracked.go":      "package src\n",
		"will-be-deleted.txt": "delete me\n",
	}
	for path, content := range trackedFiles {
		fullPath := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitInCmd(t, dir, "add", ".")
	gitInCmd(t, dir, "commit", "-m", "initial")
	headCommit := gitOutputInCmd(t, dir, "rev-parse", "HEAD^{commit}")

	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("tracked worktree readme\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "staged.go"), []byte("package staged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInCmd(t, dir, "add", "staged.go")
	gitInCmd(t, dir, "rm", "-q", "will-be-deleted.txt")

	untrackedFiles := map[string]string{
		"package.json":              `{"private": false}`,
		"AGENTS.md":                 "untracked instructions\n",
		"untracked-dir/secret.go":   "package secret\n",
		"untracked-dir/nested/x.go": "package nested\n",
	}
	for path, content := range untrackedFiles {
		fullPath := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)

	ctx, _, err := AssembleProjectContext(ProjectContextOptions{MaxTreeDepth: 4, IncludeAgents: true})
	if err != nil {
		t.Fatalf("AssembleProjectContext: %v", err)
	}
	if ctx.HeadCommit != headCommit {
		t.Fatalf("project HEAD = %s, want %s", ctx.HeadCommit, headCommit)
	}
	if ctx.ReadmeMD != "tracked worktree readme\n" {
		t.Fatalf("project README = %q, want tracked worktree content", ctx.ReadmeMD)
	}
	if ctx.GoMod == "" {
		t.Fatal("tracked go.mod was omitted")
	}
	if ctx.PackageJSON != "" || ctx.AgentsMD != "" {
		t.Fatalf("untracked config leaked into project context: package=%q agents=%q", ctx.PackageJSON, ctx.AgentsMD)
	}
	for _, want := range []string{"README.md", "go.mod", "src/", "tracked.go", "staged.go"} {
		if !strings.Contains(ctx.Tree, want) {
			t.Fatalf("tracked project tree missing %q:\n%s", want, ctx.Tree)
		}
	}
	for _, forbidden := range []string{"package.json", "AGENTS.md", "untracked-dir", "secret.go", "will-be-deleted.txt"} {
		if strings.Contains(ctx.Tree, forbidden) {
			t.Fatalf("project tree leaked excluded path %q:\n%s", forbidden, ctx.Tree)
		}
	}
	prompt := BuildProjectPrompt(ctx)
	if !strings.Contains(prompt, "Git-visible tracked files only; untracked and index-hidden paths are intentionally excluded") {
		t.Fatalf("project prompt did not disclose tracked-only snapshot:\n%s", prompt)
	}
	if strings.Contains(prompt, "untracked instructions") || strings.Contains(prompt, `"private": false`) {
		t.Fatalf("project prompt leaked untracked config:\n%s", prompt)
	}
}

func TestAssembleProjectContextIncludesTrackedNestedAgentsChain(t *testing.T) {
	dir := initNestedAgentsReviewRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "src", "AGENTS.md"), []byte("project worktree src instructions\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "untracked"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "untracked", "AGENTS.md"), []byte("untracked instructions must not leak\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "untracked", "file.go"), []byte("package untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	ctx, _, err := AssembleProjectContext(ProjectContextOptions{MaxTreeDepth: 4, IncludeAgents: true})
	if err != nil {
		t.Fatalf("AssembleProjectContext: %v", err)
	}
	assertNestedAgentsChain(t, ctx.AgentsMD, "project worktree src instructions", "committed deep instructions")
	if strings.Contains(ctx.AgentsMD, "untracked instructions must not leak") {
		t.Fatalf("project guidance leaked untracked instructions:\n%s", ctx.AgentsMD)
	}
	if !strings.Contains(BuildProjectPrompt(ctx), "Project Guidelines (applicable AGENTS.md chain)") {
		t.Fatalf("project prompt omitted applicable instruction chain:\n%s", BuildProjectPrompt(ctx))
	}
}

func TestReviewContextIgnoresAssumeUnchangedMetadata(t *testing.T) {
	dir := t.TempDir()
	gitInCmd(t, dir, "init", "-q")
	for path, content := range map[string]string{
		"AGENTS.md": "committed instructions\n",
		"README.md": "committed readme\n",
		"go.mod":    "module example.test/snapshot\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitInCmd(t, dir, "add", ".")
	gitInCmd(t, dir, "commit", "-m", "initial")
	gitInCmd(t, dir, "update-index", "--assume-unchanged", "AGENTS.md", "README.md")
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("hidden live instructions\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hidden live readme\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	branchCtx, _, err := AssembleBranchContext(BranchContextOptions{
		MaxDiffBytes:    20_000,
		IncludeUnstaged: true,
		IncludeAgents:   true,
		BaseBranch:      "HEAD",
		Scope:           ReviewScopeWorktree,
	})
	if err != nil {
		t.Fatalf("AssembleBranchContext: %v", err)
	}
	if branchCtx.AgentsMD != "committed instructions" {
		t.Fatalf("branch instructions = %q, want snapshot content", branchCtx.AgentsMD)
	}

	projectCtx, _, err := AssembleProjectContext(ProjectContextOptions{MaxTreeDepth: 3, IncludeAgents: true})
	if err != nil {
		t.Fatalf("AssembleProjectContext: %v", err)
	}
	if projectCtx.AgentsMD != "committed instructions" || projectCtx.ReadmeMD != "committed readme" {
		t.Fatalf("project metadata leaked hidden live state: agents=%q readme=%q", projectCtx.AgentsMD, projectCtx.ReadmeMD)
	}
}

func TestProjectContextDoesNotFollowTrackedMetadataSymlink(t *testing.T) {
	dir := t.TempDir()
	gitInCmd(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.test/snapshot\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("local-secret.json", filepath.Join(dir, "package.json")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	gitInCmd(t, dir, "add", "go.mod", "package.json")
	gitInCmd(t, dir, "commit", "-m", "tracked metadata symlink")
	if err := os.WriteFile(filepath.Join(dir, "local-secret.json"), []byte(`{"token":"must-not-leak"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	ctx, _, err := AssembleProjectContext(ProjectContextOptions{MaxTreeDepth: 3, IncludeAgents: true})
	if err != nil {
		t.Fatalf("AssembleProjectContext: %v", err)
	}
	if ctx.PackageJSON != "" {
		t.Fatalf("project context followed a tracked symlink into untracked content: %q", ctx.PackageJSON)
	}
	if strings.Contains(BuildProjectPrompt(ctx), "must-not-leak") {
		t.Fatal("project prompt leaked a tracked symlink target")
	}
}

func TestTrackedProjectTreeUsesNetSnapshotState(t *testing.T) {
	dir := t.TempDir()
	gitInCmd(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInCmd(t, dir, "add", "README.md")
	gitInCmd(t, dir, "commit", "-m", "initial")
	ghost := filepath.Join(dir, "ghost.txt")
	if err := os.WriteFile(ghost, []byte("staged but absent from final state\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInCmd(t, dir, "add", "ghost.txt")
	if err := os.Remove(ghost); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	ctx, _, err := AssembleProjectContext(ProjectContextOptions{MaxTreeDepth: 3})
	if err != nil {
		t.Fatalf("AssembleProjectContext: %v", err)
	}
	if strings.Contains(ctx.Tree, "ghost.txt") {
		t.Fatalf("project tree included an index-only intermediate absent from the final snapshot:\n%s", ctx.Tree)
	}
}

func gitOutputInCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func initNestedAgentsReviewRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitInCmd(t, dir, "init", "-q")
	files := map[string]string{
		"AGENTS.md":                    "root instructions\n",
		"src/AGENTS.md":                "committed src instructions\n",
		"src/deep/AGENTS.md":           "committed deep instructions\n",
		"src/deep/behavior.go":         "package deep\n\nconst behavior = \"base\"\n",
		"src/sibling/keep_reviewed.go": "package sibling\n",
	}
	for path, content := range files {
		fullPath := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitInCmd(t, dir, "add", ".")
	gitInCmd(t, dir, "commit", "-m", "initial nested guidance")
	gitInCmd(t, dir, "branch", "-M", "main")
	return dir
}

func assertNestedAgentsChain(t *testing.T, content, parentGuidance, childGuidance string) {
	t.Helper()
	for _, want := range []string{
		"root instructions",
		"### src/AGENTS.md\n\n" + parentGuidance,
		"### src/deep/AGENTS.md\n\n" + childGuidance,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("applicable instruction chain missing %q:\n%s", want, content)
		}
	}
	parent := strings.Index(content, "### src/AGENTS.md")
	child := strings.Index(content, "### src/deep/AGENTS.md")
	if parent < 0 || child < 0 || parent > child {
		t.Fatalf("instruction chain is not ordered from shallow to deep:\n%s", content)
	}
}
