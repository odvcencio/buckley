package tool

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/odvcencio/buckley/pkg/filewatch"
	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

func TestFileChangeTracking_TracksWriteFile(t *testing.T) {
	watcher := filewatch.NewFileWatcher(10)
	var changes []filewatch.FileChange
	watcher.Subscribe("*", func(change filewatch.FileChange) {
		changes = append(changes, change)
	})

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "note.txt")

	mw := FileChangeTracking(watcher)
	exec := mw(func(ctx *ExecutionContext) (*builtin.Result, error) {
		if err := os.WriteFile(path, []byte("hello"), 0644); err != nil {
			return nil, err
		}
		return &builtin.Result{
			Success: true,
			Data: map[string]any{
				"path": path,
			},
			DisplayData: map[string]any{
				"is_new": true,
			},
		}, nil
	})

	_, err := exec(&ExecutionContext{
		ToolName: "write_file",
		CallID:   "call-1",
		Params: map[string]any{
			"path": path,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if changes[0].Type != filewatch.ChangeCreated {
		t.Fatalf("expected created change, got %q", changes[0].Type)
	}
	if changes[0].CallID != "call-1" {
		t.Fatalf("expected call ID recorded, got %q", changes[0].CallID)
	}
}

func TestFileChangeTracking_TracksApplyPatch(t *testing.T) {
	watcher := filewatch.NewFileWatcher(10)
	var changes []filewatch.FileChange
	watcher.Subscribe("*", func(change filewatch.FileChange) {
		changes = append(changes, change)
	})

	patch := "diff --git a/foo.txt b/foo.txt\n--- a/foo.txt\n+++ b/foo.txt\n@@ -1 +1 @@\n-old\n+new\n"

	mw := FileChangeTracking(watcher)
	exec := mw(func(ctx *ExecutionContext) (*builtin.Result, error) {
		return &builtin.Result{Success: true}, nil
	})

	_, err := exec(&ExecutionContext{
		ToolName: "apply_patch",
		CallID:   "call-2",
		Params: map[string]any{
			"patch": patch,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if changes[0].Path != "foo.txt" {
		t.Fatalf("expected patch path foo.txt, got %q", changes[0].Path)
	}
	if changes[0].Type != filewatch.ChangeModified {
		t.Fatalf("expected modified change, got %q", changes[0].Type)
	}
}

func TestFileChangeTracking_SkipsFailures(t *testing.T) {
	watcher := filewatch.NewFileWatcher(10)
	called := false
	watcher.Subscribe("*", func(change filewatch.FileChange) {
		called = true
	})

	mw := FileChangeTracking(watcher)
	exec := mw(func(ctx *ExecutionContext) (*builtin.Result, error) {
		return &builtin.Result{Success: false}, nil
	})

	_, err := exec(&ExecutionContext{ToolName: "write_file"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Fatalf("expected no change notifications")
	}
}
