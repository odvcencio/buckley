package widgets

import (
	"fmt"
	"strings"
	"testing"

	"github.com/odvcencio/gotreesitter"
	"m31labs.dev/buckley/pkg/ui/scrollback"
	"m31labs.dev/fluffyui/backend"
)

func highlightSpanFixture(count int) ([]scrollback.Line, []gotreesitter.HighlightRange) {
	const content = "var value = \"café 日本語\""
	lines := make([]scrollback.Line, count)
	ranges := make([]gotreesitter.HighlightRange, 0, 2*count)
	for i := range lines {
		lines[i].Content = content
		start := uint32(i * (len(content) + 1))
		ranges = append(ranges,
			gotreesitter.HighlightRange{StartByte: start, EndByte: start + 3, Capture: "keyword"},
			gotreesitter.HighlightRange{StartByte: start + uint32(strings.IndexByte(content, '"')), EndByte: start + uint32(len(content)), Capture: "string"},
		)
	}
	return lines, ranges
}

func TestChatView_CodeLineSpans_StorageScalesWithLines(t *testing.T) {
	for _, count := range []int{32, 512, 4096} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			view := NewChatView()
			lines, ranges := highlightSpanFixture(count)
			start, capacity := 0, 0
			for i := range lines {
				line := &lines[i]
				line.Spans = view.codeLineSpans(line.Content, start, start+len(line.Content), ranges)
				if spansText(line.Spans) != line.Content || len(line.Spans) != 3 {
					t.Fatalf("line %d lost source or captures", i)
				}
				capacity += cap(line.Spans)
				start += len(line.Content) + 1
			}
			if capacity > 8*count {
				t.Fatalf("%d lines retained %d span slots; allocation grew with distant captures", count, capacity)
			}
		})
	}
}

func TestChatView_CodeLineSpans_PreservesMultilineAndUnicodeStyles(t *testing.T) {
	view := NewChatView()
	plain := backend.DefaultStyle().Foreground(backend.ColorWhite)
	muted := plain.Foreground(backend.ColorBrightBlack).Italic(true)
	keyword := plain.Foreground(backend.ColorBlue).Bold(true)
	str := plain.Foreground(backend.ColorGreen)
	view.SetCodeSyntaxStyles(plain, muted, keyword, plain, str, plain, plain, plain)
	const source = "zero\n/* café\n日本語 */\n\nvar s = \"Ω\"\nlast"
	commentStart := uint32(strings.Index(source, "/*"))
	commentEnd := uint32(strings.Index(source, "*/") + 2)
	keywordStart := uint32(strings.Index(source, "var"))
	stringStart := uint32(strings.IndexByte(source, '"'))
	ranges := []gotreesitter.HighlightRange{
		{StartByte: commentStart, EndByte: commentEnd, Capture: "comment"},
		{StartByte: keywordStart, EndByte: keywordStart + 3, Capture: "keyword"},
		{StartByte: stringStart, EndByte: stringStart + uint32(len("\"Ω\"")), Capture: "string"},
	}
	var styled []scrollback.Line
	start := 0
	for _, content := range strings.Split(source, "\n") {
		spans := view.codeLineSpans(content, start, start+len(content), ranges)
		if spansText(spans) != content {
			t.Fatalf("line at byte %d lost source: %q", start, spansText(spans))
		}
		styled = append(styled, scrollback.Line{Content: content, Spans: spans})
		start += len(content) + 1
	}
	for _, want := range []struct {
		text  string
		style backend.Style
	}{{"zero", plain}, {"/* café", muted}, {"日本語 */", muted}, {"var", keyword}, {"\"Ω\"", str}, {"last", plain}} {
		got, found := styleForText(styled, want.text)
		if !found || got != want.style {
			t.Errorf("style for %q = %v (found=%v), want %v", want.text, got, found, want.style)
		}
	}
}

func BenchmarkChatView_CodeLineSpans(b *testing.B) {
	for _, count := range []int{64, 512} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			view := NewChatView()
			lines, ranges := highlightSpanFixture(count)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				start := 0
				for i := range lines {
					line := &lines[i]
					line.Spans = view.codeLineSpans(line.Content, start, start+len(line.Content), ranges)
					start += len(line.Content) + 1
				}
			}
		})
	}
}
