package tui

import (
	"strconv"
	"testing"
	"time"

	buckleywidgets "github.com/odvcencio/buckley/pkg/buckley/ui/widgets"
	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/touch"
	"github.com/odvcencio/fluffyui/state"
)

// =============================================================================
// SimpleTelemetryBridge Tests (for Runner)
// =============================================================================

type sidebarSignalHarness struct {
	signals            SidebarSignals
	currentTask        *state.Signal[string]
	taskProgress       *state.Signal[int]
	planTasks          *state.Signal[[]buckleywidgets.PlanTask]
	runningTools       *state.Signal[[]buckleywidgets.RunningTool]
	toolHistory        *state.Signal[[]buckleywidgets.ToolHistoryEntry]
	activeTouches      *state.Signal[[]buckleywidgets.TouchSummary]
	recentFiles        *state.Signal[[]string]
	experiment         *state.Signal[string]
	experimentStatus   *state.Signal[string]
	experimentVariants *state.Signal[[]buckleywidgets.ExperimentVariant]
	rlmStatus          *state.Signal[*buckleywidgets.RLMStatus]
	rlmScratchpad      *state.Signal[[]buckleywidgets.RLMScratchpadEntry]
	circuitStatus      *state.Signal[*buckleywidgets.CircuitStatus]
}

func newSidebarSignalHarness() *sidebarSignalHarness {
	h := &sidebarSignalHarness{
		currentTask:        state.NewSignal(""),
		taskProgress:       state.NewSignal(0),
		planTasks:          state.NewSignal([]buckleywidgets.PlanTask(nil)),
		runningTools:       state.NewSignal([]buckleywidgets.RunningTool(nil)),
		toolHistory:        state.NewSignal([]buckleywidgets.ToolHistoryEntry(nil)),
		activeTouches:      state.NewSignal([]buckleywidgets.TouchSummary(nil)),
		recentFiles:        state.NewSignal([]string(nil)),
		experiment:         state.NewSignal(""),
		experimentStatus:   state.NewSignal(""),
		experimentVariants: state.NewSignal([]buckleywidgets.ExperimentVariant(nil)),
		rlmStatus:          state.NewSignal[*buckleywidgets.RLMStatus](nil),
		rlmScratchpad:      state.NewSignal([]buckleywidgets.RLMScratchpadEntry(nil)),
		circuitStatus:      state.NewSignal[*buckleywidgets.CircuitStatus](nil),
	}
	h.signals = SidebarSignals{
		CurrentTask:        h.currentTask,
		TaskProgress:       h.taskProgress,
		PlanTasks:          h.planTasks,
		RunningTools:       h.runningTools,
		ToolHistory:        h.toolHistory,
		ActiveTouches:      h.activeTouches,
		RecentFiles:        h.recentFiles,
		Experiment:         h.experiment,
		ExperimentStatus:   h.experimentStatus,
		ExperimentVariants: h.experimentVariants,
		RLMStatus:          h.rlmStatus,
		RLMScratchpad:      h.rlmScratchpad,
		CircuitStatus:      h.circuitStatus,
	}
	return h
}

func TestSimpleTelemetryBridge_New(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	harness := newSidebarSignalHarness()
	bridge := NewSimpleTelemetryBridge(hub, harness.signals)
	if bridge == nil {
		t.Fatal("expected non-nil bridge")
	}
	if bridge.hub != hub {
		t.Error("hub not set correctly")
	}
	if bridge.signals.CurrentTask != harness.currentTask {
		t.Error("signals not set correctly")
	}
}

func TestSimpleTelemetryBridge_NewNilHub(t *testing.T) {
	harness := newSidebarSignalHarness()
	bridge := NewSimpleTelemetryBridge(nil, harness.signals)
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

	harness := newSidebarSignalHarness()
	bridge := NewSimpleTelemetryBridge(hub, harness.signals)

	bridge.handleEvent(telemetry.Event{
		Type:   telemetry.EventTaskStarted,
		TaskID: "task-1",
		Data:   map[string]any{"name": "Build project"},
	})

	if harness.currentTask.Get() != "Build project" {
		t.Errorf("expected currentTask 'Build project', got %q", harness.currentTask.Get())
	}
	if harness.taskProgress.Get() != 0 {
		t.Errorf("expected taskProgress 0, got %d", harness.taskProgress.Get())
	}
}

func TestSimpleTelemetryBridge_HandleTaskCompleted(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	harness := newSidebarSignalHarness()
	bridge := NewSimpleTelemetryBridge(hub, harness.signals)

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

	if harness.taskProgress.Get() != 100 {
		t.Errorf("expected taskProgress 100, got %d", harness.taskProgress.Get())
	}
}

func TestSimpleTelemetryBridge_HandleToolEvents(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	harness := newSidebarSignalHarness()
	bridge := NewSimpleTelemetryBridge(hub, harness.signals)

	bridge.handleEvent(telemetry.Event{
		Type:      telemetry.EventToolStarted,
		TaskID:    "tool-1",
		Timestamp: time.Now(),
		Data:      map[string]any{"toolName": "bash", "command": "ls -la"},
	})

	running := harness.runningTools.Get()
	if len(running) != 1 {
		t.Errorf("expected 1 running tool, got %d", len(running))
	}
	if running[0].Name != "bash" {
		t.Errorf("expected tool name 'bash', got %q", running[0].Name)
	}

	// Complete the tool
	bridge.handleEvent(telemetry.Event{
		Type:      telemetry.EventToolCompleted,
		TaskID:    "tool-1",
		Timestamp: time.Now(),
	})

	running = harness.runningTools.Get()
	if len(running) != 0 {
		t.Errorf("expected 0 running tools after completion, got %d", len(running))
	}
}

func TestSimpleTelemetryBridge_HandleTouchesAndRecentFiles(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	harness := newSidebarSignalHarness()
	bridge := NewSimpleTelemetryBridge(hub, harness.signals)

	now := time.Now()
	bridge.handleEvent(telemetry.Event{
		Type:      telemetry.EventToolStarted,
		TaskID:    "tool-1",
		Timestamp: now,
		Data: map[string]any{
			"toolName":      "write_file",
			"operationType": "write",
			"filePath":      "foo.txt",
			"ranges":        []touch.LineRange{{Start: 1, End: 2}},
			"expiresAt":     now.Add(2 * time.Minute),
		},
	})

	touches := harness.activeTouches.Get()
	if len(touches) != 1 {
		t.Fatalf("expected 1 active touch, got %d", len(touches))
	}
	if touches[0].Path != "foo.txt" {
		t.Errorf("expected touch path foo.txt, got %q", touches[0].Path)
	}
	if len(touches[0].Ranges) != 1 {
		t.Errorf("expected 1 range, got %d", len(touches[0].Ranges))
	}

	files := harness.recentFiles.Get()
	if len(files) != 1 || files[0] != "foo.txt" {
		t.Errorf("expected recent files [foo.txt], got %v", files)
	}

	bridge.handleEvent(telemetry.Event{
		Type:      telemetry.EventToolCompleted,
		TaskID:    "tool-1",
		Timestamp: now.Add(time.Second),
		Data: map[string]any{
			"toolName":      "write_file",
			"operationType": "write",
			"filePath":      "foo.txt",
		},
	})

	touches = harness.activeTouches.Get()
	if len(touches) != 0 {
		t.Errorf("expected 0 active touches after completion, got %d", len(touches))
	}
	files = harness.recentFiles.Get()
	if len(files) == 0 || files[0] != "foo.txt" {
		t.Errorf("expected recent files to keep foo.txt, got %v", files)
	}
}

func TestSimpleTelemetryBridge_RecentFilesCap(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	harness := newSidebarSignalHarness()
	bridge := NewSimpleTelemetryBridge(hub, harness.signals)

	now := time.Now()
	for i := 0; i < maxRecentFiles+2; i++ {
		path := "file" + strconv.Itoa(i) + ".txt"
		bridge.handleEvent(telemetry.Event{
			Type:      telemetry.EventToolStarted,
			TaskID:    "tool-" + strconv.Itoa(i),
			Timestamp: now.Add(time.Duration(i) * time.Second),
			Data: map[string]any{
				"toolName":      "read_file",
				"operationType": "read",
				"filePath":      path,
			},
		})
	}

	files := harness.recentFiles.Get()
	if len(files) != maxRecentFiles {
		t.Fatalf("expected %d recent files, got %d", maxRecentFiles, len(files))
	}
	if files[0] != "file6.txt" {
		t.Errorf("expected most recent file6.txt, got %q", files[0])
	}
}

func TestSimpleTelemetryBridge_HandleExperimentEvents(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	harness := newSidebarSignalHarness()
	bridge := NewSimpleTelemetryBridge(hub, harness.signals)

	bridge.handleEvent(telemetry.Event{
		Type: telemetry.EventExperimentStarted,
		Data: map[string]any{
			"name":   "Experiment A",
			"status": "running",
			"variants": []any{
				map[string]any{"id": "v1", "name": "Variant A", "model": "model-a"},
				map[string]any{"id": "v2", "name": "Variant B", "model": "model-b"},
			},
		},
	})

	if harness.experiment.Get() != "Experiment A" {
		t.Errorf("expected experiment name Experiment A, got %q", harness.experiment.Get())
	}
	if harness.experimentStatus.Get() != "running" {
		t.Errorf("expected experiment status running, got %q", harness.experimentStatus.Get())
	}
	if len(harness.experimentVariants.Get()) != 2 {
		t.Fatalf("expected 2 experiment variants, got %d", len(harness.experimentVariants.Get()))
	}

	bridge.handleEvent(telemetry.Event{
		Type: telemetry.EventExperimentVariantStarted,
		Data: map[string]any{
			"experiment": "Experiment A",
			"variant_id": "v1",
			"variant":    "Variant A",
			"model_id":   "model-a",
		},
	})

	bridge.handleEvent(telemetry.Event{
		Type: telemetry.EventExperimentVariantCompleted,
		Data: map[string]any{
			"experiment":       "Experiment A",
			"variant_id":       "v1",
			"variant":          "Variant A",
			"model_id":         "model-a",
			"status":           "completed",
			"duration_ms":      1200,
			"total_cost":       0.42,
			"prompt_tokens":    10,
			"completion_tokens": 20,
		},
	})

	var found buckleywidgets.ExperimentVariant
	ok := false
	for _, variant := range harness.experimentVariants.Get() {
		if variant.ID == "v1" {
			found = variant
			ok = true
			break
		}
	}
	if !ok {
		t.Fatal("expected to find variant v1")
	}
	if found.Status != "completed" {
		t.Errorf("expected variant status completed, got %q", found.Status)
	}
	if found.DurationMs != 1200 {
		t.Errorf("expected duration 1200, got %d", found.DurationMs)
	}
	if found.TotalCost != 0.42 {
		t.Errorf("expected total cost 0.42, got %f", found.TotalCost)
	}

	bridge.handleEvent(telemetry.Event{
		Type: telemetry.EventExperimentCompleted,
		Data: map[string]any{
			"name":   "Experiment A",
			"status": "completed",
		},
	})

	if harness.experimentStatus.Get() != "completed" {
		t.Errorf("expected experiment status completed, got %q", harness.experimentStatus.Get())
	}
}

func TestSimpleTelemetryBridge_HandleRLMIteration(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	harness := newSidebarSignalHarness()
	bridge := NewSimpleTelemetryBridge(hub, harness.signals)

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

	status := harness.rlmStatus.Get()
	if status == nil {
		t.Fatal("expected rlmStatus to be set")
	}
	if status.Iteration != 2 {
		t.Errorf("expected iteration 2, got %d", status.Iteration)
	}
	if !status.Ready {
		t.Error("expected ready to be true")
	}
	if status.TokensUsed != 1200 {
		t.Errorf("expected tokens 1200, got %d", status.TokensUsed)
	}
}

func TestSimpleTelemetryBridge_SetPlanTasks(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	harness := newSidebarSignalHarness()
	bridge := NewSimpleTelemetryBridge(hub, harness.signals)

	tasks := []buckleywidgets.PlanTask{
		{Name: "Task 1", Status: buckleywidgets.TaskPending},
		{Name: "Task 2", Status: buckleywidgets.TaskInProgress},
	}
	bridge.SetPlanTasks(tasks)

	plan := harness.planTasks.Get()
	if len(plan) != 2 {
		t.Errorf("expected 2 plan tasks, got %d", len(plan))
	}
	if plan[0].Name != "Task 1" {
		t.Errorf("expected first task name 'Task 1', got %q", plan[0].Name)
	}
}

func TestSimpleTelemetryBridge_HandlePlanUpdate(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()

	harness := newSidebarSignalHarness()
	bridge := NewSimpleTelemetryBridge(hub, harness.signals)

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

	plan := harness.planTasks.Get()
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
