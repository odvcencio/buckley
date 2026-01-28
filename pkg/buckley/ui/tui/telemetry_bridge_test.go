package tui

import (
	"testing"
	"time"

	buckleywidgets "github.com/odvcencio/buckley/pkg/buckley/ui/widgets"
	"github.com/odvcencio/buckley/pkg/telemetry"
)

// =============================================================================
// SimpleTelemetryBridge Tests (for Runner)
// =============================================================================

// mockTelemetryPoster collects posted messages for verification.
type mockTelemetryPoster struct {
	messages []Message
}

func (m *mockTelemetryPoster) Post(msg Message) {
	m.messages = append(m.messages, msg)
}

func TestSimpleTelemetryBridge_New(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	poster := &mockTelemetryPoster{}
	bridge := NewSimpleTelemetryBridge(hub, poster)
	if bridge == nil {
		t.Fatal("expected non-nil bridge")
	}
	if bridge.hub != hub {
		t.Error("hub not set correctly")
	}
	if bridge.poster != poster {
		t.Error("poster not set correctly")
	}
}

func TestSimpleTelemetryBridge_NewNilHub(t *testing.T) {
	poster := &mockTelemetryPoster{}
	bridge := NewSimpleTelemetryBridge(nil, poster)
	if bridge == nil {
		t.Fatal("expected non-nil bridge even with nil hub")
	}
	// Should not panic when starting with nil hub
	bridge.Start(nil)
	bridge.Stop()
}

func TestSimpleTelemetryBridge_HandleTaskEvents(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	poster := &mockTelemetryPoster{}
	bridge := NewSimpleTelemetryBridge(hub, poster)

	bridge.handleEvent(telemetry.Event{
		Type:   telemetry.EventTaskStarted,
		TaskID: "task-1",
		Data:   map[string]any{"name": "Build project"},
	})

	if bridge.currentTask != "Build project" {
		t.Errorf("expected currentTask 'Build project', got %q", bridge.currentTask)
	}
	if bridge.taskProgress != 0 {
		t.Errorf("expected taskProgress 0, got %d", bridge.taskProgress)
	}
}

func TestSimpleTelemetryBridge_HandleTaskCompleted(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	poster := &mockTelemetryPoster{}
	bridge := NewSimpleTelemetryBridge(hub, poster)

	// Start a task first
	bridge.handleEvent(telemetry.Event{
		Type:   telemetry.EventTaskStarted,
		TaskID: "task-1",
		Data:   map[string]any{"name": "Test task"},
	})

	// Then complete it
	bridge.handleEvent(telemetry.Event{
		Type:   telemetry.EventTaskCompleted,
		TaskID: "task-1",
	})

	if bridge.taskProgress != 100 {
		t.Errorf("expected taskProgress 100, got %d", bridge.taskProgress)
	}
}

func TestSimpleTelemetryBridge_HandleToolEvents(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	poster := &mockTelemetryPoster{}
	bridge := NewSimpleTelemetryBridge(hub, poster)

	bridge.handleEvent(telemetry.Event{
		Type:      telemetry.EventToolStarted,
		TaskID:    "tool-1",
		Timestamp: time.Now(),
		Data:      map[string]any{"toolName": "bash", "command": "ls -la"},
	})

	if len(bridge.runningTools) != 1 {
		t.Errorf("expected 1 running tool, got %d", len(bridge.runningTools))
	}
	if bridge.runningTools[0].Name != "bash" {
		t.Errorf("expected tool name 'bash', got %q", bridge.runningTools[0].Name)
	}

	// Complete the tool
	bridge.handleEvent(telemetry.Event{
		Type:      telemetry.EventToolCompleted,
		TaskID:    "tool-1",
		Timestamp: time.Now(),
	})

	if len(bridge.runningTools) != 0 {
		t.Errorf("expected 0 running tools after completion, got %d", len(bridge.runningTools))
	}
}

func TestSimpleTelemetryBridge_HandleRLMIteration(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	poster := &mockTelemetryPoster{}
	bridge := NewSimpleTelemetryBridge(hub, poster)

	bridge.handleEvent(telemetry.Event{
		Type: telemetry.EventRLMIteration,
		Data: map[string]any{
			"iteration":      2,
			"max_iterations": 5,
			"ready":          true,
			"tokens_used":    1200,
			"summary":        "draft answer",
		},
	})

	if bridge.rlmStatus == nil {
		t.Fatal("expected rlmStatus to be set")
	}
	if bridge.rlmStatus.Iteration != 2 {
		t.Errorf("expected iteration 2, got %d", bridge.rlmStatus.Iteration)
	}
	if !bridge.rlmStatus.Ready {
		t.Error("expected ready to be true")
	}
	if bridge.rlmStatus.TokensUsed != 1200 {
		t.Errorf("expected tokens 1200, got %d", bridge.rlmStatus.TokensUsed)
	}
}

func TestSimpleTelemetryBridge_SetPlanTasks(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	poster := &mockTelemetryPoster{}
	bridge := NewSimpleTelemetryBridge(hub, poster)

	tasks := []buckleywidgets.PlanTask{
		{Name: "Task 1", Status: buckleywidgets.TaskPending},
		{Name: "Task 2", Status: buckleywidgets.TaskInProgress},
	}
	bridge.SetPlanTasks(tasks)

	if len(bridge.planTasks) != 2 {
		t.Errorf("expected 2 plan tasks, got %d", len(bridge.planTasks))
	}
	if bridge.planTasks[0].Name != "Task 1" {
		t.Errorf("expected first task name 'Task 1', got %q", bridge.planTasks[0].Name)
	}
}

func TestSimpleTelemetryBridge_PostsSnapshot(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	poster := &mockTelemetryPoster{}
	bridge := NewSimpleTelemetryBridge(hub, poster)

	// Handle an event - should post a snapshot
	bridge.handleEvent(telemetry.Event{
		Type:   telemetry.EventTaskStarted,
		TaskID: "task-1",
		Data:   map[string]any{"name": "Test"},
	})
	bridge.postSnapshot()

	if len(poster.messages) == 0 {
		t.Error("expected at least one message to be posted")
	}

	// Verify it's a SidebarStateMsg
	if _, ok := poster.messages[0].(SidebarStateMsg); !ok {
		t.Errorf("expected SidebarStateMsg, got %T", poster.messages[0])
	}
}

func TestSimpleTelemetryBridge_HandlePlanUpdate(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	poster := &mockTelemetryPoster{}
	bridge := NewSimpleTelemetryBridge(hub, poster)

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

// =============================================================================
// Helper function tests
// =============================================================================

func TestTruncate(t *testing.T) {
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

func TestFirstNonEmpty(t *testing.T) {
	cases := []struct {
		values   []string
		expected string
	}{
		{[]string{"", "b", "c"}, "b"},
		{[]string{"a", "b", "c"}, "a"},
		{[]string{"", "", "c"}, "c"},
		{[]string{"", "", ""}, ""},
	}

	for _, tt := range cases {
		result := firstNonEmpty(tt.values...)
		if result != tt.expected {
			t.Errorf("firstNonEmpty(%v) = %q, want %q", tt.values, result, tt.expected)
		}
	}
}

func TestGetInt(t *testing.T) {
	m := map[string]any{
		"int":    42,
		"int32":  int32(32),
		"int64":  int64(64),
		"float":  float64(100),
		"string": "not a number",
	}

	if got := getInt(m, "int"); got != 42 {
		t.Errorf("getInt(int) = %d, want 42", got)
	}
	if got := getInt(m, "int32"); got != 32 {
		t.Errorf("getInt(int32) = %d, want 32", got)
	}
	if got := getInt(m, "int64"); got != 64 {
		t.Errorf("getInt(int64) = %d, want 64", got)
	}
	if got := getInt(m, "float"); got != 100 {
		t.Errorf("getInt(float) = %d, want 100", got)
	}
	if got := getInt(m, "string"); got != 0 {
		t.Errorf("getInt(string) = %d, want 0", got)
	}
	if got := getInt(m, "missing"); got != 0 {
		t.Errorf("getInt(missing) = %d, want 0", got)
	}
}

func TestUpsertRunningTool(t *testing.T) {
	tools := []buckleywidgets.RunningTool{
		{ID: "1", Name: "tool1"},
		{ID: "2", Name: "tool2"},
	}

	// Update existing
	tools = upsertRunningTool(tools, buckleywidgets.RunningTool{ID: "1", Name: "updated"})
	if tools[0].Name != "updated" {
		t.Errorf("expected updated name, got %q", tools[0].Name)
	}
	if len(tools) != 2 {
		t.Errorf("expected 2 tools, got %d", len(tools))
	}

	// Insert new
	tools = upsertRunningTool(tools, buckleywidgets.RunningTool{ID: "3", Name: "tool3"})
	if len(tools) != 3 {
		t.Errorf("expected 3 tools, got %d", len(tools))
	}
}

func TestRemoveRunningTool(t *testing.T) {
	tools := []buckleywidgets.RunningTool{
		{ID: "1", Name: "tool1"},
		{ID: "2", Name: "tool2"},
		{ID: "3", Name: "tool3"},
	}

	tools = removeRunningTool(tools, "2")
	if len(tools) != 2 {
		t.Errorf("expected 2 tools, got %d", len(tools))
	}
	for _, tool := range tools {
		if tool.ID == "2" {
			t.Error("tool 2 should have been removed")
		}
	}
}

func TestUpdateTaskStatus(t *testing.T) {
	tasks := []buckleywidgets.PlanTask{
		{Name: "task1", Status: buckleywidgets.TaskPending},
		{Name: "task2", Status: buckleywidgets.TaskPending},
	}

	tasks = updateTaskStatus(tasks, "task1", buckleywidgets.TaskCompleted)
	if tasks[0].Status != buckleywidgets.TaskCompleted {
		t.Errorf("expected task1 completed, got %d", tasks[0].Status)
	}
	if tasks[1].Status != buckleywidgets.TaskPending {
		t.Errorf("expected task2 still pending, got %d", tasks[1].Status)
	}

	// Non-existent task
	tasks = updateTaskStatus(tasks, "nonexistent", buckleywidgets.TaskInProgress)
	if len(tasks) != 2 {
		t.Errorf("expected 2 tasks, got %d", len(tasks))
	}
}

func TestCloneAndSortVariants(t *testing.T) {
	variants := []buckleywidgets.ExperimentVariant{
		{Name: "beta", Status: "running"},
		{Name: "alpha", Status: "running"},
		{Name: "gamma", Status: "completed"},
	}

	cloned := cloneAndSortVariants(variants)
	if len(cloned) != 3 {
		t.Errorf("expected 3 variants, got %d", len(cloned))
	}

	// Should be sorted alphabetically
	expected := []string{"alpha", "beta", "gamma"}
	for i, v := range cloned {
		if v.Name != expected[i] {
			t.Errorf("position %d: expected %q, got %q", i, expected[i], v.Name)
		}
	}
}
