package orchestrator

import (
	"strings"
	"testing"
	"time"
)

func TestProgressTracker(t *testing.T) {
	plan := &Plan{
		ID:          "test-plan",
		FeatureName: "Test Feature",
		Tasks: []Task{
			{ID: "1", Description: "Task 1", Status: TaskCompleted},
			{ID: "2", Description: "Task 2", Status: TaskInProgress},
			{ID: "3", Description: "Task 3", Status: TaskPending},
			{ID: "4", Description: "Task 4", Status: TaskPending},
		},
	}

	tracker := NewProgressTracker(plan)
	progress := tracker.GetProgressInfo()

	if progress == nil {
		t.Fatal("GetProgressInfo() returned nil")
	}

	if progress.TotalTasks != 4 {
		t.Errorf("TotalTasks = %d, want 4", progress.TotalTasks)
	}

	if progress.CompletedTasks != 1 {
		t.Errorf("CompletedTasks = %d, want 1", progress.CompletedTasks)
	}

	if progress.InProgressTasks != 1 {
		t.Errorf("InProgressTasks = %d, want 1", progress.InProgressTasks)
	}

	if progress.PendingTasks != 2 {
		t.Errorf("PendingTasks = %d, want 2", progress.PendingTasks)
	}
}

func TestProgressTracker_Phases(t *testing.T) {
	plan := &Plan{
		ID:    "test-plan",
		Tasks: []Task{},
	}

	tracker := NewProgressTracker(plan)

	tracker.StartPhase("build")
	time.Sleep(10 * time.Millisecond)
	tracker.CompletePhase("build")

	progress := tracker.GetProgressInfo()

	if len(progress.Phases) != 1 {
		t.Errorf("Phases count = %d, want 1", len(progress.Phases))
	}

	phase := progress.Phases[0]
	if phase.Name != "build" {
		t.Errorf("Phase name = %q, want %q", phase.Name, "build")
	}

	if phase.Status != "completed" {
		t.Errorf("Phase status = %q, want %q", phase.Status, "completed")
	}

	if phase.Duration < 10*time.Millisecond {
		t.Errorf("Phase duration = %v, want >= 10ms", phase.Duration)
	}
}

func TestRenderProgressBar(t *testing.T) {
	tests := []struct {
		name     string
		progress *ProgressInfo
		width    int
		wantPct  string
	}{
		{
			name: "empty progress",
			progress: &ProgressInfo{
				TotalTasks:     10,
				CompletedTasks: 0,
			},
			width:   50,
			wantPct: "0%",
		},
		{
			name: "50% complete",
			progress: &ProgressInfo{
				TotalTasks:     10,
				CompletedTasks: 5,
			},
			width:   50,
			wantPct: "50%",
		},
		{
			name: "100% complete",
			progress: &ProgressInfo{
				TotalTasks:     10,
				CompletedTasks: 10,
			},
			width:   50,
			wantPct: "100%",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RenderProgressBar(tt.progress, tt.width)
			if !strings.Contains(result, tt.wantPct) {
				t.Errorf("RenderProgressBar() = %q, should contain %q", result, tt.wantPct)
			}
			if !strings.HasPrefix(result, "[") {
				t.Error("Progress bar should start with [")
			}
			if !strings.Contains(result, "]") {
				t.Error("Progress bar should contain ]")
			}
		})
	}
}

func TestRenderProgressBar_Nil(t *testing.T) {
	result := RenderProgressBar(nil, 50)
	if result != "" {
		t.Errorf("RenderProgressBar(nil) = %q, want empty string", result)
	}
}

func TestRenderProgressBar_ZeroTasks(t *testing.T) {
	result := RenderProgressBar(&ProgressInfo{TotalTasks: 0}, 50)
	if result != "" {
		t.Errorf("RenderProgressBar with 0 tasks = %q, want empty string", result)
	}
}

func TestRenderCompactProgress(t *testing.T) {
	progress := &ProgressInfo{
		PlanID:          "test-plan",
		PlanName:        "Test Feature",
		Status:          StatusActive,
		TotalTasks:      10,
		CompletedTasks:  5,
		InProgressTasks: 1,
		PendingTasks:    4,
		CurrentPhase:    "build",
	}

	result := RenderCompactProgress(progress)

	if !strings.Contains(result, "Test Feature") {
		t.Error("Should contain plan name")
	}
	if !strings.Contains(result, "5/10") {
		t.Error("Should contain task count")
	}
	if !strings.Contains(result, "[build]") {
		t.Error("Should contain current phase")
	}
}

func TestRenderCompactProgress_WithFailures(t *testing.T) {
	progress := &ProgressInfo{
		PlanName:       "Test",
		Status:         StatusActive,
		TotalTasks:     10,
		CompletedTasks: 5,
		FailedTasks:    2,
	}

	result := RenderCompactProgress(progress)

	if !strings.Contains(result, "2 failed") {
		t.Error("Should show failed count")
	}
}

func TestRenderDetailedProgress(t *testing.T) {
	progress := &ProgressInfo{
		PlanID:          "test-plan",
		PlanName:        "Test Feature",
		Status:          StatusActive,
		TotalTasks:      10,
		CompletedTasks:  5,
		InProgressTasks: 1,
		PendingTasks:    4,
		Duration:        5 * time.Minute,
		ETA:             4 * time.Minute,
		Phases: []PhaseProgress{
			{Name: "build", Status: "completed", Duration: 2 * time.Minute},
			{Name: "verify", Status: "in_progress"},
		},
	}

	result := RenderDetailedProgress(progress)

	if !strings.Contains(result, "Test Feature") {
		t.Error("Should contain plan name")
	}
	if !strings.Contains(result, "5 completed") {
		t.Error("Should contain completed count")
	}
	if !strings.Contains(result, "elapsed") {
		t.Error("Should show elapsed time")
	}
	if !strings.Contains(result, "remaining") {
		t.Error("Should show remaining time")
	}
	if !strings.Contains(result, "build") {
		t.Error("Should list phases")
	}
}

func TestRenderTaskList(t *testing.T) {
	plan := &Plan{
		Tasks: []Task{
			{Description: "First task", Status: TaskCompleted},
			{Description: "Second task", Status: TaskInProgress},
			{Description: "Third task", Status: TaskPending},
			{Description: "Fourth task", Status: TaskFailed},
		},
	}

	result := RenderTaskList(plan, 0)

	if !strings.Contains(result, "First task") {
		t.Error("Should contain first task")
	}
	if !strings.Contains(result, "✓") {
		t.Error("Should show completed icon")
	}
	if !strings.Contains(result, "▶") {
		t.Error("Should show in-progress icon")
	}
	if !strings.Contains(result, "✗") {
		t.Error("Should show failed icon")
	}
}

func TestRenderTaskList_WithLimit(t *testing.T) {
	plan := &Plan{
		Tasks: []Task{
			{Description: "Task 1", Status: TaskPending},
			{Description: "Task 2", Status: TaskPending},
			{Description: "Task 3", Status: TaskPending},
			{Description: "Task 4", Status: TaskPending},
			{Description: "Task 5", Status: TaskPending},
		},
	}

	result := RenderTaskList(plan, 3)

	if !strings.Contains(result, "Task 1") {
		t.Error("Should contain first task")
	}
	if !strings.Contains(result, "2 more tasks") {
		t.Error("Should show remaining count")
	}
}

func TestProgressObserver(t *testing.T) {
	plan := &Plan{
		ID: "test",
		Tasks: []Task{
			{ID: "1", Status: TaskPending},
		},
	}

	callCount := 0
	observer := NewProgressObserver(plan, func(progress *ProgressInfo) {
		callCount++
	})

	observer.OnPhaseStart("build")
	if callCount != 1 {
		t.Errorf("Callback count = %d, want 1", callCount)
	}

	observer.OnTaskUpdate("1", TaskCompleted)
	if callCount != 2 {
		t.Errorf("Callback count = %d, want 2", callCount)
	}

	observer.OnPhaseComplete("build")
	if callCount != 3 {
		t.Errorf("Callback count = %d, want 3", callCount)
	}
}

func TestStatusEmoji(t *testing.T) {
	tests := []struct {
		status PlanStatus
		want   string
	}{
		{StatusPlanning, "📝"},
		{StatusActive, "🔄"},
		{StatusCompleted, "✅"},
		{StatusPaused, "⏸️"},
		{StatusFailed, "❌"},
		{StatusCancelled, "🚫"},
		{PlanStatus("unknown"), "⏳"},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			if got := statusEmoji(tt.status); got != tt.want {
				t.Errorf("statusEmoji(%q) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		duration time.Duration
		want     string
	}{
		{30 * time.Second, "30s"},
		{90 * time.Second, "1m30s"},
		{3661 * time.Second, "1h1m"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := formatDuration(tt.duration); got != tt.want {
				t.Errorf("formatDuration(%v) = %q, want %q", tt.duration, got, tt.want)
			}
		})
	}
}
