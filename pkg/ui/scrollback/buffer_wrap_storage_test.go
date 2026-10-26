package scrollback

import (
	"reflect"
	"runtime"
	"strings"
	"testing"

	"m31labs.dev/fluffyui/backend"
)

func TestBuffer_StyledRowsRetainStyleRuns(t *testing.T) {
	text := strings.Repeat("café 日本語 ", 8)
	style := backend.DefaultStyle().Foreground(backend.ColorBlue)
	prefix := []Span{{Text: "│ ", Style: backend.DefaultStyle()}}
	spans := []Span{{Text: text[:len(text)/2], Style: style}, {Text: text[len(text)/2:], Style: style}}
	buf := NewBuffer(100, 8)
	buf.AppendStyledLine(text, spans, prefix, "assistant", true, "go")
	rows := buf.GetVisibleLines()
	if len(rows) != 1 || len(rows[0].Spans) != 2 || rows[0].Content != "│ "+text {
		t.Fatalf("styled source or merged runs changed: %#v", rows)
	}
	if got, bound := cap(rows[0].Spans), 2*len(rows[0].Spans); got > bound {
		t.Fatalf("styled row retained %d span slots for %d runs; want at most %d", got, len(rows[0].Spans), bound)
	}
	line, column, ok := buf.PositionForView(0, 1000)
	if !ok {
		t.Fatal("styled row has no source position")
	}
	buf.StartSelection(0, 0)
	buf.UpdateSelection(line, column)
	buf.EndSelection()
	if got := buf.GetSelection(); got != text {
		t.Fatalf("copied styled source = %q, want %q", got, text)
	}
}

func TestWrapStyledLine_PreservesTextAndStyles(t *testing.T) {
	blue := backend.DefaultStyle().Foreground(backend.ColorBlue)
	green := backend.DefaultStyle().Foreground(backend.ColorGreen)
	border := backend.DefaultStyle().Foreground(backend.ColorRed)
	for _, tc := range []struct {
		name          string
		text          string
		spans, prefix []Span
		width         int
		code          bool
		want          []WrappedLine
	}{
		{name: "unicode code", text: "café 日本語", spans: []Span{{Text: "café ", Style: blue}, {Text: "日本語", Style: green}}, width: 6, code: true,
			want: []WrappedLine{{Text: "café 日", Spans: []Span{{Text: "café ", Style: blue}, {Text: "日", Style: green}}}, {Text: "本語", Spans: []Span{{Text: "本語", Style: green}}}}},
		{name: "word wrapping and prefix", text: "one  two café tail", spans: []Span{{Text: "one  two café tail", Style: blue}}, prefix: []Span{{Text: "> ", Style: border}}, width: 8,
			want: []WrappedLine{{Text: "> one", Spans: []Span{{Text: "> ", Style: border}, {Text: "one", Style: blue}}}, {Text: "  two", Spans: []Span{{Text: "  "}, {Text: "two", Style: blue}}}, {Text: "  café", Spans: []Span{{Text: "  "}, {Text: "café", Style: blue}}}, {Text: "  tail", Spans: []Span{{Text: "  "}, {Text: "tail", Style: blue}}}}},
		{name: "fragmented matching styles", text: "AB🙂é", spans: []Span{{Text: "A", Style: blue}, {Text: "B", Style: blue}, {Text: "🙂", Style: green}, {Text: "é", Style: green}}, width: 8, code: true,
			want: []WrappedLine{{Text: "AB🙂é", Spans: []Span{{Text: "AB", Style: blue}, {Text: "🙂é", Style: green}}}}},
		{name: "invalid UTF-8", text: "x\xffz", width: 10, code: true,
			want: []WrappedLine{{Text: "x�z", Spans: []Span{{Text: "x�z", Style: backend.DefaultStyle()}}}}},
		{name: "empty prefix", prefix: []Span{{Text: "> ", Style: border}}, width: 8,
			want: []WrappedLine{{Text: "> ", Spans: []Span{{Text: "> ", Style: border}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := wrapStyledLine(tc.text, tc.spans, tc.prefix, tc.width, tc.code)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("wrapped text/styles = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func BenchmarkWrapStyledLine(b *testing.B) {
	blue := backend.DefaultStyle().Foreground(backend.ColorBlue)
	green := backend.DefaultStyle().Foreground(backend.ColorGreen)
	uniform := strings.Repeat("alpha ", 20)
	var alternating []Span
	for i := range 128 {
		style := blue
		if i%2 != 0 {
			style = green
		}
		alternating = append(alternating, Span{Text: "x", Style: style})
	}
	for _, tc := range []struct {
		name, text string
		spans      []Span
		code       bool
	}{
		{name: "uniform", text: uniform, spans: []Span{{Text: uniform, Style: blue}}},
		{name: "syntax", text: "var value = \"café 日本語\" // preserved", code: true, spans: []Span{{Text: "var", Style: blue}, {Text: " value = ", Style: backend.DefaultStyle()}, {Text: "\"café 日本語\"", Style: green}, {Text: " // preserved", Style: blue}}},
		{name: "alternating", text: strings.Repeat("x", 128), code: true, spans: alternating},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			var result []WrappedLine
			for range b.N {
				result = wrapStyledLine(tc.text, tc.spans, nil, 80, tc.code)
			}
			runtime.KeepAlive(result)
		})
	}
}
