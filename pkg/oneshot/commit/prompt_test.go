package commit

import (
	"strings"
	"testing"
)

func TestSuggestedDetail(t *testing.T) {
	tests := []struct {
		name  string
		stats DiffStats
		want  DetailGuidance
	}{
		{
			name:  "tiny change",
			stats: DiffStats{Files: 1, Insertions: 5, Deletions: 3},
			want:  DetailGuidance{Min: 1, Max: 3},
		},
		{
			name:  "small change - boundary",
			stats: DiffStats{Files: 2, Insertions: 15, Deletions: 5},
			want:  DetailGuidance{Min: 1, Max: 3},
		},
		{
			name:  "small-medium change",
			stats: DiffStats{Files: 5, Insertions: 50, Deletions: 20},
			want:  DetailGuidance{Min: 2, Max: 5},
		},
		{
			name:  "medium change",
			stats: DiffStats{Files: 10, Insertions: 60, Deletions: 20},
			want:  DetailGuidance{Min: 2, Max: 5},
		},
		{
			name:  "medium-large change",
			stats: DiffStats{Files: 15, Insertions: 100, Deletions: 50},
			want:  DetailGuidance{Min: 3, Max: 7},
		},
		{
			name:  "large change",
			stats: DiffStats{Files: 20, Insertions: 300, Deletions: 100},
			want:  DetailGuidance{Min: 4, Max: 9},
		},
		{
			name:  "very large change",
			stats: DiffStats{Files: 50, Insertions: 1000, Deletions: 500},
			want:  DetailGuidance{Min: 5, Max: 12},
		},
		{
			name:  "files boundary - few files many changes",
			stats: DiffStats{Files: 2, Insertions: 100, Deletions: 0},
			want:  DetailGuidance{Min: 3, Max: 7},
		},
		{
			name:  "many files few changes",
			stats: DiffStats{Files: 15, Insertions: 10, Deletions: 5},
			// 15 changes total, 15 files: files > 10, so falls to case 3 (total <= 200 && files <= 25)
			want: DetailGuidance{Min: 3, Max: 7},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := suggestedDetail(tt.stats)
			if got.Min != tt.want.Min || got.Max != tt.want.Max {
				t.Errorf("suggestedDetail() = {%d, %d}, want {%d, %d}",
					got.Min, got.Max, tt.want.Min, tt.want.Max)
			}
		})
	}
}

func TestFileStatusDescription(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{"A", "Added"},
		{"M", "Modified"},
		{"D", "Deleted"},
		{"R", "Renamed"},
		{"C", "Copied"},
		{"T", "Type changed"},
		{"U", "Unmerged"},
		{"X", "X"}, // Unknown status returns itself
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			got := fileStatusDescription(tt.status)
			if got != tt.want {
				t.Errorf("fileStatusDescription(%q) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

func TestSystemPrompt(t *testing.T) {
	prompt := SystemPrompt()

	// Should contain key instructions
	if !strings.Contains(prompt, "git commit message generator") {
		t.Error("expected prompt to mention git commit message generator")
	}
	if !strings.Contains(prompt, "generate_commit") {
		t.Error("expected prompt to mention generate_commit tool")
	}
	if !strings.Contains(prompt, "action") {
		t.Error("expected prompt to mention action field")
	}
	if !strings.Contains(prompt, "subject") {
		t.Error("expected prompt to mention subject field")
	}
	if !strings.Contains(prompt, "body") {
		t.Error("expected prompt to mention body field")
	}
	if !strings.Contains(prompt, "imperative mood") {
		t.Error("expected prompt to mention imperative mood")
	}
}

func TestBuildPrompt(t *testing.T) {
	ctx := &Context{
		RepoRoot: "/home/user/project",
		Branch:   "feature/test",
		Areas:    []string{"api", "model"},
		Stats: DiffStats{
			Files:       3,
			Insertions:  50,
			Deletions:   20,
			BinaryFiles: 0,
		},
		Files: []FileChange{
			{Status: "A", Path: "pkg/api/handler.go"},
			{Status: "M", Path: "pkg/model/user.go"},
			{Status: "D", Path: "pkg/model/old.go"},
		},
		Diff:     "+new code\n-old code",
		AgentsMD: "# Project Guidelines\nBe nice.",
	}

	prompt := BuildPrompt(ctx)

	// Check project guidelines section
	if !strings.Contains(prompt, "## Project Guidelines") {
		t.Error("expected Project Guidelines section")
	}
	if !strings.Contains(prompt, "Be nice.") {
		t.Error("expected AGENTS.md content")
	}

	// Check repository section
	if !strings.Contains(prompt, "## Repository") {
		t.Error("expected Repository section")
	}
	if !strings.Contains(prompt, "/home/user/project") {
		t.Error("expected repo root")
	}
	if !strings.Contains(prompt, "feature/test") {
		t.Error("expected branch name")
	}
	if !strings.Contains(prompt, "api, model") {
		t.Error("expected affected areas")
	}

	// Check changes summary section
	if !strings.Contains(prompt, "## Changes Summary") {
		t.Error("expected Changes Summary section")
	}
	if !strings.Contains(prompt, "**Files changed:** 3") {
		t.Error("expected files changed count")
	}
	if !strings.Contains(prompt, "**Insertions:** 50") {
		t.Error("expected insertions count")
	}
	if !strings.Contains(prompt, "**Deletions:** 20") {
		t.Error("expected deletions count")
	}

	// Check staged files section
	if !strings.Contains(prompt, "## Staged Files") {
		t.Error("expected Staged Files section")
	}
	if !strings.Contains(prompt, "Added: pkg/api/handler.go") {
		t.Error("expected added file")
	}
	if !strings.Contains(prompt, "Modified: pkg/model/user.go") {
		t.Error("expected modified file")
	}
	if !strings.Contains(prompt, "Deleted: pkg/model/old.go") {
		t.Error("expected deleted file")
	}

	// Check diff section
	if !strings.Contains(prompt, "## Diff") {
		t.Error("expected Diff section")
	}
	if !strings.Contains(prompt, "```diff") {
		t.Error("expected diff code block")
	}
	if !strings.Contains(prompt, "+new code") {
		t.Error("expected diff content")
	}
}

func TestBuildPrompt_WithBinaryFiles(t *testing.T) {
	ctx := &Context{
		RepoRoot: "/project",
		Branch:   "main",
		Stats: DiffStats{
			Files:       2,
			Insertions:  10,
			Deletions:   5,
			BinaryFiles: 1,
		},
		Files: []FileChange{
			{Status: "M", Path: "code.go"},
		},
		Diff: "diff content",
	}

	prompt := BuildPrompt(ctx)

	if !strings.Contains(prompt, "**Binary files:** 1") {
		t.Error("expected binary files count")
	}
}

func TestBuildPrompt_WithRenamedFile(t *testing.T) {
	ctx := &Context{
		RepoRoot: "/project",
		Branch:   "main",
		Stats:    DiffStats{Files: 1},
		Files: []FileChange{
			{Status: "R", Path: "new_name.go", OldPath: "old_name.go"},
		},
		Diff: "diff content",
	}

	prompt := BuildPrompt(ctx)

	// Should show rename with arrow
	if !strings.Contains(prompt, "old_name.go") {
		t.Error("expected old file name")
	}
	if !strings.Contains(prompt, "new_name.go") {
		t.Error("expected new file name")
	}
	if !strings.Contains(prompt, "→") {
		t.Error("expected arrow for rename")
	}
}

func TestBuildPrompt_WithoutAgentsMD(t *testing.T) {
	ctx := &Context{
		RepoRoot: "/project",
		Branch:   "main",
		Stats:    DiffStats{Files: 1},
		Files: []FileChange{
			{Status: "M", Path: "code.go"},
		},
		Diff:     "diff content",
		AgentsMD: "", // No agents file
	}

	prompt := BuildPrompt(ctx)

	// Should NOT have project guidelines section
	if strings.Contains(prompt, "## Project Guidelines") {
		t.Error("should not have Project Guidelines section when AgentsMD is empty")
	}
}

func TestBuildPrompt_WithoutAreas(t *testing.T) {
	ctx := &Context{
		RepoRoot: "/project",
		Branch:   "main",
		Stats:    DiffStats{Files: 1},
		Files: []FileChange{
			{Status: "M", Path: "main.go"}, // Root file, no area
		},
		Diff:  "diff content",
		Areas: nil,
	}

	prompt := BuildPrompt(ctx)

	// Should NOT have affected areas line
	if strings.Contains(prompt, "**Affected areas:**") {
		t.Error("should not have Affected areas when Areas is empty")
	}
}

func TestBuildPrompt_SuggestedDetail(t *testing.T) {
	// Test that suggested detail is included based on stats
	ctx := &Context{
		RepoRoot: "/project",
		Branch:   "main",
		Stats: DiffStats{
			Files:      5,
			Insertions: 50,
			Deletions:  30,
		},
		Files: []FileChange{
			{Status: "M", Path: "code.go"},
		},
		Diff: "diff content",
	}

	prompt := BuildPrompt(ctx)

	// Should include suggested detail guidance
	if !strings.Contains(prompt, "**Suggested body detail:**") {
		t.Error("expected Suggested body detail line")
	}
	// For these stats (80 total, 5 files), should be 2-5 bullets
	if !strings.Contains(prompt, "2-5 bullet points") {
		t.Error("expected 2-5 bullet points guidance")
	}
}

func TestDetailGuidance_Boundaries(t *testing.T) {
	// Test exact boundary conditions
	tests := []struct {
		total int
		files int
		want  DetailGuidance
	}{
		// Exact boundary: 20 changes, 2 files -> still small
		{20, 2, DetailGuidance{Min: 1, Max: 3}},
		// Just over small boundary
		{21, 2, DetailGuidance{Min: 2, Max: 5}},
		{20, 3, DetailGuidance{Min: 2, Max: 5}},
		// Exact boundary: 80 changes, 10 files -> still medium
		{80, 10, DetailGuidance{Min: 2, Max: 5}},
		// Just over medium boundary
		{81, 10, DetailGuidance{Min: 3, Max: 7}},
		{80, 11, DetailGuidance{Min: 3, Max: 7}},
		// Exact boundary: 200 changes, 25 files -> still medium-large
		{200, 25, DetailGuidance{Min: 3, Max: 7}},
		// Just over
		{201, 25, DetailGuidance{Min: 4, Max: 9}},
		{200, 26, DetailGuidance{Min: 4, Max: 9}},
		// Exact boundary: 500 changes -> still large
		{500, 50, DetailGuidance{Min: 4, Max: 9}},
		// Very large
		{501, 50, DetailGuidance{Min: 5, Max: 12}},
	}

	for _, tt := range tests {
		stats := DiffStats{
			Insertions: tt.total / 2,
			Deletions:  tt.total - tt.total/2,
			Files:      tt.files,
		}
		got := suggestedDetail(stats)
		if got.Min != tt.want.Min || got.Max != tt.want.Max {
			t.Errorf("suggestedDetail(total=%d, files=%d) = {%d, %d}, want {%d, %d}",
				tt.total, tt.files, got.Min, got.Max, tt.want.Min, tt.want.Max)
		}
	}
}
