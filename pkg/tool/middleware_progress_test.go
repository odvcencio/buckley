package tool

import (
	"testing"

	"github.com/odvcencio/buckley/pkg/tool/builtin"
	"github.com/odvcencio/fluffyui/progress"
)

func TestProgressMiddlewareTracksLongRunningTools(t *testing.T) {
	manager := progress.NewProgressManager()
	var snapshots [][]progress.Progress
	manager.SetOnChange(func(items []progress.Progress) {
		copied := make([]progress.Progress, len(items))
		copy(copied, items)
		snapshots = append(snapshots, copied)
	})

	mw := Progress(manager, map[string]string{"run_shell": "Shell command"})
	exec := mw(func(ctx *ExecutionContext) (*builtin.Result, error) {
		return &builtin.Result{Success: true}, nil
	})

	_, err := exec(&ExecutionContext{ToolName: "run_shell", CallID: "call-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(snapshots) < 3 {
		t.Fatalf("expected snapshots for start/done, got %d", len(snapshots))
	}
	started := snapshots[1]
	if len(started) != 1 || started[0].ID != "call-1" {
		t.Fatalf("expected progress entry for call-1, got %#v", started)
	}
	done := snapshots[len(snapshots)-1]
	if len(done) != 0 {
		t.Fatalf("expected empty progress after done, got %#v", done)
	}
}

func TestProgressMiddlewareSkipsUnknownTools(t *testing.T) {
	manager := progress.NewProgressManager()
	var snapshots [][]progress.Progress
	manager.SetOnChange(func(items []progress.Progress) {
		copied := make([]progress.Progress, len(items))
		copy(copied, items)
		snapshots = append(snapshots, copied)
	})

	mw := Progress(manager, map[string]string{"run_shell": "Shell command"})
	exec := mw(func(ctx *ExecutionContext) (*builtin.Result, error) {
		return &builtin.Result{Success: true}, nil
	})

	_, err := exec(&ExecutionContext{ToolName: "read_file"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(snapshots) != 1 {
		t.Fatalf("expected only initial snapshot, got %d", len(snapshots))
	}
}
