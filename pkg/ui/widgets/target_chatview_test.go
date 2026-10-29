package widgets

import (
	"m31labs.dev/fluffyui/backend"
	"m31labs.dev/fluffyui/markdown"
	"m31labs.dev/fluffyui/theme"
	"reflect"
	"strings"
	"testing"
)

func TestChatView_TargetedSnapshots(t *testing.T) {
	c := NewChatView()
	c.AddTargetMessage(11, "first", "assistant")
	c.AddMessage("later", "user")
	if !c.ReplaceTargetText(11, "new\nlines") {
		t.Fatal("target missing")
	}
	c.Search("later")
	if _, n := c.SearchMatches(); n != 1 {
		t.Fatalf("later match count %d", n)
	}
	if c.ReplaceTargetText(12, "wrong") {
		t.Fatal("unknown target updated")
	}
	if !c.ReplaceTargetText(11, "") {
		t.Fatal("empty replacement failed")
	}
	c.EndTarget(11)
	if c.ReplaceTargetText(11, "wrong") {
		t.Fatal("ended target updated")
	}
	c.ClearContent()
	c.AddTargetMessage(11, "again", "assistant")
	if !c.ReplaceTargetText(11, "updated") {
		t.Fatal("reused target failed")
	}
	if c.lastContent != "" {
		t.Fatal("widget retained duplicate target source")
	}
}

func TestChatView_TargetMarkdownReflowAfterPartialTrim(t *testing.T) {
	setup := func() *ChatView {
		c := NewChatView()
		c.SetMarkdownRenderer(markdown.NewRenderer(theme.DefaultTheme()), backend.DefaultStyle())
		c.buffer.SetMaxLines(5)
		return c
	}
	content := "alpha\n\nbeta\n\ngamma"
	final := content + "\n\n```go\nfmt.Println(1)\n```"
	live := setup()
	live.AddTargetMessage(7, content, "assistant")
	live.AddMessage("later", "user")
	if !live.ReplaceTargetText(7, final) {
		t.Fatal("partially retained target missing")
	}
	fresh := setup()
	fresh.AddTargetMessage(7, final, "assistant")
	fresh.AddMessage("later", "user")
	extract := func(c *ChatView) []string {
		var out []string
		for _, l := range c.buffer.GetVisibleLines() {
			out = append(out, l.Content)
		}
		return out
	}
	if a, b := extract(live), extract(fresh); !reflect.DeepEqual(a, b) {
		t.Fatalf("reflow got %q want %q", a, b)
	}
	if live.buffer.LineCount() > 5 {
		t.Fatal("limits exceeded")
	}
}

func TestChatView_LegacyAppendDoesNotMutateActiveTarget(t *testing.T) {
	c := NewChatView()
	c.AddTargetMessage(5, "protected", "assistant")
	c.AppendText(" wrong")
	c.Search("wrong")
	if _, n := c.SearchMatches(); n != 0 {
		t.Fatal("legacy append changed active target")
	}
	c.AddMessage("tool", "tool")
	c.AppendText(" output")
	c.Search("tool output")
	if _, n := c.SearchMatches(); n != 1 {
		t.Fatal("legacy tool append did not update")
	}
}

func TestChatView_ThinkingCleanupPreservesReasoning(t *testing.T) {
	c := NewChatView()
	c.AddMessage("", "thinking")
	c.RemoveThinkingIndicator()
	if c.buffer.LineCount() != 0 {
		t.Fatal("indicator remains")
	}
	c.AddTargetMessage(10, "actual reasoning", "thinking")
	c.RemoveThinkingIndicator()
	c.Search("actual reasoning")
	if _, n := c.SearchMatches(); n != 1 {
		t.Fatal("active reasoning removed")
	}
	c.EndTarget(10)
	c.RemoveThinkingIndicator()
	c.Search("actual reasoning")
	if _, n := c.SearchMatches(); n != 1 {
		t.Fatal("retired reasoning removed")
	}
	c.ClearContent()
	c.AddMessage("replayed reasoning", "thinking")
	c.RemoveThinkingIndicator()
	c.Search("replayed reasoning")
	if _, n := c.SearchMatches(); n != 1 {
		t.Fatal("replayed reasoning removed")
	}
}

func TestChatView_ReasoningBeforeAnswerKeepsAnswerVisible(t *testing.T) {
	c := NewChatView()
	c.buffer.Resize(80, 3)
	c.AddTargetMessage(20, "final answer", "assistant")
	if !c.AddTargetMessageBefore(21, 20, strings.Repeat("long reasoning\n", 70), "thinking") {
		t.Fatal("anchor missing")
	}
	if !c.ReplaceTargetText(20, "complete answer") {
		t.Fatal("answer target missing")
	}
	visible := c.buffer.GetVisibleLines()
	if len(visible) == 0 || visible[len(visible)-1].Content != "complete answer" {
		t.Fatalf("answer not visible at bottom: %+v", visible)
	}
	if c.AddTargetMessageBefore(22, 99, "wrong", "thinking") {
		t.Fatal("missing anchor accepted")
	}
}

func TestChatView_ThinkingCleanupFindsIndicatorBeforeMessages(t *testing.T) {
	c := NewChatView()
	c.AddMessage("", "thinking")
	c.AddMessage("", "thinking")
	c.buffer.Resize(8, 20)
	c.AddTargetMessage(5, "actual reasoning", "thinking")
	c.EndTarget(5)
	c.AddTargetMessage(6, "final answer", "assistant")
	c.RemoveThinkingIndicator()
	c.Search("thinking...")
	if _, n := c.SearchMatches(); n != 0 {
		t.Fatal("indicator survived behind messages")
	}
	c.Search("actual reasoning")
	if _, n := c.SearchMatches(); n != 1 {
		t.Fatal("reasoning removed")
	}
	c.Search("final answer")
	if _, n := c.SearchMatches(); n != 1 {
		t.Fatal("answer removed")
	}
	if !c.ReplaceTargetText(6, "updated answer") {
		t.Fatal("answer target lost")
	}
}
