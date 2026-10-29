package scrollback

import "testing"

func TestTargetMessage_InteriorReplacementAndEviction(t *testing.T) {
	b := NewBuffer(80, 20)
	b.AppendTargetMessage(7, []Line{{Content: "first", Source: "assistant"}, {Content: "second", Source: "assistant"}})
	b.AppendMessage([]Line{{Content: "later", Source: "user"}})
	b.StartSelection(2, 0)
	b.UpdateSelection(2, 5)
	b.EndSelection()
	b.Search("later")
	if !b.ReplaceTargetMessage(7, []Line{{Content: "new", Source: "assistant"}, {Content: "middle", Source: "assistant"}, {Content: "tail", Source: "assistant"}}) {
		t.Fatal("target missing")
	}
	if got := b.GetSelection(); got != "later" {
		t.Fatalf("selection %q", got)
	}
	if _, got := b.SearchMatches(); got != 1 {
		t.Fatalf("search matches %d", got)
	}
	if b.LineCount() != 4 || b.lines[3].Content != "later" || b.bytes <= 0 {
		t.Fatalf("lines=%+v bytes=%d", b.lines, b.bytes)
	}
	b.SetMaxLines(3)
	if !b.ReplaceTargetMessage(7, []Line{{Content: "new", Source: "assistant"}, {Content: "middle", Source: "assistant"}, {Content: "tail extended", Source: "assistant"}}) {
		t.Fatal("partially trimmed target missing")
	}
	if b.lines[0].Content != "middle" || b.lines[1].Content != "tail extended" || b.lines[2].Content != "later" {
		t.Fatalf("partial trim: %+v", b.lines)
	}
	b.SetMaxLines(1)
	if b.ReplaceTargetMessage(7, []Line{{Content: "wrong"}}) || b.lines[0].Content != "later" {
		t.Fatal("evicted target changed another message")
	}
}

func TestTargetMessage_EndClearAndEmpty(t *testing.T) {
	b := NewBuffer(80, 20)
	b.AppendTargetMessage(3, []Line{{Content: "old", Source: "assistant"}})
	if !b.ReplaceTargetMessage(3, []Line{{Content: "", Source: "assistant"}}) || b.lines[0].Content != "" {
		t.Fatal("empty replacement failed")
	}
	b.EndTarget(3)
	if b.ReplaceTargetMessage(3, []Line{{Content: "wrong"}}) {
		t.Fatal("ended target remained live")
	}
	b.AppendTargetMessage(4, []Line{{Content: "active", Source: "assistant"}})
	b.Clear()
	if b.ReplaceTargetMessage(4, []Line{{Content: "wrong"}}) {
		t.Fatal("clear retained target")
	}
	b.AppendTargetMessage(4, []Line{{Content: "new", Source: "assistant"}})
	if !b.ReplaceTargetMessage(4, []Line{{Content: "updated", Source: "assistant"}}) || b.lines[0].Content != "updated" {
		t.Fatal("re-add failed")
	}
}

func TestTargetMessage_ManualScrollAndOverlappingSelection(t *testing.T) {
	b := NewBuffer(80, 2)
	b.AppendMessage([]Line{{Content: "before"}})
	b.AppendTargetMessage(9, []Line{{Content: "target"}})
	b.AppendMessage([]Line{{Content: "after1"}, {Content: "after2"}, {Content: "after3"}})
	b.ScrollToBottom()
	b.ScrollUp(1)
	top, _, _ := b.ScrollPosition()
	b.StartSelection(0, 0)
	b.UpdateSelection(3, 3)
	b.EndSelection()
	if !b.ReplaceTargetMessage(9, []Line{{Content: "target1"}, {Content: "target2"}, {Content: "target3"}}) {
		t.Fatal("missing")
	}
	next, _, _ := b.ScrollPosition()
	if next != top+2 {
		t.Fatalf("scroll top=%d want %d", next, top+2)
	}
	if b.HasSelection() {
		t.Fatal("selection spanning replacement was preserved")
	}
}

func TestTargetMessage_ByteBudgetAndAccounting(t *testing.T) {
	b := NewBuffer(80, 10)
	b.SetMaxBytes(2500)
	b.AppendTargetMessage(1, []Line{{Content: "one", Source: "assistant"}, {Content: "two", Source: "assistant"}})
	b.AppendMessage([]Line{{Content: "later", Source: "user"}})
	if !b.ReplaceTargetMessage(1, []Line{{Content: "one changed", Source: "assistant"}, {Content: "two changed", Source: "assistant"}, {Content: "three", Source: "assistant"}}) {
		t.Fatal("missing")
	}
	sum, rows := 0, 0
	for _, line := range b.lines {
		sum += lineRetainedBytes(line)
		rows += len(line.Wrapped)
	}
	if b.bytes != sum || b.totalRows != rows || b.bytes > b.maxBytes {
		t.Fatalf("bytes=%d sum=%d rows=%d want=%d limit=%d", b.bytes, sum, b.totalRows, rows, b.maxBytes)
	}
}

func TestTargetMessage_InsertBeforePreservesViewportSelectionAndSearch(t *testing.T) {
	b := NewBuffer(80, 2)
	b.AppendMessage([]Line{{Content: "before"}})
	b.AppendTargetMessage(1, []Line{{Content: "answer", Source: "assistant"}})
	b.AppendMessage([]Line{{Content: "after"}, {Content: "more"}})
	b.ScrollToBottom()
	b.ScrollUp(1)
	top, _, _ := b.ScrollPosition()
	b.StartSelection(2, 0)
	b.UpdateSelection(2, 5)
	b.EndSelection()
	b.Search("after")
	if !b.InsertTargetMessageBefore(2, 1, []Line{{Content: "thought", Source: "thinking"}, {Content: "detail", Source: "thinking"}}) {
		t.Fatal("anchor missing")
	}
	if b.lines[1].Content != "thought" || b.lines[3].Content != "answer" {
		t.Fatalf("order %+v", b.lines)
	}
	if got := b.GetSelection(); got != "after" {
		t.Fatalf("selection=%q", got)
	}
	if _, n := b.SearchMatches(); n != 1 {
		t.Fatalf("search count=%d", n)
	}
	if now, _, _ := b.ScrollPosition(); now != top+2 {
		t.Fatalf("scroll=%d want=%d", now, top+2)
	}
	rows, bytes := 0, 0
	for _, l := range b.lines {
		rows += len(l.Wrapped)
		bytes += lineRetainedBytes(l)
	}
	if rows != b.totalRows || bytes != b.bytes {
		t.Fatal("incorrect accounting")
	}
	if b.InsertTargetMessageBefore(3, 99, []Line{{Content: "bad"}}) {
		t.Fatal("missing anchor accepted")
	}
}

func TestAuxRemoval_PreservesLaterSelectionSearchAndViewport(t *testing.T) {
	b := NewBuffer(80, 2)
	b.AppendAuxLine("waiting", LineStyle{}, "thinking")
	b.AppendMessage([]Line{{Content: "reasoning"}, {Content: "answer"}, {Content: "tail"}})
	b.ScrollToBottom()
	b.ScrollUp(1)
	top, _, _ := b.ScrollPosition()
	b.StartSelection(2, 0)
	b.UpdateSelection(2, 6)
	b.EndSelection()
	b.Search("answer")
	if !b.RemoveAuxLinesIfSource("thinking") {
		t.Fatal("missing indicator")
	}
	if got := b.GetSelection(); got != "answer" {
		t.Fatalf("selection=%q", got)
	}
	if _, n := b.SearchMatches(); n != 1 {
		t.Fatalf("matches=%d", n)
	}
	if now, _, _ := b.ScrollPosition(); now != top-1 {
		t.Fatalf("scroll=%d want %d", now, top-1)
	}
	rows, bytes := 0, 0
	for _, l := range b.lines {
		rows += len(l.Wrapped)
		bytes += lineRetainedBytes(l)
	}
	if rows != b.totalRows || bytes != b.bytes {
		t.Fatal("incorrect accounting")
	}
}
