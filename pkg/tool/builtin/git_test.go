package builtin

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitStatusTool(t *testing.T) {
	tool := &GitStatusTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "git_status" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "git_status")
		}
		if tool.Description() == "" {
			t.Error("Description() should not be empty")
		}
		params := tool.Parameters()
		if params.Type != "object" {
			t.Errorf("Parameters().Type = %q, want %q", params.Type, "object")
		}
	})

	t.Run("execute in git repo", func(t *testing.T) {
		// Test in actual repo (we're in buckley)
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Should succeed since we're in a git repo
		if !result.Success {
			t.Logf("git_status failed (may not be in git repo): %s", result.Error)
		}
	})
}

func TestGitDiffTool(t *testing.T) {
	tool := &GitDiffTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "git_diff" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "git_diff")
		}
		if tool.Description() == "" {
			t.Error("Description() should not be empty")
		}
	})

	t.Run("execute without args", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// May succeed or fail depending on git state, just check no panic
		_ = result
	})

	t.Run("execute with staged flag", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{"staged": true})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		_ = result
	})
}

func TestGitLogTool(t *testing.T) {
	tool := &GitLogTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "git_log" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "git_log")
		}
	})

	t.Run("execute with default count", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Should work in git repo
		if result.Success {
			if _, ok := result.Data["commits"]; !ok {
				t.Error("expected 'commits' in result data")
			}
		}
	})

	t.Run("execute with custom count", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{"count": 5})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		_ = result
	})

	t.Run("execute with path filter", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{"path": "pkg/"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		_ = result
	})
}

func TestGitBlameTool(t *testing.T) {
	tool := &GitBlameTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "git_blame" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "git_blame")
		}
	})

	t.Run("missing path parameter", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for missing path")
		}
	})

	t.Run("blame existing file", func(t *testing.T) {
		// Use a file that definitely exists in the repo
		result, err := tool.Execute(map[string]any{"path": "AGENTS.md"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			if result.Data["blame"] == nil {
				t.Error("expected 'blame' in result data")
			}
		}
	})
}

func TestListMergeConflictsTool(t *testing.T) {
	tool := &ListMergeConflictsTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "list_merge_conflicts" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "list_merge_conflicts")
		}
	})

	t.Run("execute in clean repo", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// In a clean repo, should return empty conflicts list
		if result.Success {
			if conflicts, ok := result.Data["conflicts"].([]string); ok {
				// Should be empty or have actual conflicts
				_ = conflicts
			}
		}
	})
}

func TestMarkResolvedTool(t *testing.T) {
	tool := &MarkResolvedTool{}

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "mark_conflict_resolved" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "mark_conflict_resolved")
		}
	})

	t.Run("missing path parameter", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for missing path")
		}
	})
}

// Helper to create a test git repo
func createTestGitRepo(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()

	// Initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Skipf("git init failed: %v (git may not be installed)", err)
	}

	// Configure git for commits
	cmd = exec.Command("git", "config", "user.email", "test@test.com")
	cmd.Dir = tmpDir
	cmd.Run()

	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = tmpDir
	cmd.Run()

	// Create a file and commit
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("initial content"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	cmd = exec.Command("git", "add", "test.txt")
	cmd.Dir = tmpDir
	cmd.Run()

	cmd = exec.Command("git", "commit", "-m", "initial commit")
	cmd.Dir = tmpDir
	cmd.Run()

	return tmpDir
}

func TestGitToolsInIsolatedRepo(t *testing.T) {
	// Create isolated test repo
	repoDir := createTestGitRepo(t)

	// Save and restore working directory
	originalWd, _ := os.Getwd()
	if err := os.Chdir(repoDir); err != nil {
		t.Fatalf("failed to change to test repo: %v", err)
	}
	defer os.Chdir(originalWd)

	t.Run("git_status in test repo", func(t *testing.T) {
		tool := &GitStatusTool{}
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("expected success in test repo: %s", result.Error)
		}
	})

	t.Run("git_log in test repo", func(t *testing.T) {
		tool := &GitLogTool{}
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("expected success in test repo: %s", result.Error)
		}
	})

	t.Run("git_diff with changes", func(t *testing.T) {
		// Modify a file to create diff
		testFile := filepath.Join(repoDir, "test.txt")
		if err := os.WriteFile(testFile, []byte("modified content"), 0644); err != nil {
			t.Fatalf("failed to modify test file: %v", err)
		}

		tool := &GitDiffTool{}
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("expected success: %s", result.Error)
		}
		if diff, ok := result.Data["diff"].(string); ok {
			if diff == "" {
				t.Error("expected non-empty diff")
			}
		}
	})

	t.Run("git_diff truncates output when limit is set", func(t *testing.T) {
		testFile := filepath.Join(repoDir, "test.txt")
		payload := strings.Repeat("line\n", 200)
		if err := os.WriteFile(testFile, []byte(payload), 0644); err != nil {
			t.Fatalf("failed to modify test file: %v", err)
		}

		tool := &GitDiffTool{}
		tool.SetMaxOutputBytes(50)
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("expected success: %s", result.Error)
		}
		diff, _ := result.Data["diff"].(string)
		if len(diff) > 50 {
			t.Fatalf("expected diff <= 50 bytes, got %d", len(diff))
		}
		if truncated, ok := result.Data["diff_truncated"].(bool); !ok || !truncated {
			t.Fatalf("expected diff_truncated=true, got %v", result.Data["diff_truncated"])
		}
	})
}
