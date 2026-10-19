package scrollback

import (
	"strings"
	"testing"
)

func TestBuffer_ByteBudgetSurvivesAppendReplaceResizeAndClear(t *testing.T) {
	b := NewBuffer(80, 20)
	const limit = 16 << 10
	b.SetMaxBytes(limit)
	for i := range 500 {
		b.AppendMessage([]Line{{Content: strings.Repeat("line", 100), Source: "assistant"}})
		b.AppendText("streaming")
		if i%10 == 0 {
			b.Resize(20+i%80, 20)
		}
		if b.bytes > limit {
			t.Fatalf("retained %d at %d", b.bytes, i)
		}
	}
	b.Clear()
	if b.bytes != 0 || len(b.lines) != 0 || cap(b.lines) != 0 {
		t.Fatalf("clear retained allocation: bytes=%d count=%d cap=%d", b.bytes, len(b.lines), cap(b.lines))
	}
}

func TestBuffer_ByteBudgetKeepsStreamingReplacementAfterEviction(t *testing.T) {
	b := NewBuffer(80, 20)
	b.SetMaxBytes(2048)
	message := make([]Line, 20)
	for i := range message {
		message[i] = Line{Content: "old streamed line", Source: "assistant"}
	}
	b.AppendMessage(message)
	if b.LineCount() >= len(message) {
		t.Fatal("test did not evict old lines")
	}
	b.ReplaceLastMessage([]Line{{Content: "replacement", Source: "assistant"}})
	if b.LineCount() != 1 || b.lines[0].Content != "replacement" {
		t.Fatalf("replacement duplicated evicted tail: %+v", b.lines)
	}
}
