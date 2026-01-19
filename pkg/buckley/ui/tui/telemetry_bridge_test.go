package tui

import (
	"context"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/touch"
	buckleywidgets "github.com/odvcencio/buckley/pkg/buckley/ui/widgets"
)

func TestTelemetryUIBridge_New(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	// Bridge can be created without an app (for testing event handling)
	bridge := NewTelemetryUIBridge(hub, nil)
	if bridge == nil {
		t.Fatal("expected non-nil bridge")
	}
	if bridge.hub != hub {
		t.Error("hub not set correctly")
	}
	if bridge.activeTouches == nil {
		t.Error("expected activeTouches map to be initialized")
	}
}

func TestTelemetryUIBridge_HandleTaskEvents(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	bridge := NewTelemetryUIBridge(hub, nil)

	// Simulate task started
	bridge.handleEvent(telemetry.Event{
		Type:   telemetry.EventTaskStarted,
		TaskID: "task-1",
		Data:   map[string]any{"name": "Build project"},
	})

	if bridge.currentTask != "Build project" {
		t.Errorf("expected currentTask 'Build project', got '%s'", bridge.currentTask)
	}
	if bridge.taskProgress != 0 {
		t.Errorf("expected taskProgress 0, got %d", bridge.taskProgress)
	}
}

func TestTelemetryUIBridge_HandlePlanUpdate(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	bridge := NewTelemetryUIBridge(hub, nil)

	// Simulate plan update with tasks
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

	if len(bridge.planTasks) != 3 {
		t.Fatalf("expected 3 plan tasks, got %d", len(bridge.planTasks))
	}
	if bridge.planTasks[0].Status != buckleywidgets.TaskCompleted {
		t.Errorf("expected first task completed, got %d", bridge.planTasks[0].Status)
	}
	if bridge.planTasks[1].Status != buckleywidgets.TaskInProgress {
		t.Errorf("expected second task in progress, got %d", bridge.planTasks[1].Status)
	}
	if bridge.planTasks[2].Status != buckleywidgets.TaskPending {
		t.Errorf("expected third task pending, got %d", bridge.planTasks[2].Status)
	}
}

func TestTelemetryUIBridge_HandleRunningTools(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	bridge := NewTelemetryUIBridge(hub, nil)

	// Simulate shell command started
	bridge.handleEvent(telemetry.Event{
		Type:   telemetry.EventShellCommandStarted,
		TaskID: "shell-1",
		Data:   map[string]any{"command": "go build ./..."},
	})

	if len(bridge.runningTools) != 1 {
		t.Fatalf("expected 1 running tool, got %d", len(bridge.runningTools))
	}

	tool := bridge.runningTools["shell-1"]
	if tool.Name != "shell" {
		t.Errorf("expected tool name 'shell', got '%s'", tool.Name)
	}
	if tool.Command != "go build ./..." {
		t.Errorf("expected command 'go build ./...', got '%s'", tool.Command)
	}

	// Simulate shell command completed
	bridge.handleEvent(telemetry.Event{
		Type:   telemetry.EventShellCommandCompleted,
		TaskID: "shell-1",
	})

	if len(bridge.runningTools) != 0 {
		t.Errorf("expected 0 running tools after completion, got %d", len(bridge.runningTools))
	}
}

func TestTelemetryUIBridge_HandleRecentFiles(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	bridge := NewTelemetryUIBridge(hub, nil)

	// Simulate editor events
	bridge.handleEvent(telemetry.Event{
		Type: telemetry.EventEditorApply,
		Data: map[string]any{"path": "pkg/main.go"},
	})
	bridge.handleEvent(telemetry.Event{
		Type: telemetry.EventEditorApply,
		Data: map[string]any{"path": "pkg/utils.go"},
	})

	if len(bridge.recentFiles) != 2 {
		t.Fatalf("expected 2 recent files, got %d", len(bridge.recentFiles))
	}
	// Most recent first
	if bridge.recentFiles[0] != "pkg/utils.go" {
		t.Errorf("expected first file 'pkg/utils.go', got '%s'", bridge.recentFiles[0])
	}

	// Adding same file again should move it to front
	bridge.handleEvent(telemetry.Event{
		Type: telemetry.EventEditorApply,
		Data: map[string]any{"path": "pkg/main.go"},
	})

	if len(bridge.recentFiles) != 2 {
		t.Errorf("expected still 2 files after re-adding, got %d", len(bridge.recentFiles))
	}
	if bridge.recentFiles[0] != "pkg/main.go" {
		t.Errorf("expected first file 'pkg/main.go' after re-add, got '%s'", bridge.recentFiles[0])
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

	if bridge.rlmStatus == nil {
		t.Fatalf("expected rlmStatus to be set")
	}
	if bridge.rlmStatus.Iteration != 2 {
		t.Fatalf("expected iteration 2, got %d", bridge.rlmStatus.Iteration)
	}
	if !bridge.rlmStatus.Ready {
		t.Fatalf("expected ready to be true")
	}
	if bridge.rlmStatus.TokensUsed != 1200 {
		t.Fatalf("expected tokens 1200, got %d", bridge.rlmStatus.TokensUsed)
	}
	if bridge.rlmStatus.Summary != "draft answer" {
		t.Fatalf("expected summary to be set")
	}
	if len(bridge.rlmScratchpad) != 1 {
		t.Fatalf("expected scratchpad entry to be recorded")
	}
	if bridge.rlmScratchpad[0].Type != "artifact" {
		t.Fatalf("expected scratchpad type to be artifact")
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

	if len(bridge.activeTouches) != 1 {
		t.Fatalf("expected 1 active touch, got %d", len(bridge.activeTouches))
	}
	entry := bridge.activeTouches["tool-1"]
	if entry.summary.Path != "pkg/buckley/ui/widgets/sidebar.go" {
		t.Errorf("expected touch path set, got %s", entry.summary.Path)
	}
	if entry.summary.Operation != "write" {
		t.Errorf("expected operation write, got %s", entry.summary.Operation)
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
	if len(bridge.activeTouches) != 0 {
		t.Errorf("expected active touches cleared, got %d", len(bridge.activeTouches))
	}
}

func TestTelemetryUIBridge_Truncate(t *testing.T) {
	tests := []struct {
		input    string
		max      int
		expected string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is a longer string", 10, "this is..."},
	}

	for _, tt := range tests {
		result := truncate(tt.input, tt.max)
		if result != tt.expected {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.max, result, tt.expected)
		}
	}
}

func TestTelemetryUIBridge_SetPlanTasks(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	bridge := NewTelemetryUIBridge(hub, nil)

	tasks := []buckleywidgets.PlanTask{
		{Name: "Task 1", Status: buckleywidgets.TaskCompleted},
		{Name: "Task 2", Status: buckleywidgets.TaskInProgress},
	}

	bridge.SetPlanTasks(tasks)

	if len(bridge.planTasks) != 2 {
		t.Errorf("expected 2 tasks, got %d", len(bridge.planTasks))
	}
}

func TestTelemetryUIBridge_HandleTaskFailed(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	bridge := NewTelemetryUIBridge(hub, nil)

	// Start a task first
	bridge.handleEvent(telemetry.Event{
		Type:   telemetry.EventTaskStarted,
		TaskID: "task-1",
		Data:   map[string]any{"name": "Build project"},
	})

	if bridge.currentTask != "Build project" {
		t.Errorf("expected currentTask 'Build project', got '%s'", bridge.currentTask)
	}

	// Now fail the task
	bridge.handleEvent(telemetry.Event{
		Type:   telemetry.EventTaskFailed,
		TaskID: "task-1",
	})

	// After failure, currentTask should be cleared
	if bridge.currentTask != "" {
		t.Errorf("expected empty currentTask after failure, got '%s'", bridge.currentTask)
	}
	if bridge.taskProgress != 0 {
		t.Errorf("expected taskProgress 0 after failure, got %d", bridge.taskProgress)
	}
}

func TestTelemetryUIBridge_HandleTaskCompleted(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	bridge := NewTelemetryUIBridge(hub, nil)

	// Start a task first
	bridge.handleEvent(telemetry.Event{
		Type:   telemetry.EventTaskStarted,
		TaskID: "task-1",
		Data:   map[string]any{"name": "Build project"},
	})

	// Complete the task
	bridge.handleEvent(telemetry.Event{
		Type:   telemetry.EventTaskCompleted,
		TaskID: "task-1",
	})

	// Task progress should be 100 immediately after completion
	if bridge.TaskProgress() != 100 {
		t.Errorf("expected taskProgress 100 after completion, got %d", bridge.TaskProgress())
	}
}

func TestTelemetryUIBridge_UpdateTaskStatus(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	bridge := NewTelemetryUIBridge(hub, nil)

	// Set up plan tasks
	bridge.planTasks = []buckleywidgets.PlanTask{
		{Name: "task-1", Status: buckleywidgets.TaskPending},
		{Name: "task-2", Status: buckleywidgets.TaskPending},
	}

	// Update status of task-1
	bridge.updateTaskStatus("task-1", buckleywidgets.TaskCompleted)

	if bridge.planTasks[0].Status != buckleywidgets.TaskCompleted {
		t.Errorf("expected task-1 to be completed, got %d", bridge.planTasks[0].Status)
	}
	if bridge.planTasks[1].Status != buckleywidgets.TaskPending {
		t.Errorf("expected task-2 to still be pending, got %d", bridge.planTasks[1].Status)
	}

	// Update non-existent task (should not panic)
	bridge.updateTaskStatus("task-999", buckleywidgets.TaskFailed)
}

func TestTelemetryUIBridge_HandleBuilderEvents(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	bridge := NewTelemetryUIBridge(hub, nil)

	// Simulate builder started
	bridge.handleEvent(telemetry.Event{
		Type:   telemetry.EventBuilderStarted,
		TaskID: "builder-1",
	})

	if len(bridge.runningTools) != 1 {
		t.Fatalf("expected 1 running tool, got %d", len(bridge.runningTools))
	}

	tool := bridge.runningTools["builder-1"]
	if tool.Name != "builder" {
		t.Errorf("expected tool name 'builder', got '%s'", tool.Name)
	}

	// Simulate builder completed
	bridge.handleEvent(telemetry.Event{
		Type:   telemetry.EventBuilderCompleted,
		TaskID: "builder-1",
	})

	if len(bridge.runningTools) != 0 {
		t.Errorf("expected 0 running tools after completion, got %d", len(bridge.runningTools))
	}
}

func TestTelemetryUIBridge_HandleBuilderFailed(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	bridge := NewTelemetryUIBridge(hub, nil)

	// Simulate builder started
	bridge.handleEvent(telemetry.Event{
		Type:   telemetry.EventBuilderStarted,
		TaskID: "builder-1",
	})

	// Simulate builder failed
	bridge.handleEvent(telemetry.Event{
		Type:   telemetry.EventBuilderFailed,
		TaskID: "builder-1",
	})

	if len(bridge.runningTools) != 0 {
		t.Errorf("expected 0 running tools after failure, got %d", len(bridge.runningTools))
	}
}

func TestTelemetryUIBridge_HandleResearchEvents(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	bridge := NewTelemetryUIBridge(hub, nil)

	// Simulate research started
	bridge.handleEvent(telemetry.Event{
		Type: telemetry.EventResearchStarted,
		Data: map[string]any{"query": "find all Go files"},
	})

	if len(bridge.runningTools) != 1 {
		t.Fatalf("expected 1 running tool, got %d", len(bridge.runningTools))
	}

	tool := bridge.runningTools["research"]
	if tool.Name != "research" {
		t.Errorf("expected tool name 'research', got '%s'", tool.Name)
	}
	if tool.Command != "find all Go files" {
		t.Errorf("expected command 'find all Go files', got '%s'", tool.Command)
	}

	// Simulate research completed
	bridge.handleEvent(telemetry.Event{
		Type: telemetry.EventResearchCompleted,
	})

	if len(bridge.runningTools) != 0 {
		t.Errorf("expected 0 running tools after completion, got %d", len(bridge.runningTools))
	}
}

func TestTelemetryUIBridge_HandleResearchFailed(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	bridge := NewTelemetryUIBridge(hub, nil)

	// Simulate research started with no query
	bridge.handleEvent(telemetry.Event{
		Type: telemetry.EventResearchStarted,
		Data: map[string]any{},
	})

	// Simulate research failed
	bridge.handleEvent(telemetry.Event{
		Type: telemetry.EventResearchFailed,
	})

	if len(bridge.runningTools) != 0 {
		t.Errorf("expected 0 running tools after failure, got %d", len(bridge.runningTools))
	}
}

func TestTelemetryUIBridge_HandleShellCommandFailed(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	bridge := NewTelemetryUIBridge(hub, nil)

	// Simulate shell command started with no command data
	bridge.handleEvent(telemetry.Event{
		Type:   telemetry.EventShellCommandStarted,
		TaskID: "shell-1",
		Data:   map[string]any{},
	})

	if len(bridge.runningTools) != 1 {
		t.Fatalf("expected 1 running tool, got %d", len(bridge.runningTools))
	}

	// Command should be empty string since no command was provided
	tool := bridge.runningTools["shell-1"]
	if tool.Command != "" {
		t.Errorf("expected empty command, got '%s'", tool.Command)
	}

	// Simulate shell command failed
	bridge.handleEvent(telemetry.Event{
		Type:   telemetry.EventShellCommandFailed,
		TaskID: "shell-1",
	})

	if len(bridge.runningTools) != 0 {
		t.Errorf("expected 0 running tools after failure, got %d", len(bridge.runningTools))
	}
}

func TestTelemetryUIBridge_HandleEditorInline(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	bridge := NewTelemetryUIBridge(hub, nil)

	// Simulate editor inline event
	bridge.handleEvent(telemetry.Event{
		Type: telemetry.EventEditorInline,
		Data: map[string]any{"path": "pkg/test.go"},
	})

	if len(bridge.recentFiles) != 1 {
		t.Fatalf("expected 1 recent file, got %d", len(bridge.recentFiles))
	}
	if bridge.recentFiles[0] != "pkg/test.go" {
		t.Errorf("expected 'pkg/test.go', got '%s'", bridge.recentFiles[0])
	}
}

func TestTelemetryUIBridge_RecentFilesLimit(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	bridge := NewTelemetryUIBridge(hub, nil)

	// Add more than 5 files
	files := []string{"file0.go", "file1.go", "file2.go", "file3.go", "file4.go", "file5.go", "file6.go", "file7.go", "file8.go", "file9.go"}
	for _, f := range files {
		bridge.handleEvent(telemetry.Event{
			Type: telemetry.EventEditorApply,
			Data: map[string]any{"path": f},
		})
	}

	// Should be capped at 5
	if len(bridge.recentFiles) != 5 {
		t.Errorf("expected 5 recent files (capped), got %d", len(bridge.recentFiles))
	}

	// Most recent should be last added
	if bridge.recentFiles[0] != "file9.go" {
		t.Errorf("expected 'file9.go' at front, got '%s'", bridge.recentFiles[0])
	}
}

func TestTelemetryUIBridge_HandlePlanCreated(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	bridge := NewTelemetryUIBridge(hub, nil)

	// Simulate plan created with tasks
	bridge.handleEvent(telemetry.Event{
		Type: telemetry.EventPlanCreated,
		Data: map[string]any{
			"tasks": []any{
				map[string]any{"name": "Task 1", "status": "done"},
				map[string]any{"name": "Task 2", "status": "running"},
				map[string]any{"name": "Task 3", "status": "failed"},
				map[string]any{"name": "Task 4"}, // No status
			},
		},
	})

	if len(bridge.planTasks) != 4 {
		t.Fatalf("expected 4 plan tasks, got %d", len(bridge.planTasks))
	}
	if bridge.planTasks[0].Status != buckleywidgets.TaskCompleted {
		t.Errorf("expected first task completed (done), got %d", bridge.planTasks[0].Status)
	}
	if bridge.planTasks[1].Status != buckleywidgets.TaskInProgress {
		t.Errorf("expected second task in progress (running), got %d", bridge.planTasks[1].Status)
	}
	if bridge.planTasks[2].Status != buckleywidgets.TaskFailed {
		t.Errorf("expected third task failed, got %d", bridge.planTasks[2].Status)
	}
	if bridge.planTasks[3].Status != buckleywidgets.TaskPending {
		t.Errorf("expected fourth task pending (no status), got %d", bridge.planTasks[3].Status)
	}
}

func TestTelemetryUIBridge_HandlePlanUpdateNoTasks(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	bridge := NewTelemetryUIBridge(hub, nil)

	// Set initial tasks
	bridge.planTasks = []buckleywidgets.PlanTask{
		{Name: "Old task", Status: buckleywidgets.TaskPending},
	}

	// Simulate plan update without tasks key
	bridge.handleEvent(telemetry.Event{
		Type: telemetry.EventPlanUpdated,
		Data: map[string]any{
			"name": "my-plan",
		},
	})

	// Old tasks should remain
	if len(bridge.planTasks) != 1 {
		t.Errorf("expected tasks to remain when no tasks in update, got %d", len(bridge.planTasks))
	}
}

func TestTelemetryUIBridge_HandleTaskStartedNoName(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	bridge := NewTelemetryUIBridge(hub, nil)

	// Start task without name in data (should use TaskID)
	bridge.handleEvent(telemetry.Event{
		Type:   telemetry.EventTaskStarted,
		TaskID: "task-id-123",
		Data:   map[string]any{},
	})

	// Should use TaskID as name
	if bridge.currentTask != "task-id-123" {
		t.Errorf("expected currentTask 'task-id-123', got '%s'", bridge.currentTask)
	}
}

func TestTelemetryUIBridge_GetString(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]any
		key      string
		expected string
	}{
		{
			name:     "existing string key",
			input:    map[string]any{"name": "test"},
			key:      "name",
			expected: "test",
		},
		{
			name:     "missing key",
			input:    map[string]any{"name": "test"},
			key:      "other",
			expected: "",
		},
		{
			name:     "non-string value",
			input:    map[string]any{"count": 42},
			key:      "count",
			expected: "",
		},
		{
			name:     "nil map",
			input:    nil,
			key:      "name",
			expected: "",
		},
		{
			name:     "empty map",
			input:    map[string]any{},
			key:      "name",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getString(tt.input, tt.key)
			if result != tt.expected {
				t.Errorf("getString(%v, %q) = %q, want %q", tt.input, tt.key, result, tt.expected)
			}
		})
	}
}

func TestTelemetryUIBridge_TruncateEdgeCases(t *testing.T) {
	tests := []struct {
		input    string
		max      int
		expected string
	}{
		{"", 10, ""},
		{"a", 10, "a"},
		{"abc", 3, "abc"},
		{"abcd", 3, "..."},
		{"ab", 3, "ab"},
	}

	for _, tt := range tests {
		result := truncate(tt.input, tt.max)
		if result != tt.expected {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.max, result, tt.expected)
		}
	}
}

func TestTelemetryUIBridge_StartStop(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	bridge := NewTelemetryUIBridge(hub, nil)

	// Start the bridge
	ctx := context.Background()
	bridge.Start(ctx)

	// Give the goroutine time to start
	time.Sleep(10 * time.Millisecond)

	// Stop the bridge
	bridge.Stop()

	// Should be able to stop multiple times without panic
	bridge.Stop()
}

func TestTelemetryUIBridge_StartWithCancelledContext(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	bridge := NewTelemetryUIBridge(hub, nil)

	// Create a context that we'll cancel immediately
	ctx, cancel := context.WithCancel(context.Background())

	// Start the bridge
	bridge.Start(ctx)

	// Cancel the context
	cancel()

	// Give time for the goroutine to notice the cancellation
	time.Sleep(10 * time.Millisecond)

	// Stop should complete quickly since context is cancelled
	bridge.Stop()
}

func TestTelemetryUIBridge_ForwardLoopProcessesEvents(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	bridge := NewTelemetryUIBridge(hub, nil)

	// Start the bridge
	ctx := context.Background()
	bridge.Start(ctx)

	// Publish an event through the hub
	hub.Publish(telemetry.Event{
		Type:   telemetry.EventTaskStarted,
		TaskID: "test-task",
		Data:   map[string]any{"name": "Test Task"},
	})

	// Give time for the event to be processed
	time.Sleep(50 * time.Millisecond)

	// Verify the event was processed
	bridge.mu.Lock()
	currentTask := bridge.currentTask
	bridge.mu.Unlock()

	if currentTask != "Test Task" {
		t.Errorf("expected currentTask 'Test Task', got '%s'", currentTask)
	}

	// Stop the bridge
	bridge.Stop()
}

func TestTelemetryUIBridge_ForwardLoopHandlesChannelClose(t *testing.T) {
	hub := telemetry.NewHub()

	bridge := NewTelemetryUIBridge(hub, nil)

	// Start the bridge
	ctx := context.Background()
	bridge.Start(ctx)

	// Give time for the goroutine to start
	time.Sleep(10 * time.Millisecond)

	// Close the hub (which should close the event channel)
	hub.Close()

	// Give time for the goroutine to notice the channel close
	time.Sleep(10 * time.Millisecond)

	// Stop should complete quickly since channel is closed
	bridge.Stop()
}

func TestTelemetryUIBridge_StopWithNilCancel(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	bridge := NewTelemetryUIBridge(hub, nil)

	// Stop without ever starting (cancel is nil)
	// Should not panic
	bridge.Stop()
}

func TestTelemetryUIBridge_StopWithNilUnsubscribe(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	bridge := &TelemetryUIBridge{
		hub:          hub,
		app:          nil,
		runningTools: make(map[string]buckleywidgets.RunningTool),
		// eventCh and unsubscribe are nil
	}

	// Stop with nil unsubscribe should not panic
	bridge.Stop()
}
