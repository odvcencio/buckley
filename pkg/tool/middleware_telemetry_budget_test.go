package tool

import (
	"m31labs.dev/buckley/pkg/telemetry"
	"m31labs.dev/buckley/pkg/tool/builtin"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTelemetryMiddleware_UsesResolvedWriteTarget(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	t.Chdir(other)
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	hub := telemetry.NewHub()
	t.Cleanup(hub.Close)
	events, unsub := hub.Subscribe()
	t.Cleanup(unsub)
	registry := NewEmptyRegistry()
	registry.Register(&builtin.WriteFileTool{})
	registry.SetWorkDir(root)
	registry.SetMaxFileSizeBytes(4)
	registry.EnableTelemetry(hub, "owned")
	result, err := registry.Execute("write_file", map[string]any{"path": "note.txt", "content": "new"})
	if err != nil || !result.Success {
		t.Fatalf("execute=%+v err=%v", result, err)
	}
	got := drainTelemetryEvents(events)
	if len(got) != 2 {
		t.Fatalf("events=%d want2", len(got))
	}
	for i := 0; i < 2; i++ {
		event := got[i]
		if event.Data["addedLines"] != float64(1) || event.Data["removedLines"] != float64(1) {
			t.Fatalf("wrong workspace preview: %+v", event)
		}
	}
}

func TestTelemetryMiddleware_ReusesBatchDiff(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "note.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0600); err != nil {
		t.Fatal(err)
	}
	hub := telemetry.NewHub()
	t.Cleanup(hub.Close)
	events, unsub := hub.Subscribe()
	t.Cleanup(unsub)
	registry := NewEmptyRegistry()
	registry.Register(&builtin.EditFileTool{})
	registry.SetWorkDir(root)
	registry.EnableTelemetry(hub, "owned")
	result, err := registry.Execute("edit_file", map[string]any{"path": "note.txt", "old_string": "", "new_string": "", "edits": []any{map[string]any{"old_string": "alpha", "new_string": "FIRST"}, map[string]any{"old_string": "beta", "new_string": "SECOND"}}})
	if err != nil || !result.Success {
		t.Fatalf("execute=%+v err=%v", result, err)
	}
	got := drainTelemetryEvents(events)
	if len(got) != 2 {
		t.Fatalf("events=%d want2", len(got))
	}
	started, completed := got[0], got[1]
	if started.Data["addedLines"] != float64(0) || started.Data["removedLines"] != float64(0) {
		t.Fatalf("started event guessed batch effect: %+v", started)
	}
	if completed.Data["addedLines"] != float64(2) || completed.Data["removedLines"] != float64(2) {
		t.Fatalf("missing canonical batch counts: %+v", completed)
	}
}

func TestTelemetryMiddleware_DeniedFileHasNoPreview(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	hub := telemetry.NewHub()
	t.Cleanup(hub.Close)
	events, unsub := hub.Subscribe()
	t.Cleanup(unsub)
	registry := NewEmptyRegistry()
	registry.Register(&builtin.WriteFileTool{})
	registry.EnableTelemetry(hub, "owned")
	result, err := registry.Execute("write_file", map[string]any{"path": path, "content": "new\n"})
	if err != nil || result.Success || !strings.Contains(result.Error, "permission denied") {
		t.Fatalf("execute=%+v err=%v", result, err)
	}
	got := drainTelemetryEvents(events)
	if len(got) != 2 {
		t.Fatalf("events=%d want2", len(got))
	}
	for i := 0; i < 2; i++ {
		event := got[i]
		if event.Data["addedLines"] != float64(0) || event.Data["removedLines"] != float64(0) {
			t.Fatalf("denied preview: %+v", event)
		}
	}
}

func BenchmarkTelemetryMiddleware_RejectedFile(b *testing.B) {
	path := filepath.Join(b.TempDir(), "note.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("unchanged row\n", 350000)+"old\n"), 0600); err != nil {
		b.Fatal(err)
	}
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		b.Run(name, func(b *testing.B) {
			registry := NewEmptyRegistry()
			registry.Register(&builtin.EditFileTool{})
			registry.SetMaxFileSizeBytes(4)
			hub := telemetry.NewHub()
			b.Cleanup(hub.Close)
			if enabled {
				registry.EnableTelemetry(hub, "owned")
			}
			params := map[string]any{"path": path, "old_string": "old", "new_string": "new"}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				result, err := registry.Execute("edit_file", params)
				if err != nil || result.Success {
					b.Fatalf("expected rejection: %+v %v", result, err)
				}
			}
		})
	}
}
