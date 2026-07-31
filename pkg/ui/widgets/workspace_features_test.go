package widgets

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"m31labs.dev/fluffyui/markdown"
	"m31labs.dev/fluffyui/runtime"
	"m31labs.dev/fluffyui/theme"
)

func TestActivityPanelClickOpensDetail(t *testing.T) {
	panel := NewActivityPanel()
	panel.SetActivities([]ActivityRecord{{
		ID:        "tool-1",
		Kind:      "tool",
		Title:     "read_file",
		Summary:   "pkg/model/manager.go",
		Detail:    "Result:\nfull file detail",
		Status:    ActivityCompleted,
		StartedAt: time.Now(),
	}})
	panel.Layout(runtime.Rect{X: 10, Y: 2, Width: 36, Height: 12})
	result := panel.HandleMessage(runtime.MouseMsg{X: 14, Y: 5, Button: runtime.MouseLeft, Action: runtime.MousePress})
	if !result.Handled || !panel.showDetail {
		t.Fatal("activity row click should open detail")
	}
}

func TestActivityDetailLinesIncludeMetadata(t *testing.T) {
	started := time.Now().Add(-time.Second)
	joined := strings.Join(activityDetailLines(ActivityRecord{
		Title:      "write_file",
		Summary:    "updated parser",
		Path:       "pkg/parser.go",
		Operation:  "write",
		Status:     ActivityCompleted,
		StartedAt:  started,
		FinishedAt: time.Now(),
		Detail:     "Arguments:\n{\"path\":\"pkg/parser.go\"}",
	}, 40), "\n")
	for _, want := range []string{"Status: completed", "Path:", "pkg/parser.go", "Operation: write", "Arguments:"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("detail missing %q: %s", want, joined)
		}
	}
}

func TestPrepareMarkdownForWorkspaceCompactsTables(t *testing.T) {
	renderer := markdown.NewRenderer(theme.DefaultTheme())
	lines := prepareMarkdownForWorkspace(renderer.RenderWidth("assistant", "| Name | State |\n| --- | --- |\n| parser | ready |", 80))

	var dataRow, topBorder, headerSeparator, bottomBorder string
	for _, line := range lines {
		text := markdownSpanText(line.Spans)
		switch {
		case strings.Contains(text, "parser"):
			dataRow = text
		case strings.HasPrefix(text, "╭"):
			topBorder = text
		case strings.HasPrefix(text, "├"):
			headerSeparator = text
		case strings.HasPrefix(text, "╰"):
			bottomBorder = text
		}
	}

	if dataRow == "" || topBorder == "" || headerSeparator == "" || bottomBorder == "" {
		t.Fatalf("expected data and border rows, got data=%q top=%q sep=%q bottom=%q", dataRow, topBorder, headerSeparator, bottomBorder)
	}
	if strings.Contains(dataRow, "│ ") || strings.Contains(dataRow, " │") {
		t.Fatalf("table row was not compacted: %q", dataRow)
	}

	dataWidth := utf8.RuneCountInString(dataRow)
	for name, border := range map[string]string{"top": topBorder, "header separator": headerSeparator, "bottom": bottomBorder} {
		if width := utf8.RuneCountInString(border); width != dataWidth {
			t.Fatalf("%s border width = %d, data row width = %d; junctions will not align: border=%q data=%q", name, width, dataWidth, border, dataRow)
		}
	}
}

func TestPrepareMarkdownForWorkspaceRendersTaskGlyphs(t *testing.T) {
	renderer := markdown.NewRenderer(theme.DefaultTheme())
	lines := prepareMarkdownForWorkspace(renderer.RenderWidth("assistant", "- [ ] inspect parser\n- [x] add fixture", 80))
	var rendered strings.Builder
	for _, line := range lines {
		rendered.WriteString(markdownSpanText(line.Spans))
		rendered.WriteByte('\n')
	}
	if !strings.Contains(rendered.String(), "☐ inspect parser") || !strings.Contains(rendered.String(), "☑ add fixture") {
		t.Fatalf("task glyphs missing: %q", rendered.String())
	}
}

func TestPrepareMarkdownForWorkspaceSkipsCodeAndProse(t *testing.T) {
	renderer := markdown.NewRenderer(theme.DefaultTheme())
	source := "- [ ] real todo\n\n" +
		"note that `arr[x] value` reads an element, and so does arr[x] value\n\n" +
		"```go\n// [ ] pending\nv := arr[x] // still just code\n```\n"
	lines := prepareMarkdownForWorkspace(renderer.RenderWidth("assistant", source, 80))

	var rendered strings.Builder
	for _, line := range lines {
		rendered.WriteString(markdownSpanText(line.Spans))
		rendered.WriteByte('\n')
	}
	got := rendered.String()

	if !strings.Contains(got, "☐ real todo") {
		t.Fatalf("real task-list line should render a glyph: %q", got)
	}
	if !strings.Contains(got, "arr[x] value") {
		t.Fatalf("prose containing arr[x] value should pass through untouched: %q", got)
	}
	if !strings.Contains(got, "[ ] pending") {
		t.Fatalf("fenced code containing [ ] pending should pass through untouched: %q", got)
	}
	if strings.Contains(got, "☑") {
		t.Fatalf("no line should have been mistaken for a checked task: %q", got)
	}
}

func TestMarkdownPPForTerminalPreservesTaskLists(t *testing.T) {
	got := markdownPPForTerminal("- [ ] inspect parser\n- [x] add fixture")
	if !strings.Contains(got, "- [ ] inspect parser") || !strings.Contains(got, "- [x] add fixture") {
		t.Fatalf("Markdown++ lowering lost task list semantics: %q", got)
	}
}

func TestChatViewScrollbarClickAndDrag(t *testing.T) {
	view := NewChatView()
	view.Layout(runtime.Rect{X: 0, Y: 0, Width: 40, Height: 6})
	for i := 0; i < 30; i++ {
		view.AddMessage("line", "system")
	}
	view.ScrollToTop()
	before, _, _ := view.ScrollPosition()
	press := view.HandleMessage(runtime.MouseMsg{X: 39, Y: 3, Button: runtime.MouseLeft, Action: runtime.MousePress})
	after, _, _ := view.ScrollPosition()
	if !press.Handled || after <= before {
		t.Fatalf("scrollbar click did not seek: before=%d after=%d", before, after)
	}
	if !view.HandleMessage(runtime.MouseMsg{X: 39, Y: 5, Button: runtime.MouseNone, Action: runtime.MouseMove}).Handled {
		t.Fatal("scrollbar drag should be handled")
	}
	if !view.HandleMessage(runtime.MouseMsg{X: 39, Y: 5, Button: runtime.MouseLeft, Action: runtime.MouseRelease}).Handled || view.scrollbarDragging {
		t.Fatal("scrollbar release should end dragging")
	}
}

func TestSemanticScrollbarYClamps(t *testing.T) {
	if got := semanticScrollbarY(-1, 100, 10); got != 0 {
		t.Fatalf("low mark = %d, want 0", got)
	}
	if got := semanticScrollbarY(200, 100, 10); got != 9 {
		t.Fatalf("high mark = %d, want 9", got)
	}
}
