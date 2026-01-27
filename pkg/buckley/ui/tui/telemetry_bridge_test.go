package tui

import (
	"testing"
	"time"

	buckleywidgets "github.com/odvcencio/buckley/pkg/buckley/ui/widgets"
	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/touch"
)

func TestTelemetryUIBridge_New(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	bridge := NewTelemetryUIBridge(hub, nil)
	if bridge == nil {
		t.Fatal("expected non-nil bridge")
	}
	if bridge.hub != hub {
		t.Error("hub not set correctly")
	}
	if bridge.touchEntries == nil {
		t.Error("expected touch signal to be initialized")
	}
}

func TestTelemetryUIBridge_HandleTaskEvents(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	bridge := NewTelemetryUIBridge(hub, nil)

	bridge.handleEvent(telemetry.Event{
		Type:   telemetry.EventTaskStarted,
		TaskID: "task-1",
		Data:   map[string]any{"name": "Build project"},
	})

	if bridge.currentTask.Get() != "Build project" {
		t.Errorf("expected currentTask 'Build project', got %q", bridge.currentTask.Get())
	}
	if bridge.taskProgress.Get() != 0 {
		t.Errorf("expected taskProgress 0, got %d", bridge.taskProgress.Get())
	}
}

func TestTelemetryUIBridge_HandlePlanUpdate(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	bridge := NewTelemetryUIBridge(hub, nil)

	bridge.handleEvent(telemetry.Event{
		Type: telemetry.EventPlanUpdated,
		Data: map[string]any{
			"tasks": []any{
				map[string]any{"name": "Task 1", "status": "completed"},
				map[string]any{"name": "Task 2", "status": "in_progress"},
				map[string]any{"name": "Task 3", "status": "pending"},
			},
		},
	})

	plan := bridge.planTasks.Get()
	if len(plan) != 3 {
		t.Fatalf("expected 3 plan tasks, got %d", len(plan))
	}
	if plan[0].Status != buckleywidgets.TaskCompleted {
		t.Errorf("expected first task completed, got %d", plan[0].Status)
	}
	if plan[1].Status != buckleywidgets.TaskInProgress {
		t.Errorf("expected second task in progress, got %d", plan[1].Status)
	}
	if plan[2].Status != buckleywidgets.TaskPending {
		t.Errorf("expected third task pending, got %d", plan[2].Status)
	}
}

func TestTelemetryUIBridge_HandleRunningTools(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	bridge := NewTelemetryUIBridge(hub, nil)

	bridge.handleEvent(telemetry.Event{
		Type:   telemetry.EventShellCommandStarted,
		TaskID: "shell-1",
		Data:   map[string]any{"command": "go build ./..."},
	})

	tools := bridge.runningTools.Get()
	if len(tools) != 1 {
		t.Fatalf("expected 1 running tool, got %d", len(tools))
	}
	if tools[0].Name != "shell" {
		t.Errorf("expected tool name 'shell', got %q", tools[0].Name)
	}
	if tools[0].Command != "go build ./..." {
		t.Errorf("expected command 'go build ./...', got %q", tools[0].Command)
	}

	bridge.handleEvent(telemetry.Event{
		Type:   telemetry.EventShellCommandCompleted,
		TaskID: "shell-1",
	})

	if len(bridge.runningTools.Get()) != 0 {
		t.Errorf("expected 0 running tools after completion, got %d", len(bridge.runningTools.Get()))
	}
}

func TestTelemetryUIBridge_HandleRecentFiles(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	bridge := NewTelemetryUIBridge(hub, nil)

	bridge.handleEvent(telemetry.Event{
		Type: telemetry.EventEditorApply,
		Data: map[string]any{"path": "pkg/main.go"},
	})
	bridge.handleEvent(telemetry.Event{
		Type: telemetry.EventEditorApply,
		Data: map[string]any{"path": "pkg/utils.go"},
	})

	files := bridge.recentFiles.Get()
	if len(files) != 2 {
		t.Fatalf("expected 2 recent files, got %d", len(files))
	}
	if files[0] != "pkg/utils.go" {
		t.Errorf("expected first file 'pkg/utils.go', got %q", files[0])
	}

	bridge.handleEvent(telemetry.Event{
		Type: telemetry.EventEditorApply,
		Data: map[string]any{"path": "pkg/main.go"},
	})

	files = bridge.recentFiles.Get()
	if files[0] != "pkg/main.go" {
		t.Errorf("expected first file 'pkg/main.go' after re-add, got %q", files[0])
	}
}

func TestTelemetryUIBridge_HandleToolEvents(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	bridge := NewTelemetryUIBridge(hub, nil)
	expiresAt := time.Now().Add(5 * time.Minute).Truncate(time.Second)

	bridge.handleEvent(telemetry.Event{
		Type:      telemetry.EventToolStarted,
		TaskID:    "tool-1",
		Timestamp: time.Now(),
		Data: map[string]any{
			"filePath":      "pkg/buckley/ui/widgets/sidebar.go",
			"operationType": "write",
			"ranges":        []touch.LineRange{{Start: 10, End: 20}},
			"expiresAt":     expiresAt.Format(time.RFC3339),
		},
	})

	entries := bridge.touchEntries.Get()
	if len(entries) != 1 {
		t.Fatalf("expected 1 touch entry, got %d", len(entries))
	}
	entry := entries[0]
	if entry.summary.Path != "pkg/buckley/ui/widgets/sidebar.go" {
		t.Errorf("expected touch path set, got %q", entry.summary.Path)
	}
	if entry.summary.Operation != "write" {
		t.Errorf("expected operation write, got %q", entry.summary.Operation)
	}
	if len(entry.summary.Ranges) != 1 || entry.summary.Ranges[0].Start != 10 || entry.summary.Ranges[0].End != 20 {
		t.Errorf("expected range 10-20, got %+v", entry.summary.Ranges)
	}
	if !entry.expiresAt.Equal(expiresAt) {
		t.Errorf("expected expiresAt %s, got %s", expiresAt, entry.expiresAt)
	}

	bridge.handleEvent(telemetry.Event{
		Type:   telemetry.EventToolCompleted,
		TaskID: "tool-1",
	})
	if len(bridge.touchEntries.Get()) != 0 {
		t.Errorf("expected touch entries cleared, got %d", len(bridge.touchEntries.Get()))
	}
}

func TestTelemetryUIBridge_HandleRLMIteration(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	bridge := NewTelemetryUIBridge(hub, nil)

	bridge.handleEvent(telemetry.Event{
		Type: telemetry.EventRLMIteration,
		Data: map[string]any{
			"iteration":      2,
			"max_iterations": 5,
			"ready":          true,
			"tokens_used":    1200,
			"summary":        "draft answer",
			"scratchpad": []map[string]any{
				{
					"key":     "artifacts/patch-1",
					"type":    "artifact",
					"summary": "generated patch",
				},
			},
		},
	})

	status := bridge.rlmStatus.Get()
	if status == nil {
		t.Fatalf("expected rlmStatus to be set")
	}
	if status.Iteration != 2 {
		t.Fatalf("expected iteration 2, got %d", status.Iteration)
	}
	if !status.Ready {
		t.Fatalf("expected ready to be true")
	}
	if status.TokensUsed != 1200 {
		t.Fatalf("expected tokens 1200, got %d", status.TokensUsed)
	}
	if status.Summary != "draft answer" {
		t.Fatalf("expected summary to be set")
	}
	if len(bridge.rlmScratchpad.Get()) != 1 {
		t.Fatalf("expected scratchpad entry to be recorded")
	}
}

func TestTelemetryUIBridge_Truncate(t *testing.T) {
	cases := []struct {
		input    string
		max      int
		expected string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is a longer string", 10, "this is..."},
	}

	for _, tt := range cases {
		result := truncate(tt.input, tt.max)
		if result != tt.expected {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.max, result, tt.expected)
		}
	}
}
