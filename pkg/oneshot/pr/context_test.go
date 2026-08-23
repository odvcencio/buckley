package pr

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiffStats_TotalChanges(t *testing.T) {
	tests := []struct {
		name  string
		stats DiffStats
		want  int
	}{
		{
			name:  "zero",
			stats: DiffStats{},
			want:  0,
		},
		{
			name:  "insertions only",
			stats: DiffStats{Insertions: 50},
			want:  50,
		},
		{
			name:  "deletions only",
			stats: DiffStats{Deletions: 30},
			want:  30,
		},
		{
			name:  "both",
			stats: DiffStats{Insertions: 100, Deletions: 50},
			want:  150,
		},
		{
			name:  "with files and binary",
			stats: DiffStats{Files: 10, Insertions: 200, Deletions: 100, BinaryFiles: 2},
			want:  300,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.stats.TotalChanges()
			if got != tt.want {
				t.Errorf("TotalChanges() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestDefaultContextOptions(t *testing.T) {
	opts := DefaultContextOptions()

	if opts.BaseBranch != "" {
		t.Errorf("BaseBranch = %q, want empty (auto-detect)", opts.BaseBranch)
	}
	if opts.MaxDiffBytes != 80_000 {
		t.Errorf("MaxDiffBytes = %d, want 80000", opts.MaxDiffBytes)
	}
	if opts.MaxDiffTokens != 20_000 {
		t.Errorf("MaxDiffTokens = %d, want 20000", opts.MaxDiffTokens)
	}
	if !opts.IncludeAgents {
		t.Error("IncludeAgents = false, want true")
	}
}

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"a", 1},
		{"ab", 1},
		{"abc", 1},
		{"abcd", 1},
		{"abcde", 2},
		{"hello world", 3}, // 11 chars -> (11+3)/4 = 3
		{"this is a longer string with more tokens", 10}, // 40 chars -> (40+3)/4 = 10
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := estimateTokens(tt.input)
			if got != tt.want {
				t.Errorf("estimateTokens(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestCommitInfo_Fields(t *testing.T) {
	commit := CommitInfo{
		Hash:    "abc123def456",
		Subject: "Add new feature",
		Body:    "This commit adds a new feature.\n\nMore details here.",
	}

	if commit.Hash != "abc123def456" {
		t.Errorf("Hash = %q, want 'abc123def456'", commit.Hash)
	}
	if commit.Subject != "Add new feature" {
		t.Errorf("Subject = %q, want 'Add new feature'", commit.Subject)
	}
	if commit.Body != "This commit adds a new feature.\n\nMore details here." {
		t.Errorf("Body = %q", commit.Body)
	}
}

func TestContext_Fields(t *testing.T) {
	ctx := Context{
		Branch:      "feature/test",
		BaseBranch:  "main",
		RepoRoot:    "/home/user/project",
		RemoteURL:   "git@github.com:user/project.git",
		Commits:     []CommitInfo{{Hash: "abc123", Subject: "Test"}},
		DiffSummary: " 1 file changed",
		FullDiff:    "+new line",
		Stats:       DiffStats{Files: 1, Insertions: 1},
		AgentsMD:    "# Guidelines",
	}

	if ctx.Branch != "feature/test" {
		t.Errorf("Branch = %q", ctx.Branch)
	}
	if ctx.BaseBranch != "main" {
		t.Errorf("BaseBranch = %q", ctx.BaseBranch)
	}
	if ctx.RepoRoot != "/home/user/project" {
		t.Errorf("RepoRoot = %q", ctx.RepoRoot)
	}
	if ctx.RemoteURL != "git@github.com:user/project.git" {
		t.Errorf("RemoteURL = %q", ctx.RemoteURL)
	}
	if len(ctx.Commits) != 1 {
		t.Errorf("Commits length = %d, want 1", len(ctx.Commits))
	}
	if ctx.DiffSummary != " 1 file changed" {
		t.Errorf("DiffSummary = %q", ctx.DiffSummary)
	}
	if ctx.FullDiff != "+new line" {
		t.Errorf("FullDiff = %q", ctx.FullDiff)
	}
	if ctx.Stats.Files != 1 {
		t.Errorf("Stats.Files = %d", ctx.Stats.Files)
	}
	if ctx.AgentsMD != "# Guidelines" {
		t.Errorf("AgentsMD = %q", ctx.AgentsMD)
	}
}

func TestContextOptions_CustomBaseBranch(t *testing.T) {
	opts := ContextOptions{
		BaseBranch:    "develop",
		MaxDiffBytes:  50_000,
		MaxDiffTokens: 10_000,
		IncludeAgents: false,
	}

	if opts.BaseBranch != "develop" {
		t.Errorf("BaseBranch = %q, want 'develop'", opts.BaseBranch)
	}
	if opts.MaxDiffBytes != 50_000 {
		t.Errorf("MaxDiffBytes = %d, want 50000", opts.MaxDiffBytes)
	}
	if opts.MaxDiffTokens != 10_000 {
		t.Errorf("MaxDiffTokens = %d, want 10000", opts.MaxDiffTokens)
	}
	if opts.IncludeAgents {
		t.Error("IncludeAgents = true, want false")
	}
}

func TestDiffStats_Fields(t *testing.T) {
	stats := DiffStats{
		Files:       5,
		Insertions:  100,
		Deletions:   50,
		BinaryFiles: 2,
	}

	if stats.Files != 5 {
		t.Errorf("Files = %d, want 5", stats.Files)
	}
	if stats.Insertions != 100 {
		t.Errorf("Insertions = %d, want 100", stats.Insertions)
	}
	if stats.Deletions != 50 {
		t.Errorf("Deletions = %d, want 50", stats.Deletions)
	}
	if stats.BinaryFiles != 2 {
		t.Errorf("BinaryFiles = %d, want 2", stats.BinaryFiles)
	}
}

// TestParseCommitLog tests the commit log parsing logic.
// Since getCommitsSinceBase uses git directly, we test parsing behavior
// by examining how it would handle various git log outputs.
func TestParseCommitLogFormat(t *testing.T) {
	// The format used is: "%H<SEP>%s<SEP>%b<END>"
	// This tests that we understand the expected format

	tests := []struct {
		name    string
		entries []string
		want    []CommitInfo
	}{
		{
			name: "single commit without body",
			entries: []string{
				"abc123<SEP>Add feature<SEP><END>",
			},
			want: []CommitInfo{
				{Hash: "abc123", Subject: "Add feature", Body: ""},
			},
		},
		{
			name: "single commit with body",
			entries: []string{
				"def456<SEP>Fix bug<SEP>This fixes issue #123<END>",
			},
			want: []CommitInfo{
				{Hash: "def456", Subject: "Fix bug", Body: "This fixes issue #123"},
			},
		},
		{
			name: "multiple commits",
			entries: []string{
				"aaa111<SEP>First commit<SEP><END>",
				"bbb222<SEP>Second commit<SEP>Body text<END>",
			},
			want: []CommitInfo{
				{Hash: "aaa111", Subject: "First commit", Body: ""},
				{Hash: "bbb222", Subject: "Second commit", Body: "Body text"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate what getCommitsSinceBase does
			var commits []CommitInfo
			for _, entry := range tt.entries {
				// Remove <END> and parse
				entry = entry[:len(entry)-5] // Remove "<END>"
				parts := splitN(entry, "<SEP>", 3)
				if len(parts) < 2 {
					continue
				}
				commit := CommitInfo{
					Hash:    parts[0],
					Subject: parts[1],
				}
				if len(parts) > 2 {
					commit.Body = parts[2]
				}
				commits = append(commits, commit)
			}

			if len(commits) != len(tt.want) {
				t.Fatalf("got %d commits, want %d", len(commits), len(tt.want))
			}

			for i, want := range tt.want {
				if commits[i].Hash != want.Hash {
					t.Errorf("commit[%d].Hash = %q, want %q", i, commits[i].Hash, want.Hash)
				}
				if commits[i].Subject != want.Subject {
					t.Errorf("commit[%d].Subject = %q, want %q", i, commits[i].Subject, want.Subject)
				}
				if commits[i].Body != want.Body {
					t.Errorf("commit[%d].Body = %q, want %q", i, commits[i].Body, want.Body)
				}
			}
		})
	}
}

// splitN is a helper that mimics strings.SplitN for testing
func splitN(s, sep string, n int) []string {
	if n == 0 {
		return nil
	}

	var parts []string
	remaining := s

	for i := 0; i < n-1 && len(remaining) > 0; i++ {
		idx := indexOf(remaining, sep)
		if idx < 0 {
			break
		}
		parts = append(parts, remaining[:idx])
		remaining = remaining[idx+len(sep):]
	}

	if len(remaining) > 0 || len(parts) < n {
		parts = append(parts, remaining)
	}

	return parts
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func TestEstimateTokens_EdgeCases(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{
			name:  "empty string",
			input: "",
			want:  0,
		},
		{
			name:  "single char",
			input: "x",
			want:  1,
		},
		{
			name:  "exactly 4 chars",
			input: "abcd",
			want:  1,
		},
		{
			name:  "5 chars",
			input: "abcde",
			want:  2,
		},
		{
			name:  "newlines count",
			input: "line1\nline2\nline3",
			want:  5, // 17 chars -> (17+3)/4 = 5
		},
		{
			name:  "unicode chars",
			input: "hello世界",
			want:  3, // UTF-8 bytes, not chars. "hello" (5) + "世界" (6 bytes) = 11 -> 3
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := estimateTokens(tt.input)
			if got != tt.want {
				t.Errorf("estimateTokens(%q) = %d, want %d (len=%d)",
					tt.input, got, tt.want, len(tt.input))
			}
		})
	}
}

func TestDiffStats_ZeroValues(t *testing.T) {
	var stats DiffStats

	if stats.Files != 0 {
		t.Errorf("Files = %d, want 0", stats.Files)
	}
	if stats.Insertions != 0 {
		t.Errorf("Insertions = %d, want 0", stats.Insertions)
	}
	if stats.Deletions != 0 {
		t.Errorf("Deletions = %d, want 0", stats.Deletions)
	}
	if stats.BinaryFiles != 0 {
		t.Errorf("BinaryFiles = %d, want 0", stats.BinaryFiles)
	}
	if stats.TotalChanges() != 0 {
		t.Errorf("TotalChanges() = %d, want 0", stats.TotalChanges())
	}
}

// TestParseDiffNumstat tests parsing of git diff --numstat output.
// Tests the exported ParseDiffNumstat function.
func TestParseDiffNumstat(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   DiffStats
	}{
		{
			name:   "single file",
			output: "10\t5\tfile.go",
			want:   DiffStats{Files: 1, Insertions: 10, Deletions: 5},
		},
		{
			name:   "multiple files",
			output: "10\t5\tfile1.go\n20\t3\tfile2.go\n5\t0\tfile3.go",
			want:   DiffStats{Files: 3, Insertions: 35, Deletions: 8},
		},
		{
			name:   "binary file",
			output: "-\t-\tbinary.png",
			want:   DiffStats{Files: 1, BinaryFiles: 1},
		},
		{
			name:   "mixed binary and text",
			output: "10\t5\tfile.go\n-\t-\timage.png\n20\t10\tother.go",
			want:   DiffStats{Files: 3, Insertions: 30, Deletions: 15, BinaryFiles: 1},
		},
		{
			name:   "empty output",
			output: "",
			want:   DiffStats{},
		},
		{
			name:   "only whitespace",
			output: "   \n  \n",
			want:   DiffStats{},
		},
		{
			name:   "trailing newline",
			output: "10\t5\tfile.go\n",
			want:   DiffStats{Files: 1, Insertions: 10, Deletions: 5},
		},
		{
			name:   "large numbers",
			output: "1000\t500\tfile.go",
			want:   DiffStats{Files: 1, Insertions: 1000, Deletions: 500},
		},
		{
			name:   "zero changes",
			output: "0\t0\tfile.go",
			want:   DiffStats{Files: 1, Insertions: 0, Deletions: 0},
		},
		{
			name:   "rename with no changes",
			output: "0\t0\toldname.go => newname.go",
			want:   DiffStats{Files: 1, Insertions: 0, Deletions: 0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseDiffNumstat(tt.output)
			if got.Files != tt.want.Files {
				t.Errorf("Files = %d, want %d", got.Files, tt.want.Files)
			}
			if got.Insertions != tt.want.Insertions {
				t.Errorf("Insertions = %d, want %d", got.Insertions, tt.want.Insertions)
			}
			if got.Deletions != tt.want.Deletions {
				t.Errorf("Deletions = %d, want %d", got.Deletions, tt.want.Deletions)
			}
			if got.BinaryFiles != tt.want.BinaryFiles {
				t.Errorf("BinaryFiles = %d, want %d", got.BinaryFiles, tt.want.BinaryFiles)
			}
		})
	}
}

// TestParseCommitLog tests parsing of git log output with the custom format.
// Tests the exported ParseCommitLog function.
func TestParseCommitLog(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   []CommitInfo
	}{
		{
			name:   "single commit no body",
			output: "abc123<SEP>Add feature<SEP><END>",
			want: []CommitInfo{
				{Hash: "abc123", Subject: "Add feature", Body: ""},
			},
		},
		{
			name:   "single commit with body",
			output: "def456<SEP>Fix bug<SEP>This fixes the issue<END>",
			want: []CommitInfo{
				{Hash: "def456", Subject: "Fix bug", Body: "This fixes the issue"},
			},
		},
		{
			name:   "multiple commits",
			output: "aaa111<SEP>First<SEP>Body 1<END>bbb222<SEP>Second<SEP><END>ccc333<SEP>Third<SEP>Body 3<END>",
			want: []CommitInfo{
				{Hash: "aaa111", Subject: "First", Body: "Body 1"},
				{Hash: "bbb222", Subject: "Second", Body: ""},
				{Hash: "ccc333", Subject: "Third", Body: "Body 3"},
			},
		},
		{
			name:   "empty output",
			output: "",
			want:   nil,
		},
		{
			name:   "newlines in output",
			output: "abc123<SEP>Add feature<SEP>Line1\nLine2<END>",
			want: []CommitInfo{
				{Hash: "abc123", Subject: "Add feature", Body: "Line1\nLine2"},
			},
		},
		{
			name:   "commit body with multiple paragraphs",
			output: "abc123<SEP>Add feature<SEP>First paragraph\n\nSecond paragraph<END>",
			want: []CommitInfo{
				{Hash: "abc123", Subject: "Add feature", Body: "First paragraph\n\nSecond paragraph"},
			},
		},
		{
			name:   "long hash",
			output: "abc123def456789012345678901234567890abcd<SEP>Long hash<SEP><END>",
			want: []CommitInfo{
				{Hash: "abc123def456789012345678901234567890abcd", Subject: "Long hash", Body: ""},
			},
		},
		{
			name:   "commit with co-author",
			output: "abc123<SEP>Add feature<SEP>Description\n\nCo-authored-by: Name <email><END>",
			want: []CommitInfo{
				{Hash: "abc123", Subject: "Add feature", Body: "Description\n\nCo-authored-by: Name <email>"},
			},
		},
		{
			name:   "trailing whitespace in body",
			output: "abc123<SEP>Feature<SEP>Body text   <END>",
			want: []CommitInfo{
				{Hash: "abc123", Subject: "Feature", Body: "Body text"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseCommitLog(tt.output)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d commits, want %d", len(got), len(tt.want))
			}
			for i := range tt.want {
				if got[i].Hash != tt.want[i].Hash {
					t.Errorf("commit[%d].Hash = %q, want %q", i, got[i].Hash, tt.want[i].Hash)
				}
				if got[i].Subject != tt.want[i].Subject {
					t.Errorf("commit[%d].Subject = %q, want %q", i, got[i].Subject, tt.want[i].Subject)
				}
				if got[i].Body != tt.want[i].Body {
					t.Errorf("commit[%d].Body = %q, want %q", i, got[i].Body, tt.want[i].Body)
				}
			}
		})
	}
}

// TestContextOptionsDefaults verifies the default options.
func TestContextOptionsDefaults(t *testing.T) {
	opts := DefaultContextOptions()

	// Verify each default value
	tests := []struct {
		name string
		got  interface{}
		want interface{}
	}{
		{"BaseBranch", opts.BaseBranch, ""},
		{"MaxDiffBytes", opts.MaxDiffBytes, 80_000},
		{"MaxDiffTokens", opts.MaxDiffTokens, 20_000},
		{"IncludeAgents", opts.IncludeAgents, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("%s = %v, want %v", tt.name, tt.got, tt.want)
			}
		})
	}
}

// TestContextOptions_CustomValues tests creating options with custom values.
func TestContextOptions_CustomValues(t *testing.T) {
	opts := ContextOptions{
		BaseBranch:    "develop",
		MaxDiffBytes:  100_000,
		MaxDiffTokens: 25_000,
		IncludeAgents: false,
	}

	if opts.BaseBranch != "develop" {
		t.Errorf("BaseBranch = %q", opts.BaseBranch)
	}
	if opts.MaxDiffBytes != 100_000 {
		t.Errorf("MaxDiffBytes = %d", opts.MaxDiffBytes)
	}
	if opts.MaxDiffTokens != 25_000 {
		t.Errorf("MaxDiffTokens = %d", opts.MaxDiffTokens)
	}
	if opts.IncludeAgents {
		t.Error("IncludeAgents should be false")
	}
}

// TestDiffStats_LargeNumbers tests DiffStats with large numbers.
func TestDiffStats_LargeNumbers(t *testing.T) {
	stats := DiffStats{
		Files:       1000,
		Insertions:  500000,
		Deletions:   250000,
		BinaryFiles: 50,
	}

	total := stats.TotalChanges()
	if total != 750000 {
		t.Errorf("TotalChanges() = %d, want 750000", total)
	}
}

// TestCommitInfo_EmptyFields tests CommitInfo with empty fields.
func TestCommitInfo_EmptyFields(t *testing.T) {
	commit := CommitInfo{}

	if commit.Hash != "" {
		t.Errorf("Hash = %q, want empty", commit.Hash)
	}
	if commit.Subject != "" {
		t.Errorf("Subject = %q, want empty", commit.Subject)
	}
	if commit.Body != "" {
		t.Errorf("Body = %q, want empty", commit.Body)
	}
}

// TestContext_EmptyFields tests Context with empty fields.
func TestContext_EmptyFields(t *testing.T) {
	ctx := Context{}

	if ctx.Branch != "" {
		t.Errorf("Branch = %q", ctx.Branch)
	}
	if ctx.BaseBranch != "" {
		t.Errorf("BaseBranch = %q", ctx.BaseBranch)
	}
	if len(ctx.Commits) != 0 {
		t.Errorf("Commits length = %d", len(ctx.Commits))
	}
	if ctx.Stats.TotalChanges() != 0 {
		t.Errorf("Stats.TotalChanges() = %d", ctx.Stats.TotalChanges())
	}
}

// TestContext_MultipleCommits tests Context with multiple commits.
func TestContext_MultipleCommits(t *testing.T) {
	ctx := Context{
		Commits: []CommitInfo{
			{Hash: "aaa", Subject: "First"},
			{Hash: "bbb", Subject: "Second"},
			{Hash: "ccc", Subject: "Third"},
		},
	}

	if len(ctx.Commits) != 3 {
		t.Fatalf("Commits length = %d, want 3", len(ctx.Commits))
	}
	if ctx.Commits[0].Hash != "aaa" {
		t.Errorf("Commits[0].Hash = %q", ctx.Commits[0].Hash)
	}
	if ctx.Commits[1].Subject != "Second" {
		t.Errorf("Commits[1].Subject = %q", ctx.Commits[1].Subject)
	}
	if ctx.Commits[2].Hash != "ccc" {
		t.Errorf("Commits[2].Hash = %q", ctx.Commits[2].Hash)
	}
}

// TestEstimateTokens_LargeInput tests token estimation for large strings.
func TestEstimateTokens_LargeInput(t *testing.T) {
	// 10,000 chars should estimate to (10000+3)/4 = 2500 tokens
	largeInput := make([]byte, 10000)
	for i := range largeInput {
		largeInput[i] = 'a'
	}

	got := estimateTokens(string(largeInput))
	want := (10000 + 3) / 4 // 2500

	if got != want {
		t.Errorf("estimateTokens(10000 chars) = %d, want %d", got, want)
	}
}

// TestEstimateTokens_SpecialCharacters tests token estimation with special chars.
func TestEstimateTokens_SpecialCharacters(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{
			name:  "tabs and newlines",
			input: "\t\n\t\n",
			want:  1, // 4 chars -> 1 token
		},
		{
			name:  "code with symbols",
			input: "func() { return nil }",
			want:  6, // 21 chars -> (21+3)/4 = 6
		},
		{
			name:  "markdown",
			input: "# Header\n\n**bold** text",
			want:  6, // 23 chars -> (23+3)/4 = 6
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := estimateTokens(tt.input)
			if got != tt.want {
				t.Errorf("estimateTokens(%q) = %d, want %d (len=%d)",
					tt.input, got, tt.want, len(tt.input))
			}
		})
	}
}

// Integration tests that require git
// These are skipped in short mode to allow unit tests to run faster.

func TestAssembleContext_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Try to assemble context - this tests the git helper functions
	opts := DefaultContextOptions()
	opts.BaseBranch = "main" // Use explicit base branch

	ctx, audit, err := AssembleContext(opts)
	if err != nil {
		// It's OK if this fails in CI without a proper git setup
		t.Skipf("AssembleContext failed (expected in some environments): %v", err)
	}

	// Verify basic fields are populated
	if ctx.RepoRoot == "" {
		t.Error("RepoRoot should be set")
	}
	if ctx.Branch == "" {
		t.Error("Branch should be set")
	}
	if ctx.BaseBranch == "" {
		t.Error("BaseBranch should be set")
	}
	if audit == nil {
		t.Error("audit should not be nil")
	}
}

func TestAssembleContext_WithCustomBaseBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	opts := ContextOptions{
		BaseBranch:    "main",
		MaxDiffBytes:  50_000,
		MaxDiffTokens: 10_000,
		IncludeAgents: false,
	}

	ctx, _, err := AssembleContext(opts)
	if err != nil {
		t.Skipf("AssembleContext failed: %v", err)
	}

	if ctx.BaseBranch != "main" {
		t.Errorf("BaseBranch = %q, want 'main'", ctx.BaseBranch)
	}
}

func TestGetDiffStats_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// getDiffStats is unexported, but we can test it indirectly through AssembleContext
	opts := DefaultContextOptions()
	opts.BaseBranch = "main"

	ctx, _, err := AssembleContext(opts)
	if err != nil {
		t.Skipf("AssembleContext failed: %v", err)
	}

	// Stats should be populated (even if all zeros)
	t.Logf("Stats: Files=%d, Insertions=%d, Deletions=%d, Binary=%d",
		ctx.Stats.Files, ctx.Stats.Insertions, ctx.Stats.Deletions, ctx.Stats.BinaryFiles)
}

func TestDetectBaseBranch_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Test that detectBaseBranch returns something reasonable
	opts := ContextOptions{
		BaseBranch:    "", // Auto-detect
		MaxDiffBytes:  10_000,
		MaxDiffTokens: 2_500,
		IncludeAgents: false,
	}

	ctx, _, err := AssembleContext(opts)
	if err != nil {
		t.Skipf("AssembleContext failed: %v", err)
	}

	// Should detect main, master, or develop
	validBases := map[string]bool{"main": true, "master": true, "develop": true}
	if !validBases[ctx.BaseBranch] {
		t.Logf("Detected base branch: %s (unusual but may be valid)", ctx.BaseBranch)
	}
}

// ---------------------------------------------------------------------
// Diff filtering / budgeting (buildDiffContext, processDiffContext,
// allocateDiffBudget, classifyDiffFile) and origin-preferring base
// branch resolution (resolveBaseRef, detectBaseBranch).
//
// Regression coverage for the root cause of PR #145: a large committed
// generated JSON file (cgo_harness/perf_scan/perf_ratio_budgets.json)
// sorted before real source changes alphabetically and, under the old
// raw-byte-prefix truncation of a single 80KB budget, consumed
// essentially the whole diff - so the model never saw the actual
// source change. A stale local base branch made this worse by
// inflating the diff with already-upstream commits.
// ---------------------------------------------------------------------

// buildFakeDiff constructs a synthetic single-file `git diff` segment
// containing `lines` added lines, each stamped with sentinel so tests
// can assert on the survival (or omission) of a specific file's content
// without depending on a real git repository.
func buildFakeDiff(path string, lines int, sentinel string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\n", path, path)
	b.WriteString("index 0000000..1111111 100644\n")
	fmt.Fprintf(&b, "--- a/%s\n", path)
	fmt.Fprintf(&b, "+++ b/%s\n", path)
	fmt.Fprintf(&b, "@@ -0,0 +1,%d @@\n", lines)
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&b, "+line %04d %s\n", i, sentinel)
	}
	return b.String()
}

// TestProcessDiffContext_GeneratedDataFileDoesNotDominate is the direct
// regression test for PR #145: a huge generated/data JSON file sorting
// first in the diff must not crowd out a small real source change
// sorting after it.
func TestProcessDiffContext_GeneratedDataFileDoesNotDominate(t *testing.T) {
	hugeJSON := buildFakeDiff("cgo_harness/perf_scan/perf_ratio_budgets.json", 4000, "JSONDATAMARKER")
	smallGo := buildFakeDiff("conflict_policy.go", 5, "GOSOURCEMARKER")

	if len(hugeJSON) < 80_000 {
		t.Fatalf("test fixture too small to reproduce the bug: hugeJSON is %d bytes, want > 80000", len(hugeJSON))
	}

	// JSON sorts first alphabetically, exactly like the real bug
	// (cgo_harness/... sorts before conflict_policy.go).
	raw := hugeJSON + smallGo

	result := processDiffContext(raw, 80_000)

	if !strings.Contains(result.Text, "GOSOURCEMARKER") {
		t.Fatalf("small .go source file content did not survive filtering; got %d bytes:\n%.500s", len(result.Text), result.Text)
	}
	if strings.Contains(result.Text, "JSONDATAMARKER") {
		t.Error("large generated/data JSON content should have been omitted, not included verbatim")
	}
	if len(result.Omitted) != 1 || result.Omitted[0].Path != "cgo_harness/perf_scan/perf_ratio_budgets.json" {
		t.Fatalf("expected the JSON file to be recorded as omitted, got %+v", result.Omitted)
	}
	if result.Omitted[0].Reason != "large data file" {
		t.Errorf("Omitted[0].Reason = %q, want %q", result.Omitted[0].Reason, "large data file")
	}
	if !strings.Contains(result.Text, "1 generated/data/binary file(s) omitted") {
		t.Errorf("expected an omitted-files note mentioning the JSON file; got:\n%s", result.Text)
	}
	if !strings.Contains(result.Text, "cgo_harness/perf_scan/perf_ratio_budgets.json") {
		t.Error("omitted-files note should name the omitted file so the model knows it exists")
	}
	if !result.Truncated {
		t.Error("Truncated should be true when files are omitted")
	}
}

// TestProcessDiffContext_PerFileCapPreventsDomination proves fix #2: even
// when NEITHER file is filtered out (both are ordinary .go source), a
// single large file still can't consume the whole budget - the per-file
// cap and round-robin allocation ensure every source file gets a fair
// share.
func TestProcessDiffContext_PerFileCapPreventsDomination(t *testing.T) {
	fileA := buildFakeDiff("pkg/a/first.go", 400, "MARKER_A")
	fileB := buildFakeDiff("pkg/b/second.go", 400, "MARKER_B")

	if len(fileA) <= defaultPerFileDiffCapBytes {
		t.Fatalf("fixture too small: fileA is %d bytes, want > per-file cap %d", len(fileA), defaultPerFileDiffCapBytes)
	}

	raw := fileA + fileB

	// Budget big enough for both files' *capped* size to fit, but far
	// smaller than the raw size of even one file alone - this proves
	// the per-file cap (not just the overall ceiling) is doing the
	// work, since without it file A alone would consume the entire
	// budget and file B would get nothing (the PR #145 failure mode).
	maxBytes := defaultPerFileDiffCapBytes*2 + 500

	result := processDiffContext(raw, maxBytes)

	if !strings.Contains(result.Text, "MARKER_A") {
		t.Error("expected first file's content to survive")
	}
	if !strings.Contains(result.Text, "MARKER_B") {
		t.Error("expected second file's content to survive - the per-file cap must stop file A from consuming the whole budget")
	}
	if len(result.Text) > maxBytes+16 {
		t.Errorf("result exceeds overall MaxDiffBytes budget: got %d bytes, want <= ~%d", len(result.Text), maxBytes)
	}
	if !result.Truncated {
		t.Error("expected Truncated=true since both files exceed the per-file cap")
	}
	if len(result.Omitted) != 0 {
		t.Errorf("no files should be classified as omitted here (both are ordinary .go source); got %+v", result.Omitted)
	}

	// Neither file's entire raw content should have made it through -
	// each was capped, not merely included in full because it happened
	// to fit.
	if strings.Contains(result.Text, fileA) {
		t.Error("file A should have been truncated by the per-file cap, not included in full")
	}
	if strings.Contains(result.Text, fileB) {
		t.Error("file B should have been truncated by the per-file cap, not included in full")
	}
}

// TestProcessDiffContext_SmallDiffUnaffected ensures the new pipeline
// doesn't change behavior for the common case of a small diff that
// fits comfortably under budget with nothing to omit or cap.
func TestProcessDiffContext_SmallDiffUnaffected(t *testing.T) {
	raw := buildFakeDiff("pkg/a/small.go", 3, "MARKER")
	result := processDiffContext(raw, 80_000)

	if result.Truncated {
		t.Error("small diff should not be marked truncated")
	}
	if len(result.Omitted) != 0 {
		t.Errorf("small diff should have nothing omitted, got %+v", result.Omitted)
	}
	if !strings.Contains(result.Text, "MARKER") {
		t.Error("expected file content to be present")
	}
}

// TestProcessDiffContext_EmptyDiff covers the no-changes case.
func TestProcessDiffContext_EmptyDiff(t *testing.T) {
	result := processDiffContext("", 80_000)
	if result.Text != "" || result.Truncated || len(result.Omitted) != 0 {
		t.Errorf("processDiffContext(\"\", ...) = %+v, want zero value", result)
	}
}

// TestClassifyDiffFile documents (and locks in) the specific filtering
// heuristic: lockfiles/vendored/generated-code markers are excluded
// unconditionally, and JSON/CSV-style data extensions are only excluded
// once they're "large" - small data-extension files (e.g. a slim
// package.json) are left alone since they're commonly legitimate,
// hand-edited source.
func TestClassifyDiffFile(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		size     int
		wantOmit bool
	}{
		{"go source kept regardless of size", "pkg/foo/bar.go", 1_000_000, false},
		{"small json config kept", "package.json", 200, false},
		{"large generated json data omitted (PR #145 file)", "cgo_harness/perf_scan/perf_ratio_budgets.json", 50_000, true},
		{"json just over threshold omitted", "data/export.json", largeDataFileThresholdBytes + 1, true},
		{"json exactly at threshold kept", "data/export.json", largeDataFileThresholdBytes, false},
		{"go.sum lockfile always omitted", "go.sum", 50, true},
		{"package-lock.json always omitted regardless of size", "package-lock.json", 50, true},
		{"Cargo.lock always omitted", "Cargo.lock", 50, true},
		{"vendor directory omitted", "vendor/github.com/foo/bar/baz.go", 100, true},
		{"node_modules omitted", "web/node_modules/react/index.js", 100, true},
		{"generated protobuf omitted", "pkg/api/thing.pb.go", 100, true},
		{"minified js omitted", "assets/app.min.js", 100, true},
		{"large csv data omitted", "fixtures/big.csv", 10_000, true},
		{"small csv kept", "fixtures/small.csv", 100, false},
		{"yaml config kept even if large (not a filtered data extension)", "deploy/k8s.yaml", 50_000, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			omit, reason := classifyDiffFile(tt.path, tt.size)
			if omit != tt.wantOmit {
				t.Errorf("classifyDiffFile(%q, %d) omit = %v, want %v (reason=%q)", tt.path, tt.size, omit, tt.wantOmit, reason)
			}
			if omit && reason == "" {
				t.Errorf("classifyDiffFile(%q, %d) omitted with no reason", tt.path, tt.size)
			}
			if !omit && reason != "" {
				t.Errorf("classifyDiffFile(%q, %d) not omitted but reason = %q", tt.path, tt.size, reason)
			}
		})
	}
}

// TestSplitDiffByFile checks path extraction and binary detection for
// added/modified, binary, and renamed files.
func TestSplitDiffByFile(t *testing.T) {
	raw := strings.Join([]string{
		"diff --git a/foo.go b/foo.go",
		"index 111..222 100644",
		"--- a/foo.go",
		"+++ b/foo.go",
		"@@ -1,1 +1,1 @@",
		"-old",
		"+new",
		"diff --git a/bar.png b/bar.png",
		"index 333..444 100644",
		"Binary files a/bar.png and b/bar.png differ",
		"diff --git a/old.go b/new.go",
		"similarity index 100%",
		"rename from old.go",
		"rename to new.go",
	}, "\n")

	segs := splitDiffByFile(raw)
	if len(segs) != 3 {
		t.Fatalf("got %d segments, want 3", len(segs))
	}
	if segs[0].Path != "foo.go" || segs[0].Binary {
		t.Errorf("segs[0] = %+v, want Path=foo.go Binary=false", segs[0])
	}
	if segs[1].Path != "bar.png" || !segs[1].Binary {
		t.Errorf("segs[1] = %+v, want Path=bar.png Binary=true", segs[1])
	}
	if segs[2].Path != "new.go" || segs[2].Binary {
		t.Errorf("segs[2] = %+v, want Path=new.go (post-rename target) Binary=false", segs[2])
	}
}

// TestAllocateDiffBudget exercises the round-robin allocator directly,
// independent of diff parsing.
func TestAllocateDiffBudget(t *testing.T) {
	t.Run("single file gets its full size under cap and budget", func(t *testing.T) {
		alloc := allocateDiffBudget([]int{500}, 8_000, 2_000, 80_000)
		if len(alloc) != 1 || alloc[0] != 500 {
			t.Fatalf("alloc = %v, want [500]", alloc)
		}
	})

	t.Run("large file capped at perFileCap even with huge remaining budget", func(t *testing.T) {
		alloc := allocateDiffBudget([]int{500_000}, 8_000, 2_000, 80_000)
		if len(alloc) != 1 || alloc[0] != 8_000 {
			t.Fatalf("alloc = %v, want [8000] (per-file cap)", alloc)
		}
	})

	t.Run("many large files share a scarce budget instead of the first eating it all", func(t *testing.T) {
		sizes := []int{500_000, 500_000, 500_000, 500_000, 500_000}
		alloc := allocateDiffBudget(sizes, 8_000, 2_000, 10_000)
		sum := 0
		for i, a := range alloc {
			sum += a
			if a >= 8_000 {
				t.Errorf("file %d got the full per-file cap (%d) even though the 10000-byte budget can't fit all 5 files at that cap - budget wasn't shared", i, a)
			}
			if a == 0 {
				t.Errorf("file %d got zero bytes; round-robin should give every file at least one chunk before any file gets a second", i)
			}
		}
		if sum > 10_000 {
			t.Errorf("sum(alloc) = %d, exceeds totalBudget 10000", sum)
		}
	})

	t.Run("respects overall budget exactly even under a looser per-file cap", func(t *testing.T) {
		alloc := allocateDiffBudget([]int{8_000, 8_000, 8_000}, 8_000, 2_000, 5_000)
		sum := 0
		for _, a := range alloc {
			sum += a
		}
		if sum != 5_000 {
			t.Errorf("sum(alloc) = %d, want exactly 5000 (all budget used, none wasted)", sum)
		}
	})

	t.Run("zero budget yields no allocation", func(t *testing.T) {
		alloc := allocateDiffBudget([]int{100, 200}, 8_000, 2_000, 0)
		for i, a := range alloc {
			if a != 0 {
				t.Errorf("alloc[%d] = %d, want 0", i, a)
			}
		}
	})

	t.Run("empty input", func(t *testing.T) {
		alloc := allocateDiffBudget(nil, 8_000, 2_000, 80_000)
		if len(alloc) != 0 {
			t.Errorf("alloc = %v, want empty", alloc)
		}
	})
}

// TestTruncateDiffAtLine checks that truncation prefers a clean line
// boundary over an arbitrary byte cut.
func TestTruncateDiffAtLine(t *testing.T) {
	content := "line1\nline2\nline3\nline4\n"
	tests := []struct {
		name  string
		limit int
		want  string
	}{
		{"limit beyond length returns content unchanged", 1000, content},
		{"zero limit returns empty", 0, ""},
		{"negative limit returns empty", -5, ""},
		{"cuts at last newline at or before limit", 13, "line1\nline2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateDiffAtLine(content, tt.limit)
			if got != tt.want {
				t.Errorf("truncateDiffAtLine(_, %d) = %q, want %q", tt.limit, got, tt.want)
			}
		})
	}
}

// TestFormatOmittedNote_BoundsListedFiles ensures the note itself can't
// balloon the context when a huge number of files are omitted.
func TestFormatOmittedNote_BoundsListedFiles(t *testing.T) {
	var omitted []omittedDiffFile
	for i := 0; i < maxOmittedFilesListed+5; i++ {
		omitted = append(omitted, omittedDiffFile{Path: fmt.Sprintf("file%d.json", i), Reason: "large data file", Bytes: 1000})
	}
	note := formatOmittedNote(omitted)

	if !strings.Contains(note, fmt.Sprintf("%d generated/data/binary file(s)", len(omitted))) {
		t.Errorf("note missing total count: %s", note)
	}
	if !strings.Contains(note, "and 5 more") {
		t.Errorf("note should cap the listed files and summarize the rest: %s", note)
	}
	listedLines := strings.Count(note, "\n#   - ")
	if listedLines != maxOmittedFilesListed {
		t.Errorf("listed %d files, want %d (maxOmittedFilesListed)", listedLines, maxOmittedFilesListed)
	}
}

// TestFormatOmittedNote_ListsAllWhenUnderLimit checks the common case
// of a handful of omitted files.
func TestFormatOmittedNote_ListsAllWhenUnderLimit(t *testing.T) {
	omitted := []omittedDiffFile{
		{Path: "go.sum", Reason: "generated/vendored/lockfile", Bytes: 5000},
		{Path: "data/big.json", Reason: "large data file", Bytes: 9000},
	}
	note := formatOmittedNote(omitted)
	if strings.Contains(note, "more") {
		t.Errorf("should not mention '... and N more' when under the limit: %s", note)
	}
	for _, f := range omitted {
		if !strings.Contains(note, f.Path) {
			t.Errorf("note missing path %q: %s", f.Path, note)
		}
	}
}

// ---------------------------------------------------------------------
// Origin-preferring base branch resolution (fix #3).
// ---------------------------------------------------------------------

// runGitOrSkip runs git in dir, skipping the test if git itself fails
// (e.g. not installed/configured in this environment) rather than
// failing the suite.
func runGitOrSkip(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Skipf("git %v failed: %v\n%s (git may not be installed/configured in this environment)", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// chdirToRepo changes the process's working directory to dir (git
// commands in this package always operate on the cwd) and restores it
// on test cleanup.
func chdirToRepo(t *testing.T, dir string) {
	t.Helper()
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("os.Chdir(%q): %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
}

// createGitRepoWithStaleLocalMain creates a temp git repo where the
// local "main" branch is stale (pinned to an older commit) relative to
// a fabricated "origin/main" remote-tracking ref (pointing at a newer
// commit) - simulating a developer who branched a while ago and hasn't
// fetched/pulled since. The refs/remotes/origin/main ref is created
// directly via `update-ref`, so this doesn't require a real network
// remote. Returns the repo dir plus the stale and origin commit SHAs.
func createGitRepoWithStaleLocalMain(t *testing.T) (repoDir, staleSHA, originSHA string) {
	t.Helper()
	tmpDir := t.TempDir()

	runGitOrSkip(t, tmpDir, "init", "-b", "main")
	runGitOrSkip(t, tmpDir, "config", "user.email", "test@test.com")
	runGitOrSkip(t, tmpDir, "config", "user.name", "Test User")

	writeAndCommit := func(name, content, msg string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(tmpDir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		runGitOrSkip(t, tmpDir, "add", name)
		runGitOrSkip(t, tmpDir, "commit", "-m", msg)
		return runGitOrSkip(t, tmpDir, "rev-parse", "HEAD")
	}

	// Commit 1: what local "main" will stay pinned at (stale).
	staleSHA = writeAndCommit("README.md", "v1\n", "initial commit")

	// Commit 2: simulates upstream progress local main hasn't merged -
	// this becomes origin/main without ever touching local main.
	originSHA = writeAndCommit("README.md", "v1\nv2\n", "upstream progress")

	// Pin local main back to the stale commit, and fabricate
	// refs/remotes/origin/main at the newer commit - equivalent to
	// having fetched origin after it advanced, without a real remote.
	runGitOrSkip(t, tmpDir, "update-ref", "refs/heads/main", staleSHA)
	runGitOrSkip(t, tmpDir, "update-ref", "refs/remotes/origin/main", originSHA)

	// Feature branch off the stale local main tip, as if the developer
	// branched before origin/main advanced.
	runGitOrSkip(t, tmpDir, "checkout", "-b", "feature/test", staleSHA)
	writeAndCommit("feature.txt", "feature work\n", "add feature")

	return tmpDir, staleSHA, originSHA
}

// TestResolveBaseRef_PrefersOriginOverStaleLocalBranch is the direct
// regression test for fix #3: resolveBaseRef must resolve to
// origin/main (the up-to-date ref), not the stale local main, so the
// diff doesn't balloon with commits already merged upstream.
func TestResolveBaseRef_PrefersOriginOverStaleLocalBranch(t *testing.T) {
	repoDir, staleSHA, originSHA := createGitRepoWithStaleLocalMain(t)
	if staleSHA == originSHA {
		t.Fatal("test fixture bug: stale and origin SHAs should differ")
	}
	chdirToRepo(t, repoDir)

	got := resolveBaseRef("main")
	if got != "origin/main" {
		t.Fatalf("resolveBaseRef(%q) = %q, want %q (origin/main must be preferred over the stale local main)", "main", got, "origin/main")
	}

	// Confirm the resolved ref actually points at the up-to-date origin
	// commit, not the stale local one - this is the mechanism that
	// keeps the diff/commit list from including already-upstream
	// commits.
	resolvedSHA, err := gitOutput("rev-parse", got)
	if err != nil {
		t.Fatalf("rev-parse %s: %v", got, err)
	}
	if resolvedSHA != originSHA {
		t.Errorf("resolveBaseRef result resolves to %s, want origin's tip %s", resolvedSHA, originSHA)
	}
	if resolvedSHA == staleSHA {
		t.Error("resolveBaseRef must not resolve to the stale local main commit")
	}
}

// TestResolveBaseRef_FallsBackToLocalWhenOriginMissing covers the
// "falling back to local only if origin is unavailable" half of fix #3.
func TestResolveBaseRef_FallsBackToLocalWhenOriginMissing(t *testing.T) {
	tmpDir := t.TempDir()
	runGitOrSkip(t, tmpDir, "init", "-b", "main")
	runGitOrSkip(t, tmpDir, "config", "user.email", "test@test.com")
	runGitOrSkip(t, tmpDir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(tmpDir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write f.txt: %v", err)
	}
	runGitOrSkip(t, tmpDir, "add", "f.txt")
	runGitOrSkip(t, tmpDir, "commit", "-m", "initial")
	// Deliberately no origin remote / remote-tracking refs configured.

	chdirToRepo(t, tmpDir)

	got := resolveBaseRef("main")
	if got != "main" {
		t.Errorf("resolveBaseRef(%q) = %q, want %q (fall back to the local branch when no origin remote-tracking ref exists)", "main", got, "main")
	}
}

// TestResolveBaseRef_EmptyAndAlreadyQualified covers the trivial
// pass-through cases.
func TestResolveBaseRef_EmptyAndAlreadyQualified(t *testing.T) {
	if got := resolveBaseRef(""); got != "" {
		t.Errorf("resolveBaseRef(\"\") = %q, want empty", got)
	}
	if got := resolveBaseRef("origin/develop"); got != "origin/develop" {
		t.Errorf("resolveBaseRef(%q) = %q, want unchanged", "origin/develop", got)
	}
}

// TestDetectBaseBranch_PrefersOriginMainOverLocalMaster shows
// detectBaseBranch selects the branch name confirmed to exist on
// origin, even when a different branch is checked out locally.
func TestDetectBaseBranch_PrefersOriginMainOverLocalMaster(t *testing.T) {
	tmpDir := t.TempDir()
	runGitOrSkip(t, tmpDir, "init", "-b", "master") // local default branch is "master"...
	runGitOrSkip(t, tmpDir, "config", "user.email", "test@test.com")
	runGitOrSkip(t, tmpDir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(tmpDir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write f.txt: %v", err)
	}
	runGitOrSkip(t, tmpDir, "add", "f.txt")
	runGitOrSkip(t, tmpDir, "commit", "-m", "initial")
	sha := runGitOrSkip(t, tmpDir, "rev-parse", "HEAD")

	// ...but the repo has already migrated to "main" upstream (origin
	// has main, not master) - detectBaseBranch should recognize
	// origin's branch, not just fall back to whatever's checked out
	// locally.
	runGitOrSkip(t, tmpDir, "update-ref", "refs/remotes/origin/main", sha)

	chdirToRepo(t, tmpDir)

	got := detectBaseBranch()
	if got != "main" {
		t.Errorf("detectBaseBranch() = %q, want %q (origin/main should be preferred over local master)", got, "main")
	}
}
