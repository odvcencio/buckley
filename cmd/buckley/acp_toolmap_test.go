package main

import (
	"path/filepath"
	"testing"

	"m31labs.dev/buckley/v2/pkg/acp"
	"m31labs.dev/buckley/v2/pkg/tool"
)

// TestToolCallKind_MatchesRegisteredToolNames locks S2: the kind/title maps
// key on tools' real registered Name() values, not their pre-rename names.
// shell_command, patch_file, headless_browse, terminal_editor, and
// mark_resolved no longer exist as registered tools (run_shell, apply_patch,
// browse_url, edit_file_terminal, mark_conflict_resolved replaced them), so
// every tool call using the current names previously rendered as kind
// "other" in the client UI.
func TestToolCallKind_MatchesRegisteredToolNames(t *testing.T) {
	t.Parallel()

	registry := tool.NewRegistry()
	cases := []struct {
		name string
		kind string
	}{
		{"run_shell", acp.ToolKindExecute},
		{"apply_patch", acp.ToolKindEdit},
		{"browse_url", acp.ToolKindFetch},
		{"edit_file_terminal", acp.ToolKindExecute},
		{"mark_conflict_resolved", acp.ToolKindEdit},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, ok := registry.Get(tc.name); !ok {
				t.Fatalf("tool %q is not registered -- toolCallKind entry does not match a real tool name", tc.name)
			}
			if got := toolCallKind(tc.name); got != tc.kind {
				t.Fatalf("toolCallKind(%q) = %q, want %q", tc.name, got, tc.kind)
			}
		})
	}

	// The pre-rename names are not registered tools and must fall through
	// to "other" rather than silently reusing a kind for the wrong tool.
	for _, stale := range []string{"shell_command", "patch_file", "headless_browse", "terminal_editor", "mark_resolved"} {
		if _, ok := registry.Get(stale); ok {
			t.Fatalf("stale tool name %q unexpectedly registered -- update this test's assumptions", stale)
		}
		if got := toolCallKind(stale); got != acp.ToolKindOther {
			t.Fatalf("toolCallKind(%q) = %q, want %q (stale name should not match a kind)", stale, got, acp.ToolKindOther)
		}
	}
}

func TestToolCallTitle_RunShell(t *testing.T) {
	t.Parallel()
	got := toolCallTitle("run_shell", map[string]any{"command": "go test ./..."})
	want := "Run: go test ./..."
	if got != want {
		t.Fatalf("toolCallTitle(run_shell) = %q, want %q", got, want)
	}
}

func TestToolCallTitle_ApplyPatch(t *testing.T) {
	t.Parallel()
	got := toolCallTitle("apply_patch", map[string]any{"path": "pkg/foo.go"})
	want := "Edit pkg/foo.go"
	if got != want {
		t.Fatalf("toolCallTitle(apply_patch) = %q, want %q", got, want)
	}
}

// TestToolCallLocations_ResolvesAgainstWorkDir locks the second half of S2:
// tool-call locations must resolve against the session's WorkingDirectory,
// not the ACP process's own cwd.
func TestToolCallLocations_ResolvesAgainstWorkDir(t *testing.T) {
	t.Parallel()

	got := toolCallLocations(map[string]any{"path": "pkg/foo.go"}, "/workspace/project")
	if len(got) != 1 {
		t.Fatalf("locations = %v, want 1 entry", got)
	}
	want := filepath.Join("/workspace/project", "pkg/foo.go")
	if got[0].Path != want {
		t.Fatalf("Path = %q, want %q", got[0].Path, want)
	}
}

// TestToolCallLocations_AbsolutePathIgnoresWorkDir asserts an already
// absolute tool path is left untouched regardless of the session workDir.
func TestToolCallLocations_AbsolutePathIgnoresWorkDir(t *testing.T) {
	t.Parallel()

	got := toolCallLocations(map[string]any{"path": "/etc/hosts"}, "/workspace/project")
	if len(got) != 1 || got[0].Path != "/etc/hosts" {
		t.Fatalf("locations = %v, want [/etc/hosts]", got)
	}
}

// TestResolveACPPath_FallsBackToProcessCwdWhenWorkDirUnset preserves the
// prior behavior for sessions that never negotiated a working directory.
func TestResolveACPPath_FallsBackToProcessCwdWhenWorkDirUnset(t *testing.T) {
	t.Parallel()

	got := resolveACPPath("relative/path.go", "")
	if !filepath.IsAbs(got) {
		t.Fatalf("resolveACPPath with empty workDir = %q, want an absolute path", got)
	}
}
