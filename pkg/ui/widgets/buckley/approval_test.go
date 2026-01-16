package buckley

import (
	"testing"

	"github.com/odvcencio/buckley/pkg/ui/runtime"
	"github.com/odvcencio/buckley/pkg/ui/terminal"
)

func TestApprovalWidget_Measure(t *testing.T) {
	req := ApprovalRequest{
		ID:        "test-1",
		Tool:      "run_shell",
		Operation: "shell:write",
		Command:   "rm -rf node_modules",
	}
	w := NewApprovalWidget(req)

	size := w.Measure(runtime.Constraints{
		MaxWidth:  80,
		MaxHeight: 24,
	})

	// Should have reasonable dimensions
	if size.Width < 40 || size.Width > 70 {
		t.Errorf("unexpected width %d", size.Width)
	}
	if size.Height < 10 {
		t.Errorf("unexpected height %d", size.Height)
	}
}

func TestApprovalWidget_Layout_Centers(t *testing.T) {
	req := ApprovalRequest{
		ID:   "test-1",
		Tool: "write_file",
	}
	w := NewApprovalWidget(req)

	// Layout in larger area
	w.Layout(runtime.Rect{X: 0, Y: 0, Width: 120, Height: 40})

	bounds := w.Bounds()

	// Should be centered
	if bounds.X < 10 {
		t.Errorf("expected X > 10, got %d", bounds.X)
	}
	if bounds.Y < 5 {
		t.Errorf("expected Y > 5, got %d", bounds.Y)
	}
}

func TestApprovalWidget_HandleAllow(t *testing.T) {
	req := ApprovalRequest{
		ID:   "req-123",
		Tool: "run_shell",
	}
	w := NewApprovalWidget(req)

	// Press 'a' for allow
	result := w.HandleMessage(runtime.KeyMsg{Key: terminal.KeyRune, Rune: 'a'})

	if !result.Handled {
		t.Error("expected handled")
	}

	if len(result.Commands) != 2 {
		t.Fatalf("expected 2 commands, got %d", len(result.Commands))
	}

	// First command should be ApprovalResponse
	resp, ok := result.Commands[0].(ApprovalResponse)
	if !ok {
		t.Fatalf("expected ApprovalResponse, got %T", result.Commands[0])
	}

	if resp.RequestID != "req-123" {
		t.Errorf("expected request ID 'req-123', got '%s'", resp.RequestID)
	}
	if !resp.Approved {
		t.Error("expected Approved=true")
	}
	if resp.AlwaysAllow {
		t.Error("expected AlwaysAllow=false")
	}

	// Second command should be PopOverlay
	_, ok = result.Commands[1].(runtime.PopOverlay)
	if !ok {
		t.Errorf("expected PopOverlay, got %T", result.Commands[1])
	}
}

func TestApprovalWidget_HandleDeny(t *testing.T) {
	req := ApprovalRequest{
		ID:   "req-456",
		Tool: "write_file",
	}
	w := NewApprovalWidget(req)

	// Press 'd' for deny
	result := w.HandleMessage(runtime.KeyMsg{Key: terminal.KeyRune, Rune: 'd'})

	if !result.Handled {
		t.Error("expected handled")
	}

	resp, ok := result.Commands[0].(ApprovalResponse)
	if !ok {
		t.Fatalf("expected ApprovalResponse, got %T", result.Commands[0])
	}

	if resp.Approved {
		t.Error("expected Approved=false for deny")
	}
}

func TestApprovalWidget_HandleAlwaysAllow(t *testing.T) {
	req := ApprovalRequest{
		ID:   "req-789",
		Tool: "run_shell",
	}
	w := NewApprovalWidget(req)

	// Press 'l' for always allow
	result := w.HandleMessage(runtime.KeyMsg{Key: terminal.KeyRune, Rune: 'l'})

	if !result.Handled {
		t.Error("expected handled")
	}

	resp, ok := result.Commands[0].(ApprovalResponse)
	if !ok {
		t.Fatalf("expected ApprovalResponse, got %T", result.Commands[0])
	}

	if !resp.Approved {
		t.Error("expected Approved=true for always allow")
	}
	if !resp.AlwaysAllow {
		t.Error("expected AlwaysAllow=true")
	}
}

func TestApprovalWidget_HandleEscape(t *testing.T) {
	req := ApprovalRequest{
		ID:   "req-esc",
		Tool: "run_shell",
	}
	w := NewApprovalWidget(req)

	// Press Escape = deny
	result := w.HandleMessage(runtime.KeyMsg{Key: terminal.KeyEscape})

	if !result.Handled {
		t.Error("expected handled")
	}

	resp, ok := result.Commands[0].(ApprovalResponse)
	if !ok {
		t.Fatalf("expected ApprovalResponse, got %T", result.Commands[0])
	}

	if resp.Approved {
		t.Error("expected Approved=false for escape")
	}
}

func TestApprovalWidget_HandleYesNo(t *testing.T) {
	tests := []struct {
		key      rune
		approved bool
	}{
		{'y', true},
		{'Y', true},
		{'n', false},
		{'N', false},
		{'A', true},
		{'D', false},
	}

	for _, tt := range tests {
		t.Run(string(tt.key), func(t *testing.T) {
			w := NewApprovalWidget(ApprovalRequest{ID: "test"})
			result := w.HandleMessage(runtime.KeyMsg{Key: terminal.KeyRune, Rune: tt.key})

			if !result.Handled {
				t.Error("expected handled")
			}

			resp, ok := result.Commands[0].(ApprovalResponse)
			if !ok {
				t.Fatalf("expected ApprovalResponse, got %T", result.Commands[0])
			}

			if resp.Approved != tt.approved {
				t.Errorf("key '%c': expected Approved=%v, got %v", tt.key, tt.approved, resp.Approved)
			}
		})
	}
}

func TestApprovalWidget_ScrollDiff(t *testing.T) {
	// Create request with multiple diff lines
	lines := make([]DiffLine, 20)
	for i := range lines {
		lines[i] = DiffLine{Type: DiffContext, Content: "line content"}
	}

	req := ApprovalRequest{
		ID:        "diff-scroll",
		Tool:      "write_file",
		DiffLines: lines,
	}
	w := NewApprovalWidget(req)

	// Initial scroll should be 0
	if w.scrollOffset != 0 {
		t.Errorf("expected initial scrollOffset 0, got %d", w.scrollOffset)
	}

	// Scroll down
	result := w.HandleMessage(runtime.KeyMsg{Key: terminal.KeyDown})
	if !result.Handled {
		t.Error("down should be handled")
	}
	if w.scrollOffset != 1 {
		t.Errorf("expected scrollOffset 1 after down, got %d", w.scrollOffset)
	}

	// Scroll up
	result = w.HandleMessage(runtime.KeyMsg{Key: terminal.KeyUp})
	if !result.Handled {
		t.Error("up should be handled")
	}
	if w.scrollOffset != 0 {
		t.Errorf("expected scrollOffset 0 after up, got %d", w.scrollOffset)
	}

	// Scroll up at top shouldn't go negative
	w.HandleMessage(runtime.KeyMsg{Key: terminal.KeyUp})
	if w.scrollOffset != 0 {
		t.Errorf("scrollOffset should not go negative, got %d", w.scrollOffset)
	}
}

func TestApprovalWidget_Render(t *testing.T) {
	req := ApprovalRequest{
		ID:          "render-test",
		Tool:        "run_shell",
		Operation:   "shell:write",
		Description: "Execute a shell command",
		Command:     "npm install",
	}
	w := NewApprovalWidget(req)
	w.Focus()
	w.Layout(runtime.Rect{X: 0, Y: 0, Width: 80, Height: 24})

	buf := runtime.NewBuffer(80, 24)
	ctx := runtime.RenderContext{Buffer: buf}

	// Should not panic
	w.Render(ctx)

	// Check for border corners in the centered position
	bounds := w.Bounds()
	topLeft := buf.Get(bounds.X, bounds.Y)
	if topLeft.Rune != '╭' {
		t.Errorf("expected top-left corner, got '%c'", topLeft.Rune)
	}
}

func TestApprovalWidget_WithDiff(t *testing.T) {
	req := ApprovalRequest{
		ID:        "diff-test",
		Tool:      "write_file",
		Operation: "write",
		FilePath:  "pkg/api/server.go",
		DiffLines: []DiffLine{
			{Type: DiffRemove, Content: "func oldHandler() {"},
			{Type: DiffAdd, Content: "func newHandler() {"},
			{Type: DiffContext, Content: "    // handler code"},
			{Type: DiffAdd, Content: "    log.Info(\"request\")"},
			{Type: DiffContext, Content: "}"},
		},
		AddedLines:   2,
		RemovedLines: 1,
	}
	w := NewApprovalWidget(req)
	w.Layout(runtime.Rect{X: 0, Y: 0, Width: 80, Height: 30})

	buf := runtime.NewBuffer(80, 30)
	ctx := runtime.RenderContext{Buffer: buf}

	// Should render without panic
	w.Render(ctx)

	// Widget should be taller to accommodate diff
	size := w.Measure(runtime.Constraints{MaxWidth: 80, MaxHeight: 30})
	if size.Height < 15 {
		t.Errorf("expected taller widget for diff preview, got height %d", size.Height)
	}
}

func TestFormatDiffSummary(t *testing.T) {
	tests := []struct {
		added    int
		removed  int
		expected string
	}{
		{0, 0, "no changes"},
		{5, 0, "+5 lines"},
		{0, 3, "-3 lines"},
		{10, 5, "+10 lines, -5 lines"},
		{1, 1, "+1 lines, -1 lines"},
	}

	for _, tt := range tests {
		result := formatDiffSummary(tt.added, tt.removed)
		if result != tt.expected {
			t.Errorf("formatDiffSummary(%d, %d) = %q, want %q",
				tt.added, tt.removed, result, tt.expected)
		}
	}
}
