package widgets

import (
	"strings"
	"testing"

	"m31labs.dev/buckley/v2/pkg/ui/scrollback"
	"m31labs.dev/fluffyui/backend"
	"m31labs.dev/fluffyui/markdown"
	"m31labs.dev/fluffyui/runtime"
	"m31labs.dev/fluffyui/theme"
)

func TestChatView_TreeSitterHighlightsGoCodeBlock(t *testing.T) {
	cv := NewChatView()
	cv.SetMarkdownRenderer(markdown.NewRenderer(theme.DefaultTheme()), backend.DefaultStyle().Background(backend.ColorBlack))
	cv.SetCodeSyntaxStyles(
		backend.DefaultStyle().Foreground(backend.ColorWhite),
		backend.DefaultStyle().Foreground(backend.ColorBrightBlack),
		backend.DefaultStyle().Foreground(backend.ColorBlue),
		backend.DefaultStyle().Foreground(backend.ColorCyan),
		backend.DefaultStyle().Foreground(backend.ColorGreen),
		backend.DefaultStyle().Foreground(backend.ColorYellow),
		backend.DefaultStyle().Foreground(backend.ColorMagenta),
		backend.DefaultStyle().Foreground(backend.ColorRed),
	)
	cv.Layout(runtime.Rect{Width: 100, Height: 24})

	lines := cv.renderMarkdownLines("```go\nfunc greet(name string) {\n\tfmt.Println(\"hello\", name)\n}\n```", "assistant")

	funcStyle, foundFunc := styleForText(lines, "func")
	if !foundFunc {
		t.Fatal("missing func token")
	}
	if got := funcStyle.ForegroundColor(); got != backend.ColorBlue {
		t.Fatalf("func style foreground = %v, want keyword accent %v", got, backend.ColorBlue)
	}
	stringStyle, foundString := styleForText(lines, "\"hello\"")
	if !foundString {
		t.Fatal("missing string token")
	}
	if got := stringStyle.ForegroundColor(); got != backend.ColorGreen {
		t.Fatalf("string style foreground = %v, want string success %v", got, backend.ColorGreen)
	}
}

func TestChatView_TreeSitterFallsBackForUnknownFenceLanguage(t *testing.T) {
	cv := NewChatView()
	cv.SetMarkdownRenderer(markdown.NewRenderer(theme.DefaultTheme()), backend.DefaultStyle())
	cv.Layout(runtime.Rect{Width: 100, Height: 24})

	lines := cv.renderMarkdownLines("```not-a-real-language\nexample text\n```", "assistant")
	for _, line := range lines {
		if line.IsCode && !line.IsCodeHeader && strings.Contains(line.Content, "example text") {
			if len(line.Spans) == 0 || spansText(line.Spans) != "example text" {
				t.Fatalf("unknown language fallback lost code content: %#v", line)
			}
			return
		}
	}
	t.Fatal("missing unknown-language code line")
}

func styleForText(lines []scrollback.Line, want string) (backend.Style, bool) {
	for _, line := range lines {
		for _, span := range line.Spans {
			if span.Text == want {
				return span.Style, true
			}
		}
	}
	return backend.DefaultStyle(), false
}
