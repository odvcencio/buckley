package widgets

import (
	"reflect"
	"testing"

	"m31labs.dev/fluffyui/backend"
	"m31labs.dev/fluffyui/markdown"
	"m31labs.dev/fluffyui/runtime"
	"m31labs.dev/fluffyui/theme"
)

func TestChatView_ClearContent_ReusesSyntaxAndResetsTranscript(t *testing.T) {
	view := NewChatView()
	view.SetMarkdownRenderer(markdown.NewRenderer(theme.DefaultTheme()), backend.DefaultStyle())
	view.Layout(runtime.Rect{Width: 80, Height: 40})
	view.AddMessage("```go\npackage old\nconst previous = 1\n```", "assistant")
	if len(view.highlighters) != 1 {
		t.Fatal("fixture did not create a cached syntax parser")
	}
	parser := view.highlighters[0].highlighter
	view.StartSelection(0, 0)
	view.UpdateSelection(1, 2)
	view.ClearContent()
	if view.buffer.LineCount() != 0 || view.HasSelection() || view.lastSource != "" || view.lastContent != "" {
		t.Fatal("content reset retained transcript or streaming state")
	}
	if len(view.highlighters) != 1 || view.highlighters[0].highlighter != parser {
		t.Fatal("content reset discarded the bounded syntax parser")
	}
	const content = "```go\npackage main\nfunc answer() string { return \"café 日本語\" }\n"
	view.AddMessage(content, "assistant")
	view.AppendText("```")
	fresh := NewChatView()
	fresh.SetMarkdownRenderer(markdown.NewRenderer(theme.DefaultTheme()), backend.DefaultStyle())
	fresh.Layout(runtime.Rect{Width: 80, Height: 40})
	fresh.AddMessage(content+"```", "assistant")
	if !reflect.DeepEqual(view.buffer.GetVisibleLines(), fresh.buffer.GetVisibleLines()) {
		t.Fatal("reused parser changed rendered source, styles or streaming replacement")
	}
	if len(view.highlighters) != 1 || view.highlighters[0].highlighter != parser {
		t.Fatal("reloaded code rebuilt its syntax parser")
	}
}
