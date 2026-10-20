package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/internal/textdiff"
	"m31labs.dev/buckley/pkg/mission"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestApprovalMiddleware_FilePreviewMatchesExecution(t *testing.T) {
	for _, tc := range []struct {
		name, before, after string
		tool                Tool
		params              map[string]any
	}{
		{"newline-only", "old", "old\n", &builtin.WriteFileTool{}, map[string]any{"content": "old\n"}},
		{"write-relative", "old", "new", &builtin.WriteFileTool{}, map[string]any{"content": "new"}},
		{"batch-placeholders", "alpha beta", "FIRST SECOND", &builtin.EditFileTool{}, map[string]any{"old_string": "", "new_string": "", "replace_all": false, "edits": []any{map[string]any{"old_string": "alpha", "new_string": "FIRST"}, map[string]any{"old_string": "beta", "new_string": "SECOND"}}}},
		{"insert-int", "alpha\nomega", "alpha\nbeta\nomega", &builtin.InsertTextTool{}, map[string]any{"line": 2, "text": "beta"}},
		{"delete-int", "a\nb\nc", "a\nc", &builtin.DeleteLinesTool{}, map[string]any{"start_line": 2, "end_line": 2}},
		{"regex-replace", "item=12\n", "item=13\n", &builtin.SearchReplaceTool{}, map[string]any{"search": `(item=)(\d+)`, "replace": "${1}13", "use_regex": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			project := filepath.Join(root, "project")
			other := filepath.Join(root, "other")
			if err := os.MkdirAll(project, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(other, 0700); err != nil {
				t.Fatal(err)
			}
			t.Chdir(other)
			path := filepath.Join(project, "note.txt")
			if err := os.WriteFile(path, []byte(tc.before), 0600); err != nil {
				t.Fatal(err)
			}
			store, err := storage.New(filepath.Join(root, "mission.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			if err := store.CreateSession(&storage.Session{ID: "preview", CreatedAt: time.Now(), LastActive: time.Now(), Status: storage.SessionStatusActive}); err != nil {
				t.Fatal(err)
			}
			missions := mission.NewStore(store.DB())
			registry := NewEmptyRegistry()
			registry.Register(tc.tool)
			registry.SetWorkDir(project)
			registry.EnableMissionControl(missions, "agent", true, 3*time.Second)
			registry.UpdateMissionSession("preview")
			tc.params["path"] = "note.txt"
			done := make(chan struct{})
			var result *builtin.Result
			var execErr error
			go func() { result, execErr = registry.Execute(tc.tool.Name(), tc.params); close(done) }()
			id := waitForPendingChange(t, store.DB())
			var recordedPath, diff string
			if err := store.DB().QueryRow(`SELECT file_path,diff FROM pending_changes WHERE id=?`, id).Scan(&recordedPath, &diff); err != nil {
				t.Fatal(err)
			}
			want := textdiff.Unified(path, tc.before, tc.after)
			// Settle the waiting operation before reporting a failed assertion.
			matches := recordedPath == path && diff == want
			if err := missions.UpdatePendingChangeStatus(id, "approved", "fixture"); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("approval did not settle")
			}
			if !matches {
				t.Errorf("approval target=%q diff=%q; want target=%q diff=%q", recordedPath, diff, path, want)
			}
			if execErr != nil || result == nil || !result.Success {
				t.Fatalf("execute: %+v %v", result, execErr)
			}
			if got, err := os.ReadFile(path); err != nil || string(got) != tc.after {
				t.Fatalf("execution bytes=%q want=%q err=%v", got, tc.after, err)
			}
		})
	}
}

func newMissionPreviewRegistry(t *testing.T, tool Tool, project string) (*Registry, *storage.Store, *mission.Store) {
	t.Helper()
	store, err := storage.New(filepath.Join(t.TempDir(), "mission.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateSession(&storage.Session{ID: "preview", CreatedAt: time.Now(), LastActive: time.Now(), Status: storage.SessionStatusActive}); err != nil {
		t.Fatal(err)
	}
	missions := mission.NewStore(store.DB())
	registry := NewEmptyRegistry()
	registry.Register(tool)
	registry.SetWorkDir(project)
	registry.EnableMissionControl(missions, "agent", true, 3*time.Second)
	registry.UpdateMissionSession("preview")
	return registry, store, missions
}

func TestApprovalMiddleware_ApprovedEvidenceStaysBound(t *testing.T) {
	for _, scenario := range []string{"changed", "created", "replaced", "retargeted", "arguments", "workspace", "rejected"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "note.txt")
			if scenario != "created" {
				if err := os.WriteFile(path, []byte("alpha beta"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var tool Tool = &builtin.WriteFileTool{}
			params := map[string]any{"path": "note.txt", "content": "approved"}
			if scenario == "arguments" {
				tool = &builtin.EditFileTool{}
				params = map[string]any{"path": "note.txt", "edits": []any{map[string]any{"old_string": "alpha", "new_string": "approved"}}}
			}
			registry, store, missions := newMissionPreviewRegistry(t, tool, root)
			type outcome struct {
				result *builtin.Result
				err    error
			}
			done := make(chan outcome, 1)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			go func() { res, err := registry.ExecuteWithContext(ctx, tool.Name(), params); done <- outcome{res, err} }()
			id := waitForPendingChange(t, store.DB())
			expected := "alpha beta"
			status := "approved"
			switch scenario {
			case "changed", "created":
				expected = "concurrent"
				if err := os.WriteFile(path, []byte(expected), 0600); err != nil {
					t.Fatal(err)
				}
			case "replaced":
				replacement := filepath.Join(root, "replacement")
				if err := os.WriteFile(replacement, []byte(expected), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(replacement, path); err != nil {
					t.Fatal(err)
				}
			case "retargeted":
				target := filepath.Join(root, "target")
				if err := os.WriteFile(target, []byte(expected), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "arguments":
				params["edits"].([]any)[0].(map[string]any)["new_string"] = "unapproved"
				params["path"] = "other.txt"
				expected = "approved beta"
			case "workspace":
				registry.SetWorkDir(t.TempDir())
			case "rejected":
				status = "rejected"
			}
			if err := missions.UpdatePendingChangeStatus(id, status, "fixture"); err != nil {
				t.Fatal(err)
			}
			select {
			case out := <-done:
				if out.err != nil || out.result == nil {
					t.Fatalf("execute: %+v", out)
				}
				if out.result.Success != (scenario == "arguments") {
					t.Fatalf("unexpected result: %+v", out.result)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("approval did not settle")
			}
			if got, err := os.ReadFile(path); err != nil || string(got) != expected {
				t.Fatalf("contents=%q want=%q err=%v", got, expected, err)
			}
		})
	}
}

func TestApprovalMiddleware_InvalidMutationNeverParks(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tool   Tool
		params map[string]any
	}{
		{"oversized-source", &builtin.WriteFileTool{}, map[string]any{"path": "note.txt", "content": "new"}},
		{"oversized-output", &builtin.EditFileTool{}, map[string]any{"path": "small.txt", "old_string": "abc", "new_string": "too much text"}},
		{"invalid-batch", &builtin.EditFileTool{}, map[string]any{"path": "small.txt", "edits": []any{map[string]any{"old_string": "abc", "new_string": "xyz"}, map[string]any{"old_string": "missing", "new_string": "oops"}}}},
		{"deep-symlink-escape", &builtin.WriteFileTool{}, map[string]any{"path": "link/missing/deep/file.txt", "content": "abc"}},
		{"oversized-creation", &builtin.WriteFileTool{}, map[string]any{"path": "new/deep/file.txt", "content": "too much text"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			outside := t.TempDir()
			for name, content := range map[string]string{"note.txt": "large source", "small.txt": "abc"} {
				if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
				t.Fatal(err)
			}
			registry, store, _ := newMissionPreviewRegistry(t, tc.tool, root)
			registry.SetMaxFileSizeBytes(4)
			result, err := registry.Execute(tc.tool.Name(), tc.params)
			if err != nil || result == nil || result.Success || strings.Contains(result.Error, "approval wait") {
				t.Fatalf("expected preflight rejection: %+v %v", result, err)
			}
			var count int
			if err := store.DB().QueryRow(`SELECT count(*) FROM pending_changes`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("pending=%d err=%v", count, err)
			}
			for _, dir := range []string{filepath.Join(root, "new"), filepath.Join(outside, "missing")} {
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Fatalf("preview created %s: %v", dir, err)
				}
			}
			if got, err := os.ReadFile(filepath.Join(root, "small.txt")); err != nil || string(got) != "abc" {
				t.Fatalf("preview changed source: %q %v", got, err)
			}
		})
	}
}
