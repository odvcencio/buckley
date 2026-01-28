package widgets

import (
	"testing"

	"github.com/odvcencio/fluffy-ui/backend"
	"github.com/odvcencio/fluffy-ui/runtime"
	"github.com/odvcencio/fluffy-ui/terminal"
)

func TestSidebar_New(t *testing.T) {
	s := NewSidebar()

	if !s.showCurrentTask {
		t.Error("showCurrentTask should be true by default")
	}
	if !s.showPlan {
		t.Error("showPlan should be true by default")
	}
	if !s.showTools {
		t.Error("showTools should be true by default")
	}
	if !s.showContext {
		t.Error("showContext should be true by default")
	}
	if !s.showTouches {
		t.Error("showTouches should be true by default")
	}
	if !s.showRecentFiles {
		t.Error("showRecentFiles should be true by default")
	}
}

func TestSidebar_SetCurrentTask(t *testing.T) {
	s := NewSidebar()

	s.SetCurrentTask("Implement auth", 50)

	if s.currentTask != "Implement auth" {
		t.Errorf("expected task 'Implement auth', got '%s'", s.currentTask)
	}
	if s.taskProgress != 50 {
		t.Errorf("expected progress 50, got %d", s.taskProgress)
	}

	// Test bounds checking
	s.SetCurrentTask("Test", -10)
	if s.taskProgress != 0 {
		t.Errorf("negative progress should be clamped to 0, got %d", s.taskProgress)
	}

	s.SetCurrentTask("Test", 150)
	if s.taskProgress != 100 {
		t.Errorf("progress > 100 should be clamped to 100, got %d", s.taskProgress)
	}
}

func TestSidebar_SetPlanTasks(t *testing.T) {
	s := NewSidebar()

	tasks := []PlanTask{
		{Name: "Design API", Status: TaskCompleted},
		{Name: "Write tests", Status: TaskCompleted},
		{Name: "Implement", Status: TaskInProgress},
		{Name: "Review", Status: TaskPending},
	}
	s.SetPlanTasks(tasks)

	if len(s.planTasks) != 4 {
		t.Errorf("expected 4 tasks, got %d", len(s.planTasks))
	}
	if s.planTasks[2].Status != TaskInProgress {
		t.Error("task 3 should be in progress")
	}
}

func TestSidebar_SetRunningTools(t *testing.T) {
	s := NewSidebar()

	tools := []RunningTool{
		{ID: "1", Name: "run_shell", Command: "npm test"},
		{ID: "2", Name: "read_file"},
	}
	s.SetRunningTools(tools)

	if len(s.runningTools) != 2 {
		t.Errorf("expected 2 tools, got %d", len(s.runningTools))
	}
	if s.runningTools[0].Command != "npm test" {
		t.Errorf("expected command 'npm test', got '%s'", s.runningTools[0].Command)
	}
}

func TestSidebar_SetRecentFiles(t *testing.T) {
	s := NewSidebar()

	files := []string{
		"pkg/api/server.go",
		"pkg/buckley/ui/widgets/sidebar.go",
	}
	s.SetRecentFiles(files)

	if len(s.recentFiles) != 2 {
		t.Errorf("expected 2 files, got %d", len(s.recentFiles))
	}
}

func TestSidebar_SetActiveTouches(t *testing.T) {
	s := NewSidebar()

	touches := []TouchSummary{
		{Path: "pkg/buckley/ui/widgets/sidebar.go", Operation: "write"},
		{Path: "pkg/buckley/ui/tui/app_widget.go", Operation: "read"},
	}
	s.SetActiveTouches(touches)

	if len(s.activeTouches) != 2 {
		t.Errorf("expected 2 touches, got %d", len(s.activeTouches))
	}
	if s.activeTouches[0].Path != "pkg/buckley/ui/widgets/sidebar.go" {
		t.Errorf("expected first touch path, got %s", s.activeTouches[0].Path)
	}
}

func TestSidebar_ToggleRecentFiles(t *testing.T) {
	s := NewSidebar()

	s.ToggleRecentFiles()
	if s.showRecentFiles {
		t.Error("should be hidden after toggle")
	}

	s.ToggleRecentFiles()
	if !s.showRecentFiles {
		t.Error("should be shown after second toggle")
	}
}

func TestSidebar_Measure(t *testing.T) {
	s := NewSidebar()

	size := s.Measure(runtime.Constraints{MaxWidth: 40, MaxHeight: 30})

	// Default width is 24
	if size.Width != 24 {
		t.Errorf("expected width 24, got %d", size.Width)
	}
	if size.Height != 30 {
		t.Errorf("expected height 30, got %d", size.Height)
	}

	// Constrained width
	size = s.Measure(runtime.Constraints{MaxWidth: 15, MaxHeight: 20})
	if size.Width != 15 {
		t.Errorf("expected constrained width 15, got %d", size.Width)
	}
}

func TestSidebar_HandleMessage_SectionToggles(t *testing.T) {
	s := NewSidebar()

	// Toggle current task with '1'
	result := s.HandleMessage(runtime.KeyMsg{Key: terminal.KeyRune, Rune: '1'})
	if !result.Handled {
		t.Error("'1' should be handled")
	}
	if s.showCurrentTask {
		t.Error("showCurrentTask should be false after '1'")
	}

	// Toggle plan with '2'
	result = s.HandleMessage(runtime.KeyMsg{Key: terminal.KeyRune, Rune: '2'})
	if !result.Handled {
		t.Error("'2' should be handled")
	}
	if s.showPlan {
		t.Error("showPlan should be false after '2'")
	}

	// Toggle tools with '3'
	result = s.HandleMessage(runtime.KeyMsg{Key: terminal.KeyRune, Rune: '3'})
	if !result.Handled {
		t.Error("'3' should be handled")
	}
	if s.showTools {
		t.Error("showTools should be false after '3'")
	}

	// Toggle context with '4'
	result = s.HandleMessage(runtime.KeyMsg{Key: terminal.KeyRune, Rune: '4'})
	if !result.Handled {
		t.Error("'4' should be handled")
	}
	if s.showContext {
		t.Error("showContext should be false after '4'")
	}

	// Toggle touches with '5'
	result = s.HandleMessage(runtime.KeyMsg{Key: terminal.KeyRune, Rune: '5'})
	if !result.Handled {
		t.Error("'5' should be handled")
	}
	if s.showTouches {
		t.Error("showTouches should be false after '5'")
	}

	// Toggle recent files with '6'
	result = s.HandleMessage(runtime.KeyMsg{Key: terminal.KeyRune, Rune: '6'})
	if !result.Handled {
		t.Error("'6' should be handled")
	}
	if s.showRecentFiles {
		t.Error("showRecentFiles should be false after '6'")
	}
}

func TestSidebar_HandleMessage_TabSwitch(t *testing.T) {
	s := NewSidebar()
	if s.tabs == nil {
		t.Fatal("expected tabs to be initialized")
	}
	s.tabs.Focus()

	result := s.HandleMessage(runtime.KeyMsg{Key: terminal.KeyRight})
	if !result.Handled {
		t.Fatal("expected right key to switch tabs")
	}
	if s.tabs.SelectedIndex() != 1 {
		t.Fatalf("expected tab index 1, got %d", s.tabs.SelectedIndex())
	}

	result = s.HandleMessage(runtime.KeyMsg{Key: terminal.KeyLeft})
	if !result.Handled {
		t.Fatal("expected left key to switch tabs")
	}
	if s.tabs.SelectedIndex() != 0 {
		t.Fatalf("expected tab index 0, got %d", s.tabs.SelectedIndex())
	}
}

func TestSidebar_Render(t *testing.T) {
	s := NewSidebar()
	s.SetCurrentTask("Implement feature", 75)
	s.SetPlanTasks([]PlanTask{
		{Name: "Design", Status: TaskCompleted},
		{Name: "Implement", Status: TaskInProgress},
		{Name: "Test", Status: TaskPending},
	})
	s.SetRunningTools([]RunningTool{
		{ID: "1", Name: "run_shell", Command: "npm test"},
	})
	s.Layout(runtime.Rect{X: 0, Y: 0, Width: 30, Height: 25})

	buf := runtime.NewBuffer(30, 25)
	ctx := runtime.RenderContext{Buffer: buf}

	// Should not panic
	s.Render(ctx)

	// Check for left border
	cell := buf.Get(0, 0)
	if cell.Rune != '│' {
		t.Errorf("expected left border '│', got '%c'", cell.Rune)
	}
}

func TestSidebar_UpdatePlanTableRows(t *testing.T) {
	s := NewSidebar()
	s.SetPlanTasks([]PlanTask{
		{Name: "Alpha", Status: TaskPending},
		{Name: "Beta", Status: TaskInProgress},
	})

	if s.planTable == nil {
		t.Fatal("expected plan table to be initialized")
	}
	if len(s.planTable.Rows) != 2 {
		t.Fatalf("expected 2 plan rows, got %d", len(s.planTable.Rows))
	}
	if s.planTable.Rows[0][0] != "Alpha" {
		t.Fatalf("expected first row Alpha, got %q", s.planTable.Rows[0][0])
	}
	if s.planTable.Rows[1][1] != "running" {
		t.Fatalf("expected second row status running, got %q", s.planTable.Rows[1][1])
	}
}

func TestSidebar_UpdateFilesTree(t *testing.T) {
	s := NewSidebar()
	s.SetProjectPath("/tmp/buckley")
	s.SetRecentFiles([]string{"pkg/main.go", "pkg/ui/sidebar.go"})

	if s.filesTree == nil || s.filesTree.Root == nil {
		t.Fatal("expected files tree root")
	}
	if s.filesTree.Root.Label != "buckley" {
		t.Fatalf("expected root label buckley, got %q", s.filesTree.Root.Label)
	}
	if len(s.filesTree.Root.Children) == 0 {
		t.Fatal("expected file nodes under root")
	}
}

func TestSidebar_UpdateTouchesTree(t *testing.T) {
	s := NewSidebar()
	s.SetActiveTouches([]TouchSummary{
		{Path: "pkg/main.go", Operation: "write", Ranges: []TouchRange{{Start: 1, End: 3}}},
	})

	if s.touchesTree == nil || s.touchesTree.Root == nil {
		t.Fatal("expected touches tree root")
	}
	if len(s.touchesTree.Root.Children) == 0 {
		t.Fatal("expected touch children")
	}
	if s.touchesTree.Root.Children[0].Label != "pkg/main.go" {
		t.Fatalf("expected touch label pkg/main.go, got %q", s.touchesTree.Root.Children[0].Label)
	}
}

func TestSidebar_RenderBackgroundFill(t *testing.T) {
	s := NewSidebar()
	bg := backend.DefaultStyle().Background(backend.ColorRGB(8, 9, 10))
	s.SetStyles(
		backend.DefaultStyle(),
		backend.DefaultStyle(),
		backend.DefaultStyle(),
		backend.DefaultStyle(),
		backend.DefaultStyle(),
		bg,
	)

	bounds := runtime.Rect{X: 0, Y: 0, Width: 12, Height: 5}
	s.Layout(bounds)

	buf := runtime.NewBuffer(bounds.Width, bounds.Height)
	ctx := runtime.RenderContext{Buffer: buf, Bounds: bounds}
	s.Render(ctx)

	cell := buf.Get(1, 0)
	if cell.Style.BG() != bg.BG() {
		t.Fatalf("expected background color %v, got %v", bg.BG(), cell.Style.BG())
	}
}

func TestSidebar_Render_SmallBounds(t *testing.T) {
	s := NewSidebar()
	s.Layout(runtime.Rect{X: 0, Y: 0, Width: 5, Height: 3})

	buf := runtime.NewBuffer(5, 3)
	ctx := runtime.RenderContext{Buffer: buf}

	// Should not panic with small bounds
	s.Render(ctx)
}

func TestTaskStatus_Values(t *testing.T) {
	// Verify task status constants
	if TaskPending != 0 {
		t.Errorf("expected TaskPending=0, got %d", TaskPending)
	}
	if TaskInProgress != 1 {
		t.Errorf("expected TaskInProgress=1, got %d", TaskInProgress)
	}
	if TaskCompleted != 2 {
		t.Errorf("expected TaskCompleted=2, got %d", TaskCompleted)
	}
	if TaskFailed != 3 {
		t.Errorf("expected TaskFailed=3, got %d", TaskFailed)
	}
}
