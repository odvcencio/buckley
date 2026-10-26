package scrollback

import (
	"strings"
	"testing"
)

func TestPositionForView_WordWrapPreservesSourceOffsets(t *testing.T) {
	const text = "alpha   beta   café 日本語 tail"
	for _, tc := range []struct {
		name   string
		styled bool
		prefix string
		code   bool
	}{
		{name: "plain"},
		{name: "styled", styled: true},
		{name: "prefixed", styled: true, prefix: "▶ "},
		{name: "code", code: true},
		{name: "prefixed code", styled: true, prefix: "▶ ", code: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := NewBuffer(12+len([]rune(tc.prefix)), 40)
			line := Line{Content: text, IsCode: tc.code}
			if tc.styled {
				line.Spans = []Span{{Text: text[:15]}, {Text: text[15:]}}
			}
			if tc.prefix != "" {
				line.Prefix = []Span{{Text: tc.prefix}}
			}
			buf.AppendMessage([]Line{line})
			rows := buf.GetVisibleLines()
			if len(rows) < 3 {
				t.Fatalf("fixture has %d wrapped rows, want at least 3", len(rows))
			}
			endLine, endColumn, ok := buf.PositionForView(len(rows)-1, 1000)
			if !ok || endLine != 0 || endColumn != len(text) {
				t.Fatalf("last row maps to (%d, %d, %v), want (0, %d, true)", endLine, endColumn, ok, len(text))
			}
			for _, reverse := range []bool{false, true} {
				if reverse {
					buf.StartSelection(endLine, endColumn)
					buf.UpdateSelection(0, 0)
				} else {
					buf.StartSelection(0, 0)
					buf.UpdateSelection(endLine, endColumn)
				}
				buf.EndSelection()
				if got := buf.GetSelection(); got != text {
					t.Fatalf("reverse=%v: copied %q, want complete source %q", reverse, got, text)
				}
			}
			if !tc.code {
				startLine, startColumn, ok := buf.PositionForView(1, len([]rune(tc.prefix)))
				if !ok || startLine != 0 || startColumn != strings.Index(text, "beta") {
					t.Fatalf("second row maps to (%d, %d, %v), want beta at byte %d", startLine, startColumn, ok, strings.Index(text, "beta"))
				}
				buf.StartSelection(startLine, startColumn)
				buf.UpdateSelection(endLine, endColumn)
				buf.EndSelection()
				if got := buf.GetSelection(); got != "beta   café 日本語 tail" {
					t.Fatalf("copied wrapped suffix %q", got)
				}
			}
		})
	}
}
