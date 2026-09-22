package builtin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

	t.Run("path overrides non-repo workdir", func(t *testing.T) {
		repoDir := createTestGitRepo(t)
		tool := &GitLogTool{}
		tool.SetWorkDir(filepath.Dir(repoDir))

		result, err := tool.Execute(map[string]any{
			"path":  repoDir,
			"count": float64(1),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Fatalf("expected success with explicit repo path: %s", result.Error)
		}
		commits, ok := result.Data["commits"].([]string)
		if !ok || len(commits) != 1 {
			t.Fatalf("expected one commit, got %#v", result.Data["commits"])
		}
	})

	t.Run("git failure includes stderr", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skipf("git not installed: %v", err)
		}
		tool := &GitLogTool{}
		tool.SetWorkDir(t.TempDir())

		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Fatal("expected git log to fail outside a git repo")
		}
		if !strings.Contains(result.Error, "git command failed") {
			t.Fatalf("expected wrapped git failure, got %q", result.Error)
		}
		if !strings.Contains(result.Error, "not a git repository") && !strings.Contains(result.Error, "fatal:") {
			t.Fatalf("expected stderr details, got %q", result.Error)
		}
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

	// Change to test repo directory (auto-restored by t.Chdir)
	t.Chdir(repoDir)

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

func TestGitDiffTool_PagesRecoverExactPatch(t *testing.T) {
	repo := createTestGitRepo(t)
	path := filepath.Join(repo, "test.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("changed line\n", 320)+strings.Repeat("🚀", 100)+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tool := &GitDiffTool{}
	tool.SetWorkDir(repo)
	defaultPage, err := tool.Execute(map[string]any{})
	if err != nil || !defaultPage.Success || defaultPage.Data["diff_truncated"] != true || len(defaultPage.Data["diff"].(string)) > defaultDiffPageBytes {
		t.Fatalf("default page is not bounded: %+v %v", defaultPage, err)
	}
	for _, staged := range []bool{false, true} {
		if staged {
			cmd := exec.Command("git", "add", "test.txt")
			cmd.Dir = repo
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("stage: %v: %s", err, output)
			}
		}
		args := []string{"diff"}
		if staged {
			args = append(args, "--cached")
		}
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		want, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		name := "unstaged"
		if staged {
			name = "staged"
		}
		t.Run(name, func(t *testing.T) {
			var rebuilt strings.Builder
			offset := int64(0)
			expectedHash := ""
			for page := 0; page < 100; page++ {
				params := map[string]any{"staged": staged, "file": "test.txt", "byte_offset": offset, "max_bytes": 75}
				if page > 0 {
					params["expected_sha256"] = expectedHash
				}
				result, err := tool.Execute(params)
				if err != nil || !result.Success {
					t.Fatalf("page %d: %+v, %v", page, result, err)
				}
				data := result.Data
				if data["byte_offset"] != offset || data["total_bytes"] != int64(len(want)) {
					t.Fatalf("page %d metadata: %+v", page, data)
				}
				serialized, err := json.Marshal(data)
				if err != nil {
					t.Fatal(err)
				}
				var visible map[string]any
				if err := json.Unmarshal(serialized, &visible); err != nil {
					t.Fatal(err)
				}
				part := visible["diff"].(string)
				if len(part) == 0 || len(part) > 75 {
					t.Fatalf("page %d length %d", page, len(part))
				}
				rebuilt.WriteString(part)
				expectedHash = data["diff_sha256"].(string)
				if next, hasMore := data["next_byte_offset"]; hasMore {
					if data["diff_truncated"] != true || next.(int64) != offset+int64(len(part)) {
						t.Fatalf("bad continuation on page %d: %+v", page, data)
					}
					offset = next.(int64)
					continue
				}
				if rebuilt.String() != string(want) {
					t.Fatalf("pages did not reconstruct exact patch: got %d bytes, want %d", rebuilt.Len(), len(want))
				}
				digest := sha256.Sum256(want)
				if expectedHash != hex.EncodeToString(digest[:]) {
					t.Fatalf("wrong diff hash: %s", expectedHash)
				}
				return
			}
			t.Fatal("pagination did not terminate")
		})
	}
}

func TestGitDiffTool_RejectsInvalidOrChangedPage(t *testing.T) {
	repo := createTestGitRepo(t)
	path := filepath.Join(repo, "test.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("line\n", 50)), 0644); err != nil {
		t.Fatal(err)
	}
	tool := &GitDiffTool{}
	tool.SetWorkDir(repo)
	first, err := tool.Execute(map[string]any{"max_bytes": 50})
	if err != nil || !first.Success || first.Data["next_byte_offset"] == nil {
		t.Fatalf("first page: %+v %v", first, err)
	}
	if err := os.WriteFile(path, []byte("changed again\n"), 0644); err != nil {
		t.Fatal(err)
	}
	changed, err := tool.Execute(map[string]any{"byte_offset": first.Data["next_byte_offset"], "expected_sha256": first.Data["diff_sha256"]})
	if err != nil || changed.Success || !strings.Contains(changed.Error, "diff changed") {
		t.Fatalf("changed diff accepted: %+v %v", changed, err)
	}
	for _, params := range []map[string]any{
		{"byte_offset": -1}, {"byte_offset": 1.5}, {"byte_offset": json.Number("9007199254740992")},
		{"max_bytes": 0}, {"max_bytes": 3}, {"max_bytes": 8193}, {"max_bytes": "huge"},
		{"expected_sha256": "bad"}, {"byte_offset": 999999},
	} {
		t.Run(fmt.Sprint(params), func(t *testing.T) {
			result, err := tool.Execute(params)
			if err != nil || result.Success || result.Error == "" {
				t.Fatalf("invalid page accepted: %+v %v", result, err)
			}
		})
	}
}
