package experiment

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEvaluateCriteria(t *testing.T) {
	// Create temp directory for file tests
	tmpDir, err := os.MkdirTemp("", "criteria-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a test file
	testFile := filepath.Join(tmpDir, "exists.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	tests := []struct {
		name         string
		worktreePath string
		workingDir   string
		output       string
		criteria     []SuccessCriterion
		wantCount    int
		wantPassed   int
	}{
		{
			name:         "empty worktree path returns nil",
			worktreePath: "",
			criteria:     []SuccessCriterion{{ID: 1, Type: CriterionContains, Target: "test"}},
			wantCount:    0,
		},
		{
			name:         "empty criteria returns nil",
			worktreePath: tmpDir,
			criteria:     nil,
			wantCount:    0,
		},
		{
			name:         "criteria with ID 0 is skipped",
			worktreePath: tmpDir,
			criteria:     []SuccessCriterion{{ID: 0, Type: CriterionContains, Target: "test"}},
			wantCount:    0,
		},
		{
			name:         "contains criterion - passes",
			worktreePath: tmpDir,
			output:       "this is a test output",
			criteria:     []SuccessCriterion{{ID: 1, Type: CriterionContains, Target: "test"}},
			wantCount:    1,
			wantPassed:   1,
		},
		{
			name:         "contains criterion - fails",
			worktreePath: tmpDir,
			output:       "this is output",
			criteria:     []SuccessCriterion{{ID: 1, Type: CriterionContains, Target: "missing"}},
			wantCount:    1,
			wantPassed:   0,
		},
		{
			name:         "file_exists criterion - passes",
			worktreePath: tmpDir,
			criteria:     []SuccessCriterion{{ID: 1, Type: CriterionFileExists, Target: "exists.txt"}},
			wantCount:    1,
			wantPassed:   1,
		},
		{
			name:         "file_exists criterion - fails",
			worktreePath: tmpDir,
			criteria:     []SuccessCriterion{{ID: 1, Type: CriterionFileExists, Target: "missing.txt"}},
			wantCount:    1,
			wantPassed:   0,
		},
		{
			name:         "manual criterion - always fails with message",
			worktreePath: tmpDir,
			criteria:     []SuccessCriterion{{ID: 1, Type: CriterionManual, Target: "check manually"}},
			wantCount:    1,
			wantPassed:   0,
		},
		{
			name:         "command criterion - passes",
			worktreePath: tmpDir,
			criteria:     []SuccessCriterion{{ID: 1, Type: CriterionCommand, Target: "true"}},
			wantCount:    1,
			wantPassed:   1,
		},
		{
			name:         "command criterion - fails",
			worktreePath: tmpDir,
			criteria:     []SuccessCriterion{{ID: 1, Type: CriterionCommand, Target: "false"}},
			wantCount:    1,
			wantPassed:   0,
		},
		{
			name:         "test_pass criterion - passes",
			worktreePath: tmpDir,
			criteria:     []SuccessCriterion{{ID: 1, Type: CriterionTestPass, Target: "echo success"}},
			wantCount:    1,
			wantPassed:   1,
		},
		{
			name:         "multiple criteria",
			worktreePath: tmpDir,
			output:       "test output",
			criteria: []SuccessCriterion{
				{ID: 1, Type: CriterionContains, Target: "test"},
				{ID: 2, Type: CriterionFileExists, Target: "exists.txt"},
				{ID: 3, Type: CriterionContains, Target: "missing"},
			},
			wantCount:  3,
			wantPassed: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			got := EvaluateCriteria(ctx, tt.worktreePath, tt.workingDir, tt.output, tt.criteria)

			if len(got) != tt.wantCount {
				t.Errorf("EvaluateCriteria() count = %v, want %v", len(got), tt.wantCount)
				return
			}

			passed := 0
			for _, eval := range got {
				if eval.Passed {
					passed++
				}
				if eval.EvaluatedAt.IsZero() {
					t.Error("EvaluateCriteria() did not set EvaluatedAt")
				}
			}

			if passed != tt.wantPassed {
				t.Errorf("EvaluateCriteria() passed = %v, want %v", passed, tt.wantPassed)
			}
		})
	}
}

func TestEvaluateCriteria_NilContext(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "criteria-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	criteria := []SuccessCriterion{{ID: 1, Type: CriterionCommand, Target: "echo hello"}}

	// Should not panic with nil context
	got := EvaluateCriteria(nil, tmpDir, "", "", criteria)
	if len(got) != 1 {
		t.Errorf("EvaluateCriteria(nil ctx) count = %v, want 1", len(got))
	}
}

func TestEvaluateCriteria_ContextCancellation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "criteria-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	// Give context time to expire
	time.Sleep(5 * time.Millisecond)

	criteria := []SuccessCriterion{{ID: 1, Type: CriterionCommand, Target: "sleep 10"}}
	got := EvaluateCriteria(ctx, tmpDir, "", "", criteria)

	if len(got) != 1 {
		t.Fatalf("EvaluateCriteria() count = %v, want 1", len(got))
	}

	// Command should fail due to context cancellation
	if got[0].Passed {
		t.Error("EvaluateCriteria() with cancelled context should fail")
	}
}

func TestEvaluateCriteria_FileExistsAbsolutePath(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "criteria-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	absPath := filepath.Join(tmpDir, "absolute.txt")
	if err := os.WriteFile(absPath, []byte("test"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	criteria := []SuccessCriterion{{ID: 1, Type: CriterionFileExists, Target: absPath}}
	got := EvaluateCriteria(context.Background(), tmpDir, "", "", criteria)

	if len(got) != 1 || !got[0].Passed {
		t.Error("EvaluateCriteria() should pass for absolute path to existing file")
	}
}

func TestEvaluateCriteria_UnsupportedType(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "criteria-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	criteria := []SuccessCriterion{{ID: 1, Type: CriterionType("unknown"), Target: "test"}}
	got := EvaluateCriteria(context.Background(), tmpDir, "", "", criteria)

	if len(got) != 1 {
		t.Fatalf("EvaluateCriteria() count = %v, want 1", len(got))
	}

	if got[0].Passed {
		t.Error("EvaluateCriteria() unsupported type should fail")
	}

	if got[0].Details == "" {
		t.Error("EvaluateCriteria() unsupported type should have details")
	}
}

func TestResolveWorkingDir(t *testing.T) {
	tests := []struct {
		name         string
		worktreePath string
		workingDir   string
		want         string
	}{
		{
			name:         "empty working dir returns worktree path",
			worktreePath: "/path/to/worktree",
			workingDir:   "",
			want:         "/path/to/worktree",
		},
		{
			name:         "whitespace working dir returns worktree path",
			worktreePath: "/path/to/worktree",
			workingDir:   "   ",
			want:         "/path/to/worktree",
		},
		{
			name:         "absolute working dir returned as-is",
			worktreePath: "/path/to/worktree",
			workingDir:   "/absolute/path",
			want:         "/absolute/path",
		},
		{
			name:         "relative working dir joined with worktree",
			worktreePath: "/path/to/worktree",
			workingDir:   "subdir",
			want:         "/path/to/worktree/subdir",
		},
		{
			name:         "relative path with subdirs",
			worktreePath: "/worktree",
			workingDir:   "a/b/c",
			want:         "/worktree/a/b/c",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveWorkingDir(tt.worktreePath, tt.workingDir)
			if got != tt.want {
				t.Errorf("resolveWorkingDir() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTruncateDetails(t *testing.T) {
	tests := []struct {
		name  string
		value string
		limit int
		want  string
	}{
		{
			name:  "zero limit returns empty",
			value: "test",
			limit: 0,
			want:  "",
		},
		{
			name:  "negative limit returns empty",
			value: "test",
			limit: -1,
			want:  "",
		},
		{
			name:  "value shorter than limit unchanged",
			value: "short",
			limit: 100,
			want:  "short",
		},
		{
			name:  "value equal to limit unchanged",
			value: "exact",
			limit: 5,
			want:  "exact",
		},
		{
			name:  "value longer than limit truncated with ellipsis",
			value: "this is a long string",
			limit: 10,
			want:  "this is a ...",
		},
		{
			name:  "empty value unchanged",
			value: "",
			limit: 100,
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateDetails(tt.value, tt.limit)
			if got != tt.want {
				t.Errorf("truncateDetails() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRunCriterionCommand(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "criteria-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	tests := []struct {
		name       string
		command    string
		wantPassed bool
		wantOutput bool
	}{
		{
			name:       "empty command fails",
			command:    "",
			wantPassed: false,
		},
		{
			name:       "whitespace command fails",
			command:    "   ",
			wantPassed: false,
		},
		{
			name:       "successful command passes",
			command:    "echo hello",
			wantPassed: true,
			wantOutput: true,
		},
		{
			name:       "failing command fails",
			command:    "exit 1",
			wantPassed: false,
		},
		{
			name:       "command with output",
			command:    "echo 'test output'",
			wantPassed: true,
			wantOutput: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			passed, details := runCriterionCommand(ctx, tmpDir, tt.command)

			if passed != tt.wantPassed {
				t.Errorf("runCriterionCommand() passed = %v, want %v", passed, tt.wantPassed)
			}

			if tt.wantOutput && details == "" {
				t.Error("runCriterionCommand() expected output but got empty details")
			}
		})
	}
}
