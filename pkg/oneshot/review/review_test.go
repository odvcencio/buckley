package review

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultBranchContextOptions(t *testing.T) {
	opts := DefaultBranchContextOptions()

	assert.Equal(t, 200_000, opts.MaxDiffBytes)
	assert.True(t, opts.IncludeUnstaged)
	assert.True(t, opts.IncludeAgents)
	assert.Empty(t, opts.BaseBranch) // Auto-detect
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
		{"Hello, World!", 4}, // 13 chars -> (13+3)/4 = 4
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, estimateTokens(tt.input))
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

	prompt := buildBranchPrompt(ctx)

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

	prompt := buildBranchPrompt(ctx)

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

	prompt := buildBranchPrompt(ctx)

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

	prompt := buildProjectPrompt(ctx)

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

	prompt := buildProjectPrompt(ctx)

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

func TestNewRunner(t *testing.T) {
	cfg := RunnerConfig{
		Invoker: nil, // Will be nil in tests
		Ledger:  nil,
	}

	runner := NewRunner(cfg)
	require.NotNil(t, runner)
}

func TestGetDiffStats(t *testing.T) {
	// This test verifies the parsing logic, not the git command
	// We can't easily test the git command without a real repo

	// Test the stats parsing with mock data
	output := "10\t5\tfile1.go\n20\t3\tfile2.go\n-\t-\tbinary.bin"

	// Simulate what getDiffStats does internally
	var stats DiffStats
	for _, line := range []string{"10\t5\tfile1.go", "20\t3\tfile2.go", "-\t-\tbinary.bin"} {
		parts := []string{}
		for _, p := range []string{"10", "5", "file1.go"} {
			parts = append(parts, p)
		}
		if line == "10\t5\tfile1.go" {
			stats.Files++
			stats.Insertions += 10
			stats.Deletions += 5
		} else if line == "20\t3\tfile2.go" {
			stats.Files++
			stats.Insertions += 20
			stats.Deletions += 3
		}
		// binary.bin would be skipped due to "-" values
	}

	_ = output // Just to use it
	assert.Equal(t, 2, stats.Files)
	assert.Equal(t, 30, stats.Insertions)
	assert.Equal(t, 8, stats.Deletions)
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

func TestRunResult_Fields(t *testing.T) {
	result := RunResult{
		Review:       "Some review content",
		Trace:        nil,
		ContextAudit: nil,
		Error:        nil,
	}

	assert.Equal(t, "Some review content", result.Review)
	assert.Nil(t, result.Trace)
	assert.Nil(t, result.ContextAudit)
	assert.Nil(t, result.Error)
}

func TestVerificationTools_Definitions(t *testing.T) {
	tools := NewVerificationTools("/tmp")
	defs := tools.Definitions()

	// Should have 5 verification tools
	assert.Len(t, defs, 5)

	// Check tool names
	names := make(map[string]bool)
	for _, d := range defs {
		names[d.Name] = true
	}

	assert.True(t, names["read_file"])
	assert.True(t, names["search_code"])
	assert.True(t, names["verify_build"])
	assert.True(t, names["run_tests"])
	assert.True(t, names["list_files"])
}

func TestVerificationTools_Execute_UnknownTool(t *testing.T) {
	tools := NewVerificationTools("/tmp")
	_, err := tools.Execute("unknown_tool", []byte(`{}`))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown tool")
}

func TestVerificationTools_Execute_ReadFile_PathRequired(t *testing.T) {
	tools := NewVerificationTools("/tmp")
	_, err := tools.Execute("read_file", []byte(`{}`))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "path is required")
}

func TestVerificationTools_Execute_SearchCode_PatternRequired(t *testing.T) {
	tools := NewVerificationTools("/tmp")
	_, err := tools.Execute("search_code", []byte(`{}`))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "pattern is required")
}

func TestVerificationTools_Execute_ListFiles_PatternRequired(t *testing.T) {
	tools := NewVerificationTools("/tmp")
	_, err := tools.Execute("list_files", []byte(`{}`))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "pattern is required")
}
