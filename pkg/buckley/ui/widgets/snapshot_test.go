package widgets

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/odvcencio/fluffy-ui/backend"
	"github.com/odvcencio/fluffy-ui/runtime"
	uitesting "github.com/odvcencio/fluffy-ui/testing"
)

var updateSnapshots = flag.Bool("update-snapshots", false, "Update golden snapshot files")

// renderSnapshot renders via the simulation backend for snapshot comparison.
func renderSnapshot(w runtime.Widget, width, height int) string {
	be := uitesting.RenderWidget(w, width, height)
	output := be.Capture()
	if !strings.HasSuffix(output, "\n") {
		output += "\n"
	}
	return output
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

func TestSnapshot_InputArea(t *testing.T) {
	ia := NewInputArea()
	ia.SetStyles(
		backend.DefaultStyle(),
		backend.DefaultStyle(),
		backend.DefaultStyle(),
	)
	ia.SetModeStyles(
		backend.DefaultStyle().Foreground(backend.ColorGreen),
		backend.DefaultStyle().Foreground(backend.ColorYellow),
		backend.DefaultStyle().Foreground(backend.ColorCyan),
		backend.DefaultStyle().Foreground(backend.ColorMagenta),
	)
	ia.Focus()

	output := renderSnapshot(ia, 80, 3)
	assertSnapshot(t, "inputarea", output)
}

func TestSnapshot_Header(t *testing.T) {
	h := NewHeader()
	h.SetModelName("gpt-4o")
	h.SetStyles(
		backend.DefaultStyle(),
		backend.DefaultStyle().Bold(true),
		backend.DefaultStyle(),
	)

	output := renderSnapshot(h, 80, 1)
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

	output := renderSnapshot(s, 80, 1)
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

	output := renderSnapshot(s, 30, 20)
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

	output := renderSnapshot(a, 60, 15)
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

	output := renderSnapshot(a, 60, 18)
	assertSnapshot(t, "approval_diff", output)
}
