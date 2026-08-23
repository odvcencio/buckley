package builtin

import (
	"testing"
)

// TestToolMetadata tests that all built-in tools have proper metadata
func TestToolMetadata(t *testing.T) {
	tools := []struct {
		tool interface {
			Name() string
			Description() string
			Parameters() ParameterSchema
		}
		expectedName string
	}{
		{&ReadFileTool{}, "read_file"},
		{&WriteFileTool{}, "write_file"},
		{&ListDirectoryTool{}, "list_directory"},
		{&PatchFileTool{}, "apply_patch"},
		{&SearchTextTool{}, "search_text"},
		{&SearchReplaceTool{}, "search_replace"},
		{&FindFilesTool{}, "find_files"},
		{&FileExistsTool{}, "file_exists"},
		{&GetFileInfoTool{}, "get_file_info"},
		{&GitStatusTool{}, "git_status"},
		{&GitDiffTool{}, "git_diff"},
		{&GitLogTool{}, "git_log"},
		{&GitBlameTool{}, "git_blame"},
		{&ListMergeConflictsTool{}, "list_merge_conflicts"},
		{&MarkResolvedTool{}, "mark_conflict_resolved"},
		{&ShellCommandTool{}, "run_shell"},
		{&CreateSkillTool{}, "create_skill"},
	}

	for _, tt := range tools {
		t.Run(tt.expectedName, func(t *testing.T) {
			// Test Name
			if got := tt.tool.Name(); got != tt.expectedName {
				t.Errorf("Name() = %q, want %q", got, tt.expectedName)
			}

			// Test Description is not empty
			if desc := tt.tool.Description(); desc == "" {
				t.Error("Description() returned empty string")
			}

			// Test Parameters returns valid schema
			params := tt.tool.Parameters()
			if params.Type != "object" {
				t.Errorf("Parameters().Type = %q, want %q", params.Type, "object")
			}
		})
	}
}

// TestReadFileTool_Execute tests ReadFileTool execution
func TestReadFileTool_Execute(t *testing.T) {
	tool := &ReadFileTool{}

	t.Run("missing path parameter", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for missing path")
		}
		if result.Error == "" {
			t.Error("expected error message")
		}
	})

	t.Run("invalid path type", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{"path": 123})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for invalid path type")
		}
	})

	t.Run("nonexistent file", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{"path": "/nonexistent/file.txt"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for nonexistent file")
		}
	})
}

// TestWriteFileTool_Execute tests WriteFileTool execution
func TestWriteFileTool_Execute(t *testing.T) {
	tool := &WriteFileTool{}

	t.Run("missing path parameter", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{"content": "test"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for missing path")
		}
	})

	t.Run("missing content parameter", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{"path": "/tmp/test.txt"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for missing content")
		}
	})
}

// TestListDirectoryTool_Execute tests ListDirectoryTool execution
func TestListDirectoryTool_Execute(t *testing.T) {
	tool := &ListDirectoryTool{}

	t.Run("current directory (no path)", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Should default to current directory and succeed
		if !result.Success {
			t.Errorf("expected success for current directory: %s", result.Error)
		}
	})

	t.Run("nonexistent directory", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{"path": "/nonexistent/directory"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for nonexistent directory")
		}
	})
}

// TestGitStatusTool_Execute tests GitStatusTool execution
func TestGitStatusTool_Execute(t *testing.T) {
	tool := &GitStatusTool{}

	// Git status should work in the buckley repo
	result, err := tool.Execute(map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// We expect this to succeed since we're in a git repo
	if !result.Success {
		t.Logf("git status failed (may not be in git repo): %s", result.Error)
	}
}

// TestSearchTextTool_Execute tests SearchTextTool execution
func TestSearchTextTool_Execute(t *testing.T) {
	tool := &SearchTextTool{}

	t.Run("missing pattern", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for missing pattern")
		}
	})
}

// TestFileExistsTool_Execute tests FileExistsTool execution
func TestFileExistsTool_Execute(t *testing.T) {
	tool := &FileExistsTool{}

	t.Run("missing path parameter", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for missing path")
		}
	})

	t.Run("nonexistent file", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{"path": "/nonexistent/file.txt"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("file_exists should succeed, checking existence: %s", result.Error)
		}
		if exists, ok := result.Data["exists"].(bool); !ok || exists {
			t.Error("expected exists=false for nonexistent file")
		}
	})
}

// TestShellCommandTool_Execute tests ShellCommandTool execution
func TestShellCommandTool_Execute(t *testing.T) {
	tool := &ShellCommandTool{}

	t.Run("missing command parameter", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Success {
			t.Error("expected failure for missing command")
		}
	})

	t.Run("simple echo command", func(t *testing.T) {
		result, err := tool.Execute(map[string]any{
			"command": "echo hello",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Errorf("expected success for echo command: %s", result.Error)
		}
	})
}
