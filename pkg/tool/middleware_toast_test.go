package tool

import (
	"strings"
	"testing"

	"github.com/odvcencio/buckley/pkg/tool/builtin"
	"github.com/odvcencio/buckley/pkg/ui/toast"
)

func TestToastNotificationsOnFailure(t *testing.T) {
	manager := toast.NewToastManager()
	var snapshots [][]*toast.Toast
	manager.SetOnChange(func(items []*toast.Toast) {
		copied := make([]*toast.Toast, len(items))
		copy(copied, items)
		snapshots = append(snapshots, copied)
	})

	mw := ToastNotifications(manager)
	exec := mw(func(ctx *ExecutionContext) (*builtin.Result, error) {
		return &builtin.Result{Success: false, Error: "boom"}, nil
	})

	_, _ = exec(&ExecutionContext{ToolName: "run_shell"})

	if len(snapshots) < 2 {
		t.Fatalf("expected toast snapshots, got %d", len(snapshots))
	}
	last := snapshots[len(snapshots)-1]
	if len(last) != 1 {
		t.Fatalf("expected 1 toast, got %#v", last)
	}
	if last[0].Level != toast.ToastError {
		t.Fatalf("expected error toast, got %s", last[0].Level)
	}
	if !strings.Contains(last[0].Title, "run_shell") {
		t.Fatalf("expected title to include tool name, got %q", last[0].Title)
	}
}

func TestToastNotificationsSkipsSuccess(t *testing.T) {
	manager := toast.NewToastManager()
	var snapshots [][]*toast.Toast
	manager.SetOnChange(func(items []*toast.Toast) {
		copied := make([]*toast.Toast, len(items))
		copy(copied, items)
		snapshots = append(snapshots, copied)
	})

	mw := ToastNotifications(manager)
	exec := mw(func(ctx *ExecutionContext) (*builtin.Result, error) {
		return &builtin.Result{Success: true}, nil
	})

	_, _ = exec(&ExecutionContext{ToolName: "read_file"})

	if len(snapshots) != 1 {
		t.Fatalf("expected only initial snapshot, got %d", len(snapshots))
	}
}
