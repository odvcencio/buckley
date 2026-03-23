package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultBranchContextOptions(t *testing.T) {
	opts := DefaultBranchContextOptions()

	assert.Equal(t, 200_000, opts.MaxDiffBytes)
	assert.True(t, opts.IncludeUnstaged)
	assert.True(t, opts.IncludeAgents)
	assert.Empty(t, opts.BaseBranch)
}

func TestDefaultProjectContextOptions(t *testing.T) {
	opts := DefaultProjectContextOptions()

	assert.Equal(t, 3, opts.MaxTreeDepth)
	assert.True(t, opts.IncludeAgents)
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
	assert.Contains(t, prompt, "/home/user/project")
	assert.Contains(t, prompt, "feature/new-feature")
	assert.Contains(t, prompt, "main")
	assert.Contains(t, prompt, "Files Changed")
	assert.Contains(t, prompt, "2 files, +50/-10 lines")
	assert.Contains(t, prompt, "file1.go")
	assert.Contains(t, prompt, "file2.go")
	assert.Contains(t, prompt, "Full Diff")
	assert.Contains(t, prompt, "Commits on this Branch")
	assert.Contains(t, prompt, "abc123 Some commit")
}

func TestBuildBranchPrompt_WithUnstaged(t *testing.T) {
	ctx := &BranchContext{
		RepoRoot:   "/home/user/project",
		Branch:     "main",
		BaseBranch: "main",
		Diff:       "some diff",
		Unstaged:   "unstaged changes here",
	}

	prompt := BuildBranchPrompt(ctx)

	assert.Contains(t, prompt, "Unstaged Changes")
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

	assert.Contains(t, prompt, "Project Guidelines (AGENTS.md)")
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
	assert.Contains(t, prompt, "/home/user/project")
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
		Tree:        "tree",
		GoMod:       "gomod",
		PackageJSON: "pkg",
		ReadmeMD:    "readme",
		AgentsMD:    "agents",
		RecentLog:   "log",
	}

	assert.Equal(t, "/root", ctx.RepoRoot)
	assert.Equal(t, "main", ctx.Branch)
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

func TestReviewBranchDef_Interface(t *testing.T) {
	def := ReviewBranchDef{}

	assert.Equal(t, "review", def.Name())
	assert.NotEmpty(t, def.SystemPrompt())
	assert.Contains(t, def.AllowedTools(), "read")
	assert.Contains(t, def.AllowedTools(), "bash")
	assert.Contains(t, def.AllowedTools(), "write")

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
	assert.Contains(t, def.AllowedTools(), "read")
	assert.NotContains(t, def.AllowedTools(), "write")
}

func TestReviewPRDef_Interface(t *testing.T) {
	def := ReviewPRDef{}

	assert.Equal(t, "review-pr", def.Name())
	assert.NotEmpty(t, def.SystemPrompt())
	assert.Contains(t, def.AllowedTools(), "bash")
	assert.NotContains(t, def.AllowedTools(), "write")
}

func TestFixFindingDef_Interface(t *testing.T) {
	def := FixFindingDef{}

	assert.Equal(t, "fix-finding", def.Name())
	assert.NotEmpty(t, def.SystemPrompt())
	assert.Contains(t, def.AllowedTools(), "write")

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
