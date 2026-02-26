package widgets

import (
	"strings"

	"github.com/odvcencio/fluffyui/state"
)

func (c *ChatView) ownsReasoning() bool {
	if c == nil || c.reasoningTextSig == nil || c.reasoningPreviewSig == nil || c.reasoningVisibleSig == nil {
		return false
	}
	if c.ownedReasoningTextSig == nil || c.ownedReasoningPreviewSig == nil || c.ownedReasoningVisibleSig == nil {
		return false
	}
	textSig, ok := c.reasoningTextSig.(*state.Signal[string])
	if !ok || textSig != c.ownedReasoningTextSig {
		return false
	}
	previewSig, ok := c.reasoningPreviewSig.(*state.Signal[string])
	if !ok || previewSig != c.ownedReasoningPreviewSig {
		return false
	}
	visibleSig, ok := c.reasoningVisibleSig.(*state.Signal[bool])
	return ok && visibleSig == c.ownedReasoningVisibleSig
}

// AppendReasoning appends streaming reasoning text (dimmed).
func (c *ChatView) AppendReasoning(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	if c.ownsReasoning() {
		if c.ownedReasoningTextSig != nil {
			current := c.ownedReasoningTextSig.Get()
			c.ownedReasoningTextSig.Set(current + text)
		}
		if c.ownedReasoningVisibleSig != nil {
			c.ownedReasoningVisibleSig.Set(true)
		}
	}
}

// CollapseReasoning collapses reasoning block to preview.
func (c *ChatView) CollapseReasoning(preview, full string) {
	if c.ownsReasoning() {
		if c.ownedReasoningPreviewSig != nil {
			c.ownedReasoningPreviewSig.Set(strings.TrimSpace(preview))
		}
		if c.ownedReasoningTextSig != nil {
			c.ownedReasoningTextSig.Set(full)
		}
		if c.ownedReasoningVisibleSig != nil {
			c.ownedReasoningVisibleSig.Set(true)
		}
	}
}

// ClearReasoningBlock clears reasoning state for new message.
func (c *ChatView) ClearReasoningBlock() {
	if c.ownsReasoning() {
		if c.ownedReasoningTextSig != nil {
			c.ownedReasoningTextSig.Set("")
		}
		if c.ownedReasoningPreviewSig != nil {
			c.ownedReasoningPreviewSig.Set("")
		}
		if c.ownedReasoningVisibleSig != nil {
			c.ownedReasoningVisibleSig.Set(false)
		}
	}
}

// ToggleReasoning toggles reasoning block between collapsed and expanded.
// Returns true if a toggle occurred.
func (c *ChatView) ToggleReasoning() bool {
	if c.reasoning == nil {
		return false
	}
	return c.reasoning.ToggleExpanded()
}
