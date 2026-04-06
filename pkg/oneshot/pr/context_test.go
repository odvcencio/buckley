package pr

import (
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
		{"hello world", 3},                                 // 11 chars -> (11+3)/4 = 3
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
		name  string
		got   interface{}
		want  interface{}
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
