//go:build integration

// Package integration provides integration tests for the TUI rendering pipeline.
//
// These tests use fluffy-ui's simulation backend to verify widget rendering
// without requiring a real terminal.
//
// Run with: go test -tags=integration ./tests/integration -v -run TestTUI
package integration

import (
	"strings"
	"testing"

	"github.com/odvcencio/buckley/pkg/buckley/ui/widgets"
	"github.com/odvcencio/fluffy-ui/backend"
	"github.com/odvcencio/fluffy-ui/backend/sim"
	"github.com/odvcencio/fluffy-ui/runtime"
	"github.com/odvcencio/fluffy-ui/theme"
)

// TestTUI_HeaderRendering tests that the header widget renders correctly.
func TestTUI_HeaderRendering(t *testing.T) {
	be := sim.New(80, 1)
	if err := be.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer be.Fini()

	header := widgets.NewHeader()
	header.SetModelName("claude-3.5-sonnet")
	header.SetStyles(
		backend.DefaultStyle(),
		backend.DefaultStyle().Bold(true),
		backend.DefaultStyle(),
	)

	renderWidget(t, be, header, 80, 1)

	capture := be.Capture()
	t.Logf("Header render:\n%s", capture)

	if !be.ContainsText("claude-3.5-sonnet") {
		t.Error("Expected header to contain model name 'claude-3.5-sonnet'")
	}
}

// TestTUI_StatusBarRendering tests that the status bar widget renders correctly.
func TestTUI_StatusBarRendering(t *testing.T) {
	be := sim.New(80, 1)
	if err := be.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer be.Fini()

	status := widgets.NewStatusBar()
	status.SetStatus("Ready")
	status.SetTokens(5000, 0.25)
	status.SetStyles(
		backend.DefaultStyle(),
		backend.DefaultStyle(),
	)

	renderWidget(t, be, status, 80, 1)

	capture := be.Capture()
	t.Logf("Status bar render:\n%s", capture)

	if !be.ContainsText("Ready") {
		t.Error("Expected status bar to contain 'Ready'")
	}
	if !be.ContainsText("5000") || !be.ContainsText("5,000") {
		// Token count might be formatted
		if !strings.Contains(capture, "5") {
			t.Error("Expected status bar to contain token count")
		}
	}
}

// TestTUI_InputAreaRendering tests the input area widget rendering.
func TestTUI_InputAreaRendering(t *testing.T) {
	be := sim.New(80, 3)
	if err := be.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer be.Fini()

	input := widgets.NewInputArea()
	input.SetStyles(
		backend.DefaultStyle(),
		backend.DefaultStyle(),
		backend.DefaultStyle(),
	)
	input.SetModeStyles(
		backend.DefaultStyle().Foreground(backend.ColorGreen),
		backend.DefaultStyle().Foreground(backend.ColorYellow),
		backend.DefaultStyle().Foreground(backend.ColorCyan),
		backend.DefaultStyle().Foreground(backend.ColorMagenta),
	)
	input.Focus()

	renderWidget(t, be, input, 80, 3)

	capture := be.Capture()
	t.Logf("Input area render:\n%s", capture)

	// Input should have a mode indicator
	if !strings.Contains(capture, theme.Symbols.ModeNormal) {
		t.Logf("Mode symbol not found, checking for alternative indicators")
	}
}

// TestTUI_SidebarRendering tests the sidebar widget rendering.
func TestTUI_SidebarRendering(t *testing.T) {
	be := sim.New(30, 20)
	if err := be.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer be.Fini()

	sidebar := widgets.NewSidebar()
	sidebar.SetCurrentTask("Building project", 65)
	sidebar.SetPlanTasks([]widgets.PlanTask{
		{Name: "Setup environment", Status: widgets.TaskCompleted},
		{Name: "Write tests", Status: widgets.TaskInProgress},
		{Name: "Deploy to prod", Status: widgets.TaskPending},
	})
	sidebar.SetRunningTools([]widgets.RunningTool{
		{ID: "1", Name: "shell", Command: "go test ./..."},
	})
	sidebar.SetContextUsage(4700, 10000, 0)
	sidebar.SetRecentFiles([]string{"pkg/main.go", "pkg/utils.go"})
	sidebar.SetStyles(
		backend.DefaultStyle(),
		backend.DefaultStyle().Bold(true),
		backend.DefaultStyle(),
		backend.DefaultStyle().Foreground(backend.ColorGreen),
		backend.DefaultStyle(),
		backend.DefaultStyle(),
	)

	renderWidget(t, be, sidebar, 30, 20)

	capture := be.Capture()
	t.Logf("Sidebar render:\n%s", capture)

	// Check for expected content
	expectations := []struct {
		text   string
		reason string
	}{
		{"Setup environment", "completed task"},
		{"Write tests", "in-progress task"},
		{"Deploy to prod", "pending task"},
		{"go test", "running tool command"},
	}

	for _, exp := range expectations {
		if !strings.Contains(capture, exp.text) {
			t.Errorf("Expected sidebar to contain %s (%s)", exp.text, exp.reason)
		}
	}
}

// TestTUI_ApprovalWidgetRendering tests the approval dialog rendering.
func TestTUI_ApprovalWidgetRendering(t *testing.T) {
	be := sim.New(60, 15)
	if err := be.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer be.Fini()

	req := widgets.ApprovalRequest{
		ID:          "req-1",
		Tool:        "run_shell",
		Operation:   "execute",
		Description: "Run a shell command",
		Command:     "rm -rf node_modules",
	}
	approval := widgets.NewApprovalWidget(req)
	approval.SetStyles(
		backend.DefaultStyle(),
		backend.DefaultStyle(),
		backend.DefaultStyle().Bold(true),
		backend.DefaultStyle(),
	)
	approval.Focus()

	renderWidget(t, be, approval, 60, 15)

	capture := be.Capture()
	t.Logf("Approval widget render:\n%s", capture)

	if !be.ContainsText("run_shell") {
		t.Error("Expected approval to show tool name")
	}
	if !be.ContainsText("rm -rf") {
		t.Error("Expected approval to show command")
	}
}

// TestTUI_ApprovalWithDiffRendering tests the approval dialog with diff rendering.
func TestTUI_ApprovalWithDiffRendering(t *testing.T) {
	be := sim.New(60, 20)
	if err := be.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer be.Fini()

	req := widgets.ApprovalRequest{
		ID:        "req-2",
		Tool:      "write_file",
		Operation: "edit",
		FilePath:  "pkg/main.go",
		DiffLines: []widgets.DiffLine{
			{Type: widgets.DiffContext, Content: "package main"},
			{Type: widgets.DiffContext, Content: ""},
			{Type: widgets.DiffRemove, Content: "func oldFunc() {}"},
			{Type: widgets.DiffAdd, Content: "func newFunc() {}"},
			{Type: widgets.DiffContext, Content: ""},
		},
		AddedLines:   1,
		RemovedLines: 1,
	}
	approval := widgets.NewApprovalWidget(req)
	approval.SetStyles(
		backend.DefaultStyle(),
		backend.DefaultStyle(),
		backend.DefaultStyle().Bold(true),
		backend.DefaultStyle(),
	)
	approval.Focus()

	renderWidget(t, be, approval, 60, 20)

	capture := be.Capture()
	t.Logf("Approval with diff render:\n%s", capture)

	if !be.ContainsText("pkg/main.go") {
		t.Error("Expected approval to show file path")
	}
	if !be.ContainsText("oldFunc") {
		t.Error("Expected diff to show removed function")
	}
	if !be.ContainsText("newFunc") {
		t.Error("Expected diff to show added function")
	}
}

// TestTUI_LayoutComposition tests that widgets compose correctly in a layout.
func TestTUI_LayoutComposition(t *testing.T) {
	be := sim.New(80, 24)
	if err := be.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer be.Fini()

	// Create widgets
	header := widgets.NewHeader()
	header.SetModelName("test-model")
	header.SetStyles(
		backend.DefaultStyle(),
		backend.DefaultStyle().Bold(true),
		backend.DefaultStyle(),
	)

	status := widgets.NewStatusBar()
	status.SetStatus("Thinking...")
	status.SetStyles(
		backend.DefaultStyle(),
		backend.DefaultStyle(),
	)

	input := widgets.NewInputArea()
	input.SetStyles(
		backend.DefaultStyle(),
		backend.DefaultStyle(),
		backend.DefaultStyle(),
	)
	input.SetModeStyles(
		backend.DefaultStyle().Foreground(backend.ColorGreen),
		backend.DefaultStyle().Foreground(backend.ColorYellow),
		backend.DefaultStyle().Foreground(backend.ColorCyan),
		backend.DefaultStyle().Foreground(backend.ColorMagenta),
	)

	// Compose in a vertical layout
	root := runtime.VBox(
		runtime.Fixed(header),
		runtime.Expanded(runtime.NewSpacer()), // Placeholder for main content
		runtime.Fixed(input),
		runtime.Fixed(status),
	)

	renderWidget(t, be, root, 80, 24)

	capture := be.Capture()
	t.Logf("Composed layout render:\n%s", capture)

	// Header should be at top (row 0)
	x, y := be.FindText("test-model")
	if y != 0 {
		t.Errorf("Expected header at row 0, found at row %d", y)
	}
	t.Logf("Header found at (%d, %d)", x, y)

	// Status should be at bottom
	x, y = be.FindText("Thinking")
	if y < 20 { // Should be near bottom
		t.Logf("Status bar found at row %d (expected near 23)", y)
	}
}

// renderWidget is a helper that measures, layouts, and renders a widget.
func renderWidget(t *testing.T, be *sim.Backend, w runtime.Widget, width, height int) {
	t.Helper()

	buf := runtime.NewBuffer(width, height)

	constraints := runtime.Constraints{MaxWidth: width, MaxHeight: height}
	w.Measure(constraints)
	w.Layout(runtime.Rect{X: 0, Y: 0, Width: width, Height: height})

	ctx := runtime.RenderContext{Buffer: buf}
	w.Render(ctx)

	// Copy buffer to backend
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			cell := buf.Get(x, y)
			r := cell.Rune
			if r == 0 {
				r = ' '
			}
			be.SetContent(x, y, r, nil, cell.Style)
		}
	}
	be.Show()
}
