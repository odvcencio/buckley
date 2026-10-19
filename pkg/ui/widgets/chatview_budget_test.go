package widgets

import (
	"strings"
	"testing"
	"unicode/utf8"

	"m31labs.dev/fluffyui/backend"
	"m31labs.dev/fluffyui/markdown"
	"m31labs.dev/fluffyui/runtime"
	"m31labs.dev/fluffyui/theme"
)

func TestChatView_MemoryBudgetBoundsStreamingCache(t *testing.T) {
	c := NewChatView()
	const limit = 64 << 10
	c.SetMemoryBudget(limit)
	c.SetMarkdownRenderer(markdown.NewRenderer(theme.DefaultTheme()), backend.DefaultStyle())
	c.Layout(runtime.Rect{Width: 80, Height: 20})
	c.AddMessage(strings.Repeat("界\n", 5000), "assistant")
	for range 100 {
		c.AppendText(strings.Repeat("界", 500))
	}
	if len(c.lastContent) > limit/16 || !utf8.ValidString(c.lastContent) {
		t.Fatalf("unbounded streaming: cached=%d utf8=%v", len(c.lastContent), utf8.ValidString(c.lastContent))
	}
	c.ReplaceText("final replacement")
	if c.lastContent != "final replacement" {
		t.Fatalf("replace cached=%q", c.lastContent)
	}
}
