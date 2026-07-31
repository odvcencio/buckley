package widgets

import (
	"strings"
	"testing"
	"unsafe"

	"m31labs.dev/fluffyui/runtime"
)

// TestActivityPanelDetailLinesForCachesUnchangedRecord checks that calling
// detailLinesFor twice with the same record ID, width, and Detail length
// reuses the previously wrapped slice instead of re-wrapping.
func TestActivityPanelDetailLinesForCachesUnchangedRecord(t *testing.T) {
	p := NewActivityPanel()
	record := ActivityRecord{ID: "task-1", Detail: "line one\nline two"}

	first := p.detailLinesFor(record, 40)
	second := p.detailLinesFor(record, 40)

	if unsafe.SliceData(first) != unsafe.SliceData(second) {
		t.Fatal("expected detailLinesFor to reuse the cached slice for an unchanged record/width")
	}
}

// TestActivityPanelDetailLinesForInvalidatesOnWidthChange guards the width
// half of the cache key: a resize must re-wrap.
func TestActivityPanelDetailLinesForInvalidatesOnWidthChange(t *testing.T) {
	p := NewActivityPanel()
	record := ActivityRecord{ID: "task-1", Detail: "line one line two line three"}

	first := p.detailLinesFor(record, 10)
	second := p.detailLinesFor(record, 40)

	if unsafe.SliceData(first) == unsafe.SliceData(second) {
		t.Fatal("expected a width change to invalidate the cache")
	}
}

// TestActivityPanelDetailLinesForInvalidatesOnIDChange guards the record-ID
// half of the cache key: selecting a different record must re-wrap.
func TestActivityPanelDetailLinesForInvalidatesOnIDChange(t *testing.T) {
	p := NewActivityPanel()
	first := p.detailLinesFor(ActivityRecord{ID: "task-1", Detail: "hello"}, 40)
	second := p.detailLinesFor(ActivityRecord{ID: "task-2", Detail: "hello"}, 40)

	if unsafe.SliceData(first) == unsafe.SliceData(second) {
		t.Fatal("expected a record ID change to invalidate the cache")
	}
}

// TestActivityPanelDetailLinesForInvalidatesOnDetailLengthChange guards the
// Detail-length half of the cache key, which is how a streaming record's
// growing output invalidates stale wrapped lines.
func TestActivityPanelDetailLinesForInvalidatesOnDetailLengthChange(t *testing.T) {
	p := NewActivityPanel()
	first := p.detailLinesFor(ActivityRecord{ID: "task-1", Detail: "short"}, 40)
	second := p.detailLinesFor(ActivityRecord{ID: "task-1", Detail: "much longer detail text than before"}, 40)

	if unsafe.SliceData(first) == unsafe.SliceData(second) {
		t.Fatal("expected a Detail length change to invalidate the cache")
	}
	if len(second) == 0 {
		t.Fatal("expected wrapped lines for the updated record")
	}
}

// TestActivityPanelDetailLinesForMatchesDirectWrap checks the cache doesn't
// change wrapped output relative to calling activityDetailLines directly.
func TestActivityPanelDetailLinesForMatchesDirectWrap(t *testing.T) {
	p := NewActivityPanel()
	record := ActivityRecord{
		ID:      "task-1",
		Status:  ActivityCompleted,
		Summary: "summary text",
		Detail:  "some detail spanning\nmultiple lines of output",
	}
	got := p.detailLinesFor(record, 20)
	want := activityDetailLines(record, 20)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("cached lines diverge from direct wrap:\ngot:  %v\nwant: %v", got, want)
	}
}

// TestClampDetailOffset locks in the shared clamp logic extracted out of
// renderDetail and clampOffsets: the offset never exceeds lineCount-visible,
// and never goes negative when the content already fits on screen.
func TestClampDetailOffset(t *testing.T) {
	tests := []struct {
		name                             string
		offset, lineCount, visible, want int
	}{
		{name: "already in range", offset: 0, lineCount: 10, visible: 5, want: 0},
		{name: "clamped to max", offset: 100, lineCount: 10, visible: 5, want: 5},
		{name: "within bounds unchanged", offset: 3, lineCount: 10, visible: 5, want: 3},
		{name: "content fits on screen", offset: 3, lineCount: 3, visible: 5, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clampDetailOffset(tt.offset, tt.lineCount, tt.visible); got != tt.want {
				t.Errorf("clampDetailOffset(%d, %d, %d) = %d, want %d", tt.offset, tt.lineCount, tt.visible, got, tt.want)
			}
		})
	}
}

// TestActivityPanelRenderDetailAndClampOffsetsAgreeOnScrollLimit exercises
// both call sites that used to duplicate the clamp logic (renderDetail and
// clampOffsets) through the panel's real Layout/Render/scroll path, to
// guard against the dedupe changing behavior.
func TestActivityPanelRenderDetailAndClampOffsetsAgreeOnScrollLimit(t *testing.T) {
	p := NewActivityPanel()
	var detail strings.Builder
	for i := 0; i < 200; i++ {
		detail.WriteString("line of streamed output content\n")
	}
	p.SetActivities([]ActivityRecord{{ID: "task-1", Detail: detail.String(), Status: ActivityCompleted}})
	p.selected = 0
	p.showDetail = true

	bounds := runtime.Rect{X: 0, Y: 0, Width: 40, Height: 10}
	p.Layout(bounds)
	p.scroll(100000) // scroll far past the end; clampOffsets should cap it

	if p.detailOffset <= 0 {
		t.Fatalf("expected scrolling past the end to still land at a positive offset, got %d", p.detailOffset)
	}

	offsetAfterClamp := p.detailOffset
	p.Layout(bounds) // re-layout should not change the clamp result
	if p.detailOffset != offsetAfterClamp {
		t.Fatalf("expected stable clamp across relayout, got %d then %d", offsetAfterClamp, p.detailOffset)
	}
}
