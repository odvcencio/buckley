package buckley

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/odvcencio/buckley/pkg/ui/backend"
	"github.com/odvcencio/buckley/pkg/ui/runtime"
)

var updateSnapshots = flag.Bool("update-snapshots", false, "Update golden snapshot files")

// renderToString renders a widget to a string for snapshot comparison.
func renderToString(w runtime.Widget, width, height int) string {
	buf := runtime.NewBuffer(width, height)

	constraints := runtime.Constraints{MaxWidth: width, MaxHeight: height}
	w.Measure(constraints)
	w.Layout(runtime.Rect{X: 0, Y: 0, Width: width, Height: height})

	ctx := runtime.RenderContext{Buffer: buf}
	w.Render(ctx)

	var sb strings.Builder
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			cell := buf.Get(x, y)
			if cell.Rune == 0 {
				sb.WriteRune(' ')
			} else {
				sb.WriteRune(cell.Rune)
			}
		}
		sb.WriteRune('\n')
	}
	return sb.String()
}

// assertSnapshot compares rendered output against a golden file.
func assertSnapshot(t *testing.T, name string, actual string) {
	t.Helper()

	goldenPath := filepath.Join("testdata", name+".golden")

	if *updateSnapshots {
		if err := os.MkdirAll("testdata", 0755); err != nil {
			t.Fatalf("failed to create testdata dir: %v", err)
		}
		if err := os.WriteFile(goldenPath, []byte(actual), 0644); err != nil {
			t.Fatalf("failed to write golden file: %v", err)
		}
		t.Logf("Updated snapshot: %s", goldenPath)
		return
	}

	expected, err := os.ReadFile(goldenPath)
	if err != nil {
		if os.IsNotExist(err) {
			t.Fatalf("snapshot file not found: %s\nRun with -update-snapshots to create it.\nActual output:\n%s", goldenPath, actual)
		}
		t.Fatalf("failed to read golden file: %v", err)
	}

	if actual != string(expected) {
		t.Errorf("snapshot mismatch for %s\n\nExpected:\n%s\n\nActual:\n%s\n\nRun with -update-snapshots to update.", name, string(expected), actual)
	}
}

func TestSnapshot_Header(t *testing.T) {
	h := NewHeader()
	h.SetModelName("gpt-4o")
	h.SetStyles(
		backend.DefaultStyle(),
		backend.DefaultStyle().Bold(true),
		backend.DefaultStyle(),
	)

	output := renderToString(h, 80, 1)
	assertSnapshot(t, "header", output)
}

func TestSnapshot_StatusBar(t *testing.T) {
	s := NewStatusBar()
	s.SetStatus("Ready")
	s.SetTokens(5000, 0.25)
	s.SetStyles(
		backend.DefaultStyle(),
		backend.DefaultStyle(),
	)

	output := renderToString(s, 80, 1)
	assertSnapshot(t, "statusbar", output)
}

func TestSnapshot_Sidebar(t *testing.T) {
	s := NewSidebar()
	s.SetCurrentTask("Building project", 65)
	s.SetPlanTasks([]PlanTask{
		{Name: "Setup environment", Status: TaskCompleted},
		{Name: "Write tests", Status: TaskInProgress},
		{Name: "Deploy to prod", Status: TaskPending},
	})
	s.SetRunningTools([]RunningTool{
		{ID: "1", Name: "shell", Command: "go test ./..."},
	})
	s.SetContextUsage(4700, 10000, 0)
	s.SetRecentFiles([]string{"pkg/main.go", "pkg/utils.go"})
	s.SetStyles(
		backend.DefaultStyle(),
		backend.DefaultStyle().Bold(true),
		backend.DefaultStyle(),
		backend.DefaultStyle().Foreground(backend.ColorGreen),
		backend.DefaultStyle(),
		backend.DefaultStyle(),
	)

	output := renderToString(s, 30, 20)
	assertSnapshot(t, "sidebar", output)
}

func TestSnapshot_Approval(t *testing.T) {
	req := ApprovalRequest{
		ID:          "req-1",
		Tool:        "run_shell",
		Operation:   "execute",
		Description: "Run a shell command",
		Command:     "rm -rf node_modules",
	}
	a := NewApprovalWidget(req)
	a.SetStyles(
		backend.DefaultStyle(),
		backend.DefaultStyle(),
		backend.DefaultStyle().Bold(true),
		backend.DefaultStyle(),
	)
	a.Focus()

	output := renderToString(a, 60, 15)
	assertSnapshot(t, "approval", output)
}

func TestSnapshot_ApprovalWithDiff(t *testing.T) {
	req := ApprovalRequest{
		ID:        "req-2",
		Tool:      "write_file",
		Operation: "edit",
		FilePath:  "pkg/main.go",
		DiffLines: []DiffLine{
			{Type: DiffContext, Content: "package main"},
			{Type: DiffContext, Content: ""},
			{Type: DiffRemove, Content: "func oldFunc() {}"},
			{Type: DiffAdd, Content: "func newFunc() {}"},
			{Type: DiffContext, Content: ""},
		},
		AddedLines:   1,
		RemovedLines: 1,
	}
	a := NewApprovalWidget(req)
	a.SetStyles(
		backend.DefaultStyle(),
		backend.DefaultStyle(),
		backend.DefaultStyle().Bold(true),
		backend.DefaultStyle(),
	)
	a.Focus()

	output := renderToString(a, 60, 18)
	assertSnapshot(t, "approval_diff", output)
}
